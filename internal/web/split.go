package web

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// splitListener shares the web port between the web interface and ICE over
// TCP (RFC 6544), so the dialer and meetings still get audio from networks
// that block UDP: the browser connects to the port the page came from. The
// first byte tells them apart: ICE over TCP starts with a two-byte frame
// length (0x00 for a STUN binding request), TLS with 0x16 and plain HTTP
// with a letter.
type splitListener struct {
	ln        net.Listener
	web, ice  *subListener
	closeOnce sync.Once
}

func newSplitListener(ln net.Listener) *splitListener {
	s := &splitListener{ln: ln}
	s.web = &subListener{parent: s, conns: make(chan net.Conn), done: make(chan struct{})}
	s.ice = &subListener{parent: s, conns: make(chan net.Conn), done: make(chan struct{}), own: true}
	go s.run()
	return s
}

// Web is the listener for the web server; closing it closes the port.
func (s *splitListener) Web() net.Listener { return s.web }

// ICE is the listener for ICE over TCP; closing it only stops ICE.
func (s *splitListener) ICE() net.Listener { return s.ice }

func (s *splitListener) run() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			s.close()
			return
		}
		go s.route(c)
	}
}

// route reads the first byte (slow clients have 10 seconds) and hands the
// connection, with that byte put back, to the web server or to ICE.
func (s *splitListener) route(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	first := make([]byte, 1)
	if _, err := io.ReadFull(c, first); err != nil {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	pc := &peekedConn{Conn: c, r: io.MultiReader(bytes.NewReader(first), c)}
	to := s.web
	if first[0] == 0x00 {
		to = s.ice
		// Real-time media: keep the kernel's send queue short too.
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetWriteBuffer(64 << 10)
			_ = tc.SetNoDelay(true)
		}
	}
	select {
	case to.conns <- pc:
	case <-to.done:
		c.Close()
	}
}

func (s *splitListener) close() {
	s.closeOnce.Do(func() {
		s.ln.Close()
		s.web.stop()
		s.ice.stop()
	})
}

type subListener struct {
	parent *splitListener
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	own    bool // Close stops only this side
}

func (l *subListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *subListener) Close() error {
	if l.own {
		l.stop()
	} else {
		l.parent.close()
	}
	return nil
}

func (l *subListener) stop() { l.once.Do(func() { close(l.done) }) }

func (l *subListener) Addr() net.Addr { return l.parent.ln.Addr() }

type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
