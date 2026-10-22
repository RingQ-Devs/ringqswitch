package sip

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Handler receives messages from the transaction layer.
type Handler interface {
	// OnRequest is called for every new request except ACK.
	OnRequest(tx *ServerTx)
	// OnAck is called for ACKs that do not belong to a server transaction
	// (i.e. ACKs for 2xx responses).
	OnAck(req *Message)
	// OnStrayResponse is called for responses matching no client transaction.
	OnStrayResponse(resp *Message)
}

// Config configures a Stack.
type Config struct {
	Transport TransportConfig
	// Advertise returns the host and port to put in Via/Contact headers for
	// messages sent to dst.
	Advertise func(dst Addr) (string, int)
	UserAgent string
	Logger    *slog.Logger
	// Accept is consulted for every received message; returning false drops
	// it silently (used for IP blocking).
	Accept func(src Addr) bool
	// ConnClosed is called when a TCP connection goes away.
	ConnClosed func(Addr)
	// TraceMessages logs every message sent and received at debug level.
	TraceMessages bool
	// T1 is the RTT estimate that drives retransmission timers (default 500ms).
	T1 time.Duration
}

// Stack is a SIP transport plus RFC 3261 transaction layer.
type Stack struct {
	stats   counters
	notice  noticer
	cfg     Config
	log     *slog.Logger
	tr      *transport
	handler Handler

	T1, T2, T4 time.Duration

	mu  sync.Mutex
	srv map[string]*ServerTx
	cli map[string]*ClientTx
}

// ErrTxTerminated is returned when responding on a finished transaction.
var ErrTxTerminated = errors.New("sip: transaction terminated")

// NewStack starts listening and dispatching messages to h.
func NewStack(cfg Config, h Handler) (*Stack, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.T1 <= 0 {
		cfg.T1 = 500 * time.Millisecond
	}
	s := &Stack{
		cfg: cfg, log: cfg.Logger, handler: h,
		T1: cfg.T1, T2: 4 * time.Second, T4: 5 * time.Second,
		srv: map[string]*ServerTx{}, cli: map[string]*ClientTx{},
	}
	tr, err := listenTransport(cfg.Transport, cfg.Logger, s.onMessage, cfg.ConnClosed, &s.stats, &s.notice)
	if err != nil {
		return nil, err
	}
	s.tr = tr
	tr.start()
	return s, nil
}

// Port returns the listening port.
func (s *Stack) Port() int { return s.tr.port() }

// Close stops the stack.
func (s *Stack) Close() { s.tr.close() }

// AddListener listens on another port too (a second SIP profile). Messages
// received there carry Addr.Local = port, and replies leave from it.
func (s *Stack) AddListener(ip string, port int, tcp bool) error {
	return s.tr.addListener(ip, port, tcp)
}

// RemoveListener stops listening on an extra port.
func (s *Stack) RemoveListener(port int) { s.tr.removeListener(uint16(port)) }

// Listening reports whether an extra port is open.
func (s *Stack) Listening(port int) bool { return s.tr.listening(uint16(port)) }

// Advertise returns the address to advertise towards dst.
func (s *Stack) Advertise(dst Addr) (string, int) {
	if s.cfg.Advertise != nil {
		return s.cfg.Advertise(dst)
	}
	return "127.0.0.1", s.Port()
}

func (s *Stack) send(m *Message, dst Addr) error {
	if s.cfg.UserAgent != "" {
		hdr := "User-Agent"
		if !m.IsRequest() {
			hdr = "Server"
		}
		if m.Get(hdr) == "" {
			m.Add(hdr, s.cfg.UserAgent)
		}
	}
	b := m.Bytes()
	if s.cfg.TraceMessages {
		s.log.Debug("sip send", "dst", dst.String(), "msg", "\n"+string(b))
	}
	if err := s.tr.send(b, dst); err != nil {
		s.stats.sendErrors.Add(1)
		if s.notice.allow("send|" + dst.String()) {
			s.log.Warn("sending SIP failed (logged once a minute per destination)", "dst", dst.String(), "msg", m.Short(), "err", err)
		}
		return err
	}
	s.stats.sent.Add(1)
	return nil
}

// Stats returns traffic counters.
func (s *Stack) Stats() Stats { return s.stats.snapshot() }

func (s *Stack) invalid(m *Message, why string) {
	s.stats.invalid.Add(1)
	if s.notice.allow("invalid|" + m.Src.AP.Addr().String()) {
		s.log.Warn("dropping invalid SIP message (logged once a minute per address)", "src", m.Src.String(), "reason", why, "msg", m.Short())
	}
}

// SendStateless sends a message outside any transaction (ACK for 2xx,
// responses to stray requests). Requests without a Via get a fresh one.
func (s *Stack) SendStateless(m *Message, dst Addr) error {
	if m.IsRequest() && m.Get("Via") == "" {
		m.Prepend("Via", s.newVia(dst).String())
		if m.Get("Max-Forwards") == "" {
			m.Add("Max-Forwards", "70")
		}
	}
	return s.send(m, dst)
}

func (s *Stack) newVia(dst Addr) *Via {
	host, port := s.Advertise(dst)
	return &Via{
		Transport: strings.ToUpper(dst.Transport), Host: host, Port: port,
		Params: Params{{Key: "branch", Value: NewBranch()}, {Key: "rport", Flag: true}},
	}
}

func (s *Stack) onMessage(m *Message) {
	if s.cfg.Accept != nil && !s.cfg.Accept(m.Src) {
		s.stats.filtered.Add(1)
		return
	}
	if s.cfg.TraceMessages {
		s.log.Debug("sip recv", "src", m.Src.String(), "msg", "\n"+string(m.Bytes()))
	}
	if m.Get("Call-ID") == "" || m.Get("CSeq") == "" || m.Get("From") == "" || m.Get("To") == "" || m.Get("Via") == "" {
		s.invalid(m, "missing Via, From, To, Call-ID or CSeq")
		return
	}
	if m.IsRequest() {
		s.onRequest(m)
	} else {
		s.onResponse(m)
	}
}

func serverTxKey(m *Message, via *Via) string {
	method := m.Method
	if method == "ACK" {
		method = "INVITE"
	}
	sentBy := via.Host + ":" + strconv.Itoa(via.Port)
	if br := via.Branch(); strings.HasPrefix(br, BranchMagic) {
		return br + "|" + sentBy + "|" + method
	}
	// RFC 2543 compatibility.
	from, _ := m.From()
	fromTag := ""
	if from != nil {
		fromTag = from.Tag()
	}
	seq, _ := m.CSeq()
	return fmt.Sprintf("2543|%s|%d|%s|%s|%s", m.CallID(), seq, fromTag, sentBy, method)
}

func (s *Stack) onRequest(m *Message) {
	via, err := m.TopVia()
	if err != nil {
		s.invalid(m, "bad Via: "+err.Error())
		return
	}
	// RFC 3261 18.2.1 / RFC 3581: record where the request really came from.
	src := m.Src.AP.Addr().String()
	changed := false
	if via.Host != src {
		via.Params.Set("received", src)
		changed = true
	}
	if via.Params.Has("rport") {
		via.Params.Set("rport", strconv.Itoa(int(m.Src.AP.Port())))
		changed = true
	}
	if changed {
		m.setFirst("Via", via.String())
	}
	key := serverTxKey(m, via)

	s.mu.Lock()
	tx := s.srv[key]
	if m.Method == "ACK" {
		s.mu.Unlock()
		if tx != nil && tx.handleAck() {
			return
		}
		s.handler.OnAck(m)
		return
	}
	if tx != nil {
		s.mu.Unlock()
		tx.handleRetransmit()
		return
	}
	tx = newServerTx(s, key, m)
	tx.dst = replyAddr(m.Src, via)
	s.srv[key] = tx
	s.mu.Unlock()
	go s.handler.OnRequest(tx)
}

// replyAddr is where responses to a request go (RFC 3261 18.2.2, RFC
// 3581): back over the connection for TCP; over UDP to the source port
// when the client asked for rport or is behind NAT (its Via names another
// address), else to the port its Via names, which is where it listens.
// Carriers often send from one port and listen on 5060: a reply to the
// source port never reaches them.
func replyAddr(src Addr, via *Via) Addr {
	if src.Reliable() || via.Params.Has("rport") {
		return src
	}
	if ip, err := netip.ParseAddr(via.Host); err == nil && ip.Unmap() != src.AP.Addr().Unmap() {
		return src
	}
	port := via.Port
	if port == 0 {
		port = 5060
	}
	if port == int(src.AP.Port()) || port > 65535 {
		return src
	}
	src.AP = netip.AddrPortFrom(src.AP.Addr(), uint16(port))
	return src
}

func (s *Stack) onResponse(m *Message) {
	via, err := m.TopVia()
	if err != nil {
		return
	}
	_, method := m.CSeq()
	key := via.Branch() + "|" + method
	s.mu.Lock()
	tx := s.cli[key]
	s.mu.Unlock()
	if tx == nil {
		go s.handler.OnStrayResponse(m)
		return
	}
	tx.handleResponse(m)
}

// FindInviteTx returns the INVITE server transaction a CANCEL refers to.
func (s *Stack) FindInviteTx(cancel *Message) *ServerTx {
	via, err := cancel.TopVia()
	if err != nil {
		return nil
	}
	c := cancel.Clone()
	c.Method = "INVITE"
	key := serverTxKey(c, via)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv[key]
}

func (s *Stack) removeServer(key string, tx *ServerTx) {
	s.mu.Lock()
	if s.srv[key] == tx {
		delete(s.srv, key)
	}
	s.mu.Unlock()
}

func (s *Stack) removeClient(key string, tx *ClientTx) {
	s.mu.Lock()
	if s.cli[key] == tx {
		delete(s.cli, key)
	}
	s.mu.Unlock()
}

// Counts returns the number of live server and client transactions.
func (s *Stack) Counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.srv), len(s.cli)
}

// ---------------------------------------------------------------------------
// Server transactions

type txState int

const (
	stTrying txState = iota
	stProceeding
	stCompleted
	stConfirmed
	stAccepted
	stTerminated
)

// ServerTx is an INVITE or non-INVITE server transaction.
type ServerTx struct {
	s       *Stack
	key     string
	Request *Message
	dst     Addr // where responses go

	mu       sync.Mutex
	state    txState
	invite   bool
	last     *Message
	interval time.Duration
	retrans  *time.Timer
	acked    bool
	onNoAck  func()
	done     chan struct{}
}

func newServerTx(s *Stack, key string, req *Message) *ServerTx {
	tx := &ServerTx{s: s, key: key, Request: req, dst: req.Src, invite: req.Method == "INVITE", done: make(chan struct{})}
	if !tx.invite {
		// Safety net in case the TU never answers.
		time.AfterFunc(64*s.T1+5*time.Second, func() {
			tx.mu.Lock()
			if tx.state < stCompleted {
				tx.terminateLocked()
			}
			tx.mu.Unlock()
		})
	}
	return tx
}

// Key returns the transaction key.
func (tx *ServerTx) Key() string { return tx.key }

// Done is closed when the transaction terminates.
func (tx *ServerTx) Done() <-chan struct{} { return tx.done }

// OnNoAck registers a callback fired when a 2xx to INVITE is never ACKed.
func (tx *ServerTx) OnNoAck(f func()) {
	tx.mu.Lock()
	tx.onNoAck = f
	tx.mu.Unlock()
}

// FinalSent reports whether a final response has been sent.
func (tx *ServerTx) FinalSent() bool {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.last != nil && tx.last.StatusCode >= 200
}

// Respond sends a response through the transaction.
func (tx *ServerTx) Respond(resp *Message) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	switch tx.state {
	case stTerminated, stCompleted, stConfirmed:
		return ErrTxTerminated
	case stAccepted:
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return ErrTxTerminated
		}
	}
	tx.last = resp
	err := tx.s.send(resp, tx.dst)
	code := resp.StatusCode
	reliable := tx.Request.Src.Reliable()
	switch {
	case code < 200:
		tx.state = stProceeding
	case !tx.invite:
		tx.state = stCompleted
		wait := 64 * tx.s.T1 // Timer J
		if reliable {
			wait = 0
		}
		tx.afterLocked(wait, tx.terminate)
	case code < 300:
		if tx.state == stAccepted {
			return err
		}
		tx.state = stAccepted
		if !reliable {
			tx.interval = tx.s.T1
			tx.retrans = time.AfterFunc(tx.interval, tx.retransmit2xx)
		}
		time.AfterFunc(64*tx.s.T1, func() {
			tx.mu.Lock()
			acked, cb := tx.acked, tx.onNoAck
			tx.terminateLocked()
			tx.mu.Unlock()
			if !acked && cb != nil {
				cb()
			}
		})
	default:
		tx.state = stCompleted
		if !reliable {
			tx.interval = tx.s.T1
			tx.retrans = time.AfterFunc(tx.interval, tx.retransmitFinal) // Timer G
		}
		time.AfterFunc(64*tx.s.T1, tx.terminate) // Timer H
	}
	return err
}

func (tx *ServerTx) afterLocked(d time.Duration, f func()) {
	if d <= 0 {
		tx.terminateLocked()
		return
	}
	time.AfterFunc(d, f)
}

func (tx *ServerTx) retransmit2xx() {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.state != stAccepted || tx.acked {
		return
	}
	_ = tx.s.send(tx.last, tx.dst)
	tx.interval = min(tx.interval*2, tx.s.T2)
	tx.retrans = time.AfterFunc(tx.interval, tx.retransmit2xx)
}

func (tx *ServerTx) retransmitFinal() {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.state != stCompleted {
		return
	}
	_ = tx.s.send(tx.last, tx.dst)
	tx.interval = min(tx.interval*2, tx.s.T2)
	tx.retrans = time.AfterFunc(tx.interval, tx.retransmitFinal)
}

// Acked tells the transaction the 2xx has been acknowledged (the ACK for a
// 2xx is matched by the dialog layer, not by the transaction).
func (tx *ServerTx) Acked() {
	tx.mu.Lock()
	tx.acked = true
	if tx.retrans != nil {
		tx.retrans.Stop()
	}
	tx.mu.Unlock()
}

// handleAck processes an ACK matching this transaction. It returns true when
// the ACK was absorbed (ACK for a non-2xx final response).
func (tx *ServerTx) handleAck() bool {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	switch tx.state {
	case stCompleted:
		tx.state = stConfirmed
		if tx.retrans != nil {
			tx.retrans.Stop()
		}
		wait := tx.s.T4 // Timer I
		if tx.Request.Src.Reliable() {
			wait = 0
		}
		tx.afterLocked(wait, tx.terminate)
		return true
	case stConfirmed, stTrying, stProceeding:
		return true
	}
	return false
}

func (tx *ServerTx) handleRetransmit() {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.last != nil && tx.state != stTerminated {
		_ = tx.s.send(tx.last, tx.dst)
	}
}

func (tx *ServerTx) terminate() {
	tx.mu.Lock()
	tx.terminateLocked()
	tx.mu.Unlock()
}

func (tx *ServerTx) terminateLocked() {
	if tx.state == stTerminated {
		return
	}
	tx.state = stTerminated
	if tx.retrans != nil {
		tx.retrans.Stop()
	}
	close(tx.done)
	go tx.s.removeServer(tx.key, tx)
}

// ---------------------------------------------------------------------------
// Client transactions

// ClientTx is an INVITE or non-INVITE client transaction.
type ClientTx struct {
	s       *Stack
	key     string
	Request *Message
	dst     Addr
	invite  bool

	mu        sync.Mutex
	state     txState
	interval  time.Duration
	retrans   *time.Timer
	timeout   *time.Timer
	ack       *Message
	responses chan *Message
	closed    bool
}

// Request sends req in a new client transaction. A Via header with a fresh
// branch is added.
func (s *Stack) Request(req *Message, dst Addr) (*ClientTx, error) {
	req.Prepend("Via", s.newVia(dst).String())
	if req.Get("Max-Forwards") == "" {
		req.Add("Max-Forwards", "70")
	}
	return s.startClientTx(req, dst)
}

func (s *Stack) startClientTx(req *Message, dst Addr) (*ClientTx, error) {
	via, err := req.TopVia()
	if err != nil {
		return nil, err
	}
	_, method := req.CSeq()
	tx := &ClientTx{
		s: s, key: via.Branch() + "|" + method, Request: req, dst: dst,
		invite: req.Method == "INVITE", responses: make(chan *Message, 32),
	}
	s.mu.Lock()
	s.cli[tx.key] = tx
	s.mu.Unlock()

	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := s.send(req, dst); err != nil {
		tx.terminateLocked()
		return nil, err
	}
	if !dst.Reliable() {
		tx.interval = s.T1
		tx.retrans = time.AfterFunc(tx.interval, tx.retransmit) // Timer A / E
	}
	tx.timeout = time.AfterFunc(64*s.T1, tx.onTimeout) // Timer B / F
	return tx, nil
}

// Responses delivers responses; it is closed after the final response.
func (tx *ClientTx) Responses() <-chan *Message { return tx.responses }

// Dst returns the destination of the transaction.
func (tx *ClientTx) Dst() Addr { return tx.dst }

// SetAck stores the ACK sent for a 2xx so it is repeated when the 2xx is
// retransmitted.
func (tx *ClientTx) SetAck(ack *Message) {
	tx.mu.Lock()
	tx.ack = ack
	tx.mu.Unlock()
}

func (tx *ClientTx) retransmit() {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.state == stTrying || (tx.state == stProceeding && !tx.invite) {
		_ = tx.s.send(tx.Request, tx.dst)
		if tx.invite {
			tx.interval *= 2
		} else if tx.state == stProceeding {
			tx.interval = tx.s.T2
		} else {
			tx.interval = min(tx.interval*2, tx.s.T2)
		}
		tx.retrans = time.AfterFunc(tx.interval, tx.retransmit)
	}
}

func (tx *ClientTx) onTimeout() {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	// An INVITE that got a provisional response is governed by the TU.
	if tx.state == stTrying || (tx.state == stProceeding && !tx.invite) {
		tx.deliverLocked(NewResponse(tx.Request, 408, "Request Timeout"))
		tx.terminateLocked()
	}
}

// ForceTimeout terminates a pending transaction with a synthetic 408 (used
// when a CANCEL is never answered with a final response).
func (tx *ClientTx) ForceTimeout() {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.state == stTrying || tx.state == stProceeding {
		tx.deliverLocked(NewResponse(tx.Request, 408, "Request Timeout"))
		tx.terminateLocked()
	}
}

func (tx *ClientTx) handleResponse(m *Message) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	code := m.StatusCode
	switch tx.state {
	case stTrying, stProceeding:
		if code < 200 {
			tx.state = stProceeding
			if tx.invite && tx.retrans != nil {
				tx.retrans.Stop()
			}
			tx.deliverLocked(m)
			return
		}
		tx.stopTimersLocked()
		switch {
		case tx.invite && code < 300:
			tx.state = stAccepted
			tx.deliverLocked(m)
			tx.closeLocked()
			time.AfterFunc(64*tx.s.T1, tx.terminate)
		case tx.invite:
			tx.state = stCompleted
			tx.ack = tx.nonSuccessAck(m)
			_ = tx.s.send(tx.ack, tx.dst)
			tx.deliverLocked(m)
			tx.closeLocked()
			if tx.dst.Reliable() {
				tx.terminateLocked()
			} else {
				time.AfterFunc(32*time.Second, tx.terminate) // Timer D
			}
		default:
			tx.state = stCompleted
			tx.deliverLocked(m)
			tx.closeLocked()
			if tx.dst.Reliable() {
				tx.terminateLocked()
			} else {
				time.AfterFunc(tx.s.T4, tx.terminate) // Timer K
			}
		}
	case stAccepted:
		if code >= 200 && code < 300 && tx.ack != nil {
			_ = tx.s.send(tx.ack, tx.dst)
		}
	case stCompleted:
		if tx.invite && code >= 300 && tx.ack != nil {
			_ = tx.s.send(tx.ack, tx.dst)
		}
	}
}

func (tx *ClientTx) nonSuccessAck(resp *Message) *Message {
	req := tx.Request
	ack := NewRequest("ACK", req.RequestURI)
	ack.Add("Via", req.Get("Via"))
	ack.Add("Max-Forwards", "70")
	for _, r := range req.GetAll("Route") {
		ack.Add("Route", r)
	}
	ack.Add("From", req.Get("From"))
	ack.Add("To", resp.Get("To"))
	ack.Add("Call-ID", req.CallID())
	seq, _ := req.CSeq()
	ack.Add("CSeq", fmt.Sprintf("%d ACK", seq))
	return ack
}

func (tx *ClientTx) deliverLocked(m *Message) {
	if tx.closed {
		return
	}
	select {
	case tx.responses <- m:
	default:
		tx.s.log.Warn("dropping response, receiver too slow", "resp", m.Short())
	}
}

func (tx *ClientTx) closeLocked() {
	if !tx.closed {
		tx.closed = true
		close(tx.responses)
	}
}

func (tx *ClientTx) stopTimersLocked() {
	if tx.retrans != nil {
		tx.retrans.Stop()
	}
	if tx.timeout != nil {
		tx.timeout.Stop()
	}
}

func (tx *ClientTx) terminate() {
	tx.mu.Lock()
	tx.terminateLocked()
	tx.mu.Unlock()
}

func (tx *ClientTx) terminateLocked() {
	if tx.state == stTerminated {
		return
	}
	tx.state = stTerminated
	tx.stopTimersLocked()
	tx.closeLocked()
	go tx.s.removeClient(tx.key, tx)
}

// Cancel sends a CANCEL for a pending INVITE client transaction.
func (s *Stack) Cancel(inv *ClientTx, reason string) error {
	req := inv.Request
	c := NewRequest("CANCEL", req.RequestURI)
	c.Add("Via", req.Get("Via"))
	c.Add("Max-Forwards", "70")
	for _, r := range req.GetAll("Route") {
		c.Add("Route", r)
	}
	c.Add("From", req.Get("From"))
	c.Add("To", req.Get("To"))
	c.Add("Call-ID", req.CallID())
	seq, _ := req.CSeq()
	c.Add("CSeq", fmt.Sprintf("%d CANCEL", seq))
	if reason != "" {
		c.Add("Reason", reason)
	}
	ctx, err := s.startClientTx(c, inv.dst)
	if err != nil {
		return err
	}
	go func() {
		for range ctx.Responses() {
		}
	}()
	// RFC 3261 9.1: give up on the INVITE if no final response arrives.
	time.AfterFunc(64*s.T1, inv.ForceTimeout)
	return nil
}
