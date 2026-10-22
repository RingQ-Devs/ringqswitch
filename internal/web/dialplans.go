package web

import (
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Cal4Care-Developers/RingQswitch/internal/db"
)

// Dialplans: the web interface stores call routing the way RingQ does,
// as <extension> XML in v_dialplans, so RingQswitch (and any other switch, if it
// is ever used again) routes the calls.

func xa(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), `"`, "&#34;")
}

// action turns a destination value "app:data" (e.g. "transfer:1001 XML
// example.com", "hangup:USER_BUSY") into an <action> element.
func action(v string) string {
	app, data, _ := strings.Cut(strings.TrimSpace(v), ":")
	if app == "" {
		return ""
	}
	return fmt.Sprintf(`    <action application="%s" data="%s"/>`+"\n", xa(app), xa(data))
}

func numberRegex(n string) string {
	return "^" + regexp.QuoteMeta(n) + "$"
}

type dialplanRow struct {
	uuid        string
	domainUUID  string // "" = all domains
	context     string
	name        string
	number      string
	order       int
	enabled     bool
	continueOn  bool
	xml         string
	description string
}

func upsertDialplan(ctx context.Context, tx pgx.Tx, st *db.Store, d dialplanRow) error {
	if _, err := tx.Exec(ctx, `delete from v_dialplans where dialplan_uuid = $1::uuid`, d.uuid); err != nil {
		return err
	}
	row := map[string]string{
		"dialplan_uuid": d.uuid, "domain_uuid": d.domainUUID, "dialplan_context": d.context, "dialplan_name": d.name,
		"dialplan_number": d.number, "dialplan_order": strconv.Itoa(d.order), "dialplan_enabled": strconv.FormatBool(d.enabled),
		"dialplan_continue": strconv.FormatBool(d.continueOn), "dialplan_xml": d.xml, "dialplan_description": d.description,
		"insert_date": time.Now().Format(time.RFC3339),
	}
	return insertRow(ctx, tx, st, "v_dialplans", row)
}

func deleteDialplan(ctx context.Context, tx pgx.Tx, uuid string) error {
	if uuid == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `delete from v_dialplans where dialplan_uuid = $1::uuid`, uuid)
	return err
}

// ---------------------------------------------------------------------------
// Generators

// didPattern matches a DID as carriers send it: with or without +, and
// with or without its country's calling code when the country is set
// (65 + 61234567: +6561234567, 6561234567 and 61234567).
func didPattern(number, prefix string) string {
	num := strings.TrimPrefix(strings.TrimSpace(number), "+")
	prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "+")
	if prefix == "" {
		return `^\+?` + regexp.QuoteMeta(num) + `$`
	}
	local := strings.TrimPrefix(num, prefix)
	if local == "" {
		local = num
	}
	return `^(?:\+?` + regexp.QuoteMeta(prefix) + `)?` + regexp.QuoteMeta(local) + `$`
}

func didDialplan(dom domainRow, r map[string]string) string {
	if r[didFaxExt] != "" {
		return didFaxDialplan(dom, r)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<extension name="%s" continue="false">`+"\n", xa(r["destination_number"]))
	if gw := r["gateway_uuid"]; gw != "" {
		fmt.Fprintf(&b, `  <condition field="${gateway_uuid}" expression="^%s$"/>`+"\n", xa(regexp.QuoteMeta(gw)))
	}
	if cid := strings.TrimSpace(r["caller_id_filter"]); cid != "" {
		fmt.Fprintf(&b, `  <condition field="caller_id_number" expression="%s"/>`+"\n", xa(cid))
	}
	fmt.Fprintf(&b, `  <condition field="destination_number" expression="%s">`+"\n", xa(didPattern(r["destination_number"], r["destination_prefix"])))
	b.WriteString(`    <action application="export" data="call_direction=inbound" inline="true"/>` + "\n")
	fmt.Fprintf(&b, `    <action application="set" data="domain_uuid=%s" inline="true"/>`+"\n", xa(dom.uuid))
	fmt.Fprintf(&b, `    <action application="set" data="domain_name=%s" inline="true"/>`+"\n", xa(dom.name))
	fmt.Fprintf(&b, `    <action application="set" data="caller_destination=%s" inline="true"/>`+"\n", xa(r["destination_number"]))
	if p := r["destination_cid_name_prefix"]; p != "" {
		fmt.Fprintf(&b, `    <action application="set" data="effective_caller_id_name=%s#${caller_id_name}"/>`+"\n", xa(p))
	}
	if truthy(r["destination_record"]) {
		b.WriteString(`    <action application="record_session" data="${recordings_dir}/${domain_name}/archive/${strftime(%Y)}/${strftime(%b)}/${strftime(%d)}/${uuid}.wav"/>` + "\n")
	}
	b.WriteString(action(r["action"]))
	b.WriteString("  </condition>\n</extension>\n")
	return b.String()
}

// Row keys (not columns) saveDialplan sets on a DID that goes to a fax
// extension (all three), or to eFax for an extension (the extension only).
const (
	didFaxKey   = "fax_target_uuid"
	didFaxExt   = "fax_target_extension"
	didFaxIdent = "fax_target_ident"
	didFaxSkip  = "fax_target_skip" // a fax extension being deleted
)

// didFaxDialplan: a DID assigned to a fax extension, or to eFax for an
// extension, answers as a fax machine itself, like the fax DIDs of RingQ on
// RingQ.
func didFaxDialplan(dom domainRow, r map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<extension name="%s" continue="false" uuid="%s">`+"\n", xa(r["destination_number"]), xa(r["dialplan_uuid"]))
	if gw := r["gateway_uuid"]; gw != "" {
		fmt.Fprintf(&b, `  <condition field="${gateway_uuid}" expression="^%s$"/>`+"\n", xa(regexp.QuoteMeta(gw)))
	}
	if cid := strings.TrimSpace(r["caller_id_filter"]); cid != "" {
		fmt.Fprintf(&b, `  <condition field="caller_id_number" expression="%s"/>`+"\n", xa(cid))
	}
	expr := `^((\+)?` + regexp.QuoteMeta(strings.TrimPrefix(r["destination_number"], "+")) + `)$`
	if r["destination_prefix"] != "" {
		expr = didPattern(r["destination_number"], r["destination_prefix"])
	}
	fmt.Fprintf(&b, `  <condition field="${destination_number}" expression="%s">`+"\n", xa(expr))
	b.WriteString(`    <action application="export" data="call_direction=inbound" inline="true"/>` + "\n")
	fmt.Fprintf(&b, `    <action application="set" data="domain_name=%s" inline="true"/>`+"\n", xa(dom.name))
	fmt.Fprintf(&b, `    <action application="set" data="caller_destination=%s" inline="true"/>`+"\n", xa(r["destination_number"]))
	b.WriteString(faxActions(dom, r[didFaxKey], r[didFaxExt], firstNonEmpty(r[didFaxIdent], r["destination_number"])))
	b.WriteString("  </condition>\n</extension>\n")
	return b.String()
}

func routeDialplan(dom domainRow, r map[string]string) string {
	strip := atoi(r["route_strip"], 0)
	number := xa(r["route_prepend"]) + "${destination_number:" + strconv.Itoa(strip) + "}"
	if strip == 0 {
		number = xa(r["route_prepend"]) + "${destination_number}"
	}
	var legs []string
	for _, gw := range strings.Split(r["gateway_uuids"], ",") {
		if gw = strings.TrimSpace(gw); gw != "" {
			legs = append(legs, "ringq/gateway/"+xa(gw)+"/"+number)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<extension name="%s" continue="false">`+"\n", xa(r["route_name"]))
	b.WriteString(`  <condition field="${user_exists}" expression="^false$"/>` + "\n")
	if f := callerFilter(r); f != "" { // only calls from these extensions
		fmt.Fprintf(&b, `  <condition field="${user_name}" expression="%s"/>`+"\n", xa(f))
	}
	fmt.Fprintf(&b, `  <condition field="destination_number" expression="%s">`+"\n", xa(r["route_pattern"]))
	b.WriteString(`    <action application="export" data="call_direction=outbound" inline="true"/>` + "\n")
	if truthy(r["route_block"]) {
		b.WriteString(`    <action application="hangup" data="CALL_REJECTED"/>` + "\n")
		b.WriteString("  </condition>\n</extension>\n")
		return b.String()
	}
	b.WriteString(`    <action application="unset" data="call_timeout"/>` + "\n")
	b.WriteString(`    <action application="set" data="hangup_after_bridge=true"/>` + "\n")
	cidName := firstNonEmpty(r["caller_id_name"], "${outbound_caller_id_name}")
	cidNum := firstNonEmpty(r["caller_id_number"], "${outbound_caller_id_number}")
	fmt.Fprintf(&b, `    <action application="set" data="effective_caller_id_name=%s"/>`+"\n", xa(cidName))
	fmt.Fprintf(&b, `    <action application="set" data="effective_caller_id_number=%s"/>`+"\n", xa(cidNum))
	// How the call presents itself to the carrier (for the gateway leg only).
	if v := ruleFromUser[r["sip_from_user"]]; v != "" {
		fmt.Fprintf(&b, `    <action application="export" data="nolocal:sip_from_user=%s"/>`+"\n", v)
	}
	if v := ruleFromDisplay[r["sip_from_display"]]; v != "" {
		fmt.Fprintf(&b, `    <action application="export" data="nolocal:sip_from_display=%s"/>`+"\n", v)
	}
	if r["sip_from_host"] == "domain" {
		fmt.Fprintf(&b, `    <action application="export" data="nolocal:sip_from_host=%s"/>`+"\n", xa(dom.name))
	}
	if t := r["cid_type"]; t != "" {
		fmt.Fprintf(&b, `    <action application="export" data="nolocal:sip_cid_type=%s"/>`+"\n", xa(t))
	}
	b.WriteString(`    <action application="set" data="continue_on_fail=1,2,3,6,25,34,38,41,42,44,58,63,88,111,403,501,602,607"/>` + "\n")
	fmt.Fprintf(&b, `    <action application="bridge" data="%s"/>`+"\n", strings.Join(legs, "|"))
	b.WriteString("  </condition>\n</extension>\n")
	return b.String()
}

func ringGroupDialplan(dom domainRow, r map[string]string) string {
	return fmt.Sprintf(`<extension name="%s" continue="false">
  <condition field="destination_number" expression="%s">
    <action application="ring_ready" data=""/>
    <action application="set" data="ring_group_uuid=%s"/>
    <action application="lua" data="app.lua ring_groups"/>
  </condition>
</extension>
`, xa(r["ring_group_name"]), xa(numberRegex(r["ring_group_extension"])), xa(r["ring_group_uuid"]))
}

func ivrDialplan(dom domainRow, r map[string]string) string {
	return fmt.Sprintf(`<extension name="%s" continue="false">
  <condition field="destination_number" expression="%s">
    <action application="ring_ready" data=""/>
    <action application="answer" data=""/>
    <action application="sleep" data="500"/>
    <action application="set" data="hangup_after_bridge=true"/>
    <action application="set" data="ivr_menu_uuid=%s"/>
    <action application="ivr" data="%s"/>
    <action application="hangup" data=""/>
  </condition>
</extension>
`, xa(r["ivr_menu_name"]), xa(numberRegex(r["ivr_menu_extension"])), xa(r["ivr_menu_uuid"]), xa(r["ivr_menu_uuid"]))
}

// aiAgentDialplan answers the AI agent's number and hands the call to the
// agent (the ai_agent application); when the agent transfers the call the
// dialplan of the new destination runs instead of the hangup.
func aiAgentDialplan(dom domainRow, r map[string]string) string {
	return fmt.Sprintf(`<extension name="%s" continue="false">
  <condition field="destination_number" expression="%s">
    <action application="set" data="ai_agent_uuid=%s"/>
    <action application="set" data="hangup_after_bridge=true"/>
    <action application="ai_agent" data="%s"/>
    <action application="hangup" data=""/>
  </condition>
</extension>
`, xa(r["agent_name"]), xa(numberRegex(r["agent_extension"])), xa(r["ai_agent_uuid"]), xa(r["ai_agent_uuid"]))
}

func queueDialplan(dom domainRow, r map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<extension name="%s" continue="false">`+"\n", xa(r["queue_name"]))
	fmt.Fprintf(&b, `  <condition field="destination_number" expression="%s">`+"\n", xa(numberRegex(r["queue_extension"])))
	b.WriteString(`    <action application="answer" data=""/>` + "\n")
	b.WriteString(`    <action application="set" data="hangup_after_bridge=true"/>` + "\n")
	if k := r["queue_cc_exit_keys"]; k != "" {
		fmt.Fprintf(&b, `    <action application="set" data="cc_exit_keys=%s"/>`+"\n", xa(k))
	}
	fmt.Fprintf(&b, `    <action application="set" data="call_center_queue_uuid=%s"/>`+"\n", xa(r["call_center_queue_uuid"]))
	fmt.Fprintf(&b, `    <action application="callcenter" data="%s@%s"/>`+"\n", xa(r["queue_extension"]), xa(dom.name))
	b.WriteString(action(r["queue_timeout_action"]))
	b.WriteString("  </condition>\n</extension>\n")
	return b.String()
}

func conferenceDialplan(dom domainRow, r map[string]string) string {
	return fmt.Sprintf(`<extension name="%s" continue="false">
  <condition field="destination_number" expression="%s">
    <action application="answer" data=""/>
    <action application="conference" data="%s@default"/>
  </condition>
</extension>
`, xa(r["conference_name"]), xa(numberRegex(r["conference_extension"])), xa(r["conference_extension"]))
}

// faxActions answer the call as a fax machine, in the form RingQ's fax DIDs
// use on RingQ; the fax lands in the eFax inbox (fax-dir/<domain>/inbox)
// and the eFax worker files it (fax_jobs) from the call's events. The T.38
// and ECM variables are kept for the RingQ portal; RingQswitch faxes as audio.
func faxActions(dom domainRow, faxUUID, faxExt, ident string) string {
	var b strings.Builder
	act := func(app, data string) {
		fmt.Fprintf(&b, `    <action application="%s" data="%s"/>`+"\n", app, xa(data))
	}
	act("set", "fax_direction=in")
	act("set", "domain_uuid="+dom.uuid)
	if faxUUID != "" {
		act("set", "fax_uuid="+faxUUID)
	}
	if faxExt != "" {
		act("set", "fax_extension="+faxExt)
	}
	if ident != "" {
		act("set", "fax_ident="+ident)
	}
	act("set", "fax_verbose=true")
	act("set", "fax_enable_t38=true")
	act("set", "fax_enable_t38_request=false")
	act("set", "fax_enable_ecm=true")
	act("set", "ignore_early_media=true")
	act("set", "fax_file=${fax_dir}/"+dom.uuid+"/inbox/${uuid}.pdf")
	act("answer", "")
	act("stop_record_session", "${record_path}/${record_name}")
	act("rxfax", "${fax_file}")
	act("hangup", "")
	return b.String()
}

// faxDialplan: the fax extension's own number (dialled inside the domain, or
// a DID transferred to it) answers as a fax machine.
func faxDialplan(dom domainRow, r map[string]string) string {
	return fmt.Sprintf(`<extension name="fax %s" continue="false" uuid="%s">
  <condition field="destination_number" expression="%s">
%s  </condition>
</extension>
`, xa(r["fax_name"]), xa(r["dialplan_uuid"]), xa(numberRegex(r["fax_extension"])),
		faxActions(dom, r["fax_uuid"], r["fax_extension"], firstNonEmpty(r["fax_caller_id_number"], r["fax_extension"])))
}

// weekday names -> dialplan wday numbers (1 = Sunday).
var wdayNumbers = map[string]string{"sun": "1", "mon": "2", "tue": "3", "wed": "4", "thu": "5", "fri": "6", "sat": "7"}

func timeConditionDialplan(dom domainRow, r map[string]string) string {
	num := xa(numberRegex(r["condition_extension"]))
	var b strings.Builder
	var ranges []string
	for _, d := range strings.Split(r["holidays"], ",") {
		d = strings.TrimSpace(d)
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		ranges = append(ranges, t.Format("2006-01-02")+" 00:00~"+t.AddDate(0, 0, 1).Format("2006-01-02")+" 00:00")
	}
	if len(ranges) > 0 {
		fmt.Fprintf(&b, `<extension name="%s (holidays)" continue="false">`+"\n", xa(r["condition_name"]))
		fmt.Fprintf(&b, `  <condition field="destination_number" expression="%s"/>`+"\n", num)
		fmt.Fprintf(&b, `  <condition date-time="%s">`+"\n", xa(strings.Join(ranges, ",")))
		b.WriteString(action(r["nomatch_action"]))
		b.WriteString("  </condition>\n</extension>\n")
	}
	var days []string
	for _, d := range strings.Split(strings.ToLower(r["weekdays"]), ",") {
		if n, ok := wdayNumbers[strings.TrimSpace(d)]; ok {
			days = append(days, n)
		}
	}
	attrs := ""
	if len(days) > 0 && len(days) < 7 {
		attrs += fmt.Sprintf(` wday="%s"`, strings.Join(days, ","))
	}
	if r["time_from"] != "" && r["time_to"] != "" {
		attrs += fmt.Sprintf(` time-of-day="%s-%s"`, xa(r["time_from"]), xa(r["time_to"]))
	}
	fmt.Fprintf(&b, `<extension name="%s" continue="false">`+"\n", xa(r["condition_name"]))
	fmt.Fprintf(&b, `  <condition field="destination_number" expression="%s"/>`+"\n", num)
	fmt.Fprintf(&b, `  <condition%s>`+"\n", attrs)
	b.WriteString(action(r["match_action"]))
	b.WriteString(strings.Replace(action(r["nomatch_action"]), "<action ", "<anti-action ", 1))
	b.WriteString("  </condition>\n</extension>\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Default dialplans (every domain)

type baseDialplan struct {
	name, number string
	order        int
	continueOn   bool
	xml          string
	description  string
}

const recordPath = "${recordings_dir}/${domain_name}/archive/${strftime(%Y)}/${strftime(%b)}/${strftime(%d)}/${uuid}.wav"

var baseDialplans = []baseDialplan{
	{"call-direction", "", 15, true, `<extension name="call-direction" continue="true">
  <condition field="${call_direction}" expression="^(inbound|outbound|local)$" break="never">
    <anti-action application="export" data="call_direction=local" inline="true"/>
  </condition>
</extension>`, "sets call_direction"},
	{"user_exists", "", 20, true, `<extension name="user_exists" continue="true">
  <condition field="" expression="">
    <action application="set" data="user_exists=${user_exists id ${destination_number} ${domain_name}}" inline="true"/>
  </condition>
  <condition field="${user_exists}" expression="^true$">
    <action application="set" data="extension_uuid=${user_data ${destination_number}@${domain_name} var extension_uuid}" inline="true"/>
  </condition>
</extension>`, "looks the dialled number up in the directory"},
	{"user_record", "", 25, true, `<extension name="user_record" continue="true">
  <condition field="${user_record}" expression="^(all|outbound|local)$" break="never">
    <action application="set" data="rq_record_call=true" inline="true"/>
  </condition>
  <condition field="${user_exists}" expression="^true$" break="never">
    <action application="set" data="rq_callee_record=${user_data ${destination_number}@${domain_name} var user_record}" inline="true"/>
  </condition>
  <condition field="${rq_callee_record}" expression="^(all|inbound|local)$" break="never">
    <action application="set" data="rq_record_call=true" inline="true"/>
  </condition>
  <condition field="${rq_record_call}" expression="^true$">
    <action application="record_session" data="` + recordPath + `"/>
  </condition>
</extension>`, "records calls of extensions with call recording on"},
	{"variables", "", 30, true, `<extension name="variables" continue="true">
  <condition field="" expression="">
    <action application="set" data="call_timeout=30"/>
    <action application="set" data="hangup_after_bridge=true"/>
  </condition>
</extension>`, "default call variables"},
	{"agent-login", "*22", 40, false, `<extension name="agent-login" continue="false">
  <condition field="destination_number" expression="^\*22$">
    <action application="rq_agent_status" data="toggle"/>
  </condition>
</extension>`, "*22: call-centre agent log in / log out"},
	{"agent-break", "*23", 41, false, `<extension name="agent-break" continue="false">
  <condition field="destination_number" expression="^\*23$">
    <action application="rq_agent_status" data="break-toggle"/>
  </condition>
</extension>`, "*23: call-centre agent break on / off"},
	{"voicemail", "*99", 50, false, `<extension name="voicemail" continue="false">
  <condition field="destination_number" expression="^\*99(\d{2,10})$">
    <action application="answer" data=""/>
    <action application="voicemail" data="default ${domain_name} $1"/>
  </condition>
</extension>`, "*99<ext>: leave a voicemail"},
	{"voicemail-check", "*97", 51, false, `<extension name="voicemail-check" continue="false">
  <condition field="destination_number" expression="^\*97$">
    <action application="answer" data=""/>
    <action application="voicemail" data="check default ${domain_name}"/>
  </condition>
</extension>`, "*97: one beep per new voicemail (listen in the web interface)"},
	{"park", "*59xx", 52, false, `<extension name="park" continue="false">
  <condition field="destination_number" expression="^(\*59\d\d)$">
    <action application="valet_park" data="${domain_name} $1"/>
  </condition>
</extension>`, "*5901-*5999: park / retrieve a call"},
	{"pickup", "**", 53, false, `<extension name="pickup" continue="false">
  <condition field="destination_number" expression="^\*\*(\d+)$">
    <action application="rq_pickup" data="$1"/>
  </condition>
</extension>`, "**<ext>: answer a call ringing that extension"},
	{"group-pickup", "*8", 54, false, `<extension name="group-pickup" continue="false">
  <condition field="destination_number" expression="^\*8$">
    <action application="rq_pickup" data="group"/>
  </condition>
</extension>`, "*8: answer a call ringing your call group"},
	{"intercom", "*80", 55, false, `<extension name="intercom" continue="false">
  <condition field="destination_number" expression="^\*80(\d+)$">
    <action application="set" data="sip_h_Call-Info=&lt;sip:${domain_name}&gt;;answer-after=0"/>
    <action application="set" data="sip_h_Alert-Info=info=alert-autoanswer"/>
    <action application="bridge" data="user/$1@${domain_name}"/>
  </condition>
</extension>`, "*80<ext>: intercom (the phone answers by itself)"},
	{"echo", "9196", 60, false, `<extension name="echo" continue="false">
  <condition field="destination_number" expression="^9196$">
    <action application="answer"/>
    <action application="echo"/>
  </condition>
</extension>`, "9196: echo test"},
	{"tone", "9197", 61, false, `<extension name="tone" continue="false">
  <condition field="destination_number" expression="^9197$">
    <action application="answer"/>
    <action application="playback" data="tone_stream://%(1000,0,1004);loops=-1"/>
  </condition>
</extension>`, "9197: 1 kHz test tone"},
	{"local_extension", "[ext]", 999, true, `<extension name="local_extension" continue="true">
  <condition field="${user_exists}" expression="true">
    <action application="export" data="dialed_extension=${destination_number}" inline="true"/>
  </condition>
  <condition field="" expression="">
    <action application="set" data="hangup_after_bridge=true"/>
    <action application="set" data="continue_on_fail=true"/>
    <action application="hash" data="insert/${domain_name}-last_dial_ext/${dialed_extension}/${uuid}"/>
    <action application="bridge" data="user/${destination_number}@${domain_name}"/>
    <action application="lua" data="app.lua failure_handler"/>
  </condition>
</extension>`, "calls between extensions; busy / no answer forwarding and voicemail"},
}

var defaultAuxCodes = []struct {
	name  string
	block bool
	order int
}{{"Break", false, 10}, {"Lunch", false, 20}, {"Meeting", true, 30}, {"Training", true, 40}, {"Personal", false, 50}}

// InstallDefaults adds what a RingQswitch database needs and is missing:
// the RingQ tables RingQswitch uses (all of them in a new
// database), RingQswitch's rq_* tables, the default dialplans (feature
// codes, extension calls...), default AUX
// codes per domain, and dialplans for ring groups, IVR menus, queues,
// conferences, DIDs, outbound routes and time conditions that have none.
// Existing rows are never changed.
// installDialplans adds the dialplans that are missing: skipped when one
// with that name or feature code exists (a RingQ database has its own
// copies per domain).
func installDialplans(ctx context.Context, tx pgx.Tx, st *db.Store, list []baseDialplan) (int, error) {
	added := 0
	for _, d := range list {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from v_dialplans where dialplan_name = $1 or ($2 <> '' and dialplan_number = $2)`, d.name, d.number).Scan(&n); err != nil {
			return added, err
		}
		if n > 0 {
			continue
		}
		if err := upsertDialplan(ctx, tx, st, dialplanRow{uuid: newUUID(), context: "${domain_name}", name: d.name, number: d.number,
			order: d.order, enabled: true, continueOn: d.continueOn, xml: d.xml, description: "RingQswitch default: " + d.description}); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

// InstallDeskPhoneCodes adds the desk phone dial codes (DND, park slots,
// pickup, call return...) that the database does not have yet; the server
// does it at start, so an upgrade gets them.
func InstallDeskPhoneCodes(ctx context.Context, st *db.Store) (int, error) {
	if !st.HasTable("v_dialplans") {
		return 0, nil
	}
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	n, err := installDialplans(ctx, tx, st, deskPhoneDialplans)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// deskPhoneDialplans are the dial codes of the RingQ desk phone guide.
var deskPhoneDialplans = []baseDialplan{
	{"own-voicemail", "[own ext]", 44, false, `<extension name="own-voicemail" continue="false">
  <condition field="${user_name}" expression="^.+$"/>
  <condition field="${cond(${destination_number} == ${user_name} ? own : other)}" expression="^own$">
    <action application="answer" data=""/>
    <action application="voicemail" data="check default ${domain_name} ${user_name}"/>
  </condition>
</extension>`, "dialing your own extension checks your voicemail (the phone's Message key)"},
	{"call-return", "*69", 45, false, `<extension name="call-return" continue="false">
  <condition field="destination_number" expression="^\*69$">
    <action application="rq_call_return" data=""/>
  </condition>
</extension>`, "*69: call back the last caller"},
	{"redial", "*870", 46, false, `<extension name="redial" continue="false">
  <condition field="destination_number" expression="^(redial|\*870)$">
    <action application="rq_redial" data=""/>
  </condition>
</extension>`, "*870: redial the last number"},
	{"team-voicemail", "*88", 47, false, `<extension name="team-voicemail" continue="false">
  <condition field="destination_number" expression="^\*88(\d{1,10})$">
    <action application="answer" data=""/>
    <action application="voicemail" data="default ${domain_name} $1"/>
  </condition>
</extension>`, "*88<team>: leave a message in a team (group) voicemail box"},
	{"dnd-on", "*78", 48, false, `<extension name="dnd-on" continue="false">
  <condition field="destination_number" expression="^\*78$">
    <action application="rq_dnd" data="on"/>
  </condition>
</extension>`, "*78: do not disturb on"},
	{"dnd-off", "*79", 48, false, `<extension name="dnd-off" continue="false">
  <condition field="destination_number" expression="^\*79$">
    <action application="rq_dnd" data="off"/>
  </condition>
</extension>`, "*79: do not disturb off"},
	{"dnd-toggle", "*77", 48, false, `<extension name="dnd-toggle" continue="false">
  <condition field="destination_number" expression="^\*77$">
    <action application="rq_dnd" data="toggle"/>
  </condition>
</extension>`, "*77: do not disturb on / off"},
	{"agent-status-number", "*26", 49, false, `<extension name="agent-status-number" continue="false">
  <condition field="destination_number" expression="^\*26(\d{1,3})$">
    <action application="rq_agent_status" data="aux $1"/>
  </condition>
</extension>`, "*26<n>: agent status number n (1 Available, then the aux codes)"},
	{"park-slots", "*591", 52, false, `<extension name="park-slots" continue="false">
  <condition field="destination_number" expression="^(\*59[1-5])$">
    <action application="valet_park" data="${domain_name} $1"/>
  </condition>
</extension>`, "*591-*595: parking slots 1 to 5 (park during a call, pick up when idle)"},
	{"pickup-ext", "*8[ext]", 56, false, `<extension name="pickup-ext" continue="false">
  <condition field="destination_number" expression="^\*8(\d{2,10})$">
    <action application="rq_pickup" data="$1"/>
  </condition>
</extension>`, "*8<ext>: answer a call ringing that extension (BLF keys)"},
}

func InstallDefaults(ctx context.Context, st *db.Store) (int, error) {
	created, err := st.CreateMissingTables(ctx)
	if err != nil {
		return 0, err
	}
	if err := st.EnsureSchema(ctx); err != nil {
		return 0, err
	}
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	added := len(created)
	n, err := installDialplans(ctx, tx, st, append(append([]baseDialplan{}, baseDialplans...), deskPhoneDialplans...))
	if err != nil {
		return 0, err
	}
	added += n
	doms, err := domainsTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, dom := range doms {
		if st.HasTable("rq_aux_codes") {
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from rq_aux_codes where domain_uuid = $1::uuid`, dom.uuid).Scan(&n); err != nil {
				return 0, err
			}
			if n == 0 {
				for _, a := range defaultAuxCodes {
					if err := insertRow(ctx, tx, st, "rq_aux_codes", map[string]string{"aux_code_uuid": newUUID(), "domain_uuid": dom.uuid,
						"aux_code_name": a.name, "block_direct_calls": strconv.FormatBool(a.block), "aux_code_order": strconv.Itoa(a.order),
						"aux_code_enabled": "true", "insert_date": time.Now().Format(time.RFC3339)}); err != nil {
						return 0, err
					}
					added++
				}
			}
		}
		for _, r := range resources {
			if r.dialplan == nil || !st.HasTable(r.table) || !st.HasColumn(r.table, "dialplan_uuid") {
				continue
			}
			rows, err := jsonRowsTx(ctx, tx, fmt.Sprintf(`select to_jsonb(t) from %s t where domain_uuid = $1::uuid
				and (dialplan_uuid is null or not exists (select 1 from v_dialplans d where d.dialplan_uuid = t.dialplan_uuid))`, r.table), dom.uuid)
			if err != nil {
				return 0, err
			}
			for _, row := range rows {
				if r.decorate != nil {
					r.decorate(row)
				}
				if r.numberCol != "" && row[r.numberCol] != "" {
					// A dialplan for this number already exists (written by
					// RingQ or an older setup script): use it.
					var existing string
					err := tx.QueryRow(ctx, `select dialplan_uuid::text from v_dialplans where domain_uuid = $1::uuid and dialplan_number = $2
						order by dialplan_order limit 1`, dom.uuid, row[r.numberCol]).Scan(&existing)
					if err == nil {
						if _, err := tx.Exec(ctx, fmt.Sprintf(`update %s set dialplan_uuid = $1::uuid where %s = $2::uuid`, r.table, r.key), existing, row[r.key]); err != nil {
							return 0, err
						}
						continue
					}
					if err != pgx.ErrNoRows {
						return 0, err
					}
				}
				if err := saveDialplan(ctx, tx, st, r, dom, row); err != nil {
					return 0, err
				}
				added++
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	sc, err := installDefaultScanners(ctx, st)
	return added + sc, err
}
