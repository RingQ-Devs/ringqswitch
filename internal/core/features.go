package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// Native implementations of RingQ features that the portal's dialplans call as Lua
// scripts or module configuration: ring groups (app.lua ring_groups) and IVR
// menus (the ivr application with menus from v_ivr_menus).

// ---------------------------------------------------------------------------
// Ring groups

// ring_group <uuid|extension|name>; RingQ dialplans call
// "lua app.lua ring_groups" with ring_group_uuid set, which ends up here.
func appRingGroup(ctx context.Context, c *Core, ch *Channel, data string) error {
	id := strings.TrimSpace(data)
	domain := ch.Var("domain_name")
	if id == "" {
		id = ch.Var("ring_group_uuid")
	}
	rg := c.config().Features.RingGroup(id, domain)
	if rg == nil {
		return fmt.Errorf("ring group %q not found", id)
	}
	if !rg.Enabled {
		ch.log.Info("ring group disabled", "ring_group", rg.Name)
		return nil
	}
	ctxName := firstNonEmpty(rg.Context, rg.Domain, domain)
	ch.SetVar("ring_group_uuid", rg.UUID)
	ch.SetVar("ring_group_name", rg.Name)
	ch.flow("ring_group", "Ring group "+rg.Name)
	if rg.ForwardEnabled && rg.ForwardDestination != "" {
		ch.log.Info("ring group forwarded", "ring_group", rg.Name, "destination", rg.ForwardDestination)
		c.transfer(ch, rg.ForwardDestination, ctxName)
		return nil
	}
	if rg.Greeting != "" {
		if err := startMedia(ch, false); err == nil {
			if path := c.resolveSound(ch, rg.Greeting); path != "" {
				_, _ = c.play(ctx, ch, path, false)
			}
		}
	}
	dial, timeout := c.ringGroupDialString(ch, rg, ctxName)
	if dial == "" {
		ch.log.Info("ring group has no available destinations", "ring_group", rg.Name)
	} else {
		ch.log.Info("ring group", "ring_group", rg.Name, "strategy", rg.Strategy, "dial", dial)
		ch.SetVar("continue_on_fail", "true")
		ch.SetVar("hangup_after_bridge", "true")
		if timeout > 0 {
			ch.SetVar("call_timeout", strconv.Itoa(timeout))
		}
		if err := appBridge(ctx, c, ch, dial); err != nil {
			return err
		}
		if ch.IsHungUp() || ctx.Err() != nil || ch.Var("originate_disposition") == "SUCCESS" {
			return nil
		}
	}
	// Nobody answered.
	if rg.TimeoutApp == "" {
		return nil
	}
	ch.log.Info("ring group timeout action", "ring_group", rg.Name, "app", rg.TimeoutApp, "data", rg.TimeoutData)
	return c.execApp(ctx, ch, rg.TimeoutApp, rg.TimeoutData, ctxName)
}

// ringGroupDialString builds the dial string the way the RingQ portal's
// ring_groups script does, and returns the overall timeout.
func (c *Core) ringGroupDialString(ch *Channel, rg *config.RingGroup, ctxName string) (string, int) {
	dests := append([]config.RingGroupDestination(nil), rg.Destinations...)
	strategy := strings.ToLower(rg.Strategy)
	if strategy == "random" {
		rand.Shuffle(len(dests), func(i, j int) { dests[i], dests[j] = dests[j], dests[i] })
	}
	cidName := ch.Var("caller_id_name")
	if v := ch.Var("effective_caller_id_name"); v != "" {
		cidName = v
	}
	if rg.CIDNamePrefix != "" {
		cidName = rg.CIDNamePrefix + "#" + cidName
	}
	cidNum := firstNonEmpty(ch.Var("effective_caller_id_number"), ch.Var("caller_id_number"))
	if rg.CIDNumberPrefix != "" {
		cidNum = rg.CIDNumberPrefix + cidNum
	}
	var legs []string
	total, longest := 0, 0
	for _, d := range dests {
		num := strings.TrimSpace(d.Number)
		if num == "" {
			continue
		}
		t := d.Timeout
		if t <= 0 {
			t = 30
		}
		vars := []string{"leg_timeout=" + strconv.Itoa(t), "ring_group_uuid=" + rg.UUID}
		if strategy == "simultaneous" || strategy == "enterprise" || strategy == "" {
			if d.Delay > 0 {
				vars = append(vars, "leg_delay_start="+strconv.Itoa(d.Delay))
			}
			longest = max(longest, d.Delay+t)
		}
		total += t
		var target string
		if u := c.config().Directory.LookupIn(rg.Domain, num); u != nil {
			if strategy == "rollover" && c.userBusy(u) {
				continue
			}
			target = "user/" + u.ID + "@" + u.Domain
			vars = append(vars, "origination_caller_id_name='"+cidName+"'", "origination_caller_id_number="+cidNum)
		} else {
			// External number: through the dialplan (outbound routes).
			name, number := cidName, cidNum
			if rg.CallerIDName != "" {
				name = rg.CallerIDName
			}
			if rg.CallerIDNumber != "" {
				number = rg.CallerIDNumber
			}
			target = "loopback/" + num + "/" + ctxName
			vars = append(vars, "origination_caller_id_name='"+name+"'", "origination_caller_id_number="+number)
		}
		legs = append(legs, "["+strings.Join(vars, ",")+"]"+target)
	}
	if len(legs) == 0 {
		return "", 0
	}
	switch strategy {
	case "sequence", "rollover", "random":
		timeout := total
		if rg.CallTimeout > 0 {
			timeout = rg.CallTimeout
		}
		return "{ignore_early_media=true}" + strings.Join(legs, "|"), timeout
	}
	timeout := longest
	if rg.CallTimeout > 0 {
		timeout = rg.CallTimeout
	}
	return "{ignore_early_media=true,originate_timeout=" + strconv.Itoa(max(timeout, 1)) + "}" + strings.Join(legs, ","), timeout
}

// userBusy reports whether a user has a call in progress.
func (c *Core) userBusy(u *config.User) bool {
	for _, ch := range c.Channels() {
		v := ch.Vars()
		if strings.EqualFold(v["dialed_user"], u.ID) && strings.EqualFold(v["dialed_domain"], u.Domain) {
			return true
		}
		if strings.EqualFold(v["user_name"], u.ID) && strings.EqualFold(v["domain_name"], u.Domain) && v["sip_authorized"] == "true" {
			return true
		}
	}
	return false
}

// execApp runs "app data" on a channel the way a dialplan action would:
// transfer moves the call, anything else runs to completion.
func (c *Core) execApp(ctx context.Context, ch *Channel, app, data, ctxName string) error {
	app = strings.TrimSpace(app)
	data = ch.Expand(strings.TrimSpace(data))
	if app == "transfer" {
		f := strings.Fields(data)
		if len(f) == 0 {
			return errors.New("transfer: missing destination")
		}
		dest, dctx := f[0], ctxName
		if len(f) == 2 {
			dctx = f[1]
		} else if len(f) >= 3 {
			dctx = f[2]
		}
		c.transfer(ch, dest, dctx)
		return nil
	}
	fn := c.apps[app]
	if fn == nil {
		return fmt.Errorf("unknown application %q", app)
	}
	ch.fire(EvChannelExecute, "Application", app, "Application-Data", data)
	err := fn(ctx, c, ch, data)
	ch.fire(EvChannelExecuteDone, "Application", app, "Application-Data", data)
	return err
}

// ---------------------------------------------------------------------------
// Sound lookup

// resolveSound finds a prompt: absolute paths, the domain's recordings
// (RingQ stores greetings by file name), the sounds directory, and
// the standard sounds layout (en/us/callie/<dir>/8000/<file>). tone_stream
// and silence_stream specs are returned as is. It returns "" when nothing
// playable is found (TTS "say:" / "phrase:" prompts included).
func (c *Core) resolveSound(ch *Channel, name string) string {
	name = strings.TrimSpace(ch.Expand(name))
	switch {
	case name == "":
		return ""
	case strings.HasPrefix(name, "tone_stream://"), strings.HasPrefix(name, "silence_stream://"):
		return name
	case strings.HasPrefix(name, "say:"), strings.HasPrefix(name, "phrase:"), strings.HasPrefix(name, "${"):
		return ""
	}
	name = strings.TrimPrefix(name, "file_string://")
	if i := strings.Index(name, "!"); i > 0 {
		name = name[:i] // file_string lists: play the first file only
	}
	s := c.config().Settings
	var candidates []string
	if filepath.IsAbs(name) {
		candidates = append(candidates, name)
	} else {
		domain := ch.Var("domain_name")
		candidates = append(candidates,
			filepath.Join(s.RecordingsDir, domain, name),
			filepath.Join(s.RecordingsDir, name),
			filepath.Join(s.SoundsDir, name))
		if dir, file := filepath.Split(name); dir != "" {
			if pack := soundPack(s.SoundsDir, c.config().Directory.Find(domain)); pack != "" {
				candidates = append(candidates, filepath.Join(pack, dir, "8000", file))
			}
			candidates = append(candidates, filepath.Join(s.SoundsDir, "en", "us", "callie", dir, "8000", file))
		}
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	ch.log.Debug("sound not found", "sound", name)
	return ""
}

// soundPack is the folder of the domain's language in the standard sounds
// layout (fr-ca: sounds/fr/ca/<voice>), "" for English or when that
// language is not installed.
func soundPack(soundsDir string, dom *config.Domain) string {
	if dom == nil || dom.Language == "" || strings.HasPrefix(dom.Language, "en") {
		return ""
	}
	lang, region, _ := strings.Cut(strings.ReplaceAll(dom.Language, "_", "-"), "-")
	base := filepath.Join(soundsDir, lang, region)
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}

// playSound plays a prompt if it can be found; with stop it returns the
// first digit pressed.
func (c *Core) playSound(ctx context.Context, ch *Channel, name string, stop bool) (rune, error) {
	path := c.resolveSound(ch, name)
	if path == "" {
		return 0, nil
	}
	d, err := c.play(ctx, ch, path, stop)
	if err != nil && ctx.Err() == nil {
		ch.log.Warn("cannot play prompt", "sound", path, "err", err)
		return d, nil
	}
	return d, err
}

// ---------------------------------------------------------------------------
// IVR menus

const maxIVRDepth = 10

// ivr <menu uuid|name>
func appIVR(ctx context.Context, c *Core, ch *Channel, data string) error {
	id := strings.TrimSpace(data)
	if id == "" {
		id = ch.Var("ivr_menu_uuid")
	}
	m := c.config().Features.IVRMenu(id, ch.Var("domain_name"))
	if m == nil {
		return fmt.Errorf("ivr menu %q not found", id)
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	return c.runIVR(ctx, ch, m, 0)
}

type ivrResult int

const (
	ivrDone ivrResult = iota // left the menu (transfer, hangup, exit)
	ivrBack                  // return to the parent menu
	ivrTop                   // restart from the top menu
)

func (c *Core) runIVR(ctx context.Context, ch *Channel, m *config.IVRMenu, depth int) error {
	res, err := c.ivrMenu(ctx, ch, m, depth)
	for err == nil && res == ivrTop && depth == 0 {
		res, err = c.ivrMenu(ctx, ch, m, depth)
	}
	return err
}

func (c *Core) ivrMenu(ctx context.Context, ch *Channel, m *config.IVRMenu, depth int) (ivrResult, error) {
	if depth > maxIVRDepth {
		return ivrDone, errors.New("ivr: menus nested too deeply")
	}
	ctxName := firstNonEmpty(m.Context, m.Domain, ch.Var("domain_name"))
	ch.SetVar("ivr_menu_uuid", m.UUID)
	ch.log.Info("ivr menu", "menu", m.Name)
	ch.flow("ivr", "IVR menu "+m.Name)
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	interDigit := m.InterDigitTimeout
	if interDigit <= 0 {
		interDigit = 2 * time.Second
	}
	maxLen := m.DigitLen
	if maxLen <= 0 {
		maxLen = 5
	}
	maxFailures, maxTimeouts := max(m.MaxFailures, 1), max(m.MaxTimeouts, 1)
	failures, timeouts := 0, 0
	greeting := m.GreetLong
	for {
		if ch.IsHungUp() || ctx.Err() != nil {
			return ivrDone, nil
		}
		ch.FlushDTMF()
		first, err := c.playSound(ctx, ch, greeting, true)
		if err != nil {
			return ivrDone, err
		}
		digits, err := c.ivrCollect(ctx, ch, m, first, maxLen, timeout, interDigit)
		if err != nil {
			return ivrDone, err
		}
		greeting = firstNonEmpty(m.GreetShort, m.GreetLong)
		if digits == "" {
			timeouts++
			if timeouts >= maxTimeouts {
				break
			}
			continue
		}
		ch.SetVar("ivr_digits", digits)
		opt := matchIVROption(m, digits)
		ch.flow("digits", "Pressed "+digits+map[bool]string{true: "", false: " (not an option)"}[opt != nil])
		if opt != nil {
			res, handled, err := c.ivrAction(ctx, ch, m, opt, ctxName, depth)
			if err != nil {
				ch.log.Warn("ivr option failed", "menu", m.Name, "digits", digits, "err", err)
			}
			if handled {
				return res, nil
			}
			continue
		}
		if m.DirectDial && c.config().Directory.LookupIn(firstNonEmpty(m.Domain, ch.Var("domain_name")), digits) != nil {
			ch.log.Info("ivr direct dial", "menu", m.Name, "destination", digits)
			c.ivrCallerID(ch, m)
			c.transfer(ch, digits, ctxName)
			return ivrDone, nil
		}
		failures++
		ch.log.Info("ivr invalid entry", "menu", m.Name, "digits", digits, "failures", failures)
		if failures >= maxFailures {
			break
		}
		if _, err := c.playSound(ctx, ch, m.InvalidSound, false); err != nil {
			return ivrDone, err
		}
	}
	// Too many failures or timeouts.
	if _, err := c.playSound(ctx, ch, m.ExitSound, false); err != nil {
		return ivrDone, err
	}
	if m.ExitApp != "" && !ch.IsHungUp() {
		c.ivrCallerID(ch, m)
		return ivrDone, c.execApp(ctx, ch, m.ExitApp, m.ExitData, ctxName)
	}
	return ivrDone, nil
}

// ivrCollect gathers digits: the first may already have interrupted the
// greeting; collection ends early when the entry can only match one option.
func (c *Core) ivrCollect(ctx context.Context, ch *Channel, m *config.IVRMenu, first rune, maxLen int, timeout, interDigit time.Duration) (string, error) {
	var digits []rune
	if first != 0 {
		if first == '#' {
			return "", nil
		}
		digits = append(digits, first)
	}
	for len(digits) < maxLen {
		if len(digits) > 0 && ivrComplete(m, string(digits)) {
			break
		}
		wait := timeout
		if len(digits) > 0 {
			wait = interDigit
		}
		t := time.NewTimer(wait)
		select {
		case d := <-ch.dtmf:
			t.Stop()
			if d == '#' {
				return string(digits), nil
			}
			digits = append(digits, d)
		case <-t.C:
			return string(digits), nil
		case <-ctx.Done():
			t.Stop()
			return string(digits), ctx.Err()
		}
	}
	return string(digits), nil
}

// ivrComplete reports whether digits exactly match an option and no longer
// option starts with them (so there is no need to wait for more).
func ivrComplete(m *config.IVRMenu, digits string) bool {
	exact := false
	for _, o := range m.Options {
		if o.Digits == digits {
			exact = true
		} else if strings.HasPrefix(o.Digits, digits) || strings.HasPrefix(o.Digits, "/") {
			return false
		}
	}
	return exact && !m.DirectDial
}

func matchIVROption(m *config.IVRMenu, digits string) *config.IVROption {
	for i := range m.Options {
		o := &m.Options[i]
		if o.Digits == digits {
			return o
		}
		// /regex/ digit patterns are accepted too.
		if len(o.Digits) > 2 && strings.HasPrefix(o.Digits, "/") && strings.HasSuffix(o.Digits, "/") {
			if re, err := cachedRegexp(o.Digits[1 : len(o.Digits)-1]); err == nil && re.MatchString(digits) {
				return o
			}
		}
	}
	return nil
}

func (c *Core) ivrCallerID(ch *Channel, m *config.IVRMenu) {
	if m.CIDPrefix != "" {
		name := firstNonEmpty(ch.Var("effective_caller_id_name"), ch.Var("caller_id_name"))
		ch.SetVar("effective_caller_id_name", m.CIDPrefix+"#"+name)
	}
}

// ivrAction runs a selected option. handled reports that the menu is left.
func (c *Core) ivrAction(ctx context.Context, ch *Channel, m *config.IVRMenu, o *config.IVROption, ctxName string, depth int) (ivrResult, bool, error) {
	param := strings.TrimSpace(o.Param)
	switch o.Action {
	case "menu-exec-app", "":
		app, data, _ := strings.Cut(param, " ")
		if app == "" {
			return ivrDone, false, nil
		}
		if strings.Contains(data, "$1") {
			data = strings.ReplaceAll(data, "$1", ch.Var("ivr_digits"))
		}
		c.ivrCallerID(ch, m)
		err := c.execApp(ctx, ch, app, data, ctxName)
		// Transfers and hangups leave the menu; other apps return to it.
		if ch.IsHungUp() || app == "transfer" || app == "hangup" || app == "bridge" {
			return ivrDone, true, err
		}
		return ivrDone, false, err
	case "menu-sub":
		sub := c.config().Features.IVRMenu(param, m.Domain)
		if sub == nil {
			return ivrDone, false, fmt.Errorf("sub menu %q not found", param)
		}
		res, err := c.ivrMenu(ctx, ch, sub, depth+1)
		switch {
		case err != nil || res == ivrDone:
			return ivrDone, true, err
		case res == ivrTop && depth > 0:
			return ivrTop, true, nil
		}
		return ivrDone, false, nil // back here (or top reached at depth 0)
	case "menu-top":
		if depth == 0 {
			return ivrDone, false, nil
		}
		return ivrTop, true, nil
	case "menu-back":
		if depth == 0 {
			return ivrDone, false, nil
		}
		return ivrBack, true, nil
	case "menu-exit":
		_, err := c.playSound(ctx, ch, m.ExitSound, false)
		return ivrDone, true, err
	case "menu-play-sound":
		_, err := c.playSound(ctx, ch, param, false)
		return ivrDone, false, err
	}
	return ivrDone, false, fmt.Errorf("unsupported ivr action %q", o.Action)
}
