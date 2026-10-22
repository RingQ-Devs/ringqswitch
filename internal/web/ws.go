package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
)

// The realtime channel: one WebSocket per browser tab (/api/ws) carries
// live dashboard updates, chat, presence, the dialer and meeting
// signalling. Messages are JSON objects with a "t" (type) field.

type wsClient struct {
	s    *Server
	p    *principal
	conn *websocket.Conn
	send chan []byte
	id   string
	host string // the page's host (for pageCandidates)

	mu         sync.Mutex
	live       bool   // subscribed to live data
	liveDomain string // "" = all domains (administrator)
	closed     bool
	phone      *webPhone
	meet       *meetPeer
}

type hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
	poke    chan struct{} // something changed: push live data soon
}

func newHub() *hub {
	return &hub{clients: map[*wsClient]struct{}{}, poke: make(chan struct{}, 1)}
}

func (h *hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *hub) list() []*wsClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		out = append(out, c)
	}
	return out
}

// online reports whether a principal key has a connected tab.
func (h *hub) online(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if strings.EqualFold(c.p.key(), key) {
			return true
		}
	}
	return false
}

// sendTo delivers a message to every tab of the given principal keys.
func (h *hub) sendTo(keys []string, msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[strings.ToLower(k)] = true
	}
	for _, c := range h.list() {
		if want[strings.ToLower(c.p.key())] {
			c.write(data)
		}
	}
}

// broadcastDomain delivers a message to every tab signed in to a domain.
func (h *hub) broadcastDomain(domain string, msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	for _, c := range h.list() {
		if strings.EqualFold(c.p.Domain, domain) {
			c.write(data)
		}
	}
}

func (c *wsClient) write(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.send <- data:
	default: // a stuck browser does not slow the others down
	}
}

func (c *wsClient) json(msg any) {
	if data, err := json.Marshal(msg); err == nil {
		c.write(data)
	}
}

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 16384}

// sameOrigin rejects WebSocket connections opened by other web sites
// (the browser sends the session cookie with them).
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request, p *principal) {
	if !sameOrigin(r) {
		httpError(w, http.StatusForbidden, "cross-origin WebSocket refused")
		return
	}
	u := upgrader
	u.CheckOrigin = sameOrigin
	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.runClient(conn, p, r.Host)
}

func (s *Server) runClient(conn *websocket.Conn, p *principal, host string) {
	c := &wsClient{s: s, p: p, conn: conn, send: make(chan []byte, 256), id: newUUID(), host: host}
	wasOnline := s.hub.online(p.key())
	s.hub.add(c)
	if !wasOnline && p.Kind == "extension" {
		s.presenceChanged(p.Domain)
	}
	if p.Kind == "extension" {
		s.liveReplay(c)
	}
	done := make(chan struct{})
	go c.writer(done)
	c.reader()
	close(done)
	c.mu.Lock()
	c.closed = true
	phone, meet := c.phone, c.meet
	c.phone, c.meet = nil, nil
	c.mu.Unlock()
	s.hub.remove(c)
	if phone != nil {
		phone.close()
	}
	if meet != nil {
		meet.leave("disconnected")
	}
	if p.Kind == "extension" && !s.hub.online(p.key()) {
		s.presenceChanged(p.Domain)
	}
}

func (c *wsClient) writer(done <-chan struct{}) {
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	defer c.conn.Close()
	for {
		select {
		case data := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

func (c *wsClient) reader() {
	c.conn.SetReadLimit(1 << 20)
	_ = c.conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	c.conn.SetPongHandler(func(string) error { return c.conn.SetReadDeadline(time.Now().Add(70 * time.Second)) })
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		var m wsMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		c.handle(m)
	}
}

// wsMsg is an incoming message. Fields are shared by the message types.
type wsMsg struct {
	T         string          `json:"t"`
	Ref       string          `json:"ref,omitempty"` // echoed in the reply
	Domain    string          `json:"domain,omitempty"`
	Room      string          `json:"room,omitempty"`
	Body      string          `json:"body,omitempty"`
	Number    string          `json:"number,omitempty"`
	Call      string          `json:"call,omitempty"`
	Target    string          `json:"target,omitempty"`
	Digits    string          `json:"digits,omitempty"`
	On        bool            `json:"on,omitempty"`
	Peer      string          `json:"peer,omitempty"`
	SDP       json.RawMessage `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Gen       int64           `json:"gen,omitempty"` // dialer: which phone.start a message belongs to
}

func (c *wsClient) reply(m wsMsg, msg map[string]any) {
	if m.Ref != "" {
		msg["ref"] = m.Ref
	}
	c.json(msg)
}

func (c *wsClient) fail(m wsMsg, err error) {
	c.reply(m, map[string]any{"t": "error", "error": err.Error()})
}

func (c *wsClient) handle(m wsMsg) {
	switch {
	case m.T == "ping":
		c.reply(m, map[string]any{"t": "pong", "time": time.Now().UnixMilli()})
	case m.T == "live":
		domain := m.Domain
		if !c.p.admin() {
			domain = c.p.Domain
		}
		c.mu.Lock()
		c.live, c.liveDomain = m.On, domain
		c.mu.Unlock()
		if m.On {
			c.json(map[string]any{"t": "live", "data": c.s.live(domain)})
		}
	case strings.HasPrefix(m.T, "chat."):
		if c.p.Kind != "extension" {
			c.fail(m, errForbidden)
			return
		}
		c.s.chatWS(c, m)
	case strings.HasPrefix(m.T, "phone."):
		c.s.phoneWS(c, m)
	case strings.HasPrefix(m.T, "meet."):
		c.s.meetWS(c, m)
	}
}

// livePump pushes live data to subscribed tabs: at once when calls,
// registrations or agents change (at most 4 times a second), and every
// 2 seconds otherwise.
func (s *Server) livePump(stop <-chan struct{}) {
	sub := s.sw.Events().Subscribe(512, func(e *core.Event) bool {
		switch e.Name {
		case core.EvChannelCreate, core.EvChannelAnswer, core.EvChannelBridge, core.EvChannelUnbridge, core.EvChannelHangupComplete,
			core.EvChannelCallState:
			return true
		case core.EvCustom:
			sc := e.Subclass()
			return sc == "callcenter::info" || strings.HasPrefix(sc, "ringq::register") || strings.HasPrefix(sc, "ringq::unregister") ||
				strings.HasPrefix(sc, "ringq::expire") || strings.HasPrefix(sc, "conference::")
		}
		return false
	})
	defer sub.Close()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var last time.Time
	pending := false
	push := func() {
		last, pending = time.Now(), false
		byDomain := map[string][]*wsClient{}
		for _, c := range s.hub.list() {
			c.mu.Lock()
			d, on := c.liveDomain, c.live
			c.mu.Unlock()
			if on {
				byDomain[d] = append(byDomain[d], c)
			}
		}
		for d, clients := range byDomain {
			data, err := json.Marshal(map[string]any{"t": "live", "data": s.live(d)})
			if err != nil {
				continue
			}
			for _, c := range clients {
				c.write(data)
			}
		}
	}
	soon := time.NewTimer(time.Hour)
	defer soon.Stop()
	for {
		select {
		case <-stop:
			return
		case <-sub.C:
			pending = true
		case <-s.hub.poke:
			pending = true
		case <-tick.C:
			push()
			continue
		case <-soon.C:
		}
		if pending {
			if wait := 250*time.Millisecond - time.Since(last); wait > 0 {
				soon.Reset(wait)
				continue
			}
			push()
		}
	}
}

// presenceChanged tells the domain's tabs to refresh their contact list.
func (s *Server) presenceChanged(domain string) {
	s.hub.broadcastDomain(domain, map[string]any{"t": "presence"})
}
