package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
)

// Writer mirrors live state into the live state tables
// (registrations, channels, calls) and inserts CDRs, from the event bus.
// It runs on its own goroutine so database latency never delays calls.
type Writer struct {
	s       *Store
	sw      *core.Core
	sub     *core.Subscription
	done    chan struct{}
	host    string
	errs    atomic.Uint64
	written atomic.Uint64
}

const writerTimeout = 10 * time.Second

// StartWriter starts mirroring the switch's events into the database.
func (s *Store) StartWriter(sw *core.Core) *Writer {
	w := &Writer{s: s, done: make(chan struct{}), host: s.cfg.Hostname}
	ctx, cancel := context.WithTimeout(context.Background(), writerTimeout)
	defer cancel()
	// Rows left by a previous run of this host are stale.
	if s.cfg.WriteRegistrations && s.HasTable("registrations") {
		w.exec(ctx, `delete from registrations where hostname = $1`, w.host)
	}
	if s.cfg.WriteChannels {
		if s.HasTable("channels") {
			w.exec(ctx, `delete from channels where hostname = $1`, w.host)
		}
		if s.HasTable("calls") {
			w.exec(ctx, `delete from calls where hostname = $1`, w.host)
		}
	}
	w.sw = sw
	sw.SetVoicemailStore(s) // listening to voicemail by phone
	w.syncCallCenterTables(ctx)
	w.sub = sw.Events().Subscribe(16384, func(e *core.Event) bool {
		switch e.Name {
		case core.EvReloadXML:
			return true
		case core.EvCustom:
			switch sub := e.Subclass(); {
			case sub == "callcenter::info", sub == "vm::maintenance", sub == "ringq::user_vars", sub == "ringq::guard":
				return true
			default:
				return s.cfg.WriteRegistrations && strings.HasPrefix(sub, "ringq::")
			}
		case core.EvChannelHangupComplete:
			return true
		case core.EvChannelCreate, core.EvChannelAnswer, core.EvChannelBridge, core.EvChannelUnbridge,
			core.EvChannelExecute, core.EvChannelCallState, core.EvChannelProgress, core.EvChannelProgressMedia:
			return s.cfg.WriteChannels
		}
		return false
	})
	go w.run()
	return w
}

// Close stops the writer after it has drained queued events.
func (w *Writer) Close() {
	w.sub.Close()
	close(w.sub.C)
	<-w.done
}

// Stats returns the number of statements executed and failed.
func (w *Writer) Stats() (written, failed, dropped uint64) {
	return w.written.Load(), w.errs.Load(), w.sub.Dropped()
}

func (w *Writer) run() {
	defer close(w.done)
	lastDropped := uint64(0)
	for e := range w.sub.C {
		ctx, cancel := context.WithTimeout(context.Background(), writerTimeout)
		w.handle(ctx, e)
		cancel()
		if d := w.sub.Dropped(); d != lastDropped {
			w.s.log.Warn("database writer is falling behind; events dropped", "dropped", d)
			lastDropped = d
		}
	}
}

func (w *Writer) exec(ctx context.Context, sql string, args ...any) bool {
	if _, err := w.s.pool.Exec(ctx, sql, args...); err != nil {
		if n := w.errs.Add(1); n <= 20 || n%100 == 0 {
			w.s.log.Warn("database write failed", "err", err, "failures", n)
		}
		return false
	}
	w.written.Add(1)
	return true
}

func eventVars(e *core.Event) map[string]string {
	vars := map[string]string{}
	for _, h := range e.Headers {
		if k, ok := strings.CutPrefix(h[0], "variable_"); ok {
			vars[k] = h[1]
		}
	}
	return vars
}

func (w *Writer) handle(ctx context.Context, e *core.Event) {
	switch e.Name {
	case core.EvReloadXML:
		w.syncCallCenterTables(ctx)
	case core.EvCustom:
		switch e.Subclass() {
		case "callcenter::info":
			w.callCenter(ctx, e)
		case "vm::maintenance":
			w.voicemail(ctx, e)
		case "ringq::user_vars":
			w.userVars(ctx, e)
		case "ringq::guard":
			w.guardEvent(ctx, e)
		default:
			w.registration(ctx, e)
		}
	case core.EvChannelCreate:
		w.channelCreate(ctx, e)
	case core.EvChannelExecute:
		w.update(ctx, "channels", e.Get("Unique-ID"), map[string]string{
			"application": e.Get("Application"), "application_data": e.Get("Application-Data")})
	case core.EvChannelAnswer, core.EvChannelCallState, core.EvChannelProgress, core.EvChannelProgressMedia:
		w.update(ctx, "channels", e.Get("Unique-ID"), map[string]string{
			"callstate": e.Get("Channel-Call-State"), "state": e.Get("Channel-State"),
			"read_codec": e.Get("Channel-Read-Codec-Name"), "write_codec": e.Get("Channel-Write-Codec-Name"),
			"callee_name": e.Get("Caller-Callee-ID-Name"), "callee_num": e.Get("Caller-Callee-ID-Number")})
	case core.EvChannelBridge:
		w.bridge(ctx, e)
	case core.EvChannelUnbridge:
		if w.s.HasTable("calls") {
			w.exec(ctx, `delete from calls where caller_uuid = $1 and hostname = $2`, e.Get("Bridge-A-Unique-ID"), w.host)
		}
	case core.EvChannelHangupComplete:
		uuid := e.Get("Unique-ID")
		if w.s.cfg.WriteChannels {
			if w.s.HasTable("channels") {
				w.exec(ctx, `delete from channels where uuid = $1 and hostname = $2`, uuid, w.host)
			}
			if w.s.HasTable("calls") {
				w.exec(ctx, `delete from calls where (caller_uuid = $1 or callee_uuid = $1) and hostname = $2`, uuid, w.host)
			}
		}
		if w.s.cfg.WriteCDR {
			w.cdr(ctx, e)
		}
	}
}

// ---------------------------------------------------------------------------
// registrations

func (w *Writer) registration(ctx context.Context, e *core.Event) {
	if !w.s.HasTable("registrations") {
		return
	}
	user, realm, contact := e.Get("User"), e.Get("Realm"), e.Get("Contact")
	url := "sofia/internal/" + contact
	switch e.Subclass() {
	case "ringq::register":
		exp, _ := strconv.Atoi(e.Get("Expires"))
		w.insertRow(ctx, "registrations", map[string]string{
			"reg_user": user, "realm": realm, "token": e.Get("Call-ID"), "url": url,
			"expires":    strconv.FormatInt(time.Now().Unix()+int64(exp)+60, 10),
			"network_ip": e.Get("Network-IP"), "network_port": e.Get("Network-Port"),
			"network_proto": e.Get("Network-Proto"), "hostname": w.host, "metadata": e.Get("User-Agent"),
		}, `delete from registrations where reg_user = $1 and realm = $2 and url = $3 and hostname = $4`,
			user, realm, url, w.host)
	case "ringq::unregister", "ringq::expire":
		w.exec(ctx, `delete from registrations where reg_user = $1 and realm = $2 and url = $3 and hostname = $4`,
			user, realm, url, w.host)
	}
}

// ---------------------------------------------------------------------------
// channels and calls

func (w *Writer) channelCreate(ctx context.Context, e *core.Event) {
	if !w.s.HasTable("channels") {
		return
	}
	v := eventVars(e)
	created := time.Now()
	if us, err := strconv.ParseInt(e.Get("Caller-Channel-Created-Time"), 10, 64); err == nil && us > 0 {
		created = time.UnixMicro(us)
	}
	row := map[string]string{
		"uuid": e.Get("Unique-ID"), "direction": e.Get("Call-Direction"),
		"created": created.Format("2006-01-02 15:04:05"), "created_epoch": strconv.FormatInt(created.Unix(), 10),
		"name": e.Get("Channel-Name"), "state": e.Get("Channel-State"), "callstate": e.Get("Channel-Call-State"),
		"cid_name": e.Get("Caller-Caller-ID-Name"), "cid_num": e.Get("Caller-Caller-ID-Number"),
		"ip_addr": e.Get("Caller-Network-Addr"), "dest": e.Get("Caller-Destination-Number"),
		"dialplan": "XML", "context": e.Get("Caller-Context"), "hostname": w.host,
		"presence_id": v["presence_id"], "accountcode": v["accountcode"],
		"call_uuid":        firstNonEmpty(v["originating_leg_uuid"], e.Get("Unique-ID")),
		"initial_cid_name": e.Get("Caller-Caller-ID-Name"), "initial_cid_num": e.Get("Caller-Caller-ID-Number"),
		"initial_ip_addr": e.Get("Caller-Network-Addr"), "initial_dest": e.Get("Caller-Destination-Number"),
		"initial_dialplan": "XML", "initial_context": e.Get("Caller-Context"),
		"callee_name": v["callee_id_name"], "callee_num": v["callee_id_number"],
	}
	w.insertRow(ctx, "channels", row, "")
}

func (w *Writer) bridge(ctx context.Context, e *core.Event) {
	a, b := e.Get("Bridge-A-Unique-ID"), e.Get("Bridge-B-Unique-ID")
	if a == "" || b == "" {
		return
	}
	if w.s.HasTable("calls") {
		now := time.Now()
		w.insertRow(ctx, "calls", map[string]string{
			"call_uuid": a, "call_created": now.Format("2006-01-02 15:04:05"),
			"call_created_epoch": strconv.FormatInt(now.Unix(), 10),
			"caller_uuid":        a, "callee_uuid": b, "hostname": w.host,
		}, `delete from calls where caller_uuid = $1 and hostname = $2`, a, w.host)
	}
	w.update(ctx, "channels", a, map[string]string{"call_uuid": a, "callstate": "ACTIVE",
		"callee_name": e.Get("Other-Leg-Caller-ID-Name"), "callee_num": e.Get("Other-Leg-Caller-ID-Number")})
	w.update(ctx, "channels", b, map[string]string{"call_uuid": a, "callstate": "ACTIVE"})
}

// update sets non-empty values on the row with the given uuid.
func (w *Writer) update(ctx context.Context, table, uuid string, vals map[string]string) {
	if uuid == "" || !w.s.HasTable(table) {
		return
	}
	var sets []string
	args := []any{uuid, w.host}
	for col, v := range vals {
		if v == "" || !w.s.HasColumn(table, col) {
			continue
		}
		args = append(args, v)
		sets = append(sets, quoteIdent(col)+" = "+w.cast(table, col, len(args)))
	}
	if len(sets) == 0 {
		return
	}
	w.exec(ctx, "update "+quoteIdent(table)+" set "+strings.Join(sets, ", ")+" where uuid = $1 and hostname = $2", args...)
}

// cast renders parameter n converted to the column's type, so every value
// can be sent as text.
func (w *Writer) cast(table, col string, n int) string {
	p := "$" + strconv.Itoa(n)
	switch t := w.s.columnType(table, col); t {
	case "", "text", "character varying":
		return p
	case "USER-DEFINED", "ARRAY":
		return p
	default:
		return p + "::text::" + t
	}
}

// insertRow inserts the columns of row that the table has (empty values
// become NULL). A non-empty pre statement runs first in the same
// transaction (used to replace rows).
func (w *Writer) insertRow(ctx context.Context, table string, row map[string]string, pre string, preArgs ...any) bool {
	var cols, vals []string
	var args []any
	for col, v := range row {
		if !w.s.HasColumn(table, col) {
			continue
		}
		switch w.s.columnType(table, col) {
		case "USER-DEFINED", "ARRAY":
			continue
		}
		var arg any
		if v != "" {
			arg = v
		}
		args = append(args, arg)
		cols = append(cols, quoteIdent(col))
		vals = append(vals, w.cast(table, col, len(args)))
	}
	if len(cols) == 0 {
		return false
	}
	sql := "insert into " + quoteIdent(table) + " (" + strings.Join(cols, ", ") + ") values (" +
		strings.Join(vals, ", ") + ") on conflict do nothing"
	if pre == "" {
		return w.exec(ctx, sql, args...)
	}
	tx, err := w.s.pool.Begin(ctx)
	if err != nil {
		w.errs.Add(1)
		w.s.log.Warn("database write failed", "err", err)
		return false
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, pre, preArgs...); err == nil {
		if _, err = tx.Exec(ctx, sql, args...); err == nil {
			if err = tx.Commit(ctx); err == nil {
				w.written.Add(1)
				return true
			}
		}
		w.errs.Add(1)
		w.s.log.Warn("database write failed", "table", table, "err", err)
		return false
	} else {
		w.errs.Add(1)
		w.s.log.Warn("database write failed", "table", table, "err", err)
	}
	return false
}

// ---------------------------------------------------------------------------
// CDR

func epochStamp(epoch string) string {
	n, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	return time.Unix(n, 0).Format(time.RFC3339)
}

// waitSeconds is how long the caller waited for a person: in a queue from
// joining it until an agent answered (or until the caller gave up), else
// until the call was bridged, answered or given up ("" if unknown).
func waitSeconds(v map[string]string, answered bool) string {
	ep := func(k string) int64 {
		n, _ := strconv.ParseInt(v[k], 10, 64)
		return n
	}
	since := func(from int64, until ...int64) string {
		for _, t := range until {
			if t > 0 && from > 0 && t >= from {
				return strconv.FormatInt(t-from, 10)
			}
		}
		return ""
	}
	start, end := ep("start_epoch"), ep("end_epoch")
	if joined := ep("cc_queue_joined_epoch"); joined > 0 {
		if ans := ep("cc_queue_answered_epoch"); ans > 0 {
			return since(joined, ans)
		}
		return since(joined, ep("cc_queue_canceled_epoch"), ep("cc_queue_terminated_epoch"), end)
	}
	if b := ep("bridge_epoch"); b > 0 {
		return since(start, b)
	}
	if answered {
		return since(start, ep("answer_epoch"))
	}
	return since(start, end) // rang until the caller gave up
}

// cdr writes a RingQ v_xml_cdr row (and the same row into every other
// configured CDR table) for a finished channel.
func (w *Writer) cdr(ctx context.Context, e *core.Event) {
	v := eventVars(e)
	leg := "a"
	if v["originating_leg_uuid"] != "" || v["originator"] != "" {
		leg = "b"
	}
	if leg == "b" && !w.s.cfg.CDRBLeg {
		return
	}
	cause := firstNonEmpty(v["hangup_cause"], e.Get("Hangup-Cause"))
	answered := v["answer_epoch"] != "" && v["answer_epoch"] != "0"
	direction := v["call_direction"]
	if direction == "" {
		switch {
		case v["gateway_name"] != "" && v["sip_authorized"] != "true", v["context"] == "public":
			direction = "inbound"
		case v["sip_authorized"] == "true":
			direction = "local"
		default:
			direction = e.Get("Call-Direction")
		}
	}
	bridged := v["bridge_uuid"] != "" || v["last_bridge_to"] != ""
	// RingQswitch picks up a call itself to play a queue's music, an IVR or
	// a voicemail greeting: that is not someone answering. A caller who
	// joined a queue that no agent answered for (and who was not put through
	// to anyone afterwards) missed the call; one who leaves a message
	// reached voicemail.
	set := func(k string) bool { return v[k] != "" && v[k] != "0" }
	queueLeft := set("cc_queue_joined_epoch") && !set("cc_queue_answered_epoch") && !bridged
	missed := (!answered || queueLeft) && !bridged && cause != "LOSE_RACE" && direction != "outbound" && leg == "a"
	status := "failed"
	switch {
	case v["voicemail_message"] == "true":
		status = "voicemail"
	case queueLeft:
		status = "missed"
	case answered:
		status = "answered"
	case cause == "ORIGINATOR_CANCEL":
		status = "cancelled"
		if direction != "outbound" {
			status = "missed"
		}
	case cause == "USER_BUSY":
		status = "busy"
	case missed:
		status = "missed"
	}
	waitsec := waitSeconds(v, answered)
	domainUUID := v["domain_uuid"]
	if domainUUID == "" && v["domain_name"] != "" {
		domainUUID = w.domainUUID(ctx, v["domain_name"])
	}
	payload, _ := json.Marshal(map[string]any{
		"core-uuid": e.Get("Core-UUID"), "switchname": w.host, "channel_data": map[string]string{
			"state": e.Get("Channel-State"), "direction": e.Get("Call-Direction"), "state_number": "11",
		},
		"variables": v,
	})
	row := map[string]string{
		"xml_cdr_uuid": v["uuid"], "domain_uuid": domainUUID, "extension_uuid": v["extension_uuid"],
		"sip_call_id": v["sip_call_id"], "domain_name": v["domain_name"], "accountcode": v["accountcode"],
		"direction": direction, "default_language": v["default_language"], "context": v["context"],
		"caller_id_name": v["caller_id_name"], "caller_id_number": v["caller_id_number"],
		"caller_destination": firstNonEmpty(v["caller_destination"], v["destination_number"]),
		"source_number":      firstNonEmpty(v["effective_caller_id_number"], v["caller_id_number"]),
		"destination_number": v["destination_number"],
		"start_epoch":        v["start_epoch"], "start_stamp": epochStamp(v["start_epoch"]),
		"answer_epoch": v["answer_epoch"], "answer_stamp": epochStamp(v["answer_epoch"]),
		"end_epoch": v["end_epoch"], "end_stamp": epochStamp(v["end_epoch"]),
		"duration": v["duration"], "mduration": v["mduration"], "billsec": v["billsec"], "billmsec": v["billmsec"],
		"hold_accum_seconds": "0", "bridge_uuid": firstNonEmpty(v["bridge_uuid"], v["last_bridge_to"]),
		"read_codec": v["read_codec"], "read_rate": v["read_rate"], "write_codec": v["write_codec"], "write_rate": v["write_rate"],
		"remote_media_ip": v["remote_media_ip"], "network_addr": v["network_addr"],
		"record_path": v["record_path"], "record_name": v["record_name"], "record_length": v["record_length"],
		"leg": leg, "originating_leg_uuid": v["originating_leg_uuid"], "pdd_ms": v["pdd_ms"],
		"last_app": v["last_app"], "last_arg": v["last_arg"],
		"missed_call": strconv.FormatBool(missed), "waitsec": waitsec, "digits_dialed": v["digits_dialed"], "pin_number": v["pin_number"],
		"hangup_cause": cause, "hangup_cause_q850": v["hangup_cause_q850"],
		"sip_hangup_disposition": v["sip_hangup_disposition"], "status": status,
		"ring_group_uuid": v["ring_group_uuid"], "ivr_menu_uuid": v["ivr_menu_uuid"],
		"call_center_queue_uuid": v["call_center_queue_uuid"], "cc_side": v["cc_side"], "cc_queue": v["cc_queue"],
		"cc_member_uuid": v["cc_member_uuid"], "cc_queue_joined_epoch": v["cc_queue_joined_epoch"],
		"cc_queue_answered_epoch": v["cc_queue_answered_epoch"], "cc_queue_terminated_epoch": v["cc_queue_terminated_epoch"],
		"cc_queue_canceled_epoch": v["cc_queue_canceled_epoch"], "cc_cancel_reason": v["cc_cancel_reason"],
		"cc_cause": v["cc_cause"], "cc_agent": v["cc_agent_name"], "cc_agent_uuid": v["cc_agent_uuid"],
		"cc_agent_type": v["cc_agent_type"], "voicemail_message": strconv.FormatBool(v["voicemail_message"] == "true"),
		"json": string(payload), "insert_date": time.Now().Format(time.RFC3339),
	}
	if row["record_name"] == "" {
		delete(row, "record_path")
	}
	for k, val := range row {
		if strings.HasSuffix(k, "_uuid") && val != "" && !looksLikeUUID(val) {
			delete(row, k) // never let a malformed value fail the whole insert
		}
	}
	for _, table := range w.s.cfg.CDRTables {
		if !w.s.HasTable(table) {
			continue
		}
		w.insertRow(ctx, table, row, "")
	}
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}

func (w *Writer) domainUUID(ctx context.Context, name string) string {
	var id string
	if err := w.s.pool.QueryRow(ctx, `select domain_uuid::text from v_domains where lower(domain_name) = lower($1) limit 1`, name).Scan(&id); err != nil {
		return ""
	}
	return id
}

// ---------------------------------------------------------------------------
// Call centre and voicemail

func (w *Writer) epochTime(v string) string {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	return time.Unix(n, 0).Format(time.RFC3339)
}

// agentDomain resolves the domain uuid of an agent.
func (w *Writer) domainOf(ctx context.Context, name string) string {
	if name == "" {
		return ""
	}
	return w.domainUUID(ctx, name)
}

func (w *Writer) callCenter(ctx context.Context, e *core.Event) {
	now := time.Now()
	switch e.Get("CC-Action") {
	case "agent-status-change":
		agent, status, aux := e.Get("CC-Agent"), e.Get("CC-Agent-Status"), e.Get("CC-Agent-Aux")
		if !looksLikeUUID(agent) {
			return
		}
		domain := w.domainOf(ctx, e.Get("CC-Agent-Domain"))
		if w.s.HasTable("agents") {
			if n := w.execCount(ctx, `update agents set status = $2, last_status_change = $3 where name = $1`, agent, status, now.Unix()); n == 0 {
				w.insertRow(ctx, "agents", map[string]string{
					"name": agent, "instance_id": "single_box", "status": status, "state": e.Get("CC-Agent-State"),
					"last_status_change": strconv.FormatInt(now.Unix(), 10),
				}, "")
			}
		}
		if w.s.HasTable("rq_agent_state") {
			w.insertRow(ctx, "rq_agent_state", map[string]string{
				"call_center_agent_uuid": agent, "domain_uuid": domain, "agent_name": e.Get("CC-Agent-Name"),
				"extension": e.Get("CC-Agent-Extension"), "status": status, "aux_code": aux, "state": e.Get("CC-Agent-State"),
				"status_since": now.Format(time.RFC3339), "hostname": w.host, "update_date": now.Format(time.RFC3339),
			}, `delete from rq_agent_state where call_center_agent_uuid = $1::uuid`, agent)
		}
		if w.s.HasTable("rq_agent_status_log") {
			w.insertRow(ctx, "rq_agent_status_log", map[string]string{
				"log_uuid": newUUID(), "domain_uuid": domain, "call_center_agent_uuid": agent,
				"agent_name": e.Get("CC-Agent-Name"), "extension": e.Get("CC-Agent-Extension"), "status": status, "aux_code": aux,
				"previous_status": e.Get("CC-Agent-Previous-Status"), "previous_aux_code": e.Get("CC-Agent-Previous-Aux"),
				"previous_seconds": e.Get("CC-Agent-Previous-Seconds"), "source": e.Get("CC-Source"), "hostname": w.host,
				"insert_date": now.Format(time.RFC3339),
			}, "")
		}
		if w.s.HasTable("q_auxcodes_list") {
			// RingQ's AUX history table.
			label := status
			if aux != "" {
				label = aux
			}
			w.insertRow(ctx, "q_auxcodes_list", map[string]string{
				"auxcode_uuid": newUUID(), "domain_uuid": domain, "auxcode_status": label,
				"extension": e.Get("CC-Agent-Extension"), "username": e.Get("CC-Agent-Name"), "insert_date": now.Format(time.RFC3339),
			}, "")
		}
	case "agent-state-change":
		agent := e.Get("CC-Agent")
		if w.s.HasTable("agents") && looksLikeUUID(agent) {
			w.exec(ctx, `update agents set state = $2 where name = $1`, agent, e.Get("CC-Agent-State"))
		}
		if w.s.HasTable("rq_agent_state") && looksLikeUUID(agent) {
			w.exec(ctx, `update rq_agent_state set state = $2, update_date = now() where call_center_agent_uuid = $1::uuid`, agent, e.Get("CC-Agent-State"))
		}
	case "member-queue-start":
		if w.s.HasTable("members") {
			w.insertRow(ctx, "members", map[string]string{
				"queue": e.Get("CC-Queue"), "instance_id": "single_box", "uuid": e.Get("CC-Member-UUID"),
				"session_uuid": e.Get("CC-Member-Session-UUID"), "cid_number": e.Get("CC-Member-CID-Number"),
				"cid_name": e.Get("CC-Member-CID-Name"), "system_epoch": strconv.FormatInt(now.Unix(), 10),
				"joined_epoch": e.Get("CC-Member-Joined-Time"), "base_score": "0", "skill_score": "0", "state": "Waiting",
			}, `delete from members where uuid = $1`, e.Get("CC-Member-UUID"))
		}
	case "bridge-agent-start":
		if w.s.HasTable("members") {
			w.exec(ctx, `update members set state = 'Answered', serving_agent = $2, bridge_epoch = $3 where uuid = $1`,
				e.Get("CC-Member-UUID"), e.Get("CC-Agent"), now.Unix())
		}
	case "member-queue-end":
		member := e.Get("CC-Member-UUID")
		if w.s.HasTable("members") {
			w.exec(ctx, `delete from members where uuid = $1`, member)
		}
		domain := w.domainOf(ctx, e.Get("CC-Queue-Domain"))
		joined := e.Get("CC-Member-Joined-Time")
		left := e.Get("CC-Member-Leaving-Time")
		answered := e.Get("CC-Agent-Answered-Time")
		result := e.Get("RQ-Result")
		if w.s.HasTable("rq_queue_calls") && looksLikeUUID(member) {
			ext := e.Get("CC-Queue")
			if i := strings.IndexByte(ext, '@'); i > 0 {
				ext = ext[:i]
			}
			w.insertRow(ctx, "rq_queue_calls", map[string]string{
				"queue_call_uuid": member, "domain_uuid": domain, "call_center_queue_uuid": e.Get("CC-Queue-UUID"),
				"call_uuid": e.Get("CC-Member-Session-UUID"), "queue_name": e.Get("CC-Queue-Name"), "queue_extension": ext,
				"caller_id_name": e.Get("CC-Member-CID-Name"), "caller_id_number": e.Get("CC-Member-CID-Number"),
				"joined_at": w.epochTime(joined), "answered_at": w.epochTime(answered), "ended_at": w.epochTime(left),
				"wait_seconds": e.Get("RQ-Wait-Seconds"), "talk_seconds": e.Get("RQ-Talk-Seconds"), "result": result,
				"call_center_agent_uuid": e.Get("CC-Agent"), "agent_name": e.Get("CC-Agent-Name"),
				"agent_extension": e.Get("CC-Agent-Extension"), "agents_offered": e.Get("RQ-Agents-Offered"),
				"no_answer_count": e.Get("RQ-No-Answer"), "hostname": w.host,
			}, "")
		}
		if w.s.HasTable("q_call_queues_list") && looksLikeUUID(member) {
			// RingQ's queue call log.
			w.insertRow(ctx, "q_call_queues_list", map[string]string{
				"domain_uuid": domain, "queue_uuid": e.Get("CC-Queue-UUID"), "call_uuid": member,
				"caller_id_number": e.Get("CC-Member-CID-Number"), "caller_id_name": e.Get("CC-Member-CID-Name"),
				"callee_id_number": e.Get("CC-Agent-Extension"), "callee_id_name": e.Get("CC-Agent-Name"),
				"call_direction": "inbound", "call_type": result, "call_duration": e.Get("RQ-Talk-Seconds"),
				"call_start_time": joined, "call_answered_time": answered, "call_waiting_time": e.Get("RQ-Wait-Seconds"),
				"call_end_time": left, "call_time_to_answer": e.Get("RQ-Wait-Seconds"), "insert_date": now.Format(time.RFC3339),
			}, "")
		}
	}
}

// userVars saves the DND and forwarding an extension changed from its
// phone (*78, the DND key...) or the apps, in v_extensions.
func (w *Writer) userVars(ctx context.Context, e *core.Event) {
	if !w.s.HasTable("v_extensions") {
		return
	}
	dom := w.domainUUID(ctx, e.Get("Domain"))
	if dom == "" {
		return
	}
	set := map[string]string{}
	for _, h := range e.Headers {
		if col, ok := strings.CutPrefix(h[0], "Var-"); ok {
			set[col] = h[1]
		}
	}
	for _, col := range []string{"do_not_disturb", "forward_all_enabled", "forward_all_destination", "forward_busy_enabled",
		"forward_busy_destination", "forward_no_answer_enabled", "forward_no_answer_destination"} {
		v, ok := set[col]
		if !ok || !w.s.HasColumn("v_extensions", col) {
			continue
		}
		w.exec(ctx, fmt.Sprintf(`update v_extensions set %s = %s, update_date = now() where domain_uuid = $1::uuid and extension = $2`,
			col, w.cast("v_extensions", col, 3)), dom, e.Get("User"), v)
	}
}

func (w *Writer) voicemail(ctx context.Context, e *core.Event) {
	if e.Get("VM-Action") != "leave-message" || !w.s.HasTable("v_voicemail_messages") {
		return
	}
	domain := w.domainOf(ctx, e.Get("VM-Domain"))
	w.insertRow(ctx, "v_voicemail_messages", map[string]string{
		"domain_uuid": domain, "voicemail_message_uuid": e.Get("VM-Message-UUID"), "voicemail_uuid": e.Get("VM-UUID"),
		"created_epoch": e.Get("VM-Created"), "caller_id_name": e.Get("VM-Caller-ID-Name"),
		"caller_id_number": e.Get("VM-Caller-ID-Number"), "message_length": e.Get("VM-Message-Len"),
		"message_status": "", "message_priority": "normal", "insert_date": time.Now().Format(time.RFC3339),
	}, "")
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (w *Writer) execCount(ctx context.Context, sql string, args ...any) int64 {
	tag, err := w.s.pool.Exec(ctx, sql, args...)
	if err != nil {
		if n := w.errs.Add(1); n <= 20 || n%100 == 0 {
			w.s.log.Warn("database write failed", "err", err, "failures", n)
		}
		return -1
	}
	w.written.Add(1)
	return tag.RowsAffected()
}

// syncCallCenterTables mirrors the agents and tiers of the configuration
// into the call centre's agents and tiers tables, which RingQ-style
// panels and wallboards read.
func (w *Writer) syncCallCenterTables(ctx context.Context) {
	if w.sw == nil {
		return
	}
	cfg := w.sw.Config()
	if w.s.HasTable("agents") {
		live := map[string]core.AgentInfo{}
		for _, a := range w.sw.Agents("") {
			live[strings.ToLower(a.UUID)] = a
		}
		var keep []string
		for id, a := range cfg.Features.Agents {
			keep = append(keep, a.UUID)
			info := live[id]
			status, state := firstNonEmpty(info.Status, a.Status), firstNonEmpty(info.State, "Waiting")
			row := map[string]string{
				"name": a.UUID, "instance_id": "single_box", "type": firstNonEmpty(a.Type, "callback"), "contact": a.Contact,
				"status": status, "state": state, "max_no_answer": strconv.Itoa(a.MaxNoAnswer),
				"wrap_up_time": strconv.Itoa(int(a.WrapUpTime.Seconds())), "reject_delay_time": strconv.Itoa(int(a.RejectDelay.Seconds())),
				"busy_delay_time": strconv.Itoa(int(a.BusyDelay.Seconds())), "no_answer_delay_time": strconv.Itoa(int(a.NoAnswerDelay.Seconds())),
				"last_status_change": strconv.FormatInt(info.StatusSince, 10), "calls_answered": strconv.Itoa(info.CallsAnswered),
				"talk_time": strconv.Itoa(info.TalkSeconds), "no_answer_count": strconv.Itoa(info.NoAnswerCount),
			}
			w.insertRow(ctx, "agents", row, `delete from agents where name = $1`, a.UUID)
		}
		if len(keep) > 0 {
			w.exec(ctx, `delete from agents where instance_id = 'single_box' and not (name = any($1))`, keep)
		} else {
			w.exec(ctx, `delete from agents where instance_id = 'single_box'`)
		}
	}
	if w.s.HasTable("tiers") {
		w.exec(ctx, `delete from tiers`)
		for _, t := range cfg.Features.Tiers {
			q := cfg.Features.Queues[strings.ToLower(t.QueueUUID)]
			if q == nil {
				continue
			}
			w.insertRow(ctx, "tiers", map[string]string{"queue": q.QueueID(), "agent": t.AgentUUID, "state": "Ready",
				"level": strconv.Itoa(t.Level), "position": strconv.Itoa(t.Position)}, "")
		}
	}
}

// ---------------------------------------------------------------------------
// Voicemail by phone (core.VoicemailStore)

// VoicemailMessages lists a mailbox's messages, oldest first.
func (s *Store) VoicemailMessages(ctx context.Context, voicemailUUID string) ([]core.VoicemailMessage, error) {
	if !s.HasTable("v_voicemail_messages") {
		return nil, fmt.Errorf("no v_voicemail_messages table")
	}
	rows, err := s.pool.Query(ctx, `select voicemail_message_uuid::text, coalesce(caller_id_number, ''), coalesce(caller_id_name, ''),
		coalesce(created_epoch::bigint, 0), coalesce(message_length::bigint, 0), coalesce(message_status, '')
		from v_voicemail_messages where voicemail_uuid = $1::uuid order by created_epoch`, voicemailUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.VoicemailMessage
	for rows.Next() {
		var m core.VoicemailMessage
		var created, secs int64
		var status string
		if err := rows.Scan(&m.UUID, &m.CallerNumber, &m.CallerName, &created, &secs, &status); err != nil {
			return nil, err
		}
		m.Created, m.Seconds, m.New = time.Unix(created, 0), int(secs), status == ""
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveVoicemailMessage marks a message as heard.
func (s *Store) SaveVoicemailMessage(ctx context.Context, messageUUID string) error {
	_, err := s.pool.Exec(ctx, `update v_voicemail_messages set message_status = 'saved' where voicemail_message_uuid = $1::uuid`, messageUUID)
	return err
}

// DeleteVoicemailMessage removes a message's record (the caller removes the file).
func (s *Store) DeleteVoicemailMessage(ctx context.Context, messageUUID string) error {
	_, err := s.pool.Exec(ctx, `delete from v_voicemail_messages where voicemail_message_uuid = $1::uuid`, messageUUID)
	return err
}

// guardEvent keeps SIP Guard's bans (sip_security_blocks, one row per
// address and protection, restored at start) and its history
// (security_audit_log).
func (w *Writer) guardEvent(ctx context.Context, e *core.Event) {
	ip, jail, action := e.Get("Network-IP"), e.Get("Jail"), e.Get("Action")
	if ip == "" || jail == "" {
		return
	}
	meta, _ := json.Marshal(map[string]string{"until": e.Get("Ban-Until"), "since": e.Get("Ban-Since"), "actor": e.Get("Actor"),
		"source": e.Get("Source")})
	since, _ := strconv.ParseInt(e.Get("Ban-Since"), 10, 64)
	if since == 0 {
		since = time.Now().Unix()
	}
	if w.s.HasTable("sip_security_blocks") {
		switch action {
		case "ban":
			w.exec(ctx, `insert into sip_security_blocks (ip_address, category, reason, failure_count, first_seen_at, last_seen_at,
				blocked_at, status, source, jail_name, metadata) values ($1::inet, $2, $3, $4, to_timestamp($5), now(), to_timestamp($5),
				'banned', $6, $2, $7::jsonb) on conflict (ip_address, category) do update set reason = excluded.reason,
				failure_count = sip_security_blocks.failure_count + excluded.failure_count, last_seen_at = now(),
				blocked_at = excluded.blocked_at, unblocked_at = null, status = 'banned', source = excluded.source,
				jail_name = excluded.jail_name, metadata = excluded.metadata`,
				ip, jail, firstNonEmpty(e.Get("Reason"), jail), atoi(e.Get("Failures"), 0), since, firstNonEmpty(e.Get("Source"), "auto"), string(meta))
		case "unban", "expire":
			status := map[string]string{"unban": "unbanned", "expire": "expired"}[action]
			w.exec(ctx, `update sip_security_blocks set status = $3, unblocked_at = now() where ip_address = $1::inet and category = $2
				and status = 'banned'`, ip, jail, status)
		}
	}
	if w.s.HasTable("security_audit_log") {
		w.exec(ctx, `insert into security_audit_log (actor, action, ip_address, category, reason, metadata)
			values ($1, $2, $3::inet, $4, $5, $6::jsonb)`, truncate(e.Get("Actor"), 64), action, ip, jail, e.Get("Reason"), string(meta))
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
