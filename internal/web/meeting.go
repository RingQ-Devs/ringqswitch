package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// Web meetings: video rooms in the browser. The server is an SFU (it
// forwards each participant's audio, camera and screen to the others
// without decoding), so every participant sends one stream whatever the
// size of the meeting. Meetings are planned in rq_meetings; anyone with the
// link (and the passcode, if set) can join, optionally through a waiting
// room the host controls. Signalling uses the WebSocket ("meet.*").

const maxMeetingPeers = 30

type meetChat struct {
	From string `json:"from"`
	Name string `json:"name"`
	Text string `json:"text"`
	Time int64  `json:"time"`
}

type meetRoom struct {
	s          *Server
	code       string
	title      string
	domain     string
	domainUUID string
	host       string // principal key of the meeting's host
	started    time.Time

	mu           sync.Mutex
	lobby        bool // guests wait until a host lets them in
	locked       bool
	peers        map[string]*meetPeer // in the meeting
	waiting      map[string]*meetPeer // in the waiting room
	tracks       map[string]*meetTrack
	chat         []meetChat
	transcribing bool
	transcript   []meetChat
	present      *meetPresent
	files        map[string]*meetFile
	fileBytes    int
	board        bool
	strokes      []json.RawMessage
	attended     []meetAttendee
	seen         map[string]bool
}

// meetPresent is the file shown to everyone (Present file).
type meetPresent struct {
	Peer  string `json:"peer"`
	Name  string `json:"name"` // who presents
	File  string `json:"file"`
	Title string `json:"title"` // the file name
	Type  string `json:"type"`
	Page  int    `json:"page"`
}

type meetFile struct {
	name, ctype string
	data        []byte
}

type meetAttendee struct {
	Name   string `json:"name"`
	Key    string `json:"key,omitempty"`
	Guest  bool   `json:"guest,omitempty"`
	Email  string `json:"email,omitempty"`
	Joined int64  `json:"joined"`
}

const (
	maxMeetFile      = 25 << 20
	maxMeetFiles     = 200 << 20
	maxBoardStrokes  = 5000
	maxTranscriptLen = 5000
	maxMeetChat      = 1000
)

type meetTrack struct {
	local *webrtc.TrackLocalStaticRTP
	owner string // peer id
	kind  string // audio, camera, screen
	ssrc  uint32
}

type meetPeer struct {
	id    string
	room  *meetRoom
	c     *wsClient
	name  string
	key   string
	email string
	host  bool
	guest bool
	token string // authorises this participant's file uploads and downloads

	mu          sync.Mutex
	pc          *webrtc.PeerConnection
	audio       bool
	video       bool
	screen      bool
	hand        bool
	negotiating bool
	again       bool
	left        bool
}

type meetings struct {
	mu     sync.Mutex
	rooms  map[string]*meetRoom
	tokens map[string]*meetPeer
}

var codeChars = "abcdefghijkmnpqrstuvwxyz23456789"

// newMeetingCode makes a code like 46ak-c6kg-hvcz.
func newMeetingCode() string {
	b := make([]byte, 12)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(codeChars))))
		b[i] = codeChars[n.Int64()]
	}
	return string(b[:4]) + "-" + string(b[4:8]) + "-" + string(b[8:])
}

// meetingLink is the address people open to join.
func meetingLink(code string) string { return "/meet/m/" + code }

type meetingRow struct {
	UUID, Domain, DomainUUID, Code, Title, Passcode, Host, HostName string
	Lobby, Guests                                                   bool
}

func (s *Server) meetingByCode(ctx context.Context, code string) (*meetingRow, error) {
	var m meetingRow
	err := s.store.Pool().QueryRow(ctx, `select m.meeting_uuid::text, d.domain_name, d.domain_uuid::text, m.meeting_code, coalesce(m.title, ''),
		coalesce(m.passcode, ''), coalesce(m.host, ''), coalesce(m.host_name, ''), m.lobby, m.allow_guests
		from rq_meetings m join v_domains d on d.domain_uuid = m.domain_uuid where m.meeting_code = $1`, strings.ToLower(strings.TrimSpace(code))).
		Scan(&m.UUID, &m.Domain, &m.DomainUUID, &m.Code, &m.Title, &m.Passcode, &m.Host, &m.HostName, &m.Lobby, &m.Guests)
	if err != nil {
		return nil, notFound("meeting")
	}
	return &m, nil
}

// meetingsAPI: plan meetings (signed-in extensions).
//
//	GET  /api/meetings            the domain's planned meetings
//	POST /api/meetings            {title, passcode, scheduled_at, duration_minutes, lobby, allow_guests, description, instant}
//	PUT/DELETE /api/meetings/{code}
//	GET  /api/meetings/history[/{uuid}]   finished meetings: who came, chat, transcript
func (s *Server) meetingsAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, parts []string) error {
	code := part(parts, 0)
	switch {
	case code == "history" && r.Method == http.MethodGet:
		return s.meetingHistory(ctx, w, p, part(parts, 1))
	case code == "" && r.Method == http.MethodGet:
		rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('uuid', meeting_uuid, 'code', meeting_code, 'title', title,
			'passcode', coalesce(passcode, ''), 'host', host, 'host_name', coalesce(host_name, ''), 'scheduled_at', scheduled_at,
			'duration_minutes', duration_minutes, 'lobby', lobby, 'allow_guests', allow_guests, 'description', coalesce(description, ''))
			from rq_meetings where domain_uuid = $1::uuid and not instant order by coalesce(scheduled_at, insert_date) desc limit 500`, p.DomainUUID)
		if err != nil {
			return err
		}
		live := map[string]int{}
		s.meets.mu.Lock()
		for c, room := range s.meets.rooms {
			room.mu.Lock()
			live[c] = len(room.peers)
			room.mu.Unlock()
		}
		s.meets.mu.Unlock()
		out := []map[string]any{}
		for _, m := range rows {
			x := map[string]any{}
			for k, v := range m {
				x[k] = v
			}
			x["participants"] = live[m["code"]]
			x["link"] = meetingLink(m["code"])
			x["can_edit"] = p.owner() || m["host"] == p.Extension
			if !(p.owner() || m["host"] == p.Extension) {
				x["passcode"] = ""
			}
			out = append(out, x)
		}
		writeJSON(w, out)
		return nil
	case code == "" && r.Method == http.MethodPost, code != "" && r.Method == http.MethodPut:
		var body struct {
			Title       string `json:"title"`
			Passcode    string `json:"passcode"`
			ScheduledAt string `json:"scheduled_at"`
			Duration    any    `json:"duration_minutes"`
			Lobby       any    `json:"lobby"`
			AllowGuests any    `json:"allow_guests"`
			Description string `json:"description"`
			Instant     bool   `json:"instant"`
		}
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		body.Title = strings.TrimSpace(body.Title)
		if body.Title == "" {
			body.Title = p.Name + "'s meeting"
		}
		if utf8.RuneCountInString(body.Title) > 120 || utf8.RuneCountInString(body.Description) > 2000 {
			return badRequest("title or description too long")
		}
		if body.Passcode != "" && !reDigits.MatchString(body.Passcode) || len(body.Passcode) > 12 {
			return badRequest("passcode: up to 12 digits")
		}
		var when any
		if body.ScheduledAt != "" {
			t, err := time.Parse(time.RFC3339, body.ScheduledAt)
			if err != nil {
				if t, err = time.ParseInLocation("2006-01-02T15:04", body.ScheduledAt, time.Local); err != nil {
					return badRequest("scheduled time: invalid")
				}
			}
			when = t
		}
		dur := min(max(atoi(toStr(body.Duration), 60), 5), 24*60)
		// The waiting room is on unless the organizer turns it off: the
		// organizer lets each attendee in.
		lobby := body.Lobby == nil || truthy(toStr(body.Lobby))
		guests := body.AllowGuests == nil || truthy(toStr(body.AllowGuests))
		if code == "" {
			code = newMeetingCode()
			if _, err := s.store.Pool().Exec(ctx, `insert into rq_meetings (meeting_uuid, domain_uuid, meeting_code, title, passcode, host, host_name,
				scheduled_at, duration_minutes, lobby, allow_guests, description, instant, insert_date)
				values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10, $11, $12, $13, now())`, newUUID(), p.DomainUUID, code, body.Title,
				body.Passcode, p.Extension, p.Name, when, dur, lobby, guests, body.Description, body.Instant); err != nil {
				return err
			}
		} else {
			m, err := s.meetingByCode(ctx, code)
			if err != nil || m.DomainUUID != p.DomainUUID {
				return notFound("meeting")
			}
			if !p.owner() && m.Host != p.Extension {
				return errForbidden
			}
			if _, err := s.store.Pool().Exec(ctx, `update rq_meetings set title = $2, passcode = nullif($3, ''), scheduled_at = $4,
				duration_minutes = $5, lobby = $6, allow_guests = $7, description = $8 where meeting_code = $1`,
				m.Code, body.Title, body.Passcode, when, dur, lobby, guests, body.Description); err != nil {
				return err
			}
		}
		writeJSON(w, map[string]string{"code": code, "link": meetingLink(code)})
		return nil
	case code != "" && r.Method == http.MethodDelete:
		m, err := s.meetingByCode(ctx, code)
		if err != nil || m.DomainUUID != p.DomainUUID {
			return notFound("meeting")
		}
		if !p.owner() && m.Host != p.Extension {
			return errForbidden
		}
		if _, err := s.store.Pool().Exec(ctx, `delete from rq_meetings where meeting_code = $1`, m.Code); err != nil {
			return err
		}
		s.endRoom(m.Code, "the meeting was deleted")
		writeJSON(w, map[string]string{"ok": "deleted"})
		return nil
	}
	return errMethod
}

// guestAPI serves meeting participants without an account (and files to
// everyone in a meeting, whose WebSocket carries no cookie for guests):
//
//	GET  /api/guest/meeting/{code}     title and what is needed to join
//	GET  /api/guest/ws?code=&name=&email=&passcode=   the meeting WebSocket
//	POST /api/guest/file?token=&name=  a file to present (body: the file)
//	GET  /api/guest/file/{id}?token=   a presented file
func (s *Server) guestAPI(w http.ResponseWriter, r *http.Request, parts []string) error {
	if err := s.needDB(); err != nil {
		return err
	}
	if s.rtc == nil {
		return &apiError{http.StatusServiceUnavailable, "meetings are off (webrtc-port)"}
	}
	ctx := r.Context()
	switch part(parts, 0) {
	case "meeting":
		m, err := s.meetingByCode(ctx, part(parts, 1))
		if err != nil {
			return err
		}
		p := s.sessionPrincipal(r)
		member := p != nil && p.Kind == "extension" && p.DomainUUID == m.DomainUUID
		lobby, live := m.Lobby, 0
		s.meets.mu.Lock()
		if room := s.meets.rooms[m.Code]; room != nil {
			room.mu.Lock()
			lobby, live = room.lobby || room.locked, len(room.peers)
			room.mu.Unlock()
		}
		s.meets.mu.Unlock()
		writeJSON(w, map[string]any{"code": m.Code, "title": m.Title, "host": m.HostName, "passcode": m.Passcode != "" && !member,
			"lobby": lobby, "guests": m.Guests, "signed_in": member, "name": map[bool]string{true: p.nameOrEmpty(), false: ""}[member],
			"link": meetingLink(m.Code), "in_meeting": live, "brand": s.cfg.Brand, "organizer": member && p.Extension == m.Host})
		return nil
	case "file":
		return s.meetFileAPI(w, r, part(parts, 1))
	case "ws":
		q := r.URL.Query()
		m, err := s.meetingByCode(ctx, q.Get("code"))
		if err != nil {
			return err
		}
		p := s.sessionPrincipal(r)
		if p == nil || p.Kind != "extension" || p.DomainUUID != m.DomainUUID {
			// A guest.
			ip := clientIP(r)
			if s.loginBlocked(ip) {
				return &apiError{http.StatusTooManyRequests, "too many wrong passcodes; wait 10 minutes"}
			}
			if !m.Guests {
				return &apiError{http.StatusForbidden, "this meeting is for signed-in users only"}
			}
			if m.Passcode != "" && q.Get("passcode") != m.Passcode {
				s.loginFailed(ip)
				return &apiError{http.StatusForbidden, "wrong passcode"}
			}
			name := strings.TrimSpace(q.Get("name"))
			if name == "" || utf8.RuneCountInString(name) > 60 {
				return badRequest("enter your name (60 characters at most)")
			}
			email := strings.TrimSpace(q.Get("email"))
			if email != "" && (len(email) > 120 || !reEmail.MatchString(email)) {
				return badRequest("email: not an email address")
			}
			p = &principal{Kind: "guest", User: "guest:" + newUUID()[:8], Name: name, Role: "guest", Domain: m.Domain, DomainUUID: m.DomainUUID, Email: email}
		}
		if !sameOrigin(r) {
			return errForbidden
		}
		u := upgrader
		u.CheckOrigin = sameOrigin
		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
			return nil
		}
		s.runClient(conn, p, r.Host)
		return nil
	}
	return notFound("page")
}

func (p *principal) nameOrEmpty() string {
	if p == nil {
		return ""
	}
	return p.Name
}

func (s *Server) room(m *meetingRow) *meetRoom {
	s.meets.mu.Lock()
	defer s.meets.mu.Unlock()
	if r := s.meets.rooms[m.Code]; r != nil {
		return r
	}
	r := &meetRoom{s: s, code: m.Code, title: m.Title, domain: m.Domain, domainUUID: m.DomainUUID, host: m.Host + "@" + m.Domain, lobby: m.Lobby,
		started: time.Now(), peers: map[string]*meetPeer{}, waiting: map[string]*meetPeer{}, tracks: map[string]*meetTrack{},
		files: map[string]*meetFile{}, seen: map[string]bool{}}
	s.meets.rooms[m.Code] = r
	go r.keyframes()
	s.log.Info("meeting started", "code", m.Code, "title", m.Title)
	s.sw.RecordEvent("call", fmt.Sprintf("meeting %q (%s) started", m.Title, m.Code))
	return r
}

func (s *Server) endRoom(code, reason string) {
	s.meets.mu.Lock()
	r := s.meets.rooms[code]
	s.meets.mu.Unlock()
	if r == nil {
		return
	}
	r.mu.Lock()
	var all []*meetPeer
	for _, p := range r.peers {
		all = append(all, p)
	}
	for _, p := range r.waiting {
		all = append(all, p)
	}
	r.mu.Unlock()
	for _, p := range all {
		p.c.json(map[string]any{"t": "meet.ended", "reason": reason})
		p.leave(reason)
	}
}

func (s *Server) meetWS(c *wsClient, m wsMsg) {
	if s.rtc == nil {
		c.fail(m, errors.New("meetings are off (webrtc-port)"))
		return
	}
	c.mu.Lock()
	peer := c.meet
	c.mu.Unlock()
	if m.T == "meet.join" {
		if peer != nil {
			peer.leave("rejoined")
		}
		if err := s.joinMeeting(c, m); err != nil {
			c.fail(m, err)
		}
		return
	}
	if peer == nil {
		c.fail(m, errors.New("not in a meeting"))
		return
	}
	r := peer.room
	switch m.T {
	case "meet.answer":
		var sd webrtc.SessionDescription
		if json.Unmarshal(m.SDP, &sd) != nil {
			return
		}
		peer.mu.Lock()
		pc := peer.pc
		peer.mu.Unlock()
		if pc == nil {
			return
		}
		if err := pc.SetRemoteDescription(sd); err != nil {
			s.log.Debug("meeting: answer", "err", err)
		}
		peer.mu.Lock()
		peer.negotiating = false
		again := peer.again
		peer.again = false
		peer.mu.Unlock()
		if again {
			peer.offer()
		}
	case "meet.ice":
		var cand webrtc.ICECandidateInit
		peer.mu.Lock()
		pc := peer.pc
		peer.mu.Unlock()
		if pc != nil && json.Unmarshal(m.Candidate, &cand) == nil && cand.Candidate != "" {
			_ = pc.AddICECandidate(cand)
		}
	case "meet.state":
		var st struct{ Audio, Video, Screen, Hand bool }
		if json.Unmarshal(m.Data, &st) == nil {
			peer.mu.Lock()
			peer.audio, peer.video, peer.screen, peer.hand = st.Audio, st.Video, st.Screen, st.Hand
			peer.mu.Unlock()
			r.broadcastPeers()
		}
	case "meet.chat":
		text := strings.TrimSpace(m.Body)
		if text == "" || utf8.RuneCountInString(text) > 2000 {
			return
		}
		msg := meetChat{From: peer.id, Name: peer.name, Text: text, Time: time.Now().UnixMilli()}
		r.mu.Lock()
		r.chat = append(r.chat, msg)
		if len(r.chat) > maxMeetChat {
			r.chat = r.chat[len(r.chat)-maxMeetChat:]
		}
		r.mu.Unlock()
		r.broadcast(map[string]any{"t": "meet.chat", "message": msg})
	case "meet.transcribe":
		r.mu.Lock()
		r.transcribing = m.On
		r.mu.Unlock()
		r.broadcast(map[string]any{"t": "meet.transcribe", "on": m.On, "by": peer.name})
	case "meet.caption":
		// Each browser transcribes its own microphone; final lines are kept.
		text := strings.TrimSpace(m.Body)
		if text == "" || utf8.RuneCountInString(text) > 1000 {
			return
		}
		line := meetChat{From: peer.id, Name: peer.name, Text: text, Time: time.Now().UnixMilli()}
		r.mu.Lock()
		on := r.transcribing
		if on && m.On && len(r.transcript) < maxTranscriptLen {
			r.transcript = append(r.transcript, line)
		}
		r.mu.Unlock()
		if on {
			r.broadcast(map[string]any{"t": "meet.caption", "line": line, "final": m.On})
		}
	case "meet.present":
		var req struct {
			File string `json:"file"`
			Page int    `json:"page"`
			Stop bool   `json:"stop"`
		}
		if json.Unmarshal(m.Data, &req) != nil {
			return
		}
		r.mu.Lock()
		cur := r.present
		switch {
		case req.Stop:
			if cur != nil && (cur.Peer == peer.id || peer.host) {
				r.present = nil
			}
		case req.File != "":
			if f := r.files[req.File]; f != nil {
				r.present = &meetPresent{Peer: peer.id, Name: peer.name, File: req.File, Title: f.name, Type: f.ctype, Page: max(req.Page, 1)}
			}
		case cur != nil && cur.Peer == peer.id:
			cp := *cur
			cp.Page = min(max(req.Page, 1), 5000)
			r.present = &cp
		}
		now := r.present
		r.mu.Unlock()
		r.broadcast(map[string]any{"t": "meet.present", "present": now})
	case "meet.board":
		r.mu.Lock()
		r.board = m.On
		r.mu.Unlock()
		r.broadcast(map[string]any{"t": "meet.board", "on": m.On, "by": peer.name})
	case "meet.draw":
		if len(m.Data) == 0 || len(m.Data) > 16<<10 || !json.Valid(m.Data) {
			return
		}
		r.mu.Lock()
		if len(r.strokes) >= maxBoardStrokes {
			r.mu.Unlock()
			c.fail(m, errors.New("the whiteboard is full: clear it first"))
			return
		}
		r.strokes = append(r.strokes, append(json.RawMessage(nil), m.Data...))
		r.mu.Unlock()
		r.broadcast(map[string]any{"t": "meet.draw", "peer": peer.id, "stroke": json.RawMessage(m.Data)})
	case "meet.clear":
		r.mu.Lock()
		r.strokes = nil
		r.mu.Unlock()
		r.broadcast(map[string]any{"t": "meet.clear", "by": peer.name})
	case "meet.reaction":
		if utf8.RuneCountInString(m.Body) <= 4 {
			r.broadcast(map[string]any{"t": "meet.reaction", "peer": peer.id, "emoji": m.Body})
		}
	case "meet.leave":
		peer.leave("left")
	default:
		// Host controls.
		if !peer.host {
			c.fail(m, errors.New("only the host can do this"))
			return
		}
		r.mu.Lock()
		target := r.peers[m.Peer]
		if target == nil {
			target = r.waiting[m.Peer]
		}
		r.mu.Unlock()
		switch m.T {
		case "meet.admit":
			if target != nil {
				r.admit(target)
			}
		case "meet.deny":
			if target != nil {
				target.c.json(map[string]any{"t": "meet.ended", "reason": "the host did not let you in"})
				target.leave("denied")
			}
		case "meet.mute":
			if target != nil {
				target.c.json(map[string]any{"t": "meet.mute", "by": peer.name})
			}
		case "meet.muteall":
			r.mu.Lock()
			for _, p := range r.peers {
				if p != peer {
					p.c.json(map[string]any{"t": "meet.mute", "by": peer.name})
				}
			}
			r.mu.Unlock()
		case "meet.remove":
			if target != nil && target != peer {
				target.c.json(map[string]any{"t": "meet.ended", "reason": "the host removed you from the meeting"})
				target.leave("removed")
			}
		case "meet.lock":
			r.mu.Lock()
			r.locked = m.On
			r.mu.Unlock()
			r.broadcastPeers()
		case "meet.lobby":
			// The waiting room on or off; off lets everyone waiting in.
			r.mu.Lock()
			r.lobby = m.On
			r.mu.Unlock()
			r.broadcastPeers()
			r.notifyHosts()
		case "meet.end":
			s.endRoom(r.code, "the host ended the meeting")
		}
	}
}

func (s *Server) joinMeeting(c *wsClient, m wsMsg) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := s.meetingByCode(ctx, m.Room)
	if err != nil {
		return err
	}
	if c.p.DomainUUID != row.DomainUUID {
		return notFound("meeting")
	}
	r := s.room(row)
	peer := &meetPeer{id: newUUID()[:8], room: r, c: c, name: c.p.Name, key: c.p.key(), email: c.p.Email, guest: c.p.Kind == "guest",
		audio: true, video: true, token: strings.ReplaceAll(newUUID(), "-", "")}
	if c.p.Kind == "extension" {
		peer.name = strings.TrimSpace(c.p.Name)
		if peer.name == "" || peer.name == c.p.Extension {
			peer.name = "Ext " + c.p.Extension
		}
		// The organizer (who made the meeting) is its host; a meeting made
		// by the server account has the domain's owners as hosts.
		peer.host = strings.EqualFold(c.p.key(), r.host) || (r.host == "@"+r.domain && c.p.Role == RoleOwner)
	}
	c.mu.Lock()
	c.meet = peer
	c.mu.Unlock()
	s.meets.mu.Lock()
	s.meets.tokens[peer.token] = peer
	s.meets.mu.Unlock()
	r.mu.Lock()
	full := len(r.peers) >= maxMeetingPeers
	locked := r.locked && !peer.host
	hostHere := false
	for _, p := range r.peers {
		hostHere = hostHere || p.host
	}
	// Everybody but the organizer waits in the waiting room when the
	// meeting has one (it has by default), until the organizer lets them
	// in; nobody gets into a locked meeting without being let in.
	wait := locked || (!peer.host && r.lobby)
	if wait {
		r.waiting[peer.id] = peer
	}
	r.mu.Unlock()
	if full {
		peer.leave("full")
		return errors.New("the meeting is full")
	}
	c.json(map[string]any{"t": "meet.joined", "me": peer.id, "host": peer.host, "title": r.title, "code": r.code, "waiting": wait,
		"link": meetingLink(r.code), "started": r.started.UnixMilli(), "now": time.Now().UnixMilli(), "token": peer.token})
	if wait {
		msg := "Waiting for the organizer to let you in…"
		if !hostHere {
			msg = "The organizer has not joined yet. They will let you in when they do."
		}
		c.json(map[string]any{"t": "meet.waiting", "message": msg})
		r.notifyHosts()
		return nil
	}
	r.admit(peer)
	return nil
}

// admit moves a peer into the meeting and connects its media.
func (r *meetRoom) admit(peer *meetPeer) {
	pc, err := r.s.rtc.meetAPI.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		peer.c.json(map[string]any{"t": "meet.ended", "reason": "media error: " + err.Error()})
		peer.leave("error")
		return
	}
	// The participant's own uplinks: microphone, camera, screen.
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeVideo} {
		if _, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			pc.Close()
			peer.leave("error")
			return
		}
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			peer.c.json(map[string]any{"t": "meet.ice", "candidate": c.ToJSON()})
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed {
			peer.c.json(map[string]any{"t": "meet.media", "state": "failed"})
		}
	})
	pc.OnTrack(func(remote *webrtc.TrackRemote, recv *webrtc.RTPReceiver) {
		kind := "audio"
		if remote.Kind() == webrtc.RTPCodecTypeVideo {
			kind = "camera"
			trs := pc.GetTransceivers()
			if len(trs) > 2 && trs[2].Receiver() == recv {
				kind = "screen"
			}
		}
		id := peer.id + "|" + kind
		local, err := webrtc.NewTrackLocalStaticRTP(remote.Codec().RTPCodecCapability, id, id)
		if err != nil {
			return
		}
		r.mu.Lock()
		r.tracks[id] = &meetTrack{local: local, owner: peer.id, kind: kind, ssrc: uint32(remote.SSRC())}
		r.mu.Unlock()
		r.signalAll()
		defer func() {
			r.mu.Lock()
			delete(r.tracks, id)
			r.mu.Unlock()
			r.signalAll()
		}()
		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			if err := local.WriteRTP(pkt); err != nil && !errors.Is(err, io.ErrClosedPipe) {
				return
			}
		}
	})
	peer.mu.Lock()
	peer.pc = pc
	peer.mu.Unlock()
	r.mu.Lock()
	delete(r.waiting, peer.id)
	r.peers[peer.id] = peer
	who := peer.key
	if peer.guest {
		who = "guest:" + peer.name + "|" + peer.email
	}
	if !r.seen[who] {
		r.seen[who] = true
		a := meetAttendee{Name: peer.name, Guest: peer.guest, Email: peer.email, Joined: time.Now().UnixMilli()}
		if !peer.guest {
			a.Key = peer.key
		}
		r.attended = append(r.attended, a)
	}
	state := map[string]any{"t": "meet.admitted", "chat": append([]meetChat(nil), r.chat...), "transcribing": r.transcribing,
		"transcript": append([]meetChat(nil), r.transcript...), "present": r.present, "board": r.board,
		"strokes": append([]json.RawMessage(nil), r.strokes...), "started": r.started.UnixMilli()}
	r.mu.Unlock()
	peer.c.json(state)
	r.broadcastPeers()
	r.notifyHosts()
	r.signalAll()
}

// signalAll renegotiates every participant so each receives the others'
// current tracks.
func (r *meetRoom) signalAll() {
	r.mu.Lock()
	peers := make([]*meetPeer, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p)
	}
	r.mu.Unlock()
	for _, p := range peers {
		p.offer()
	}
}

// offer updates the tracks sent to a peer and sends it an offer (or marks
// that another offer is needed when one is already in flight).
func (p *meetPeer) offer() {
	p.mu.Lock()
	if p.left || p.pc == nil {
		p.mu.Unlock()
		return
	}
	if p.negotiating {
		p.again = true
		p.mu.Unlock()
		return
	}
	p.negotiating = true
	pc := p.pc
	p.mu.Unlock()
	r := p.room
	r.mu.Lock()
	want := map[string]*meetTrack{}
	for id, t := range r.tracks {
		if t.owner != p.id {
			want[id] = t
		}
	}
	r.mu.Unlock()
	have := map[string]bool{}
	for _, s := range pc.GetSenders() {
		t := s.Track()
		if t == nil {
			continue
		}
		if _, ok := want[t.ID()]; !ok {
			_ = pc.RemoveTrack(s)
			continue
		}
		have[t.ID()] = true
	}
	for id, t := range want {
		if have[id] {
			continue
		}
		// A transceiver of its own: AddTrack would reuse the participant's
		// upload transceivers (microphone, camera, screen).
		tr, err := pc.AddTransceiverFromTrack(t.local, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			continue
		}
		sender := tr.Sender()
		go func() { // read RTCP (PLI from this viewer goes to the publisher)
			buf := make([]byte, 1500)
			for {
				n, _, err := sender.Read(buf)
				if err != nil {
					return
				}
				pkts, err := rtcp.Unmarshal(buf[:n])
				if err != nil {
					continue
				}
				for _, pk := range pkts {
					if _, ok := pk.(*rtcp.PictureLossIndication); ok {
						r.requestKeyframe(t.owner, t.ssrc)
					}
				}
			}
		}()
	}
	sd, err := pc.CreateOffer(nil)
	if err == nil {
		err = pc.SetLocalDescription(sd)
	}
	if err != nil {
		p.mu.Lock()
		p.negotiating = false
		p.mu.Unlock()
		return
	}
	// Who is who: the stream ids of the forwarded tracks are "peer|kind".
	p.c.json(map[string]any{"t": "meet.offer", "sdp": pc.LocalDescription()})
	if tr := pc.GetTransceivers(); len(tr) > 0 {
		for _, cand := range r.s.rtc.pageCandidates(p.c.host, tr[0].Mid()) {
			p.c.json(map[string]any{"t": "meet.ice", "candidate": cand})
		}
	}
}

func (r *meetRoom) requestKeyframe(owner string, ssrc uint32) {
	r.mu.Lock()
	p := r.peers[owner]
	r.mu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	pc := p.pc
	p.mu.Unlock()
	if pc != nil {
		_ = pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
	}
}

// keyframes asks publishers for a keyframe every 3 seconds so late
// joiners and lossy viewers recover quickly.
func (r *meetRoom) keyframes() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for range t.C {
		r.s.meets.mu.Lock()
		alive := r.s.meets.rooms[r.code] == r
		r.s.meets.mu.Unlock()
		if !alive {
			return
		}
		r.mu.Lock()
		var reqs [][2]any
		for _, tr := range r.tracks {
			if tr.kind != "audio" {
				reqs = append(reqs, [2]any{tr.owner, tr.ssrc})
			}
		}
		r.mu.Unlock()
		for _, q := range reqs {
			r.requestKeyframe(q[0].(string), q[1].(uint32))
		}
	}
}

func (r *meetRoom) broadcast(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	r.mu.Lock()
	peers := make([]*meetPeer, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p)
	}
	r.mu.Unlock()
	for _, p := range peers {
		p.c.write(data)
	}
}

func (r *meetRoom) broadcastPeers() {
	r.mu.Lock()
	list := []map[string]any{}
	for _, p := range r.peers {
		p.mu.Lock()
		list = append(list, map[string]any{"id": p.id, "name": p.name, "host": p.host, "guest": p.guest, "audio": p.audio,
			"video": p.video, "screen": p.screen, "hand": p.hand})
		p.mu.Unlock()
	}
	locked, lobby := r.locked, r.lobby
	r.mu.Unlock()
	r.broadcast(map[string]any{"t": "meet.peers", "peers": list, "locked": locked, "lobby": lobby})
}

// notifyHosts tells the hosts who is in the waiting room.
func (r *meetRoom) notifyHosts() {
	r.mu.Lock()
	waiting := []map[string]any{}
	for _, p := range r.waiting {
		waiting = append(waiting, map[string]any{"id": p.id, "name": p.name})
	}
	var hosts []*meetPeer
	for _, p := range r.peers {
		if p.host {
			hosts = append(hosts, p)
		}
	}
	r.mu.Unlock()
	for _, h := range hosts {
		h.c.json(map[string]any{"t": "meet.lobby", "waiting": waiting})
	}
	// A host arriving lets the waiting guests in when there is no lobby.
	r.mu.Lock()
	lobby := r.lobby
	r.mu.Unlock()
	if len(hosts) > 0 && !lobby {
		r.mu.Lock()
		locked := r.locked
		var admit []*meetPeer
		if !locked {
			for _, p := range r.waiting {
				admit = append(admit, p)
			}
		}
		r.mu.Unlock()
		for _, p := range admit {
			r.admit(p)
		}
	}
}

func (p *meetPeer) leave(reason string) {
	p.mu.Lock()
	if p.left {
		p.mu.Unlock()
		return
	}
	p.left = true
	pc := p.pc
	p.mu.Unlock()
	r := p.room
	r.s.meets.mu.Lock()
	delete(r.s.meets.tokens, p.token)
	r.s.meets.mu.Unlock()
	r.mu.Lock()
	delete(r.peers, p.id)
	delete(r.waiting, p.id)
	for id, t := range r.tracks {
		if t.owner == p.id {
			delete(r.tracks, id)
		}
	}
	stopPresent := r.present != nil && r.present.Peer == p.id
	if stopPresent {
		r.present = nil
	}
	empty := len(r.peers) == 0 && len(r.waiting) == 0
	r.mu.Unlock()
	if stopPresent && !empty {
		r.broadcast(map[string]any{"t": "meet.present", "present": nil})
	}
	p.c.mu.Lock()
	if p.c.meet == p {
		p.c.meet = nil
	}
	p.c.mu.Unlock()
	if pc != nil {
		pc.Close()
	}
	if empty {
		r.s.meets.mu.Lock()
		if r.s.meets.rooms[r.code] == r {
			delete(r.s.meets.rooms, r.code)
		}
		r.s.meets.mu.Unlock()
		r.s.log.Info("meeting ended", "code", r.code, "duration", time.Since(r.started).Round(time.Second))
		go r.saveHistory()
		return
	}
	r.broadcastPeers()
	r.notifyHosts()
	r.signalAll()
}
