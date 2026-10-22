package core

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// callcenter_config: the call centre API, in the output format clients read, so
// existing panels and wallboards keep working. Agent status and state are
// runtime values; agent/tier/queue configuration lives in the database
// (v_call_center_*), so "add", "del" and "set <setting>" reload it.

func init() {
	apiCommands["callcenter_config"] = apiCommand{
		"callcenter_config agent|tier|queue <command> ...",
		"call centre: agent set status <agent> '<status>' [aux], agent list, queue list agents|members|tiers <queue>, queue count ...",
		apiCallcenterConfig,
	}
	apiCommands["conference"] = apiCommand{
		"conference list | conference <room> list | conference <room> mute|unmute|kick <member uuid>",
		"conference rooms", apiConference,
	}
}

var agentColumns = "name|instance_id|uuid|type|contact|status|state|max_no_answer|wrap_up_time|reject_delay_time|busy_delay_time|no_answer_delay_time|last_bridge_start|last_bridge_end|last_offered_call|last_status_change|no_answer_count|calls_answered|talk_time|ready_time|external_calls_count"

func unix(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.Unix(), 10)
}

func (cc *callCenter) agentRow(a *ccAgent) string {
	cfg := a.cfg
	return strings.Join([]string{cfg.UUID, "single_box", "", firstNonEmpty(cfg.Type, "callback"), cfg.Contact, a.status, a.state,
		strconv.Itoa(cfg.MaxNoAnswer), strconv.Itoa(int(cfg.WrapUpTime.Seconds())), strconv.Itoa(int(cfg.RejectDelay.Seconds())),
		strconv.Itoa(int(cfg.BusyDelay.Seconds())), strconv.Itoa(int(cfg.NoAnswerDelay.Seconds())), unix(a.bridgeStart),
		unix(a.bridgeEnd), unix(a.lastOffered), unix(a.statusSince), strconv.Itoa(a.noAnswer), strconv.Itoa(a.answered),
		strconv.Itoa(int(a.talk.Seconds())), unix(a.readyAt), "0"}, "|")
}

func apiCallcenterConfig(c *Core, args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: %s", apiCommands["callcenter_config"].usage)
	}
	cc := c.cc
	obj, cmd, rest := strings.ToLower(args[0]), strings.ToLower(args[1]), args[2:]
	f := c.config().Features
	switch obj {
	case "agent":
		switch cmd {
		case "set":
			if len(rest) < 3 {
				return "", fmt.Errorf("usage: callcenter_config agent set status|state|<setting> <agent> <value>")
			}
			key, agent, value := strings.ToLower(rest[0]), rest[1], strings.Join(rest[2:], " ")
			switch key {
			case "status":
				status, aux := value, ""
				// Optional AUX code after the status: 'On Break' Lunch
				if len(rest) > 3 {
					status, aux = rest[2], strings.Join(rest[3:], " ")
				}
				if err := c.SetAgentStatus(AgentStatusChange{Agent: agent, Status: status, Aux: aux, Source: "api"}); err != nil {
					return "", err
				}
			case "aux":
				if err := c.SetAgentStatus(AgentStatusChange{Agent: agent, Status: config.AgentOnBreak, Aux: value, Source: "api"}); err != nil {
					return "", err
				}
			case "state":
				a := f.Agent(agent, "")
				if a == nil {
					return "", fmt.Errorf("invalid agent")
				}
				cc.mu.Lock()
				if ag := cc.agents[strings.ToLower(a.UUID)]; ag != nil {
					cc.setStateLocked(ag, value, nil)
				}
				cc.mu.Unlock()
				cc.poke()
			default:
				c.requestReload() // contact, wrap_up_time...: stored in v_call_center_agents
			}
			return "+OK\n", nil
		case "get":
			if len(rest) < 2 {
				return "", fmt.Errorf("usage: callcenter_config agent get status|state <agent>")
			}
			a := f.Agent(rest[1], "")
			if a == nil {
				return "", fmt.Errorf("invalid agent")
			}
			cc.mu.Lock()
			defer cc.mu.Unlock()
			ag := cc.agents[strings.ToLower(a.UUID)]
			if ag == nil {
				return "", fmt.Errorf("invalid agent")
			}
			switch strings.ToLower(rest[0]) {
			case "status":
				return ag.status + "\n", nil
			case "state":
				return ag.state + "\n", nil
			case "aux":
				return ag.aux + "\n", nil
			}
			return "", fmt.Errorf("unknown agent attribute %q", rest[0])
		case "list":
			cc.mu.Lock()
			defer cc.mu.Unlock()
			var b strings.Builder
			b.WriteString(agentColumns + "\n")
			for _, ag := range cc.agents {
				if ag.cfg == nil {
					continue
				}
				if len(rest) > 0 && f.Agent(rest[0], "") != ag.cfg {
					continue
				}
				b.WriteString(cc.agentRow(ag) + "\n")
			}
			b.WriteString("+OK\n")
			return b.String(), nil
		case "add", "del":
			c.requestReload()
			return "+OK\n", nil
		}
	case "tier":
		switch cmd {
		case "list":
			var b strings.Builder
			b.WriteString("queue|agent|state|level|position\n")
			for _, t := range f.Tiers {
				q := f.Queues[strings.ToLower(t.QueueUUID)]
				if q == nil || (len(rest) > 0 && f.Queue(rest[0], "") != q) {
					continue
				}
				fmt.Fprintf(&b, "%s|%s|Ready|%d|%d\n", q.QueueID(), t.AgentUUID, t.Level, t.Position)
			}
			b.WriteString("+OK\n")
			return b.String(), nil
		case "add", "del", "set", "reload":
			c.requestReload()
			return "+OK\n", nil
		}
	case "queue":
		switch cmd {
		case "load", "unload", "reload":
			c.requestReload()
			return "+OK\n", nil
		case "list":
			if len(rest) == 0 {
				var b strings.Builder
				b.WriteString("name|strategy|moh_sound|time_base_score|tier_rules_apply|tier_rule_wait_second|tier_rule_no_agent_no_wait|discard_abandoned_after|abandoned_resume_allowed|tier_rule_wait_multiply_level|max_wait_time|max_wait_time_with_no_agent|max_wait_time_with_no_agent_time_reached|record_template\n")
				for _, q := range f.Queues {
					fmt.Fprintf(&b, "%s|%s|%s|system|%v|%d|%v|%d|%v|%v|%d|%d|%d|%s\n", q.QueueID(), q.Strategy, q.MOHSound,
						q.TierRulesApply, int(q.TierRuleWaitSecond.Seconds()), q.TierRuleNoAgentNoWait, int(q.DiscardAbandonedAfter.Seconds()),
						q.AbandonedResumeAllowed, q.TierRuleWaitMultiplyLevel, int(q.MaxWaitTime.Seconds()), int(q.MaxWaitTimeNoAgent.Seconds()),
						int(q.MaxWaitTimeNoAgentReached.Seconds()), q.RecordTemplate)
				}
				b.WriteString("+OK\n")
				return b.String(), nil
			}
			what := strings.ToLower(rest[0])
			if len(rest) < 2 {
				return "", fmt.Errorf("usage: callcenter_config queue list agents|members|tiers <queue>")
			}
			q := f.Queue(rest[1], "")
			if q == nil {
				return "", fmt.Errorf("invalid queue")
			}
			return cc.queueList(c, q, what, rest[2:])
		case "count":
			if len(rest) < 2 {
				return "", fmt.Errorf("usage: callcenter_config queue count agents|members|tiers <queue> [status]")
			}
			q := f.Queue(rest[1], "")
			if q == nil {
				return "", fmt.Errorf("invalid queue")
			}
			out, err := cc.queueList(c, q, strings.ToLower(rest[0]), rest[2:])
			if err != nil {
				return "", err
			}
			n := strings.Count(out, "\n") - 2 // header and +OK
			return strconv.Itoa(max(n, 0)) + "\n", nil
		}
	}
	return "", fmt.Errorf("usage: %s", apiCommands["callcenter_config"].usage)
}

func (cc *callCenter) queueList(c *Core, q *config.Queue, what string, filter []string) (string, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	rq := cc.queues[strings.ToLower(q.UUID)]
	var b strings.Builder
	switch what {
	case "agents":
		b.WriteString(agentColumns + "\n")
		for _, t := range cc.tiers[strings.ToLower(q.UUID)] {
			a := cc.agents[strings.ToLower(t.AgentUUID)]
			if a == nil || a.cfg == nil {
				continue
			}
			if len(filter) > 0 && !strings.EqualFold(strings.Join(filter, " "), a.status) {
				continue
			}
			b.WriteString(cc.agentRow(a) + "\n")
		}
	case "members":
		b.WriteString("queue|instance_id|uuid|session_uuid|cid_number|cid_name|system_epoch|joined_epoch|rejoined_epoch|bridge_epoch|abandoned_epoch|base_score|skill_score|serving_agent|serving_system|state\n")
		if rq != nil {
			for _, m := range rq.members {
				agent, bridge := "", "0"
				if m.agent != nil {
					agent = m.agent.cfg.UUID
					bridge = unix(m.answered)
				}
				fmt.Fprintf(&b, "%s|single_box|%s|%s|%s|%s|%s|%s|0|%s|0|0|0|%s|single_box|%s\n", q.QueueID(), m.uuid, m.ch.UUID,
					m.cidNumber, m.cidName, unix(m.joined), unix(m.joined), bridge, agent, m.state)
			}
		}
	case "tiers":
		b.WriteString("queue|agent|state|level|position\n")
		for _, t := range cc.tiers[strings.ToLower(q.UUID)] {
			fmt.Fprintf(&b, "%s|%s|Ready|%d|%d\n", q.QueueID(), t.AgentUUID, t.Level, t.Position)
		}
	default:
		return "", fmt.Errorf("list agents, members or tiers")
	}
	b.WriteString("+OK\n")
	return b.String(), nil
}

func apiConference(c *Core, args []string) (string, error) {
	if len(args) == 0 || strings.EqualFold(args[0], "list") {
		var b strings.Builder
		for _, r := range c.Conferences("") {
			fmt.Fprintf(&b, "+OK Conference %s@%s (%d member(s)%s)\n", r.Name, r.Domain, len(r.Members), map[bool]string{true: " recording", false: ""}[r.Recording])
			for _, m := range r.Members {
				fmt.Fprintf(&b, "%s;%s;%s;%s\n", m.UUID, m.CIDName, m.CIDNumber, map[bool]string{true: "muted", false: "speaking"}[m.Muted])
			}
		}
		if b.Len() == 0 {
			return "+OK No active conferences.\n", nil
		}
		return b.String(), nil
	}
	if len(args) < 2 {
		return "", fmt.Errorf("usage: %s", apiCommands["conference"].usage)
	}
	switch strings.ToLower(args[1]) {
	case "list":
		return apiConference(c, nil)
	case "mute", "unmute":
		if len(args) < 3 || !c.ConferenceMute(args[2], strings.EqualFold(args[1], "mute")) {
			return "", fmt.Errorf("member not found")
		}
		return "+OK\n", nil
	case "kick", "hup":
		if len(args) < 3 {
			return "", fmt.Errorf("member uuid needed")
		}
		ch := c.Channel(args[2])
		if ch == nil {
			return "", fmt.Errorf("member not found")
		}
		ch.Hangup(CauseNormalClearing)
		return "+OK\n", nil
	}
	return "", fmt.Errorf("usage: %s", apiCommands["conference"].usage)
}
