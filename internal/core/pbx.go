package core

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// PBX features: agent feature codes, voicemail with message waiting
// indication, call park, call pickup, music on hold for held calls and
// attended transfer.

const (
	toneUp   = "tone_stream://%(150,50,800);%(250,0,1200);loops=1"
	toneDown = "tone_stream://%(150,50,1200);%(250,0,600);loops=1"
	toneErr  = "tone_stream://%(200,100,400);%(200,100,400);%(200,0,400);loops=1"
	toneBeep = "tone_stream://%(500,0,1000);loops=1"
)

// ---------------------------------------------------------------------------
// Agent feature codes

// rq_agent_status <toggle|login|logout|available|break [aux]|break-toggle>
// changes the status of the agent whose extension is calling, and plays a
// rising tone (now available) or a falling tone (break / logged out).
func appAgentStatus(ctx context.Context, c *Core, ch *Channel, data string) error {
	if err := startMedia(ch, false); err != nil {
		return err
	}
	ext := firstNonEmpty(ch.Var("user_name"), ch.Var("caller_id_number"))
	domain := ch.Var("domain_name")
	a := c.config().Features.AgentByExtension(ext, domain)
	if a == nil {
		ch.log.Info("agent feature code: extension is not an agent", "extension", ext, "domain", domain)
		_, _ = c.play(ctx, ch, toneErr, false)
		return nil
	}
	cur := c.agentStatus(a.UUID)
	action, aux, _ := strings.Cut(strings.TrimSpace(data), " ")
	var status string
	switch strings.ToLower(action) {
	case "aux": // *26N: the status numbered N (1 Available, then the aux codes)
		st, name, ok := c.auxByNumber(domain, aux)
		if !ok {
			ch.log.Info("agent feature code: no such aux code number", "number", aux, "domain", domain)
			_, _ = c.play(ctx, ch, toneErr, false)
			return nil
		}
		status, aux = st, name
	case "login", "available":
		status = config.AgentAvailable
	case "logout":
		status = config.AgentLoggedOut
	case "break":
		status = config.AgentOnBreak
	case "break-toggle":
		status = config.AgentOnBreak
		if cur == config.AgentOnBreak {
			status = config.AgentAvailable
		}
	default: // toggle
		status = config.AgentAvailable
		if cur != config.AgentLoggedOut {
			status = config.AgentLoggedOut
		}
	}
	if status == config.AgentOnBreak && aux == "" {
		aux = "Break"
	}
	if err := c.SetAgentStatus(AgentStatusChange{Agent: a.UUID, Status: status, Aux: aux, Source: "phone"}); err != nil {
		_, _ = c.play(ctx, ch, toneErr, false)
		return err
	}
	tone := toneDown
	if status == config.AgentAvailable {
		tone = toneUp
	}
	_, _ = c.play(ctx, ch, tone, false)
	return nil
}

func (c *Core) agentStatus(id string) string {
	c.cc.mu.Lock()
	defer c.cc.mu.Unlock()
	if a := c.cc.agents[strings.ToLower(id)]; a != nil {
		return a.status
	}
	return ""
}

// ---------------------------------------------------------------------------
// Voicemail

// mwiState keeps the message counts sent to phones.
type mwiState struct {
	mu     sync.Mutex
	counts map[string][2]int // id@domain -> new, old
}

func (c *Core) voicemailDir(domain, id string) string {
	return filepath.Join(c.config().Settings.VoicemailDir, domain, id)
}

// voicemail [check] [auth] <profile> <domain> [<id>] (the dialplan syntax).
// Leaving a message is supported; listening is done in the web UI.
func appVoicemail(ctx context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	check := false
	for len(f) > 0 && (f[0] == "check" || f[0] == "auth") {
		check = check || f[0] == "check"
		f = f[1:]
	}
	domain := ch.Var("domain_name")
	id := firstNonEmpty(ch.Var("voicemail_id"), ch.Var("dialed_extension"), ch.Var("destination_number"))
	if len(f) >= 2 {
		domain = f[1]
	}
	if len(f) >= 3 {
		id = f[2]
	}
	if check || ch.Var("voicemail_action") == "check" {
		ch.flow("voicemail", "Checked voicemail")
		return c.voicemailCheck(ctx, ch, domain, firstNonEmpty(ch.Var("voicemail_id"), map[bool]string{true: id}[len(f) >= 3]))
	}
	ch.flow("voicemail", "Voicemail box "+id)
	return c.voicemailDeposit(ctx, ch, domain, id)
}

func (c *Core) voicemailDeposit(ctx context.Context, ch *Channel, domain, id string) error {
	vm := c.config().Features.Voicemail(id, domain)
	if vm == nil || !vm.Enabled {
		ch.log.Info("no voicemail box", "box", id+"@"+domain)
		return nil
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	dir := c.voicemailDir(vm.Domain, vm.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("voicemail: %w", err)
	}
	ch.FlushDTMF()
	greeting := ""
	if vm.GreetingID > 0 {
		greeting = filepath.Join(dir, fmt.Sprintf("greeting_%d.wav", vm.GreetingID))
	}
	if greeting != "" {
		if _, err := os.Stat(greeting); err != nil {
			greeting = ""
		}
	}
	if greeting == "" && !truthy(ch.Var("skip_greeting")) {
		greeting = c.prompt(ch, "voicemail-greeting") // "...Please leave a message after the tone."
	}
	if greeting != "" {
		_, _ = c.play(ctx, ch, greeting, true)
	}
	if _, err := c.play(ctx, ch, toneBeep, false); err != nil {
		return nil
	}
	msgID := sip.NewUUID()
	path := filepath.Join(dir, "msg_"+msgID+".wav")
	ch.SetVar("record_terminators", firstNonEmpty(ch.Var("record_terminators"), "#"))
	maxSecs := int(c.config().Settings.VoicemailMax.Seconds())
	if maxSecs <= 0 {
		maxSecs = 180
	}
	_ = appRecord(ctx, c, ch, path+" "+strconv.Itoa(maxSecs))
	secs, _ := strconv.Atoi(ch.Var("record_seconds"))
	if secs < 2 {
		_ = os.Remove(path)
		ch.log.Info("voicemail too short; discarded", "box", vm.ID+"@"+vm.Domain)
		return nil
	}
	cidName := firstNonEmpty(ch.Var("effective_caller_id_name"), ch.Var("caller_id_name"))
	cidNum := firstNonEmpty(ch.Var("effective_caller_id_number"), ch.Var("caller_id_number"))
	e := NewEvent(EvCustom, "vm::maintenance")
	e.Add("VM-Action", "leave-message")
	e.Add("VM-User", vm.ID)
	e.Add("VM-Domain", vm.Domain)
	e.Add("VM-UUID", vm.UUID)
	e.Add("VM-Message-UUID", msgID)
	e.Add("VM-Caller-ID-Name", cidName)
	e.Add("VM-Caller-ID-Number", cidNum)
	e.Add("VM-Message-Len", strconv.Itoa(secs))
	e.Add("VM-File-Path", path)
	e.Add("VM-Created", strconv.FormatInt(time.Now().Unix(), 10))
	c.events.Publish(e)
	ch.SetVar("voicemail_message", "true")
	ch.log.Info("voicemail left", "box", vm.ID+"@"+vm.Domain, "seconds", secs, "from", cidNum)
	c.recordEvent("call", fmt.Sprintf("voicemail for %s@%s from %s %s (%ds)", vm.ID, vm.Domain, cidName, cidNum, secs))
	c.mwi.mu.Lock()
	n := c.mwi.counts[strings.ToLower(vm.ID+"@"+vm.Domain)]
	n[0]++
	c.mwi.counts[strings.ToLower(vm.ID+"@"+vm.Domain)] = n
	c.mwi.mu.Unlock()
	go c.sendMWI(vm.ID, vm.Domain)
	if !ch.IsHungUp() {
		_, _ = c.play(ctx, ch, toneDown, false)
		ch.Hangup(CauseNormalClearing)
	}
	return nil
}

// voicemailCheck plays the caller's messages (the Message key, *97, or
// dialing one's own extension).
func (c *Core) voicemailCheck(ctx context.Context, ch *Channel, domain, id string) error {
	if err := startMedia(ch, false); err != nil {
		return err
	}
	id = firstNonEmpty(ch.Var("voicemail_id"), id, ch.Var("user_name"))
	vm := c.config().Features.Voicemail(id, domain)
	if vm == nil || !vm.Enabled {
		_ = wait(ctx, 300*time.Millisecond)
		if p := c.prompt(ch, "vm-no-box"); p != "" {
			_, _ = c.play(ctx, ch, p, false)
		} else {
			_, _ = c.play(ctx, ch, toneErr, false)
		}
		return nil
	}
	return c.voicemailListen(ctx, ch, vm)
}

// syncMWI refreshes message counts after a (re)load and notifies phones
// whose counts changed.
func (c *Core) syncMWI(f *config.Features) {
	c.mwi.mu.Lock()
	if c.mwi.counts == nil {
		c.mwi.counts = map[string][2]int{}
	}
	var changed [][2]string
	seen := map[string]bool{}
	for k, vm := range f.Voicemails {
		seen[k] = true
		n := [2]int{vm.NewMessages, vm.OldMessages}
		if old, ok := c.mwi.counts[k]; !ok || old != n {
			if ok || n[0] > 0 {
				changed = append(changed, [2]string{vm.ID, vm.Domain})
			}
			c.mwi.counts[k] = n
		}
	}
	for k := range c.mwi.counts {
		if !seen[k] {
			delete(c.mwi.counts, k)
		}
	}
	c.mwi.mu.Unlock()
	for _, x := range changed {
		go c.sendMWI(x[0], x[1])
	}
}

// sendMWI sends an unsolicited message-summary NOTIFY to every registered
// phone of the mailbox owner (most phones light their message lamp).
func (c *Core) sendMWI(id, domain string) {
	if c.stack == nil {
		return
	}
	c.mwi.mu.Lock()
	n := c.mwi.counts[strings.ToLower(id+"@"+domain)]
	c.mwi.mu.Unlock()
	waiting := "no"
	if n[0] > 0 {
		waiting = "yes"
	}
	body := fmt.Sprintf("Messages-Waiting: %s\r\nMessage-Account: sip:%s@%s\r\nVoice-Message: %d/%d (0/0)\r\n", waiting, id, domain, n[0], n[1])
	for _, b := range c.reg.Lookup(id, domain) {
		c.notify(b, "message-summary", "application/simple-message-summary", []byte(body))
	}
	// Phones that subscribed to the mailbox (message key) hear in their
	// subscription too.
	c.pres.init()
	c.pres.mu.Lock()
	var subs []*subscription
	for _, s := range c.pres.subs {
		if s.event == "message-summary" && strings.EqualFold(s.resource, id) && strings.EqualFold(s.domain, domain) {
			subs = append(subs, s)
		}
	}
	c.pres.mu.Unlock()
	for _, s := range subs {
		c.notifySub(s, "")
	}
}

// notify sends an out-of-dialog NOTIFY to a registered contact.
func (c *Core) notify(b *Binding, event, ctype string, body []byte) {
	ruri, err := sip.ParseURI(b.Contact)
	if err != nil {
		return
	}
	aor := &sip.Address{URI: &sip.URI{Scheme: "sip", User: b.User, Host: b.Domain}}
	from := aor.Clone()
	from.Params.Set("tag", sip.NewTag())
	req := sip.NewRequest("NOTIFY", ruri)
	req.Add("From", from.String())
	req.Add("To", aor.String())
	req.Add("Call-ID", sip.NewCallID())
	req.Add("CSeq", "1 NOTIFY")
	req.Add("Contact", c.contactFor(b.User, b.Src))
	req.Add("Event", event)
	req.Add("Subscription-State", "active;expires=3600")
	if ctype != "" {
		req.Add("Content-Type", ctype)
		req.Body = body
	}
	tx, err := c.stack.Request(req, b.Src)
	if err != nil {
		return
	}
	for range tx.Responses() {
	}
}

// ---------------------------------------------------------------------------
// Call park

type parkLot struct {
	mu    sync.Mutex
	slots map[string]*Channel  // lot/slot -> parked channel
	since map[string]time.Time // lot/slot -> when it was parked
}

// valet_park <lot> <slot>: if the slot is free the call waits there with
// music on hold; if a call is parked there, it is connected to the caller.
// Unanswered parked calls go back to the extension that parked them (when
// known) after park-timeout.
func appValetPark(ctx context.Context, c *Core, ch *Channel, data string) error {
	f := strings.Fields(data)
	if len(f) < 2 {
		return fmt.Errorf("valet_park: need <lot> <slot>")
	}
	key := strings.ToLower(f[0] + "/" + f[1])
	c.park.mu.Lock()
	if c.park.slots == nil {
		c.park.slots = map[string]*Channel{}
		c.park.since = map[string]time.Time{}
	}
	parked := c.park.slots[key]
	if parked != nil && parked.IsHungUp() {
		parked = nil
		delete(c.park.slots, key)
	}
	if parked == nil {
		c.park.slots[key] = ch
		c.park.since[key] = time.Now()
	} else {
		delete(c.park.slots, key)
		delete(c.park.since, key)
	}
	c.park.mu.Unlock()
	c.presenceKick() // the slot's lamp

	if parked != nil {
		// Retrieve: take the parked call over and connect it here.
		c.log.Info("call retrieved from park", "slot", f[1], "by", ch.Var("caller_id_number"))
		c.recordEvent("call", fmt.Sprintf("%s retrieved the call parked on %s", ch.Var("caller_id_number"), f[1]))
		c.adopt(parked)
		waitIdle(parked)
		if err := ch.AnswerLocal(false); err != nil {
			return err
		}
		if !parked.IsAnswered() {
			if err := parked.AnswerLocal(false); err != nil {
				return err
			}
		}
		c.bridgeChannels(ctx, ch, parked)
		if !ch.IsHungUp() && ctx.Err() == nil {
			ch.Hangup(CauseNormalClearing)
		}
		return nil
	}

	// Park.
	if err := startMedia(ch, false); err != nil {
		return err
	}
	ch.SetVar("valet_parking_spot", f[1])
	c.log.Info("call parked", "slot", f[1], "caller", ch.Var("caller_id_number"))
	c.recordEvent("call", fmt.Sprintf("%s parked on %s", ch.Var("caller_id_number"), f[1]))
	defer func() {
		c.park.mu.Lock()
		if c.park.slots[key] == ch {
			delete(c.park.slots, key)
			delete(c.park.since, key)
		}
		c.park.mu.Unlock()
		c.presenceKick()
	}()
	timeout := c.config().Settings.ParkTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := c.startMOH(pctx, ch, ch.Var("hold_music"), "", 0, "", nil)
	<-pctx.Done()
	<-done
	if ctx.Err() != nil || ch.IsHungUp() {
		return nil // retrieved (adopted) or hung up
	}
	if back := ch.Var("transfer_source_user"); back != "" {
		ch.log.Info("park timeout; returning the call", "to", back)
		c.transfer(ch, back, ch.Var("domain_name"))
		return nil
	}
	ch.Hangup(CauseNormalClearing)
	return nil
}

// waitIdle waits (briefly) until a channel's executor has let go of it.
func waitIdle(ch *Channel) {
	for i := 0; i < 200; i++ {
		ch.mu.Lock()
		busy := ch.hasExecutor
		ch.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ParkedCalls lists the parked calls of a domain ("" = all).
func (c *Core) ParkedCalls(domain string) map[string]string {
	out := map[string]string{}
	c.park.mu.Lock()
	defer c.park.mu.Unlock()
	for k, ch := range c.park.slots {
		if ch.IsHungUp() {
			continue
		}
		if domain == "" || strings.EqualFold(ch.Var("domain_name"), domain) {
			out[k] = strings.TrimSpace(ch.Var("caller_id_name") + " " + ch.Var("caller_id_number"))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Call pickup

// rq_pickup <extension> | group: answer a call ringing another extension
// (directed pickup) or any extension in the caller's call group.
func appPickup(ctx context.Context, c *Core, ch *Channel, data string) error {
	target := strings.TrimSpace(data)
	domain := ch.Var("domain_name")
	group := ""
	if target == "" || target == "group" {
		group = ch.Var("call_group")
		if group == "" {
			if u := c.config().Directory.LookupIn(domain, ch.Var("user_name")); u != nil {
				group = u.Vars["call_group"]
			}
		}
		if group == "" {
			_, _ = c.play(ctx, ch, toneErr, false)
			return nil
		}
	}
	var leg, caller *Channel
	for _, b := range c.Channels() {
		v := b.Vars()
		if b.Direction != DirectionOutbound || b.IsAnswered() || b.IsHungUp() || !strings.EqualFold(v["dialed_domain"], domain) {
			continue
		}
		if group == "" && v["dialed_user"] != target {
			continue
		}
		if group != "" {
			u := c.config().Directory.LookupIn(domain, v["dialed_user"])
			if u == nil || !strings.EqualFold(u.Vars["call_group"], group) || u.ID == ch.Var("user_name") {
				continue
			}
		}
		if a := c.Channel(v["originator"]); a != nil && !a.IsAnswered() {
			leg, caller = b, a
			break
		}
	}
	if caller == nil {
		_ = startMedia(ch, false)
		_, _ = c.play(ctx, ch, toneErr, false)
		return nil
	}
	c.log.Info("call pickup", "picked_by", ch.Var("user_name"), "ringing", leg.Var("dialed_user"), "caller", caller.Var("caller_id_number"))
	c.recordEvent("call", fmt.Sprintf("%s picked up the call from %s ringing %s", ch.Var("user_name"), caller.Var("caller_id_number"), leg.Var("dialed_user")))
	return c.takeOver(ctx, ch, caller)
}

// intercept <uuid>: pickup of a known (ringing) channel.
func appIntercept(ctx context.Context, c *Core, ch *Channel, data string) error {
	caller := c.Channel(strings.TrimSpace(data))
	if caller == nil || caller.IsAnswered() || caller.IsHungUp() {
		_ = startMedia(ch, false)
		_, _ = c.play(ctx, ch, toneErr, false)
		return nil
	}
	return c.takeOver(ctx, ch, caller)
}

// takeOver stops whatever caller is doing (ringing other phones), answers
// it and connects it to ch.
func (c *Core) takeOver(ctx context.Context, ch, caller *Channel) error {
	c.adopt(caller)
	waitIdle(caller)
	if caller.IsHungUp() {
		return nil
	}
	if err := ch.AnswerLocal(false); err != nil {
		return err
	}
	if err := caller.AnswerLocal(false); err != nil {
		caller.Hangup(CauseIncompatibleDestination)
		return err
	}
	c.bridgeChannels(ctx, ch, caller)
	if !ch.IsHungUp() && ctx.Err() == nil {
		ch.Hangup(CauseNormalClearing)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Hold music

// setHold plays music on hold to the bridge partner of ch while ch has put
// the call on hold, and reconnects the two when it resumes.
func (c *Core) setHold(ch *Channel, on bool) {
	p := ch.Partner()
	ch.mu.Lock()
	if on == ch.held || (on && p == nil) {
		ch.mu.Unlock()
		return
	}
	ch.held = on
	cancel, done := ch.holdCancel, ch.holdDone
	ch.holdCancel, ch.holdDone = nil, nil
	ch.mu.Unlock()
	if !on {
		if cancel != nil {
			cancel()
			<-done
		}
		if p != nil && ch.Partner() == p && !p.IsHungUp() && !ch.IsHungUp() {
			media.Bridge(ch.ep, p.ep)
		}
		ch.log.Info("call resumed")
		return
	}
	media.Unbridge(ch.ep, p.ep)
	p.ep.SetMode(media.ModeLocal)
	ctx, stop := context.WithCancel(p.ctx)
	d := c.startMOH(ctx, p, firstNonEmpty(ch.Var("hold_music"), p.Var("hold_music")), "", 0, "", nil)
	ch.mu.Lock()
	ch.holdCancel, ch.holdDone = stop, d
	ch.mu.Unlock()
	ch.log.Info("call on hold; music to the other party")
}

// stopHold cancels hold music when a bridge ends.
func (ch *Channel) stopHold() {
	ch.mu.Lock()
	cancel, done := ch.holdCancel, ch.holdDone
	ch.holdCancel, ch.holdDone, ch.held = nil, nil, false
	ch.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// ---------------------------------------------------------------------------
// Attended transfer (REFER with Replaces)

type replaces struct{ callID, toTag, fromTag string }

func parseReplaces(headers string) (replaces, bool) {
	var r replaces
	for _, h := range strings.Split(headers, "&") {
		k, v, ok := strings.Cut(h, "=")
		if !ok || !strings.EqualFold(k, "Replaces") {
			continue
		}
		if dec, err := url.QueryUnescape(v); err == nil {
			v = dec
		}
		parts := strings.Split(v, ";")
		r.callID = parts[0]
		for _, p := range parts[1:] {
			pk, pv, _ := strings.Cut(strings.TrimSpace(p), "=")
			switch strings.ToLower(pk) {
			case "to-tag":
				r.toTag = pv
			case "from-tag":
				r.fromTag = pv
			}
		}
		return r, r.callID != ""
	}
	return r, false
}

// attendedTransfer connects the transferee (the other party of ch) with the
// party of the consultation call identified by Replaces, then ends both of
// the transferring phone's calls.
func (c *Core) attendedTransfer(ch *Channel, tx *sip.ServerTx, rep replaces, dest string) {
	consult := c.findDialog(rep.callID, rep.toTag)
	if consult == nil {
		consult = c.findDialog(rep.callID, rep.fromTag)
	}
	transferee := ch.Partner()
	if consult == nil || transferee == nil {
		_ = tx.Respond(sip.NewResponse(tx.Request, 481, "Replaced Dialog Not Found"))
		return
	}
	resp := sip.NewResponse(tx.Request, 202, "")
	ch.mu.Lock()
	resp.Add("Contact", ch.dlg.contact)
	ch.mu.Unlock()
	_ = tx.Respond(resp)
	target := consult.Partner()
	go ch.sendInDialog("NOTIFY", [][2]string{{"Event", "refer"}, {"Subscription-State", "terminated;reason=noresource"}},
		"message/sipfrag;version=2.0", []byte("SIP/2.0 200 OK\r\n"))
	if target == nil {
		// Consultation still ringing: blind-transfer to its destination.
		ch.log.Info("attended transfer before answer; transferring blind", "dest", dest)
		consult.Hangup(CauseNormalClearing)
		ctxName := firstNonEmpty(ch.Var("user_context"), ch.Var("context"))
		transferee.SetVar("transfer_source_user", ch.Var("user_name"))
		c.transfer(transferee, dest, ctxName)
		ch.Hangup(CauseBlindTransfer)
		return
	}
	ch.log.Info("attended transfer", "transferee", transferee.UUID, "target", target.UUID)
	c.recordEvent("call", fmt.Sprintf("attended transfer by %s: %s connected to %s", ch.Var("caller_id_number"),
		transferee.Var("caller_id_number"), target.Var("destination_number")))
	c.adopt(transferee)
	c.adopt(target)
	ch.Hangup(CauseBlindTransfer)
	consult.Hangup(CauseBlindTransfer)
	waitIdle(transferee)
	transferee.mu.Lock()
	transferee.hasExecutor = true
	transferee.mu.Unlock()
	go c.runExecutorWith(transferee, []dialplan.Action{{App: "uuid_bridge_to", Data: target.UUID}})
}
