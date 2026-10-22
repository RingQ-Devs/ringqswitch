package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Internal chat between the extensions of a domain: one-to-one ("direct")
// and group rooms, history, unread counts, file attachments and typing
// notices. Messages travel over the WebSocket; history is in rq_chat_*.

const maxChatFile = 20 << 20

type chatMessage struct {
	UUID       string `json:"uuid"`
	Room       string `json:"room"`
	Sender     string `json:"sender"`
	SenderName string `json:"sender_name"`
	Body       string `json:"body"`
	FileName   string `json:"file_name,omitempty"`
	FileSize   int64  `json:"file_size,omitempty"`
	FileType   string `json:"file_type,omitempty"`
	Time       string `json:"time"`
}

// roomMembers returns the members of a room in the principal's domain,
// or an error when the principal is not one of them.
func (s *Server) roomMembers(ctx context.Context, p *principal, room string) ([]string, error) {
	if !reUUID.MatchString(room) {
		return nil, notFound("chat")
	}
	rows, err := s.store.Pool().Query(ctx, `select m.member from rq_chat_members m join rq_chat_rooms r on r.room_uuid = m.room_uuid
		where m.room_uuid = $1::uuid and r.domain_uuid = $2::uuid`, room, p.DomainUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []string
	in := false
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		members = append(members, m)
		in = in || m == p.Extension
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !in {
		return nil, notFound("chat")
	}
	return members, nil
}

func memberKeys(members []string, domain string) []string {
	keys := make([]string, len(members))
	for i, m := range members {
		keys[i] = m + "@" + domain
	}
	return keys
}

func (s *Server) postChat(ctx context.Context, p *principal, room, body string, file *chatMessage) (*chatMessage, error) {
	members, err := s.roomMembers(ctx, p, room)
	if err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" && file == nil {
		return nil, badRequest("empty message")
	}
	if utf8.RuneCountInString(body) > 4000 {
		return nil, badRequest("message too long (4000 characters at most)")
	}
	msg := &chatMessage{UUID: newUUID(), Room: room, Sender: p.Extension, SenderName: p.Name, Body: body, Time: time.Now().UTC().Format(time.RFC3339Nano)}
	if file != nil {
		msg.UUID, msg.FileName, msg.FileSize, msg.FileType = file.UUID, file.FileName, file.FileSize, file.FileType
	}
	if _, err := s.store.Pool().Exec(ctx, `insert into rq_chat_messages (message_uuid, room_uuid, domain_uuid, sender, sender_name, body,
		file_name, file_size, file_type, insert_date) values ($1, $2, $3, $4, $5, $6, nullif($7, ''), nullif($8, 0), nullif($9, ''), now())`,
		msg.UUID, room, p.DomainUUID, msg.Sender, msg.SenderName, msg.Body, msg.FileName, msg.FileSize, msg.FileType); err != nil {
		return nil, err
	}
	_, _ = s.store.Pool().Exec(ctx, `update rq_chat_members set last_read = now() where room_uuid = $1::uuid and member = $2`, room, p.Extension)
	s.hub.sendTo(memberKeys(members, p.Domain), map[string]any{"t": "chat.msg", "message": msg})
	return msg, nil
}

func (s *Server) chatWS(c *wsClient, m wsMsg) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch m.T {
	case "chat.send":
		msg, err := s.postChat(ctx, c.p, m.Room, m.Body, nil)
		if err != nil {
			c.fail(m, err)
			return
		}
		c.reply(m, map[string]any{"t": "chat.sent", "message": msg})
	case "chat.typing":
		members, err := s.roomMembers(ctx, c.p, m.Room)
		if err != nil {
			return
		}
		var others []string
		for _, x := range members {
			if x != c.p.Extension {
				others = append(others, x)
			}
		}
		s.hub.sendTo(memberKeys(others, c.p.Domain), map[string]any{"t": "chat.typing", "room": m.Room, "from": c.p.Extension, "name": c.p.Name})
	case "chat.read":
		if _, err := s.roomMembers(ctx, c.p, m.Room); err == nil {
			_, _ = s.store.Pool().Exec(ctx, `update rq_chat_members set last_read = now() where room_uuid = $1::uuid and member = $2`, m.Room, c.p.Extension)
		}
	}
}

func (s *Server) chatAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, parts []string) error {
	what, id, sub := part(parts, 0), part(parts, 1), part(parts, 2)
	switch {
	case what == "rooms" && id == "" && r.Method == http.MethodGet:
		rooms, err := s.chatRooms(ctx, p)
		if err != nil {
			return err
		}
		writeJSON(w, rooms)
	case what == "rooms" && id == "" && r.Method == http.MethodPost:
		var body struct {
			Name    string   `json:"name"`
			Members []string `json:"members"`
		}
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" || utf8.RuneCountInString(body.Name) > 80 {
			return badRequest("group name: 1 to 80 characters")
		}
		members, err := s.validMembers(ctx, p, append(body.Members, p.Extension))
		if err != nil {
			return err
		}
		room := newUUID()
		if err := s.createRoom(ctx, p, room, "group", body.Name, "", members); err != nil {
			return err
		}
		s.hub.sendTo(memberKeys(members, p.Domain), map[string]any{"t": "chat.rooms"})
		writeJSON(w, map[string]string{"room": room})
	case what == "direct" && r.Method == http.MethodPost:
		var body struct{ Extension string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		room, err := s.directRoom(ctx, p, body.Extension)
		if err != nil {
			return err
		}
		writeJSON(w, map[string]string{"room": room})
	case what == "rooms" && id != "" && sub == "messages" && r.Method == http.MethodGet:
		if _, err := s.roomMembers(ctx, p, id); err != nil {
			return err
		}
		before := r.URL.Query().Get("before")
		if before == "" {
			before = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
		}
		if _, err := time.Parse(time.RFC3339Nano, before); err != nil {
			return badRequest("before: a time")
		}
		limit := min(max(atoi(r.URL.Query().Get("limit"), 50), 1), 200)
		rows, err := s.store.Pool().Query(ctx, `select message_uuid::text, sender, coalesce(sender_name, ''), coalesce(body, ''),
			coalesce(file_name, ''), coalesce(file_size, 0), coalesce(file_type, ''), insert_date
			from rq_chat_messages where room_uuid = $1::uuid and insert_date < $2::timestamptz order by insert_date desc limit $3`, id, before, limit)
		if err != nil {
			return err
		}
		out := []chatMessage{}
		for rows.Next() {
			var m chatMessage
			var t time.Time
			if err := rows.Scan(&m.UUID, &m.Sender, &m.SenderName, &m.Body, &m.FileName, &m.FileSize, &m.FileType, &t); err != nil {
				rows.Close()
				return err
			}
			m.Room, m.Time = id, t.UTC().Format(time.RFC3339Nano)
			out = append(out, m)
		}
		rows.Close()
		sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
		writeJSON(w, out)
	case what == "rooms" && id != "" && sub == "messages" && r.Method == http.MethodPost:
		var body struct{ Body string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		msg, err := s.postChat(ctx, p, id, body.Body, nil)
		if err != nil {
			return err
		}
		writeJSON(w, msg)
	case what == "rooms" && id != "" && sub == "files" && r.Method == http.MethodPost:
		if _, err := s.roomMembers(ctx, p, id); err != nil {
			return err
		}
		name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("name")))
		if name == "" || name == "." || name == "/" || utf8.RuneCountInString(name) > 120 {
			return badRequest("file name missing")
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxChatFile))
		if err != nil {
			return badRequest("file too large (20 MB at most)")
		}
		if len(data) == 0 {
			return badRequest("empty file")
		}
		msgID := newUUID()
		dir := filepath.Join(s.cfg.DataDir, "chat", p.DomainUUID)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, msgID), data, 0o640); err != nil {
			return err
		}
		ftype := http.DetectContentType(data)
		msg, err := s.postChat(ctx, p, id, r.URL.Query().Get("caption"), &chatMessage{UUID: msgID, FileName: name, FileSize: int64(len(data)), FileType: ftype})
		if err != nil {
			os.Remove(filepath.Join(dir, msgID))
			return err
		}
		writeJSON(w, msg)
	case what == "files" && id != "" && r.Method == http.MethodGet:
		if !reUUID.MatchString(id) {
			return notFound("file")
		}
		var room, name, ftype string
		if err := s.store.Pool().QueryRow(ctx, `select room_uuid::text, coalesce(file_name, ''), coalesce(file_type, '') from rq_chat_messages
			where message_uuid = $1::uuid and domain_uuid = $2::uuid`, id, p.DomainUUID).Scan(&room, &name, &ftype); err != nil || name == "" {
			return notFound("file")
		}
		if _, err := s.roomMembers(ctx, p, room); err != nil {
			return err
		}
		f, err := os.Open(filepath.Join(s.cfg.DataDir, "chat", p.DomainUUID, id))
		if err != nil {
			return notFound("file")
		}
		defer f.Close()
		st, _ := f.Stat()
		h := w.Header()
		// Uploaded files never run as part of this site.
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		switch ftype {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
			h.Set("Content-Type", ftype)
			if r.URL.Query().Get("download") != "" {
				h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
			}
		default:
			h.Set("Content-Type", "application/octet-stream")
			h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		}
		http.ServeContent(w, r, "", st.ModTime(), f)
	case what == "rooms" && id != "" && sub == "read" && r.Method == http.MethodPost:
		if _, err := s.roomMembers(ctx, p, id); err != nil {
			return err
		}
		_, _ = s.store.Pool().Exec(ctx, `update rq_chat_members set last_read = now() where room_uuid = $1::uuid and member = $2`, id, p.Extension)
		writeJSON(w, map[string]string{"ok": "read"})
	case what == "rooms" && id != "" && sub == "" && r.Method == http.MethodPut:
		// Rename a group or change its members.
		members, err := s.roomMembers(ctx, p, id)
		if err != nil {
			return err
		}
		var kind string
		if err := s.store.Pool().QueryRow(ctx, `select room_type from rq_chat_rooms where room_uuid = $1::uuid`, id).Scan(&kind); err != nil {
			return err
		}
		if kind != "group" {
			return badRequest("only groups can be changed")
		}
		var body struct {
			Name    *string  `json:"name"`
			Members []string `json:"members"`
		}
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		tx, err := s.store.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if body.Name != nil {
			n := strings.TrimSpace(*body.Name)
			if n == "" || utf8.RuneCountInString(n) > 80 {
				return badRequest("group name: 1 to 80 characters")
			}
			if _, err := tx.Exec(ctx, `update rq_chat_rooms set room_name = $2 where room_uuid = $1::uuid`, id, n); err != nil {
				return err
			}
		}
		var newMembers []string
		if body.Members != nil {
			newMembers, err = s.validMembers(ctx, p, append(body.Members, p.Extension))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `delete from rq_chat_members where room_uuid = $1::uuid and not (member = any($2))`, id, newMembers); err != nil {
				return err
			}
			for _, m := range newMembers {
				if _, err := tx.Exec(ctx, `insert into rq_chat_members (room_uuid, member) values ($1, $2) on conflict do nothing`, id, m); err != nil {
					return err
				}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		s.hub.sendTo(memberKeys(append(members, newMembers...), p.Domain), map[string]any{"t": "chat.rooms"})
		writeJSON(w, map[string]string{"ok": "saved"})
	case what == "rooms" && id != "" && sub == "" && r.Method == http.MethodDelete:
		// Leave a group (the last member leaving deletes it).
		if _, err := s.roomMembers(ctx, p, id); err != nil {
			return err
		}
		if _, err := s.store.Pool().Exec(ctx, `delete from rq_chat_members where room_uuid = $1::uuid and member = $2`, id, p.Extension); err != nil {
			return err
		}
		var left int
		_ = s.store.Pool().QueryRow(ctx, `select count(*) from rq_chat_members where room_uuid = $1::uuid`, id).Scan(&left)
		if left == 0 {
			_, _ = s.store.Pool().Exec(ctx, `delete from rq_chat_messages where room_uuid = $1::uuid`, id)
			_, _ = s.store.Pool().Exec(ctx, `delete from rq_chat_rooms where room_uuid = $1::uuid`, id)
		}
		writeJSON(w, map[string]string{"ok": "left"})
	default:
		return notFound("page")
	}
	return nil
}

func (s *Server) validMembers(ctx context.Context, p *principal, list []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, m := range list {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		if !reNumber.MatchString(m) {
			return nil, badRequest("member %q: an extension number", m)
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) > 200 {
		return nil, badRequest("200 members at most")
	}
	rows, err := s.store.Pool().Query(ctx, `select extension from v_extensions where domain_uuid = $1::uuid and extension = any($2)`, p.DomainUUID, out)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var e string
		if rows.Scan(&e) == nil {
			found[e] = true
		}
	}
	for _, m := range out {
		if !found[m] {
			return nil, badRequest("extension %s does not exist", m)
		}
	}
	return out, nil
}

func (s *Server) createRoom(ctx context.Context, p *principal, room, kind, name, directKey string, members []string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `insert into rq_chat_rooms (room_uuid, domain_uuid, room_type, room_name, direct_key, created_by, insert_date)
		values ($1, $2, $3, $4, nullif($5, ''), $6, now())`, room, p.DomainUUID, kind, name, directKey, p.Extension); err != nil {
		return err
	}
	for _, m := range members {
		if _, err := tx.Exec(ctx, `insert into rq_chat_members (room_uuid, member) values ($1, $2) on conflict do nothing`, room, m); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) directRoom(ctx context.Context, p *principal, other string) (string, error) {
	members, err := s.validMembers(ctx, p, []string{p.Extension, other})
	if err != nil {
		return "", err
	}
	sort.Strings(members)
	key := strings.Join(members, "|")
	var room string
	err = s.store.Pool().QueryRow(ctx, `select room_uuid::text from rq_chat_rooms where domain_uuid = $1::uuid and direct_key = $2 limit 1`,
		p.DomainUUID, key).Scan(&room)
	if err == nil {
		// Make sure both are still members (one may have left).
		for _, m := range members {
			_, _ = s.store.Pool().Exec(ctx, `insert into rq_chat_members (room_uuid, member) values ($1, $2) on conflict do nothing`, room, m)
		}
		return room, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	room = newUUID()
	return room, s.createRoom(ctx, p, room, "direct", "", key, members)
}

type chatRoom struct {
	UUID     string       `json:"uuid"`
	Type     string       `json:"type"`
	Name     string       `json:"name"`
	Members  []string     `json:"members"`
	Unread   int          `json:"unread"`
	Last     *chatMessage `json:"last,omitempty"`
	LastTime string       `json:"last_time"`
}

func (s *Server) chatRooms(ctx context.Context, p *principal) ([]chatRoom, error) {
	rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('uuid', r.room_uuid, 'type', r.room_type, 'name', coalesce(r.room_name, ''),
		'members', (select coalesce(string_agg(x.member, ',' order by x.member), '') from rq_chat_members x where x.room_uuid = r.room_uuid),
		'unread', (select count(*) from rq_chat_messages g where g.room_uuid = r.room_uuid and g.sender <> $2
			and g.insert_date > coalesce(m.last_read, 'epoch'::timestamptz)),
		'last', (select jsonb_build_object('uuid', g.message_uuid, 'sender', g.sender, 'sender_name', coalesce(g.sender_name, ''),
			'body', coalesce(g.body, ''), 'file_name', coalesce(g.file_name, ''), 'time', g.insert_date)
			from rq_chat_messages g where g.room_uuid = r.room_uuid order by g.insert_date desc limit 1),
		'created', r.insert_date)
		from rq_chat_rooms r join rq_chat_members m on m.room_uuid = r.room_uuid and m.member = $2
		where r.domain_uuid = $1::uuid`, p.DomainUUID, p.Extension)
	if err != nil {
		return nil, err
	}
	names := s.extensionNames(ctx, p.DomainUUID)
	out := []chatRoom{}
	for _, r := range rows {
		room := chatRoom{UUID: r["uuid"], Type: r["type"], Name: r["name"], Unread: atoi(r["unread"], 0), LastTime: r["created"]}
		if r["members"] != "" {
			room.Members = strings.Split(r["members"], ",")
		}
		if r["last"] != "" {
			var last chatMessage
			if json.Unmarshal([]byte(r["last"]), &last) == nil {
				room.Last, room.LastTime = &last, last.Time
			}
		}
		if room.Type == "direct" {
			for _, m := range room.Members {
				if m != p.Extension || len(room.Members) == 1 {
					room.Name = strings.TrimSpace(m + " " + names[m])
				}
			}
		}
		out = append(out, room)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTime > out[j].LastTime })
	return out, nil
}

func (s *Server) extensionNames(ctx context.Context, domainUUID string) map[string]string {
	names := map[string]string{}
	rows, err := s.store.Pool().Query(ctx, `select extension, coalesce(nullif(trim(coalesce(directory_first_name, '') || ' ' ||
		coalesce(directory_last_name, '')), ''), nullif(effective_caller_id_name, extension), '') from v_extensions where domain_uuid = $1::uuid`, domainUUID)
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var e, n string
		if rows.Scan(&e, &n) == nil {
			names[e] = n
		}
	}
	return names
}

// directory lists the domain's extensions with their presence, for the
// chat contacts and the dialer.
func (s *Server) directory(ctx context.Context, w http.ResponseWriter, dom domainRow) error {
	names := s.extensionNames(ctx, dom.uuid)
	phones := map[string]int{}
	for _, b := range s.sw.Registrar().All() {
		if strings.EqualFold(b.Domain, dom.name) {
			phones[b.User]++
		}
	}
	onCall := map[string]bool{}
	for _, ch := range s.sw.Channels() {
		info := ch.Snapshot()
		if !strings.EqualFold(firstNonEmpty(info.Domain, info.Context), dom.name) {
			continue
		}
		onCall[info.CIDNum], onCall[info.Destination], onCall[info.CalleeNum] = true, true, true
	}
	agents := map[string][3]string{}
	for _, a := range s.sw.Agents(dom.name) {
		agents[a.Extension] = [3]string{a.Status, a.Aux, a.UUID}
	}
	dnd := map[string]bool{}
	for e := range names {
		dnd[e] = truthy(s.sw.UserVar(e, dom.name, "do_not_disturb")) // as now (a phone's *78 counts at once)
	}
	exts := make([]string, 0, len(names))
	for e := range names {
		exts = append(exts, e)
	}
	sort.Slice(exts, func(i, j int) bool {
		if len(exts[i]) != len(exts[j]) {
			return len(exts[i]) < len(exts[j])
		}
		return exts[i] < exts[j]
	})
	out := []map[string]any{}
	for _, e := range exts {
		a := agents[e]
		out = append(out, map[string]any{"extension": e, "name": names[e], "web": s.hub.online(e + "@" + dom.name),
			"phone": phones[e] > 0, "on_call": onCall[e], "agent_status": a[0], "aux": a[1], "agent": a[2], "dnd": dnd[e]})
	}
	writeJSON(w, out)
	return nil
}
