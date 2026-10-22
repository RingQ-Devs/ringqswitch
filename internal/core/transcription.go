package core

import (
	"path/filepath"
	"strings"
	"time"
)

// Call transcription: the web server says which calls are transcribed.
// Those are recorded in stereo, each party on its own channel (the caller,
// Speaker A, on the left; who answered, Speaker B, on the right), so the
// transcript tells the speakers apart. A call that is not recorded yet is
// recorded from the moment it is bridged; a call already recorded
// (record_session, a DID's "Record calls") is recorded in stereo.
//
// Variables on the recorded (A) leg, read when the call ends:
// transcribe=true, transcribe_customer (A or B: the outside party, if
// any), transcribe_a and transcribe_b (who the speakers are).

// TranscribeFunc reports whether calls of a domain are transcribed;
// external: the call involves a trunk (in or out), not only extensions.
type TranscribeFunc func(domain string, external bool) bool

// SetTranscription installs the function (nil: nothing is transcribed).
func (c *Core) SetTranscription(fn TranscribeFunc) {
	if fn == nil {
		c.transcribe.Store(nil)
		return
	}
	c.transcribe.Store(&fn)
}

func (c *Core) transcribes(ch *Channel, external bool) bool {
	fn := c.transcribe.Load()
	if fn == nil {
		return false
	}
	return (*fn)(c.domainOf(ch), external)
}

func (c *Core) domainOf(ch *Channel) string {
	if d := ch.Var("domain_name"); d != "" {
		return d
	}
	return c.config().Directory.Domain
}

// outside reports whether a leg is the trunk side of a call.
func outside(ch *Channel) bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.gateway != nil {
		return true
	}
	return ch.Direction == DirectionInbound && ch.vars["call_direction"] == "inbound"
}

func externalLeg(ch *Channel) bool {
	switch ch.Var("call_direction") {
	case "inbound", "outbound":
		return true
	}
	return outside(ch)
}

// recordForTranscription runs when a and b are bridged: it starts the
// stereo recording of a call to transcribe (on a) and notes who speaks.
func (c *Core) recordForTranscription(a, b *Channel) {
	external := externalLeg(a) || externalLeg(b)
	if !c.transcribes(a, external) {
		return
	}
	customer := ""
	switch {
	case outside(a):
		customer = "A"
	case outside(b):
		customer = "B"
	}
	ai, bi := a.identity(), b.identity()
	a.mu.Lock()
	rec := a.rec
	if a.vars["transcribe_b"] == "" { // the first party it was bridged to
		a.vars["transcribe_a"] = joinName(ai[1], ai[2])
		a.vars["transcribe_b"] = joinName(b.vars["callee_id_name"], firstNonEmpty(b.vars["callee_id_number"], bi[3]))
		a.vars["transcribe_customer"] = customer
	}
	a.mu.Unlock()
	if rec != nil {
		if rec.stereo {
			a.SetVar("transcribe", "true")
		}
		return
	}
	now := time.Now()
	path := filepath.Join(c.config().Settings.RecordingsDir, c.domainOf(a), "archive",
		now.Format("2006"), now.Format("Jan"), now.Format("02"), a.UUID+".wav")
	r, err := newSessionRecorder(path, true)
	if err != nil {
		a.log.Warn("transcription: cannot record the call", "path", path, "err", err)
		return
	}
	a.mu.Lock()
	if a.rec != nil { // raced with record_session
		a.mu.Unlock()
		r.close()
		return
	}
	a.rec = r
	a.vars["record_path"] = filepath.Dir(path)
	a.vars["record_name"] = filepath.Base(path)
	a.vars["record_session"] = "true"
	a.vars["record_stereo"] = "true"
	a.vars["transcribe"] = "true"
	a.mu.Unlock()
	a.log.Info("recording the call for its transcript", "path", path)
}

// joinName names a party: "Jay Bayron (1000)"; a name that already holds
// the number ("Ext 1000") stays as it is.
func joinName(name, number string) string {
	switch {
	case name == "" || name == number:
		return number
	case number == "" || strings.Contains(name, number):
		return name
	}
	return name + " (" + number + ")"
}
