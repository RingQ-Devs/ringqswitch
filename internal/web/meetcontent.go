package web

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Files presented in a meeting (Present file) live in memory while the
// meeting runs. Guests have no session cookie, so uploads and downloads
// carry the participant's meeting token instead.

var presentTypes = map[string]string{
	".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp",
}

func (s *Server) meetPeerByToken(token string) *meetPeer {
	if token == "" {
		return nil
	}
	s.meets.mu.Lock()
	defer s.meets.mu.Unlock()
	p := s.meets.tokens[token]
	if p == nil {
		return nil
	}
	p.mu.Lock()
	left := p.left
	p.mu.Unlock()
	if left {
		return nil
	}
	return p
}

// meetFileAPI: POST /api/guest/file?token=&name= (body: the file) and
// GET /api/guest/file/{id}?token=.
func (s *Server) meetFileAPI(w http.ResponseWriter, r *http.Request, id string) error {
	peer := s.meetPeerByToken(r.URL.Query().Get("token"))
	if peer == nil {
		return errForbidden
	}
	room := peer.room
	room.mu.Lock()
	_, inside := room.peers[peer.id]
	room.mu.Unlock()
	if !inside {
		return errForbidden
	}
	switch {
	case id == "" && r.Method == http.MethodPost:
		if r.Header.Get("X-RQ") != "1" {
			return errForbidden
		}
		name := strings.TrimSpace(filepath.Base(r.URL.Query().Get("name")))
		ctype := presentTypes[strings.ToLower(filepath.Ext(name))]
		if ctype == "" || utf8.RuneCountInString(name) > 200 {
			return badRequest("present a PDF or a picture (PNG, JPEG, GIF, WebP)")
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMeetFile))
		if err != nil {
			return badRequest("the file is too large (%d MB at most)", maxMeetFile>>20)
		}
		sniffed := http.DetectContentType(data)
		if !strings.HasPrefix(sniffed, ctype) && !(ctype == "image/webp" && strings.HasPrefix(sniffed, "image/webp")) {
			return badRequest("the file is not what its name says (%s)", sniffed)
		}
		fid := strings.ReplaceAll(newUUID(), "-", "")
		room.mu.Lock()
		if room.fileBytes+len(data) > maxMeetFiles {
			room.mu.Unlock()
			return badRequest("this meeting has too many files")
		}
		room.files[fid] = &meetFile{name: name, ctype: ctype, data: data}
		room.fileBytes += len(data)
		room.mu.Unlock()
		writeJSON(w, map[string]any{"id": fid, "name": name, "type": ctype})
		return nil
	case id != "" && r.Method == http.MethodGet:
		room.mu.Lock()
		f := room.files[id]
		room.mu.Unlock()
		if f == nil {
			return notFound("file")
		}
		h := w.Header()
		h.Set("Content-Type", f.ctype)
		h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": f.name}))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "private, max-age=3600")
		// Shown in a frame of the meeting page (the browser's PDF viewer).
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy", "frame-ancestors 'self'")
		_, _ = w.Write(f.data)
		return nil
	}
	return errMethod
}

// saveHistory keeps what a finished meeting leaves behind: who came, the
// chat, the transcript and the names of the presented files.
func (r *meetRoom) saveHistory() {
	s := r.s
	if s.store == nil || !s.store.HasTable("rq_meeting_history") {
		return
	}
	r.mu.Lock()
	attended := append([]meetAttendee(nil), r.attended...)
	chat := append([]meetChat{}, r.chat...) // [] rather than null when empty
	transcript := append([]meetChat{}, r.transcript...)
	var files []string
	for _, f := range r.files {
		files = append(files, f.name)
	}
	r.mu.Unlock()
	if len(attended) == 0 {
		return
	}
	var members []string
	for _, a := range attended {
		if a.Key != "" {
			members = append(members, a.Key)
		}
	}
	js := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	if files == nil {
		files = []string{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.store.Pool().Exec(ctx, `insert into rq_meeting_history (history_uuid, domain_uuid, meeting_code, title, host, started, ended,
		participants, members, chat, transcript, files) values ($1, $2::uuid, $3, $4, $5, $6, now(), $7::jsonb, $8, $9::jsonb, $10::jsonb, $11::jsonb)`,
		newUUID(), r.domainUUID, r.code, r.title, r.host, r.started, js(attended), members, js(chat), js(transcript), js(files)); err != nil {
		s.log.Warn("meeting: could not save its history", "code", r.code, "err", err)
	}
}

// meetingHistory: GET /api/meetings/history (the latest 50 meetings you
// took part in; owners and managers see the whole domain's) and
// GET /api/meetings/history/{uuid} (with the chat and transcript).
func (s *Server) meetingHistory(ctx context.Context, w http.ResponseWriter, p *principal, id string) error {
	if !s.store.HasTable("rq_meeting_history") {
		writeJSON(w, []any{})
		return nil
	}
	cols := `history_uuid, meeting_code, coalesce(title, ''), started, ended, participants, coalesce(array_length(members, 1), 0)`
	where := `domain_uuid = $1::uuid and ($2 or $3 = any(members))`
	args := []any{p.DomainUUID, p.supervisor(), p.key()}
	if id != "" {
		if !reUUID.MatchString(id) {
			return notFound("meeting")
		}
		var row struct {
			UUID, Code, Title       string
			Started, Ended          time.Time
			Participants            json.RawMessage
			Members                 int
			Chat, Transcript, Files json.RawMessage
		}
		arr := func(col string) string {
			return `case when jsonb_typeof(` + col + `) = 'array' then ` + col + ` else '[]'::jsonb end`
		}
		err := s.store.Pool().QueryRow(ctx, `select `+cols+`, `+arr("chat")+`, `+arr("transcript")+`, `+arr("files")+` from rq_meeting_history where `+where+` and history_uuid = $4::uuid`,
			append(args, id)...).Scan(&row.UUID, &row.Code, &row.Title, &row.Started, &row.Ended, &row.Participants, &row.Members, &row.Chat, &row.Transcript, &row.Files)
		if err != nil {
			return notFound("meeting")
		}
		writeJSON(w, map[string]any{"uuid": row.UUID, "code": row.Code, "title": row.Title, "started": row.Started.UnixMilli(),
			"ended": row.Ended.UnixMilli(), "participants": row.Participants, "chat": row.Chat, "transcript": row.Transcript, "files": row.Files})
		return nil
	}
	count := func(col string) string {
		return `case when jsonb_typeof(` + col + `) = 'array' then jsonb_array_length(` + col + `) else 0 end`
	}
	rows, err := s.store.Pool().Query(ctx, `select `+cols+`, `+count("chat")+`, `+count("transcript")+`
		from rq_meeting_history where `+where+` order by ended desc limit 50`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var uuid, code, title string
		var started, ended time.Time
		var participants json.RawMessage
		var members, chat, transcript int
		if err := rows.Scan(&uuid, &code, &title, &started, &ended, &participants, &members, &chat, &transcript); err != nil {
			return err
		}
		out = append(out, map[string]any{"uuid": uuid, "code": code, "title": title, "started": started.UnixMilli(), "ended": ended.UnixMilli(),
			"participants": participants, "chat_count": chat, "transcript_count": transcript})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, out)
	return nil
}
