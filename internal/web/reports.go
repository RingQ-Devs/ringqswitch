package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// Call history, reports, voicemail messages and sound files.

// period reads ?from=YYYY-MM-DD&to=YYYY-MM-DD (to inclusive; default today)
// in the server's time zone.
func period(r *http.Request) (time.Time, time.Time, error) {
	q := r.URL.Query()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if q.Get("all") == "1" { // the whole history
		return time.Unix(0, 0), today.AddDate(0, 0, 2), nil
	}
	from, to := today, today
	var err error
	if v := q.Get("from"); v != "" {
		if from, err = time.ParseInLocation("2006-01-02", v, time.Local); err != nil {
			return from, to, badRequest("from: use YYYY-MM-DD")
		}
	}
	if v := q.Get("to"); v != "" {
		if to, err = time.ParseInLocation("2006-01-02", v, time.Local); err != nil {
			return from, to, badRequest("to: use YYYY-MM-DD")
		}
	}
	if to.Before(from) {
		from, to = to, from
	}
	return from, to.AddDate(0, 0, 1), nil
}

// paging reads a page's limit and offset; paged is false when the browser
// asked for every row (up to most).
func paging(r *http.Request, most int) (limit, offset int, paged bool) {
	q := r.URL.Query()
	if q.Get("limit") == "" {
		return most, 0, false
	}
	return min(max(atoi(q.Get("limit"), 50), 1), most), max(atoi(q.Get("offset"), 0), 0), true
}

// cdrColumns are the call history columns sent to the browser (when the
// table has them).
var cdrColumns = []string{"xml_cdr_uuid", "direction", "caller_id_name", "caller_id_number", "caller_destination", "destination_number",
	"start_stamp", "answer_stamp", "end_stamp", "duration", "billsec", "waitsec", "hangup_cause", "status", "missed_call", "record_name",
	"cc_queue", "cc_agent", "cc_cancel_reason", "voicemail_message", "leg", "last_app",
	"start_epoch", "answer_epoch", "end_epoch", "cc_queue_joined_epoch", "cc_queue_answered_epoch", "cc_queue_canceled_epoch", "cc_queue_terminated_epoch"}

// cdrStatusExpr is a call record's result as shown: its status, except
// that a call RingQswitch only picked up itself is not "answered" (records
// written before 0.7.4 could say so): one that joined a queue no agent
// answered for, and was not put through to anyone, is "missed"; one where
// a message was left is "voicemail". The stored rows are not changed.
func (s *Server) cdrStatusExpr() string {
	var whens []string
	if s.store.HasColumn("v_xml_cdr", "voicemail_message") {
		whens = append(whens, "when status = 'answered' and coalesce(voicemail_message::text, '') in ('true', 't') then 'voicemail'")
	}
	if s.store.HasColumn("v_xml_cdr", "cc_queue_joined_epoch") && s.store.HasColumn("v_xml_cdr", "cc_queue_answered_epoch") && s.store.HasColumn("v_xml_cdr", "bridge_uuid") {
		whens = append(whens, "when status = 'answered' and coalesce(cc_queue_joined_epoch::text, '') not in ('', '0') and coalesce(cc_queue_answered_epoch::text, '') in ('', '0') and coalesce(bridge_uuid::text, '') = '' then 'missed'")
	}
	if len(whens) == 0 {
		return "status"
	}
	return "(case " + strings.Join(whens, " ") + " else status end)"
}

// cdrTimes fills in the wait and the talk time of a call record: a queue
// call waited from joining the queue until an agent answered and talked
// from then (its billsec counts from when the queue took the call). Older
// records get their wait this way too.
func cdrTimes(r map[string]string) {
	ep := func(k string) int64 {
		f, _ := strconv.ParseFloat(r[k], 64)
		return int64(f)
	}
	joined, ans, end := ep("cc_queue_joined_epoch"), ep("cc_queue_answered_epoch"), ep("end_epoch")
	r["talksec"] = r["billsec"]
	if joined <= 0 {
		return
	}
	switch {
	case ans >= joined:
		r["waitsec"] = strconv.FormatInt(ans-joined, 10)
		if end >= ans {
			r["talksec"] = strconv.FormatInt(end-ans, 10)
		}
	default:
		for _, t := range []int64{ep("cc_queue_canceled_epoch"), ep("cc_queue_terminated_epoch"), end} {
			if t >= joined {
				r["waitsec"] = strconv.FormatInt(t-joined, 10)
				break
			}
		}
		r["talksec"] = "0"
	}
}

// ownScope limits what a User (not an Owner) sees to their own extension.
type ownScope struct {
	on      bool
	ext     string
	extUUID string
	agent   string // agent uuid, when the extension is an agent
}

// cdrOwnCondition matches the calls of an extension: made from it, to it
// (directly, through a DID, a ring group or a queue), or answered as agent.
func (s *Server) cdrOwnCondition(own ownScope, args *[]any) string {
	*args = append(*args, own.ext)
	n := len(*args)
	conds := []string{fmt.Sprintf("caller_id_number = $%d", n), fmt.Sprintf("destination_number = $%d", n)}
	for _, c := range []string{"source_number", "caller_destination"} {
		if s.store.HasColumn("v_xml_cdr", c) {
			conds = append(conds, fmt.Sprintf("%s = $%d", c, n))
		}
	}
	if s.store.ColumnType("v_xml_cdr", "json") == "jsonb" {
		conds = append(conds, fmt.Sprintf("json->'variables'->>'dialed_extension' = $%d", n),
			fmt.Sprintf("json->'variables'->>'dialed_user' = $%d", n))
	}
	if s.store.HasColumn("v_xml_cdr", "extension_uuid") && reUUID.MatchString(own.extUUID) {
		*args = append(*args, own.extUUID)
		conds = append(conds, fmt.Sprintf("extension_uuid = $%d::uuid", len(*args)))
	}
	if s.store.HasColumn("v_xml_cdr", "cc_agent_uuid") && reUUID.MatchString(own.agent) {
		*args = append(*args, own.agent)
		conds = append(conds, fmt.Sprintf("cc_agent_uuid = $%d::uuid", len(*args)))
	}
	return "(" + strings.Join(conds, " or ") + ")"
}

func (s *Server) cdr(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow, own ownScope) error {
	if r.Method != http.MethodGet {
		return errMethod
	}
	if !s.store.HasTable("v_xml_cdr") {
		writeJSON(w, map[string]any{"rows": []any{}, "total": 0})
		return nil
	}
	from, to, err := period(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	var fields []string
	status := s.cdrStatusExpr()
	for _, c := range cdrColumns {
		if s.store.HasColumn("v_xml_cdr", c) {
			if c == "status" {
				fields = append(fields, "'status', "+status)
				continue
			}
			fields = append(fields, fmt.Sprintf("'%s', %s", c, c))
		}
	}
	if s.store.HasColumn("v_xml_cdr", "json") {
		fields = append(fields, "'rq_flow', "+flowColumn)
	}
	where := []string{"domain_uuid = $1::uuid", "start_stamp >= $2", "start_stamp < $3"}
	args := []any{dom.uuid, from, to}
	if own.on {
		where = append(where, s.cdrOwnCondition(own, &args))
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(v)+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(caller_id_number ilike $%d or caller_id_name ilike $%d or destination_number ilike $%d or caller_destination ilike $%d)", n, n, n, n))
	}
	if v := q.Get("direction"); v == "inbound" || v == "outbound" || v == "local" {
		args = append(args, v)
		where = append(where, fmt.Sprintf("direction = $%d", len(args)))
	}
	if v := q.Get("status"); regexp.MustCompile(`^[a-z]{1,20}$`).MatchString(v) && s.store.HasColumn("v_xml_cdr", "status") {
		args = append(args, v)
		where = append(where, fmt.Sprintf("%s = $%d", status, len(args)))
	}
	limit, offset, paged := paging(r, 10000)
	if !paged {
		limit = 100
	}
	cond := strings.Join(where, " and ")
	var total int
	if err := s.store.Pool().QueryRow(ctx, "select count(*) from v_xml_cdr where "+cond, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := s.store.JSONRows(ctx, fmt.Sprintf("select jsonb_build_object(%s) from v_xml_cdr where %s order by start_stamp desc limit %d offset %d",
		strings.Join(fields, ", "), cond, limit, offset), args...)
	if err != nil {
		return err
	}
	if rows == nil {
		rows = []map[string]string{}
	}
	for _, r := range rows {
		cdrTimes(r)
		if steps := parseFlow(r["rq_flow"]); len(steps) > 0 {
			r["answered_by"], r["ended_by"] = flowSummary(steps)
		}
		delete(r, "rq_flow")
	}
	s.trAnnotate(ctx, rows, "xml_cdr_uuid")
	writeJSON(w, map[string]any{"rows": rows, "total": total})
	return nil
}

func (s *Server) recording(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow, id string, own ownScope) error {
	if !reUUID.MatchString(id) || !s.store.HasColumn("v_xml_cdr", "record_name") {
		return notFound("recording")
	}
	var dir, name string
	args := []any{id, dom.uuid}
	cond := "true"
	if own.on {
		cond = s.cdrOwnCondition(own, &args)
	}
	if err := s.store.Pool().QueryRow(ctx, `select coalesce(record_path, ''), coalesce(record_name, '') from v_xml_cdr
		where xml_cdr_uuid = $1::uuid and domain_uuid = $2::uuid and `+cond, args...).Scan(&dir, &name); err != nil || name == "" {
		return notFound("recording")
	}
	path := filepath.Join(dir, name)
	if !inside(s.sw.Config().Settings.RecordingsDir, path) {
		return notFound("recording")
	}
	return serveAudio(w, r, path)
}

// ---------------------------------------------------------------------------
// Reports

func (s *Server) report(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow, kind string, own ownScope) error {
	if r.Method != http.MethodGet {
		return errMethod
	}
	from, to, err := period(r)
	if err != nil {
		return err
	}
	var out any
	limit, offset, paged := paging(r, 50000)
	total := -1 // rows in the period, for the list reports
	page := fmt.Sprintf(" limit %d offset %d", limit, offset)
	switch kind {
	case "agent-status":
		var rows []*agentStatusRow
		rows, err = s.agentStatusReport(ctx, dom, from, to)
		if own.on {
			mine := []*agentStatusRow{}
			for _, a := range rows {
				if a.AgentUUID == own.agent {
					mine = append(mine, a)
				}
			}
			rows = mine
		}
		out = rows
	case "hourly":
		out, err = s.hourlyReport(ctx, dom, from, to)
	case "agent-log":
		agent := r.URL.Query().Get("agent")
		if own.on {
			agent = own.agent
			if agent == "" {
				out, total = []map[string]string{}, 0
				break
			}
		}
		if agent != "" && !reUUID.MatchString(agent) {
			return badRequest("invalid agent")
		}
		where := `from rq_agent_status_log where domain_uuid = $1::uuid and insert_date >= $2 and insert_date < $3
			and ($4 = '' or call_center_agent_uuid::text = $4)`
		total, err = s.count(ctx, "rq_agent_status_log", where, dom.uuid, from, to, agent)
		if err == nil {
			out, err = s.rows(ctx, "rq_agent_status_log", `select jsonb_build_object('time', insert_date, 'agent_uuid', call_center_agent_uuid,
			'agent_name', agent_name, 'extension', extension, 'status', status, 'aux_code', coalesce(aux_code, ''),
			'previous_status', coalesce(previous_status, ''), 'previous_aux_code', coalesce(previous_aux_code, ''),
			'previous_seconds', coalesce(previous_seconds, 0), 'source', coalesce(source, '')) `+where+` order by insert_date desc`+page,
				dom.uuid, from, to, agent)
		}
	case "queues":
		sla := max(atoi(r.URL.Query().Get("sla"), 20), 1)
		out, err = s.rows(ctx, "rq_queue_calls", `select jsonb_build_object('queue_uuid', call_center_queue_uuid,
			'queue_name', max(queue_name), 'queue_extension', max(queue_extension), 'offered', count(*),
			'answered', count(*) filter (where result = 'answered'), 'abandoned', count(*) filter (where result = 'abandoned'),
			'timeout', count(*) filter (where result in ('timeout', 'no_agent_timeout')),
			'exited', count(*) filter (where result in ('exit_with_key', 'break_out')),
			'avg_wait', coalesce(round(avg(wait_seconds) filter (where result = 'answered')), 0),
			'max_wait', coalesce(max(wait_seconds), 0),
			'avg_talk', coalesce(round(avg(talk_seconds) filter (where result = 'answered')), 0),
			'talk_total', coalesce(sum(talk_seconds) filter (where result = 'answered'), 0),
			'avg_abandon_wait', coalesce(round(avg(wait_seconds) filter (where result = 'abandoned')), 0),
			'in_sla', count(*) filter (where result = 'answered' and wait_seconds <= $4))
			from rq_queue_calls where domain_uuid = $1::uuid and joined_at >= $2 and joined_at < $3
			group by call_center_queue_uuid order by max(queue_extension)`, dom.uuid, from, to, sla)
	case "agents":
		out, err = s.rows(ctx, "rq_queue_calls", `select jsonb_build_object('agent_uuid', call_center_agent_uuid,
			'agent_name', max(agent_name), 'extension', max(agent_extension), 'answered', count(*),
			'talk_total', coalesce(sum(talk_seconds), 0), 'avg_talk', coalesce(round(avg(talk_seconds)), 0),
			'avg_wait', coalesce(round(avg(wait_seconds)), 0), 'queues', string_agg(distinct queue_name, ', '))
			from rq_queue_calls where domain_uuid = $1::uuid and joined_at >= $2 and joined_at < $3
			and result = 'answered' and call_center_agent_uuid is not null
			group by call_center_agent_uuid order by max(agent_name)`, dom.uuid, from, to)
	case "queue-calls":
		queue := r.URL.Query().Get("queue")
		if queue != "" && !reUUID.MatchString(queue) {
			return badRequest("invalid queue")
		}
		callCol := "null"
		if s.store.HasColumn("rq_queue_calls", "call_uuid") {
			callCol = "call_uuid"
		}
		where := `from rq_queue_calls where domain_uuid = $1::uuid and joined_at >= $2 and joined_at < $3
			and ($4 = '' or call_center_queue_uuid::text = $4)`
		total, err = s.count(ctx, "rq_queue_calls", where, dom.uuid, from, to, queue)
		if err == nil {
			out, err = s.rows(ctx, "rq_queue_calls", `select jsonb_build_object('uuid', queue_call_uuid, 'call_uuid', `+callCol+`, 'queue_name', queue_name,
			'queue_extension', queue_extension, 'caller_id_name', caller_id_name, 'caller_id_number', caller_id_number,
			'joined_at', joined_at, 'answered_at', answered_at, 'ended_at', ended_at, 'wait_seconds', wait_seconds,
			'talk_seconds', talk_seconds, 'result', result, 'agent_name', coalesce(agent_name, ''),
			'agent_extension', coalesce(agent_extension, ''), 'agents_offered', agents_offered, 'no_answer_count', no_answer_count) `+where+
				` order by joined_at desc`+page, dom.uuid, from, to, queue)
		}
		if rows, ok := out.([]map[string]string); ok && err == nil {
			s.trAnnotate(ctx, rows, "call_uuid")
		}
	default:
		return notFound("report")
	}
	if err != nil {
		return err
	}
	if paged && total >= 0 {
		writeJSON(w, map[string]any{"rows": out, "total": total})
		return nil
	}
	writeJSON(w, out)
	return nil
}

func (s *Server) rows(ctx context.Context, table, sql string, args ...any) ([]map[string]string, error) {
	if !s.store.HasTable(table) {
		return []map[string]string{}, nil
	}
	rows, err := s.store.JSONRows(ctx, sql, args...)
	if rows == nil {
		rows = []map[string]string{}
	}
	return rows, err
}

// count counts a table's rows matching a "from ... where ..." clause.
func (s *Server) count(ctx context.Context, table, from string, args ...any) (int, error) {
	if !s.store.HasTable(table) {
		return 0, nil
	}
	var n int
	err := s.store.Pool().QueryRow(ctx, "select count(*) "+from, args...).Scan(&n)
	return n, err
}

type agentStatusRow struct {
	AgentUUID string         `json:"agent_uuid"`
	Name      string         `json:"agent_name"`
	Extension string         `json:"extension"`
	Seconds   map[string]int `json:"seconds"` // "Available", "On Break: Lunch", "Logged Out"...
	Breaks    map[string]int `json:"breaks"`  // number of times each AUX code was used
	LoggedIn  int            `json:"logged_in_seconds"`
	Available int            `json:"available_seconds"`
	OnBreak   int            `json:"break_seconds"`
	Changes   int            `json:"changes"`
	Current   string         `json:"current"`
}

// agentStatusReport adds up, per agent, the time spent in each status and
// AUX code within the period. Each log row closes the previous status
// (previous_seconds long); the status in progress is added from the live
// state.
func (s *Server) agentStatusReport(ctx context.Context, dom domainRow, from, to time.Time) ([]*agentStatusRow, error) {
	byAgent := map[string]*agentStatusRow{}
	get := func(id, name, ext string) *agentStatusRow {
		a := byAgent[id]
		if a == nil {
			a = &agentStatusRow{AgentUUID: id, Seconds: map[string]int{}, Breaks: map[string]int{}}
			byAgent[id] = a
		}
		a.Name, a.Extension = firstNonEmpty(a.Name, name), firstNonEmpty(a.Extension, ext)
		return a
	}
	addTime := func(a *agentStatusRow, status, aux string, secs int) {
		if secs <= 0 {
			return
		}
		key := status
		if status == "On Break" {
			key = "On Break: " + firstNonEmpty(aux, "Break")
			a.OnBreak += secs
		}
		if strings.HasPrefix(status, "Available") {
			a.Available += secs
		}
		if status != "Logged Out" && status != "" {
			a.LoggedIn += secs
		}
		a.Seconds[key] += secs
	}
	if s.store.HasTable("rq_agent_status_log") {
		rows, err := s.store.Pool().Query(ctx, `select call_center_agent_uuid::text, coalesce(agent_name, ''), coalesce(extension, ''),
			coalesce(status, ''), coalesce(aux_code, ''), coalesce(previous_status, ''), coalesce(previous_aux_code, ''),
			greatest(0, extract(epoch from least(insert_date, $3) - greatest(insert_date - make_interval(secs => coalesce(previous_seconds, 0)), $2)))::bigint,
			insert_date < $3
			from rq_agent_status_log
			where domain_uuid = $1::uuid and call_center_agent_uuid is not null and insert_date > $2
			and insert_date - make_interval(secs => coalesce(previous_seconds, 0)) < $3`, dom.uuid, from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name, ext, status, aux, prev, prevAux string
			var secs int64
			var inPeriod bool
			if err := rows.Scan(&id, &name, &ext, &status, &aux, &prev, &prevAux, &secs, &inPeriod); err != nil {
				rows.Close()
				return nil, err
			}
			a := get(id, name, ext)
			addTime(a, prev, prevAux, int(secs))
			if inPeriod {
				a.Changes++
				if status == "On Break" {
					a.Breaks[firstNonEmpty(aux, "Break")]++
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	for _, ag := range s.sw.Agents(dom.name) {
		a := get(ag.UUID, ag.Name, ag.Extension)
		a.Current = ag.Status
		if ag.Aux != "" {
			a.Current += ": " + ag.Aux
		}
		since := time.Unix(ag.StatusSince, 0)
		start, end := since, now
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if end.After(start) {
			addTime(a, ag.Status, ag.Aux, int(end.Sub(start).Seconds()))
		}
	}
	out := make([]*agentStatusRow, 0, len(byAgent))
	for _, a := range byAgent {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---------------------------------------------------------------------------
// Voicemail messages

func (s *Server) voicemailMessages(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow, id, action string, own ownScope) error {
	if !s.store.HasTable("v_voicemail_messages") || !s.store.HasTable("v_voicemails") {
		if id == "" && r.Method == http.MethodGet {
			writeJSON(w, []any{})
			return nil
		}
		return notFound("voicemail")
	}
	if id == "" {
		if r.Method != http.MethodGet {
			return errMethod
		}
		box := r.URL.Query().Get("box")
		if own.on {
			box = own.ext
		}
		text := "''"
		if s.store.HasColumn("v_voicemail_messages", "message_transcription") {
			text = "coalesce(m.message_transcription, '')"
		}
		rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('uuid', m.voicemail_message_uuid, 'box', v.voicemail_id,
			'created', m.created_epoch, 'caller_id_name', coalesce(m.caller_id_name, ''), 'caller_id_number', coalesce(m.caller_id_number, ''),
			'length', m.message_length, 'status', coalesce(m.message_status, ''), 'message_transcription', `+text+`)
			from v_voicemail_messages m join v_voicemails v on v.voicemail_uuid = m.voicemail_uuid
			where v.domain_uuid = $1::uuid and ($2 = '' or v.voicemail_id = $2) order by m.created_epoch desc limit 2000`, dom.uuid, box)
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []map[string]string{}
		}
		s.trAnnotateVoicemail(ctx, rows)
		writeJSON(w, rows)
		return nil
	}
	if !reUUID.MatchString(id) {
		return notFound("message")
	}
	var box string
	if err := s.store.Pool().QueryRow(ctx, `select v.voicemail_id from v_voicemail_messages m join v_voicemails v on v.voicemail_uuid = m.voicemail_uuid
		where m.voicemail_message_uuid = $1::uuid and v.domain_uuid = $2::uuid`, id, dom.uuid).Scan(&box); err != nil {
		return notFound("message")
	}
	if own.on && box != own.ext {
		return notFound("message")
	}
	vmDir := s.sw.Config().Settings.VoicemailDir
	path := filepath.Join(vmDir, dom.name, box, "msg_"+id+".wav")
	switch {
	case action == "audio" && r.Method == http.MethodGet:
		if !inside(vmDir, path) {
			return notFound("message")
		}
		return serveAudio(w, r, path)
	case action == "transcript":
		return s.trVoicemail(ctx, w, r, dom, id, box, path)
	case action == "read" && r.Method == http.MethodPost:
		if _, err := s.store.Pool().Exec(ctx, `update v_voicemail_messages set message_status = 'saved' where voicemail_message_uuid = $1::uuid`, id); err != nil {
			return err
		}
	case action == "" && r.Method == http.MethodDelete:
		if _, err := s.store.Pool().Exec(ctx, `delete from v_voicemail_messages where voicemail_message_uuid = $1::uuid`, id); err != nil {
			return err
		}
		if inside(vmDir, path) {
			_ = os.Remove(path)
		}
		if s.store.HasTable("rq_transcripts") { // its transcript goes with it
			_, _ = s.store.Pool().Exec(ctx, `delete from rq_transcripts where call_uuid = $1::uuid and kind = 'voicemail'`, id)
		}
	default:
		return errMethod
	}
	s.reload() // message counts and the phones' message lights
	writeJSON(w, map[string]string{"ok": "done"})
	return nil
}

// ---------------------------------------------------------------------------
// Sounds: prompts (sounds/prompts) and music on hold (sounds/music/<folder>)

var reFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,80}$`)

// soundList returns the prompt files and the music folders with their files.
func (s *Server) soundList() ([]string, map[string][]string) {
	dir := s.sw.Config().Settings.SoundsDir
	prompts := []string{}
	if entries, err := os.ReadDir(filepath.Join(dir, "prompts")); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".wav") {
				prompts = append(prompts, e.Name())
			}
		}
	}
	music := map[string][]string{"default": {}}
	if entries, err := os.ReadDir(filepath.Join(dir, "music")); err == nil {
		for _, e := range entries {
			if !e.IsDir() || !reFileName.MatchString(e.Name()) || e.Name() == "8000" {
				continue
			}
			files := []string{}
			if sub, err := os.ReadDir(filepath.Join(dir, "music", e.Name())); err == nil {
				for _, f := range sub {
					if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ".wav") {
						files = append(files, f.Name())
					}
				}
			}
			music[e.Name()] = files
		}
	}
	return prompts, music
}

// soundPath turns "prompts/x.wav" or "music/<folder>/x.wav" into a path in
// the sounds directory.
func (s *Server) soundPath(rel string) (string, error) {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	ok := len(parts) == 2 && parts[0] == "prompts" && reFileName.MatchString(parts[1]) ||
		len(parts) == 3 && parts[0] == "music" && reFileName.MatchString(parts[1]) && reFileName.MatchString(parts[2])
	if !ok || !strings.HasSuffix(strings.ToLower(parts[len(parts)-1]), ".wav") {
		return "", badRequest("invalid sound file name")
	}
	dir := s.sw.Config().Settings.SoundsDir
	p := filepath.Join(append([]string{dir}, parts...)...)
	if !inside(dir, p) {
		return "", badRequest("invalid sound file name")
	}
	return p, nil
}

func (s *Server) sounds(w http.ResponseWriter, r *http.Request, sub string) error {
	q := r.URL.Query()
	switch {
	case sub == "file" && r.Method == http.MethodGet:
		p, err := s.soundPath(q.Get("path"))
		if err != nil {
			return err
		}
		return serveAudio(w, r, p)
	case sub == "" && r.Method == http.MethodGet:
		prompts, music := s.soundList()
		writeJSON(w, map[string]any{"prompts": prompts, "music": music})
		return nil
	case sub == "" && r.Method == http.MethodDelete:
		p, err := s.soundPath(q.Get("path"))
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil {
			return notFound("sound file")
		}
		writeJSON(w, map[string]string{"ok": "deleted"})
		return nil
	case sub == "" && r.Method == http.MethodPost:
		// Body: the WAV or MP3 file itself. ?path=prompts/welcome.wav or
		// music/<folder>/song.wav. Converted to 8 kHz mono 16-bit WAV.
		rel := q.Get("path")
		if strings.HasSuffix(strings.ToLower(rel), ".mp3") {
			rel = rel[:len(rel)-4]
		}
		if !strings.HasSuffix(strings.ToLower(rel), ".wav") {
			rel += ".wav"
		}
		p, err := s.soundPath(rel)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 50<<20))
		if err != nil {
			return badRequest("file too large (50 MB at most)")
		}
		var pcm []int16
		if media.IsMP3(data) {
			if pcm, err = media.DecodeMP3(data); err != nil {
				return badRequest("not an MP3 file this switch can read (%v)", err)
			}
		} else if pcm, err = media.DecodeWAV(data); err != nil {
			return badRequest("not a WAV or MP3 file this switch can read (%v); save it as WAV (PCM) or MP3 and try again", err)
		}
		if len(pcm) < 800 {
			return badRequest("the file is too short")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		tmp := p + ".tmp"
		wav, err := media.CreateWAV(tmp)
		if err != nil {
			return err
		}
		if err := wav.Write(pcm); err != nil {
			wav.Close()
			os.Remove(tmp)
			return err
		}
		if err := wav.Close(); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
		s.log.Info("web: sound uploaded", "path", p, "seconds", len(pcm)/8000)
		writeJSON(w, map[string]any{"ok": "uploaded", "path": rel, "seconds": strconv.Itoa(len(pcm) / 8000)})
		return nil
	}
	return errMethod
}

// hourlyReport counts queue calls per hour (for the wallboard chart).
func (s *Server) hourlyReport(ctx context.Context, dom domainRow, from, to time.Time) ([]map[string]string, error) {
	return s.rows(ctx, "rq_queue_calls", `select jsonb_build_object('hour', hr, 'queue_uuid', q, 'offered', offered,
		'answered', answered, 'abandoned', abandoned) from (
		select extract(hour from joined_at)::int as hr, coalesce(call_center_queue_uuid::text, '') as q, count(*) as offered,
		count(*) filter (where result = 'answered') as answered, count(*) filter (where result = 'abandoned') as abandoned
		from rq_queue_calls where domain_uuid = $1::uuid and joined_at >= $2 and joined_at < $3
		group by 1, 2) t order by hr`, dom.uuid, from, to)
}
