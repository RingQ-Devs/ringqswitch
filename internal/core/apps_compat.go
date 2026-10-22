package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// Applications that RingQ dialplans use. Where RingQswitch
// has no equivalent feature the application is accepted and does nothing,
// so stored dialplans run unchanged; see docs/DATABASE.md for the list.

// inlineApps may run during the dialplan hunt (inline="true").
var inlineApps = map[string]bool{
	"set": true, "unset": true, "export": true, "multiset": true, "set_global": true,
	"log": true, "hash": true, "db": true, "event": true, "set_user": true,
	"set_profile_var": true, "push": true, "unshift": true, "limit": true,
}

// noopApps are accepted and ignored (features without an equivalent yet, or
// that only matter to another switch's internals).
var noopApps = []string{
	"bind_meta_app", "bind_digit_action", "digit_action_set_realm", "clear_digit_action",
	"set_zombie_exec", "presence", "privacy", "set_audio_level", "start_dtmf", "stop_dtmf",
	"jitterbuffer", "set_mute", "enable_heartbeat", "rename", "eval", "send_display",
	"tone_detect", "stop_tone_detect", "sound_test", "wait_for_silence", "detect_speech",
	"enable_keepalive", "media_reset", "sched_heartbeat", "set_media_stats", "unbind_meta_app",
	"remove_bugs", "flush_dtmf", "gentones_stop", "spandsp_start_dtmf", "spandsp_stop_dtmf",
	"start_dtmf_generate", "stop_dtmf_generate", "displace_session", "stop_displace_session",
	"hold", "unhold", "deduplicate_dtmf", "mkdir", "group",
}

func compatApps(apps map[string]App) {
	for _, n := range noopApps {
		apps[n] = appNoop
	}
	for name, fn := range map[string]App{
		"export":              appExport,
		"multiset":            appMultiset,
		"set_global":          appSetGlobal,
		"set_profile_var":     appSet,
		"push":                appPush,
		"unshift":             appPush,
		"hash":                appHash,
		"db":                  appDB,
		"limit":               appLimit,
		"event":               appEvent,
		"set_user":            appSetUser,
		"sched_hangup":        appSchedHangup,
		"sched_transfer":      appSchedTransfer,
		"record_session":      appRecordSession,
		"stop_record_session": appStopRecordSession,
		"read":                appRead,
		"redirect":            appRedirect,
		"endless_playback":    appEndlessPlayback,
		"gentones":            appGentones,
		"say":                 appUnsupported,
		"phrase":              appUnsupported,
		"speak":               appUnsupported,
		"lua":                 appLua,
		"failure_handler":     appFailureHandler,
		"ring_group":          appRingGroup,
		"uuid_bridge_to":      appBridgeTo,
		"ivr":                 appIVR,
		"voicemail":           appVoicemail,
		"valet_park":          appValetPark,
		"rq_pickup":           appPickup,
		"intercept":           appIntercept,
		"callcenter":          appCallcenter,
		"rq_agent_status":     appAgentStatus,
		"rq_dnd":              appDND,
		"rq_call_return":      appCallReturn,
		"rq_redial":           appRedial,
		"conference":          appConference,
		"fifo":                appUnsupported,

		"eavesdrop": appUnsupported,
		"att_xfer":  appUnsupported,
		"system":    appUnsupported,
	} {
		apps[name] = fn
	}
}

func appNoop(context.Context, *Core, *Channel, string) error { return nil }

func appUnsupported(_ context.Context, _ *Core, ch *Channel, data string) error {
	app := ch.currentApp()
	ch.log.Warn("application not supported by RingQswitch yet; continuing", "app", app, "data", data)
	return nil
}

func (ch *Channel) currentApp() string {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.app
}

// export [nolocal:]name=value sets a variable and marks it for B legs.
func appExport(_ context.Context, _ *Core, ch *Channel, data string) error {
	name, value, ok := strings.Cut(data, "=")
	if !ok {
		return fmt.Errorf("export: expected name=value, got %q", data)
	}
	name = strings.TrimSpace(name)
	local := true
	if n, ok := strings.CutPrefix(name, "nolocal:"); ok {
		name, local = n, false
	}
	if local {
		ch.SetVar(name, value)
	} else {
		ch.SetVar("nolocal_"+name, value)
	}
	ch.addExport(name)
	return nil
}

func (ch *Channel) addExport(name string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	for _, n := range strings.Split(ch.vars["export_vars"], ",") {
		if n == name {
			return
		}
	}
	if ch.vars["export_vars"] == "" {
		ch.vars["export_vars"] = name
	} else {
		ch.vars["export_vars"] += "," + name
	}
}

// multiset a=1 b=2, or ^^<sep>a=1<sep>b=2.
func appMultiset(ctx context.Context, c *Core, ch *Channel, data string) error {
	var items []string
	if strings.HasPrefix(data, "^^") && len(data) > 2 {
		items = strings.Split(data[3:], data[2:3])
	} else {
		items = strings.Fields(data)
	}
	for _, it := range items {
		if strings.Contains(it, "=") {
			_ = appSet(ctx, c, ch, it)
		}
	}
	return nil
}

func appSetGlobal(_ context.Context, c *Core, _ *Channel, data string) error {
	name, value, ok := strings.Cut(data, "=")
	if !ok {
		return fmt.Errorf("set_global: expected name=value")
	}
	c.globals.set(strings.TrimSpace(name), value)
	return nil
}

// push/unshift name=value appends to an array variable (ARRAY::a|:b).
func appPush(_ context.Context, _ *Core, ch *Channel, data string) error {
	name, value, ok := strings.Cut(data, "=")
	if !ok {
		return nil
	}
	name = strings.TrimSpace(name)
	cur := strings.TrimPrefix(ch.Var(name), "ARRAY::")
	if cur == "" {
		ch.SetVar(name, value)
		return nil
	}
	ch.SetVar(name, "ARRAY::"+cur+"|:"+value)
	return nil
}

// hash insert/<realm>/<key>/<value> | delete/<realm>/<key>
func appHash(_ context.Context, c *Core, _ *Channel, data string) error {
	return kvApp(c, "hash", data)
}

func appDB(_ context.Context, c *Core, _ *Channel, data string) error {
	return kvApp(c, "db", data)
}

func kvApp(c *Core, ns, data string) error {
	parts := strings.SplitN(data, "/", 4)
	if len(parts) < 3 {
		return fmt.Errorf("%s: bad arguments %q", ns, data)
	}
	switch strings.ToLower(parts[0]) {
	case "insert":
		v := ""
		if len(parts) == 4 {
			v = parts[3]
		}
		c.hashes.set(ns, parts[1], parts[2], v)
	case "delete":
		c.hashes.del(ns, parts[1], parts[2])
	case "delete_ifmatch":
		if len(parts) == 4 {
			if v, ok := c.hashes.get(ns, parts[1], parts[2]); ok && v == parts[3] {
				c.hashes.del(ns, parts[1], parts[2])
			}
		}
	}
	return nil
}

// limit <backend> <realm> <resource> [<max>[/<interval>]] [<transfer_destination>]
//
// Counts the live channels holding the same realm/resource; above max the
// call goes to the destination ("!CAUSE" or "error/cause" hang up).
func appLimit(_ context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) < 3 {
		return nil
	}
	key := f[1] + "/" + f[2]
	maxCalls := -1
	if len(f) > 3 {
		lim, _, _ := strings.Cut(f[3], "/")
		if n, err := strconv.Atoi(lim); err == nil {
			maxCalls = n
		}
	}
	if maxCalls > 0 {
		n := 0
		for _, other := range c.Channels() {
			if other != ch && other.Var("limit_key") == key {
				n++
			}
		}
		if n >= maxCalls {
			dest := ""
			if len(f) > 4 {
				dest = f[4]
			}
			ch.log.Info("limit reached", "resource", key, "max", maxCalls, "destination", dest)
			ch.SetVar("limit_usage", strconv.Itoa(n))
			switch {
			case strings.HasPrefix(dest, "!"):
				ch.Hangup(ParseCause(dest[1:]))
			case strings.HasPrefix(dest, "error/"):
				ch.Hangup(ParseCause(strings.TrimPrefix(dest, "error/")))
			case dest != "":
				c.transfer(ch, dest, "")
			default:
				ch.Hangup(CauseUserBusy)
			}
			return nil
		}
	}
	ch.SetVar("limit_key", key)
	return nil
}

func appEvent(_ context.Context, c *Core, ch *Channel, data string) error {
	e := NewEvent(EvCustom, "")
	for _, kv := range strings.Split(data, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if strings.EqualFold(k, "Event-Name") {
			e.Name = v
			e.Headers[0][1] = v
			continue
		}
		e.Add(k, v)
	}
	e.Add("Unique-ID", ch.UUID)
	c.events.Publish(e)
	return nil
}

// set_user <user>[@<domain>] [<prefix>] copies a directory user's variables.
func appSetUser(_ context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) == 0 {
		return nil
	}
	id, domain, _ := strings.Cut(f[0], "@")
	if domain == "" {
		domain = ch.Var("domain_name")
	}
	u := c.config().Directory.LookupIn(domain, id)
	if u == nil {
		return fmt.Errorf("set_user: no user %s", f[0])
	}
	prefix := ""
	if len(f) > 1 {
		prefix = f[1]
	}
	for k, v := range u.Vars {
		ch.SetVar(prefix+k, v)
	}
	ch.SetVar(prefix+"user_name", u.ID)
	ch.SetVar(prefix+"domain_name", u.Domain)
	return nil
}

func schedDelay(arg string) (time.Duration, bool) {
	rel := strings.HasPrefix(arg, "+")
	n, err := strconv.ParseInt(strings.TrimPrefix(arg, "+"), 10, 64)
	if err != nil {
		return 0, false
	}
	if rel {
		return time.Duration(n) * time.Second, true
	}
	return time.Until(time.Unix(n, 0)), true
}

// sched_hangup [+]<time> [<cause>]
func appSchedHangup(_ context.Context, _ *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) == 0 {
		return nil
	}
	d, ok := schedDelay(f[0])
	if !ok {
		return fmt.Errorf("sched_hangup: bad time %q", f[0])
	}
	cause := CauseAllottedTimeout
	if len(f) > 1 {
		cause = ParseCause(f[1])
	}
	t := time.AfterFunc(d, func() { ch.Hangup(cause) })
	go func() { <-ch.Done(); t.Stop() }()
	return nil
}

// sched_transfer [+]<time> <extension> [<dialplan>] [<context>]
func appSchedTransfer(_ context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) < 2 {
		return nil
	}
	d, ok := schedDelay(f[0])
	if !ok {
		return nil
	}
	dest, ctxName := f[1], ""
	if len(f) > 3 {
		ctxName = f[3]
	} else if len(f) == 3 {
		ctxName = f[2]
	}
	t := time.AfterFunc(d, func() { c.transfer(ch, dest, ctxName) })
	go func() { <-ch.Done(); t.Stop() }()
	return nil
}

// read <min> <max> <sound> <variable> <timeout_ms> <terminators>
func appRead(ctx context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) < 4 {
		return fmt.Errorf("read: need min max sound variable [timeout] [terminators]")
	}
	minD, _ := strconv.Atoi(f[0])
	maxD, _ := strconv.Atoi(f[1])
	timeout := 5000
	if len(f) > 4 {
		if n, err := strconv.Atoi(f[4]); err == nil && n > 0 {
			timeout = n
		}
	}
	terms := "#"
	if len(f) > 5 {
		terms = f[5]
	}
	if maxD < 1 {
		maxD = 1
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	ch.FlushDTMF()
	var first rune
	if f[2] != "" && f[2] != "silence_stream://0" {
		var err error
		if first, err = c.play(ctx, ch, f[2], true); err != nil {
			ch.log.Debug("read: prompt failed", "err", err)
		}
	}
	digits, err := collectDigits(ctx, ch, first, maxD, terms, time.Duration(timeout)*time.Millisecond, time.Duration(timeout)*time.Millisecond)
	if err != nil {
		return err
	}
	if len(digits) < minD {
		ch.SetVar("read_result", "failure")
		digits = ""
	} else {
		ch.SetVar("read_result", "success")
	}
	ch.SetVar(f[3], digits)
	return nil
}

// redirect <sip uri>: answer an unanswered call with 302 Moved Temporarily.
func appRedirect(_ context.Context, _ *Core, ch *Channel, data string) error {
	if ch.Direction != DirectionInbound || ch.IsAnswered() || ch.inviteTx == nil {
		return errors.New("redirect: only unanswered inbound calls can be redirected")
	}
	target := strings.TrimSpace(data)
	if !strings.HasPrefix(target, "<") {
		target = "<" + target + ">"
	}
	resp := ch.inviteResponse(302, "")
	resp.Add("Contact", target)
	_ = ch.inviteTx.Respond(resp)
	ch.hangup(CauseRedirectionToNewDest, hangupCancelled)
	return nil
}

func appEndlessPlayback(ctx context.Context, c *Core, ch *Channel, data string) error {
	if err := startMedia(ch, true); err != nil {
		return err
	}
	for ctx.Err() == nil {
		if _, err := c.play(ctx, ch, strings.TrimSpace(data), false); err != nil {
			if ctx.Err() == nil {
				// An unplayable file must not spin: wait instead.
				<-ctx.Done()
			}
			return nil
		}
	}
	return nil
}

func appGentones(ctx context.Context, c *Core, ch *Channel, data string) error {
	spec, _, _ := strings.Cut(data, "|")
	return appPlayback(ctx, c, ch, "tone_stream://"+spec)
}

// ---------------------------------------------------------------------------
// lua dispatch: RingQ scripts with a native equivalent

func appLua(ctx context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) == 0 {
		return nil
	}
	script := strings.TrimSuffix(filepath.Base(f[0]), ".lua")
	name := script
	if script == "app" && len(f) > 1 {
		name = f[1]
	}
	switch name {
	case "failure_handler":
		return appFailureHandler(ctx, c, ch, "")
	case "voicemail":
		return appVoicemail(ctx, c, ch, "")
	case "ring_groups", "ring_group":
		return appRingGroup(ctx, c, ch, ch.Var("ring_group_uuid"))
	case "ivr_menu", "ivr_menus":
		return appIVR(ctx, c, ch, ch.Var("ivr_menu_uuid"))
	case "do_not_disturb": // the RingQ portal's *76-*79 dialplans set enabled=true/false/toggle
		return appDND(ctx, c, ch, ch.Var("enabled"))
	case "intercept": // the RingQ portal's **<ext>
		if len(f) > 1 {
			return appPickup(ctx, c, ch, f[1])
		}
		return appPickup(ctx, c, ch, "group")
	case "intercept_group": // the RingQ portal's *8
		return appPickup(ctx, c, ch, "group")
	}
	ch.log.Warn("lua script has no RingQswitch equivalent; continuing", "script", data)
	return nil
}

// appFailureHandler is the RingQ portal's failure_handler.lua: after a failed
// bridge to an extension, follow the extension's busy / no answer / not
// registered forwarding.
func appFailureHandler(ctx context.Context, c *Core, ch *Channel, _ string) error {
	if ch.IsHungUp() || ch.IsAnswered() {
		return nil
	}
	disp := firstNonEmpty(ch.Var("originate_disposition"), ch.Var("last_bridge_hangup_cause"))
	ext := firstNonEmpty(ch.Var("dialed_extension"), ch.Var("destination_number"))
	domain := ch.Var("domain_name")
	userVar := func(name string) string {
		if v, ok := c.userOverride(ext, domain, name); ok {
			return v // changed from a phone or the apps since the last load
		}
		if v := ch.Var(name); v != "" {
			return v
		}
		if u := c.config().Directory.LookupIn(domain, ext); u != nil {
			return u.Vars[name]
		}
		return ""
	}
	var kind string
	switch disp {
	case "USER_BUSY", "CALL_REJECTED":
		kind = "busy"
	case "NO_ANSWER", "NO_USER_RESPONSE", "ORIGINATOR_CANCEL_NO_ANSWER", "ALLOTTED_TIMEOUT":
		kind = "no_answer"
	case "USER_NOT_REGISTERED", "SUBSCRIBER_ABSENT", "UNALLOCATED_NUMBER", "DESTINATION_OUT_OF_ORDER":
		kind = "user_not_registered"
	default:
		return nil
	}
	dest := ""
	if truthy(userVar("forward_" + kind + "_enabled")) {
		dest = userVar("forward_" + kind + "_destination")
	}
	if dest == "" {
		// No forwarding: leave a voicemail when the extension has a box.
		if vm := c.config().Features.Voicemail(ext, domain); vm != nil && vm.Enabled {
			ch.log.Info("to voicemail", "on", kind, "extension", ext)
			return appVoicemail(ctx, c, ch, "default "+domain+" "+ext)
		}
		return nil
	}
	ctxName := firstNonEmpty(ch.Var("user_context"), domain, ch.Var("context"))
	if ctxName == "public" {
		ctxName = domain
	}
	ch.log.Info("call forward", "on", kind, "extension", ext, "destination", dest)
	ch.SetVar("forward_"+kind, "true")
	c.transfer(ch, dest, ctxName)
	return nil
}

// ---------------------------------------------------------------------------
// Session recording

// sessionRecorder mixes the audio received from the caller (read) and from
// the bridged party (write) into a WAV file, mono or stereo.
type sessionRecorder struct {
	path   string
	stereo bool
	read   chan []int16
	write  chan []int16
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	secs   float64
	err    error
}

func newSessionRecorder(path string, stereo bool) (*sessionRecorder, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	channels := 1
	if stereo {
		channels = 2
	}
	w, err := media.CreateWAVChannels(path, channels)
	if err != nil {
		return nil, err
	}
	r := &sessionRecorder{path: path, stereo: stereo, read: make(chan []int16, 16), write: make(chan []int16, 16),
		stop: make(chan struct{}), done: make(chan struct{})}
	go r.run(w)
	return r, nil
}

func offer(q chan []int16, f []int16) {
	select {
	case q <- f:
	default: // keep latency bounded: drop the oldest frame
		select {
		case <-q:
		default:
		}
		select {
		case q <- f:
		default:
		}
	}
}

func take(q chan []int16) []int16 {
	select {
	case f := <-q:
		return f
	default:
		return nil
	}
}

func (r *sessionRecorder) run(w *media.WAVWriter) {
	defer close(r.done)
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	started := false
	for {
		select {
		case <-r.stop:
			// Drain what is buffered so the end of the call is not lost.
			for {
				a, b := take(r.read), take(r.write)
				if a == nil && b == nil {
					break
				}
				r.writeFrame(w, a, b)
			}
			r.secs = float64(w.Samples()) / 8000
			r.err = w.Close()
			return
		case <-t.C:
		}
		a, b := take(r.read), take(r.write)
		if !started {
			if a == nil && b == nil {
				continue // nothing until media flows (ringing time is skipped)
			}
			started = true
		}
		r.writeFrame(w, a, b)
	}
}

func (r *sessionRecorder) writeFrame(w *media.WAVWriter, a, b []int16) {
	n := max(len(a), len(b), media.FrameSamples)
	sample := func(f []int16, i int) int32 {
		if i < len(f) {
			return int32(f[i])
		}
		return 0
	}
	var out []int16
	if r.stereo {
		out = make([]int16, 2*n)
		for i := 0; i < n; i++ {
			out[2*i], out[2*i+1] = int16(sample(a, i)), int16(sample(b, i))
		}
	} else {
		out = make([]int16, n)
		for i := 0; i < n; i++ {
			out[i] = int16(max(-32768, min(32767, sample(a, i)+sample(b, i))))
		}
	}
	_ = w.Write(out)
}

func (r *sessionRecorder) close() (float64, error) {
	r.once.Do(func() { close(r.stop) })
	<-r.done
	return r.secs, r.err
}

// refreshTaps points the audio taps of ch's endpoint at the recorders that
// need it: ch's own (caller audio) and its bridge partner's (other party).
func refreshTaps(ch *Channel) {
	if ch == nil || ch.ep == nil {
		return
	}
	ch.mu.Lock()
	var sinks []chan []int16
	if ch.rec != nil {
		sinks = append(sinks, ch.rec.read)
	}
	sinks = append(sinks, ch.hears...)
	partner := ch.partner
	ch.mu.Unlock()
	if partner != nil {
		partner.mu.Lock()
		if partner.rec != nil {
			sinks = append(sinks, partner.rec.write)
		}
		partner.mu.Unlock()
	}
	if len(sinks) == 0 {
		ch.ep.SetTap(nil)
		return
	}
	ch.ep.SetTap(func(f []int16) {
		for _, s := range sinks {
			offer(s, f)
		}
	})
}

// record_session <path>: record the call (both parties) until hangup or
// stop_record_session. record_stereo=true puts each party on its own
// channel. Only .wav is written; other extensions are replaced.
func appRecordSession(_ context.Context, c *Core, ch *Channel, data string) error {
	path := strings.TrimSpace(data)
	if path == "" {
		return errors.New("record_session: missing path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.config().Settings.RecordingsDir, path)
	}
	if ext := filepath.Ext(path); !strings.EqualFold(ext, ".wav") {
		path = strings.TrimSuffix(path, ext) + ".wav"
	}
	ch.mu.Lock()
	busy := ch.rec != nil
	ch.mu.Unlock()
	if busy {
		return nil
	}
	// A call to transcribe is recorded in stereo (each party on a channel).
	transcribe := c.transcribes(ch, externalLeg(ch))
	rec, err := newSessionRecorder(path, truthy(ch.Var("record_stereo")) || transcribe)
	if err != nil {
		return fmt.Errorf("record_session: %w", err)
	}
	ch.mu.Lock()
	if transcribe {
		ch.vars["transcribe"] = "true"
	}
	ch.rec = rec
	ch.vars["record_path"] = filepath.Dir(path)
	ch.vars["record_name"] = filepath.Base(path)
	ch.vars["record_session"] = "true"
	partner := ch.partner
	ch.mu.Unlock()
	refreshTaps(ch)
	refreshTaps(partner)
	ch.log.Info("recording session", "path", path)
	return nil
}

func appStopRecordSession(_ context.Context, _ *Core, ch *Channel, _ string) error {
	ch.stopRecording()
	return nil
}

func (ch *Channel) stopRecording() {
	ch.mu.Lock()
	rec := ch.rec
	ch.rec = nil
	partner := ch.partner
	ch.mu.Unlock()
	if rec == nil {
		return
	}
	refreshTaps(ch)
	refreshTaps(partner)
	secs, err := rec.close()
	if err != nil {
		ch.log.Warn("recording failed", "path", rec.path, "err", err)
		return
	}
	ch.mu.Lock()
	ch.vars["record_seconds"] = strconv.Itoa(int(secs))
	ch.vars["record_ms"] = strconv.Itoa(int(secs * 1000))
	ch.vars["record_length"] = strconv.Itoa(int(secs + 0.5))
	ch.mu.Unlock()
	ch.log.Info("recording saved", "path", rec.path, "seconds", fmt.Sprintf("%.1f", secs))
}

// runAnswerHooks executes api_on_answer and execute_on_answer.
func (ch *Channel) runAnswerHooks() {
	c := ch.core
	if cmd := ch.Var("api_on_answer"); cmd != "" {
		out := c.API(ch.Expand(cmd))
		ch.log.Debug("api_on_answer", "cmd", cmd, "result", strings.TrimSpace(out))
	}
	if app := ch.Var("execute_on_answer"); app != "" {
		name, data, _ := strings.Cut(strings.TrimSpace(ch.Expand(app)), " ")
		if fn := c.apps[name]; fn != nil && (inlineApps[name] || name == "record_session" || name == "sched_hangup" || name == "sched_transfer") {
			if err := fn(ch.ctx, c, ch, data); err != nil {
				ch.log.Warn("execute_on_answer failed", "app", name, "err", err)
			}
		} else {
			ch.log.Warn("execute_on_answer: application cannot run on answer", "app", name)
		}
	}
}
