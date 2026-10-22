package core

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Presence for desk phones and the browser dialer. Phones SUBSCRIBE to:
//
//   - dialog: a busy lamp field (BLF) key. For an extension the lamp shows
//     whether it is idle, ringing (the key picks the call up) or on a call;
//     for a park slot (*591...) whether a call is parked there.
//   - message-summary: the message lamp (voicemail waiting).
//   - as-feature-event: the DND key kept in step with the apps (the phones'
//     "feature key synchronisation"), with call forwarding.
//
// Each subscription is a SIP dialog of its own; RingQswitch sends a NOTIFY
// with the full state when it starts and whenever the state changes.

const (
	subMinExpires = 60
	subMaxExpires = 3600
	subDefExpires = 1800
)

// BLF is the state of an extension or a park slot.
type BLF struct {
	Resource  string `json:"resource"`            // extension number or park slot (*591)
	State     string `json:"state"`               // idle, ringing or busy
	Direction string `json:"direction,omitempty"` // recipient (called) or initiator (calling)
	Number    string `json:"number,omitempty"`    // the other party (parked: the parked caller)
	Name      string `json:"name,omitempty"`
	Since     int64  `json:"since,omitempty"` // Unix seconds
}

type blfDialog struct {
	id                          string // channel uuid
	callID, localTag, remoteTag string // the SIP dialog with the watched phone, from its side
	state                       string // early or confirmed
	direction                   string
	number, name                string
	since                       time.Time
}

type blfEntry struct {
	resource string
	domain   string
	dialogs  []blfDialog
}

func (e blfEntry) summary() BLF {
	b := BLF{Resource: e.resource, State: "idle"}
	for _, d := range e.dialogs {
		st := "busy"
		if d.state == "early" && d.direction == "recipient" {
			st = "ringing"
		}
		if b.State == "idle" || st == "ringing" && b.State != "ringing" {
			b.State, b.Direction, b.Number, b.Name, b.Since = st, d.direction, d.number, d.name, d.since.Unix()
		}
	}
	return b
}

// signature changes when what a watcher sees changes.
func (e blfEntry) signature() string {
	var parts []string
	for _, d := range e.dialogs {
		parts = append(parts, d.id+":"+d.state+":"+d.direction)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

type subscription struct {
	mu        sync.Mutex
	key       string // Call-ID|From tag
	event     string // dialog, message-summary or as-feature-event
	eventID   string
	user      string // the subscriber
	domain    string
	resource  string // what is watched
	callID    string
	localTag  string
	remoteTag string
	localURI  *sip.Address // the SUBSCRIBE's To (our From)
	remoteURI *sip.Address // the SUBSCRIBE's From (our To)
	target    *sip.URI     // the subscriber's Contact
	src       sip.Addr
	expires   time.Time
	cseq      uint32
	version   int
	gone      bool
}

type presence struct {
	mu      sync.Mutex
	subs    map[string]*subscription
	current map[string]blfEntry // resource@domain (lower case) -> state
	kick    chan struct{}

	vars       map[string]map[string]string // user@domain -> user variables set from a phone or the apps
	varsAt     map[string]time.Time         // user@domain -> when they were set
	lastCaller map[string]string            // user@domain -> who called it last
	lastDialed map[string]string            // user@domain -> what it dialed last
}

func (p *presence) init() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.subs == nil {
		p.subs = map[string]*subscription{}
		p.current = map[string]blfEntry{}
		p.kick = make(chan struct{}, 1)
		p.vars = map[string]map[string]string{}
		p.varsAt = map[string]time.Time{}
		p.lastCaller = map[string]string{}
		p.lastDialed = map[string]string{}
	}
}

// presenceKick asks for the lamps to be worked out again (soon).
func (c *Core) presenceKick() {
	c.pres.init()
	select {
	case c.pres.kick <- struct{}{}:
	default:
	}
}

// ---- state

// computeBLF works out the state of every extension that has a call and of
// every park slot holding one.
func (c *Core) computeBLF() map[string]blfEntry {
	out := map[string]blfEntry{}
	add := func(res, domain string, d blfDialog) {
		if res == "" || domain == "" {
			return
		}
		k := strings.ToLower(res + "@" + domain)
		e := out[k]
		e.resource, e.domain = res, strings.ToLower(domain)
		if len(e.dialogs) < 4 {
			e.dialogs = append(e.dialogs, d)
		}
		out[k] = e
	}
	for _, ch := range c.Channels() {
		if ch.IsHungUp() {
			continue
		}
		v := ch.Vars()
		ch.mu.Lock()
		callID, local, remote := ch.dlg.callID, ch.dlg.localTag, ch.dlg.remoteTag
		since := ch.Created
		ch.mu.Unlock()
		answered := ch.IsAnswered()
		switch {
		case ch.Direction == DirectionOutbound && v["dialed_user"] != "":
			// A leg to the phone: ringing, then on the call.
			d := blfDialog{id: ch.UUID, callID: callID, localTag: remote, remoteTag: local, state: "early", direction: "recipient",
				number: v["caller_id_number"], name: v["caller_id_name"], since: since}
			if answered {
				d.state = "confirmed"
			}
			add(v["dialed_user"], v["dialed_domain"], d)
		case ch.Direction == DirectionInbound && v["user_name"] != "" && v["sip_authorized"] == "true":
			// A call the phone (or the browser dialer) makes: busy.
			add(v["user_name"], v["domain_name"], blfDialog{id: ch.UUID, callID: callID, localTag: remote, remoteTag: local,
				state: "confirmed", direction: "initiator", number: firstNonEmpty(v["callee_id_number"], v["destination_number"]),
				name: v["callee_id_name"], since: since})
		}
	}
	c.park.mu.Lock()
	for key, ch := range c.park.slots {
		if ch.IsHungUp() {
			continue
		}
		lot, slot, _ := strings.Cut(key, "/")
		since := c.park.since[key]
		v := ch.Vars()
		add(slot, lot, blfDialog{id: ch.UUID, state: "confirmed", direction: "recipient", number: v["caller_id_number"],
			name: v["caller_id_name"], since: since})
	}
	c.park.mu.Unlock()
	return out
}

// presenceLoop keeps the lamps in step with calls and parked calls.
func (c *Core) presenceLoop() {
	defer c.wg.Done()
	c.pres.init()
	sub := c.events.Subscribe(4096, func(e *Event) bool {
		switch e.Name {
		case EvChannelCreate, EvChannelProgress, EvChannelProgressMedia, EvChannelAnswer, EvChannelBridge,
			EvChannelHangup, EvChannelDestroy, EvChannelCallState:
			return true
		}
		return false
	})
	defer sub.Close()
	sweep := time.NewTicker(15 * time.Second)
	defer sweep.Stop()
	soon := time.NewTimer(time.Hour)
	defer soon.Stop()
	pending := false
	for {
		select {
		case <-c.stop:
			return
		case e := <-sub.C:
			if e.Name == EvChannelCreate {
				c.noteCall(e.Get("Unique-ID"))
			}
			if !pending {
				pending = true
				soon.Reset(80 * time.Millisecond)
			}
		case <-c.pres.kick:
			if !pending {
				pending = true
				soon.Reset(40 * time.Millisecond)
			}
		case <-soon.C:
			pending = false
			c.updateBLF()
		case <-sweep.C:
			c.expireSubs()
		}
	}
}

// noteCall remembers who called an extension (*69) and what it dialed (*870).
func (c *Core) noteCall(uuid string) {
	ch := c.Channel(uuid)
	if ch == nil {
		return
	}
	v := ch.Vars()
	c.pres.mu.Lock()
	defer c.pres.mu.Unlock()
	switch {
	case ch.Direction == DirectionOutbound && v["dialed_user"] != "":
		if n := v["caller_id_number"]; n != "" && n != v["dialed_user"] {
			c.pres.lastCaller[strings.ToLower(v["dialed_user"]+"@"+v["dialed_domain"])] = n
		}
	case ch.Direction == DirectionInbound && v["user_name"] != "" && v["sip_authorized"] == "true":
		if d := v["destination_number"]; d != "" && !strings.HasPrefix(d, "*") && d != v["user_name"] && d != "redial" {
			c.pres.lastDialed[strings.ToLower(v["user_name"]+"@"+v["domain_name"])] = d
		}
	}
}

func (c *Core) lastCall(kind, user, domain string) string {
	c.pres.init()
	c.pres.mu.Lock()
	defer c.pres.mu.Unlock()
	if kind == "caller" {
		return c.pres.lastCaller[strings.ToLower(user+"@"+domain)]
	}
	return c.pres.lastDialed[strings.ToLower(user+"@"+domain)]
}

func (c *Core) updateBLF() {
	next := c.computeBLF()
	c.pres.mu.Lock()
	prev := c.pres.current
	c.pres.current = next
	changed := map[string]bool{}
	for k, e := range next {
		if p, ok := prev[k]; !ok || p.signature() != e.signature() {
			changed[k] = true
		}
	}
	for k := range prev {
		if _, ok := next[k]; !ok {
			changed[k] = true
		}
	}
	var todo []*subscription
	for _, s := range c.pres.subs {
		if s.event == "dialog" && changed[strings.ToLower(s.resource+"@"+s.domain)] {
			todo = append(todo, s)
		}
	}
	c.pres.mu.Unlock()
	if len(changed) == 0 {
		return
	}
	for _, s := range todo {
		go c.notifySub(s, "")
	}
	domains := map[string][]string{}
	for k := range changed {
		res, dom, _ := strings.Cut(k, "@")
		domains[dom] = append(domains[dom], res)
	}
	for dom, list := range domains {
		sort.Strings(list)
		e := NewEvent(EvCustom, "ringq::blf")
		e.Add("Domain", dom)
		e.Add("Resources", strings.Join(list, ","))
		c.events.Publish(e)
	}
}

func (c *Core) blfEntry(resource, domain string) blfEntry {
	c.pres.init()
	c.pres.mu.Lock()
	defer c.pres.mu.Unlock()
	if e, ok := c.pres.current[strings.ToLower(resource+"@"+domain)]; ok {
		return e
	}
	return blfEntry{resource: resource, domain: domain}
}

// BLFStates returns the extensions and park slots of a domain that are not
// idle.
func (c *Core) BLFStates(domain string) []BLF {
	c.pres.init()
	c.pres.mu.Lock()
	defer c.pres.mu.Unlock()
	out := []BLF{}
	for _, e := range c.pres.current {
		if strings.EqualFold(e.domain, domain) {
			out = append(out, e.summary())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out
}

// ---- user variables set from phones and apps (DND, call forwarding)

// userVarNames are the extension settings phones and the apps may change.
var userVarNames = map[string]bool{"do_not_disturb": true, "forward_all_enabled": true, "forward_all_destination": true,
	"forward_busy_enabled": true, "forward_busy_destination": true, "forward_no_answer_enabled": true,
	"forward_no_answer_destination": true}

// UserVar is an extension's setting as it is now (a change made from a
// phone applies at once, before the database is read again).
func (c *Core) UserVar(user, domain, name string) string {
	if v, ok := c.userOverride(user, domain, name); ok {
		return v
	}
	if u := c.config().Directory.LookupIn(domain, user); u != nil {
		return u.Vars[name]
	}
	return ""
}

func (c *Core) userOverride(user, domain, name string) (string, bool) {
	c.pres.init()
	c.pres.mu.Lock()
	defer c.pres.mu.Unlock()
	v, ok := c.pres.vars[strings.ToLower(user+"@"+domain)][name]
	return v, ok
}

// userVars are a user's variables with the changes made since the last load.
func (c *Core) userVars(u *config.User) map[string]string {
	c.pres.init()
	c.pres.mu.Lock()
	o := c.pres.vars[strings.ToLower(u.ID+"@"+u.Domain)]
	if len(o) == 0 {
		c.pres.mu.Unlock()
		return u.Vars
	}
	m := make(map[string]string, len(u.Vars)+len(o))
	for k, v := range u.Vars {
		m[k] = v
	}
	for k, v := range o {
		m[k] = v
	}
	c.pres.mu.Unlock()
	return m
}

// SetUserVars changes extension settings (DND, forwarding) at once, keeps
// the phones' DND keys in step and has them saved in the database
// (ringq::user_vars event, written to v_extensions).
func (c *Core) SetUserVars(user, domain string, vars map[string]string, source string) error {
	u := c.config().Directory.LookupIn(domain, user)
	if u == nil {
		return fmt.Errorf("no extension %s@%s", user, domain)
	}
	c.pres.init()
	key := strings.ToLower(u.ID + "@" + u.Domain)
	e := NewEvent(EvCustom, "ringq::user_vars")
	e.Add("User", u.ID)
	e.Add("Domain", u.Domain)
	e.Add("Source", source)
	c.pres.mu.Lock()
	m := c.pres.vars[key]
	if m == nil {
		m = map[string]string{}
		c.pres.vars[key] = m
	}
	for k, v := range vars {
		if !userVarNames[k] {
			continue
		}
		m[k] = v
		e.Add("Var-"+k, v)
	}
	c.pres.varsAt[key] = time.Now()
	var todo []*subscription
	for _, s := range c.pres.subs {
		if s.event == "as-feature-event" && strings.EqualFold(s.user, u.ID) && strings.EqualFold(s.domain, u.Domain) {
			todo = append(todo, s)
		}
	}
	c.pres.mu.Unlock()
	c.events.Publish(e)
	c.log.Info("extension settings changed", "user", u.ID+"@"+u.Domain, "source", source, "vars", vars)
	for _, s := range todo {
		go c.notifySub(s, "")
	}
	return nil
}

// reconcileUserVars drops the changes the database now holds (after a
// load): those it shows, and all but the latest seconds' (saved by then; a
// change made on the extension page since wins).
func (c *Core) reconcileUserVars(cfg *config.Config) {
	c.pres.init()
	c.pres.mu.Lock()
	defer c.pres.mu.Unlock()
	for key, m := range c.pres.vars {
		user, domain, _ := strings.Cut(key, "@")
		u := cfg.Directory.LookupIn(domain, user)
		if u == nil || time.Since(c.pres.varsAt[key]) > 3*time.Second {
			delete(c.pres.vars, key)
			delete(c.pres.varsAt, key)
			continue
		}
		for k, v := range m {
			if u.Vars[k] == v || truthy(u.Vars[k]) == truthy(v) && (strings.HasSuffix(k, "_enabled") || k == "do_not_disturb") {
				delete(m, k)
			}
		}
		if len(m) == 0 {
			delete(c.pres.vars, key)
			delete(c.pres.varsAt, key)
		}
	}
}

// ---- SUBSCRIBE

func parseEvent(h string) (string, string) {
	f := strings.Split(h, ";")
	ev := strings.ToLower(strings.TrimSpace(f[0]))
	id := ""
	for _, p := range f[1:] {
		if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "id") {
			id = v
		}
	}
	return ev, id
}

const allowEvents = "dialog, message-summary, as-feature-event"

func (c *Core) handleSubscribe(tx *sip.ServerTx) {
	req := tx.Request
	c.pres.init()
	event, eventID := parseEvent(req.Get("Event"))
	switch event {
	case "dialog", "message-summary", "as-feature-event":
	default:
		resp := sip.NewResponse(req, 489, "")
		resp.Add("Allow-Events", allowEvents)
		_ = tx.Respond(resp)
		return
	}
	from, err1 := req.From()
	to, err2 := req.To()
	if err1 != nil || err2 != nil || from.Tag() == "" {
		_ = tx.Respond(sip.NewResponse(req, 400, ""))
		return
	}
	expires := subDefExpires
	if e := req.Get("Expires"); e != "" {
		if n, err := strconv.Atoi(e); err == nil && n >= 0 {
			expires = n
		}
	}
	if expires > 0 && expires < subMinExpires {
		resp := sip.NewResponse(req, 423, "")
		resp.Add("Min-Expires", strconv.Itoa(subMinExpires))
		_ = tx.Respond(resp)
		return
	}
	expires = min(expires, subMaxExpires)
	key := req.CallID() + "|" + from.Tag()

	if to.Tag() != "" {
		// Refresh, or end (Expires: 0).
		c.pres.mu.Lock()
		s := c.pres.subs[key]
		c.pres.mu.Unlock()
		if s == nil || s.localTag != to.Tag() {
			_ = tx.Respond(sip.NewResponse(req, 481, ""))
			return
		}
		s.mu.Lock()
		s.expires = time.Now().Add(time.Duration(expires) * time.Second)
		if ct := req.Get("Contact"); ct != "" {
			if a, err := sip.ParseAddress(ct); err == nil {
				s.target = a.URI
			}
		}
		s.src = req.Src
		s.mu.Unlock()
		resp := sip.NewResponse(req, 200, "")
		resp.Add("Expires", strconv.Itoa(expires))
		resp.Add("Contact", c.contactFor(s.resource, req.Src))
		_ = tx.Respond(resp)
		if event == "as-feature-event" {
			c.featureEventBody(s.user, s.domain, req.Body)
		}
		if expires == 0 {
			c.dropSub(s)
			go c.notifySub(s, "timeout")
			return
		}
		go c.notifySub(s, "")
		return
	}

	user, res := c.checkAuth(req, "Proxy-Authorization")
	if res == authMissing {
		user, res = c.checkAuth(req, "Authorization")
	}
	switch res {
	case authMissing, authStale:
		c.challenge(tx, true, res == authStale)
		return
	case authBad, authUnknown:
		c.authFailed(tx, from.URI.User, res)
		return
	}
	resource := strings.TrimPrefix(to.URI.User, "park+")
	if resource == "" {
		_ = tx.Respond(sip.NewResponse(req, 404, ""))
		return
	}
	if event == "as-feature-event" && !strings.EqualFold(resource, user.ID) {
		_ = tx.Respond(sip.NewResponse(req, 403, ""))
		return
	}
	ct := req.Get("Contact")
	a, err := sip.ParseAddress(ct)
	if err != nil {
		_ = tx.Respond(sip.NewResponse(req, 400, "Missing Contact"))
		return
	}
	s := &subscription{key: key, event: event, eventID: eventID, user: user.ID, domain: user.Domain, resource: resource,
		callID: req.CallID(), localTag: sip.NewTag(), remoteTag: from.Tag(), localURI: stripTag(to), remoteURI: stripTag(from),
		target: a.URI, src: req.Src, expires: time.Now().Add(time.Duration(expires) * time.Second), cseq: 1}
	resp := sip.NewResponse(req, 200, "")
	resp.SetToTag(s.localTag)
	resp.Add("Expires", strconv.Itoa(expires))
	resp.Add("Contact", c.contactFor(resource, req.Src))
	_ = tx.Respond(resp)
	if event == "as-feature-event" {
		c.featureEventBody(user.ID, user.Domain, req.Body)
	}
	if expires == 0 {
		// A fetch: the state once.
		go c.notifySub(s, "timeout")
		return
	}
	c.pres.mu.Lock()
	if old := c.pres.subs[key]; old != nil {
		old.mu.Lock()
		old.gone = true
		old.mu.Unlock()
	}
	c.pres.subs[key] = s
	c.pres.mu.Unlock()
	c.log.Debug("subscription", "event", event, "user", user.ID+"@"+user.Domain, "resource", resource, "expires", expires)
	go c.notifySub(s, "")
}

func stripTag(a *sip.Address) *sip.Address {
	b := a.Clone()
	b.Params.Del("tag")
	return b
}

func (c *Core) dropSub(s *subscription) {
	c.pres.mu.Lock()
	if c.pres.subs[s.key] == s {
		delete(c.pres.subs, s.key)
	}
	c.pres.mu.Unlock()
	s.mu.Lock()
	s.gone = true
	s.mu.Unlock()
}

func (c *Core) expireSubs() {
	now := time.Now()
	c.pres.mu.Lock()
	var gone []*subscription
	for k, s := range c.pres.subs {
		s.mu.Lock()
		if now.After(s.expires) {
			gone = append(gone, s)
			delete(c.pres.subs, k)
		}
		s.mu.Unlock()
	}
	c.pres.mu.Unlock()
	for _, s := range gone {
		go c.notifySub(s, "timeout")
	}
}

// Subscriptions lists the phones' subscriptions (show subscriptions).
func (c *Core) Subscriptions() []map[string]string {
	c.pres.init()
	c.pres.mu.Lock()
	defer c.pres.mu.Unlock()
	var out []map[string]string
	for _, s := range c.pres.subs {
		s.mu.Lock()
		out = append(out, map[string]string{"event": s.event, "user": s.user, "domain": s.domain, "resource": s.resource,
			"contact": s.target.String(), "source": s.src.String(), "expires": strconv.FormatInt(s.expires.Unix(), 10)})
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["user"]+out[i]["event"]+out[i]["resource"] < out[j]["user"]+out[j]["event"]+out[j]["resource"]
	})
	return out
}

// ---- NOTIFY

// notifySub sends the subscription's state; terminated (a reason) ends it.
func (c *Core) notifySub(s *subscription, terminated string) {
	if c.stack == nil {
		return
	}
	// The state first: the presence lock is never taken under a
	// subscription's lock.
	var entry blfEntry
	var features []string
	switch s.event {
	case "dialog":
		entry = c.blfEntry(s.resource, s.domain)
	case "as-feature-event":
		features = c.featureEventXML(s.user, s.domain)
	}
	s.mu.Lock()
	if s.gone && terminated == "" {
		s.mu.Unlock()
		return
	}
	var bodies []string
	ctype := ""
	switch s.event {
	case "dialog":
		s.version++
		ctype = "application/dialog-info+xml"
		bodies = []string{dialogInfoXML(entry, s.resource, s.domain, s.version)}
	case "message-summary":
		ctype = "application/simple-message-summary"
		c.mwi.mu.Lock()
		n := c.mwi.counts[strings.ToLower(s.resource+"@"+s.domain)]
		c.mwi.mu.Unlock()
		waiting := "no"
		if n[0] > 0 {
			waiting = "yes"
		}
		bodies = []string{fmt.Sprintf("Messages-Waiting: %s\r\nMessage-Account: sip:%s@%s\r\nVoice-Message: %d/%d (0/0)\r\n",
			waiting, s.resource, s.domain, n[0], n[1])}
	case "as-feature-event":
		ctype = "application/x-as-feature-event+xml"
		bodies = features
	}
	state := "active;expires=" + strconv.Itoa(max(0, int(time.Until(s.expires).Seconds())))
	if terminated != "" {
		state = "terminated;reason=" + terminated
	}
	var reqs []*sip.Message
	for _, body := range bodies {
		req := sip.NewRequest("NOTIFY", s.target.Clone())
		from := s.localURI.Clone()
		from.Params.Set("tag", s.localTag)
		to := s.remoteURI.Clone()
		to.Params.Set("tag", s.remoteTag)
		req.Add("From", from.String())
		req.Add("To", to.String())
		req.Add("Call-ID", s.callID)
		s.cseq++
		req.Add("CSeq", strconv.FormatUint(uint64(s.cseq), 10)+" NOTIFY")
		req.Add("Contact", c.contactFor(s.resource, s.src))
		ev := s.event
		if s.eventID != "" {
			ev += ";id=" + s.eventID
		}
		req.Add("Event", ev)
		req.Add("Subscription-State", state)
		req.Add("Content-Type", ctype)
		req.Body = []byte(body)
		reqs = append(reqs, req)
	}
	dst := s.src
	s.mu.Unlock()
	for _, req := range reqs {
		tx, err := c.stack.Request(req, dst)
		if err != nil {
			return
		}
		var final *sip.Message
		for r := range tx.Responses() {
			final = r
		}
		if final == nil || final.StatusCode == 481 || final.StatusCode >= 400 && final.StatusCode != 401 && final.StatusCode != 407 {
			if terminated == "" {
				c.log.Debug("subscription ended by the phone", "user", s.user+"@"+s.domain, "event", s.event, "resource", s.resource)
				c.dropSub(s)
			}
			return
		}
	}
}

func xmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// dialogInfoXML is the full state of an extension or park slot (RFC 4235).
func dialogInfoXML(e blfEntry, resource, domain string, version int) string {
	entity := "sip:" + resource + "@" + domain
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="%d" state="full" entity="%s">`+"\n", version, xmlEsc(entity))
	for _, d := range e.dialogs {
		attrs := fmt.Sprintf(` id="%s"`, xmlEsc(d.id))
		if d.callID != "" {
			attrs += fmt.Sprintf(` call-id="%s" local-tag="%s" remote-tag="%s"`, xmlEsc(d.callID), xmlEsc(d.localTag), xmlEsc(d.remoteTag))
		}
		fmt.Fprintf(&b, `  <dialog%s direction="%s">`+"\n", attrs, d.direction)
		fmt.Fprintf(&b, "    <state>%s</state>\n", d.state)
		if d.number != "" {
			uri := "sip:" + d.number + "@" + domain
			fmt.Fprintf(&b, `    <remote><identity display="%s">%s</identity><target uri="%s"/></remote>`+"\n",
				xmlEsc(firstNonEmpty(d.name, d.number)), xmlEsc(uri), xmlEsc(uri))
		}
		fmt.Fprintf(&b, `    <local><identity>%s</identity><target uri="%s"/></local>`+"\n", xmlEsc(entity), xmlEsc(entity))
		b.WriteString("  </dialog>\n")
	}
	b.WriteString("</dialog-info>\n")
	return b.String()
}

const cstaNS = "http://www.ecma-international.org/standards/ecma-323/csta/ed3"

// featureEventXML are the DND and forwarding states of a user, one NOTIFY
// each (as the phones expect them).
func (c *Core) featureEventXML(user, domain string) []string {
	v := func(n string) string { return c.UserVar(user, domain, n) }
	head := `<?xml version="1.0" encoding="ISO-8859-1"?>` + "\n"
	out := []string{head + fmt.Sprintf(`<DoNotDisturbEvent xmlns="%s"><device>%s</device><doNotDisturbOn>%t</doNotDisturbOn></DoNotDisturbEvent>`,
		cstaNS, xmlEsc(user), truthy(v("do_not_disturb")))}
	for _, f := range [][2]string{{"forwardImmediate", "all"}, {"forwardBusy", "busy"}, {"forwardNoAns", "no_answer"}} {
		ring := ""
		if f[0] == "forwardNoAns" {
			ring = "<ringCount>3</ringCount>"
		}
		out = append(out, head+fmt.Sprintf(`<ForwardingEvent xmlns="%s"><device>%s</device><forwardingType>%s</forwardingType>`+
			`<forwardStatus>%t</forwardStatus><forwardTo>%s</forwardTo>%s</ForwardingEvent>`, cstaNS, xmlEsc(user), f[0],
			truthy(v("forward_"+f[1]+"_enabled")), xmlEsc(v("forward_"+f[1]+"_destination")), ring))
	}
	return out
}

var reXMLTag = regexp.MustCompile(`<(\w+)>([^<]*)</\w+>`)

// featureEventBody applies what a phone's DND or forwarding key asked for
// (SetDoNotDisturb, SetForwarding in the SUBSCRIBE).
func (c *Core) featureEventBody(user, domain string, body []byte) {
	s := string(body)
	if len(s) == 0 {
		return
	}
	vals := map[string]string{}
	for _, m := range reXMLTag.FindAllStringSubmatch(s, -1) {
		vals[m[1]] = strings.TrimSpace(m[2])
	}
	switch {
	case strings.Contains(s, "SetDoNotDisturb"):
		_ = c.SetUserVars(user, domain, map[string]string{"do_not_disturb": strconv.FormatBool(truthy(vals["doNotDisturbOn"]))}, "phone")
	case strings.Contains(s, "SetForwarding"):
		kind := map[string]string{"forwardImmediate": "all", "forwardBusy": "busy", "forwardNoAns": "no_answer"}[vals["forwardingType"]]
		if kind == "" {
			return
		}
		on := truthy(vals["activateForward"])
		set := map[string]string{"forward_" + kind + "_enabled": strconv.FormatBool(on)}
		if d := vals["forwardDN"]; d != "" || on {
			set["forward_"+kind+"_destination"] = d
		}
		_ = c.SetUserVars(user, domain, set, "phone")
	}
}

// CheckSync asks a user's phones to fetch their configuration again (the
// keys after a change); reboot restarts them too. It returns how many
// phones were told.
func (c *Core) CheckSync(user, domain string, reboot bool) int {
	if c.stack == nil {
		return 0
	}
	event := "check-sync"
	if reboot {
		event = "check-sync;reboot=true"
	}
	list := c.reg.Lookup(user, domain)
	for _, b := range list {
		go c.notify(b, event, "", nil)
	}
	c.log.Info("check-sync sent", "user", user+"@"+domain, "reboot", reboot, "phones", len(list))
	return len(list)
}

func init() {
	apiCommands["luarun"] = apiCommand{"luarun app.lua event_notify <profile> reboot|check_sync <user@domain> [vendor]",
		"desk phone reboot / re-provisioning, as the portal sends it (other scripts are not run)", apiLuarun}
	apiCommands["show_subscriptions"] = apiCommand{"show_subscriptions", "the phones' BLF, message and DND subscriptions",
		func(c *Core, args []string) (string, error) { return toJSON(c.Subscriptions()) }}
}

func apiLuarun(c *Core, args []string) (string, error) {
	// luarun app.lua event_notify internal reboot 1001@example.com <vendor>
	if len(args) >= 5 && strings.TrimSuffix(args[0], ".lua") == "app" && args[1] == "event_notify" {
		user, domain, _ := strings.Cut(args[4], "@")
		n := c.CheckSync(user, domain, args[3] == "reboot")
		return fmt.Sprintf("+OK sent to %d phone(s)\n", n), nil
	}
	return "", fmt.Errorf("lua scripts are not run by RingQswitch (%s)", strings.Join(args, " "))
}
