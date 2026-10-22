package core

import (
	"errors"
	"slices"
)

// Hear streams what one party of a call says: 20 ms frames of 8 kHz audio
// as they arrive from that leg (live transcription listens this way). The
// stream drops frames rather than slow the call down. stop ends it; it
// also ends, closed, when the leg hangs up.
func (c *Core) Hear(uuid string) (frames <-chan []int16, stop func(), err error) {
	ch := c.Channel(uuid)
	if ch == nil || ch.IsHungUp() {
		return nil, nil, errors.New("no such call")
	}
	q := make(chan []int16, 100)
	ch.mu.Lock()
	ch.hears = append(ch.hears, q)
	ch.mu.Unlock()
	refreshTaps(ch)
	done := make(chan struct{})
	stopped := false
	stop = func() {
		ch.mu.Lock()
		if stopped {
			ch.mu.Unlock()
			return
		}
		stopped = true
		ch.hears = slices.DeleteFunc(ch.hears, func(x chan []int16) bool { return x == q })
		ch.mu.Unlock()
		close(done)
		refreshTaps(ch)
	}
	out := make(chan []int16, 100)
	go func() {
		defer close(out)
		for {
			select {
			case f := <-q:
				select {
				case out <- f:
				default: // the listener is behind: drop the frame
				}
			case <-done:
				return
			case <-ch.Done():
				stop()
				return
			}
		}
	}()
	return out, stop, nil
}
