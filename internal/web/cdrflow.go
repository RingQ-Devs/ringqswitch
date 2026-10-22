package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// A call's path in Call history (GET /api/d/{domain}/cdr/{uuid}/flow):
// the steps RingQswitch kept while the call ran (the variable
// rq_call_flow, stored with the CDR), or, for calls without them, what the
// call record tells.

type flowStep struct {
	At   int64  `json:"t"` // Unix milliseconds
	Kind string `json:"k"`
	Text string `json:"x"`
}

// flowColumn reads a call's kept steps from the CDR's variables.
const flowColumn = `json::jsonb -> 'variables' ->> 'rq_call_flow'`

func parseFlow(s string) []flowStep {
	var steps []flowStep
	if s != "" {
		_ = json.Unmarshal([]byte(s), &steps)
	}
	return steps
}

// flowSummary says who answered and who ended a call, for its row.
func flowSummary(steps []flowStep) (answered, ended string) {
	for _, st := range steps {
		switch st.Kind {
		case "answer":
			if answered == "" {
				answered = strings.TrimPrefix(st.Text, "Answered by ")
			}
		case "hangup":
			t := strings.TrimSuffix(strings.TrimSuffix(st.Text, " hung up before an answer"), " hung up")
			if who, _, ok := strings.Cut(t, "'s line ended"); ok {
				t = who
			}
			ended = t
		case "end":
			if ended == "" && strings.Contains(st.Text, "ended by the system") {
				ended = "the system"
			}
		}
	}
	return answered, ended
}

func partyName(name, number string) string {
	switch {
	case name == "" || name == number:
		return number
	case number == "" || strings.Contains(name, number):
		return name
	}
	return name + " (" + number + ")"
}

func epochMs(v string) int64 {
	f, _ := strconv.ParseFloat(v, 64)
	return int64(f * 1000)
}

// hangupText says why a call ended, when it is not the usual.
func hangupText(cause string) string {
	switch cause {
	case "", "NORMAL_CLEARING", "ORIGINATOR_CANCEL", "SUCCESS":
		return ""
	case "USER_BUSY":
		return "busy"
	case "NO_ANSWER", "NO_USER_RESPONSE", "ALLOTTED_TIMEOUT":
		return "no answer"
	case "USER_NOT_REGISTERED", "SUBSCRIBER_ABSENT":
		return "not registered"
	case "UNALLOCATED_NUMBER", "NO_ROUTE_DESTINATION":
		return "number not found"
	}
	return strings.ToLower(strings.ReplaceAll(cause, "_", " "))
}

// flowFromCDR rebuilds a call's path from its record (calls made before
// RingQswitch kept one, or written by the previous switch).
func flowFromCDR(r map[string]string) []flowStep {
	var out []flowStep
	add := func(at int64, kind, text string) {
		if at > 0 {
			out = append(out, flowStep{At: at, Kind: kind, Text: text})
		}
	}
	who := partyName(r["caller_id_name"], r["caller_id_number"])
	start, answer, end := epochMs(r["start_epoch"]), epochMs(r["answer_epoch"]), epochMs(r["end_epoch"])
	add(start, "start", who+" called "+firstNonEmpty(r["caller_destination"], r["destination_number"]))
	if q := r["cc_queue"]; q != "" {
		name, _, _ := strings.Cut(q, "@")
		add(max(epochMs(r["cc_queue_joined_epoch"]), start), "queue", "Waiting in queue "+name)
	}
	if answer > 0 {
		text := "Answered"
		if a := r["cc_agent"]; a != "" && !reUUID.MatchString(a) {
			text += " by " + a
		}
		add(max(epochMs(r["cc_queue_answered_epoch"]), answer), "answer", text)
	} else {
		add(end, "no_answer", "Not answered"+map[bool]string{true: ": " + r["status"], false: ""}[r["status"] != "" && r["status"] != "answered"])
	}
	text := "Call ended"
	switch r["sip_hangup_disposition"] {
	case "recv_bye":
		add(end, "hangup", who+" hung up")
	case "recv_cancel":
		add(end, "hangup", who+" hung up before an answer")
	case "send_bye":
		if answer > 0 {
			add(end, "hangup", "The called side hung up")
		}
	}
	if t := hangupText(r["hangup_cause"]); t != "" {
		text += " · " + t
	}
	add(end, "end", text)
	return out
}

func (s *Server) cdrFlow(ctx context.Context, w http.ResponseWriter, dom domainRow, id string, own ownScope) error {
	if !reUUID.MatchString(id) || !s.store.HasTable("v_xml_cdr") {
		return notFound("call")
	}
	var fields []string
	for _, c := range append(cdrColumns, "sip_hangup_disposition") {
		if s.store.HasColumn("v_xml_cdr", c) {
			if c == "status" {
				fields = append(fields, "'status', "+s.cdrStatusExpr())
				continue
			}
			fields = append(fields, fmt.Sprintf("'%s', %s", c, c))
		}
	}
	if s.store.HasColumn("v_xml_cdr", "json") {
		fields = append(fields, "'rq_flow', "+flowColumn)
	}
	args := []any{id, dom.uuid}
	cond := "true"
	if own.on {
		cond = s.cdrOwnCondition(own, &args)
	}
	rows, err := s.store.JSONRows(ctx, `select jsonb_build_object(`+strings.Join(fields, ", ")+`) from v_xml_cdr
		where xml_cdr_uuid = $1::uuid and domain_uuid = $2::uuid and `+cond, args...)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return notFound("call")
	}
	steps := parseFlow(rows[0]["rq_flow"])
	kept := len(steps) > 0
	if !kept {
		steps = flowFromCDR(rows[0])
	}
	writeJSON(w, map[string]any{"steps": steps, "kept": kept})
	return nil
}
