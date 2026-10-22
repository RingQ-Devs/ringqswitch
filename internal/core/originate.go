package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sdp"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// legSpec is one destination of a dial string with its own variables.
type legSpec struct {
	vars   map[string]string
	target string
}

// dialString is "{globals}leg1,leg2|leg3": comma separated legs ring in
// parallel, '|' separated groups are tried in sequence.
type dialString struct {
	vars   map[string]string
	groups [][]legSpec
}

func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	quote := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quote = !quote
		case quote:
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			if depth > 0 {
				depth--
			}
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func parseVarBlock(body string, into map[string]string) {
	for _, kv := range splitTop(body, ',') {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		}
		into[strings.TrimSpace(k)] = v
	}
}

func takeBlock(s string) (body, rest string, ok bool) {
	if s == "" || (s[0] != '{' && s[0] != '[') {
		return "", s, false
	}
	closeCh := byte('}')
	if s[0] == '[' {
		closeCh = ']'
	}
	quote := false
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\'':
			quote = !quote
		case closeCh:
			if !quote {
				return s[1:i], s[i+1:], true
			}
		}
	}
	return "", s, false
}

func parseDialString(s string) (*dialString, error) {
	ds := &dialString{vars: map[string]string{}}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		body, rest, ok := takeBlock(s)
		if !ok {
			return nil, fmt.Errorf("unterminated {} in dial string %q", s)
		}
		parseVarBlock(body, ds.vars)
		s = rest
	}
	for _, g := range splitTop(s, '|') {
		var group []legSpec
		for _, l := range splitTop(g, ',') {
			l = strings.TrimSpace(l)
			leg := legSpec{vars: map[string]string{}}
			for {
				body, rest, ok := takeBlock(l)
				if !ok {
					break
				}
				parseVarBlock(body, leg.vars)
				l = strings.TrimSpace(rest)
			}
			if strings.HasPrefix(l, "{") || strings.HasPrefix(l, "[") {
				return nil, fmt.Errorf("unterminated block in dial string %q", s)
			}
			if l == "" {
				continue
			}
			leg.target = l
			group = append(group, leg)
		}
		if len(group) > 0 {
			ds.groups = append(ds.groups, group)
		}
	}
	if len(ds.groups) == 0 {
		return nil, errors.New("empty dial string")
	}
	return ds, nil
}

// target is a concrete SIP destination.
type target struct {
	ruri   *sip.URI
	to     *sip.Address
	dst    sip.Addr
	gw     *Gateway
	number string
	name   string
	vars   map[string]string
}

const maxRouteDepth = 6

// legDomain is the domain a leg is placed in: the leg's domain_name
// variable, the originating channel's, or the default domain.
func (c *Core) legDomain(a *Channel, vars map[string]string) string {
	if d := vars["domain_name"]; d != "" {
		return d
	}
	if a != nil {
		if d := a.Var("domain_name"); d != "" {
			return d
		}
	}
	return c.config().Directory.Domain
}

// resolveTarget turns one dial string destination into SIP targets.
//
//	user/<id>[@<domain>]            registered contacts of a directory user
//	gateway/<name|uuid>/<number>     through a trunk
//	ringq/gateway/<name|uuid>/<n>    same (also sofia/gateway/..., as
//	                                 older RingQ dialplans spell it)
//	ringq/<profile>/<user@host|uri>  a local user or a SIP URI (or sofia/...)
//	sip:<uri>                        a SIP URI
//	loopback/<number>[/<context>]    whatever the dialplan bridges number to
//	error/<cause>                    fail with a cause
func (c *Core) resolveTarget(a *Channel, spec string, vars map[string]string, depth int) ([]target, Cause) {
	if depth > maxRouteDepth {
		c.log.Warn("routing loop", "target", spec)
		return nil, CauseExchangeRoutingError
	}
	switch {
	case strings.HasPrefix(spec, "user/"):
		id, domain, _ := strings.Cut(strings.TrimPrefix(spec, "user/"), "@")
		if domain == "" {
			domain = c.legDomain(a, vars)
		}
		return c.resolveUser(a, id, domain, vars, depth)
	case strings.HasPrefix(spec, "ringq/gateway/"), strings.HasPrefix(spec, "sofia/gateway/"):
		return c.resolveGateway(spec[strings.Index(spec, "/gateway/")+len("/gateway/"):])
	case strings.HasPrefix(spec, "gateway/"):
		return c.resolveGateway(strings.TrimPrefix(spec, "gateway/"))
	case strings.HasPrefix(spec, "ringq/"), strings.HasPrefix(spec, "sofia/"):
		_, rest, _ := strings.Cut(spec, "/")
		_, dest, ok := strings.Cut(rest, "/")
		if !ok || dest == "" {
			return nil, CauseInvalidURL
		}
		if !strings.HasPrefix(dest, "sip:") && !strings.HasPrefix(dest, "sips:") {
			user, host, _ := strings.Cut(dest, "@")
			if host == "" || c.config().Directory.Find(host) != nil {
				return c.resolveUser(a, user, firstNonEmpty(host, c.legDomain(a, vars)), vars, depth)
			}
			dest = "sip:" + dest
		}
		return c.resolveSIP(dest)
	case strings.HasPrefix(spec, "sip:"):
		return c.resolveSIP(spec)
	case strings.HasPrefix(spec, "loopback/"):
		return c.resolveLoopback(a, strings.TrimPrefix(spec, "loopback/"), vars, depth+1)
	case strings.HasPrefix(spec, "error/"):
		return nil, ParseCause(strings.TrimPrefix(spec, "error/"))
	}
	return nil, CauseInvalidURL
}

func (c *Core) resolveUser(a *Channel, id, domain string, vars map[string]string, depth int) ([]target, Cause) {
	u := c.config().Directory.LookupIn(domain, id)
	if u == nil || u.Disabled {
		return nil, CauseUnallocatedNumber
	}
	uv := c.userVars(u) // with DND and forwarding changed from phones
	if truthy(uv["do_not_disturb"]) {
		return nil, CauseUserBusy
	}
	if c.cc.blocksDirect(u.ID, u.Domain) {
		c.log.Info("agent on break with direct calls blocked", "user", u.ID+"@"+u.Domain)
		return nil, CauseUserBusy
	}
	if truthy(uv["forward_all_enabled"]) {
		if dest := uv["forward_all_destination"]; dest != "" && dest != u.ID {
			c.log.Info("call forward all", "user", u.ID+"@"+u.Domain, "destination", dest)
			return c.resolveLoopback(a, dest+"/"+firstNonEmpty(u.Vars["user_context"], u.Domain), vars, depth+1)
		}
	}
	bindings := c.reg.Lookup(u.ID, u.Domain)
	if len(bindings) == 0 {
		return nil, CauseUserNotRegistered
	}
	var out []target
	for _, b := range bindings {
		ruri, err := sip.ParseURI(b.Contact)
		if err != nil {
			continue
		}
		tv := map[string]string{}
		for k, v := range uv {
			tv[k] = v
		}
		tv["dialed_user"] = u.ID
		tv["dialed_domain"] = u.Domain
		tv["presence_id"] = u.ID + "@" + u.Domain
		tv["callee_id_name"] = firstNonEmpty(u.Vars["effective_caller_id_name"], u.ID)
		tv["callee_id_number"] = firstNonEmpty(u.Vars["effective_caller_id_number"], u.ID)
		out = append(out, target{
			ruri:   ruri,
			to:     &sip.Address{URI: &sip.URI{Scheme: "sip", User: u.ID, Host: u.Domain}},
			dst:    b.Src,
			number: u.ID,
			name:   fmt.Sprintf("ringq/internal/%s@%s", u.ID, u.Domain),
			vars:   tv,
		})
	}
	if len(out) == 0 {
		return nil, CauseUserNotRegistered
	}
	return out, 0
}

func (c *Core) resolveGateway(rest string) ([]target, Cause) {
	name, number, ok := strings.Cut(rest, "/")
	if !ok || number == "" {
		return nil, CauseInvalidURL
	}
	gw := c.gateway(name)
	if gw == nil {
		return nil, CauseInvalidGateway
	}
	dst, err := gw.resolve()
	if err != nil {
		c.log.Warn("gateway unreachable", "gateway", gw.cfg.Name, "err", err)
		return nil, CauseGatewayDown
	}
	host, port := gw.hostPort()
	ruri := &sip.URI{Scheme: "sip", User: number, Host: host, Port: port}
	if gw.cfg.Transport == "tcp" {
		ruri.Params.Set("transport", "tcp")
	}
	return []target{{
		ruri:   ruri,
		to:     &sip.Address{URI: &sip.URI{Scheme: "sip", User: number, Host: gw.cfg.Realm}},
		dst:    dst,
		gw:     gw,
		number: number,
		name:   fmt.Sprintf("ringq/gateway/%s/%s", gw.cfg.Name, number),
		vars: map[string]string{"gateway_name": gw.cfg.Name, "sip_gateway_name": gw.cfg.Name, "gateway_uuid": gw.cfg.UUID,
			"gateway_label": firstNonEmpty(gw.cfg.Label, gw.cfg.Name)},
	}}, 0
}

func (c *Core) resolveSIP(spec string) ([]target, Cause) {
	u, err := sip.ParseURI(spec)
	if err != nil {
		return nil, CauseInvalidURL
	}
	// A registered contact is reached at the address it registered from
	// (NAT), not at the possibly private address in the URI.
	var dst sip.Addr
	if b := c.reg.byContact(u.String()); b != nil {
		dst = b.Src
	} else if dst, err = sip.Resolve(u.Host, u.Port, u.Transport()); err != nil {
		return nil, CauseNoRouteDestination
	}
	return []target{{
		ruri: u, to: &sip.Address{URI: u.Clone()}, dst: dst, number: u.User,
		name: "ringq/external/" + strings.TrimPrefix(spec, "sip:"), vars: map[string]string{},
	}}, 0
}

// resolveLoopback implements the loopback endpoint for the common
// case: the destination is routed through the dialplan of the given context
// with a scratch set of variables, and whatever the first bridge there would
// dial becomes the target (carrying the variables the dialplan set). It
// covers forwarding to external numbers, other extensions and ring groups.
func (c *Core) resolveLoopback(a *Channel, rest string, vars map[string]string, depth int) ([]target, Cause) {
	if depth > maxRouteDepth {
		c.log.Warn("loopback routing loop", "destination", rest)
		return nil, CauseExchangeRoutingError
	}
	parts := strings.Split(rest, "/")
	dest := parts[0]
	ctxName := ""
	if len(parts) > 1 {
		ctxName = parts[1]
	}
	if dest == "" {
		return nil, CauseInvalidURL
	}
	sv := newScratchVars(c)
	if a != nil {
		for k, v := range a.Vars() {
			sv.vars[k] = v
		}
	}
	for k, v := range vars {
		sv.vars[k] = v
	}
	if ctxName == "" {
		ctxName = firstNonEmpty(sv.vars["user_context"], c.legDomain(a, vars))
		if ctxName == "public" {
			ctxName = c.legDomain(a, vars)
		}
	}
	sv.vars["destination_number"] = dest
	sv.vars["context"] = ctxName
	sv.vars["loopback_leg"] = "B"
	sv.vars["is_loopback"] = "true"
	actions, _, err := c.config().Dialplan.Hunt(ctxName, sv, c.domainNow(firstNonEmpty(sv.vars["domain_name"], ctxName)))
	if err != nil || len(actions) == 0 {
		c.log.Info("loopback: no route", "destination", dest, "context", ctxName)
		return nil, CauseNoRouteDestination
	}
	carried := map[string]string{} // variables the dialplan set for the leg
	for _, act := range actions {
		data := sv.Expand(act.Data)
		switch act.App {
		case "set", "export", "set_profile_var":
			name, value, _ := strings.Cut(data, "=")
			name = strings.TrimPrefix(strings.TrimSpace(name), "nolocal:")
			sv.vars[name] = value
			carried[name] = value
		case "unset":
			delete(sv.vars, strings.TrimSpace(data))
			delete(carried, strings.TrimSpace(data))
		case "hangup":
			return nil, ParseCause(data)
		case "transfer":
			f := strings.Fields(data)
			if len(f) == 0 {
				continue
			}
			next := f[0] + "/" + ctxName
			if len(f) == 3 {
				next = f[0] + "/" + f[2]
			} else if len(f) == 2 {
				next = f[0] + "/" + f[1]
			}
			return c.resolveLoopback(a, next, mergeVars(vars, carried), depth+1)
		case "bridge":
			ds, err := parseDialString(data)
			if err != nil {
				return nil, CauseInvalidURL
			}
			legVars := mergeVars(vars, carried, ds.vars)
			if n := firstNonEmpty(carried["effective_caller_id_number"], ds.vars["origination_caller_id_number"]); n != "" {
				legVars["origination_caller_id_number"] = n
			}
			if n := firstNonEmpty(carried["effective_caller_id_name"], ds.vars["origination_caller_id_name"]); n != "" {
				legVars["origination_caller_id_name"] = n
			}
			last := CauseNoRouteDestination
			for _, group := range ds.groups {
				var out []target
				for _, leg := range group {
					lv := mergeVars(legVars, leg.vars)
					ts, cause := c.resolveTarget(a, leg.target, lv, depth+1)
					if len(ts) == 0 {
						last = cause
						continue
					}
					for _, t := range ts {
						t.vars = mergeVars(lv, t.vars)
						out = append(out, t)
					}
				}
				if len(out) > 0 {
					return out, 0
				}
			}
			return nil, last
		}
	}
	c.log.Info("loopback: dialplan does not bridge", "destination", dest, "context", ctxName)
	return nil, CauseNoRouteDestination
}

func mergeVars(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// scratchVars is a variable set for dialplan hunts that have no channel.
type scratchVars struct {
	c    *Core
	vars map[string]string
}

func newScratchVars(c *Core) *scratchVars { return &scratchVars{c: c, vars: map[string]string{}} }

func (s *scratchVars) Var(name string) string { return s.vars[name] }

func (s *scratchVars) SetVar(name, value string) {
	if value == "" {
		delete(s.vars, name)
		return
	}
	s.vars[name] = value
}

func (s *scratchVars) Expand(v string) string { return s.c.expand(v, s.Var) }

func (s *scratchVars) Field(name string) string { return s.c.lookupVar(name, s.Var) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

type legEventKind int

const (
	legRinging legEventKind = iota
	legEarly
	legAnswered
	legFailed
)

type legEvent struct {
	kind  legEventKind
	ch    *Channel
	cause Cause
}

func sendLegEvent(ch chan<- legEvent, ev legEvent) {
	select {
	case ch <- ev:
	default:
	}
}

// originate places outbound legs for a dial string on behalf of a (which may
// be nil for API calls) and returns the first leg that answers.
func (c *Core) originate(ctx context.Context, a *Channel, dial string, extra map[string]string) (*Channel, Cause) {
	ds, err := parseDialString(dial)
	if err != nil {
		c.log.Warn("bad dial string", "dial", dial, "err", err)
		return nil, CauseInvalidURL
	}
	globals := map[string]string{}
	for k, v := range extra {
		globals[k] = v
	}
	for k, v := range ds.vars {
		globals[k] = v
	}
	last := CauseNoRouteDestination
	for _, group := range ds.groups {
		b, cause := c.originateGroup(ctx, a, globals, group)
		if b != nil {
			return b, CauseNone
		}
		last = cause
		if ctx.Err() != nil || (a != nil && a.IsHungUp()) {
			return nil, CauseOriginatorCancel
		}
	}
	return nil, last
}

// legHook, carried in the originate context, is told why each leg that
// did not answer failed (vars are the leg's variables). The call centre
// uses it to apply per-agent no-answer and busy delays.
type legHook func(vars map[string]string, cause Cause)

type legHookKey struct{}

func withLegHook(ctx context.Context, h legHook) context.Context {
	return context.WithValue(ctx, legHookKey{}, h)
}

// plannedLeg is a dial string destination with its timing.
type plannedLeg struct {
	spec    legSpec
	vars    map[string]string
	delay   time.Duration // leg_delay_start
	timeout time.Duration // leg_timeout or the call timeout
}

func secondsVar(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

func (c *Core) originateGroup(ctx context.Context, a *Channel, globals map[string]string, group []legSpec) (*Channel, Cause) {
	timeout := c.config().Settings.CallTimeout
	if a != nil {
		if d := secondsVar(a.Var("call_timeout")); d > 0 {
			timeout = d
		}
	}
	if d := secondsVar(firstNonEmpty(globals["originate_timeout"], globals["call_timeout"])); d > 0 {
		timeout = d
	}
	var plan []plannedLeg
	var longest time.Duration
	for _, spec := range group {
		vars := mergeVars(globals, spec.vars)
		p := plannedLeg{spec: spec, vars: vars, timeout: timeout}
		if d := secondsVar(vars["leg_timeout"]); d > 0 {
			p.timeout = d
		}
		p.delay = secondsVar(vars["leg_delay_start"])
		longest = max(longest, p.delay+p.timeout)
		plan = append(plan, p)
	}

	events := make(chan legEvent, 64)
	starts := make(chan int, len(plan))
	var legs []*Channel
	var timers []*time.Timer
	var causes []Cause
	defer func() {
		for _, t := range timers {
			t.Stop()
		}
	}()
	outstanding := 0 // legs started and not failed, plus legs still waiting to start
	reported := map[*Channel]bool{}
	hook, _ := ctx.Value(legHookKey{}).(legHook)
	launch := func(p plannedLeg) {
		targets, cause := c.resolveTarget(a, p.spec.target, p.vars, 0)
		if len(targets) == 0 {
			c.log.Info("originate target unavailable", "target", p.spec.target, "cause", cause.String())
			causes = append(causes, cause)
			if hook != nil {
				hook(p.vars, cause)
			}
			return
		}
		for _, t := range targets {
			b, err := c.newOutboundLeg(a, t, p.vars, events)
			if err != nil {
				c.log.Warn("originate failed", "target", p.spec.target, "err", err)
				causes = append(causes, CauseDestinationOutOfOrder)
				if hook != nil {
					hook(p.vars, CauseDestinationOutOfOrder)
				}
				continue
			}
			legs = append(legs, b)
			outstanding++
			leg := b
			timers = append(timers, time.AfterFunc(p.timeout, func() {
				if !leg.IsAnswered() {
					leg.Hangup(CauseNoAnswer)
					// Report now: the phone's answer to our CANCEL may be
					// slow or never come.
					sendLegEvent(events, legEvent{kind: legFailed, ch: leg, cause: CauseNoAnswer})
				}
			}))
		}
	}
	for i, p := range plan {
		if p.delay == 0 {
			launch(p)
			continue
		}
		outstanding++
		idx := i
		timers = append(timers, time.AfterFunc(p.delay, func() { starts <- idx }))
	}
	if outstanding == 0 {
		return nil, pickCause(causes)
	}
	// Safety net in case a leg never reports back.
	timer := time.NewTimer(longest + 5*time.Second)
	defer timer.Stop()
	single := len(plan) == 1 && len(legs) == 1
	ignoreEarly := truthy(globals["ignore_early_media"])
	var early *Channel
	hangupAll := func(cause Cause, except *Channel) {
		for _, l := range legs {
			if l != except {
				l.Hangup(cause)
			}
		}
	}
	for outstanding > 0 {
		select {
		case <-ctx.Done():
			hangupAll(CauseOriginatorCancel, nil)
			c.stopEarly(a, early)
			return nil, CauseOriginatorCancel
		case <-timer.C:
			hangupAll(CauseNoAnswer, nil)
			c.stopEarly(a, early)
			return nil, CauseNoAnswer
		case i := <-starts:
			outstanding--
			launch(plan[i])
		case ev := <-events:
			switch ev.kind {
			case legRinging:
				if a != nil {
					a.RingReady()
				}
			case legEarly:
				if a == nil {
					continue
				}
				if single && early == nil && !ignoreEarly {
					if err := c.startEarly(a, ev.ch); err != nil {
						a.log.Warn("early media failed", "err", err)
						a.RingReady()
					} else {
						early = ev.ch
					}
				} else {
					a.RingReady()
				}
			case legAnswered:
				hangupAll(CauseLoseRace, ev.ch)
				return ev.ch, CauseNone
			case legFailed:
				if reported[ev.ch] {
					continue // already counted (leg timeout, then the 487)
				}
				reported[ev.ch] = true
				outstanding--
				causes = append(causes, ev.cause)
				if hook != nil {
					hook(ev.ch.Vars(), ev.cause)
				}
				if ev.ch == early {
					c.stopEarly(a, early)
					early = nil
				}
			}
		}
	}
	return nil, pickCause(causes)
}

// pickCause chooses the most meaningful failure of parallel legs.
func pickCause(causes []Cause) Cause {
	if len(causes) == 0 {
		return CauseNoRouteDestination
	}
	for _, pref := range []Cause{CauseUserBusy, CauseCallRejected, CauseNoAnswer, CauseNoUserResponse} {
		for _, c := range causes {
			if c == pref {
				return c
			}
		}
	}
	return causes[len(causes)-1]
}

func (c *Core) startEarly(a, b *Channel) error {
	if a.Direction == DirectionInbound && !a.IsAnswered() {
		if err := c.answerFromPeer(a, b, true); err != nil {
			return err
		}
	}
	media.Bridge(a.ep, b.ep)
	return nil
}

func (c *Core) stopEarly(a, b *Channel) {
	if a == nil || b == nil {
		return
	}
	media.Unbridge(a.ep, b.ep)
}

// answerFromPeer answers (or pre-answers) inbound leg a using the media
// negotiated with outbound leg b, translated to a's payload numbering.
func (c *Core) answerFromPeer(a, b *Channel, early bool) error {
	a.mu.Lock()
	hasLocal := a.localSDP != nil
	offer := a.offer
	a.mu.Unlock()
	if hasLocal {
		if early {
			return nil
		}
		return a.Answer(nil)
	}
	b.mu.Lock()
	bs := b.remoteSDP
	b.mu.Unlock()
	if bs == nil {
		return errors.New("peer has no media")
	}
	ba, _ := bs.Audio()
	if ba == nil {
		return errors.New("peer has no audio")
	}
	var s *sdp.Session
	if offer == nil {
		codecs := ba.Codecs()
		a.setCodecs(codecs)
		a.mu.Lock()
		a.lateOffer = true
		s = sdp.NewOffer(a.localLocked(), codecs, bs.Direction(ba))
		a.mu.Unlock()
	} else {
		oa, _ := offer.Audio()
		mapped := sdp.MapToOffer(oa.Codecs(), ba.Codecs())
		if len(mapped) == 0 {
			return errIncompatible
		}
		a.setCodecs(mapped)
		s = sdp.Answer(offer, a.local(), mapped, bs.Direction(ba))
	}
	if early {
		return a.PreAnswer(s)
	}
	return a.Answer(s)
}

// newOutboundLeg creates an outbound channel and sends the INVITE.
func (c *Core) newOutboundLeg(a *Channel, t target, vars map[string]string, events chan legEvent) (*Channel, error) {
	ep, err := c.media.Allocate()
	if err != nil {
		return nil, err
	}
	b := c.newChannel(DirectionOutbound)
	b.ep = ep
	b.gateway = t.gw

	var aVar func(string) string = func(string) string { return "" }
	if a != nil {
		aVar = a.Var
	}
	// Variables of the new leg: exported variables of the caller, then the
	// dial string's, then the target's own (directory user, loopback route).
	all := map[string]string{}
	if a != nil {
		for _, name := range strings.Split(a.Var("export_vars"), ",") {
			if name = strings.TrimSpace(name); name != "" {
				// A nolocal: export is meant for the B leg only: it wins over
				// the caller's own variable of that name (e.g. sip_from_user).
				if v := firstNonEmpty(a.Var("nolocal_"+name), a.Var(name)); v != "" {
					all[name] = v
				}
			}
		}
	}
	for k, v := range vars {
		all[k] = v
	}
	for k, v := range t.vars {
		all[k] = v
	}
	cidNum := firstNonEmpty(t.vars["origination_caller_id_number"], vars["origination_caller_id_number"], aVar("effective_caller_id_number"), aVar("caller_id_number"), "0000000000")
	cidName := firstNonEmpty(t.vars["origination_caller_id_name"], vars["origination_caller_id_name"], aVar("effective_caller_id_name"), aVar("caller_id_name"), cidNum)

	fromUser, fromHost := cidNum, ""
	if t.gw != nil {
		fromHost = t.gw.cfg.FromDomain
		if !t.gw.cfg.CallerIDInFrom {
			fromUser = t.gw.cfg.FromUser
		}
	} else {
		fromHost, _ = c.sipAdvertise(t.dst)
	}
	idHost := fromHost // host of P-Asserted-Identity / Remote-Party-ID
	// sip_from_user, sip_from_display and sip_from_host (an
	// outbound rule's "Outbound call settings") override the From header.
	if v := strings.TrimSpace(all["sip_from_user"]); v != "" && !strings.ContainsAny(v, "@<>\" \r\n") {
		fromUser = v
	}
	if v := strings.TrimSpace(all["sip_from_host"]); v != "" && !strings.ContainsAny(v, "@<>\" \r\n") {
		fromHost = v
	}
	display := cidName
	if v := strings.TrimSpace(all["sip_from_display"]); v != "" && !strings.ContainsAny(v, "\r\n") {
		display = v
	}
	from := &sip.Address{Display: display, URI: &sip.URI{Scheme: "sip", User: fromUser, Host: fromHost}}
	b.dlg = dialog{
		callID: sip.NewCallID(), localTag: sip.NewTag(),
		localURI: from, remoteURI: t.to, remoteTarget: t.ruri, dst: t.dst,
		contact: c.contactFor(fromUser, t.dst),
	}
	for k, v := range all {
		b.vars[k] = v
	}
	b.vars["destination_number"] = t.number
	b.vars["caller_id_number"] = cidNum
	b.vars["caller_id_name"] = cidName
	b.vars["channel_name"] = t.name
	b.vars["sip_call_id"] = b.dlg.callID
	b.vars["network_addr"] = t.dst.AP.Addr().String()
	b.vars["context"] = firstNonEmpty(t.vars["user_context"], aVar("context"), "default")
	if a != nil {
		b.vars["originator"] = a.UUID
		b.vars["originating_leg_uuid"] = a.UUID
	}

	// Media offer: pass the caller's codecs straight through when it has not
	// negotiated media yet; otherwise offer what we can transcode to.
	var offer *sdp.Session
	if a != nil {
		a.mu.Lock()
		aOffer, aLocal := a.offer, a.localSDP
		a.mu.Unlock()
		if aOffer != nil && aLocal == nil {
			offer = sdp.OfferFromPeer(aOffer, b.local())
		}
		if offer == nil && aLocal != nil {
			prefer := ""
			if sc, ok := a.ep.SendCodec(); ok {
				prefer = sc.Name
			}
			offer = sdp.NewOffer(b.local(), c.localOfferCodecs(prefer), "sendrecv")
		}
	}
	if offer == nil {
		offer = sdp.NewOffer(b.local(), c.localOfferCodecs(""), "sendrecv")
	}
	b.localSDP = offer
	ep.OnDTMF(b.queueDTMF)

	b.mu.Lock()
	req := b.dlg.request("INVITE")
	b.inviteSeq = b.dlg.localSeq
	b.mu.Unlock()
	req.Add("Allow", allowHeader)
	req.Add("Supported", "replaces")
	// The caller ID header (sip_cid_type: pid, rpid or none). Gateways that
	// keep their account in From send P-Asserted-Identity by default.
	cidType := strings.ToLower(strings.TrimSpace(all["sip_cid_type"]))
	if cidType == "" && t.gw != nil && !t.gw.cfg.CallerIDInFrom {
		cidType = "pid"
	}
	if idHost == "" {
		idHost = fromHost
	}
	id := &sip.Address{Display: cidName, URI: &sip.URI{Scheme: "sip", User: cidNum, Host: idHost}}
	switch cidType {
	case "pid", "pai":
		req.Add("P-Asserted-Identity", id.String())
	case "rpid":
		req.Add("Remote-Party-ID", id.String()+";party=calling;screen=yes;privacy=off")
	}
	for k, v := range all {
		if name, ok := strings.CutPrefix(k, "sip_h_"); ok && name != "" {
			req.Add(name, v)
		}
	}
	if a != nil {
		// X- headers of the caller pass through to the B leg.
		for k, v := range a.Vars() {
			if name, ok := strings.CutPrefix(k, "sip_h_"); ok && name != "" && all[k] == "" {
				req.Add(name, v)
			}
		}
	}
	req.Add("Content-Type", "application/sdp")
	req.Body = offer.Marshal()
	b.inviteReq = req.Clone()

	c.addChannel(b)
	b.notice(fmt.Sprintf("New Channel %s [%s]", b.name(), b.UUID))
	b.log.Info("new outbound leg", "to", t.number, "dst", t.dst.String(), "cid", cidNum)
	b.fire(EvChannelCreate)
	tx, err := c.stack.Request(req, t.dst)
	if err != nil {
		b.hangup(CauseDestinationOutOfOrder, hangupRemoteFinal)
		return nil, err
	}
	b.mu.Lock()
	b.inviteCli = tx
	b.mu.Unlock()
	go c.runOutboundLeg(b, tx, events)
	return b, nil
}

func (c *Core) runOutboundLeg(b *Channel, tx *sip.ClientTx, events chan<- legEvent) {
	for {
		var final *sip.Message
		for resp := range tx.Responses() {
			if resp.StatusCode < 200 {
				c.onLegProvisional(b, tx, resp, events)
				continue
			}
			final = resp
		}
		if final == nil {
			b.hangup(CauseRecoveryOnTimerExpire, hangupRemoteFinal)
			sendLegEvent(events, legEvent{kind: legFailed, ch: b, cause: CauseRecoveryOnTimerExpire})
			return
		}
		code := final.StatusCode
		b.SetVar("sip_term_status", strconv.Itoa(code))
		if b.IsHungUp() && code >= 300 {
			// We gave up on this leg (leg timeout, lost race, caller gone):
			// report our cause, not the 487 that answers our CANCEL.
			sendLegEvent(events, legEvent{kind: legFailed, ch: b, cause: b.Cause()})
			return
		}
		if code < 300 {
			c.onLegAnswered(b, tx, final, events)
			return
		}
		if (code == 401 || code == 407) && b.gateway != nil && !b.IsHungUp() {
			if next, err := c.retryWithAuth(b, final); err == nil {
				tx = next
				continue
			}
		}
		cause := CauseFromSIP(code)
		b.log.Info("outbound leg failed", "status", code, "cause", cause.String())
		b.hangup(cause, hangupRemoteFinal)
		sendLegEvent(events, legEvent{kind: legFailed, ch: b, cause: cause})
		return
	}
}

func (c *Core) onLegProvisional(b *Channel, tx *sip.ClientTx, resp *sip.Message, events chan<- legEvent) {
	if resp.StatusCode == 100 {
		return
	}
	b.mu.Lock()
	b.gotProvisional = true
	cancelPending, hungup := b.cancelPending, b.hungup
	if to, err := resp.To(); err == nil && to.Tag() != "" && b.dlg.remoteTag == "" {
		b.dlg.remoteTag = to.Tag()
	}
	b.mu.Unlock()
	if cancelPending {
		_ = c.stack.Cancel(tx, reasonHeader(b.Cause()))
		return
	}
	if hungup {
		return
	}
	if resp.HasSDP() {
		if s, err := sdp.Parse(resp.Body); err == nil {
			if a, _ := s.Audio(); a != nil {
				b.applyRemote(s)
				b.setCodecs(a.Codecs())
				b.mu.Lock()
				b.early = true
				b.callState = "EARLY"
				b.markProgressLocked()
				b.mu.Unlock()
				b.fire(EvChannelProgressMedia)
				b.fire(EvChannelCallState)
				sendLegEvent(events, legEvent{kind: legEarly, ch: b})
				return
			}
		}
	}
	b.mu.Lock()
	b.ringing = true
	b.callState = "RINGING"
	b.markProgressLocked()
	b.mu.Unlock()
	b.fire(EvChannelProgress)
	b.fire(EvChannelCallState)
	sendLegEvent(events, legEvent{kind: legRinging, ch: b})
}

func (c *Core) onLegAnswered(b *Channel, tx *sip.ClientTx, resp *sip.Message, events chan<- legEvent) {
	b.mu.Lock()
	if to, err := resp.To(); err == nil {
		b.dlg.remoteTag = to.Tag()
	}
	if ct, err := resp.Contact(); err == nil && !ct.Wildcard {
		b.dlg.remoteTarget = ct.URI.Clone()
	}
	rr := resp.GetAll("Record-Route")
	b.dlg.routeSet = nil
	for i := len(rr) - 1; i >= 0; i-- {
		b.dlg.routeSet = append(b.dlg.routeSet, rr[i])
	}
	ack := b.dlg.ack(b.inviteSeq)
	dst := b.dlg.dst
	hungup := b.hungup
	b.mu.Unlock()
	_ = c.stack.SendStateless(ack, dst)
	tx.SetAck(ack)

	if hungup {
		// We cancelled but the callee answered first: end the call, and
		// still report the leg so the originator stops waiting for it.
		b.mu.Lock()
		b.answered = true
		b.mu.Unlock()
		go b.sendBye(b.Cause())
		sendLegEvent(events, legEvent{kind: legFailed, ch: b, cause: b.Cause()})
		return
	}
	var answer *sdp.Session
	if resp.HasSDP() {
		answer, _ = sdp.Parse(resp.Body)
	}
	var aud *sdp.Media
	if answer != nil {
		aud, _ = answer.Audio()
	}
	if aud == nil {
		b.log.Warn("answer without usable SDP")
		b.mu.Lock()
		b.answered = true
		b.mu.Unlock()
		b.Hangup(CauseIncompatibleDestination)
		sendLegEvent(events, legEvent{kind: legFailed, ch: b, cause: CauseIncompatibleDestination})
		return
	}
	b.mu.Lock()
	early := b.early
	b.mu.Unlock()
	b.applyRemote(answer)
	if early && b.ep != nil {
		b.ep.Relearn() // ringback may have come from another media server
	}
	b.setCodecs(aud.Codecs())
	b.mu.Lock()
	b.answered = true
	b.answeredAt = time.Now()
	b.callState = "ACTIVE"
	b.mu.Unlock()
	b.log.Info("outbound leg answered", "codecs", fmt.Sprint(aud.Codecs()))
	b.fire(EvChannelAnswer)
	b.fire(EvChannelCallState)
	b.runAnswerHooks()
	sendLegEvent(events, legEvent{kind: legAnswered, ch: b})
}

func (c *Core) retryWithAuth(b *Channel, challenge *sip.Message) (*sip.ClientTx, error) {
	b.mu.Lock()
	if b.authTries >= 2 {
		b.mu.Unlock()
		return nil, errors.New("authentication rejected")
	}
	b.authTries++
	gw := b.gateway
	req := b.inviteReq.Clone()
	b.dlg.localSeq++
	b.inviteSeq = b.dlg.localSeq
	seq := b.inviteSeq
	dst := b.dlg.dst
	b.gotProvisional = false
	b.mu.Unlock()
	hdr, authHdr := "WWW-Authenticate", "Authorization"
	if challenge.StatusCode == 407 {
		hdr, authHdr = "Proxy-Authenticate", "Proxy-Authorization"
	}
	chal, ok := sip.ParseDigest(challenge.Get(hdr))
	if !ok {
		return nil, errors.New("unsupported challenge")
	}
	req.Del("Via")
	req.Del("Authorization")
	req.Del("Proxy-Authorization")
	req.Set("CSeq", fmt.Sprintf("%d INVITE", seq))
	req.Add(authHdr, sip.BuildAuthorization(chal, "INVITE", req.RequestURI.String(), gw.authUser(), gw.cfg.Password))
	b.mu.Lock()
	b.inviteReq = req.Clone()
	b.mu.Unlock()
	tx, err := c.stack.Request(req, dst)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.inviteCli = tx
	b.mu.Unlock()
	return tx, nil
}
