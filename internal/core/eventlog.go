package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LogEntry is one line of the operational event log shown in the web UI:
// registrations, calls, queue activity, agent status changes, gateways and
// security events.
type LogEntry struct {
	Time int64  `json:"time"` // unix milliseconds
	Kind string `json:"kind"` // registration, call, queue, agent, gateway, security, system
	Text string `json:"text"`
}

const eventLogSize = 5000

type eventLog struct {
	mu      sync.Mutex
	entries []LogEntry
	next    int
	full    bool
}

func (l *eventLog) add(kind, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make([]LogEntry, eventLogSize)
	}
	l.entries[l.next] = LogEntry{Time: time.Now().UnixMilli(), Kind: kind, Text: text}
	l.next = (l.next + 1) % eventLogSize
	if l.next == 0 {
		l.full = true
	}
}

// recordEvent adds a line to the operational event log.
func (c *Core) recordEvent(kind, text string) { c.elog.add(kind, text) }

// RecordEvent adds a line to the event log (for the web interface).
func (c *Core) RecordEvent(kind, text string) { c.elog.add(kind, text) }

// EventLog returns the most recent log entries (newest first), optionally
// filtered by kind and by a case-insensitive text search.
func (c *Core) EventLog(kind, search string, limit int) []LogEntry {
	l := &c.elog
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > eventLogSize {
		limit = 500
	}
	n := l.next
	if l.full {
		n = eventLogSize
	}
	search = strings.ToLower(search)
	out := []LogEntry{}
	for i := 0; i < n && len(out) < limit; i++ {
		idx := (l.next - 1 - i + eventLogSize) % eventLogSize
		e := l.entries[idx]
		if kind != "" && e.Kind != kind {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(e.Text), search) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// mohFiles resolves a music-on-hold setting to WAV files to play in turn:
// "local_stream://<name>" (RingQ) means the folder sounds/music/<name>,
// a folder or file path plays those files, tone_stream:// plays a tone.
// An empty result means the built-in music.
func (c *Core) mohFiles(ch *Channel, spec string) []string {
	spec = strings.TrimSpace(ch.Expand(spec))
	sounds := c.config().Settings.SoundsDir
	switch {
	case spec == "" || spec == "default":
		spec = "local_stream://default"
	case strings.HasPrefix(spec, "tone_stream://"):
		return []string{spec}
	case spec == "silence" || strings.HasPrefix(spec, "silence_stream://"):
		return []string{"silence_stream://-1"}
	}
	var dirs []string
	if name, ok := strings.CutPrefix(spec, "local_stream://"); ok {
		name = strings.Trim(name, "/")
		if name == "" {
			name = "default"
		}
		dirs = append(dirs, filepath.Join(sounds, "music", name), filepath.Join(sounds, "music", name, "8000"))
		if name == "default" {
			dirs = append(dirs, filepath.Join(sounds, "music"), filepath.Join(sounds, "music", "8000"))
		}
	} else if st, err := os.Stat(spec); err == nil && st.IsDir() {
		dirs = append(dirs, spec, filepath.Join(spec, "8000"))
	} else if p := c.resolveSound(ch, spec); p != "" {
		return []string{p}
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".wav") {
				files = append(files, filepath.Join(d, e.Name()))
			}
		}
		if len(files) > 0 {
			sort.Strings(files)
			return files
		}
	}
	return nil
}
