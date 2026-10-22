package core

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// Call centre: queues, agents and tiers with the standard call centre semantics.
//
// A caller runs the callcenter application and becomes a member of the
// queue: it hears music on hold while the dispatcher looks for agents. An
// agent is offered calls only while its status is Available (or Available
// (On Demand)), it is not already on a call, and its wrap-up / no-answer /
// busy / reject delays have passed. An agent On Break (an AUX code such as
// Lunch) or Logged Out never gets queue calls. Every status change is
// published as a callcenter::info event, logged, and stored by the database
// writer (rq_agent_status_log) for reports.

// Agent states.
const (
	ccStateIdle      = "Idle"
	ccStateWaiting   = "Waiting"
	ccStateReceiving = "Receiving"
	ccStateInCall    = "In a queue call"
)

var ccStatuses = []string{config.AgentLoggedOut, config.AgentAvailable, config.AgentAvailableOnDemand, config.AgentOnBreak}

// CCStrategies lists the queue strategies.
var CCStrategies = []string{"ring-all", "longest-idle-agent", "round-robin", "top-down",
	"agent-with-least-talk-time", "agent-with-fewest-calls", "sequentially-by-agent-order", "random", "ring-progressively"}

type ccAgent struct {
	cfg         *config.Agent
	status      string
	aux         string
	state       string
	statusSince time.Time
	stateSince  time.Time
	readyAt     time.Time // not offered calls before (wrap-up and delays)
	offered     bool
	lastOffered time.Time
	bridgeStart time.Time
	bridgeEnd   time.Time
	noAnswer    int
	answered    int
	talk        time.Duration
	member      *ccMember
	missed      int // offers not answered today
	day         int
}

func (a *ccAgent) available() bool {
	return a.status == config.AgentAvailable || a.status == config.AgentAvailableOnDemand
}

type ccMember struct {
	uuid      string
	ch        *Channel
	q         *ccQueue
	joined    time.Time
	offering  bool
	tried     map[string]bool
	offers    chan ccOffer
	offered   int // agent offers made
	noAnswer  int
	agent     *ccAgent
	answered  time.Time
	state     string
	cidName   string
	cidNumber string
}

type ccOffer struct {
	agents  []*ccAgent
	stagger time.Duration // ring-progressively
}

type ccQueue struct {
	cfg          *config.Queue
	members      []*ccMember
	rr           int
	noAgentSince time.Time
	day          int
	answered     int
	abandoned    int
	timedOut     int
	waitTotal    time.Duration
	talkTotal    time.Duration
}

type callCenter struct {
	c      *Core
	mu     sync.Mutex
	agents map[string]*ccAgent // by lower-case uuid
	queues map[string]*ccQueue // by lower-case uuid
	tiers  map[string][]config.Tier
	aux    []config.AuxCode
	wake   chan struct{}
}

func newCallCenter(c *Core) *callCenter {
	return &callCenter{c: c, agents: map[string]*ccAgent{}, queues: map[string]*ccQueue{},
		tiers: map[string][]config.Tier{}, wake: make(chan struct{}, 1)}
}

func (cc *callCenter) poke() {
	select {
	case cc.wake <- struct{}{}:
	default:
	}
}

// sync applies a (re)loaded configuration, keeping the runtime state of
// agents and the callers waiting in queues.
func (cc *callCenter) sync(f *config.Features) {
	now := time.Now()
	cc.mu.Lock()
	seen := map[string]bool{}
	for id, cfg := range f.Agents {
		seen[id] = true
		a := cc.agents[id]
		if a == nil {
			status := firstNonEmpty(cfg.SavedStatus, cfg.Status, config.AgentLoggedOut)
			if !validStatus(status) {
				status = config.AgentLoggedOut
			}
			a = &ccAgent{status: status, statusSince: now, stateSince: now, state: ccStateIdle}
			if status == config.AgentOnBreak {
				a.aux = cfg.SavedAux
			}
			if a.available() {
				a.state = ccStateWaiting
			}
			cc.agents[id] = a
		}
		a.cfg = cfg
	}
	for id, a := range cc.agents {
		if !seen[id] && a.member == nil && !a.offered {
			delete(cc.agents, id)
		}
	}
	qseen := map[string]bool{}
	for id, cfg := range f.Queues {
		qseen[id] = true
		q := cc.queues[id]
		if q == nil {
			q = &ccQueue{}
			cc.queues[id] = q
		}
		q.cfg = cfg
	}
	for id, q := range cc.queues {
		if !qseen[id] && len(q.members) == 0 {
			delete(cc.queues, id)
		}
	}
	cc.tiers = map[string][]config.Tier{}
	for _, t := range f.Tiers {
		k := strings.ToLower(t.QueueUUID)
		cc.tiers[k] = append(cc.tiers[k], t)
	}
	for k := range cc.tiers {
		ts := cc.tiers[k]
		sort.SliceStable(ts, func(i, j int) bool {
			if ts[i].Level != ts[j].Level {
				return ts[i].Level < ts[j].Level
			}
			return ts[i].Position < ts[j].Position
		})
	}
	cc.aux = append([]config.AuxCode(nil), f.AuxCodes...)
	cc.mu.Unlock()
	cc.poke()
}

func validStatus(s string) bool {
	for _, v := range ccStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// normalizeStatus accepts the usual spellings ("available", "on break",
// "break", "logout"...).
func normalizeStatus(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.Trim(s, "'\""))) {
	case "available", "login", "online", "ready":
		return config.AgentAvailable, true
	case "available (on demand)", "on demand", "available_on_demand":
		return config.AgentAvailableOnDemand, true
	case "on break", "break", "on_break", "aux", "away", "not ready":
		return config.AgentOnBreak, true
	case "logged out", "logout", "logged_out", "offline":
		return config.AgentLoggedOut, true
	}
	return "", false
}

func (cc *callCenter) run(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-cc.wake:
		case <-t.C:
		}
		cc.dispatch()
	}
}

func today() int { y, m, d := time.Now().Date(); return y*10000 + int(m)*100 + d }

// dispatch offers waiting callers to available agents.
func (cc *callCenter) dispatch() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	now := time.Now()
	ids := make([]string, 0, len(cc.queues))
	for id := range cc.queues {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		q := cc.queues[id]
		if q.cfg == nil {
			continue
		}
		if cc.staffed(q) == 0 {
			if q.noAgentSince.IsZero() {
				q.noAgentSince = now
			}
		} else {
			q.noAgentSince = time.Time{}
		}
		for _, m := range q.members {
			if m.offering || m.agent != nil {
				continue
			}
			cands := cc.candidates(q, m, now)
			if len(cands) == 0 {
				continue
			}
			offer := cc.pick(q, m, cands)
			if len(offer.agents) == 0 {
				continue
			}
			select {
			case m.offers <- offer:
			default:
				continue
			}
			m.offering = true
			m.state = "Trying"
			for _, a := range offer.agents {
				a.offered = true
				a.lastOffered = now
				cc.setStateLocked(a, ccStateReceiving, q)
			}
		}
	}
}

// loggedIn counts the queue's agents that are not logged out.
// staffed counts a queue's agents who take calls: Available, with a phone
// that can ring. An agent on a call or wrapping up counts (callers wait
// for them); on a break (also after too many missed calls), logged out or
// with no phone online does not. With none, callers leave the queue after
// its "max wait time with no agent".
func (cc *callCenter) staffed(q *ccQueue) int {
	n := 0
	for _, t := range cc.tiers[strings.ToLower(q.cfg.UUID)] {
		if a := cc.agents[strings.ToLower(t.AgentUUID)]; a != nil && a.available() && cc.reachable(a) {
			n++
		}
	}
	return n
}

// reachable reports whether an agent's phone can ring: an agent on an
// extension of this switch needs a registered phone (the browser dialer
// counts) or call forwarding; other contacts (numbers, gateways) are tried.
func (cc *callCenter) reachable(a *ccAgent) bool {
	if a == nil || a.cfg == nil || a.cfg.Contact == "" {
		return false
	}
	if a.cfg.Extension == "" {
		return true
	}
	u := cc.c.config().Directory.LookupIn(a.cfg.Domain, a.cfg.Extension)
	if u == nil || u.Disabled {
		return false
	}
	if truthy(u.Vars["forward_all_enabled"]) && u.Vars["forward_all_destination"] != "" {
		return true
	}
	return len(cc.c.reg.Lookup(u.ID, u.Domain)) > 0
}

type ccCand struct {
	a        *ccAgent
	level    int
	position int
}

// candidates returns the agents that may be offered member m now, in tier
// order, applying the tier rules.
func (cc *callCenter) candidates(q *ccQueue, m *ccMember, now time.Time) []ccCand {
	tiers := cc.tiers[strings.ToLower(q.cfg.UUID)]
	if len(tiers) == 0 {
		return nil
	}
	free := func(a *ccAgent) bool {
		if a == nil || a.cfg == nil || a.cfg.Contact == "" || !a.available() || a.offered || a.member != nil {
			return false
		}
		if a.state != ccStateWaiting || now.Before(a.readyAt) {
			return false
		}
		if !cc.reachable(a) {
			return false // no phone online: ringing it would only count as a missed call
		}
		if a.cfg.Extension != "" {
			if u := cc.c.config().Directory.LookupIn(a.cfg.Domain, a.cfg.Extension); u != nil && cc.c.userBusy(u) {
				return false // on a direct call: the queue call must not land
			}
		}
		return true
	}
	// Tier rules: higher levels open up as the caller waits.
	maxLevel := int(^uint(0) >> 1)
	if q.cfg.TierRulesApply {
		wait := now.Sub(m.joined)
		levels := []int{}
		for _, t := range tiers {
			if len(levels) == 0 || levels[len(levels)-1] != t.Level {
				levels = append(levels, t.Level)
			}
		}
		maxLevel = levels[0]
		var need time.Duration
		for i, lvl := range levels {
			if i > 0 {
				if wait < need {
					break
				}
				maxLevel = lvl
			}
			anyFree := false
			for _, t := range tiers {
				if t.Level == lvl && free(cc.agents[strings.ToLower(t.AgentUUID)]) {
					anyFree = true
					break
				}
			}
			if q.cfg.TierRuleNoAgentNoWait && !anyFree {
				continue // an empty level does not make callers wait
			}
			step := q.cfg.TierRuleWaitSecond
			if q.cfg.TierRuleWaitMultiplyLevel {
				step *= time.Duration(max(lvl, 1))
			}
			need += step
		}
	}
	var out []ccCand
	for _, t := range tiers {
		if t.Level > maxLevel {
			continue
		}
		a := cc.agents[strings.ToLower(t.AgentUUID)]
		if free(a) {
			out = append(out, ccCand{a, t.Level, t.Position})
		}
	}
	return out
}

func idleSince(a *ccAgent) time.Time {
	t := a.statusSince
	if a.bridgeEnd.After(t) {
		t = a.bridgeEnd
	}
	return t
}

// pick applies the queue strategy.
func (cc *callCenter) pick(q *ccQueue, m *ccMember, cands []ccCand) ccOffer {
	strategy := q.cfg.Strategy
	switch strategy {
	case "ring-all":
		var all []*ccAgent
		for _, c := range cands {
			all = append(all, c.a)
		}
		return ccOffer{agents: all}
	case "ring-progressively":
		var all []*ccAgent
		for _, c := range cands {
			all = append(all, c.a)
		}
		return ccOffer{agents: all, stagger: max(q.cfg.RingProgressivelyDelay, time.Second)}
	}
	// One agent at a time: skip the agents already tried for this caller
	// in this round; start a new round when everybody was tried.
	untried := cands[:0:0]
	for _, c := range cands {
		if !m.tried[c.a.cfg.UUID] {
			untried = append(untried, c)
		}
	}
	if len(untried) == 0 {
		m.tried = map[string]bool{}
		untried = cands
	}
	var chosen *ccAgent
	switch strategy {
	case "longest-idle-agent", "":
		sort.SliceStable(untried, func(i, j int) bool { return idleSince(untried[i].a).Before(idleSince(untried[j].a)) })
		chosen = untried[0].a
	case "agent-with-least-talk-time":
		sort.SliceStable(untried, func(i, j int) bool { return untried[i].a.talk < untried[j].a.talk })
		chosen = untried[0].a
	case "agent-with-fewest-calls":
		sort.SliceStable(untried, func(i, j int) bool { return untried[i].a.answered < untried[j].a.answered })
		chosen = untried[0].a
	case "random":
		chosen = untried[rand.Intn(len(untried))].a
	case "round-robin":
		// Continue after the agent offered last, in tier order.
		tiers := cc.tiers[strings.ToLower(q.cfg.UUID)]
		order := map[string]int{}
		for i, t := range tiers {
			order[strings.ToLower(t.AgentUUID)] = i
		}
		best, bestKey := untried[0].a, int(^uint(0)>>1)
		for _, c := range untried {
			idx := order[strings.ToLower(c.a.cfg.UUID)]
			key := (idx - q.rr - 1 + len(tiers)) % max(len(tiers), 1)
			if key < bestKey {
				best, bestKey = c.a, key
			}
		}
		chosen = best
		q.rr = order[strings.ToLower(chosen.cfg.UUID)]
	default: // top-down, sequentially-by-agent-order: tier order
		chosen = untried[0].a
	}
	return ccOffer{agents: []*ccAgent{chosen}}
}

// ---------------------------------------------------------------------------
// Agent status and state

func (cc *callCenter) setStateLocked(a *ccAgent, state string, q *ccQueue) {
	if a.state == state {
		return
	}
	a.state = state
	a.stateSince = time.Now()
	e := cc.agentEvent("agent-state-change", a)
	e.Add("CC-Agent-State", state)
	if q != nil {
		e.Add("CC-Queue", q.cfg.QueueID())
	}
	cc.c.events.Publish(e)
}

func (cc *callCenter) agentEvent(action string, a *ccAgent) *Event {
	e := NewEvent(EvCustom, "callcenter::info")
	e.Add("CC-Action", action)
	e.Add("CC-Agent", a.cfg.UUID)
	e.Add("CC-Agent-Name", a.cfg.Name)
	e.Add("CC-Agent-Extension", a.cfg.Extension)
	e.Add("CC-Agent-Domain", a.cfg.Domain)
	e.Add("CC-Agent-Status", a.status)
	e.Add("CC-Agent-State", a.state)
	e.Add("CC-Agent-Aux", a.aux)
	return e
}

// AgentStatusChange describes a status change for SetAgentStatus.
type AgentStatusChange struct {
	Agent  string // uuid, name, agent id or extension
	Domain string // optional
	Status string
	Aux    string // AUX code (reason) for On Break
	Source string // ui, esl, phone, system
}

// SetAgentStatus changes an agent's status (Available, On Break with an
// AUX code, Logged Out...).
func (c *Core) SetAgentStatus(ch AgentStatusChange) error {
	cfg := c.config().Features.Agent(ch.Agent, ch.Domain)
	if cfg == nil {
		return fmt.Errorf("no such agent %s", ch.Agent)
	}
	status, ok := normalizeStatus(ch.Status)
	if !ok {
		return fmt.Errorf("invalid status %q (use Available, On Break, Logged Out or Available (On Demand))", ch.Status)
	}
	aux := strings.TrimSpace(ch.Aux)
	if status != config.AgentOnBreak {
		aux = ""
	} else if aux == "" {
		aux = "Break"
	}
	return c.cc.setStatus(strings.ToLower(cfg.UUID), status, aux, firstNonEmpty(ch.Source, "api"))
}

func (cc *callCenter) setStatus(id, status, aux, source string) error {
	cc.mu.Lock()
	a := cc.agents[id]
	if a == nil {
		cc.mu.Unlock()
		return fmt.Errorf("agent %s is not loaded yet", id)
	}
	if a.status == status && a.aux == aux {
		cc.mu.Unlock()
		return nil
	}
	now := time.Now()
	prevStatus, prevAux, prevSince := a.status, a.aux, a.statusSince
	a.status, a.aux, a.statusSince = status, aux, now
	if status == config.AgentAvailable || status == config.AgentAvailableOnDemand {
		a.noAnswer = 0
		if a.state == ccStateIdle {
			cc.setStateLocked(a, ccStateWaiting, nil)
		}
	} else if a.member == nil && !a.offered {
		cc.setStateLocked(a, ccStateIdle, nil)
	}
	e := cc.agentEvent("agent-status-change", a)
	e.Add("CC-Agent-Previous-Status", prevStatus)
	e.Add("CC-Agent-Previous-Aux", prevAux)
	e.Add("CC-Agent-Previous-Seconds", strconv.Itoa(int(now.Sub(prevSince).Seconds())))
	e.Add("CC-Source", source)
	cfg := a.cfg
	cc.mu.Unlock()
	cc.c.log.Info("agent status changed", "agent", cfg.Name, "extension", cfg.Extension, "domain", cfg.Domain,
		"status", status, "aux", aux, "previous", prevStatus, "previous_aux", prevAux,
		"after", now.Sub(prevSince).Round(time.Second), "source", source)
	cc.c.events.Publish(e)
	cc.c.recordEvent("agent", fmt.Sprintf("%s (%s) %s%s, was %s%s", cfg.Name, cfg.Extension, status, auxSuffix(aux), prevStatus, auxSuffix(prevAux)))
	cc.poke()
	return nil
}

func auxSuffix(aux string) string {
	if aux == "" {
		return ""
	}
	return " [" + aux + "]"
}

// blocksDirect reports whether direct calls to an extension must be
// refused because its agent is on a break whose AUX code says so.
func (cc *callCenter) blocksDirect(ext, domain string) bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for _, a := range cc.agents {
		if a.cfg == nil || a.cfg.Extension != ext || !strings.EqualFold(a.cfg.Domain, domain) || a.status != config.AgentOnBreak {
			continue
		}
		for _, x := range cc.aux {
			if strings.EqualFold(x.Name, a.aux) && x.BlockDirect && (x.Domain == "" || strings.EqualFold(x.Domain, domain)) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Snapshots for the API, the wallboard and the web UI

// AgentInfo is the live view of an agent.
type AgentInfo struct {
	UUID          string   `json:"uuid"`
	Name          string   `json:"name"`
	Extension     string   `json:"extension"`
	Domain        string   `json:"domain"`
	Contact       string   `json:"contact"`
	Status        string   `json:"status"`
	Aux           string   `json:"aux"`
	State         string   `json:"state"`
	StatusSince   int64    `json:"status_since"`
	StateSince    int64    `json:"state_since"`
	CallsAnswered int      `json:"calls_answered"`
	TalkSeconds   int      `json:"talk_seconds"`
	NoAnswerCount int      `json:"no_answer_count"`
	MissedToday   int      `json:"missed_today"`
	LastBridgeEnd int64    `json:"last_bridge_end"`
	ReadyIn       int      `json:"ready_in"` // seconds of wrap-up / delay left
	Caller        string   `json:"caller"`   // current queue caller
	Queues        []string `json:"queues"`
	OnCall        bool     `json:"on_call"` // any call, queue or direct
}

// QueueInfo is the live view of a queue.
type QueueInfo struct {
	UUID          string   `json:"uuid"`
	Name          string   `json:"name"`
	Extension     string   `json:"extension"`
	Domain        string   `json:"domain"`
	Strategy      string   `json:"strategy"`
	Waiting       int      `json:"waiting"`
	LongestWait   int      `json:"longest_wait"`
	Agents        int      `json:"agents"`
	LoggedIn      int      `json:"logged_in"`
	Available     int      `json:"available"`
	OnBreak       int      `json:"on_break"`
	OnCall        int      `json:"on_call"`
	AnsweredToday int      `json:"answered_today"`
	AbandonToday  int      `json:"abandoned_today"`
	TimeoutToday  int      `json:"timeout_today"`
	AvgWait       int      `json:"avg_wait"`
	AvgTalk       int      `json:"avg_talk"`
	Callers       []Caller `json:"callers"`
}

// Caller is a member waiting in (or answered from) a queue.
type Caller struct {
	UUID      string `json:"uuid"`
	CIDName   string `json:"cid_name"`
	CIDNumber string `json:"cid_number"`
	Joined    int64  `json:"joined"`
	State     string `json:"state"`
	Agent     string `json:"agent"`
}

func (cc *callCenter) resetDay(q *ccQueue, d int) {
	if q.day != d {
		q.day, q.answered, q.abandoned, q.timedOut, q.waitTotal, q.talkTotal = d, 0, 0, 0, 0, 0
	}
}

// Agents returns the live agents, optionally of one domain.
func (c *Core) Agents(domain string) []AgentInfo {
	cc := c.cc
	cc.mu.Lock()
	defer cc.mu.Unlock()
	now := time.Now()
	queuesOf := map[string][]string{}
	for qid, ts := range cc.tiers {
		q := cc.queues[qid]
		if q == nil || q.cfg == nil {
			continue
		}
		for _, t := range ts {
			k := strings.ToLower(t.AgentUUID)
			queuesOf[k] = append(queuesOf[k], q.cfg.Name)
		}
	}
	var out []AgentInfo
	for id, a := range cc.agents {
		if a.cfg == nil || (domain != "" && !strings.EqualFold(a.cfg.Domain, domain)) {
			continue
		}
		if a.day != today() {
			a.day, a.missed = today(), 0
		}
		info := AgentInfo{UUID: a.cfg.UUID, Name: a.cfg.Name, Extension: a.cfg.Extension, Domain: a.cfg.Domain,
			Contact: a.cfg.Contact, Status: a.status, Aux: a.aux, State: a.state, StatusSince: a.statusSince.Unix(),
			StateSince: a.stateSince.Unix(), CallsAnswered: a.answered, TalkSeconds: int(a.talk.Seconds()),
			NoAnswerCount: a.noAnswer, MissedToday: a.missed, Queues: queuesOf[id]}
		if !a.bridgeEnd.IsZero() {
			info.LastBridgeEnd = a.bridgeEnd.Unix()
		}
		if a.readyAt.After(now) {
			info.ReadyIn = int(a.readyAt.Sub(now).Seconds() + 0.5)
		}
		if a.member != nil {
			info.Caller = strings.TrimSpace(a.member.cidName + " " + a.member.cidNumber)
		}
		if a.cfg.Extension != "" {
			if u := c.config().Directory.LookupIn(a.cfg.Domain, a.cfg.Extension); u != nil {
				info.OnCall = a.member != nil || c.userBusy(u)
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Queues returns the live queues, optionally of one domain.
func (c *Core) Queues(domain string) []QueueInfo {
	cc := c.cc
	cc.mu.Lock()
	defer cc.mu.Unlock()
	now := time.Now()
	d := today()
	var out []QueueInfo
	for id, q := range cc.queues {
		if q.cfg == nil || (domain != "" && !strings.EqualFold(q.cfg.Domain, domain)) {
			continue
		}
		cc.resetDay(q, d)
		info := QueueInfo{UUID: q.cfg.UUID, Name: q.cfg.Name, Extension: q.cfg.Extension, Domain: q.cfg.Domain,
			Strategy: q.cfg.Strategy, AnsweredToday: q.answered, AbandonToday: q.abandoned, TimeoutToday: q.timedOut}
		if q.answered > 0 {
			info.AvgWait = int(q.waitTotal.Seconds()) / q.answered
			info.AvgTalk = int(q.talkTotal.Seconds()) / q.answered
		}
		for _, m := range q.members {
			c := Caller{UUID: m.uuid, CIDName: m.cidName, CIDNumber: m.cidNumber, Joined: m.joined.Unix(), State: m.state}
			if m.agent != nil {
				c.Agent = m.agent.cfg.Name
			} else {
				info.Waiting++
				if w := int(now.Sub(m.joined).Seconds()); w > info.LongestWait {
					info.LongestWait = w
				}
			}
			info.Callers = append(info.Callers, c)
		}
		for _, t := range cc.tiers[id] {
			a := cc.agents[strings.ToLower(t.AgentUUID)]
			if a == nil {
				continue
			}
			info.Agents++
			online := cc.reachable(a) // logged in counts agents whose phone can ring
			switch {
			case a.status == config.AgentLoggedOut:
			case a.status == config.AgentOnBreak:
				if online {
					info.LoggedIn++
				}
				info.OnBreak++
			default:
				if online {
					info.LoggedIn++
				}
				if a.member != nil || a.offered {
					info.OnCall++
				} else if online {
					info.Available++
				}
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].Extension < out[j].Extension
	})
	return out
}

// ---------------------------------------------------------------------------
// The callcenter application (the caller's side)

// callcenter <queue uuid | extension@domain | name@domain>
func appCallcenter(ctx context.Context, c *Core, ch *Channel, data string) error {
	id := strings.TrimSpace(data)
	if id == "" {
		id = ch.Var("call_center_queue_uuid")
	}
	qcfg := c.config().Features.Queue(id, ch.Var("domain_name"))
	if qcfg == nil {
		return fmt.Errorf("callcenter: queue %q not found", id)
	}
	cc := c.cc
	cc.mu.Lock()
	q := cc.queues[strings.ToLower(qcfg.UUID)]
	if q == nil {
		cc.mu.Unlock()
		return fmt.Errorf("callcenter: queue %q not loaded", id)
	}
	if q.cfg.MaxCallers > 0 && len(q.members) >= q.cfg.MaxCallers {
		cc.mu.Unlock()
		ch.SetVar("cc_cause", "Cancel")
		ch.SetVar("cc_cancel_reason", "MAX_CALLERS")
		ch.log.Info("queue full", "queue", qcfg.Name, "max", qcfg.MaxCallers)
		return nil
	}
	cidName := firstNonEmpty(ch.Var("effective_caller_id_name"), ch.Var("caller_id_name"))
	cidNum := firstNonEmpty(ch.Var("effective_caller_id_number"), ch.Var("caller_id_number"))
	m := &ccMember{uuid: ch.UUID, ch: ch, q: q, joined: time.Now(), tried: map[string]bool{},
		offers: make(chan ccOffer, 1), state: "Waiting", cidName: cidName, cidNumber: cidNum}
	q.members = append(q.members, m)
	cc.resetDay(q, today())
	cc.mu.Unlock()

	now := strconv.FormatInt(m.joined.Unix(), 10)
	for k, v := range map[string]string{"cc_queue": qcfg.QueueID(), "cc_queue_name": qcfg.Name, "cc_side": "member",
		"call_center_queue_uuid": qcfg.UUID, "cc_queue_joined_epoch": now, "cc_member_uuid": m.uuid} {
		ch.SetVar(k, v)
	}
	ch.log.Info("caller joined queue", "queue", qcfg.Name, "caller", cidNum)
	ch.flow("queue", "Waiting in queue "+qcfg.Name)
	c.memberEvent("member-queue-start", m, "", nil)
	c.recordEvent("queue", fmt.Sprintf("%s %s joined queue %s", cidName, cidNum, qcfg.Name))
	cc.poke()

	result, agent := c.runMember(ctx, m)
	c.finishMember(m, result, agent)
	// Callers who leave unanswered hear why; the queue's dialplan then
	// takes them on (its "timeout action": voicemail...).
	if (result == "NO_AGENT_TIMEOUT" || result == "TIMEOUT") && !ch.IsHungUp() && ctx.Err() == nil {
		name, set := "queue-no-agent", ch.Var("cc_no_agent_sound")
		if result == "TIMEOUT" {
			name, set = "queue-timeout", ch.Var("cc_timeout_sound")
		}
		path := c.prompt(ch, name)
		if set != "" {
			path = c.resolveSound(ch, set) // "silence": nothing
		}
		if path != "" {
			_, _ = c.play(ctx, ch, path, false)
		}
	}
	return nil
}

// runMember waits in the queue until an agent takes the call (and the
// call ends), the caller hangs up, presses an exit key, or a timeout hits.
func (c *Core) runMember(ctx context.Context, m *ccMember) (string, *ccAgent) {
	ch, q := m.ch, m.q.cfg
	if err := startMedia(ch, false); err != nil {
		return "ERROR", nil
	}
	if q.Greeting != "" {
		ch.FlushDTMF()
		if d, _ := c.playSound(ctx, ch, q.Greeting, true); d != 0 && strings.ContainsRune(c.exitKeys(ch, q), d) {
			return "EXIT_WITH_KEY", nil
		}
	}
	mohCtx, stopMOH := context.WithCancel(ctx)
	exitKey := make(chan rune, 1)
	mohDone := c.startMOH(mohCtx, ch, firstNonEmpty(q.MOHSound, ch.Var("hold_music")), q.AnnounceSound, q.AnnounceFrequency, c.exitKeys(ch, q), exitKey)
	defer func() { stopMOH(); <-mohDone }()

	var maxWait <-chan time.Time
	if q.MaxWaitTime > 0 {
		t := time.NewTimer(q.MaxWaitTime)
		defer t.Stop()
		maxWait = t.C
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if ch.IsHungUp() {
				return "ABANDONED", nil
			}
			return "BREAK_OUT", nil // transferred away
		case <-exitKey:
			return "EXIT_WITH_KEY", nil
		case <-maxWait:
			return "TIMEOUT", nil
		case <-tick.C:
			if q.MaxWaitTimeNoAgent > 0 {
				c.cc.mu.Lock()
				since := m.q.noAgentSince
				c.cc.mu.Unlock()
				if !since.IsZero() && time.Since(since) >= q.MaxWaitTimeNoAgent && time.Since(m.joined) >= q.MaxWaitTimeNoAgentReached {
					return "NO_AGENT_TIMEOUT", nil
				}
			}
		case offer := <-m.offers:
			b, a := c.offerAgents(ctx, m, offer)
			if b == nil {
				continue
			}
			// An agent answered: stop the music and connect.
			stopMOH()
			<-mohDone
			c.bridgeMember(ctx, m, a, b)
			return "ANSWERED", a
		}
	}
}

func (c *Core) exitKeys(ch *Channel, q *config.Queue) string {
	return firstNonEmpty(ch.Var("cc_exit_keys"), q.ExitKeys)
}

// offerAgents rings the offered agents. It returns the answered agent leg.
func (c *Core) offerAgents(ctx context.Context, m *ccMember, offer ccOffer) (*Channel, *ccAgent) {
	q := m.q.cfg
	ch := m.ch
	cidName := m.cidName
	if q.CIDPrefix != "" {
		cidName = q.CIDPrefix + cidName
	}
	var legs []string
	byID := map[string]*ccAgent{}
	for i, a := range offer.agents {
		vars, target := splitContact(a.cfg.Contact)
		vars = append(vars, "cc_agent_uuid="+a.cfg.UUID, "cc_agent="+a.cfg.UUID, "cc_agent_name='"+strings.ReplaceAll(a.cfg.Name, "'", "")+"'",
			"leg_timeout="+strconv.Itoa(agentTimeout(a.cfg)), "cc_side=agent")
		if offer.stagger > 0 && i > 0 {
			vars = append(vars, "leg_delay_start="+strconv.Itoa(int(offer.stagger.Seconds())*i))
		}
		legs = append(legs, "["+strings.Join(vars, ",")+"]"+target)
		byID[strings.ToLower(a.cfg.UUID)] = a
	}
	m.offered += len(offer.agents)
	var ringing []string
	for _, a := range offer.agents {
		ringing = append(ringing, joinName(a.cfg.Name, a.cfg.Extension))
	}
	ch.flow("ring", "Ringing agent "+strings.Join(ringing, ", "))
	dial := "{ignore_early_media=true,origination_caller_id_name='" + strings.ReplaceAll(cidName, "'", "") +
		"',origination_caller_id_number=" + m.cidNumber + ",cc_queue=" + q.QueueID() + ",cc_member_uuid=" + m.uuid +
		",cc_member_session_uuid=" + ch.UUID + "}" + strings.Join(legs, ",")
	causes := map[string]Cause{}
	var hmu sync.Mutex
	octx := withLegHook(ctx, func(vars map[string]string, cause Cause) {
		hmu.Lock()
		causes[strings.ToLower(vars["cc_agent_uuid"])] = cause
		hmu.Unlock()
	})
	b, cause := c.originate(octx, ch, dial, nil)
	var winner *ccAgent
	if b != nil {
		winner = byID[strings.ToLower(b.Var("cc_agent_uuid"))]
	}
	hmu.Lock()
	failed := map[string]Cause{}
	for k, v := range causes {
		failed[k] = v
	}
	hmu.Unlock()
	c.cc.offerDone(m, offer, winner, failed, cause)
	if b != nil && winner == nil {
		b.Hangup(CauseNormalClearing) // cannot happen: every leg carries its agent
		return nil, nil
	}
	return b, winner
}

// splitContact turns "{a=b}user/1001@d" into leg variables and a target.
func splitContact(contact string) ([]string, string) {
	var vars []string
	c := strings.TrimSpace(contact)
	for strings.HasPrefix(c, "{") || strings.HasPrefix(c, "[") {
		body, rest, ok := takeBlock(c)
		if !ok {
			break
		}
		for _, kv := range splitTop(body, ',') {
			if kv = strings.TrimSpace(kv); kv != "" {
				vars = append(vars, kv)
			}
		}
		c = strings.TrimSpace(rest)
	}
	return vars, c
}

// offerDone records the outcome of an offer for every agent involved.
func (cc *callCenter) offerDone(m *ccMember, offer ccOffer, winner *ccAgent, causes map[string]Cause, overall Cause) {
	cc.mu.Lock()
	now := time.Now()
	q := m.q
	for _, a := range offer.agents {
		a.offered = false
		if a == winner {
			a.member = m
			a.noAnswer = 0
			a.bridgeStart = now
			m.agent = a
			m.answered = now
			m.state = "Answered"
			cc.setStateLocked(a, ccStateInCall, q)
			continue
		}
		cause, ok := causes[strings.ToLower(a.cfg.UUID)]
		if !ok {
			cause = overall
		}
		switch cause {
		case CauseLoseRace, CauseOriginatorCancel, CauseNone:
			// another agent won, or the caller left: no penalty
		case CauseUserBusy:
			a.readyAt = now.Add(a.cfg.BusyDelay)
		case CauseCallRejected:
			a.readyAt = now.Add(a.cfg.RejectDelay)
			a.missed++
		default: // no answer, phone offline...
			a.readyAt = now.Add(a.cfg.NoAnswerDelay)
			a.noAnswer++
			a.missed++
			m.noAnswer++
		}
		if len(offer.agents) == 1 {
			m.tried[a.cfg.UUID] = true
		}
		if a.member == nil {
			next := ccStateWaiting
			if !a.available() {
				next = ccStateIdle
			}
			cc.setStateLocked(a, next, q)
		}
		if cause != CauseLoseRace && cause != CauseOriginatorCancel && cause != CauseNone {
			e := cc.agentEvent("bridge-agent-fail", a)
			e.Add("CC-Queue", q.cfg.QueueID())
			e.Add("CC-Member-UUID", m.uuid)
			e.Add("CC-Hangup-Cause", cause.String())
			cc.c.events.Publish(e)
		}
		if a.cfg.MaxNoAnswer > 0 && a.noAnswer >= a.cfg.MaxNoAnswer && a.status != config.AgentOnBreak {
			// Too many unanswered calls: take the agent out, as
			// is usual, so callers stop waiting on it.
			id := strings.ToLower(a.cfg.UUID)
			go func() {
				_ = cc.setStatus(id, config.AgentOnBreak, "Max no answer", "system")
			}()
		}
	}
	if winner == nil {
		m.offering = false
		if m.state == "Trying" {
			m.state = "Waiting"
		}
	}
	cc.mu.Unlock()
	cc.poke()
}

// bridgeMember connects the caller with the agent until one hangs up.
func (c *Core) bridgeMember(ctx context.Context, m *ccMember, a *ccAgent, b *Channel) {
	ch, q := m.ch, m.q.cfg
	now := strconv.FormatInt(time.Now().Unix(), 10)
	ch.SetVar("cc_agent", a.cfg.UUID)
	ch.SetVar("cc_agent_uuid", a.cfg.UUID)
	ch.SetVar("cc_agent_name", a.cfg.Name)
	ch.SetVar("cc_agent_type", firstNonEmpty(a.cfg.Type, "callback"))
	ch.SetVar("cc_queue_answered_epoch", now)
	b.SetVar("cc_queue", q.QueueID())
	ch.log.Info("queue call answered", "queue", q.Name, "agent", a.cfg.Name, "extension", a.cfg.Extension,
		"waited", time.Since(m.joined).Round(time.Second))
	c.memberEvent("bridge-agent-start", m, "", a)
	c.recordEvent("queue", fmt.Sprintf("%s answered %s %s in queue %s after %s", a.cfg.Name, m.cidName, m.cidNumber, q.Name, time.Since(m.joined).Round(time.Second)))
	if q.RecordCalls || a.cfg.Record {
		path := ch.Expand(firstNonEmpty(q.RecordTemplate,
			"${recordings_dir}/${domain_name}/archive/${strftime(%Y)}/${strftime(%b)}/${strftime(%d)}/${uuid}.wav"))
		if err := appRecordSession(ctx, c, ch, path); err != nil {
			ch.log.Warn("queue recording failed", "err", err)
		}
	}
	c.bridgeChannels(ctx, ch, b)
	c.cc.bridgeEnd(m, a)
	c.memberEvent("bridge-agent-end", m, "", a)
	if ch.Var("hangup_after_bridge") != "false" && ctx.Err() == nil && !ch.IsHungUp() {
		ch.Hangup(CauseNormalClearing)
	}
}

func (cc *callCenter) bridgeEnd(m *ccMember, a *ccAgent) {
	cc.mu.Lock()
	now := time.Now()
	talk := now.Sub(a.bridgeStart)
	a.talk += talk
	a.answered++
	a.bridgeEnd = now
	a.member = nil
	a.readyAt = now.Add(a.cfg.WrapUpTime)
	next := ccStateWaiting
	if !a.available() {
		next = ccStateIdle
	}
	cc.setStateLocked(a, next, m.q)
	cc.mu.Unlock()
	cc.poke()
}

// finishMember removes the caller from the queue and publishes the queue
// call record.
func (c *Core) finishMember(m *ccMember, result string, a *ccAgent) {
	cc := c.cc
	cc.mu.Lock()
	q := m.q
	// An offer may still be queued for this caller: give its agents back.
	select {
	case offer := <-m.offers:
		for _, ag := range offer.agents {
			ag.offered = false
			if ag.member == nil {
				next := ccStateWaiting
				if !ag.available() {
					next = ccStateIdle
				}
				cc.setStateLocked(ag, next, q)
			}
		}
	default:
	}
	for i, x := range q.members {
		if x == m {
			q.members = append(q.members[:i:i], q.members[i+1:]...)
			break
		}
	}
	end := time.Now()
	wait := end.Sub(m.joined)
	talk := time.Duration(0)
	if !m.answered.IsZero() {
		wait = m.answered.Sub(m.joined)
		talk = end.Sub(m.answered)
	}
	cc.resetDay(q, today())
	switch result {
	case "ANSWERED":
		q.answered++
		q.waitTotal += wait
		q.talkTotal += talk
	case "ABANDONED":
		q.abandoned++
	case "TIMEOUT", "NO_AGENT_TIMEOUT":
		q.timedOut++
	}
	cc.mu.Unlock()

	ch := m.ch
	epoch := strconv.FormatInt(end.Unix(), 10)
	switch result {
	case "ANSWERED":
		ch.SetVar("cc_cause", "Terminated")
		ch.SetVar("cc_queue_terminated_epoch", epoch)
	default:
		ch.SetVar("cc_cause", "Cancel")
		ch.SetVar("cc_queue_canceled_epoch", epoch)
		reason := result
		if result == "ABANDONED" {
			reason = "NONE"
		}
		ch.SetVar("cc_cancel_reason", reason)
	}
	ch.SetVar("cc_result", strings.ToLower(result))
	if result != "ANSWERED" {
		ch.flow("queue", "Left queue "+q.cfg.Name+map[bool]string{true: ": " + queueResultText(result), false: ""}[queueResultText(result) != ""])
	}
	ch.log.Info("caller left queue", "queue", q.cfg.Name, "result", result, "waited", wait.Round(time.Second), "talk", talk.Round(time.Second))
	extra := [][2]string{
		{"RQ-Result", strings.ToLower(result)},
		{"RQ-Wait-Seconds", strconv.Itoa(int(wait.Seconds()))},
		{"RQ-Talk-Seconds", strconv.Itoa(int(talk.Seconds()))},
		{"RQ-Agents-Offered", strconv.Itoa(m.offered)},
		{"RQ-No-Answer", strconv.Itoa(m.noAnswer)},
		{"CC-Member-Leaving-Time", epoch},
	}
	if !m.answered.IsZero() {
		extra = append(extra, [2]string{"CC-Agent-Answered-Time", strconv.FormatInt(m.answered.Unix(), 10)})
	}
	c.memberEvent("member-queue-end", m, result, a, extra...)
	if result != "ANSWERED" {
		c.recordEvent("queue", fmt.Sprintf("%s %s left queue %s: %s after %s", m.cidName, m.cidNumber, q.cfg.Name, strings.ToLower(result), wait.Round(time.Second)))
	}
	cc.poke()
}

func (c *Core) memberEvent(action string, m *ccMember, result string, a *ccAgent, extra ...[2]string) {
	e := NewEvent(EvCustom, "callcenter::info")
	q := m.q.cfg
	e.Add("CC-Action", action)
	e.Add("CC-Queue", q.QueueID())
	e.Add("CC-Queue-Name", q.Name)
	e.Add("CC-Queue-UUID", q.UUID)
	e.Add("CC-Queue-Domain", q.Domain)
	e.Add("CC-Member-UUID", m.uuid)
	e.Add("CC-Member-Session-UUID", m.ch.UUID)
	e.Add("CC-Member-CID-Name", m.cidName)
	e.Add("CC-Member-CID-Number", m.cidNumber)
	e.Add("CC-Member-Joined-Time", strconv.FormatInt(m.joined.Unix(), 10))
	if a != nil {
		e.Add("CC-Agent", a.cfg.UUID)
		e.Add("CC-Agent-Name", a.cfg.Name)
		e.Add("CC-Agent-Extension", a.cfg.Extension)
	}
	if result != "" {
		cause := "Cancel"
		if result == "ANSWERED" {
			cause = "Terminated"
		}
		e.Add("CC-Cause", cause)
		e.Add("CC-Cancel-Reason", result)
	}
	for _, h := range extra {
		e.Add(h[0], h[1])
	}
	c.events.Publish(e)
}

// ---------------------------------------------------------------------------
// Music on hold

// defaultMusic plays when no music file is configured or found.
const defaultMusic = "tone_stream://%(400,50,523);%(400,50,659);%(400,50,784);%(800,1600,659);v=-20;loops=-1"

// startMOH plays music on hold (and an optional announcement every
// interval) to ch until ctx ends. A digit in exitKeys is reported on exit.
func (c *Core) startMOH(ctx context.Context, ch *Channel, spec, announce string, every time.Duration, exitKeys string, exit chan<- rune) <-chan struct{} {
	done := make(chan struct{})
	files := c.mohFiles(ch, spec)
	go func() {
		defer close(done)
		lastAnnounce := time.Now()
		i := 0
		for ctx.Err() == nil && !ch.IsHungUp() {
			if announce != "" && every > 0 && time.Since(lastAnnounce) >= every {
				lastAnnounce = time.Now()
				if d, _ := c.playSound(ctx, ch, announce, exitKeys != ""); d != 0 && strings.ContainsRune(exitKeys, d) {
					sendRune(exit, d)
					return
				}
			}
			src := defaultMusic
			if len(files) > 0 {
				src = files[i%len(files)]
				i++
			}
			// Play in slices so announcements and hangups are noticed.
			pctx, cancel := context.WithTimeout(ctx, mohSlice(every))
			d, err := c.play(pctx, ch, src, exitKeys != "")
			cancel()
			if d != 0 && strings.ContainsRune(exitKeys, d) {
				sendRune(exit, d)
				return
			}
			if err != nil && ctx.Err() == nil && pctx.Err() == nil {
				// unplayable file: fall back to the built-in music
				files = nil
				if wait(ctx, 200*time.Millisecond) != nil {
					return
				}
			}
		}
	}()
	return done
}

func mohSlice(every time.Duration) time.Duration {
	if every > 0 && every < time.Hour {
		return every
	}
	return time.Hour
}

func sendRune(ch chan<- rune, r rune) {
	select {
	case ch <- r:
	default:
	}
}

// agentTimeout is how long an agent's phone rings, in seconds.
func agentTimeout(a *config.Agent) int {
	if a.CallTimeout <= 0 {
		return 20
	}
	return max(int(a.CallTimeout.Seconds()), 1)
}
