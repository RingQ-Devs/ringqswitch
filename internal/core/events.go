package core

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Event names (stable: event socket clients subscribe to them).
const (
	EvChannelCreate         = "CHANNEL_CREATE"
	EvChannelProgress       = "CHANNEL_PROGRESS"
	EvChannelProgressMedia  = "CHANNEL_PROGRESS_MEDIA"
	EvChannelAnswer         = "CHANNEL_ANSWER"
	EvChannelBridge         = "CHANNEL_BRIDGE"
	EvChannelUnbridge       = "CHANNEL_UNBRIDGE"
	EvChannelExecute        = "CHANNEL_EXECUTE"
	EvChannelExecuteDone    = "CHANNEL_EXECUTE_COMPLETE"
	EvChannelHangup         = "CHANNEL_HANGUP"
	EvChannelHangupComplete = "CHANNEL_HANGUP_COMPLETE"
	EvChannelDestroy        = "CHANNEL_DESTROY"
	EvChannelCallState      = "CHANNEL_CALLSTATE"
	EvDTMF                  = "DTMF"
	EvHeartbeat             = "HEARTBEAT"
	EvCustom                = "CUSTOM"
	EvBackgroundJob         = "BACKGROUND_JOB"
	EvReloadXML             = "RELOADXML"
	EvShutdown              = "SHUTDOWN"
)

// Event is a switch event with ordered headers.
type Event struct {
	Name    string
	Headers [][2]string
	Body    string
}

// NewEvent creates an event with the given name (and subclass for CUSTOM).
func NewEvent(name, subclass string) *Event {
	e := &Event{Name: name}
	e.Add("Event-Name", name)
	if subclass != "" {
		e.Add("Event-Subclass", subclass)
	}
	now := time.Now()
	e.Add("Event-Date-Local", now.Format("2006-01-02 15:04:05"))
	e.Add("Event-Date-Timestamp", strconv.FormatInt(now.UnixMicro(), 10))
	return e
}

// Add appends a header.
func (e *Event) Add(k, v string) { e.Headers = append(e.Headers, [2]string{k, v}) }

// Get returns the first header value.
func (e *Event) Get(k string) string {
	for _, h := range e.Headers {
		if strings.EqualFold(h[0], k) {
			return h[1]
		}
	}
	return ""
}

// Subclass returns Event-Subclass.
func (e *Event) Subclass() string { return e.Get("Event-Subclass") }

// Plain renders "Name: url-encoded-value" lines.
func (e *Event) Plain() string {
	var b strings.Builder
	for _, h := range e.Headers {
		b.WriteString(h[0])
		b.WriteString(": ")
		b.WriteString(strings.ReplaceAll(url.QueryEscape(h[1]), "+", "%20"))
		b.WriteByte('\n')
	}
	if e.Body != "" {
		b.WriteString("Content-Length: ")
		b.WriteString(strconv.Itoa(len(e.Body)))
		b.WriteString("\n\n")
		b.WriteString(e.Body)
	}
	return b.String()
}

// JSON renders the event as a flat JSON object.
func (e *Event) JSON() string {
	m := make(map[string]string, len(e.Headers)+1)
	for _, h := range e.Headers {
		if _, dup := m[h[0]]; !dup {
			m[h[0]] = h[1]
		}
	}
	if e.Body != "" {
		m["_body"] = e.Body
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// Subscription receives events.
type Subscription struct {
	C       chan *Event
	bus     *EventBus
	filter  func(*Event) bool
	dropped atomic.Uint64
}

// Dropped returns how many events were lost because the subscriber was slow.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close unsubscribes.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	delete(s.bus.subs, s)
	s.bus.mu.Unlock()
}

// legacyHostPrefix names the host headers as older event socket clients
// expect them.
const legacyHostPrefix = "FreeSWITCH-"

// EventBus fans events out to subscribers without ever blocking publishers.
type EventBus struct {
	mu       sync.RWMutex
	subs     map[*Subscription]struct{}
	coreUUID string
	hostname string // RingQswitch-Hostname header (clients filter on it)
	ipv4     string
	seq      atomic.Uint64
}

func newEventBus(coreUUID, hostname, ipv4 string) *EventBus {
	return &EventBus{subs: map[*Subscription]struct{}{}, coreUUID: coreUUID, hostname: hostname, ipv4: ipv4}
}

// Subscribe registers a subscriber. filter may be nil.
func (b *EventBus) Subscribe(buffer int, filter func(*Event) bool) *Subscription {
	s := &Subscription{C: make(chan *Event, buffer), bus: b, filter: filter}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Publish delivers an event to all matching subscribers.
func (b *EventBus) Publish(e *Event) {
	e.Add("Core-UUID", b.coreUUID)
	e.Add("RingQswitch-Hostname", b.hostname)
	e.Add("RingQswitch-Switchname", b.hostname)
	e.Add("RingQswitch-IPv4", b.ipv4)
	// The same under the older prefix that existing integrations filter on.
	e.Add(legacyHostPrefix+"Hostname", b.hostname)
	e.Add(legacyHostPrefix+"Switchname", b.hostname)
	e.Add(legacyHostPrefix+"IPv4", b.ipv4)
	e.Add("Event-Sequence", strconv.FormatUint(b.seq.Add(1), 10))
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.filter != nil && !s.filter(e) {
			continue
		}
		select {
		case s.C <- e:
		default:
			s.dropped.Add(1)
		}
	}
}
