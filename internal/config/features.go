package config

import (
	"strings"
	"time"
)

// RingGroup is a RingQ ring group (v_ring_groups and
// v_ring_group_destinations).
type RingGroup struct {
	UUID               string
	Domain             string // domain name
	Name               string
	Extension          string
	Context            string
	Strategy           string // simultaneous, sequence, enterprise, rollover, random
	CallTimeout        int    // seconds, 0 = sum of destination timeouts
	Greeting           string
	ForwardEnabled     bool
	ForwardDestination string
	CallerIDName       string
	CallerIDNumber     string
	CIDNamePrefix      string
	CIDNumberPrefix    string
	TimeoutApp         string
	TimeoutData        string
	Ringback           string
	Enabled            bool
	Destinations       []RingGroupDestination
}

// RingGroupDestination is one member of a ring group.
type RingGroupDestination struct {
	Number  string
	Delay   int // seconds before this destination rings (simultaneous)
	Timeout int // seconds this destination rings
	Prompt  bool
}

// IVRMenu is a RingQ IVR menu (v_ivr_menus and v_ivr_menu_options).
type IVRMenu struct {
	UUID              string
	ParentUUID        string
	Domain            string
	Name              string
	Extension         string
	Context           string
	GreetLong         string
	GreetShort        string
	InvalidSound      string
	ExitSound         string
	Timeout           time.Duration // wait for the first digit after the greeting
	InterDigitTimeout time.Duration
	MaxFailures       int
	MaxTimeouts       int
	DigitLen          int
	DirectDial        bool
	ExitApp           string
	ExitData          string
	CIDPrefix         string
	Ringback          string
	Enabled           bool
	Options           []IVROption
}

// IVROption maps digits to an action: menu-exec-app (Param "app data"),
// menu-sub, menu-top, menu-back, menu-exit or menu-play-sound.
type IVROption struct {
	Digits string
	Action string
	Param  string
}

// Queue is a call-centre queue (v_call_center_queues), with
// the standard call centre semantics.
type Queue struct {
	UUID                      string
	Domain                    string
	Name                      string
	Extension                 string
	Strategy                  string // ring-all, longest-idle-agent, round-robin, top-down, agent-with-least-talk-time, agent-with-fewest-calls, sequentially-by-agent-order, random, ring-progressively
	MOHSound                  string
	Greeting                  string
	RecordTemplate            string
	MaxWaitTime               time.Duration // 0 = unlimited
	MaxWaitTimeNoAgent        time.Duration // leave when no agent is logged in for this long
	MaxWaitTimeNoAgentReached time.Duration
	TierRulesApply            bool
	TierRuleWaitSecond        time.Duration
	TierRuleWaitMultiplyLevel bool
	TierRuleNoAgentNoWait     bool
	DiscardAbandonedAfter     time.Duration
	AbandonedResumeAllowed    bool
	TimeoutAction             string // "app:data" (RingQ), run by the dialplan after the queue
	CIDPrefix                 string
	AnnounceSound             string
	AnnounceFrequency         time.Duration
	ExitKeys                  string
	MaxCallers                int
	RingProgressivelyDelay    time.Duration
	OutboundCallerIDName      string
	OutboundCallerIDNumber    string
	RecordCalls               bool
}

// QueueID is the call centre name of a queue: extension@domain.
func (q *Queue) QueueID() string { return q.Extension + "@" + q.Domain }

// Agent statuses (as the portal stores them).
const (
	AgentLoggedOut         = "Logged Out"
	AgentAvailable         = "Available"
	AgentAvailableOnDemand = "Available (On Demand)"
	AgentOnBreak           = "On Break"
)

// Agent is a call-centre agent (v_call_center_agents).
type Agent struct {
	UUID          string
	Domain        string
	Name          string
	Type          string // callback (default) or uuid-standby
	AgentID       string // login id for phone feature codes
	Password      string
	Contact       string // dial string, e.g. user/1001@domain
	Extension     string // derived from Contact when it is user/<ext>[@domain]
	UserUUID      string
	Status        string // configured status (used when there is no saved runtime status)
	CallTimeout   time.Duration
	MaxNoAnswer   int
	WrapUpTime    time.Duration
	RejectDelay   time.Duration
	BusyDelay     time.Duration
	NoAnswerDelay time.Duration
	Record        bool

	SavedStatus string // runtime status saved in the database (agents / rq_agent_state)
	SavedAux    string // AUX code saved with an On Break status
}

// Tier assigns an agent to a queue.
type Tier struct {
	QueueUUID string
	AgentUUID string
	Level     int
	Position  int
}

// AuxCode is a reason for being on break (Lunch, Meeting...).
type AuxCode struct {
	UUID        string
	Domain      string
	Name        string
	BlockDirect bool // also reject direct calls to the agent's extension (DND)
}

// Voicemail is a mailbox (v_voicemails).
type Voicemail struct {
	UUID        string
	Domain      string
	ID          string // usually the extension
	Password    string
	Enabled     bool
	MailTo      string
	GreetingID  int
	NewMessages int
	OldMessages int
}

// Conference is a conference room (v_conferences).
type Conference struct {
	UUID      string
	Domain    string
	Name      string
	Extension string
	PIN       string
	Flags     string
	Enabled   bool
	Record    bool
}

// Features holds the database-backed call features, indexed for lookup.
type Features struct {
	RingGroups  map[string]*RingGroup  // by lower-case uuid
	IVRMenus    map[string]*IVRMenu    // by lower-case uuid
	Queues      map[string]*Queue      // by lower-case uuid
	Agents      map[string]*Agent      // by lower-case uuid
	Tiers       []Tier                 // ordered by queue, level, position
	AuxCodes    []AuxCode              // all domains
	Voicemails  map[string]*Voicemail  // by lower-case id@domain
	Conferences map[string]*Conference // by lower-case uuid
}

// NewFeatures returns an empty feature set.
func NewFeatures() *Features {
	return &Features{RingGroups: map[string]*RingGroup{}, IVRMenus: map[string]*IVRMenu{},
		Queues: map[string]*Queue{}, Agents: map[string]*Agent{}, Voicemails: map[string]*Voicemail{},
		Conferences: map[string]*Conference{}}
}

// Queue finds a queue by uuid, extension@domain, name@domain, or extension
// / name within domain.
func (f *Features) Queue(id, domain string) *Queue {
	if f == nil || id == "" {
		return nil
	}
	if q := f.Queues[strings.ToLower(id)]; q != nil {
		return q
	}
	name, dom, hasDom := strings.Cut(id, "@")
	if hasDom {
		domain = dom
	}
	for _, q := range f.Queues {
		if (domain == "" || strings.EqualFold(q.Domain, domain)) && (q.Extension == name || strings.EqualFold(q.Name, name)) {
			return q
		}
	}
	return nil
}

// Agent finds an agent by uuid, name, agent id, or extension (within a
// domain when given).
func (f *Features) Agent(id, domain string) *Agent {
	if f == nil || id == "" {
		return nil
	}
	if a := f.Agents[strings.ToLower(id)]; a != nil {
		return a
	}
	name, dom, hasDom := strings.Cut(id, "@")
	if hasDom {
		domain = dom
	}
	for _, a := range f.Agents {
		if domain != "" && !strings.EqualFold(a.Domain, domain) {
			continue
		}
		if strings.EqualFold(a.Name, name) || (a.AgentID != "" && a.AgentID == name) || (a.Extension != "" && a.Extension == name) {
			return a
		}
	}
	return nil
}

// AgentByExtension finds the agent whose contact is the given extension.
func (f *Features) AgentByExtension(ext, domain string) *Agent {
	if f == nil {
		return nil
	}
	for _, a := range f.Agents {
		if a.Extension == ext && strings.EqualFold(a.Domain, domain) {
			return a
		}
	}
	return nil
}

// Aux finds an AUX code by name within a domain.
func (f *Features) Aux(name, domain string) *AuxCode {
	if f == nil {
		return nil
	}
	for i := range f.AuxCodes {
		a := &f.AuxCodes[i]
		if strings.EqualFold(a.Name, name) && (a.Domain == "" || strings.EqualFold(a.Domain, domain)) {
			return a
		}
	}
	return nil
}

// Voicemail finds a mailbox.
func (f *Features) Voicemail(id, domain string) *Voicemail {
	if f == nil {
		return nil
	}
	return f.Voicemails[strings.ToLower(id+"@"+domain)]
}

// Conference finds a room by uuid, name or extension (within a domain).
func (f *Features) Conference(id, domain string) *Conference {
	if f == nil || id == "" {
		return nil
	}
	if r := f.Conferences[strings.ToLower(id)]; r != nil {
		return r
	}
	for _, r := range f.Conferences {
		if (domain == "" || strings.EqualFold(r.Domain, domain)) && (strings.EqualFold(r.Name, id) || r.Extension == id) {
			return r
		}
	}
	return nil
}

// AgentExtension extracts the extension from an agent contact such as
// "{call_timeout=20}user/1001@pbx.example.com".
func AgentExtension(contact string) string {
	c := contact
	for strings.HasPrefix(c, "{") || strings.HasPrefix(c, "[") {
		closer := "}"
		if c[0] == '[' {
			closer = "]"
		}
		i := strings.Index(c, closer)
		if i < 0 {
			return ""
		}
		c = c[i+1:]
	}
	rest, ok := strings.CutPrefix(c, "user/")
	if !ok {
		return ""
	}
	ext, _, _ := strings.Cut(rest, "@")
	return ext
}

// RingGroup finds a ring group by uuid, or by extension or name within a
// domain ("" matches any domain).
func (f *Features) RingGroup(id, domain string) *RingGroup {
	if f == nil || id == "" {
		return nil
	}
	if rg := f.RingGroups[strings.ToLower(id)]; rg != nil {
		return rg
	}
	for _, rg := range f.RingGroups {
		if (domain == "" || strings.EqualFold(rg.Domain, domain)) && (rg.Extension == id || strings.EqualFold(rg.Name, id)) {
			return rg
		}
	}
	return nil
}

// IVRMenu finds a menu by uuid, or by name or extension within a domain.
func (f *Features) IVRMenu(id, domain string) *IVRMenu {
	if f == nil || id == "" {
		return nil
	}
	if m := f.IVRMenus[strings.ToLower(id)]; m != nil {
		return m
	}
	for _, m := range f.IVRMenus {
		if (domain == "" || strings.EqualFold(m.Domain, domain)) && (strings.EqualFold(m.Name, id) || m.Extension == id) {
			return m
		}
	}
	return nil
}
