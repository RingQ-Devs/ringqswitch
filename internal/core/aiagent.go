package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// AI agents: the "ai_agent" application answers the call and hands it to an
// AI agent run by the web server (SetAIAgent). The agent talks with the
// caller through a Talk: what the caller says, frame by frame as it
// arrives, and what to say to them, played in real time and cut short the
// moment the caller speaks over it.
//
//	<action application="ai_agent" data="<ai agent uuid>"/>

// AIAgentFunc runs a call with an AI agent until the conversation ends
// (the caller hangs up, the agent ends the call, or transfers it).
type AIAgentFunc func(ctx context.Context, t *Talk, agent string) error

// SetAIAgent installs the function that runs AI agents (nil: none).
func (c *Core) SetAIAgent(fn AIAgentFunc) {
	if fn == nil {
		c.aiAgent.Store(nil)
		return
	}
	c.aiAgent.Store(&fn)
}

func appAIAgent(ctx context.Context, c *Core, ch *Channel, data string) error {
	fn := c.aiAgent.Load()
	if fn == nil {
		ch.log.Warn("ai_agent: AI agents need the web interface and its database")
		return errors.New("ai_agent: not available")
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	t := newTalk(ctx, ch)
	defer t.close()
	return (*fn)(ctx, t, strings.TrimSpace(data))
}

// Talk is a conversation with the caller of a channel.
type Talk struct {
	ch   *Channel
	ctx  context.Context
	mu   sync.Mutex
	out  []int16 // still to say
	said int     // samples said since the last Hush
	wake chan struct{}
	done chan struct{}
	once sync.Once
}

func newTalk(ctx context.Context, ch *Channel) *Talk {
	t := &Talk{ch: ch, ctx: ctx, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go t.speak()
	return t
}

func (t *Talk) close() { t.once.Do(func() { close(t.done) }) }

// UUID is the call's channel uuid.
func (t *Talk) UUID() string { return t.ch.UUID }

// Var reads a channel variable; Vars all of them.
func (t *Talk) Var(name string) string        { return t.ch.Var(name) }
func (t *Talk) Vars() map[string]string       { return t.ch.Vars() }
func (t *Talk) SetVar(name, value string)     { t.ch.SetVar(name, value) }
func (t *Talk) Note(kind, text string)        { t.ch.flow(kind, text) }
func (t *Talk) Context() context.Context      { return t.ctx }
func (t *Talk) Digits() <-chan rune           { return t.ch.dtmf }
func (t *Talk) HungUp() bool                  { return t.ch.IsHungUp() }
func (t *Talk) Done() <-chan struct{}         { return t.ch.Done() }
func (t *Talk) Hangup()                       { t.ch.Hangup(CauseNormalClearing) }
func (t *Talk) Transfer(dest, ctxName string) { t.ch.core.transfer(t.ch, dest, ctxName) }

// Listen returns the next 20 ms of what the caller says (8 kHz).
func (t *Talk) Listen() ([]int16, error) {
	return t.ch.ep.ReadFrame(t.ctx)
}

// ListenWait is Listen waiting at most max: nil (and no error) when the
// phone sent nothing in that time (silence suppression).
func (t *Talk) ListenWait(max time.Duration) ([]int16, error) {
	ctx, cancel := context.WithTimeout(t.ctx, max)
	defer cancel()
	f, err := t.ch.ep.ReadFrame(ctx)
	if err != nil && t.ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, nil
	}
	return f, err
}

// Say queues audio (8 kHz) to play to the caller after what is queued.
func (t *Talk) Say(pcm []int16) {
	if len(pcm) == 0 {
		return
	}
	t.mu.Lock()
	t.out = append(t.out, pcm...)
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// Hush drops what is still to be said (the caller spoke over it) and
// reports how much was said since the last Hush.
func (t *Talk) Hush() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	said := t.said
	t.out, t.said = nil, 0
	return time.Duration(said) * time.Second / 8000
}

// Said is how much was said since the last Hush.
func (t *Talk) Said() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Duration(t.said) * time.Second / 8000
}

// Pending is how much is still to be said.
func (t *Talk) Pending() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Duration(len(t.out)) * time.Second / 8000
}

// WaitSaid waits until everything queued has been said (or max passed, or
// the call ended).
func (t *Talk) WaitSaid(max time.Duration) {
	deadline := time.Now().Add(max)
	for t.Pending() > 0 && time.Now().Before(deadline) {
		select {
		case <-t.ctx.Done():
			return
		case <-t.ch.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	// What the media clock still holds (two frames).
	select {
	case <-t.ctx.Done():
	case <-time.After(60 * time.Millisecond):
	}
}

// speak plays the queued audio in real time: the endpoint's queue holds two
// frames, so a Hush silences the caller's ear within about 60 ms.
func (t *Talk) speak() {
	for {
		t.mu.Lock()
		if len(t.out) == 0 {
			t.mu.Unlock()
			select {
			case <-t.wake:
				continue
			case <-t.ctx.Done():
				return
			case <-t.done:
				return
			}
		}
		n := min(media.FrameSamples, len(t.out))
		f := make([]int16, media.FrameSamples)
		copy(f, t.out[:n])
		t.out = t.out[n:]
		t.said += n
		t.mu.Unlock()
		if err := t.ch.ep.WriteFrame(t.ctx, f); err != nil {
			return
		}
	}
}
