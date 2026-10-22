package media

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Samples per 20 ms frame at 8 kHz.
const FrameSamples = 160

// Codec is a negotiated payload type.
type Codec struct {
	PT   uint8
	Name string // upper case for G.711 ("PCMU"), otherwise as negotiated
	Rate int
}

func (c Codec) same(o Codec) bool { return strings.EqualFold(c.Name, o.Name) && c.Rate == o.Rate }

// Mode selects what an endpoint does with received media.
type Mode int32

const (
	// ModeIdle drops media (DTMF is still reported).
	ModeIdle Mode = iota
	// ModeLocal terminates media: received audio is decoded for
	// applications and a 20 ms clock transmits queued frames (or silence).
	ModeLocal
	// ModeEcho reflects received media back to the sender.
	ModeEcho
	// ModeRelay forwards media to the bridged peer endpoint.
	ModeRelay
)

var (
	// ErrClosed is returned when using a closed endpoint.
	ErrClosed = errors.New("media: endpoint closed")
	// ErrNoPorts is returned when the RTP port range is exhausted.
	ErrNoPorts = errors.New("media: no free rtp ports")
)

// Stats are packet counters.
type Stats struct {
	PacketsIn, PacketsOut, BytesIn, BytesOut uint64
}

// Endpoint is our side of one RTP stream: an even RTP port and RTP+1 for RTCP.
type Endpoint struct {
	alloc *Allocator
	Port  int
	rtp   *net.UDPConn
	rtcp  *net.UDPConn
	log   *slog.Logger

	mu         sync.Mutex
	remote     netip.AddrPort
	remoteRTCP netip.AddrPort
	sdpAddr    netip.Addr // the address SetRemote was given
	latched    bool
	rtcpLatch  bool
	codecs     []Codec
	send       Codec
	haveSend   bool
	tePT       int
	mode       Mode
	peer       *Endpoint
	clockStop  chan struct{}
	onDTMF     func(rune)
	tap        func([]int16)
	raw        func([]byte)
	lastDTMFts uint32
	dtmfSeen   bool

	ssrc uint32
	seq  uint16
	ts   uint32
	mark bool

	outQ   chan []int16
	inQ    chan []int16
	closed chan struct{}
	once   sync.Once

	lastRx            atomic.Int64
	pktIn, pktOut     atomic.Uint64
	bytesIn, bytesOut atomic.Uint64
}

func newEndpoint(a *Allocator, port int, rtp, rtcp *net.UDPConn) *Endpoint {
	var b [10]byte
	_, _ = rand.Read(b[:])
	e := &Endpoint{
		alloc: a, Port: port, rtp: rtp, rtcp: rtcp, log: a.log,
		tePT: -1,
		ssrc: binary.BigEndian.Uint32(b[0:4]),
		seq:  binary.BigEndian.Uint16(b[4:6]),
		ts:   binary.BigEndian.Uint32(b[6:10]),
		mark: true,
		outQ: make(chan []int16, 2), inQ: make(chan []int16, 50),
		closed: make(chan struct{}),
	}
	go e.rtpLoop()
	go e.rtcpLoop()
	return e
}

// SetRemote sets the destination from SDP. Symmetric RTP latching will
// replace it with the source address of the first packet received.
func (e *Endpoint) SetRemote(rtp, rtcp netip.AddrPort) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rtp != e.remote {
		e.latched = false
		e.rtcpLatch = false
	}
	e.remote, e.remoteRTCP = rtp, rtcp
	e.sdpAddr = rtp.Addr()
}

// Relearn forgets the latched source, so the next packet's source becomes
// the destination again (a carrier may move the media to another server
// when the call is answered).
func (e *Endpoint) Relearn() {
	e.mu.Lock()
	e.latched = false
	e.mu.Unlock()
}

// relatchQuiet is how long the latched source must be silent before media
// from a new port of the same host (NAT rebinding) is followed.
const relatchQuiet = time.Second

// mayMoveLocked reports whether packets from src may replace the latched
// source: only once that has gone quiet, and only from the same host or the
// address the SDP named, so a stranger cannot take over the stream.
func (e *Endpoint) mayMoveLocked(src netip.AddrPort) bool {
	if last := e.lastRx.Load(); last != 0 && time.Since(time.Unix(0, last)) < relatchQuiet {
		return false
	}
	return src.Addr() == e.remote.Addr() || src.Addr() == e.sdpAddr
}

// Remote returns the current destination.
func (e *Endpoint) Remote() netip.AddrPort {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.remote
}

// SetCodecs records the codecs negotiated with this endpoint's peer. send is
// the codec used for locally generated audio; tePT is the telephone-event
// payload type or -1.
func (e *Endpoint) SetCodecs(codecs []Codec, send Codec, tePT int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.codecs = append([]Codec(nil), codecs...)
	e.send = send
	e.haveSend = send.Name != ""
	e.tePT = tePT
}

// SendCodec returns the codec used for local audio.
func (e *Endpoint) SendCodec() (Codec, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.send, e.haveSend
}

// OnDTMF registers a callback for RFC 4733 digits received while not relaying.
func (e *Endpoint) OnDTMF(f func(rune)) {
	e.mu.Lock()
	e.onDTMF = f
	e.mu.Unlock()
}

// SetMode switches the endpoint mode.
// SetTap registers f to receive the decoded audio received on this
// endpoint, whatever the mode (nil removes it). Only G.711 can be decoded;
// other codecs are not reported.
func (e *Endpoint) SetTap(f func([]int16)) {
	e.mu.Lock()
	e.tap = f
	e.mu.Unlock()
}

// SetRaw hands every datagram received on the RTP port to f, instead of
// treating it as RTP, and keeps the clock from sending audio: T.38
// (UDPTL) takes over the port. The clock still paces
// WriteFrame. nil returns to RTP. f must not keep the slice.
func (e *Endpoint) SetRaw(f func([]byte)) {
	e.mu.Lock()
	e.raw = f
	e.mu.Unlock()
}

// WriteRaw sends a datagram to the far end (T.38).
func (e *Endpoint) WriteRaw(b []byte) { e.write(b) }

func (e *Endpoint) SetMode(m Mode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setModeLocked(m)
}

func (e *Endpoint) setModeLocked(m Mode) {
	if e.mode == m {
		return
	}
	if e.clockStop != nil {
		close(e.clockStop)
		e.clockStop = nil
	}
	e.mode = m
	if m != ModeRelay {
		e.peer = nil
	}
	if m == ModeLocal {
		e.drainOut()
		e.clockStop = make(chan struct{})
		go e.clock(e.clockStop)
	}
}

// Mode returns the current mode.
func (e *Endpoint) Mode() Mode {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mode
}

func (e *Endpoint) drainOut() {
	for {
		select {
		case <-e.outQ:
		default:
			return
		}
	}
}

// Bridge relays media between two endpoints.
func Bridge(a, b *Endpoint) {
	a.mu.Lock()
	a.setModeLocked(ModeRelay)
	a.peer = b
	a.mu.Unlock()
	b.mu.Lock()
	b.setModeLocked(ModeRelay)
	b.peer = a
	b.mu.Unlock()
}

// Unbridge stops relaying between a and b (if they are still paired).
func Unbridge(a, b *Endpoint) {
	for _, pair := range [][2]*Endpoint{{a, b}, {b, a}} {
		e, p := pair[0], pair[1]
		e.mu.Lock()
		if e.mode == ModeRelay && e.peer == p {
			e.setModeLocked(ModeIdle)
		}
		e.mu.Unlock()
	}
}

// LastRx returns when media was last received (zero if never).
func (e *Endpoint) LastRx() time.Time {
	n := e.lastRx.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Stats returns packet counters.
func (e *Endpoint) Stats() Stats {
	return Stats{e.pktIn.Load(), e.pktOut.Load(), e.bytesIn.Load(), e.bytesOut.Load()}
}

// Close releases the ports.
func (e *Endpoint) Close() {
	e.once.Do(func() {
		e.mu.Lock()
		e.setModeLocked(ModeIdle)
		e.mu.Unlock()
		close(e.closed)
		e.rtp.Close()
		e.rtcp.Close()
		e.alloc.release(e.Port)
	})
}

// Closed is closed when the endpoint is closed.
func (e *Endpoint) Closed() <-chan struct{} { return e.closed }

// rtpHeader holds the fields we need from an RTP packet.
type rtpHeader struct {
	pt         uint8
	marker     bool
	seq        uint16
	ts         uint32
	ssrc       uint32
	payloadOff int
	payloadEnd int
}

func parseRTP(b []byte) (rtpHeader, bool) {
	var h rtpHeader
	if len(b) < 12 || b[0]>>6 != 2 {
		return h, false
	}
	cc := int(b[0] & 0x0F)
	off := 12 + 4*cc
	if b[0]&0x10 != 0 { // extension
		if len(b) < off+4 {
			return h, false
		}
		off += 4 + 4*int(binary.BigEndian.Uint16(b[off+2:]))
	}
	end := len(b)
	if b[0]&0x20 != 0 && end > 0 { // padding
		end -= int(b[end-1])
	}
	if off > end {
		return h, false
	}
	h.marker = b[1]&0x80 != 0
	h.pt = b[1] & 0x7F
	h.seq = binary.BigEndian.Uint16(b[2:])
	h.ts = binary.BigEndian.Uint32(b[4:])
	h.ssrc = binary.BigEndian.Uint32(b[8:])
	h.payloadOff, h.payloadEnd = off, end
	return h, true
}

func (e *Endpoint) codecFor(pt uint8) (Codec, bool) {
	for _, c := range e.codecs {
		if c.PT == pt {
			return c, true
		}
	}
	return Codec{}, false
}

func (e *Endpoint) rtpLoop() {
	buf := make([]byte, 2048)
	var pcm []int16
	for {
		n, from, err := e.rtp.ReadFromUDPAddrPort(buf)
		if err != nil {
			select {
			case <-e.closed:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		pkt := buf[:n]
		e.mu.Lock()
		raw := e.raw
		e.mu.Unlock()
		var h rtpHeader
		if raw == nil {
			var ok bool
			if h, ok = parseRTP(pkt); !ok {
				continue
			}
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())

		e.mu.Lock()
		if !e.latched {
			if e.remote != from {
				e.log.Debug("rtp latched", "port", e.Port, "sdp", e.remote, "actual", from)
			}
			e.remote, e.latched = from, true
			if !e.rtcpLatch {
				e.remoteRTCP = netip.AddrPortFrom(from.Addr(), from.Port()+1)
			}
		} else if from != e.remote {
			if !e.mayMoveLocked(from) {
				e.mu.Unlock()
				continue
			}
			e.log.Info("rtp source moved", "port", e.Port, "was", e.remote, "now", from)
			e.remote = from
			if !e.rtcpLatch {
				e.remoteRTCP = netip.AddrPortFrom(from.Addr(), from.Port()+1)
			}
		}
		if raw != nil {
			e.mu.Unlock()
			e.lastRx.Store(time.Now().UnixNano())
			e.pktIn.Add(1)
			e.bytesIn.Add(uint64(n))
			raw(pkt)
			continue
		}
		mode, peer, tePT, onDTMF, tap := e.mode, e.peer, e.tePT, e.onDTMF, e.tap
		codec, known := e.codecFor(h.pt)
		isDTMF := tePT >= 0 && int(h.pt) == tePT
		var digit rune
		if isDTMF && mode != ModeRelay {
			digit = e.dtmfLocked(h, pkt[h.payloadOff:h.payloadEnd])
		}
		e.mu.Unlock()

		e.lastRx.Store(time.Now().UnixNano())
		e.pktIn.Add(1)
		e.bytesIn.Add(uint64(n))

		if digit != 0 && onDTMF != nil {
			onDTMF(digit)
		}
		if tap != nil && known && !isDTMF {
			// Before relaying: relay rewrites the packet in place.
			if out, ok := Decode(codec.Name, pkt[h.payloadOff:h.payloadEnd], nil); ok {
				tap(out)
			}
		}
		switch mode {
		case ModeRelay:
			if peer != nil {
				peer.relay(pkt, h, codec, known, isDTMF)
			}
		case ModeEcho:
			if !isDTMF {
				binary.BigEndian.PutUint32(pkt[8:], e.ssrc)
				e.write(pkt)
			}
		case ModeLocal:
			if isDTMF || !known {
				continue
			}
			if out, ok := Decode(codec.Name, pkt[h.payloadOff:h.payloadEnd], pcm); ok {
				frame := append([]int16(nil), out...)
				select {
				case e.inQ <- frame:
				default:
				}
			}
		}
	}
}

var dtmfChars = "0123456789*#ABCD"

// dtmfLocked decodes an RFC 4733 event and returns the digit once per event.
func (e *Endpoint) dtmfLocked(h rtpHeader, payload []byte) rune {
	if len(payload) < 4 || int(payload[0]) >= len(dtmfChars) {
		return 0
	}
	end := payload[1]&0x80 != 0
	if !end || (e.dtmfSeen && e.lastDTMFts == h.ts) {
		return 0
	}
	e.dtmfSeen, e.lastDTMFts = true, h.ts
	return rune(dtmfChars[payload[0]])
}

// relay transmits a packet received on the peer endpoint, rewriting the
// payload type (and transcoding between G.711 laws) as needed.
func (e *Endpoint) relay(pkt []byte, h rtpHeader, src Codec, known, isDTMF bool) {
	e.mu.Lock()
	remote := e.remote
	pt := int(h.pt)
	ok := true
	switch {
	case isDTMF:
		pt = e.tePT
		ok = pt >= 0
	case known:
		ok = false
		for _, c := range e.codecs {
			if c.same(src) {
				pt, ok = int(c.PT), true
				break
			}
		}
		if !ok && e.haveSend && Transcode(src.Name, e.send.Name, pkt[h.payloadOff:h.payloadEnd]) {
			pt, ok = int(e.send.PT), true
		}
	}
	e.mu.Unlock()
	if !ok || !remote.IsValid() {
		return
	}
	pkt[1] = pkt[1]&0x80 | byte(pt)&0x7F
	e.writeTo(pkt, remote)
}

func (e *Endpoint) write(pkt []byte) {
	e.mu.Lock()
	remote := e.remote
	e.mu.Unlock()
	if remote.IsValid() {
		e.writeTo(pkt, remote)
	}
}

func (e *Endpoint) writeTo(pkt []byte, remote netip.AddrPort) {
	if _, err := e.rtp.WriteToUDPAddrPort(pkt, remote); err == nil {
		e.pktOut.Add(1)
		e.bytesOut.Add(uint64(len(pkt)))
	}
}

func (e *Endpoint) rtcpLoop() {
	buf := make([]byte, 2048)
	for {
		n, from, err := e.rtcp.ReadFromUDPAddrPort(buf)
		if err != nil {
			select {
			case <-e.closed:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		e.mu.Lock()
		if !e.rtcpLatch {
			e.remoteRTCP, e.rtcpLatch = from, true
		}
		peer := e.peer
		mode := e.mode
		e.mu.Unlock()
		if mode == ModeRelay && peer != nil {
			peer.mu.Lock()
			dst := peer.remoteRTCP
			peer.mu.Unlock()
			if dst.IsValid() {
				_, _ = peer.rtcp.WriteToUDPAddrPort(buf[:n], dst)
			}
		}
	}
}

// clock transmits one frame every 20 ms while in local mode.
func (e *Endpoint) clock(stop chan struct{}) {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	silence := make([]int16, FrameSamples)
	pkt := make([]byte, 12, 12+FrameSamples)
	payload := make([]byte, 0, FrameSamples)
	for {
		select {
		case <-stop:
			return
		case <-e.closed:
			return
		case <-t.C:
		}
		frame := silence
		select {
		case f := <-e.outQ:
			frame = f
		default:
		}
		e.mu.Lock()
		if !e.haveSend || !e.remote.IsValid() || e.raw != nil {
			e.ts += FrameSamples
			e.mu.Unlock()
			continue
		}
		pkt = pkt[:12]
		pkt[0] = 0x80
		pkt[1] = e.send.PT & 0x7F
		if e.mark {
			pkt[1] |= 0x80
			e.mark = false
		}
		e.seq++
		binary.BigEndian.PutUint16(pkt[2:], e.seq)
		binary.BigEndian.PutUint32(pkt[4:], e.ts)
		binary.BigEndian.PutUint32(pkt[8:], e.ssrc)
		e.ts += uint32(len(frame))
		codec, remote := e.send.Name, e.remote
		e.mu.Unlock()
		var ok bool
		if payload, ok = Encode(codec, frame, payload[:0]); !ok {
			continue
		}
		pkt = append(pkt[:12], payload...)
		e.writeTo(pkt, remote)
	}
}

// WriteFrame queues a 20 ms frame for transmission in local mode. It blocks
// until the media clock has room, which paces callers in real time.
func (e *Endpoint) WriteFrame(ctx context.Context, pcm []int16) error {
	select {
	case e.outQ <- pcm:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.closed:
		return ErrClosed
	}
}

// ReadFrame returns the next decoded frame received in local mode.
func (e *Endpoint) ReadFrame(ctx context.Context) ([]int16, error) {
	select {
	case f := <-e.inQ:
		return f, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.closed:
		return nil, ErrClosed
	}
}

// TryReadFrame returns the next received frame without waiting (nil when
// none is buffered). A backlog is trimmed to keep latency low, as needed
// by the conference mixer.
func (e *Endpoint) TryReadFrame() []int16 {
	for len(e.inQ) > 3 {
		select {
		case <-e.inQ:
		default:
		}
	}
	select {
	case f := <-e.inQ:
		return f
	default:
		return nil
	}
}

// TryWriteFrame queues a frame for transmission in local mode without
// waiting; it reports false (and drops the frame) when the queue is full.
func (e *Endpoint) TryWriteFrame(pcm []int16) bool {
	select {
	case e.outQ <- pcm:
		return true
	default:
		return false
	}
}

// FlushInput discards buffered received audio.
func (e *Endpoint) FlushInput() {
	for {
		select {
		case <-e.inQ:
		default:
			return
		}
	}
}

// Allocator hands out RTP/RTCP port pairs from a range.
type Allocator struct {
	ip       net.IP
	min, max int
	log      *slog.Logger

	mu   sync.Mutex
	next int
	used map[int]bool
}

// NewAllocator creates an allocator for [min, max] bound to ip ("" = any).
func NewAllocator(ip string, min, max int, log *slog.Logger) (*Allocator, error) {
	if min%2 != 0 {
		min++
	}
	if min <= 0 || max > 65535 || max-min < 2 {
		return nil, fmt.Errorf("media: invalid rtp port range %d-%d", min, max)
	}
	var bind net.IP
	if ip != "" && ip != "0.0.0.0" {
		if bind = net.ParseIP(ip); bind == nil {
			return nil, fmt.Errorf("media: invalid rtp ip %q", ip)
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Allocator{ip: bind, min: min, max: max, log: log, used: map[int]bool{}}, nil
}

// Allocate opens a new endpoint.
func (a *Allocator) Allocate() (*Endpoint, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	span := (a.max - a.min) / 2
	for i := 0; i < span; i++ {
		idx := (a.next + i) % span
		port := a.min + 2*idx
		if a.used[port] {
			continue
		}
		rtp, err := net.ListenUDP("udp", &net.UDPAddr{IP: a.ip, Port: port})
		if err != nil {
			continue
		}
		rtcp, err := net.ListenUDP("udp", &net.UDPAddr{IP: a.ip, Port: port + 1})
		if err != nil {
			rtp.Close()
			continue
		}
		a.used[port] = true
		a.next = (idx + 1) % span
		return newEndpoint(a, port, rtp, rtcp), nil
	}
	return nil, ErrNoPorts
}

// InUse returns the number of allocated endpoints.
func (a *Allocator) InUse() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.used)
}

func (a *Allocator) release(port int) {
	a.mu.Lock()
	delete(a.used, port)
	a.mu.Unlock()
}
