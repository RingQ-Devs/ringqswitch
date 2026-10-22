package core

import (
	"encoding/json"
	"strings"
	"time"
)

// A call's path ("call flow") is kept on the caller's leg and stored with
// its CDR as the variable rq_call_flow (JSON): where the call came from,
// the menus, queues and ring groups it went through, who rang, who
// answered and who hung up. Call history shows it when a call is opened.

type flowStep struct {
	At   int64  `json:"t"` // Unix milliseconds
	Kind string `json:"k"` // start, route, ivr, digits, queue, ring_group, ring, no_answer, answer, voicemail, conference, hangup, end
	Text string `json:"x"`
}

const maxFlowSteps = 80

func (ch *Channel) flow(kind, text string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.flowLocked(kind, text)
}

func (ch *Channel) flowLocked(kind, text string) {
	if len(ch.steps) == 0 || len(ch.steps) >= maxFlowSteps {
		return // only the leg running a dialplan keeps a path (see flowStart)
	}
	if last := ch.steps[len(ch.steps)-1]; last.Kind == kind && last.Text == text {
		return
	}
	ch.steps = append(ch.steps, flowStep{At: time.Now().UnixMilli(), Kind: kind, Text: text})
}

// flowStart begins a call's path when its dialplan first runs.
func (ch *Channel) flowStart() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if len(ch.steps) > 0 {
		return
	}
	v := ch.vars
	text := joinName(v["caller_id_name"], v["caller_id_number"]) + " called " + v["destination_number"]
	if trunk := firstNonEmpty(v["gateway_label"], v["gateway_name"], v["acl_name"]); trunk != "" && ch.Direction == DirectionInbound {
		text += " (trunk " + trunk + ")"
	}
	ch.steps = append(ch.steps, flowStep{At: ch.Created.UnixMilli(), Kind: "start", Text: text})
}

// flowEndLocked closes the path at the hang-up and stores it.
func (ch *Channel) flowEndLocked(cause Cause, how hangupHow) {
	if len(ch.steps) == 0 {
		return
	}
	v := ch.vars
	who := joinName(v["caller_id_name"], v["caller_id_number"])
	end := "Call ended"
	switch how {
	case hangupRemoteBye:
		ch.flowLocked("hangup", who+" hung up")
	case hangupCancelled:
		ch.flowLocked("hangup", who+" hung up before an answer")
	default:
		end += " · ended by the system"
	}
	if t := causeText(cause); t != "" {
		end += " · " + t
	}
	ch.flowLocked("end", end)
	if b, err := json.Marshal(ch.steps); err == nil {
		v["rq_call_flow"] = string(b)
	}
}

// causeText says why a call or a leg ended, when it is not the usual.
func causeText(c Cause) string {
	switch s := c.String(); s {
	case "NORMAL_CLEARING", "ORIGINATOR_CANCEL", "SUCCESS", "NONE", "":
		return ""
	case "USER_BUSY":
		return "busy"
	case "NO_ANSWER", "NO_USER_RESPONSE", "ALLOTTED_TIMEOUT", "PROGRESS_TIMEOUT":
		return "no answer"
	case "USER_NOT_REGISTERED", "SUBSCRIBER_ABSENT":
		return "not registered"
	case "UNALLOCATED_NUMBER", "NO_ROUTE_DESTINATION", "INVALID_NUMBER_FORMAT":
		return "number not found"
	case "CALL_REJECTED":
		return "rejected"
	default:
		return strings.ToLower(strings.ReplaceAll(s, "_", " "))
	}
}

// calleeName names the party a leg rang: "Boboy Testing (1001)".
func calleeName(b *Channel) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.vars
	return joinName(v["callee_id_name"], firstNonEmpty(v["callee_id_number"], v["dialed_user"], v["destination_number"]))
}

// gatewayLabel is a trunk's display name (dial strings may name it by its
// RingQ uuid).
func (c *Core) gatewayLabel(name string) string {
	for _, g := range c.config().Gateways {
		if strings.EqualFold(g.Name, name) || strings.EqualFold(g.UUID, name) {
			return firstNonEmpty(g.Label, g.Name)
		}
	}
	return name
}

// describeDial names who a dial string rings: "1001, 1002" or
// "6591234567 (trunk Carrier)"; trunk names a gateway.
func describeDial(dial string, trunk func(string) string) string {
	dial = strings.ReplaceAll(dial, ":_:", "|")
	for strings.HasPrefix(dial, "{") || strings.HasPrefix(dial, "<") {
		if strings.HasPrefix(dial, "<") {
			i := strings.IndexByte(dial, '>')
			if i < 0 {
				break
			}
			dial = dial[i+1:]
			continue
		}
		_, rest, ok := takeBlock(dial)
		if !ok {
			break
		}
		dial = rest
	}
	var out []string
	seen := map[string]bool{}
	for _, group := range splitTop(dial, '|') {
		for _, leg := range splitTop(group, ',') {
			leg = strings.TrimSpace(leg)
			for strings.HasPrefix(leg, "[") {
				_, rest, ok := takeBlock(leg)
				if !ok {
					break
				}
				leg = strings.TrimSpace(rest)
			}
			name := ""
			parts := strings.Split(leg, "/")
			switch {
			case leg == "" || strings.HasPrefix(leg, "error/"):
			case parts[0] == "user" && len(parts) > 1:
				name, _, _ = strings.Cut(parts[1], "@")
			case len(parts) >= 4 && parts[1] == "gateway":
				num, _, _ := strings.Cut(parts[3], "@")
				gw := parts[2]
				if trunk != nil {
					gw = trunk(gw)
				}
				name = num + " (trunk " + gw + ")"
			default:
				name, _, _ = strings.Cut(parts[len(parts)-1], "@")
			}
			if name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	if len(out) > 6 {
		out = append(out[:6], "…")
	}
	return strings.Join(out, ", ")
}

// queueResultText says why a caller left a queue without an agent.
func queueResultText(result string) string {
	switch result {
	case "ABANDONED":
		return "the caller hung up while waiting"
	case "TIMEOUT":
		return "waited too long"
	case "NO_AGENT_TIMEOUT":
		return "no agent was available"
	case "EXIT_WITH_KEY":
		return "the caller pressed a key to leave"
	case "BREAK_OUT":
		return "sent on by the queue"
	case "":
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(result, "_", " "))
}
