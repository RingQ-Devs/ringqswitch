package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Audio conferences: every member's audio is decoded, mixed every 20 ms,
// and each member hears the mix without their own voice. Members press 0
// to mute or unmute themselves and # to leave. Rooms are created on first
// join and removed when the last member leaves.

type confMember struct {
	ch     *Channel
	muted  bool
	joined time.Time
}

type confRoom struct {
	key     string
	name    string
	domain  string
	mu      sync.Mutex
	members map[*confMember]bool
	stop    chan struct{}
	rec     *media.WAVWriter
	recPath string
}

type conferences struct {
	mu    sync.Mutex
	rooms map[string]*confRoom
}

const (
	toneJoin  = "tone_stream://%(100,50,600);%(100,0,900);v=-12;loops=1"
	toneLeave = "tone_stream://%(100,50,900);%(100,0,600);v=-12;loops=1"
)

// conference <name>[@profile][+<pin>][+flags{...}] (the dialplan syntax).
// The PIN may also come from the room's settings (v_conferences).
func appConference(ctx context.Context, c *Core, ch *Channel, data string) error {
	spec := strings.TrimSpace(data)
	parts := strings.Split(spec, "+")
	name, _, _ := strings.Cut(parts[0], "@")
	ch.flow("conference", "Conference "+name)
	pin := ""
	record := false
	for _, p := range parts[1:] {
		switch {
		case strings.HasPrefix(p, "flags{"):
			record = strings.Contains(p, "record")
		case p != "":
			pin = p
		}
	}
	domain := ch.Var("domain_name")
	if room := c.config().Features.Conference(name, domain); room != nil {
		name = room.Name
		pin = firstNonEmpty(pin, room.PIN)
		record = record || room.Record
	}
	if name == "" {
		return fmt.Errorf("conference: missing room name")
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	if pin != "" && !c.askPIN(ctx, ch, pin) {
		_, _ = c.play(ctx, ch, toneErr, false)
		ch.Hangup(CauseCallRejected)
		return nil
	}
	room := c.joinRoom(domain, name, record)
	m := &confMember{ch: ch, joined: time.Now()}
	c.broadcastRoom(room, toneJoin)
	room.mu.Lock()
	room.members[m] = true
	count := len(room.members)
	room.mu.Unlock()
	ch.SetVar("conference_name", name)
	ch.log.Info("joined conference", "room", name, "members", count)
	c.recordEvent("call", fmt.Sprintf("%s joined conference %s (%d members)", ch.Var("caller_id_number"), name, count))
	c.confEvent("add-member", room, m)
	defer func() {
		c.leaveRoom(room, m)
		c.confEvent("del-member", room, m)
		c.broadcastRoom(room, toneLeave)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case d := <-ch.dtmf:
			switch d {
			case '0':
				room.mu.Lock()
				m.muted = !m.muted
				muted := m.muted
				room.mu.Unlock()
				ch.log.Info("conference mute", "room", name, "muted", muted)
			case '#':
				return nil
			}
		}
	}
}

// askPIN collects up to three PIN attempts terminated by #.
func (c *Core) askPIN(ctx context.Context, ch *Channel, pin string) bool {
	for try := 0; try < 3; try++ {
		ch.FlushDTMF()
		if _, err := c.play(ctx, ch, toneBeep, false); err != nil {
			return false
		}
		got, err := collectDigits(ctx, ch, 0, 12, "#", 10*time.Second, 5*time.Second)
		if err != nil {
			return false
		}
		if got == pin {
			return true
		}
		_, _ = c.play(ctx, ch, toneErr, false)
	}
	return false
}

func (c *Core) joinRoom(domain, name string, record bool) *confRoom {
	key := strings.ToLower(name + "@" + domain)
	c.conf.mu.Lock()
	defer c.conf.mu.Unlock()
	if c.conf.rooms == nil {
		c.conf.rooms = map[string]*confRoom{}
	}
	if r := c.conf.rooms[key]; r != nil {
		return r
	}
	r := &confRoom{key: key, name: name, domain: domain, members: map[*confMember]bool{}, stop: make(chan struct{})}
	if record {
		path := filepath.Join(c.config().Settings.RecordingsDir, domain, "conference",
			fmt.Sprintf("%s-%s-%s.wav", name, time.Now().Format("20060102-150405"), sip.NewTag()))
		if rec, err := newWAV(path); err == nil {
			r.rec, r.recPath = rec, path
		} else {
			c.log.Warn("conference recording failed", "room", name, "err", err)
		}
	}
	c.conf.rooms[key] = r
	go c.mixRoom(r)
	c.log.Info("conference started", "room", name, "domain", domain)
	return r
}

func newWAV(path string) (*media.WAVWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return media.CreateWAV(path)
}

func (c *Core) leaveRoom(r *confRoom, m *confMember) {
	r.mu.Lock()
	delete(r.members, m)
	empty := len(r.members) == 0
	r.mu.Unlock()
	if !empty {
		return
	}
	c.conf.mu.Lock()
	if c.conf.rooms[r.key] == r {
		delete(c.conf.rooms, r.key)
		close(r.stop)
	}
	c.conf.mu.Unlock()
	c.log.Info("conference ended", "room", r.name)
}

// broadcastRoom plays a short tone to every member.
func (c *Core) broadcastRoom(r *confRoom, tone string) {
	src, err := media.OpenSource(tone, "")
	if err != nil {
		return
	}
	var frames [][]int16
	buf := make([]int16, media.FrameSamples)
	for {
		n, err := src.Read(buf)
		if n == 0 || err != nil {
			break
		}
		f := make([]int16, media.FrameSamples)
		copy(f, buf[:n])
		frames = append(frames, f)
	}
	r.mu.Lock()
	members := make([]*confMember, 0, len(r.members))
	for m := range r.members {
		members = append(members, m)
	}
	r.mu.Unlock()
	go func() {
		for _, f := range frames {
			for _, m := range members {
				m.ch.ep.TryWriteFrame(f)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

// mixRoom mixes the room's audio every 20 ms until the room closes.
func (c *Core) mixRoom(r *confRoom) {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	defer func() {
		if r.rec != nil {
			_ = r.rec.Close()
			c.log.Info("conference recording saved", "room", r.name, "path", r.recPath)
		}
	}()
	type in struct {
		m     *confMember
		frame []int16
	}
	sum := make([]int32, media.FrameSamples)
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
		}
		r.mu.Lock()
		ins := make([]in, 0, len(r.members))
		for m := range r.members {
			var f []int16
			if m.ch.ep != nil {
				f = m.ch.ep.TryReadFrame()
			}
			if m.muted {
				f = nil
			}
			ins = append(ins, in{m, f})
		}
		r.mu.Unlock()
		for i := range sum {
			sum[i] = 0
		}
		for _, x := range ins {
			for i, s := range x.frame {
				if i < len(sum) {
					sum[i] += int32(s)
				}
			}
		}
		for _, x := range ins {
			out := make([]int16, media.FrameSamples)
			for i := range out {
				v := sum[i]
				if i < len(x.frame) {
					v -= int32(x.frame[i]) // nobody hears themselves
				}
				out[i] = clip16(v)
			}
			x.m.ch.ep.TryWriteFrame(out)
		}
		if r.rec != nil {
			mix := make([]int16, media.FrameSamples)
			for i := range mix {
				mix[i] = clip16(sum[i])
			}
			_ = r.rec.Write(mix)
		}
	}
}

func clip16(v int32) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	}
	return int16(v)
}

func (c *Core) confEvent(action string, r *confRoom, m *confMember) {
	e := NewEvent(EvCustom, "conference::maintenance")
	e.Add("Action", action)
	e.Add("Conference-Name", r.name)
	e.Add("Conference-Domain", r.domain)
	e.Add("Unique-ID", m.ch.UUID)
	e.Add("Caller-Caller-ID-Number", m.ch.Var("caller_id_number"))
	e.Add("Caller-Caller-ID-Name", m.ch.Var("caller_id_name"))
	r.mu.Lock()
	e.Add("Conference-Size", fmt.Sprint(len(r.members)))
	r.mu.Unlock()
	c.events.Publish(e)
}

// ConferenceMember is the live view of a conference participant.
type ConferenceMember struct {
	UUID      string `json:"uuid"`
	CIDName   string `json:"cid_name"`
	CIDNumber string `json:"cid_number"`
	Muted     bool   `json:"muted"`
	Joined    int64  `json:"joined"`
}

// ConferenceRoom is the live view of a running conference.
type ConferenceRoom struct {
	Name      string             `json:"name"`
	Domain    string             `json:"domain"`
	Recording bool               `json:"recording"`
	Members   []ConferenceMember `json:"members"`
}

// Conferences lists running conferences ("" = all domains).
func (c *Core) Conferences(domain string) []ConferenceRoom {
	c.conf.mu.Lock()
	rooms := make([]*confRoom, 0, len(c.conf.rooms))
	for _, r := range c.conf.rooms {
		if domain == "" || strings.EqualFold(r.domain, domain) {
			rooms = append(rooms, r)
		}
	}
	c.conf.mu.Unlock()
	out := []ConferenceRoom{}
	for _, r := range rooms {
		info := ConferenceRoom{Name: r.name, Domain: r.domain, Recording: r.rec != nil}
		r.mu.Lock()
		for m := range r.members {
			info.Members = append(info.Members, ConferenceMember{UUID: m.ch.UUID, CIDName: m.ch.Var("caller_id_name"),
				CIDNumber: m.ch.Var("caller_id_number"), Muted: m.muted, Joined: m.joined.Unix()})
		}
		r.mu.Unlock()
		sort.Slice(info.Members, func(i, j int) bool { return info.Members[i].Joined < info.Members[j].Joined })
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ConferenceMute mutes or unmutes a member (by channel uuid).
func (c *Core) ConferenceMute(uuid string, muted bool) bool {
	c.conf.mu.Lock()
	defer c.conf.mu.Unlock()
	for _, r := range c.conf.rooms {
		r.mu.Lock()
		for m := range r.members {
			if m.ch.UUID == uuid {
				m.muted = muted
				r.mu.Unlock()
				return true
			}
		}
		r.mu.Unlock()
	}
	return false
}
