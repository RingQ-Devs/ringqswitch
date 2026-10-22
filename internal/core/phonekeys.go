package core

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// Desk phone dial codes (the RingQ desk phone guide): do not disturb
// (*78 on, *79 off, *77 toggle), call return (*69), redial (*870), agent
// status by aux code number (*26N), and voicemail by phone (your own
// extension, the Message key).

// rq_dnd on|off|toggle: the caller's do not disturb, the same setting as in
// the apps (v_extensions.do_not_disturb).
func appDND(ctx context.Context, c *Core, ch *Channel, data string) error {
	if err := startMedia(ch, false); err != nil {
		return err
	}
	user, domain := ch.Var("user_name"), ch.Var("domain_name")
	if user == "" || c.config().Directory.LookupIn(domain, user) == nil {
		_, _ = c.play(ctx, ch, toneErr, false)
		return nil
	}
	var on bool
	switch strings.ToLower(strings.TrimSpace(data)) {
	case "on", "true", "enable", "enabled":
		on = true
	case "off", "false", "disable", "disabled":
		on = false
	default: // toggle
		on = !truthy(c.UserVar(user, domain, "do_not_disturb"))
	}
	if err := c.SetUserVars(user, domain, map[string]string{"do_not_disturb": strconv.FormatBool(on)}, "phone"); err != nil {
		_, _ = c.play(ctx, ch, toneErr, false)
		return err
	}
	name, tone := "dnd-off", toneUp
	if on {
		name, tone = "dnd-on", toneDown
	}
	ch.flow("route", map[bool]string{true: "Do not disturb on", false: "Do not disturb off"}[on])
	_ = wait(ctx, 300*time.Millisecond)
	if p := c.prompt(ch, name); p != "" {
		_, _ = c.play(ctx, ch, p, false)
	} else {
		_, _ = c.play(ctx, ch, tone, false)
	}
	return nil
}

// rq_call_return (*69) calls back whoever called the extension last;
// rq_redial (*870) dials its last number again.
func appCallReturn(ctx context.Context, c *Core, ch *Channel, data string) error {
	return c.callAgain(ctx, ch, "caller")
}

func appRedial(ctx context.Context, c *Core, ch *Channel, data string) error {
	return c.callAgain(ctx, ch, "dialed")
}

func (c *Core) callAgain(ctx context.Context, ch *Channel, kind string) error {
	user, domain := ch.Var("user_name"), ch.Var("domain_name")
	number := ""
	if user != "" {
		number = c.lastCall(kind, user, domain)
	}
	if number == "" {
		if err := startMedia(ch, false); err != nil {
			return err
		}
		if p := c.prompt(ch, "no-number"); p != "" {
			_, _ = c.play(ctx, ch, p, false)
		} else {
			_, _ = c.play(ctx, ch, toneErr, false)
		}
		return nil
	}
	ch.log.Info(map[string]string{"caller": "call return", "dialed": "redial"}[kind], "user", user, "number", number)
	c.transfer(ch, number, firstNonEmpty(ch.Var("user_context"), domain, ch.Var("context")))
	return nil
}

// AuxNumbers are the numbers of a domain's agent statuses for *26N: 1 is
// Available, then the aux codes in the order of the Aux codes page.
func AuxNumbers(f *config.Features, domain string) []string {
	out := []string{config.AgentAvailable}
	if f == nil {
		return out
	}
	for _, a := range f.AuxCodes {
		if a.Domain != "" && !strings.EqualFold(a.Domain, domain) || strings.EqualFold(a.Name, "available") {
			continue
		}
		out = append(out, a.Name)
	}
	return out
}

// auxByNumber turns *26N into a status and aux code.
func (c *Core) auxByNumber(domain, arg string) (status, aux string, ok bool) {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil {
		// A name: "Lunch", or "Available".
		if strings.EqualFold(arg, "available") {
			return config.AgentAvailable, "", true
		}
		if a := c.config().Features.Aux(arg, domain); a != nil {
			return config.AgentOnBreak, a.Name, true
		}
		return "", "", false
	}
	list := AuxNumbers(c.config().Features, domain)
	if n < 1 || n > len(list) {
		return "", "", false
	}
	if n == 1 {
		return config.AgentAvailable, "", true
	}
	return config.AgentOnBreak, list[n-1], true
}

// ---------------------------------------------------------------------------
// Voicemail by phone

// VoicemailMessage is a message in a mailbox.
type VoicemailMessage struct {
	UUID         string
	CallerNumber string
	CallerName   string
	Created      time.Time
	Seconds      int
	New          bool
}

// VoicemailStore keeps the messages' details (the database: v_voicemail_messages).
type VoicemailStore interface {
	VoicemailMessages(ctx context.Context, voicemailUUID string) ([]VoicemailMessage, error)
	SaveVoicemailMessage(ctx context.Context, messageUUID string) error
	DeleteVoicemailMessage(ctx context.Context, messageUUID string) error
}

// SetVoicemailStore sets where message details are kept (nil: the files
// alone, every message new).
func (c *Core) SetVoicemailStore(s VoicemailStore) {
	c.vmStore.Store(&s)
}

func (c *Core) voicemailMessages(ctx context.Context, vm *config.Voicemail) []VoicemailMessage {
	dir := c.voicemailDir(vm.Domain, vm.ID)
	if p := c.vmStore.Load(); p != nil && *p != nil {
		list, err := (*p).VoicemailMessages(ctx, vm.UUID)
		if err == nil {
			var out []VoicemailMessage
			for _, m := range list {
				if _, err := os.Stat(filepath.Join(dir, "msg_"+m.UUID+".wav")); err == nil {
					out = append(out, m)
				}
			}
			return out
		}
		c.log.Warn("voicemail: reading messages", "box", vm.ID+"@"+vm.Domain, "err", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "msg_*.wav"))
	var out []VoicemailMessage
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "msg_"), ".wav")
		out = append(out, VoicemailMessage{UUID: id, Created: st.ModTime(), New: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// sayNumber plays a number (0-99; more is said as "ninety nine").
func (c *Core) sayNumber(ctx context.Context, ch *Channel, n int) (rune, error) {
	n = min(max(n, 0), 99)
	parts := []int{n}
	if n >= 20 && n%10 != 0 {
		parts = []int{n / 10 * 10, n % 10}
	}
	for _, p := range parts {
		if f := c.prompt(ch, "num-"+strconv.Itoa(p)); f != "" {
			if d, err := c.play(ctx, ch, f, true); d != 0 || err != nil {
				return d, err
			}
		}
	}
	return 0, nil
}

// sayPrompts plays prompts (and numbers, as ints) until a key is pressed.
func (c *Core) sayPrompts(ctx context.Context, ch *Channel, items ...any) (rune, error) {
	for _, it := range items {
		var d rune
		var err error
		switch v := it.(type) {
		case int:
			d, err = c.sayNumber(ctx, ch, v)
		case string:
			if f := c.prompt(ch, v); f != "" {
				d, err = c.play(ctx, ch, f, true)
			}
		}
		if d != 0 || err != nil {
			return d, err
		}
	}
	return 0, nil
}

func (c *Core) waitKey(ctx context.Context, ch *Channel, d time.Duration) rune {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case k := <-ch.dtmf:
		return k
	case <-t.C:
	case <-ctx.Done():
	case <-ch.Done():
	}
	return 0
}

// voicemailListen plays a mailbox's messages: new ones first, then the
// saved ones. After each: 1 again, 2 save, 7 delete, 5 call back, # next.
func (c *Core) voicemailListen(ctx context.Context, ch *Channel, vm *config.Voicemail) error {
	msgs := c.voicemailMessages(ctx, vm)
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].New && !msgs[j].New })
	newN := 0
	for _, m := range msgs {
		if m.New {
			newN++
		}
	}
	savedN := len(msgs) - newN
	ch.FlushDTMF()
	_ = wait(ctx, 400*time.Millisecond)
	if len(msgs) == 0 {
		_, _ = c.sayPrompts(ctx, ch, "vm-no-messages")
		return nil
	}
	intro := []any{"vm-you-have"}
	if newN > 0 {
		intro = append(intro, newN, map[bool]string{true: "vm-new-message", false: "vm-new-messages"}[newN == 1])
	}
	if savedN > 0 {
		if newN > 0 {
			intro = append(intro, "vm-and")
		}
		intro = append(intro, savedN, map[bool]string{true: "vm-saved-message", false: "vm-saved-messages"}[savedN == 1])
	}
	key, err := c.sayPrompts(ctx, ch, intro...)
	if err != nil {
		return nil
	}
	store := c.vmStore.Load()
	changed := false
	defer func() {
		if changed {
			c.recountMWI(ctx, vm)
		}
	}()
	dir := c.voicemailDir(vm.Domain, vm.ID)
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		ch.flow("voicemail", "Listened to message "+strconv.Itoa(i+1))
		if key == 0 {
			key, _ = c.sayPrompts(ctx, ch, "vm-message", i+1)
		}
		if key == 0 || key == '1' {
			key, err = c.play(ctx, ch, filepath.Join(dir, "msg_"+m.UUID+".wav"), true)
			if err != nil && ch.IsHungUp() {
				return nil
			}
		}
		// The message was heard: it is no longer new.
		if m.New && store != nil && *store != nil {
			_ = (*store).SaveVoicemailMessage(ctx, m.UUID)
			msgs[i].New, changed = false, true
		}
		for tries := 0; key == 0 && tries < 2; tries++ {
			if key, _ = c.sayPrompts(ctx, ch, "vm-options"); key == 0 {
				key = c.waitKey(ctx, ch, 5*time.Second)
			}
		}
		if ch.IsHungUp() || ctx.Err() != nil {
			return nil
		}
		switch key {
		case '1':
			key = '1'
			i-- // again
			continue
		case '7':
			if store != nil && *store != nil {
				_ = (*store).DeleteVoicemailMessage(ctx, m.UUID)
			}
			_ = os.Remove(filepath.Join(dir, "msg_"+m.UUID+".wav"))
			changed = true
			key, _ = c.sayPrompts(ctx, ch, "vm-deleted")
		case '2':
			key, _ = c.sayPrompts(ctx, ch, "vm-saved")
		case '5':
			if m.CallerNumber != "" {
				c.transfer(ch, m.CallerNumber, firstNonEmpty(ch.Var("user_context"), vm.Domain))
				return nil
			}
			key = 0
		default:
			key = 0
		}
		if key == '#' || key == '2' || key == '7' {
			key = 0
		}
	}
	_, _ = c.sayPrompts(ctx, ch, "vm-no-more")
	return nil
}

// recountMWI updates the message lamp after messages were heard or deleted.
func (c *Core) recountMWI(ctx context.Context, vm *config.Voicemail) {
	msgs := c.voicemailMessages(ctx, vm)
	n := [2]int{}
	for _, m := range msgs {
		if m.New {
			n[0]++
		} else {
			n[1]++
		}
	}
	c.mwi.mu.Lock()
	if c.mwi.counts == nil {
		c.mwi.counts = map[string][2]int{}
	}
	c.mwi.counts[strings.ToLower(vm.ID+"@"+vm.Domain)] = n
	c.mwi.mu.Unlock()
	go c.sendMWI(vm.ID, vm.Domain)
}
