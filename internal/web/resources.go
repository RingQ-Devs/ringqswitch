package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/db"
)

// Resources are the things the interface manages. Each maps to a table
// (the RingQ portal's where one exists), scoped to a domain. Pseudo fields (such
// as a DID's "action" or "gateway_uuid") are translated to and from the
// stored columns by prepare and decorate.

type domainRow struct {
	uuid string
	name string
}

type childSpec struct {
	table    string
	key      string
	parent   string
	field    string // JSON field of the parent
	order    string
	columns  []string
	prepare  func(dom domainRow, parent, child map[string]string, raw map[string]any) error
	decorate func(child map[string]string)
	list     func(ctx context.Context, s *Server, dom domainRow, kids []map[string]string)
}

type resource struct {
	name       string
	table      string
	key        string
	order      string
	nameCol    string
	numberCol  string
	enabledCol string
	columns    []string
	required   []string
	defaults   func(dom domainRow) map[string]string
	children   *childSpec
	prepare    func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error
	decorate   func(row map[string]string)
	list       func(ctx context.Context, s *Server, dom domainRow, rows []map[string]string)
	dialplan   func(dom domainRow, row map[string]string) string
	dpContext  string // "" = the domain's context
	dpOrder    func(row map[string]string) int
	afterSave  func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row, old map[string]string, raw map[string]any) error
	afterDel   func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string) error
}

var resources []*resource

func resourceByName(name string) *resource {
	for _, r := range resources {
		if r.name == name {
			return r
		}
	}
	return nil
}

// numberTables are the dialable numbers within a domain (no duplicates).
var numberTables = []struct{ table, col, key, what string }{
	{"v_extensions", "extension", "extension_uuid", "extension"},
	{"v_ring_groups", "ring_group_extension", "ring_group_uuid", "ring group"},
	{"v_ivr_menus", "ivr_menu_extension", "ivr_menu_uuid", "IVR menu"},
	{"v_call_center_queues", "queue_extension", "call_center_queue_uuid", "queue"},
	{"v_conferences", "conference_extension", "conference_uuid", "conference"},
	{"v_fax", "fax_extension", "fax_uuid", "fax extension"},
	{"rq_time_conditions", "condition_extension", "time_condition_uuid", "time condition"},
	{"rq_ai_agents", "agent_extension", "ai_agent_uuid", "AI agent"},
}

var (
	reNumber      = regexp.MustCompile(`^[0-9*#]{1,15}$`)
	reDID         = regexp.MustCompile(`^\+?[0-9]{2,20}$`)
	reCallingCode = regexp.MustCompile(`^[0-9]{1,4}$`)
	reHHMM        = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	reDigits      = regexp.MustCompile(`^[0-9]*$`)
	reUUID        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	strategies    = core.CCStrategies
)

func splitAction(v string) (string, string) {
	app, data, _ := strings.Cut(strings.TrimSpace(v), ":")
	return app, data
}

func joinAction(app, data string) string {
	if app == "" {
		return ""
	}
	return app + ":" + data
}

func init() {
	resources = []*resource{
		{
			name: "extensions", table: "v_extensions", key: "extension_uuid", order: "extension", nameCol: "extension",
			numberCol: "extension", enabledCol: "enabled",
			columns: []string{"extension", "number_alias", "password", "effective_caller_id_name", "effective_caller_id_number",
				"outbound_caller_id_name", "outbound_caller_id_number", "emergency_caller_id_number", "directory_first_name",
				"directory_last_name", "user_context", "call_timeout", "call_group", "user_record", "hold_music", "max_registrations",
				"limit_max", "toll_allow", "accountcode", "do_not_disturb", "forward_all_enabled", "forward_all_destination",
				"forward_busy_enabled", "forward_busy_destination", "forward_no_answer_enabled", "forward_no_answer_destination",
				"forward_user_not_registered_enabled", "forward_user_not_registered_destination", "enabled", "description",
				"absolute_codec_string"},
			required: []string{"extension", "password"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"user_context": dom.name, "enabled": "true", "call_timeout": "30", "password": randomPassword(12),
					"do_not_disturb": "false", "forward_all_enabled": "false", "forward_busy_enabled": "false",
					"forward_no_answer_enabled": "false", "forward_user_not_registered_enabled": "false", "max_registrations": "3"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				if !reNumber.MatchString(row["extension"]) {
					return badRequest("extension: use digits only (up to 15)")
				}
				if len(row["password"]) < 6 {
					return badRequest("password: at least 6 characters")
				}
				if row["effective_caller_id_number"] == "" {
					row["effective_caller_id_number"] = row["extension"]
				}
				if row["effective_caller_id_name"] == "" {
					row["effective_caller_id_name"] = row["extension"]
				}
				return nil
			},
			list: func(ctx context.Context, s *Server, dom domainRow, rows []map[string]string) {
				s.decorateExtensions(ctx, dom, rows)
				vms := map[string]map[string]string{}
				if s.store.HasTable("v_voicemails") {
					if vrows, err := s.store.JSONRows(ctx, `select to_jsonb(v) from v_voicemails v where domain_uuid = $1::uuid`, dom.uuid); err == nil {
						for _, v := range vrows {
							vms[v["voicemail_id"]] = v
						}
					}
				}
				for _, r := range rows {
					if v := vms[r["extension"]]; v != nil {
						r["voicemail_enabled"] = strconv.FormatBool(truthy(firstNonEmpty(v["voicemail_enabled"], "true")))
						r["voicemail_password"] = v["voicemail_password"]
						r["voicemail_mail_to"] = v["voicemail_mail_to"]
					} else {
						r["voicemail_enabled"] = "false"
					}
				}
			},
			afterSave: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row, old map[string]string, raw map[string]any) error {
				if err := s.saveExtensionExtras(ctx, tx, dom, row, old, raw); err != nil {
					return err
				}
				if !s.store.HasTable("v_voicemails") {
					return nil
				}
				if old != nil && old["extension"] != row["extension"] {
					if _, err := tx.Exec(ctx, `update v_voicemails set voicemail_id = $3 where domain_uuid = $1::uuid and voicemail_id = $2`,
						dom.uuid, old["extension"], row["extension"]); err != nil {
						return err
					}
				}
				want, given := raw["voicemail_enabled"]
				if !given {
					return nil
				}
				var id string
				err := tx.QueryRow(ctx, `select voicemail_uuid::text from v_voicemails where domain_uuid = $1::uuid and voicemail_id = $2`, dom.uuid, row["extension"]).Scan(&id)
				enabled := truthy(toStr(want))
				pin := firstNonEmpty(toStr(raw["voicemail_password"]), row["extension"])
				if !reDigits.MatchString(pin) {
					return badRequest("voicemail PIN: digits only")
				}
				vm := map[string]string{"voicemail_enabled": strconv.FormatBool(enabled), "voicemail_password": pin,
					"voicemail_mail_to": toStr(raw["voicemail_mail_to"])}
				if err == pgx.ErrNoRows {
					if !enabled {
						return nil
					}
					vm["voicemail_uuid"], vm["domain_uuid"], vm["voicemail_id"] = newUUID(), dom.uuid, row["extension"]
					vm["greeting_id"] = "0"
					return insertRow(ctx, tx, s.store, "v_voicemails", vm)
				}
				if err != nil {
					return err
				}
				return updateRow(ctx, tx, s.store, "v_voicemails", "voicemail_uuid", id, vm)
			},
			afterDel: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string) error {
				if s.store.HasTable("v_voicemails") {
					if s.store.HasTable("v_voicemail_messages") {
						if _, err := tx.Exec(ctx, `delete from v_voicemail_messages where voicemail_uuid in
							(select voicemail_uuid from v_voicemails where domain_uuid = $1::uuid and voicemail_id = $2)`, dom.uuid, row["extension"]); err != nil {
							return err
						}
					}
					if _, err := tx.Exec(ctx, `delete from v_voicemails where domain_uuid = $1::uuid and voicemail_id = $2`, dom.uuid, row["extension"]); err != nil {
						return err
					}
				}
				return s.deleteExtensionExtras(ctx, tx, dom, row)
			},
		},
		{
			name: "gateways", table: "v_gateways", key: "gateway_uuid", order: "gateway", nameCol: "gateway", enabledCol: "enabled",
			columns: []string{"gateway", "username", "password", "auth_username", "realm", "from_user", "from_domain", "proxy",
				"register_proxy", "outbound_proxy", "expire_seconds", "register", "register_transport", "retry_seconds",
				"caller_id_in_from", "context", "extension", "enabled", "description"},
			required: []string{"gateway", "proxy"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"context": "public", "profile": "external", "expire_seconds": "800", "retry_seconds": "30",
					"register": "true", "register_transport": "udp", "enabled": "true", "caller_id_in_from": "false"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				row["proxy"] = strings.TrimPrefix(strings.TrimSpace(row["proxy"]), "sip:")
				if truthy(row["register"]) && row["username"] == "" {
					return badRequest("a gateway that registers needs a username")
				}
				if t := row["register_transport"]; t != "" && t != "udp" && t != "tcp" {
					return badRequest("transport: udp or tcp")
				}
				row["profile"] = firstNonEmpty(row["profile"], "external")
				return nil
			},
		},
		{
			name: "dids", table: "v_destinations", key: "destination_uuid", order: "destination_number", nameCol: "destination_number",
			enabledCol: "destination_enabled",
			columns: []string{"destination_number", "destination_prefix", "destination_cid_name_prefix", "destination_record", "destination_enabled",
				"destination_description"},
			required: []string{"destination_number"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"destination_enabled": "true", "destination_record": "false"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				row["destination_number"] = strings.ReplaceAll(strings.TrimSpace(row["destination_number"]), " ", "")
				if !reDID.MatchString(row["destination_number"]) {
					return badRequest("DID number: digits, optionally starting with +")
				}
				// The country: its calling code (the RingQ portal's destination_prefix).
				row["destination_prefix"] = strings.TrimPrefix(strings.TrimSpace(row["destination_prefix"]), "+")
				if p := row["destination_prefix"]; p != "" && !reCallingCode.MatchString(p) {
					return badRequest("country: a calling code of 1 to 4 digits")
				}
				// No destination yet = an available number (Call Flow): its
				// dialplan stays off until it is linked to something.
				act := toStr(raw["action"])
				if _, given := raw["action"]; !given && !isNew {
					act = row["action"]
				}
				app, data := splitAction(act)
				row["destination_type"], row["destination_context"] = "inbound", "public"
				row["destination_app"], row["destination_data"] = app, data
				row["destination_actions"] = "[]"
				if act != "" {
					acts, _ := json.Marshal([]map[string]string{{"destination_app": app, "destination_data": data}})
					row["destination_actions"] = string(acts)
				}
				gw := toStr(raw["gateway_uuid"])
				if _, given := raw["gateway_uuid"]; !given && !isNew {
					gw = row["gateway_uuid"]
				}
				conds := "[]"
				if gw != "" {
					if !reUUID.MatchString(gw) {
						return badRequest("invalid gateway")
					}
					b, _ := json.Marshal([]map[string]string{{"condition_field": "${gateway_uuid}", "condition_expression": "^" + gw + "$"}})
					conds = string(b)
				}
				row["destination_conditions"] = conds
				row["gateway_uuid"], row["action"] = gw, act
				var n int
				if err := tx.QueryRow(ctx, `select count(*) from v_destinations where destination_number = $1 and destination_uuid <> $2::uuid`,
					row["destination_number"], firstNonEmpty(row["destination_uuid"], "00000000-0000-0000-0000-000000000000")).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					return badRequest("DID %s already exists", row["destination_number"])
				}
				return nil
			},
			decorate: func(row map[string]string) {
				row["action"] = joinAction(row["destination_app"], row["destination_data"])
				var conds []map[string]string
				if json.Unmarshal([]byte(row["destination_conditions"]), &conds) == nil {
					for _, c := range conds {
						if c["condition_field"] == "${gateway_uuid}" {
							row["gateway_uuid"] = strings.Trim(c["condition_expression"], "^$")
						}
					}
				}
			},
			dialplan: didDialplan, dpContext: "public",
			dpOrder: func(map[string]string) int { return 100 },
		},
		{
			name: "routes", table: "rq_outbound_routes", key: "outbound_route_uuid", order: "route_order, route_name", nameCol: "route_name",
			enabledCol: "route_enabled",
			columns: []string{"route_name", "route_pattern", "route_strip", "route_prepend", "gateway_uuids", "caller_id_number",
				"caller_id_name", "route_order", "route_enabled", "route_description", "route_prefix", "route_length",
				"route_from_extensions", "route_from_ranges", "route_block", "sip_from_user", "sip_from_display", "sip_from_host", "cid_type"},
			required: []string{"route_name"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"route_strip": "0", "route_order": "100", "route_enabled": "true", "route_block": "false"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				if err := prepareRoute(row); err != nil {
					return err
				}
				for _, g := range strings.Split(row["gateway_uuids"], ",") {
					if g = strings.TrimSpace(g); g != "" && !reUUID.MatchString(g) {
						return badRequest("invalid gateway")
					}
				}
				row["route_strip"] = strconv.Itoa(atoi(row["route_strip"], 0))
				row["route_order"] = strconv.Itoa(atoi(row["route_order"], 100))
				return nil
			},
			dialplan: routeDialplan,
			// After the domain's own numbers (queues, IVRs... at 100), so a
			// broad pattern never takes their calls.
			dpOrder: func(r map[string]string) int { return 500 + min(max(atoi(r["route_order"], 100), 0), 400) },
		},
		{
			name: "ringgroups", table: "v_ring_groups", key: "ring_group_uuid", order: "ring_group_extension", nameCol: "ring_group_name",
			numberCol: "ring_group_extension", enabledCol: "ring_group_enabled",
			columns: []string{"ring_group_name", "ring_group_extension", "ring_group_strategy", "ring_group_call_timeout",
				"ring_group_greeting", "ring_group_cid_name_prefix", "ring_group_cid_number_prefix", "ring_group_forward_enabled",
				"ring_group_forward_destination", "ring_group_enabled", "ring_group_description"},
			required: []string{"ring_group_name", "ring_group_extension"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"ring_group_strategy": "simultaneous", "ring_group_call_timeout": "30", "ring_group_enabled": "true",
					"ring_group_context": dom.name, "ring_group_forward_enabled": "false"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				switch row["ring_group_strategy"] {
				case "simultaneous", "sequence", "enterprise", "rollover", "random":
				default:
					return badRequest("strategy: simultaneous, sequence, enterprise, rollover or random")
				}
				if v, ok := raw["timeout_action"]; ok {
					row["ring_group_timeout_app"], row["ring_group_timeout_data"] = splitAction(toStr(v))
				}
				row["ring_group_context"] = firstNonEmpty(row["ring_group_context"], dom.name)
				return nil
			},
			decorate: func(row map[string]string) {
				row["timeout_action"] = joinAction(row["ring_group_timeout_app"], row["ring_group_timeout_data"])
			},
			children: &childSpec{table: "v_ring_group_destinations", key: "ring_group_destination_uuid", parent: "ring_group_uuid",
				field: "destinations", order: "destination_delay, destination_number",
				columns: []string{"destination_number", "destination_delay", "destination_timeout", "destination_enabled"},
				prepare: func(dom domainRow, parent, c map[string]string, raw map[string]any) error {
					if !reNumber.MatchString(c["destination_number"]) && !reDID.MatchString(c["destination_number"]) {
						return badRequest("ring group member %q: a phone number", c["destination_number"])
					}
					c["destination_delay"] = strconv.Itoa(atoi(c["destination_delay"], 0))
					c["destination_timeout"] = strconv.Itoa(atoi(c["destination_timeout"], 30))
					c["destination_enabled"] = strconv.FormatBool(c["destination_enabled"] == "" || truthy(c["destination_enabled"]))
					return nil
				}},
			dialplan: ringGroupDialplan,
		},
		{
			name: "ivrs", table: "v_ivr_menus", key: "ivr_menu_uuid", order: "ivr_menu_extension", nameCol: "ivr_menu_name",
			numberCol: "ivr_menu_extension", enabledCol: "ivr_menu_enabled",
			columns: []string{"ivr_menu_name", "ivr_menu_extension", "ivr_menu_greet_long", "ivr_menu_greet_short",
				"ivr_menu_invalid_sound", "ivr_menu_exit_sound", "ivr_menu_timeout", "ivr_menu_inter_digit_timeout",
				"ivr_menu_max_failures", "ivr_menu_max_timeouts", "ivr_menu_digit_len", "ivr_menu_direct_dial",
				"ivr_menu_cid_prefix", "ivr_menu_enabled", "ivr_menu_description"},
			required: []string{"ivr_menu_name", "ivr_menu_extension"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"ivr_menu_timeout": "3000", "ivr_menu_inter_digit_timeout": "2000", "ivr_menu_max_failures": "3",
					"ivr_menu_max_timeouts": "3", "ivr_menu_digit_len": "5", "ivr_menu_direct_dial": "false", "ivr_menu_enabled": "true",
					"ivr_menu_context": dom.name}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				if v, ok := raw["exit_action"]; ok {
					row["ivr_menu_exit_app"], row["ivr_menu_exit_data"] = splitAction(toStr(v))
				}
				row["ivr_menu_context"] = firstNonEmpty(row["ivr_menu_context"], dom.name)
				return nil
			},
			decorate: func(row map[string]string) {
				row["exit_action"] = joinAction(row["ivr_menu_exit_app"], row["ivr_menu_exit_data"])
			},
			children: &childSpec{table: "v_ivr_menu_options", key: "ivr_menu_option_uuid", parent: "ivr_menu_uuid", field: "options",
				order:   "ivr_menu_option_order, ivr_menu_option_digits",
				columns: []string{"ivr_menu_option_digits", "ivr_menu_option_description", "ivr_menu_option_order"},
				prepare: func(dom domainRow, parent, c map[string]string, raw map[string]any) error {
					if c["ivr_menu_option_digits"] == "" {
						return badRequest("IVR option without digits")
					}
					app, data := splitAction(toStr(raw["action"]))
					switch {
					case app == "":
						return badRequest("IVR option %s: choose a destination", c["ivr_menu_option_digits"])
					case strings.HasPrefix(app, "menu-"):
						c["ivr_menu_option_action"], c["ivr_menu_option_param"] = app, data
					default:
						c["ivr_menu_option_action"], c["ivr_menu_option_param"] = "menu-exec-app", strings.TrimSpace(app+" "+data)
					}
					c["ivr_menu_option_order"] = strconv.Itoa(atoi(c["ivr_menu_option_order"], 10))
					c["ivr_menu_option_enabled"] = "true"
					return nil
				},
				decorate: func(c map[string]string) {
					if c["ivr_menu_option_action"] == "menu-exec-app" {
						app, data, _ := strings.Cut(c["ivr_menu_option_param"], " ")
						c["action"] = joinAction(app, data)
					} else {
						c["action"] = joinAction(c["ivr_menu_option_action"], c["ivr_menu_option_param"])
					}
				}},
			dialplan: ivrDialplan,
		},
		{
			name: "queues", table: "v_call_center_queues", key: "call_center_queue_uuid", order: "queue_extension", nameCol: "queue_name",
			numberCol: "queue_extension",
			columns: []string{"queue_name", "queue_extension", "queue_strategy", "queue_moh_sound", "queue_greeting",
				"queue_max_wait_time", "queue_max_wait_time_with_no_agent", "queue_max_wait_time_with_no_agent_time_reached",
				"queue_tier_rules_apply", "queue_tier_rule_wait_second", "queue_tier_rule_no_agent_no_wait",
				"queue_tier_rule_wait_multiply_level", "queue_timeout_action", "queue_discard_abandoned_after",
				"queue_cid_prefix", "queue_announce_sound", "queue_announce_frequency", "queue_cc_exit_keys",
				"queue_description", "queue_max_callers"},
			required: []string{"queue_name", "queue_extension"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"queue_strategy": "longest-idle-agent", "queue_moh_sound": "local_stream://default",
					"queue_max_wait_time": "0", "queue_max_wait_time_with_no_agent": "90",
					"queue_max_wait_time_with_no_agent_time_reached": "5", "queue_tier_rules_apply": "false",
					"queue_tier_rule_wait_second": "300", "queue_tier_rule_no_agent_no_wait": "true",
					"queue_tier_rule_wait_multiply_level": "true", "queue_discard_abandoned_after": "900"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				ok := false
				for _, st := range strategies {
					ok = ok || st == row["queue_strategy"]
				}
				if !ok {
					return badRequest("strategy: one of %s", strings.Join(strategies, ", "))
				}
				if v, given := raw["record"]; given {
					if truthy(toStr(v)) {
						row["queue_record_template"] = recordPath
					} else {
						row["queue_record_template"] = ""
					}
				}
				return nil
			},
			decorate: func(row map[string]string) {
				row["record"] = strconv.FormatBool(strings.TrimSpace(row["queue_record_template"]) != "")
			},
			// Queue members are chosen by extension; the agent record is
			// created when the extension is not an agent yet.
			children: &childSpec{table: "v_call_center_tiers", key: "call_center_tier_uuid", parent: "call_center_queue_uuid",
				field: "agents", order: "tier_level, tier_position",
				columns: []string{"call_center_agent_uuid", "extension", "tier_level", "tier_position"},
				prepare: func(dom domainRow, parent, c map[string]string, raw map[string]any) error {
					if c["extension"] == "" && !reUUID.MatchString(c["call_center_agent_uuid"]) {
						return badRequest("queue member: choose an extension")
					}
					c["tier_level"] = strconv.Itoa(atoi(c["tier_level"], 1))
					c["tier_position"] = strconv.Itoa(atoi(c["tier_position"], 1))
					c["queue_name"] = parent["queue_extension"] + "@" + dom.name
					return nil
				},
				list: func(ctx context.Context, s *Server, dom domainRow, kids []map[string]string) {
					rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('id', call_center_agent_uuid, 'contact', agent_contact)
						from v_call_center_agents where domain_uuid = $1::uuid`, dom.uuid)
					if err != nil {
						return
					}
					ext := map[string]string{}
					for _, r := range rows {
						ext[r["id"]] = extFromContact(r["contact"])
					}
					for _, k := range kids {
						k["extension"] = ext[k["call_center_agent_uuid"]]
					}
				}},
			dialplan: queueDialplan,
		},
		{
			name: "agents", table: "v_call_center_agents", key: "call_center_agent_uuid", order: "agent_name", nameCol: "agent_name",
			columns: []string{"agent_name", "agent_type", "agent_call_timeout", "agent_id", "agent_password", "agent_status",
				"agent_max_no_answer", "agent_wrap_up_time", "agent_reject_delay_time", "agent_busy_delay_time",
				"agent_no_answer_delay_time", "agent_record"},
			required: []string{"agent_name"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"agent_type": "callback", "agent_call_timeout": "20", "agent_status": "Logged Out",
					"agent_max_no_answer": "3", "agent_wrap_up_time": "10", "agent_reject_delay_time": "10",
					"agent_busy_delay_time": "60", "agent_no_answer_delay_time": "30", "agent_record": "false"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				ext := toStr(raw["extension"])
				if ext == "" {
					ext = extFromContact(row["agent_contact"])
				}
				if ext == "" {
					return badRequest("choose the agent's extension")
				}
				var n int
				if err := tx.QueryRow(ctx, `select count(*) from v_extensions where domain_uuid = $1::uuid and extension = $2`, dom.uuid, ext).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					return badRequest("extension %s does not exist", ext)
				}
				row["agent_contact"] = "user/" + ext + "@" + dom.name
				row["agent_id"] = firstNonEmpty(row["agent_id"], ext)
				if err := tx.QueryRow(ctx, `select count(*) from v_call_center_agents where domain_uuid = $1::uuid and agent_contact = $2 and call_center_agent_uuid <> $3::uuid`,
					dom.uuid, row["agent_contact"], firstNonEmpty(row["call_center_agent_uuid"], "00000000-0000-0000-0000-000000000000")).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					return badRequest("extension %s already has an agent", ext)
				}
				return nil
			},
			decorate: func(row map[string]string) { row["extension"] = extFromContact(row["agent_contact"]) },
			afterDel: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string) error {
				_, err := tx.Exec(ctx, `delete from v_call_center_tiers where call_center_agent_uuid = $1::uuid`, row["call_center_agent_uuid"])
				return err
			},
		},
		{
			name: "auxcodes", table: "rq_aux_codes", key: "aux_code_uuid", order: "aux_code_order, aux_code_name", nameCol: "aux_code_name",
			enabledCol: "aux_code_enabled",
			columns:    []string{"aux_code_name", "block_direct_calls", "aux_code_order", "aux_code_enabled"},
			required:   []string{"aux_code_name"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"block_direct_calls": "false", "aux_code_order": "100", "aux_code_enabled": "true"}
			},
			// The number desk phones dial for each (*26N: 1 is Available).
			list: func(ctx context.Context, s *Server, dom domainRow, rows []map[string]string) {
				n := 1
				for _, r := range rows {
					if !truthy(firstNonEmpty(r["aux_code_enabled"], "true")) || strings.EqualFold(r["aux_code_name"], "available") {
						continue
					}
					n++
					r["dial"] = "*26" + strconv.Itoa(n)
				}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				row["aux_code_order"] = strconv.Itoa(atoi(row["aux_code_order"], 100))
				if row["insert_date"] == "" {
					row["insert_date"] = time.Now().Format(time.RFC3339)
				}
				return nil
			},
		},
		{
			name: "timeconditions", table: "rq_time_conditions", key: "time_condition_uuid", order: "condition_extension",
			nameCol: "condition_name", numberCol: "condition_extension", enabledCol: "condition_enabled",
			columns: []string{"condition_name", "condition_extension", "weekdays", "time_from", "time_to", "holidays", "match_action",
				"nomatch_action", "condition_enabled", "condition_description"},
			required: []string{"condition_name", "condition_extension", "match_action", "nomatch_action"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"weekdays": "mon,tue,wed,thu,fri", "time_from": "09:00", "time_to": "18:00", "condition_enabled": "true"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				for _, k := range []string{"time_from", "time_to"} {
					if row[k] != "" && !reHHMM.MatchString(row[k]) {
						return badRequest("%s: use HH:MM (24 hour)", k)
					}
				}
				for _, d := range strings.Split(row["holidays"], ",") {
					if d = strings.TrimSpace(d); d != "" {
						if _, err := time.Parse("2006-01-02", d); err != nil {
							return badRequest("holiday %q: use YYYY-MM-DD", d)
						}
					}
				}
				return nil
			},
			dialplan: timeConditionDialplan,
		},
		aiAgentResource(),
		{
			name: "conferences", table: "v_conferences", key: "conference_uuid", order: "conference_extension", nameCol: "conference_name",
			numberCol: "conference_extension", enabledCol: "conference_enabled",
			columns:  []string{"conference_name", "conference_extension", "conference_pin_number", "conference_flags", "conference_enabled", "conference_description"},
			required: []string{"conference_name", "conference_extension"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"conference_enabled": "true", "conference_profile": "default"}
			},
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				if !reDigits.MatchString(row["conference_pin_number"]) {
					return badRequest("PIN: digits only")
				}
				if v, given := raw["record"]; given {
					flags := strings.ReplaceAll(row["conference_flags"], "record", "")
					if truthy(toStr(v)) {
						flags = strings.Trim(flags+"|record", "|")
					}
					row["conference_flags"] = strings.Trim(flags, "|")
				}
				row["conference_profile"] = firstNonEmpty(row["conference_profile"], "default")
				return nil
			},
			decorate: func(row map[string]string) {
				row["record"] = strconv.FormatBool(strings.Contains(row["conference_flags"], "record"))
			},
			dialplan: conferenceDialplan,
		},
		{
			// Fax extensions: a DID sent here is answered as a fax machine
			// and the fax lands in the eFax inbox.
			name: "faxes", table: "v_fax", key: "fax_uuid", order: "fax_extension", nameCol: "fax_name", numberCol: "fax_extension",
			columns:  []string{"fax_name", "fax_extension", "fax_caller_id_name", "fax_caller_id_number", "fax_email", "fax_description"},
			required: []string{"fax_name", "fax_extension"},
			defaults: func(dom domainRow) map[string]string { return map[string]string{} },
			prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
				if !reDigits.MatchString(row["fax_extension"]) {
					return badRequest("fax extension number: digits only")
				}
				row["fax_caller_id_number"] = strings.ReplaceAll(strings.TrimSpace(row["fax_caller_id_number"]), " ", "")
				if n := row["fax_caller_id_number"]; n != "" && !reDID.MatchString(n) {
					return badRequest("fax number: digits, optionally starting with +")
				}
				if e := strings.TrimSpace(row["fax_email"]); e != "" && !reEmail.MatchString(e) {
					return badRequest("email address: %s is not valid", e)
				}
				return nil
			},
			// DIDs assigned to the fax extension carry its details in their dialplans.
			afterSave: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row, old map[string]string, raw map[string]any) error {
				return refreshFaxDIDs(ctx, s, tx, dom, "", row["fax_extension"], old["fax_extension"])
			},
			afterDel: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string) error {
				return refreshFaxDIDs(ctx, s, tx, dom, row["fax_uuid"], row["fax_extension"])
			},
			dialplan: faxDialplan,
		},
		{
			name: "voicemails", table: "v_voicemails", key: "voicemail_uuid", order: "voicemail_id", nameCol: "voicemail_id",
			enabledCol: "voicemail_enabled",
			columns:    []string{"voicemail_id", "voicemail_password", "voicemail_mail_to", "voicemail_enabled", "voicemail_description"},
			required:   []string{"voicemail_id"},
			defaults: func(dom domainRow) map[string]string {
				return map[string]string{"voicemail_enabled": "true", "greeting_id": "0"}
			},
		},
	}
}

func extFromContact(c string) string {
	if i := strings.Index(c, "user/"); i >= 0 {
		rest := c[i+5:]
		ext, _, _ := strings.Cut(rest, "@")
		return ext
	}
	return ""
}

// ---------------------------------------------------------------------------
// Generic CRUD

func (s *Server) list(ctx context.Context, r *resource, dom domainRow) ([]map[string]string, error) {
	if !s.store.HasTable(r.table) {
		return []map[string]string{}, nil
	}
	rows, err := s.store.JSONRows(ctx, fmt.Sprintf(`select to_jsonb(t) from %s t where domain_uuid = $1::uuid order by %s`, r.table, r.order), dom.uuid)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if r.decorate != nil {
			r.decorate(row)
		}
	}
	if r.list != nil {
		r.list(ctx, s, dom, rows)
	}
	if rows == nil {
		rows = []map[string]string{}
	}
	return rows, nil
}

func (s *Server) get(ctx context.Context, r *resource, dom domainRow, id string) (map[string]any, error) {
	if !reUUID.MatchString(id) {
		return nil, notFound(r.name)
	}
	rows, err := s.store.JSONRows(ctx, fmt.Sprintf(`select to_jsonb(t) from %s t where domain_uuid = $1::uuid and %s = $2::uuid`, r.table, r.key), dom.uuid, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, notFound(r.name)
	}
	row := rows[0]
	if r.decorate != nil {
		r.decorate(row)
	}
	if r.list != nil {
		r.list(ctx, s, dom, rows)
	}
	out := map[string]any{}
	for k, v := range row {
		out[k] = v
	}
	if c := r.children; c != nil {
		kids, err := s.store.JSONRows(ctx, fmt.Sprintf(`select to_jsonb(t) from %s t where %s = $1::uuid order by %s`, c.table, c.parent, c.order), id)
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			if c.decorate != nil {
				c.decorate(k)
			}
		}
		if kids == nil {
			kids = []map[string]string{}
		}
		if c.list != nil {
			c.list(ctx, s, dom, kids)
		}
		out[c.field] = kids
	}
	return out, nil
}

// save creates (id == "") or updates a resource from a JSON body.
func (s *Server) save(ctx context.Context, r *resource, dom domainRow, id string, raw map[string]any) (map[string]any, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	isNew := id == ""
	var old map[string]string
	row := map[string]string{}
	if isNew {
		if r.defaults != nil {
			for k, v := range r.defaults(dom) {
				row[k] = v
			}
		}
		id = newUUID()
	} else {
		if !reUUID.MatchString(id) {
			return nil, notFound(r.name)
		}
		rows, err := jsonRowsTx(ctx, tx, fmt.Sprintf(`select to_jsonb(t) from %s t where domain_uuid = $1::uuid and %s = $2::uuid for update`, r.table, r.key), dom.uuid, id)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, notFound(r.name)
		}
		old = rows[0]
		for k, v := range old {
			row[k] = v
		}
		if r.decorate != nil {
			r.decorate(row)
		}
	}
	for _, col := range r.columns {
		if v, ok := raw[col]; ok {
			row[col] = strings.TrimSpace(toStr(v))
			if strings.Contains(col, "password") {
				row[col] = toStr(v) // passwords keep their spaces
			}
		}
	}
	row[r.key], row["domain_uuid"] = id, dom.uuid
	if r.prepare != nil {
		if err := r.prepare(ctx, s, tx, dom, row, raw, isNew); err != nil {
			return nil, err
		}
	}
	for _, req := range r.required {
		if strings.TrimSpace(row[req]) == "" {
			return nil, badRequest("%s is required", strings.ReplaceAll(req, "_", " "))
		}
	}
	if r.numberCol != "" {
		if err := checkNumber(ctx, tx, s.store, dom, row[r.numberCol], r.table, id); err != nil {
			return nil, err
		}
	}
	stored := map[string]string{}
	for k, v := range row {
		if s.store.HasColumn(r.table, k) {
			stored[k] = v
		}
	}
	if isNew {
		if s.store.HasColumn(r.table, "insert_date") && stored["insert_date"] == "" {
			stored["insert_date"] = time.Now().Format(time.RFC3339)
		}
		if err := insertRow(ctx, tx, s.store, r.table, stored); err != nil {
			return nil, err
		}
	} else {
		if s.store.HasColumn(r.table, "update_date") {
			stored["update_date"] = time.Now().Format(time.RFC3339)
		}
		delete(stored, r.key)
		delete(stored, "domain_uuid")
		if err := updateRow(ctx, tx, s.store, r.table, r.key, id, stored); err != nil {
			return nil, err
		}
	}
	if c := r.children; c != nil {
		if list, given := raw[c.field]; given {
			if err := s.saveChildren(ctx, tx, r, dom, row, list); err != nil {
				return nil, err
			}
		}
	}
	if r.afterSave != nil {
		if err := r.afterSave(ctx, s, tx, dom, row, old, raw); err != nil {
			return nil, err
		}
	}
	if r.dialplan != nil {
		if err := saveDialplan(ctx, tx, s.store, r, dom, row); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.reload()
	return s.get(ctx, r, dom, id)
}

func (s *Server) saveChildren(ctx context.Context, tx pgx.Tx, r *resource, dom domainRow, parent map[string]string, list any) error {
	c := r.children
	items, ok := list.([]any)
	if !ok && list != nil {
		return badRequest("%s: expected a list", c.field)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`delete from %s where %s = $1::uuid`, c.table, c.parent), parent[r.key]); err != nil {
		return err
	}
	for _, it := range items {
		raw, ok := it.(map[string]any)
		if !ok {
			return badRequest("%s: invalid entry", c.field)
		}
		child := map[string]string{}
		for _, col := range c.columns {
			child[col] = strings.TrimSpace(toStr(raw[col]))
		}
		if c.prepare != nil {
			if err := c.prepare(dom, parent, child, raw); err != nil {
				return err
			}
		}
		child[c.key], child[c.parent], child["domain_uuid"] = newUUID(), parent[r.key], dom.uuid
		if s.store.HasColumn(c.table, "insert_date") {
			child["insert_date"] = time.Now().Format(time.RFC3339)
		}
		if r.name == "queues" {
			id, name, err := s.ensureAgent(ctx, tx, dom, child["extension"], child["call_center_agent_uuid"], nil)
			if err != nil {
				return err
			}
			child["call_center_agent_uuid"], child["agent_name"] = id, name // v_call_center_tiers keeps the name too
		}
		stored := map[string]string{}
		for k, v := range child {
			if s.store.HasColumn(c.table, k) {
				stored[k] = v
			}
		}
		if err := insertRow(ctx, tx, s.store, c.table, stored); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) remove(ctx context.Context, r *resource, dom domainRow, id string) error {
	if !reUUID.MatchString(id) {
		return notFound(r.name)
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := jsonRowsTx(ctx, tx, fmt.Sprintf(`select to_jsonb(t) from %s t where domain_uuid = $1::uuid and %s = $2::uuid`, r.table, r.key), dom.uuid, id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return notFound(r.name)
	}
	row := rows[0]
	if r.afterDel != nil {
		if err := r.afterDel(ctx, s, tx, dom, row); err != nil {
			return err
		}
	}
	if c := r.children; c != nil {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`delete from %s where %s = $1::uuid`, c.table, c.parent), id); err != nil {
			return err
		}
	}
	if err := deleteDialplan(ctx, tx, row["dialplan_uuid"]); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`delete from %s where %s = $1::uuid`, r.table, r.key), id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.reload()
	return nil
}

// saveDialplan (re)writes the dialplan row of a resource.
func saveDialplan(ctx context.Context, tx pgx.Tx, st *db.Store, r *resource, dom domainRow, row map[string]string) error {
	if row["dialplan_uuid"] == "" || !reUUID.MatchString(row["dialplan_uuid"]) {
		row["dialplan_uuid"] = newUUID()
		if _, err := tx.Exec(ctx, fmt.Sprintf(`update %s set dialplan_uuid = $1::uuid where %s = $2::uuid`, r.table, r.key), row["dialplan_uuid"], row[r.key]); err != nil {
			return err
		}
	}
	ctxName := r.dpContext
	if ctxName == "" {
		ctxName = dom.name
	}
	order := 100
	if r.dpOrder != nil {
		order = r.dpOrder(row)
	}
	if r.name == "dids" {
		if err := didFaxTarget(ctx, tx, st, dom, row); err != nil {
			return err
		}
	}
	enabled := r.enabledCol == "" || row[r.enabledCol] == "" || truthy(row[r.enabledCol])
	if r.name == "dids" && strings.TrimSpace(row["action"]) == "" {
		enabled = false // a number not linked to anything yet
	}
	number := ""
	if r.numberCol != "" {
		number = row[r.numberCol]
	}
	return upsertDialplan(ctx, tx, st, dialplanRow{uuid: row["dialplan_uuid"], domainUUID: dom.uuid, context: ctxName,
		name: row[r.nameCol], number: number, order: order, enabled: enabled, xml: r.dialplan(dom, row),
		description: "Managed by RingQswitch (" + r.name + ")"})
}

// didFaxTarget marks a DID transferred to a fax extension of its domain,
// or sent to eFax for an extension ("efax:<extension>"), so its dialplan
// answers as a fax machine (didFaxDialplan).
func didFaxTarget(ctx context.Context, tx pgx.Tx, st *db.Store, dom domainRow, row map[string]string) error {
	delete(row, didFaxKey)
	delete(row, didFaxExt)
	delete(row, didFaxIdent)
	if row["destination_app"] == "efax" {
		if f := strings.Fields(row["destination_data"]); len(f) > 0 {
			row[didFaxExt] = f[0]
		}
		return nil
	}
	if row["destination_app"] != "transfer" || !st.HasTable("v_fax") {
		return nil
	}
	f := strings.Fields(row["destination_data"])
	if len(f) == 0 || len(f) >= 3 && f[2] != dom.name {
		return nil
	}
	var id, ident string
	err := tx.QueryRow(ctx, `select fax_uuid::text, coalesce(fax_caller_id_number, '') from v_fax where domain_uuid = $1::uuid
		and fax_extension = $2 and fax_uuid::text <> $3 limit 1`, dom.uuid, f[0], row[didFaxSkip]).Scan(&id, &ident)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	row[didFaxKey], row[didFaxExt], row[didFaxIdent] = id, f[0], ident
	return nil
}

// refreshFaxDIDs rewrites the dialplans of the DIDs that go to these fax
// extension numbers (after one changes or goes; skip = the one going).
func refreshFaxDIDs(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, skip string, exts ...string) error {
	dids := resourceByName("dids")
	if !s.store.HasTable(dids.table) {
		return nil
	}
	rows, err := jsonRowsTx(ctx, tx, `select to_jsonb(t) from v_destinations t where domain_uuid = $1::uuid and destination_app = 'transfer'
		and split_part(coalesce(destination_data, ''), ' ', 1) = any($2)`, dom.uuid, exts)
	if err != nil {
		return err
	}
	for _, row := range rows {
		dids.decorate(row)
		row[didFaxSkip] = skip
		if err := saveDialplan(ctx, tx, s.store, dids, dom, row); err != nil {
			return err
		}
	}
	return nil
}

func checkNumber(ctx context.Context, tx pgx.Tx, st *db.Store, dom domainRow, number, table, id string) error {
	if number == "" {
		return nil
	}
	for _, t := range numberTables {
		if !st.HasTable(t.table) {
			continue
		}
		var n int
		q := fmt.Sprintf(`select count(*) from %s where domain_uuid = $1::uuid and %s = $2 and %s <> $3::uuid`, t.table, t.col, t.key)
		if err := tx.QueryRow(ctx, q, dom.uuid, number, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return badRequest("number %s is already used by a %s", number, t.what)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// SQL helpers (every value is sent as text and cast to the column type)

func castParam(st *db.Store, table, col string, n int) string {
	p := "$" + strconv.Itoa(n)
	switch t := st.ColumnType(table, col); t {
	case "", "text", "character varying", "USER-DEFINED", "ARRAY":
		return p
	default:
		return p + "::text::" + t
	}
}

func isTextType(t string) bool {
	return t == "text" || t == "character varying" || t == "character"
}

func insertRow(ctx context.Context, tx pgx.Tx, st *db.Store, table string, row map[string]string) error {
	var cols, vals []string
	var args []any
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := row[k]
		if v == "" || !st.HasColumn(table, k) {
			continue
		}
		args = append(args, v)
		cols = append(cols, pgx.Identifier{k}.Sanitize())
		vals = append(vals, castParam(st, table, k, len(args)))
	}
	_, err := tx.Exec(ctx, fmt.Sprintf("insert into %s (%s) values (%s)", pgx.Identifier{table}.Sanitize(),
		strings.Join(cols, ", "), strings.Join(vals, ", ")), args...)
	return dbErr(err)
}

func updateRow(ctx context.Context, tx pgx.Tx, st *db.Store, table, key, id string, row map[string]string) error {
	var sets []string
	args := []any{id}
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := row[k]
		if k == key || !st.HasColumn(table, k) {
			continue
		}
		t := st.ColumnType(table, k)
		if v == "" && !isTextType(t) {
			if t == "uuid" || strings.HasPrefix(t, "timestamp") || t == "json" || t == "jsonb" {
				sets = append(sets, pgx.Identifier{k}.Sanitize()+" = null")
			}
			continue
		}
		args = append(args, v)
		sets = append(sets, pgx.Identifier{k}.Sanitize()+" = "+castParam(st, table, k, len(args)))
	}
	if len(sets) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, fmt.Sprintf("update %s set %s where %s = $1::uuid", pgx.Identifier{table}.Sanitize(),
		strings.Join(sets, ", "), pgx.Identifier{key}.Sanitize()), args...)
	return dbErr(err)
}

// dbErr turns value errors (bad numbers...) into 400s.
func dbErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "invalid input syntax") || strings.Contains(msg, "out of range") || strings.Contains(msg, "SQLSTATE 22") {
		return badRequest("invalid value: %s", msg)
	}
	return err
}

func jsonRowsTx(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]map[string]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var m map[string]any
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		row := map[string]string{}
		for k, v := range m {
			row[k] = toStr(v)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func domainsTx(ctx context.Context, tx pgx.Tx) ([]domainRow, error) {
	rows, err := tx.Query(ctx, `select domain_uuid::text, domain_name from v_domains order by domain_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domainRow
	for rows.Next() {
		var d domainRow
		if err := rows.Scan(&d.uuid, &d.name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case []any, map[string]any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "t", "1", "yes", "on":
		return true
	}
	return false
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return int(f)
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

const passwordChars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomPassword(n int) string {
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(passwordChars))))
		b[i] = passwordChars[k.Int64()]
	}
	return string(b)
}
