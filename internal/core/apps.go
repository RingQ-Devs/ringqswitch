package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

func builtinApps() map[string]App {
	apps := map[string]App{
		"answer":              appAnswer,
		"pre_answer":          appPreAnswer,
		"ring_ready":          appRingReady,
		"hangup":              appHangup,
		"respond":             appRespond,
		"set":                 appSet,
		"unset":               appUnset,
		"export":              appSet,
		"log":                 appLog,
		"info":                appInfo,
		"sleep":               appSleep,
		"playback":            appPlayback,
		"echo":                appEcho,
		"park":                appPark,
		"bridge":              appBridge,
		"transfer":            appTransfer,
		"play_and_get_digits": appPlayAndGetDigits,
		"record":              appRecord,
		"send_dtmf":           appSendDTMF,
		"rxfax":               appRxFax,
		"txfax":               appTxFax,
		"ai_agent":            appAIAgent,
	}
	compatApps(apps)
	return apps
}

// AppNames lists the registered applications.
func (c *Core) AppNames() []string {
	names := make([]string, 0, len(c.apps))
	for n := range c.apps {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// startMedia makes sure the channel has media terminated by the switch.
func startMedia(ch *Channel, early bool) error {
	if err := ch.AnswerLocal(early); err != nil {
		return err
	}
	ch.ep.SetMode(media.ModeLocal)
	return nil
}

func appAnswer(_ context.Context, _ *Core, ch *Channel, _ string) error {
	if err := ch.AnswerLocal(false); err != nil {
		ch.Hangup(CauseIncompatibleDestination)
		return err
	}
	ch.ep.SetMode(media.ModeLocal)
	return nil
}

func appPreAnswer(_ context.Context, _ *Core, ch *Channel, _ string) error {
	if err := startMedia(ch, true); err != nil {
		ch.Hangup(CauseIncompatibleDestination)
		return err
	}
	return nil
}

func appRingReady(_ context.Context, _ *Core, ch *Channel, _ string) error {
	ch.RingReady()
	return nil
}

func appHangup(_ context.Context, _ *Core, ch *Channel, data string) error {
	ch.Hangup(ParseCause(data))
	return nil
}

// respond <code> [reason]: send a SIP response on an unanswered call.
func appRespond(_ context.Context, _ *Core, ch *Channel, data string) error {
	codeStr, reason, _ := strings.Cut(strings.TrimSpace(data), " ")
	code, err := strconv.Atoi(codeStr)
	if err != nil || code < 100 || code > 699 {
		return fmt.Errorf("respond: bad status %q", data)
	}
	switch {
	case code == 180:
		ch.RingReady()
	case code == 183:
		return startMedia(ch, true)
	case code >= 300 && ch.Direction == DirectionInbound && !ch.IsAnswered():
		resp := ch.inviteResponse(code, strings.TrimSpace(reason))
		_ = ch.inviteTx.Respond(resp)
		ch.hangup(CauseFromSIP(code), hangupCancelled)
	}
	return nil
}

func appSet(_ context.Context, _ *Core, ch *Channel, data string) error {
	name, value, ok := strings.Cut(data, "=")
	if !ok {
		return fmt.Errorf("set: expected name=value, got %q", data)
	}
	ch.SetVar(strings.TrimSpace(name), value)
	return nil
}

func appUnset(_ context.Context, _ *Core, ch *Channel, data string) error {
	ch.SetVar(strings.TrimSpace(data), "")
	return nil
}

func appLog(_ context.Context, _ *Core, ch *Channel, data string) error {
	level, msg, _ := strings.Cut(strings.TrimSpace(data), " ")
	switch strings.ToUpper(level) {
	case "DEBUG":
		ch.log.Debug(msg)
	case "WARN", "WARNING":
		ch.log.Warn(msg)
	case "ERR", "ERROR", "CRIT":
		ch.log.Error(msg)
	case "INFO", "NOTICE":
		ch.log.Info(msg)
	default:
		ch.log.Info(strings.TrimSpace(data))
	}
	return nil
}

func appInfo(_ context.Context, _ *Core, ch *Channel, _ string) error {
	vars := ch.Vars()
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "\n  %s: %s", k, vars[k])
	}
	ch.log.Info("channel info" + b.String())
	return nil
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func appSleep(ctx context.Context, _ *Core, _ *Channel, data string) error {
	ms, err := strconv.Atoi(strings.TrimSpace(data))
	if err != nil || ms < 0 {
		return fmt.Errorf("sleep: bad duration %q", data)
	}
	return wait(ctx, time.Duration(ms)*time.Millisecond)
}

// play streams a source to the channel. With stopOnDigit it stops at the
// first DTMF digit and returns it.
func (c *Core) play(ctx context.Context, ch *Channel, target string, stopOnDigit bool) (rune, error) {
	src, err := media.OpenSource(target, c.config().Settings.SoundsDir)
	if err != nil {
		return 0, err
	}
	buf := make([]int16, media.FrameSamples)
	for {
		n, err := src.Read(buf)
		if errors.Is(err, io.EOF) || n == 0 {
			return 0, nil
		}
		frame := make([]int16, media.FrameSamples)
		copy(frame, buf[:n])
		if err := ch.ep.WriteFrame(ctx, frame); err != nil {
			return 0, err
		}
		if stopOnDigit {
			select {
			case d := <-ch.dtmf:
				return d, nil
			default:
			}
		}
	}
}

func appPlayback(ctx context.Context, c *Core, ch *Channel, data string) error {
	if err := startMedia(ch, true); err != nil {
		return err
	}
	stop := ch.Var("playback_terminators")
	d, err := c.play(ctx, ch, strings.TrimSpace(data), stop != "" && stop != "none")
	if d != 0 {
		ch.SetVar("playback_terminator_used", string(d))
	}
	return err
}

func appEcho(ctx context.Context, _ *Core, ch *Channel, _ string) error {
	if err := ch.AnswerLocal(false); err != nil {
		return err
	}
	ch.ep.SetMode(media.ModeEcho)
	<-ctx.Done()
	if !ch.IsHungUp() {
		ch.ep.SetMode(media.ModeLocal)
	}
	return nil
}

func appPark(ctx context.Context, _ *Core, ch *Channel, _ string) error {
	if ch.IsAnswered() {
		ch.ep.SetMode(media.ModeLocal)
	}
	<-ctx.Done()
	return nil
}

func appSendDTMF(_ context.Context, _ *Core, ch *Channel, data string) error {
	for _, d := range strings.TrimSpace(data) {
		if strings.ContainsRune("0123456789*#ABCD", d) {
			ch.SendDTMF(d, 250)
		}
	}
	return nil
}

// transfer <destination> [XML] [context]
func appTransfer(_ context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) == 0 {
		return errors.New("transfer: missing destination")
	}
	context := ""
	switch len(f) {
	case 2:
		context = f[1]
	case 3:
		context = f[2]
	}
	c.transfer(ch, f[0], context)
	return nil
}

func continueOnFail(setting string, cause Cause) bool {
	if truthy(setting) {
		return true
	}
	for _, item := range strings.Split(setting, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.EqualFold(item, cause.String()) || item == strconv.Itoa(int(cause)) {
			return true
		}
	}
	return false
}

// bridge <dial string>
func appBridge(ctx context.Context, c *Core, ch *Channel, data string) error {
	if who := describeDial(data, c.gatewayLabel); who != "" {
		ch.flow("ring", "Ringing "+who)
	}
	if !c.checkOutbound(ctx, ch, data) {
		return nil
	}
	b, cause := c.originate(ctx, ch, data, nil)
	if b == nil {
		ch.SetVar("originate_disposition", cause.String())
		ch.SetVar("last_bridge_hangup_cause", cause.String())
		if ch.IsHungUp() || ctx.Err() != nil {
			return nil
		}
		ch.flow("no_answer", "Not answered"+map[bool]string{true: ": " + causeText(cause), false: ""}[causeText(cause) != ""])
		ch.log.Info("bridge failed", "dial", data, "cause", cause.String())
		if !continueOnFail(ch.Var("continue_on_fail"), cause) {
			ch.Hangup(cause)
		}
		return nil
	}
	ch.SetVar("originate_disposition", "SUCCESS")
	if ch.Direction == DirectionInbound && !ch.IsAnswered() {
		if err := c.answerFromPeer(ch, b, false); err != nil {
			b.Hangup(CauseIncompatibleDestination)
			ch.Hangup(CauseIncompatibleDestination)
			return err
		}
	}
	c.bridgeChannels(ctx, ch, b)
	if truthy(ch.Var("hangup_after_bridge")) && ctx.Err() == nil {
		ch.Hangup(CauseNormalClearing)
	}
	return nil
}

// bridgeChannels relays media between a and b until one hangs up, a is
// transferred (ctx cancelled) or b is detached by a transfer.
func (c *Core) bridgeChannels(ctx context.Context, a, b *Channel) {
	ctl := &bridgeCtl{detach: make(chan struct{})}
	ai, bi := a.identity(), b.identity()
	// When the call was first bridged (bridge_epoch): the
	// caller's wait ends there.
	now := strconv.FormatInt(time.Now().Unix(), 10)
	a.mu.Lock()
	if a.vars["bridge_epoch"] == "" {
		a.vars["bridge_epoch"] = now
	}
	a.peerName, a.peerCIDName, a.peerCIDNum, a.peerDest = bi[0], bi[1], bi[2], bi[3]
	a.partner, a.bctl, a.state = b, ctl, CSExchangeMedia
	a.vars["bridge_uuid"] = b.UUID
	a.vars["last_bridge_to"] = b.UUID
	a.mu.Unlock()
	b.mu.Lock()
	b.peerName, b.peerCIDName, b.peerCIDNum, b.peerDest = ai[0], ai[1], ai[2], ai[3]
	b.partner, b.bctl, b.state = a, ctl, CSExchangeMedia
	b.vars["bridge_uuid"] = a.UUID
	if b.vars["bridge_epoch"] == "" {
		b.vars["bridge_epoch"] = now
	}
	b.mu.Unlock()
	media.Bridge(a.ep, b.ep)
	c.recordForTranscription(a, b)
	refreshTaps(a)
	refreshTaps(b)
	a.log.Info("bridged", "a", a.name(), "b", b.name())
	a.fire(EvChannelBridge, "Bridge-A-Unique-ID", a.UUID, "Bridge-B-Unique-ID", b.UUID)
	callee := calleeName(b)
	a.flow("answer", "Answered by "+callee)

	select {
	case <-a.Done():
	case <-b.Done():
		if b.Var("sip_hangup_disposition") == "recv_bye" {
			a.flow("hangup", callee+" hung up")
		} else if t := causeText(b.Cause()); t != "" {
			a.flow("hangup", callee+"'s line ended: "+t)
		}
	case <-ctx.Done():
	case <-ctl.detach:
	}
	media.Unbridge(a.ep, b.ep)
	for _, ch := range []*Channel{a, b} {
		ch.mu.Lock()
		if ch.bctl == ctl {
			ch.partner, ch.bctl = nil, nil
			if !ch.hungup {
				ch.state = CSExecute
			}
		}
		ch.mu.Unlock()
	}
	refreshTaps(a)
	refreshTaps(b)
	a.stopHold()
	b.stopHold()
	a.fire(EvChannelUnbridge, "Bridge-A-Unique-ID", a.UUID, "Bridge-B-Unique-ID", b.UUID)
	if !ctl.detached() {
		switch {
		case a.IsHungUp():
			b.Hangup(a.Cause())
		case !b.IsHungUp():
			b.Hangup(CauseNormalClearing)
		}
	}
	if b.IsHungUp() {
		a.SetVar("last_bridge_hangup_cause", b.Cause().String())
		a.SetVar("bridge_hangup_cause", b.Cause().String())
	}
}

// collectDigits gathers DTMF until max digits, a terminator, or a timeout.
func collectDigits(ctx context.Context, ch *Channel, first rune, max int, terminators string, timeout, digitTimeout time.Duration) (string, error) {
	var digits []rune
	if first != 0 {
		if strings.ContainsRune(terminators, first) {
			return "", nil
		}
		digits = append(digits, first)
	}
	for len(digits) < max {
		d := timeout
		if len(digits) > 0 {
			d = digitTimeout
		}
		t := time.NewTimer(d)
		select {
		case r := <-ch.dtmf:
			t.Stop()
			if strings.ContainsRune(terminators, r) {
				return string(digits), nil
			}
			digits = append(digits, r)
		case <-t.C:
			return string(digits), nil
		case <-ctx.Done():
			t.Stop()
			return string(digits), ctx.Err()
		}
	}
	return string(digits), nil
}

// play_and_get_digits <min> <max> <tries> <timeout_ms> <terminators> <file>
//
//	[<invalid_file>] [<var_name>] [<regexp>] [<digit_timeout_ms>]
func appPlayAndGetDigits(ctx context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) < 6 {
		return fmt.Errorf("play_and_get_digits: need at least 6 arguments, got %q", data)
	}
	num := func(i, def int) int {
		if i < len(f) {
			if n, err := strconv.Atoi(f[i]); err == nil {
				return n
			}
		}
		return def
	}
	minD, maxD, tries, timeoutMs := num(0, 1), num(1, 1), num(2, 1), num(3, 5000)
	terminators, file := f[4], f[5]
	if terminators == "none" {
		terminators = ""
	}
	invalid, varName := "", "pagd_input"
	if len(f) > 6 {
		invalid = f[6]
	}
	if len(f) > 7 {
		varName = f[7]
	}
	var re *regexp.Regexp
	if len(f) > 8 {
		var err error
		if re, err = regexp.Compile(f[8]); err != nil {
			return fmt.Errorf("play_and_get_digits: %w", err)
		}
	}
	digitTimeout := num(9, timeoutMs)
	if maxD < 1 || minD > maxD || tries < 1 {
		return fmt.Errorf("play_and_get_digits: bad limits in %q", data)
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	for try := 0; try < tries; try++ {
		ch.FlushDTMF()
		first, err := c.play(ctx, ch, file, true)
		if err != nil {
			return err
		}
		digits, err := collectDigits(ctx, ch, first, maxD, terminators,
			time.Duration(timeoutMs)*time.Millisecond, time.Duration(digitTimeout)*time.Millisecond)
		if err != nil {
			return err
		}
		if len(digits) >= minD && (re == nil || re.MatchString(digits)) {
			ch.SetVar(varName, digits)
			ch.SetVar("read_result", "success")
			return nil
		}
		ch.log.Debug("invalid input", "digits", digits, "try", try+1)
		if invalid != "" && !strings.HasPrefix(invalid, "silence_stream://") && try < tries-1 {
			if _, err := c.play(ctx, ch, invalid, false); err != nil {
				return err
			}
		}
	}
	ch.SetVar(varName, "")
	ch.SetVar("read_result", "failure")
	return nil
}

// record <path> [max_seconds]: record the caller until a terminator digit
// (record_terminators, default "#"), the time limit or hangup.
func appRecord(ctx context.Context, _ *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) == 0 {
		return errors.New("record: missing path")
	}
	path := f[0]
	limit := 300
	if len(f) > 1 {
		if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
			limit = n
		}
	}
	terminators := ch.Var("record_terminators")
	if terminators == "" {
		terminators = "#"
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	w, err := media.CreateWAV(path)
	if err != nil {
		return err
	}
	ch.ep.FlushInput()
	ch.FlushDTMF()
	rctx, cancel := context.WithTimeout(ctx, time.Duration(limit)*time.Second)
	defer cancel()
	frames := make(chan []int16, 8)
	go func() {
		defer close(frames)
		for {
			fr, err := ch.ep.ReadFrame(rctx)
			if err != nil {
				return
			}
			frames <- fr
		}
	}()
loop:
	for {
		select {
		case fr, ok := <-frames:
			if !ok {
				break loop
			}
			if err := w.Write(fr); err != nil {
				break loop
			}
		case d := <-ch.dtmf:
			if strings.ContainsRune(terminators, d) {
				ch.SetVar("record_terminator_used", string(d))
				break loop
			}
		case <-rctx.Done():
			break loop
		}
	}
	cancel()
	for range frames {
	}
	secs := float64(w.Samples()) / 8000
	ch.SetVar("record_seconds", strconv.Itoa(int(secs)))
	ch.SetVar("record_ms", strconv.Itoa(int(secs*1000)))
	ch.log.Info("recording saved", "path", path, "seconds", fmt.Sprintf("%.1f", secs))
	return w.Close()
}
