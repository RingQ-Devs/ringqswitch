// Package esl implements the RingQswitch event socket: a line based TCP
// control protocol (auth, api, bgapi, event, noevents, exit).
package esl

import (
	"bufio"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/logbus"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Server accepts event socket connections.
type Server struct {
	core     *core.Core
	password string
	ln       net.Listener
	log      *slog.Logger
	logs     *logbus.Bus // the log lines clients follow with "log 7"
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns map[*conn]struct{}
}

// Listen starts an event socket server.
func Listen(addr, password string, c *core.Core, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("event socket: %w", err)
	}
	s := &Server{core: c, password: password, ln: ln, log: log, conns: map[*conn]struct{}{}}
	s.wg.Add(1)
	go s.accept()
	log.Info("event socket listening", "addr", ln.Addr().String())
	return s, nil
}

// SetLogBus lets clients follow the log ("log 7").
func (s *Server) SetLogBus(b *logbus.Bus) { s.logs = b }

// Addr returns the listening address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the server and drops clients.
func (s *Server) Close() {
	s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		c.nc.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		c := &conn{srv: s, nc: nc, r: bufio.NewReader(nc)}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			c.serve()
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		}()
	}
}

type conn struct {
	srv *Server
	nc  net.Conn
	r   *bufio.Reader

	wmu     sync.Mutex
	sub     *core.Subscription
	logSub  *logbus.Sub
	specPtr atomic.Pointer[eventSpec]
	format  atomic.Value // "plain" or "json"
}

func (c *conn) write(s string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.nc.Write([]byte(s))
	return err
}

func (c *conn) reply(text string) error {
	return c.write("Content-Type: command/reply\nReply-Text: " + text + "\n\n")
}

// readCommand reads one command block: the command line plus optional
// "Name: value" header lines, terminated by a blank line.
func (c *conn) readCommand() (string, map[string]string, error) {
	var first string
	headers := map[string]string{}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if first == "" {
				continue
			}
			return first, headers, nil
		}
		if first == "" {
			first = line
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
}

func (c *conn) serve() {
	defer c.nc.Close()
	defer c.stopEvents()
	defer c.stopLog()
	remote := c.nc.RemoteAddr().String()
	if err := c.write("Content-Type: auth/request\n\n"); err != nil {
		return
	}
	_ = c.nc.SetReadDeadline(time.Now().Add(15 * time.Second))
	cmd, _, err := c.readCommand()
	if err != nil {
		return
	}
	pw, ok := strings.CutPrefix(cmd, "auth ")
	if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(pw)), []byte(c.srv.password)) != 1 {
		c.srv.log.Warn("event socket authentication failed", "remote", remote)
		_ = c.reply("-ERR invalid")
		return
	}
	_ = c.reply("+OK accepted")
	_ = c.nc.SetReadDeadline(time.Time{})
	c.srv.log.Info("event socket client connected", "remote", remote)
	for {
		cmd, headers, err := c.readCommand()
		if err != nil {
			return
		}
		verb, args, _ := strings.Cut(strings.TrimSpace(cmd), " ")
		args = strings.TrimSpace(args)
		switch strings.ToLower(verb) {
		case "api":
			out := c.srv.core.API(args)
			if err := c.write(fmt.Sprintf("Content-Type: api/response\nContent-Length: %d\n\n%s", len(out), out)); err != nil {
				return
			}
		case "bgapi":
			// Clients such as modesl send their own Job-UUID and wait for it.
			job := headers["job-uuid"]
			if job == "" {
				job = sip.NewUUID()
			}
			_ = c.write("Content-Type: command/reply\nReply-Text: +OK Job-UUID: " + job + "\nJob-UUID: " + job + "\n\n")
			go func() {
				out := c.srv.core.API(args)
				e := core.NewEvent(core.EvBackgroundJob, "")
				e.Add("Job-UUID", job)
				e.Add("Job-Command", args)
				e.Body = out
				c.srv.core.Events().Publish(e)
			}()
		case "event":
			c.addEvents(strings.Fields(args))
		case "nixevent":
			c.removeEvents(strings.Fields(args))
		case "noevents":
			c.update(func(s *eventSpec) {
				s.all, s.customAll, s.names, s.subclasses = false, false, map[string]bool{}, map[string]bool{}
			})
			_ = c.reply("+OK no longer listening for events")
		case "myevents":
			f := strings.Fields(args)
			if len(f) == 0 {
				_ = c.reply("-ERR missing uuid")
				continue
			}
			uuid := f[0]
			c.update(func(s *eventSpec) {
				s.all = true
				s.filters = append(s.filters, [2]string{"Unique-ID", uuid})
			})
			c.ensureSubscribed(f[1:])
			_ = c.reply("+OK Events Enabled")
		case "filter":
			c.filterCmd(args)
		case "log":
			c.logCmd(args)
		case "nolog":
			c.stopLog()
			_ = c.reply("+OK no longer logging")
		case "linger", "nolinger", "divert_events":
			_ = c.reply("+OK")
		case "sendmsg":
			c.sendmsg(args, headers)
		case "exit", "quit":
			_ = c.reply("+OK bye")
			msg := "Disconnected, goodbye.\n"
			_ = c.write(fmt.Sprintf("Content-Type: text/disconnect-notice\nContent-Length: %d\n\n%s", len(msg), msg))
			return
		default:
			_ = c.reply("-ERR command not found")
		}
	}
}

// eventSpec is an immutable snapshot of what a connection listens to.
type eventSpec struct {
	all        bool
	customAll  bool // CUSTOM without subclasses: every CUSTOM event
	names      map[string]bool
	subclasses map[string]bool
	filters    [][2]string // OR-ed header=value filters
}

func (s *eventSpec) clone() *eventSpec {
	n := &eventSpec{all: s.all, customAll: s.customAll, names: map[string]bool{}, subclasses: map[string]bool{}}
	for k := range s.names {
		n.names[k] = true
	}
	for k := range s.subclasses {
		n.subclasses[k] = true
	}
	n.filters = append(n.filters, s.filters...)
	return n
}

func (s *eventSpec) match(e *core.Event) bool {
	if !s.all {
		if e.Name == core.EvCustom {
			if !s.customAll && !s.subclasses[e.Subclass()] {
				return false
			}
		} else if !s.names[e.Name] {
			return false
		}
	}
	if len(s.filters) == 0 {
		return true
	}
	for _, f := range s.filters {
		if e.Get(f[0]) == f[1] {
			return true
		}
	}
	return false
}

func (c *conn) spec() *eventSpec {
	if s := c.specPtr.Load(); s != nil {
		return s
	}
	return &eventSpec{names: map[string]bool{}, subclasses: map[string]bool{}}
}

func (c *conn) update(f func(*eventSpec)) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	n := c.spec().clone()
	f(n)
	c.specPtr.Store(n)
}

// addEvents implements "event [plain|json] NAME... [CUSTOM sub...]".
// Subscriptions accumulate, as clients expect.
func (c *conn) addEvents(args []string) {
	format := "plain"
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "plain", "json":
			format = strings.ToLower(args[0])
			args = args[1:]
		case "xml":
			_ = c.reply("-ERR xml events are not supported, use plain or json")
			return
		}
	}
	c.update(func(s *eventSpec) {
		custom := false
		if len(args) == 0 {
			s.all = true
		}
		for _, a := range args {
			switch {
			case strings.EqualFold(a, "ALL"):
				s.all = true
			case strings.EqualFold(a, "CUSTOM"):
				custom = true
			case custom:
				s.subclasses[a] = true
			default:
				s.names[strings.ToUpper(a)] = true
			}
		}
		if custom && len(s.subclasses) == 0 {
			s.customAll = true
		}
	})
	c.format.Store(format)
	c.ensureSubscribed(nil)
	_ = c.reply("+OK event listener enabled " + format)
}

func (c *conn) removeEvents(args []string) {
	c.update(func(s *eventSpec) {
		custom := false
		for _, a := range args {
			switch {
			case strings.EqualFold(a, "ALL"):
				s.all, s.customAll = false, false
				s.names, s.subclasses = map[string]bool{}, map[string]bool{}
			case strings.EqualFold(a, "CUSTOM"):
				custom = true
				s.customAll = false
			case custom:
				delete(s.subclasses, a)
			default:
				delete(s.names, strings.ToUpper(a))
			}
		}
	})
	_ = c.reply("+OK events nixed")
}

// filterCmd implements "filter <header> <value>" and
// "filter delete <header> [<value>]".
func (c *conn) filterCmd(args string) {
	f := strings.Fields(args)
	if len(f) >= 2 && strings.EqualFold(f[0], "delete") {
		header, value := f[1], strings.Join(f[2:], " ")
		c.update(func(s *eventSpec) {
			keep := s.filters[:0]
			for _, flt := range s.filters {
				if !(strings.EqualFold(flt[0], header) && (value == "" || flt[1] == value)) {
					keep = append(keep, flt)
				}
			}
			s.filters = keep
		})
		_ = c.reply("+OK filter deleted. [" + header + "]=[" + value + "]")
		return
	}
	if len(f) < 2 {
		_ = c.reply("-ERR invalid syntax")
		return
	}
	header, value := f[0], strings.Join(f[1:], " ")
	c.update(func(s *eventSpec) { s.filters = append(s.filters, [2]string{header, value}) })
	_ = c.reply("+OK filter added. [" + header + "]=[" + value + "]")
}

// sendmsg supports call-command: hangup.
func (c *conn) sendmsg(uuid string, headers map[string]string) {
	ch := c.srv.core.Channel(strings.TrimSpace(uuid))
	if ch == nil {
		_ = c.reply("-ERR invalid session id [" + uuid + "]")
		return
	}
	switch strings.ToLower(headers["call-command"]) {
	case "hangup":
		ch.Hangup(core.ParseCause(headers["hangup-cause"]))
		_ = c.reply("+OK")
	default:
		_ = c.reply("-ERR call-command " + headers["call-command"] + " is not supported yet (supported: hangup)")
	}
}

func (c *conn) ensureSubscribed(formatArgs []string) {
	if len(formatArgs) > 0 && (formatArgs[0] == "json" || formatArgs[0] == "plain") {
		c.format.Store(formatArgs[0])
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.sub != nil {
		return
	}
	c.sub = c.srv.core.Events().Subscribe(2048, func(e *core.Event) bool { return c.spec().match(e) })
	go c.pump(c.sub)
}

func (c *conn) pump(sub *core.Subscription) {
	for e := range sub.C {
		var body, ctype string
		if f, _ := c.format.Load().(string); f == "json" {
			body, ctype = e.JSON(), "text/event-json"
		} else {
			body, ctype = e.Plain(), "text/event-plain"
		}
		if err := c.write(fmt.Sprintf("Content-Length: %d\nContent-Type: %s\n\n%s", len(body), ctype, body)); err != nil {
			return
		}
	}
}

func (c *conn) stopEvents() {
	c.wmu.Lock()
	sub := c.sub
	c.sub = nil
	c.wmu.Unlock()
	if sub != nil {
		sub.Close()
		close(sub.C)
	}
}

// logCmd implements "log [level]": the log lines of that level and more
// important ones, as log/data messages (default 7, debug).
func (c *conn) logCmd(args string) {
	if c.srv.logs == nil {
		_ = c.reply("-ERR logging is not available")
		return
	}
	level := 7
	if args != "" {
		n, ok := logbus.ParseLevel(args)
		if !ok {
			_ = c.reply("-ERR invalid log level")
			return
		}
		level = n
	}
	c.wmu.Lock()
	sub := c.logSub
	if sub == nil {
		sub = c.srv.logs.Subscribe(level)
		c.logSub = sub
		go c.pumpLog(sub)
	}
	c.wmu.Unlock()
	sub.SetLevel(level)
	_ = c.reply(fmt.Sprintf("+OK log level %s [%d]", logbus.LevelName(level), level))
}

func (c *conn) pumpLog(sub *logbus.Sub) {
	for l := range sub.C {
		body := l.Format() + "\n"
		msg := fmt.Sprintf("Content-Type: log/data\nContent-Length: %d\nLog-Level: %d\nText-Channel: 3\nLog-File: %s\nLog-Func: %s\nLog-Line: %d\nUser-Data: %s\n\n%s",
			len(body), l.Level, l.File, l.Func, l.Num, l.UUID, body)
		if err := c.write(msg); err != nil {
			return
		}
	}
}

func (c *conn) stopLog() {
	c.wmu.Lock()
	sub := c.logSub
	c.logSub = nil
	c.wmu.Unlock()
	if sub != nil {
		c.srv.logs.Unsubscribe(sub)
	}
}
