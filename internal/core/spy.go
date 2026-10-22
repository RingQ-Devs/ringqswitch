package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// Call monitoring for supervisors: a supervisor's phone is rung and joined
// to a call in progress.
//
//	listen   the supervisor hears both parties; nobody hears the supervisor
//	whisper  the supervisor also talks to the agent (the caller does not hear it)
//	barge    three-way: everybody hears everybody
//
// While monitoring, the supervisor presses 1, 2 or 3 to switch between
// them. The call's two legs are mixed locally (G.711), and reconnected
// directly when the supervisor hangs up.

type spySession struct {
	mu    sync.Mutex
	mode  string
	super *Channel
	call  string
}

type spyRooms struct {
	mu     sync.Mutex
	byCall map[string]*spySession
}

func validSpyMode(m string) bool { return m == "listen" || m == "whisper" || m == "barge" }

// Monitor rings ext@domain and joins it to the call of channel uuid.
func (c *Core) Monitor(uuid, mode, ext, domain, by string) error {
	if !validSpyMode(mode) {
		return fmt.Errorf("mode: listen, whisper or barge")
	}
	a := c.Channel(uuid)
	if a == nil {
		return fmt.Errorf("the call has ended")
	}
	b := a.Partner()
	if b == nil || !a.Answered() || !b.Answered() {
		return fmt.Errorf("the call is not connected yet")
	}
	for _, ch := range []*Channel{a, b} {
		if ch.ep == nil {
			return fmt.Errorf("the call has no media")
		}
		if cd, ok := ch.ep.SendCodec(); !ok || !media.CanTerminate(cd.Name) {
			return fmt.Errorf("this call uses a codec that cannot be monitored (only G.711 can)")
		}
	}
	key := callKey(a, b)
	c.spy.mu.Lock()
	if c.spy.byCall == nil {
		c.spy.byCall = map[string]*spySession{}
	}
	if s := c.spy.byCall[key]; s != nil {
		// Already monitored: switch the mode.
		c.spy.mu.Unlock()
		s.mu.Lock()
		s.mode = mode
		s.mu.Unlock()
		c.recordEvent("call", fmt.Sprintf("%s switched monitoring of %s to %s", by, a.Var("caller_id_number"), mode))
		return nil
	}
	s := &spySession{mode: mode, call: key}
	c.spy.byCall[key] = s
	c.spy.mu.Unlock()

	go func() {
		defer func() {
			c.spy.mu.Lock()
			delete(c.spy.byCall, key)
			c.spy.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		dial := fmt.Sprintf("{origination_caller_id_name=Monitor %s,origination_caller_id_number=%s,sip_h_Call-Info=<sip:%s>;answer-after=0,sip_h_Alert-Info=info=alert-autoanswer}user/%s@%s",
			strings.Map(safeCID, a.Var("caller_id_number")), strings.Map(safeCID, a.Var("caller_id_number")), domain, ext, domain)
		sup, cause := c.originate(ctx, nil, dial, map[string]string{"call_timeout": "30"})
		if sup == nil {
			c.recordEvent("call", fmt.Sprintf("monitoring by %s: extension %s did not answer (%s)", by, ext, cause))
			return
		}
		s.mu.Lock()
		s.super = sup
		s.mu.Unlock()
		c.log.Info("monitoring call", "call", a.UUID, "supervisor", ext, "mode", mode, "by", by)
		c.recordEvent("call", fmt.Sprintf("%s (extension %s) started %s on the call %s ↔ %s", by, ext, mode,
			a.Var("caller_id_number"), b.Var("caller_id_number")))
		c.spyLoop(s, a, b, sup)
		if !sup.IsHungUp() {
			sup.Hangup(CauseNormalClearing)
		}
	}()
	return nil
}

func safeCID(r rune) rune {
	if r == ',' || r == '}' || r == '{' || r == '=' || r == '<' || r == '>' {
		return -1
	}
	return r
}

func callKey(a, b *Channel) string {
	if a.UUID < b.UUID {
		return a.UUID + "|" + b.UUID
	}
	return b.UUID + "|" + a.UUID
}

// isAgentLeg guesses which leg is the agent (an extension of ours) for
// whisper: a leg rung on a user, or placed by a registered user.
func isAgentLeg(ch *Channel) bool {
	return ch.Var("dialed_user") != "" || (ch.Var("user_name") != "" && ch.Var("sip_authorized") == "true")
}

func (c *Core) spyLoop(s *spySession, a, b, sup *Channel) {
	agentA := isAgentLeg(a) && !isAgentLeg(b)
	media.Unbridge(a.ep, b.ep)
	a.ep.SetMode(media.ModeLocal)
	b.ep.SetMode(media.ModeLocal)
	sup.ep.SetMode(media.ModeLocal)
	defer func() {
		if !a.IsHungUp() && !b.IsHungUp() && a.Partner() == b && b.Partner() == a {
			a.mu.Lock()
			held := a.held || b.held
			a.mu.Unlock()
			if !held {
				media.Bridge(a.ep, b.ep)
			}
		}
		c.log.Info("monitoring ended", "call", a.UUID)
	}()
	sup.FlushDTMF()
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	mix := func(parts ...[]int16) []int16 {
		out := make([]int16, media.FrameSamples)
		for i := range out {
			var v int32
			for _, p := range parts {
				if i < len(p) {
					v += int32(p[i])
				}
			}
			out[i] = clip16(v)
		}
		return out
	}
	for {
		select {
		case <-sup.Done():
			return
		case <-a.Done():
			return
		case <-b.Done():
			return
		case d := <-sup.dtmf:
			s.mu.Lock()
			switch d {
			case '1':
				s.mode = "listen"
			case '2':
				s.mode = "whisper"
			case '3':
				s.mode = "barge"
			}
			s.mu.Unlock()
			continue
		case <-t.C:
		}
		if a.Partner() != b {
			return // transferred, or the bridge was broken
		}
		a.mu.Lock()
		held := a.held
		a.mu.Unlock()
		b.mu.Lock()
		held = held || b.held
		b.mu.Unlock()
		if held {
			return
		}
		s.mu.Lock()
		mode := s.mode
		s.mu.Unlock()
		fa, fb, fs := a.ep.TryReadFrame(), b.ep.TryReadFrame(), sup.ep.TryReadFrame()
		toA, toB := mix(fb), mix(fa)
		switch mode {
		case "barge":
			toA, toB = mix(fb, fs), mix(fa, fs)
		case "whisper":
			if agentA {
				toA = mix(fb, fs)
			} else {
				toB = mix(fa, fs)
			}
		}
		a.ep.TryWriteFrame(toA)
		b.ep.TryWriteFrame(toB)
		sup.ep.TryWriteFrame(mix(fa, fb))
	}
}
