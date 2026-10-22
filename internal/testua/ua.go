// Package testua is a small scriptable SIP user agent used by integration
// tests. It deliberately works at the message level (no transaction layer)
// so tests can observe and control every request and response.
package testua

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// UA is a UDP SIP phone.
type UA struct {
	User, Password string
	Domain         string // host of the AOR ("" = the server address)
	ContactParams  string // appended to the contact URI, e.g. ";rinstance=x"
	Server         netip.AddrPort
	conn           *net.UDPConn
	Local          netip.AddrPort

	mu        sync.Mutex
	waiters   map[string]chan *sip.Message // by Via branch
	requests  chan *sip.Message
	cseq      uint32
	regCallID string
	regTag    string
}

// New creates a UA bound to a random loopback port.
func New(user, password string, server netip.AddrPort) (*UA, error) {
	return NewAt(user, password, server, netip.MustParseAddr("127.0.0.1"))
}

// NewAt creates a UA bound to a random port of the given local address
// (another loopback address can stand for a carrier).
func NewAt(user, password string, server netip.AddrPort, local netip.Addr) (*UA, error) {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: local.AsSlice()})
	if err != nil {
		return nil, err
	}
	u := &UA{
		User: user, Password: password, Server: server, conn: c,
		Local:   c.LocalAddr().(*net.UDPAddr).AddrPort(),
		waiters: map[string]chan *sip.Message{}, requests: make(chan *sip.Message, 64),
		regCallID: sip.NewCallID(), regTag: sip.NewTag(),
	}
	go u.loop()
	return u, nil
}

// Close releases the socket.
func (u *UA) Close() { u.conn.Close() }

func (u *UA) loop() {
	buf := make([]byte, 65535)
	for {
		n, from, err := u.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		m, err := sip.Parse(buf[:n])
		if err != nil {
			continue
		}
		m.Src = sip.Addr{Transport: "udp", AP: netip.AddrPortFrom(from.Addr().Unmap(), from.Port())}
		if m.IsRequest() {
			select {
			case u.requests <- m:
			default:
			}
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
		}
	}
}

// Send transmits a message to the server.
func (u *UA) Send(m *sip.Message) error {
	_, err := u.conn.WriteToUDPAddrPort(m.Bytes(), u.Server)
	return err
}

func (u *UA) nextCSeq() uint32 {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cseq++
	return u.cseq
}

func (u *UA) host() string {
	if u.Domain != "" {
		return u.Domain
	}
	return u.Server.Addr().String()
}

// ContactURI is this UA's contact.
func (u *UA) ContactURI() string {
	return fmt.Sprintf("sip:%s@%s%s", u.User, u.Local, u.ContactParams)
}

func (u *UA) via(branch string) string {
	return fmt.Sprintf("SIP/2.0/UDP %s;branch=%s;rport", u.Local, branch)
}

// Request sends req with a fresh Via and returns a channel of responses.
func (u *UA) Request(req *sip.Message) (<-chan *sip.Message, string, error) {
	branch := sip.NewBranch()
	req.Prepend("Via", u.via(branch))
	if req.Get("Max-Forwards") == "" {
		req.Add("Max-Forwards", "70")
	}
	ch := make(chan *sip.Message, 16)
	u.mu.Lock()
	u.waiters[branch] = ch
	u.mu.Unlock()
	return ch, branch, u.Send(req)
}

// WaitFinal waits for a final response, collecting provisional codes.
func WaitFinal(ch <-chan *sip.Message, timeout time.Duration) (*sip.Message, []int, error) {
	var prov []int
	deadline := time.After(timeout)
	for {
		select {
		case m := <-ch:
			if m.StatusCode < 200 {
				prov = append(prov, m.StatusCode)
				continue
			}
			return m, prov, nil
		case <-deadline:
			return nil, prov, errors.New("timeout waiting for final response")
		}
	}
}

func (u *UA) authorize(req *sip.Message, resp *sip.Message) error {
	hdr, authHdr := "WWW-Authenticate", "Authorization"
	if resp.StatusCode == 407 {
		hdr, authHdr = "Proxy-Authenticate", "Proxy-Authorization"
	}
	chal, ok := sip.ParseDigest(resp.Get(hdr))
	if !ok {
		return fmt.Errorf("bad challenge %q", resp.Get(hdr))
	}
	req.Set(authHdr, sip.BuildAuthorization(chal, req.Method, req.RequestURI.String(), u.User, u.Password))
	return nil
}

// Register registers (expires > 0) or unregisters (expires = 0).
func (u *UA) Register(expires int) (*sip.Message, error) {
	build := func() *sip.Message {
		req := sip.NewRequest("REGISTER", &sip.URI{Scheme: "sip", Host: u.host()})
		aor := fmt.Sprintf("<sip:%s@%s>", u.User, u.host())
		req.Add("From", aor+";tag="+u.regTag)
		req.Add("To", aor)
		req.Add("Call-ID", u.regCallID)
		req.Add("CSeq", fmt.Sprintf("%d REGISTER", u.nextCSeq()))
		req.Add("Contact", "<"+u.ContactURI()+">")
		req.Add("Expires", strconv.Itoa(expires))
		req.Add("User-Agent", "testua")
		return req
	}
	req := build()
	ch, _, err := u.Request(req.Clone())
	if err != nil {
		return nil, err
	}
	resp, _, err := WaitFinal(ch, 5*time.Second)
	if err != nil || (resp.StatusCode != 401 && resp.StatusCode != 407) {
		return resp, err
	}
	req2 := build()
	if err := u.authorize(req2, resp); err != nil {
		return nil, err
	}
	ch, _, err = u.Request(req2)
	if err != nil {
		return nil, err
	}
	resp, _, err = WaitFinal(ch, 5*time.Second)
	return resp, err
}

// Subscription is a SUBSCRIBE dialog of the UA.
type Subscription struct {
	UA       *UA
	Resource string
	Event    string
	CallID   string
	FromTag  string
	ToTag    string
	cseq     uint32
}

func (s *Subscription) build(expires int, body []byte, ctype string) *sip.Message {
	req := sip.NewRequest("SUBSCRIBE", &sip.URI{Scheme: "sip", User: s.Resource, Host: s.UA.host()})
	req.Add("From", fmt.Sprintf("<sip:%s@%s>;tag=%s", s.UA.User, s.UA.host(), s.FromTag))
	to := fmt.Sprintf("<sip:%s@%s>", s.Resource, s.UA.host())
	if s.ToTag != "" {
		to += ";tag=" + s.ToTag
	}
	req.Add("To", to)
	req.Add("Call-ID", s.CallID)
	s.cseq++
	req.Add("CSeq", fmt.Sprintf("%d SUBSCRIBE", s.cseq))
	req.Add("Contact", "<"+s.UA.ContactURI()+">")
	req.Add("Event", s.Event)
	req.Add("Expires", strconv.Itoa(expires))
	if body != nil {
		req.Add("Content-Type", ctype)
		req.Body = body
	}
	return req
}

func (s *Subscription) send(expires int, body []byte, ctype string) (*sip.Message, error) {
	ch, _, err := s.UA.Request(s.build(expires, body, ctype))
	if err != nil {
		return nil, err
	}
	resp, _, err := WaitFinal(ch, 5*time.Second)
	if err != nil || (resp.StatusCode != 401 && resp.StatusCode != 407) {
		return resp, err
	}
	req := s.build(expires, body, ctype)
	if err := s.UA.authorize(req, resp); err != nil {
		return nil, err
	}
	if ch, _, err = s.UA.Request(req); err != nil {
		return nil, err
	}
	resp, _, err = WaitFinal(ch, 5*time.Second)
	return resp, err
}

// Subscribe starts a subscription to an event of a resource (a user part:
// an extension, a park slot...), answering the digest challenge.
func (u *UA) Subscribe(resource, event string, expires int) (*Subscription, *sip.Message, error) {
	s := &Subscription{UA: u, Resource: resource, Event: event, CallID: sip.NewCallID(), FromTag: sip.NewTag()}
	resp, err := s.send(expires, nil, "")
	if err == nil && resp.StatusCode < 300 {
		if to, e := resp.To(); e == nil {
			s.ToTag = to.Tag()
		}
	}
	return s, resp, err
}

// Refresh sends the subscription again (expires 0 ends it), with a body
// when given.
func (s *Subscription) Refresh(expires int, body []byte, ctype string) (*sip.Message, error) {
	return s.send(expires, body, ctype)
}

// Call is a call placed by or to the UA.
type Call struct {
	UA        *UA
	CallID    string
	FromTag   string
	ToTag     string
	LocalURI  string
	RemoteURI string
	Target    *sip.URI
	CSeq      uint32
	Branch    string
	Invite    *sip.Message
	RTP       *net.UDPConn
	RemoteRTP netip.AddrPort
	Final     *sip.Message
	Prov      []int
	Responses <-chan *sip.Message
}

// NewRTP opens an RTP socket.
func NewRTP() (*net.UDPConn, error) {
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
}

// SDP builds a simple PCMU/PCMA offer or answer for port.
func SDP(port int, codecs ...int) []byte {
	if len(codecs) == 0 {
		codecs = []int{0, 8}
	}
	var fmts []string
	var maps []string
	for _, c := range codecs {
		fmts = append(fmts, strconv.Itoa(c))
		switch c {
		case 0:
			maps = append(maps, "a=rtpmap:0 PCMU/8000")
		case 8:
			maps = append(maps, "a=rtpmap:8 PCMA/8000")
		case 9:
			maps = append(maps, "a=rtpmap:9 G722/8000")
		}
	}
	fmts = append(fmts, "101")
	maps = append(maps, "a=rtpmap:101 telephone-event/8000", "a=fmtp:101 0-16")
	return []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(port) + " RTP/AVP " + strings.Join(fmts, " ") + "\r\n" +
		strings.Join(maps, "\r\n") + "\r\na=ptime:20\r\na=sendrecv\r\n")
}

// SDPAddr extracts the audio address from an SDP body.
func SDPAddr(body []byte) (netip.AddrPort, []string, error) {
	var ip string
	var port int
	var fmts []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "c=IN IP4 ") {
			ip = strings.TrimPrefix(line, "c=IN IP4 ")
		}
		if strings.HasPrefix(line, "m=audio ") {
			f := strings.Fields(line)
			port, _ = strconv.Atoi(f[1])
			fmts = f[3:]
		}
	}
	a, err := netip.ParseAddr(ip)
	if err != nil || port == 0 {
		return netip.AddrPort{}, nil, fmt.Errorf("no audio in sdp: %q", body)
	}
	return netip.AddrPortFrom(a, uint16(port)), fmts, nil
}

// Dial sends an INVITE to dest (handling a 407 challenge) and waits for the
// final response. With early=true it returns as soon as a response >= 180
// arrives so the test can CANCEL.
func (u *UA) Dial(dest string, codecs []int, timeout time.Duration, early bool) (*Call, error) {
	rtp, err := NewRTP()
	if err != nil {
		return nil, err
	}
	c := &Call{UA: u, CallID: sip.NewCallID(), FromTag: sip.NewTag(), RTP: rtp,
		LocalURI:  fmt.Sprintf("<sip:%s@%s>", u.User, u.host()),
		RemoteURI: fmt.Sprintf("<sip:%s@%s>", dest, u.host())}
	port := rtp.LocalAddr().(*net.UDPAddr).Port
	for attempt := 0; attempt < 2; attempt++ {
		c.CSeq = u.nextCSeq()
		req := sip.NewRequest("INVITE", &sip.URI{Scheme: "sip", User: dest, Host: u.Server.Addr().String(), Port: int(u.Server.Port())})
		req.Add("From", `"Test `+u.User+`" `+c.LocalURI+";tag="+c.FromTag)
		req.Add("To", c.RemoteURI)
		req.Add("Call-ID", c.CallID)
		req.Add("CSeq", fmt.Sprintf("%d INVITE", c.CSeq))
		req.Add("Contact", "<"+u.ContactURI()+">")
		req.Add("Content-Type", "application/sdp")
		req.Body = SDP(port, codecs...)
		if attempt == 1 {
			if err := u.authorize(req, c.Final); err != nil {
				return nil, err
			}
		}
		c.Invite = req
		ch, branch, err := u.Request(req)
		if err != nil {
			return nil, err
		}
		c.Branch, c.Responses = branch, ch
		if early {
			deadline := time.After(timeout)
		waitEarly:
			for {
				select {
				case m := <-ch:
					if m.StatusCode < 180 {
						continue
					}
					if m.StatusCode < 200 {
						c.Prov = append(c.Prov, m.StatusCode)
						return c, nil
					}
					c.Final = m
					break waitEarly
				case <-deadline:
					return nil, errors.New("timeout waiting for ringing")
				}
			}
		} else {
			final, prov, err := WaitFinal(ch, timeout)
			if err != nil {
				return nil, err
			}
			c.Final, c.Prov = final, prov
		}
		if c.Final.StatusCode == 407 && attempt == 0 {
			c.ackNon2xx(c.Final)
			continue
		}
		break
	}
	return c, c.complete()
}

// complete processes the final response of the INVITE.
func (c *Call) complete() error {
	code := c.Final.StatusCode
	if code >= 300 {
		c.ackNon2xx(c.Final)
		return nil
	}
	to, err := c.Final.To()
	if err != nil {
		return err
	}
	c.ToTag = to.Tag()
	if ct, err := c.Final.Contact(); err == nil {
		c.Target = ct.URI
	}
	if c.Final.HasSDP() {
		c.RemoteRTP, _, err = SDPAddr(c.Final.Body)
		if err != nil {
			return err
		}
	}
	return c.Ack()
}

// WaitFinal waits for the final response after an early Dial.
func (c *Call) WaitFinal(timeout time.Duration) error {
	final, prov, err := WaitFinal(c.Responses, timeout)
	if err != nil {
		return err
	}
	c.Final = final
	c.Prov = append(c.Prov, prov...)
	return c.complete()
}

func (c *Call) ackNon2xx(resp *sip.Message) {
	ack := sip.NewRequest("ACK", c.Invite.RequestURI)
	ack.Add("Via", c.Invite.Get("Via"))
	ack.Add("From", c.Invite.Get("From"))
	ack.Add("To", resp.Get("To"))
	ack.Add("Call-ID", c.CallID)
	ack.Add("CSeq", fmt.Sprintf("%d ACK", c.CSeq))
	ack.Add("Max-Forwards", "70")
	_ = c.UA.Send(ack)
}

func (c *Call) inDialog(method string, seq uint32) *sip.Message {
	target := c.Target
	if target == nil {
		target = c.Invite.RequestURI
	}
	req := sip.NewRequest(method, target)
	req.Add("From", `"Test `+c.UA.User+`" `+c.LocalURI+";tag="+c.FromTag)
	req.Add("To", c.RemoteURI+";tag="+c.ToTag)
	req.Add("Call-ID", c.CallID)
	req.Add("CSeq", fmt.Sprintf("%d %s", seq, method))
	return req
}

// Ack acknowledges a 2xx.
func (c *Call) Ack() error {
	ack := c.inDialog("ACK", c.CSeq)
	ack.Prepend("Via", c.UA.via(sip.NewBranch()))
	ack.Add("Max-Forwards", "70")
	return c.UA.Send(ack)
}

// AckFor acknowledges the 2xx to an in-dialog INVITE (a re-INVITE).
func (c *Call) AckFor(resp *sip.Message) error {
	seq, _ := resp.CSeq()
	ack := c.inDialog("ACK", seq)
	ack.Prepend("Via", c.UA.via(sip.NewBranch()))
	ack.Add("Max-Forwards", "70")
	return c.UA.Send(ack)
}

// Bye hangs up and waits for the response.
func (c *Call) Bye() (*sip.Message, error) {
	return c.Request("BYE", "", nil)
}

// Request sends an in-dialog request and waits for its final response.
func (c *Call) Request(method, contentType string, body []byte, headers ...string) (*sip.Message, error) {
	req := c.inDialog(method, c.UA.nextCSeq())
	for i := 0; i+1 < len(headers); i += 2 {
		req.Add(headers[i], headers[i+1])
	}
	if contentType != "" {
		req.Add("Content-Type", contentType)
		req.Body = body
	}
	ch, _, err := c.UA.Request(req)
	if err != nil {
		return nil, err
	}
	resp, _, err := WaitFinal(ch, 5*time.Second)
	return resp, err
}

// Cancel cancels a pending INVITE.
func (c *Call) Cancel() (*sip.Message, error) {
	req := sip.NewRequest("CANCEL", c.Invite.RequestURI)
	req.Add("Via", c.Invite.Get("Via"))
	req.Add("From", c.Invite.Get("From"))
	req.Add("To", c.Invite.Get("To"))
	req.Add("Call-ID", c.CallID)
	req.Add("CSeq", fmt.Sprintf("%d CANCEL", c.CSeq))
	req.Add("Max-Forwards", "70")
	ch := make(chan *sip.Message, 4)
	c.UA.mu.Lock()
	// CANCEL shares the INVITE branch; route both through one waiter.
	orig := c.UA.waiters[c.Branch]
	merged := make(chan *sip.Message, 16)
	c.UA.waiters[c.Branch] = merged
	c.UA.mu.Unlock()
	go func() {
		for m := range merged {
			if _, method := m.CSeq(); method == "CANCEL" {
				ch <- m
			} else if orig != nil {
				select {
				case orig <- m:
				default:
				}
			}
		}
	}()
	if err := c.UA.Send(req); err != nil {
		return nil, err
	}
	resp, _, err := WaitFinal(ch, 5*time.Second)
	return resp, err
}

// WaitRequest returns the next received request with the given method.
func (u *UA) WaitRequest(method string, timeout time.Duration) (*sip.Message, error) {
	deadline := time.After(timeout)
	for {
		select {
		case m := <-u.requests:
			if m.Method == method {
				return m, nil
			}
			if m.Method != "ACK" && m.Method != "OPTIONS" && method != "ACK" {
				// Answer anything unexpected so the server does not retransmit.
				_ = u.Respond(m, 200, "", nil, "")
			}
		case <-deadline:
			return nil, fmt.Errorf("timeout waiting for %s", method)
		}
	}
}

// Respond answers a received request.
func (u *UA) Respond(req *sip.Message, code int, toTag string, body []byte, contentType string) error {
	resp := sip.NewResponse(req, code, "")
	if toTag != "" {
		resp.SetToTag(toTag)
	}
	if code >= 180 && code < 300 {
		resp.Add("Contact", "<"+u.ContactURI()+">")
	}
	if body != nil {
		resp.Add("Content-Type", contentType)
		resp.Body = body
	}
	_, err := u.conn.WriteToUDPAddrPort(resp.Bytes(), u.Server)
	return err
}

// Incoming is a call received by the UA.
type Incoming struct {
	UA     *UA
	Invite *sip.Message
	ToTag  string
	RTP    *net.UDPConn
	Remote netip.AddrPort
	Codecs []string
}

// WaitInvite waits for an incoming INVITE.
func (u *UA) WaitInvite(timeout time.Duration) (*Incoming, error) {
	inv, err := u.WaitRequest("INVITE", timeout)
	if err != nil {
		return nil, err
	}
	rtp, err := NewRTP()
	if err != nil {
		return nil, err
	}
	in := &Incoming{UA: u, Invite: inv, ToTag: sip.NewTag(), RTP: rtp}
	if inv.HasSDP() {
		in.Remote, in.Codecs, err = SDPAddr(inv.Body)
		if err != nil {
			return nil, err
		}
	}
	return in, nil
}

// Ring sends 180.
func (in *Incoming) Ring() error { return in.UA.Respond(in.Invite, 180, in.ToTag, nil, "") }

// Reject sends a final error response and waits for the ACK.
func (in *Incoming) Reject(code int) error {
	if err := in.UA.Respond(in.Invite, code, in.ToTag, nil, ""); err != nil {
		return err
	}
	_, err := in.UA.WaitRequest("ACK", 5*time.Second)
	return err
}

// Answer sends 200 OK with SDP and waits for the ACK.
func (in *Incoming) Answer(codecs ...int) error {
	port := in.RTP.LocalAddr().(*net.UDPAddr).Port
	if err := in.UA.Respond(in.Invite, 200, in.ToTag, SDP(port, codecs...), "application/sdp"); err != nil {
		return err
	}
	_, err := in.UA.WaitRequest("ACK", 5*time.Second)
	return err
}

// Dialog returns a Call handle for sending in-dialog requests on an
// answered incoming call.
func (in *Incoming) Dialog() *Call {
	from, _ := in.Invite.From()
	to, _ := in.Invite.To()
	c := &Call{UA: in.UA, CallID: in.Invite.CallID(), FromTag: in.ToTag, ToTag: from.Tag(),
		RTP: in.RTP, RemoteRTP: in.Remote, Invite: in.Invite}
	toURI := to.Clone()
	toURI.Params = nil
	fromURI := from.Clone()
	fromURI.Params = nil
	c.LocalURI, c.RemoteURI = toURI.String(), fromURI.String()
	if ct, err := in.Invite.Contact(); err == nil {
		c.Target = ct.URI
	}
	return c
}

// SendRTP sends one RTP packet.
func SendRTP(conn *net.UDPConn, dst netip.AddrPort, pt uint8, seq uint16, ts uint32, payload []byte) error {
	b := make([]byte, 12+len(payload))
	b[0] = 0x80
	b[1] = pt
	binary.BigEndian.PutUint16(b[2:], seq)
	binary.BigEndian.PutUint32(b[4:], ts)
	binary.BigEndian.PutUint32(b[8:], 0xCAFE)
	copy(b[12:], payload)
	_, err := conn.WriteToUDPAddrPort(b, dst)
	return err
}

// RecvRTP waits for an RTP packet whose payload satisfies match (nil = any).
func RecvRTP(conn *net.UDPConn, timeout time.Duration, match func(pt uint8, payload []byte) bool) (uint8, []byte, error) {
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 2048)
	for {
		_ = conn.SetReadDeadline(deadline)
		n, err := conn.Read(buf)
		if err != nil {
			return 0, nil, err
		}
		if n < 12 {
			continue
		}
		pt, payload := buf[1]&0x7F, append([]byte(nil), buf[12:n]...)
		if match == nil || match(pt, payload) {
			return pt, payload, nil
		}
	}
}
