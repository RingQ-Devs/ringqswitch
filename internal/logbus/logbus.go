// Package logbus copies RingQswitch's log lines to event socket clients
// that asked for them ("log 7"), at the level each client
// chose, whatever the level of the service log.
package logbus

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LevelNotice sits between info and warning (the console's NOTICE).
const LevelNotice = slog.Level(2)

// RawKey marks a line that is shown as it is, without time and level
// (the dialplan trace: "Dialplan: ... Regex (PASS) ...").
const RawKey = "rq_raw"

// Console log levels (the console's "/log 7").
var levelNames = []string{"CONSOLE", "ALERT", "CRIT", "ERR", "WARNING", "NOTICE", "INFO", "DEBUG"}

// ConsoleLevel converts a log level to the console's 0 (console) .. 7 (debug).
func ConsoleLevel(l slog.Level) int {
	switch {
	case l < slog.LevelInfo:
		return 7
	case l < LevelNotice:
		return 6
	case l < slog.LevelWarn:
		return 5
	case l < slog.LevelError:
		return 4
	case l < slog.LevelError+4:
		return 3
	}
	return 2
}

// SlogLevel is the lowest log level shown at console level n.
func SlogLevel(n int) slog.Level {
	switch {
	case n >= 7:
		return slog.LevelDebug
	case n == 6:
		return slog.LevelInfo
	case n == 5:
		return LevelNotice
	case n == 4:
		return slog.LevelWarn
	case n == 3:
		return slog.LevelError
	}
	return slog.LevelError + 4
}

// LevelName is the console's name of level n (DEBUG, INFO, NOTICE...).
func LevelName(n int) string {
	if n < 0 || n >= len(levelNames) {
		return "DEBUG"
	}
	return levelNames[n]
}

// ParseLevel reads "7", "debug", "info", "notice", "warning", "err"...
func ParseLevel(s string) (int, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) == 1 && s[0] >= '0' && s[0] <= '7' {
		return int(s[0] - '0'), true
	}
	switch s {
	case "WARN":
		return 4, true
	case "ERROR":
		return 3, true
	}
	for i, n := range levelNames {
		if n == s {
			return i, true
		}
	}
	return 0, false
}

// Line is one log line.
type Line struct {
	Time  time.Time
	Level int    // console level
	File  string // "inbound.go"
	Func  string
	Num   int    // line number in File
	UUID  string // the call it is about
	Text  string // the message and its details
	Raw   bool
}

// Format is the line as the console shows it.
func (l Line) Format() string {
	if l.Raw {
		return l.Text
	}
	src := ""
	if l.File != "" {
		src = fmt.Sprintf("%s:%d ", l.File, l.Num)
	}
	return fmt.Sprintf("%s [%s] %s%s", l.Time.Format("2006-01-02 15:04:05.000000"), LevelName(l.Level), src, l.Text)
}

// Sub is one listener.
type Sub struct {
	C     chan Line
	bus   *Bus
	level atomic.Int64 // console level
}

// SetLevel changes what the listener gets.
func (s *Sub) SetLevel(n int) {
	s.level.Store(int64(n))
	s.bus.recompute()
}

// Bus distributes log lines.
type Bus struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
	min  atomic.Int64 // the lowest slog level any listener wants
}

// New returns a bus without listeners.
func New() *Bus {
	b := &Bus{subs: map[*Sub]struct{}{}}
	b.min.Store(math.MaxInt64)
	return b
}

// Subscribe adds a listener at console level n.
func (b *Bus) Subscribe(n int) *Sub {
	s := &Sub{C: make(chan Line, 4096), bus: b}
	s.level.Store(int64(n))
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	b.recompute()
	return s
}

// Unsubscribe removes a listener and closes its channel.
func (b *Bus) Unsubscribe(s *Sub) {
	b.mu.Lock()
	_, ok := b.subs[s]
	delete(b.subs, s)
	b.mu.Unlock()
	if ok {
		close(s.C)
	}
	b.recompute()
}

func (b *Bus) recompute() {
	b.mu.Lock()
	defer b.mu.Unlock()
	lowest := int64(math.MaxInt64)
	for s := range b.subs {
		if l := int64(SlogLevel(int(s.level.Load()))); l < lowest {
			lowest = l
		}
	}
	b.min.Store(lowest)
}

// Wants says whether some listener takes lines of level l.
func (b *Bus) Wants(l slog.Level) bool { return b != nil && int64(l) >= b.min.Load() }

// Listening says whether anybody follows the log at all.
func (b *Bus) Listening() bool { return b != nil && b.min.Load() != math.MaxInt64 }

func (b *Bus) publish(l Line) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if l.Level <= int(s.level.Load()) {
			select {
			case s.C <- l:
			default: // a slow client misses lines rather than slowing calls
			}
		}
	}
}

// Handler is a slog handler that writes to next (the service log, at its
// own level) and copies lines to the bus.
type Handler struct {
	next  slog.Handler
	bus   *Bus
	attrs []slog.Attr
	group string
}

// NewHandler wraps next.
func NewHandler(next slog.Handler, bus *Bus) *Handler { return &Handler{next: next, bus: bus} }

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l) || h.bus.Wants(l)
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.next.Enabled(ctx, r.Level) {
		err = h.next.Handle(ctx, r)
	}
	if !h.bus.Wants(r.Level) {
		return err
	}
	line := Line{Time: r.Time, Level: ConsoleLevel(r.Level)}
	if r.PC != 0 {
		f, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
		line.File, line.Num = filepath.Base(f.File), f.Line
		line.Func = f.Function[strings.LastIndexByte(f.Function, '.')+1:]
	}
	var b strings.Builder
	b.WriteString(r.Message)
	add := func(a slog.Attr) {
		switch a.Key {
		case "uuid":
			line.UUID = a.Value.String()
			return
		case RawKey:
			line.Raw = a.Value.Bool()
			return
		}
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		v := a.Value.String()
		if strings.ContainsAny(v, " \t\"") {
			v = fmt.Sprintf("%q", v)
		}
		fmt.Fprintf(&b, " %s=%s", key, v)
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	line.Text = b.String()
	h.bus.publish(line)
	return err
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.next = h.next.WithAttrs(as)
	n.attrs = append(append([]slog.Attr{}, h.attrs...), as...)
	return &n
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	n := *h
	n.next = h.next.WithGroup(name)
	if n.group != "" {
		n.group += "." + name
	} else {
		n.group = name
	}
	return &n
}
