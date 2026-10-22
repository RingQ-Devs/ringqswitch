package sip

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stats counts SIP traffic so silent drops can be diagnosed.
type Stats struct {
	Received   uint64 // messages read from the network
	Sent       uint64 // messages written
	SendErrors uint64 // writes that failed
	Unparsable uint64 // packets that were not valid SIP
	Invalid    uint64 // SIP messages missing mandatory headers or with a bad Via
	Filtered   uint64 // messages dropped by the Accept hook (blocked sources)
}

type counters struct {
	received, sent, sendErrors, unparsable, invalid, filtered atomic.Uint64
}

func (c *counters) snapshot() Stats {
	return Stats{
		Received: c.received.Load(), Sent: c.sent.Load(), SendErrors: c.sendErrors.Load(),
		Unparsable: c.unparsable.Load(), Invalid: c.invalid.Load(), Filtered: c.filtered.Load(),
	}
}

// noticer rate-limits warnings to one per key per minute.
type noticer struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (n *noticer) allow(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if n.last == nil || len(n.last) > 10000 {
		n.last = map[string]time.Time{}
	}
	if t, ok := n.last[key]; ok && now.Sub(t) < time.Minute {
		return false
	}
	n.last[key] = now
	return true
}

// firstLine returns a printable summary of a packet for logs.
func firstLine(b []byte) string {
	s := string(b)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return '.'
		}
		return r
	}, s)
}
