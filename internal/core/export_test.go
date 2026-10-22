package core

import "time"

// CheckMediaNow runs the media checks of the housekeeping loop at once.
func (c *Core) CheckMediaNow() { c.checkMediaTimeouts() }

// SetNoRTPAfter changes how soon a silent leg is reported; call it before
// starting a switch and restore it after the switch stopped.
func SetNoRTPAfter(d time.Duration) (restore func()) {
	old := noRTPAfter
	noRTPAfter = d
	return func() { noRTPAfter = old }
}
