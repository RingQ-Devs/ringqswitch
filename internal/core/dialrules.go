package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// Limits on calls out through trunks, set per domain in Security settings:
// countries callers may not call (toll fraud), and calls that need a PIN
// first (the RingQ portal's PIN numbers: the PIN's account code goes with the call).

// trunkNumbers returns the numbers a dial string sends to trunks.
func trunkNumbers(data string) []string {
	ds, err := parseDialString(data)
	if err != nil {
		return nil
	}
	var out []string
	for _, g := range ds.groups {
		for _, l := range g {
			t := l.target
			if i := strings.Index(t, "gateway/"); i >= 0 && (i == 0 || strings.HasSuffix(t[:i], "/")) {
				rest := t[i+len("gateway/"):]
				if _, num, ok := strings.Cut(rest, "/"); ok && num != "" {
					num, _, _ = strings.Cut(num, "@")
					out = append(out, num)
				}
			}
		}
	}
	return out
}

// checkOutbound applies the domain's limits to a bridge through trunks. It
// returns false when the call may not go on (the caller was told why and
// the call ended).
func (c *Core) checkOutbound(ctx context.Context, ch *Channel, data string) bool {
	numbers := trunkNumbers(ch.Expand(data))
	if len(numbers) == 0 {
		return true
	}
	dom := c.config().Directory.Find(ch.Var("domain_name"))
	if dom == nil || dom.Outbound == nil {
		return true
	}
	o := dom.Outbound
	dialed := ch.Var("destination_number")
	if len(o.Blocked) > 0 {
		for _, n := range append([]string{dialed}, numbers...) {
			iso := config.CountryOfNumber(n)
			if iso == "" || !o.Blocked[iso] {
				continue
			}
			name := iso
			if cc := config.CountryByISO(iso); cc != nil {
				name = cc.Name
			}
			ch.log.Warn("call to a country that is not allowed", "number", n, "country", name, "domain", dom.Name)
			ch.flow("blocked", "Calls to "+name+" are not allowed (Security settings → Allowed countries)")
			c.recordEvent("security", fmt.Sprintf("call from %s to %s refused: %s is not an allowed country", ch.Var("caller_id_number"), n, name))
			c.sayAndHangup(ctx, ch, "country-blocked", CauseCallRejected)
			return false
		}
	}
	ext := ch.Var("user_name")
	if ch.Var("sip_authorized") != "true" || ext == "" || ch.Var("pin_verified") == "true" || !o.NeedsPIN(ext, dialed) {
		return true
	}
	pin, acct, ok := c.askDialPIN(ctx, ch, o.PINs)
	if !ok {
		if !ch.IsHungUp() {
			ch.log.Warn("call out refused: no valid PIN", "extension", ext, "number", dialed)
			c.recordEvent("security", fmt.Sprintf("call from %s to %s refused: wrong PIN", ext, dialed))
			ch.flow("blocked", "No valid PIN entered")
			ch.Hangup(CauseCallRejected)
		}
		return false
	}
	ch.SetVar("pin_verified", "true")
	ch.SetVar("pin_number", pin)
	if acct != "" {
		ch.SetVar("accountcode", acct)
	}
	ch.flow("pin", "PIN accepted")
	return true
}

// askDialPIN asks for a PIN (three tries) and returns it with its account code.
func (c *Core) askDialPIN(ctx context.Context, ch *Channel, pins map[string]string) (string, string, bool) {
	if err := startMedia(ch, false); err != nil {
		return "", "", false
	}
	for try := 0; try < 3; try++ {
		ch.FlushDTMF()
		first, err := c.play(ctx, ch, c.prompt(ch, "pin-enter"), true)
		if err != nil {
			return "", "", false
		}
		digits, err := collectDigits(ctx, ch, first, 16, "#", 10*time.Second, 5*time.Second)
		if err != nil {
			return "", "", false
		}
		if acct, ok := pins[digits]; ok && digits != "" {
			return digits, acct, true
		}
		ch.log.Info("wrong PIN", "try", try+1)
		if _, err := c.play(ctx, ch, c.prompt(ch, "pin-invalid"), false); err != nil {
			return "", "", false
		}
	}
	return "", "", false
}

// sayAndHangup plays a built-in prompt (answering early, without charging
// the caller) and ends the call.
func (c *Core) sayAndHangup(ctx context.Context, ch *Channel, prompt string, cause Cause) {
	if p := c.prompt(ch, prompt); p != "" && startMedia(ch, !ch.IsAnswered()) == nil {
		_, _ = c.play(ctx, ch, p, false)
	}
	ch.Hangup(cause)
}
