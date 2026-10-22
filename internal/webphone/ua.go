// Package webphone is the SIP side of the browser dialer: a small SIP user
// agent that registers to RingQswitch as the signed-in extension (like a
// desk phone would) and places and receives its calls. The web package
// connects its audio to the browser over WebRTC. Because it is an ordinary
// SIP phone to the switch, every feature (dialplan, voicemail, queues,
// recording, transfer, CDRs) works for the browser too.
package webphone

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sdp"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Call states.
const (
	StateDialing  = "dialing"
	StateRinging  = "ringing"
	StateEarly    = "early" // ringing with audio (ringback, announcements)
	StateAnswered = "answered"
	StateEnded    = "ended"
)

// Config describes the extension the UA registers as.
type Config struct {
	User      string
	Domain    string
	Password  func() string // read at each authentication (it may change)
	Server    netip.AddrPort
	LocalIP   netip.Addr
	UserAgent string
	Expires   int
	Log       *slog.Logger
}

// CallInfo is what the browser is told about a call.
type CallInfo struct {
	ID         string `json:"id"`
	Direction  string `json:"direction"` // in, out
	Number     string `json:"number"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Cause      string `json:"cause,omitempty"`
	Held       bool   `json:"held"`        // we put it on hold
	RemoteHold bool   `json:"remote_hold"` // the other side did
	Started    int64  `json:"started"`
	Answered   int64  `json:"answered,omitempty"`
	AutoAnswer bool   `json:"auto_answer,omitempty"` // intercom / supervisor monitoring
}

// Handler receives the UA's events.
type Handler interface {
	Registration(ok bool, reason string)
	CallUpdate(info CallInfo)
	Audio(callID string, payload []byte) // PCMU, 20 ms
	MWI(newMsgs, oldMsgs int)
}

type call struct {
	info       CallInfo
	callID     string
	localTag   string
	remoteTag  string
	localURI   string // name-addr without tag
	remoteURI  string
	target     *sip.URI // the other side's Contact
	invite     *sip.Message
	inviteResp *sip.Message // last response we sent to an incoming INVITE
	cseq       uint32       // our last CSeq in the dialog
	remoteCSeq uint32
	rtp        *net.UDPConn
	remoteRTP  netip.AddrPort
	codec      string // PCMU or PCMA
	pt         uint8
	offer      *sdp.Session // the INVITE's offer (incoming calls)
	sdpVersion uint64
	acked      chan struct{} // closed when the ACK for our 200 arrives
	ack        *sip.Message  // our ACK for a 2xx (re-sent on retransmissions)
	cancelled  bool
	seq        uint16
	ts         uint32
	ssrc       uint32
}

// UA is one registered browser dialer.
type UA struct {
	cfg   Config
	h     Handler
	conn  *net.UDPConn
	local netip.AddrPort
	log   *slog.Logger

	mu        sync.Mutex
	calls     map[string]*call // by Call-ID
	waiters   map[string]chan *sip.Message
	cseq      uint32
	regCallID string
	regTag    string
	closed    bool
	done      chan struct{}
	wg        sync.WaitGroup
	events    chan CallInfo // call updates, delivered in order
}

// Start opens the UA and registers it.
func Start(cfg Config, h Handler) (*UA, error) {
	if cfg.Expires <= 0 {
		cfg.Expires = 300
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: cfg.LocalIP.AsSlice()})
	if err != nil {
		return nil, err
	}
	u := &UA{cfg: cfg, h: h, conn: conn, local: conn.LocalAddr().(*net.UDPAddr).AddrPort(),
		log: cfg.Log.With("dialer", cfg.User+"@"+cfg.Domain), calls: map[string]*call{}, waiters: map[string]chan *sip.Message{},
		regCallID: sip.NewCallID(), regTag: sip.NewTag(), done: make(chan struct{}), events: make(chan CallInfo, 1024)}
	u.wg.Add(2)
	go u.readLoop()
	go u.registerLoop()
	go func() {
		for info := range u.events {
			h.CallUpdate(info)
		}
	}()
	return u, nil
}

// LocalAddr is the UA's SIP socket address.
func (u *UA) LocalAddr() netip.AddrPort { return u.local }

// Close hangs up, unregisters and releases the UA.
func (u *UA) Close() {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	ids := make([]string, 0, len(u.calls))
	for id := range u.calls {
		ids = append(ids, id)
	}
	u.mu.Unlock()
	for _, id := range ids {
		_ = u.Hangup(id)
	}
	close(u.done)
	_, _ = u.register(0)
	u.conn.Close()
	u.wg.Wait()
	u.mu.Lock()
	close(u.events)
	u.events = nil
	u.mu.Unlock()
}

func (u *UA) contact() string {
	return fmt.Sprintf("<sip:%s@%s;transport=udp>", u.cfg.User, u.local)
}

func (u *UA) aor() string { return fmt.Sprintf("<sip:%s@%s>", u.cfg.User, u.cfg.Domain) }

func (u *UA) nextCSeq() uint32 {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cseq++
	return u.cseq
}

func (u *UA) send(m *sip.Message) error {
	_, err := u.conn.WriteToUDPAddrPort(m.Bytes(), u.cfg.Server)
	return err
}

// ---------------------------------------------------------------------------
// Transactions

var errTimeout = errors.New("no answer from the switch")

// request sends a request with a new Via branch, retransmitting it (UDP)
// until a response arrives, and returns the channel of responses.
func (u *UA) request(req *sip.Message) (<-chan *sip.Message, string) {
	branch := sip.NewBranch()
	req.Prepend("Via", fmt.Sprintf("SIP/2.0/UDP %s;branch=%s;rport", u.local, branch))
	if req.Get("Max-Forwards") == "" {
		req.Add("Max-Forwards", "70")
	}
	if req.Get("User-Agent") == "" {
		req.Add("User-Agent", u.cfg.UserAgent)
	}
	ch := make(chan *sip.Message, 16)
	got := make(chan struct{})
	var once sync.Once
	relay := make(chan *sip.Message, 16)
	u.mu.Lock()
	u.waiters[branch] = relay
	u.mu.Unlock()
	go func() {
		defer func() {
			time.AfterFunc(32*time.Second, func() {
				u.mu.Lock()
				delete(u.waiters, branch)
				u.mu.Unlock()
			})
		}()
		final := false
		for !final {
			select {
			case m := <-relay:
				once.Do(func() { close(got) })
				final = m.StatusCode >= 200
				select {
				case ch <- m:
				default:
				}
			case <-time.After(32 * time.Second):
				once.Do(func() { close(got) })
				resp := sip.NewResponse(req, 408, "Request Timeout")
				ch <- resp
				return
			case <-u.done:
				if req.Method != "REGISTER" && req.Method != "BYE" && req.Method != "CANCEL" {
					return
				}
			}
		}
	}()
	_ = u.send(req)
	go func() { // retransmissions (timer A / E)
		interval := 500 * time.Millisecond
		for i := 0; i < 7; i++ {
			select {
			case <-got:
				return
			case <-time.After(interval):
				_ = u.send(req)
				interval = min(interval*2, 4*time.Second)
			}
		}
	}()
	return ch, branch
}

func waitFinal(ch <-chan *sip.Message, timeout time.Duration, prov func(*sip.Message)) (*sip.Message, error) {
	deadline := time.After(timeout)
	for {
		select {
		case m := <-ch:
			if m.StatusCode < 200 {
				if prov != nil {
					prov(m)
				}
				continue
			}
			return m, nil
		case <-deadline:
			return nil, errTimeout
		}
	}
}

func (u *UA) authorize(req, resp *sip.Message) bool {
	hdr, authHdr := "WWW-Authenticate", "Authorization"
	if resp.StatusCode == 407 {
		hdr, authHdr = "Proxy-Authenticate", "Proxy-Authorization"
	}
	chal, ok := sip.ParseDigest(resp.Get(hdr))
	if !ok {
		return false
	}
	req.Del("Authorization")
	req.Del("Proxy-Authorization")
	req.Set(authHdr, sip.BuildAuthorization(chal, req.Method, req.RequestURI.String(), u.cfg.User, u.cfg.Password()))
	return true
}

// respond answers a received request.
func (u *UA) respond(req *sip.Message, code int, toTag string, body []byte, extra ...string) *sip.Message {
	resp := sip.NewResponse(req, code, "")
	if toTag != "" {
		resp.SetToTag(toTag)
	}
	if code >= 101 && code < 300 && (req.Method == "INVITE" || req.Method == "UPDATE") {
		resp.Add("Contact", u.contact())
	}
	for i := 0; i+1 < len(extra); i += 2 {
		resp.Add(extra[i], extra[i+1])
	}
	if body != nil {
		resp.Add("Content-Type", "application/sdp")
		resp.Body = body
	}
	resp.Add("User-Agent", u.cfg.UserAgent)
	_ = u.send(resp)
	return resp
}

func (u *UA) readLoop() {
	defer u.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, _, err := u.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		m, err := sip.Parse(append([]byte(nil), buf[:n]...))
		if err != nil {
			continue
		}
		if m.IsRequest() {
			u.handleRequest(m)
			continue
		}
		via, err := m.TopVia()
		if err != nil {
			continue
		}
		u.mu.Lock()
		ch := u.waiters[via.Branch()]
		u.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
			continue
		}
		// A retransmitted 2xx to our INVITE: acknowledge it again.
		if _, method := m.CSeq(); method == "INVITE" && m.StatusCode >= 200 && m.StatusCode < 300 {
			u.mu.Lock()
			c := u.calls[m.CallID()]
			var ack *sip.Message
			if c != nil {
				ack = c.ack
			}
			u.mu.Unlock()
			if ack != nil {
				_ = u.send(ack)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Registration

func (u *UA) register(expires int) (*sip.Message, error) {
	build := func() *sip.Message {
		req := sip.NewRequest("REGISTER", &sip.URI{Scheme: "sip", Host: u.cfg.Domain})
		req.Add("From", u.aor()+";tag="+u.regTag)
		req.Add("To", u.aor())
		req.Add("Call-ID", u.regCallID)
		req.Add("CSeq", fmt.Sprintf("%d REGISTER", u.nextCSeq()))
		req.Add("Contact", u.contact())
		req.Add("Expires", strconv.Itoa(expires))
		return req
	}
	req := build()
	ch, _ := u.request(req)
	resp, err := waitFinal(ch, 10*time.Second, nil)
	if err != nil || (resp.StatusCode != 401 && resp.StatusCode != 407) {
		return resp, err
	}
	req2 := build()
	if !u.authorize(req2, resp) {
		return resp, nil
	}
	ch, _ = u.request(req2)
	return waitFinal(ch, 10*time.Second, nil)
}

func (u *UA) registerLoop() {
	defer u.wg.Done()
	for {
		resp, err := u.register(u.cfg.Expires)
		wait := time.Duration(u.cfg.Expires) * time.Second * 8 / 10
		switch {
		case err != nil:
			u.h.Registration(false, err.Error())
			wait = 15 * time.Second
		case resp.StatusCode != 200:
			reason := fmt.Sprintf("%d %s", resp.StatusCode, resp.Reason)
			if resp.StatusCode == 403 {
				reason = "the switch refused the extension's SIP password (403)"
			}
			u.log.Warn("dialer registration failed", "status", resp.StatusCode)
			u.h.Registration(false, reason)
			wait = 60 * time.Second // do not hammer (and get blocked)
		default:
			u.h.Registration(true, "")
		}
		select {
		case <-u.done:
			return
		case <-time.After(wait):
		}
	}
}

// ---------------------------------------------------------------------------
// Calls

// update reports a call's state. The snapshot is queued under the lock,
// so the browser sees the changes in the order they happened.
func (u *UA) update(c *call) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.events == nil {
		return
	}
	select {
	case u.events <- c.info:
	default:
		u.log.Warn("dialer: event queue full")
	}
}

func randUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

func (u *UA) newCall(callID, dir, number, name string) (*call, error) {
	rtp, err := net.ListenUDP("udp", &net.UDPAddr{IP: u.cfg.LocalIP.AsSlice()})
	if err != nil {
		return nil, err
	}
	c := &call{callID: callID, rtp: rtp, ssrc: randUint32(), seq: uint16(randUint32()), ts: randUint32(),
		sdpVersion: 1, acked: make(chan struct{}),
		info: CallInfo{ID: callID, Direction: dir, Number: number, Name: name, Started: time.Now().Unix()}}
	return c, nil
}

func (u *UA) localSDP(c *call, direction string, answerTo *sdp.Session) []byte {
	port := c.rtp.LocalAddr().(*net.UDPAddr).Port
	l := sdp.Local{IP: u.cfg.LocalIP.String(), Port: port, SessionID: strconv.FormatUint(uint64(c.ssrc), 10), Version: c.sdpVersion}
	c.sdpVersion++
	codecs := []sdp.Codec{{PT: 0, Name: "PCMU", Rate: 8000, Channels: 1}, {PT: 8, Name: "PCMA", Rate: 8000, Channels: 1}, sdp.DTMFCodec}
	if answerTo != nil {
		am, _ := answerTo.Audio()
		var offered []sdp.Codec
		if am != nil {
			offered = am.Codecs()
		}
		chosen := sdp.Select(offered, []string{"PCMU", "PCMA"})
		return sdp.Answer(answerTo, l, chosen, direction).Marshal()
	}
	return sdp.NewOffer(l, codecs, direction).Marshal()
}

// applyRemoteSDP takes the other side's address and codec.
func (c *call) applyRemoteSDP(body []byte) (direction string) {
	s, err := sdp.Parse(body)
	if err != nil {
		return ""
	}
	m, _ := s.Audio()
	if m == nil {
		return ""
	}
	if addr, err := netip.ParseAddr(s.Conn(m)); err == nil && m.Port > 0 {
		c.remoteRTP = netip.AddrPortFrom(addr, uint16(m.Port))
	}
	for _, cd := range m.Codecs() {
		if strings.EqualFold(cd.Name, "PCMU") || strings.EqualFold(cd.Name, "PCMA") {
			c.codec, c.pt = strings.ToUpper(cd.Name), uint8(cd.PT)
			break
		}
	}
	return s.Direction(m)
}

func (u *UA) rtpReader(c *call) {
	buf := make([]byte, 2048)
	for {
		n, _, err := c.rtp.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		if n < 12 || buf[0]>>6 != 2 {
			continue
		}
		off := 12 + 4*int(buf[0]&0x0f)
		if buf[0]&0x10 != 0 && n >= off+4 {
			off += 4 + 4*int(binary.BigEndian.Uint16(buf[off+2:]))
		}
		end := n
		if buf[0]&0x20 != 0 && n > off {
			end -= int(buf[n-1])
		}
		if off >= end {
			continue
		}
		pt := buf[1] & 0x7f
		u.mu.Lock()
		codec, want := c.codec, c.pt
		u.mu.Unlock()
		if pt != want || codec == "" {
			continue // DTMF events, comfort noise
		}
		payload := append([]byte(nil), buf[off:end]...)
		if codec == "PCMA" {
			media.Transcode("PCMA", "PCMU", payload)
		}
		u.h.Audio(c.callID, payload)
	}
}

// SendAudio sends 20 ms of PCMU from the browser to the call. It reports
// whether the packet went out.
func (u *UA) SendAudio(callID string, payload []byte) bool {
	u.mu.Lock()
	c := u.calls[callID]
	if c == nil || !c.remoteRTP.IsValid() || c.codec == "" || c.info.Held {
		u.mu.Unlock()
		return false
	}
	pkt := make([]byte, 12+len(payload))
	pkt[0], pkt[1] = 0x80, c.pt
	binary.BigEndian.PutUint16(pkt[2:], c.seq)
	binary.BigEndian.PutUint32(pkt[4:], c.ts)
	binary.BigEndian.PutUint32(pkt[8:], c.ssrc)
	c.seq++
	c.ts += uint32(len(payload))
	dst, codec, conn := c.remoteRTP, c.codec, c.rtp
	u.mu.Unlock()
	copy(pkt[12:], payload)
	if codec == "PCMA" {
		media.Transcode("PCMU", "PCMA", pkt[12:])
	}
	_, err := conn.WriteToUDPAddrPort(pkt, dst)
	return err == nil
}

// MediaInfo describes a call's RTP towards the switch, for the audio check.
type MediaInfo struct {
	Local  string `json:"local"`  // our RTP address
	Remote string `json:"remote"` // where we send (the switch's SDP)
	Codec  string `json:"codec"`
}

// Media returns the call's RTP addresses and codec.
func (u *UA) Media(callID string) (MediaInfo, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	c := u.calls[callID]
	if c == nil {
		return MediaInfo{}, false
	}
	mi := MediaInfo{Local: c.rtp.LocalAddr().String(), Codec: c.codec}
	if c.remoteRTP.IsValid() {
		mi.Remote = c.remoteRTP.String()
	}
	return mi, true
}

// Dial calls a number (an extension, a feature code or an outside number).
func (u *UA) Dial(number string) (string, error) {
	u.mu.Lock()
	closed := u.closed
	u.mu.Unlock()
	if closed {
		return "", errors.New("the dialer is closed")
	}
	c, err := u.newCall(sip.NewCallID(), "out", number, "")
	if err != nil {
		return "", err
	}
	c.localTag = sip.NewTag()
	c.localURI = u.aor()
	c.remoteURI = fmt.Sprintf("<sip:%s@%s>", number, u.cfg.Domain)
	c.info.State = StateDialing
	u.mu.Lock()
	u.calls[c.callID] = c
	u.mu.Unlock()
	u.update(c)
	go u.rtpReader(c)
	go u.outgoing(c)
	return c.callID, nil
}

func (u *UA) buildInvite(c *call, body []byte) *sip.Message {
	ruri := &sip.URI{Scheme: "sip", User: c.info.Number, Host: u.cfg.Server.Addr().String(), Port: int(u.cfg.Server.Port())}
	req := sip.NewRequest("INVITE", ruri)
	req.Add("From", c.localURI+";tag="+c.localTag)
	req.Add("To", c.remoteURI)
	req.Add("Call-ID", c.callID)
	req.Add("CSeq", fmt.Sprintf("%d INVITE", c.cseq))
	req.Add("Contact", u.contact())
	req.Add("Allow", "INVITE, ACK, CANCEL, BYE, REFER, NOTIFY, INFO, OPTIONS, UPDATE")
	req.Add("Content-Type", "application/sdp")
	req.Body = body
	return req
}

func (u *UA) outgoing(c *call) {
	u.mu.Lock()
	body := u.localSDP(c, "sendrecv", nil)
	u.mu.Unlock()
	var authResp *sip.Message
	for attempt := 0; attempt < 2; attempt++ {
		u.mu.Lock()
		c.cseq = u.nextCSeqLocked()
		req := u.buildInvite(c, body)
		if authResp != nil {
			u.authorize(req, authResp)
		}
		c.invite = req
		cancelled := c.cancelled
		u.mu.Unlock()
		if cancelled {
			u.end(c, "cancelled")
			return
		}
		ch, _ := u.request(req)
		final, err := waitFinal(ch, 3*time.Minute, func(m *sip.Message) {
			u.mu.Lock()
			switch {
			case m.StatusCode == 180 || m.StatusCode == 183:
				state := StateRinging
				if m.HasSDP() {
					c.applyRemoteSDP(m.Body)
					state = StateEarly
				}
				if to, err := m.To(); err == nil {
					c.remoteTag = to.Tag()
				}
				changed := c.info.State != state
				c.info.State = state
				doCancel := c.cancelled
				u.mu.Unlock()
				if changed {
					u.update(c)
				}
				if doCancel {
					u.cancel(c)
				}
				return
			}
			doCancel := c.cancelled
			u.mu.Unlock()
			if doCancel {
				u.cancel(c)
			}
		})
		if err != nil {
			u.cancel(c)
			u.end(c, "no answer")
			return
		}
		if final.StatusCode == 401 || final.StatusCode == 407 {
			u.ackNon2xx(c, final)
			if attempt == 0 {
				authResp = final
				continue
			}
		}
		if final.StatusCode >= 300 {
			u.ackNon2xx(c, final)
			u.end(c, causeText(final))
			return
		}
		u.answered(c, final)
		return
	}
	u.end(c, "authentication failed")
}

func (u *UA) nextCSeqLocked() uint32 {
	u.cseq++
	return u.cseq
}

func causeText(m *sip.Message) string {
	switch m.StatusCode {
	case 486, 600:
		return "busy"
	case 487:
		return "cancelled"
	case 404, 484:
		return "number not found"
	case 480, 408:
		return "not available"
	case 603:
		return "declined"
	case 403:
		return "not allowed"
	}
	return fmt.Sprintf("%d %s", m.StatusCode, m.Reason)
}

func (u *UA) ackNon2xx(c *call, resp *sip.Message) {
	u.mu.Lock()
	inv := c.invite
	u.mu.Unlock()
	ack := sip.NewRequest("ACK", inv.RequestURI)
	ack.Add("Via", inv.Get("Via"))
	ack.Add("From", inv.Get("From"))
	ack.Add("To", resp.Get("To"))
	ack.Add("Call-ID", c.callID)
	seq, _ := inv.CSeq()
	ack.Add("CSeq", fmt.Sprintf("%d ACK", seq))
	ack.Add("Max-Forwards", "70")
	_ = u.send(ack)
}

func (u *UA) answered(c *call, final *sip.Message) {
	u.mu.Lock()
	if to, err := final.To(); err == nil {
		c.remoteTag = to.Tag()
	}
	if ct, err := final.Contact(); err == nil {
		c.target = ct.URI
	}
	if final.HasSDP() {
		c.applyRemoteSDP(final.Body)
	}
	ack := u.inDialogLocked(c, "ACK", c.cseq)
	ack.Prepend("Via", fmt.Sprintf("SIP/2.0/UDP %s;branch=%s;rport", u.local, sip.NewBranch()))
	ack.Add("Max-Forwards", "70")
	c.ack = ack
	cancelled := c.cancelled || c.info.State == StateEnded
	if !cancelled {
		c.info.State, c.info.Answered = StateAnswered, time.Now().Unix()
	}
	u.mu.Unlock()
	_ = u.send(ack)
	if cancelled {
		_ = u.bye(c)
		return
	}
	u.update(c)
}

func (u *UA) inDialogLocked(c *call, method string, seq uint32) *sip.Message {
	target := c.target
	if target == nil {
		if c.invite != nil && c.info.Direction == "out" {
			target = c.invite.RequestURI
		} else {
			target = &sip.URI{Scheme: "sip", Host: u.cfg.Server.Addr().String(), Port: int(u.cfg.Server.Port())}
		}
	}
	req := sip.NewRequest(method, target)
	req.Add("From", c.localURI+";tag="+c.localTag)
	to := c.remoteURI
	if c.remoteTag != "" {
		to += ";tag=" + c.remoteTag
	}
	req.Add("To", to)
	req.Add("Call-ID", c.callID)
	req.Add("CSeq", fmt.Sprintf("%d %s", seq, method))
	return req
}

// inDialog sends an in-dialog request and waits for its final response,
// answering one authentication challenge.
func (u *UA) inDialog(c *call, method, contentType string, body []byte, headers ...string) (*sip.Message, error) {
	for attempt := 0; attempt < 2; attempt++ {
		u.mu.Lock()
		c.cseq = u.nextCSeqLocked()
		req := u.inDialogLocked(c, method, c.cseq)
		u.mu.Unlock()
		for i := 0; i+1 < len(headers); i += 2 {
			req.Add(headers[i], headers[i+1])
		}
		if contentType != "" {
			req.Add("Content-Type", contentType)
			req.Body = body
		}
		if method == "INVITE" {
			req.Add("Contact", u.contact())
		}
		ch, _ := u.request(req)
		resp, err := waitFinal(ch, 32*time.Second, nil)
		if err != nil {
			return nil, err
		}
		if method == "INVITE" {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				ack := u.inDialogLocked(c, "ACK", c.cseq)
				ack.Prepend("Via", fmt.Sprintf("SIP/2.0/UDP %s;branch=%s;rport", u.local, sip.NewBranch()))
				ack.Add("Max-Forwards", "70")
				u.mu.Lock()
				c.ack = ack
				u.mu.Unlock()
				_ = u.send(ack)
			} else if resp.StatusCode >= 300 {
				ack := sip.NewRequest("ACK", req.RequestURI)
				ack.Add("Via", req.Get("Via"))
				ack.Add("From", req.Get("From"))
				ack.Add("To", resp.Get("To"))
				ack.Add("Call-ID", c.callID)
				ack.Add("CSeq", fmt.Sprintf("%d ACK", c.cseq))
				ack.Add("Max-Forwards", "70")
				_ = u.send(ack)
			}
		}
		if (resp.StatusCode == 401 || resp.StatusCode == 407) && attempt == 0 {
			hdr, authHdr := "WWW-Authenticate", "Authorization"
			if resp.StatusCode == 407 {
				hdr, authHdr = "Proxy-Authenticate", "Proxy-Authorization"
			}
			if chal, ok := sip.ParseDigest(resp.Get(hdr)); ok {
				headers = append(headers, authHdr, sip.BuildAuthorization(chal, method, req.RequestURI.String(), u.cfg.User, u.cfg.Password()))
				continue
			}
		}
		return resp, nil
	}
	return nil, errors.New("authentication failed")
}

func (u *UA) cancel(c *call) {
	u.mu.Lock()
	inv := c.invite
	answered := c.info.State == StateAnswered
	u.mu.Unlock()
	if inv == nil || answered {
		return
	}
	req := sip.NewRequest("CANCEL", inv.RequestURI)
	req.Add("Via", inv.Get("Via"))
	req.Add("From", inv.Get("From"))
	req.Add("To", inv.Get("To"))
	req.Add("Call-ID", c.callID)
	seq, _ := inv.CSeq()
	req.Add("CSeq", fmt.Sprintf("%d CANCEL", seq))
	req.Add("Max-Forwards", "70")
	_ = u.send(req) // the 200 and the INVITE's 487 come back to the INVITE's waiter
}

func (u *UA) bye(c *call) error {
	resp, err := u.inDialog(c, "BYE", "", nil)
	u.end(c, "hung up")
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 && resp.StatusCode != 481 {
		return fmt.Errorf("BYE: %d", resp.StatusCode)
	}
	return nil
}

func (u *UA) end(c *call, cause string) {
	u.mu.Lock()
	if c.info.State == StateEnded {
		u.mu.Unlock()
		return
	}
	c.info.State, c.info.Cause = StateEnded, cause
	delete(u.calls, c.callID)
	u.mu.Unlock()
	c.rtp.Close()
	u.update(c)
}

func (u *UA) get(id string) (*call, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	c := u.calls[id]
	if c == nil {
		return nil, errors.New("no such call")
	}
	return c, nil
}

// Answer picks up an incoming call.
func (u *UA) Answer(id string) error {
	c, err := u.get(id)
	if err != nil {
		return err
	}
	u.mu.Lock()
	if c.info.Direction != "in" || c.info.State != StateRinging {
		u.mu.Unlock()
		return errors.New("the call is not ringing")
	}
	body := u.localSDP(c, "sendrecv", c.offer)
	c.info.State, c.info.Answered = StateAnswered, time.Now().Unix()
	inv, tag := c.invite, c.localTag
	u.mu.Unlock()
	resp := u.respond(inv, 200, tag, body)
	u.mu.Lock()
	c.inviteResp = resp
	u.mu.Unlock()
	go func() { // retransmit the 200 until the ACK (timer G)
		interval := 500 * time.Millisecond
		for i := 0; i < 10; i++ {
			select {
			case <-c.acked:
				return
			case <-u.done:
				return
			case <-time.After(interval):
				_ = u.send(resp)
				interval = min(interval*2, 4*time.Second)
			}
		}
	}()
	u.update(c)
	return nil
}

// Hangup ends a call in any state: cancels, declines or hangs up.
func (u *UA) Hangup(id string) error {
	c, err := u.get(id)
	if err != nil {
		return err
	}
	u.mu.Lock()
	state, dir := c.info.State, c.info.Direction
	inv, tag := c.invite, c.localTag
	u.mu.Unlock()
	switch {
	case state == StateAnswered:
		return u.bye(c)
	case dir == "in":
		resp := u.respond(inv, 603, tag, nil)
		u.mu.Lock()
		c.inviteResp = resp
		u.mu.Unlock()
		u.end(c, "declined")
	default:
		u.mu.Lock()
		c.cancelled = true
		u.mu.Unlock()
		u.cancel(c)
	}
	return nil
}

// Hold puts a call on hold (the other side hears music) or takes it off.
func (u *UA) Hold(id string, on bool) error {
	c, err := u.get(id)
	if err != nil {
		return err
	}
	u.mu.Lock()
	if c.info.State != StateAnswered {
		u.mu.Unlock()
		return errors.New("the call is not connected")
	}
	dir := "sendrecv"
	if on {
		dir = "sendonly"
	}
	body := u.localSDP(c, dir, nil)
	u.mu.Unlock()
	resp, err := u.inDialog(c, "INVITE", "application/sdp", body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the switch refused: %d", resp.StatusCode)
	}
	u.mu.Lock()
	c.info.Held = on
	if resp.HasSDP() {
		c.applyRemoteSDP(resp.Body)
	}
	u.mu.Unlock()
	u.update(c)
	return nil
}

// DTMF sends key presses (SIP INFO).
func (u *UA) DTMF(id, digits string) error {
	c, err := u.get(id)
	if err != nil {
		return err
	}
	for _, d := range digits {
		if !strings.ContainsRune("0123456789*#ABCD", d) {
			continue
		}
		body := []byte(fmt.Sprintf("Signal=%c\r\nDuration=160\r\n", d))
		if _, err := u.inDialog(c, "INFO", "application/dtmf-relay", body); err != nil {
			return err
		}
	}
	return nil
}

// Transfer blind-transfers the other party of a call to a number.
func (u *UA) Transfer(id, number string) error {
	c, err := u.get(id)
	if err != nil {
		return err
	}
	resp, err := u.inDialog(c, "REFER", "", nil, "Refer-To", fmt.Sprintf("<sip:%s@%s>", number, u.cfg.Domain), "Referred-By", u.aor())
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("transfer refused: %d %s", resp.StatusCode, resp.Reason)
	}
	return nil
}

// AttendedTransfer connects the other party of call id with the other
// party of call consult (after talking to them), then ends both calls.
func (u *UA) AttendedTransfer(id, consult string) error {
	c, err := u.get(id)
	if err != nil {
		return err
	}
	k, err := u.get(consult)
	if err != nil {
		return err
	}
	u.mu.Lock()
	rep := fmt.Sprintf("%s;to-tag=%s;from-tag=%s", k.callID, k.remoteTag, k.localTag)
	if k.info.Direction == "in" {
		rep = fmt.Sprintf("%s;to-tag=%s;from-tag=%s", k.callID, k.localTag, k.remoteTag)
	}
	number := k.info.Number
	u.mu.Unlock()
	referTo := fmt.Sprintf("<sip:%s@%s?Replaces=%s>", number, u.cfg.Domain, strings.NewReplacer(";", "%3B", "=", "%3D", "@", "%40").Replace(rep))
	resp, err := u.inDialog(c, "REFER", "", nil, "Refer-To", referTo, "Referred-By", u.aor())
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("transfer refused: %d %s", resp.StatusCode, resp.Reason)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Requests from the switch

func (u *UA) handleRequest(req *sip.Message) {
	if req.Method == "ACK" {
		u.mu.Lock()
		c := u.calls[req.CallID()]
		u.mu.Unlock()
		if c != nil {
			select {
			case <-c.acked:
			default:
				close(c.acked)
			}
		}
		return
	}
	u.mu.Lock()
	c := u.calls[req.CallID()]
	closed := u.closed
	u.mu.Unlock()
	seq, _ := req.CSeq()
	switch req.Method {
	case "INVITE":
		if c == nil {
			if closed {
				u.respond(req, 480, sip.NewTag(), nil)
				return
			}
			u.incoming(req)
			return
		}
		u.mu.Lock()
		if seq <= c.remoteCSeq && c.inviteResp != nil {
			resp := c.inviteResp // retransmission
			u.mu.Unlock()
			_ = u.send(resp)
			return
		}
		u.mu.Unlock()
		u.reinvite(c, req)
	case "CANCEL":
		u.respond(req, 200, "", nil)
		if c != nil {
			u.mu.Lock()
			ringing := c.info.State == StateRinging && c.info.Direction == "in"
			inv, tag := c.invite, c.localTag
			u.mu.Unlock()
			if ringing {
				resp := u.respond(inv, 487, tag, nil)
				u.mu.Lock()
				c.inviteResp = resp
				u.mu.Unlock()
				cause := "missed"
				if r := req.Get("Reason"); strings.Contains(r, "cause=200") || strings.Contains(strings.ToLower(r), "completed elsewhere") {
					cause = "answered elsewhere"
				}
				u.end(c, cause)
			}
		}
	case "BYE":
		if c == nil {
			u.respond(req, 481, "", nil)
			return
		}
		u.respond(req, 200, "", nil)
		u.end(c, "the other side hung up")
	case "NOTIFY":
		u.respond(req, 200, "", nil)
		if strings.HasPrefix(strings.ToLower(req.Get("Event")), "message-summary") {
			newMsgs, oldMsgs := parseMWI(string(req.Body))
			u.h.MWI(newMsgs, oldMsgs)
		}
	case "OPTIONS", "INFO", "UPDATE", "MESSAGE":
		u.respond(req, 200, "", nil)
	default:
		u.respond(req, 501, "", nil)
	}
}

func parseMWI(body string) (int, int) {
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "Voice-Message") {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.IndexByte(v, ' '); i > 0 {
			v = v[:i]
		}
		n, o, _ := strings.Cut(v, "/")
		nn, _ := strconv.Atoi(n)
		oo, _ := strconv.Atoi(o)
		return nn, oo
	}
	return 0, 0
}

func (u *UA) incoming(req *sip.Message) {
	from, err := req.From()
	if err != nil {
		u.respond(req, 400, "", nil)
		return
	}
	to, _ := req.To()
	number := ""
	if from.URI != nil {
		number = from.URI.User
	}
	if v := req.Get("P-Asserted-Identity"); v != "" {
		if a, err := sip.ParseAddress(v); err == nil && a.URI != nil && a.URI.User != "" {
			number = a.URI.User
		}
	}
	c, err := u.newCall(req.CallID(), "in", number, from.Display)
	if err != nil {
		u.respond(req, 500, "", nil)
		return
	}
	c.invite, c.localTag, c.remoteTag = req, sip.NewTag(), from.Tag()
	c.info.AutoAnswer = strings.Contains(req.Get("Call-Info"), "answer-after=0") || strings.Contains(strings.ToLower(req.Get("Alert-Info")), "auto")
	if ct, err := req.Contact(); err == nil {
		c.target = ct.URI
	}
	fu := from.Clone()
	fu.Params = nil
	c.remoteURI = fu.String()
	if to != nil {
		tu := to.Clone()
		tu.Params = nil
		c.localURI = tu.String()
	} else {
		c.localURI = u.aor()
	}
	c.remoteCSeq, _ = req.CSeq()
	if req.HasSDP() {
		c.applyRemoteSDP(req.Body)
		c.offer, _ = sdp.Parse(req.Body)
	}
	c.info.State = StateRinging
	u.mu.Lock()
	u.calls[c.callID] = c
	u.mu.Unlock()
	u.respond(req, 100, "", nil)
	resp := u.respond(req, 180, c.localTag, nil)
	u.mu.Lock()
	c.inviteResp = resp
	u.mu.Unlock()
	go u.rtpReader(c)
	u.update(c)
}

// reinvite answers an in-dialog INVITE (the switch putting us on hold,
// moving the media after a transfer...).
func (u *UA) reinvite(c *call, req *sip.Message) {
	u.mu.Lock()
	c.remoteCSeq, _ = req.CSeq()
	dir := "sendrecv"
	if c.info.Held {
		dir = "sendonly"
	}
	var answerTo *sdp.Session
	if req.HasSDP() {
		remoteDir := c.applyRemoteSDP(req.Body)
		c.info.RemoteHold = remoteDir == "sendonly" || remoteDir == "inactive"
		switch remoteDir {
		case "sendonly":
			dir = "recvonly"
		case "inactive":
			dir = "inactive"
		}
		answerTo, _ = sdp.Parse(req.Body)
	}
	var body []byte
	if answerTo != nil {
		body = u.localSDP(c, dir, answerTo)
	} else {
		body = u.localSDP(c, dir, nil)
	}
	if ct, err := req.Contact(); err == nil {
		c.target = ct.URI
	}
	tag := c.localTag
	c.acked = make(chan struct{})
	u.mu.Unlock()
	resp := u.respond(req, 200, tag, body)
	u.mu.Lock()
	c.inviteResp = resp
	u.mu.Unlock()
	u.update(c)
}
