package db

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// Overlay loads the database configuration into cfg, which already holds
// the XML configuration. Database domains, users and gateways are added
// (replacing XML entries with the same name); database dialplan contexts
// replace XML contexts of the same name.
func (s *Store) Overlay(ctx context.Context, cfg *config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	warn := func(format string, args ...any) {
		cfg.Warnings = append(cfg.Warnings, "database: "+fmt.Sprintf(format, args...))
	}
	if err := s.loadVars(ctx, cfg); err != nil {
		return fmt.Errorf("v_vars: %w", err)
	}
	domains, err := s.loadDomains(ctx, cfg)
	if err != nil {
		return fmt.Errorf("v_domains: %w", err)
	}
	nusers, err := s.loadExtensions(ctx, cfg, domains, warn)
	if err != nil {
		return fmt.Errorf("v_extensions: %w", err)
	}
	ngw, err := s.loadGateways(ctx, cfg, warn)
	if err != nil {
		return fmt.Errorf("v_gateways: %w", err)
	}
	if err := s.loadSIPProfiles(ctx, cfg, warn); err != nil {
		return fmt.Errorf("v_sip_profiles: %w", err)
	}
	ndp, err := s.loadDialplans(ctx, cfg, domains, warn)
	if err != nil {
		return fmt.Errorf("v_dialplans: %w", err)
	}
	if err := s.loadRingGroups(ctx, cfg, domains); err != nil {
		return fmt.Errorf("v_ring_groups: %w", err)
	}
	if err := s.loadIVRMenus(ctx, cfg, domains); err != nil {
		return fmt.Errorf("v_ivr_menus: %w", err)
	}
	if err := s.loadACL(ctx, cfg, warn); err != nil {
		return fmt.Errorf("v_access_controls: %w", err)
	}
	if err := s.loadSecurity(ctx, cfg, warn); err != nil {
		return fmt.Errorf("rq_security_rules: %w", err)
	}
	if err := s.loadGuard(ctx, cfg, warn); err != nil {
		return fmt.Errorf("sip guard: %w", err)
	}
	if err := s.loadDomainRules(ctx, cfg, domains, warn); err != nil {
		return fmt.Errorf("office hours and outbound rules: %w", err)
	}
	if err := s.loadCallCenter(ctx, cfg, domains, warn); err != nil {
		return fmt.Errorf("call center: %w", err)
	}
	if err := s.loadVoicemails(ctx, cfg, domains); err != nil {
		return fmt.Errorf("v_voicemails: %w", err)
	}
	if err := s.loadConferences(ctx, cfg, domains); err != nil {
		return fmt.Errorf("v_conferences: %w", err)
	}
	s.chooseDefaultDomain(cfg, domains)
	f := cfg.Features
	s.log.Info("database configuration loaded", "domains", len(domains), "extensions", nusers, "gateways", ngw,
		"dialplans", ndp, "ring_groups", len(f.RingGroups), "ivr_menus", len(f.IVRMenus), "queues", len(f.Queues),
		"agents", len(f.Agents), "voicemail_boxes", len(f.Voicemails), "conferences", len(f.Conferences),
		"default_domain", cfg.Directory.Domain, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

func secs(v string, def int) time.Duration { return time.Duration(atoi(v, def)) * time.Second }

func (s *Store) loadCallCenter(ctx context.Context, cfg *config.Config, domains map[string]dbDomain, warn func(string, ...any)) error {
	f := cfg.Features
	if s.HasTable("v_call_center_queues") {
		rows, err := s.jsonRows(ctx, `select to_jsonb(q) from v_call_center_queues q`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			d, ok := domains[r["domain_uuid"]]
			if !ok || r["queue_extension"] == "" {
				continue
			}
			q := &config.Queue{
				UUID: r["call_center_queue_uuid"], Domain: d.name, Name: firstNonEmpty(r["queue_name"], r["queue_extension"]),
				Extension: r["queue_extension"], Strategy: strings.ToLower(firstNonEmpty(r["queue_strategy"], "longest-idle-agent")),
				MOHSound: r["queue_moh_sound"], Greeting: r["queue_greeting"], RecordTemplate: r["queue_record_template"],
				MaxWaitTime: secs(r["queue_max_wait_time"], 0), MaxWaitTimeNoAgent: secs(r["queue_max_wait_time_with_no_agent"], 0),
				MaxWaitTimeNoAgentReached: secs(r["queue_max_wait_time_with_no_agent_time_reached"], 5),
				TierRulesApply:            truthy(r["queue_tier_rules_apply"]), TierRuleWaitSecond: secs(r["queue_tier_rule_wait_second"], 300),
				TierRuleWaitMultiplyLevel: truthy(r["queue_tier_rule_wait_multiply_level"]),
				TierRuleNoAgentNoWait:     truthy(r["queue_tier_rule_no_agent_no_wait"]),
				DiscardAbandonedAfter:     secs(r["queue_discard_abandoned_after"], 60),
				AbandonedResumeAllowed:    truthy(r["queue_abandoned_resume_allowed"]),
				TimeoutAction:             r["queue_timeout_action"], CIDPrefix: r["queue_cid_prefix"],
				AnnounceSound: r["queue_announce_sound"], AnnounceFrequency: secs(r["queue_announce_frequency"], 0),
				ExitKeys: r["queue_cc_exit_keys"], MaxCallers: atoi(r["queue_max_callers"], 0),
				OutboundCallerIDName: r["queue_outbound_caller_id_name"], OutboundCallerIDNumber: r["queue_outbound_caller_id_number"],
				RingProgressivelyDelay: 10 * time.Second,
			}
			q.RecordCalls = strings.TrimSpace(q.RecordTemplate) != ""
			f.Queues[strings.ToLower(q.UUID)] = q
		}
	}
	saved := map[string][2]string{} // agent uuid -> status, aux
	if s.HasTable("agents") {
		// the call centre's runtime table (agent name = agent uuid in RingQ)
		rows, err := s.pool.Query(ctx, `select coalesce(name,''), coalesce(status,'') from agents`)
		if err == nil {
			for rows.Next() {
				var n, st string
				if rows.Scan(&n, &st) == nil {
					saved[strings.ToLower(n)] = [2]string{st, ""}
				}
			}
			rows.Close()
		}
	}
	if s.HasTable("rq_agent_state") {
		rows, err := s.pool.Query(ctx, `select call_center_agent_uuid::text, coalesce(status,''), coalesce(aux_code,'') from rq_agent_state`)
		if err == nil {
			for rows.Next() {
				var id, st, aux string
				if rows.Scan(&id, &st, &aux) == nil && st != "" {
					saved[strings.ToLower(id)] = [2]string{st, aux}
				}
			}
			rows.Close()
		}
	}
	if s.HasTable("v_call_center_agents") {
		rows, err := s.jsonRows(ctx, `select to_jsonb(a) from v_call_center_agents a`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			d, ok := domains[r["domain_uuid"]]
			if !ok {
				continue
			}
			a := &config.Agent{
				UUID: r["call_center_agent_uuid"], Domain: d.name, Name: firstNonEmpty(r["agent_name"], r["agent_id"]),
				Type: firstNonEmpty(r["agent_type"], "callback"), AgentID: r["agent_id"], Password: r["agent_password"],
				Contact: r["agent_contact"], UserUUID: r["user_uuid"], Status: firstNonEmpty(r["agent_status"], config.AgentLoggedOut),
				CallTimeout: secs(r["agent_call_timeout"], 20), MaxNoAnswer: atoi(r["agent_max_no_answer"], 0),
				WrapUpTime: secs(r["agent_wrap_up_time"], 10), RejectDelay: secs(r["agent_reject_delay_time"], 10),
				BusyDelay: secs(r["agent_busy_delay_time"], 10), NoAnswerDelay: secs(r["agent_no_answer_delay_time"], 10),
				Record: truthy(r["agent_record"]),
			}
			a.Extension = config.AgentExtension(a.Contact)
			if a.Contact == "" {
				warn("agent %q has no contact; it cannot receive calls", a.Name)
			}
			if st, ok := saved[strings.ToLower(a.UUID)]; ok {
				a.SavedStatus, a.SavedAux = st[0], st[1]
			}
			f.Agents[strings.ToLower(a.UUID)] = a
		}
	}
	if s.HasTable("v_call_center_tiers") {
		rows, err := s.jsonRows(ctx, `select to_jsonb(t) from v_call_center_tiers t order by call_center_queue_uuid, tier_level nulls last, tier_position nulls last`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if f.Queues[strings.ToLower(r["call_center_queue_uuid"])] == nil || f.Agents[strings.ToLower(r["call_center_agent_uuid"])] == nil {
				continue
			}
			f.Tiers = append(f.Tiers, config.Tier{QueueUUID: r["call_center_queue_uuid"], AgentUUID: r["call_center_agent_uuid"],
				Level: atoi(r["tier_level"], 1), Position: atoi(r["tier_position"], 1)})
		}
	}
	if s.HasTable("rq_aux_codes") {
		rows, err := s.jsonRows(ctx, `select to_jsonb(a) from rq_aux_codes a where aux_code_enabled order by aux_code_order, aux_code_name`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			dom := ""
			if d, ok := domains[r["domain_uuid"]]; ok {
				dom = d.name
			} else if r["domain_uuid"] != "" {
				continue
			}
			f.AuxCodes = append(f.AuxCodes, config.AuxCode{UUID: r["aux_code_uuid"], Domain: dom, Name: r["aux_code_name"],
				BlockDirect: truthy(r["block_direct_calls"])})
		}
	}
	return nil
}

func (s *Store) loadVoicemails(ctx context.Context, cfg *config.Config, domains map[string]dbDomain) error {
	if !s.HasTable("v_voicemails") {
		return nil
	}
	rows, err := s.jsonRows(ctx, `select to_jsonb(v) from v_voicemails v`)
	if err != nil {
		return err
	}
	byUUID := map[string]*config.Voicemail{}
	for _, r := range rows {
		d, ok := domains[r["domain_uuid"]]
		if !ok || r["voicemail_id"] == "" {
			continue
		}
		vm := &config.Voicemail{UUID: r["voicemail_uuid"], Domain: d.name, ID: r["voicemail_id"],
			Password: r["voicemail_password"], Enabled: enabled(r["voicemail_enabled"]), MailTo: r["voicemail_mail_to"],
			GreetingID: atoi(r["greeting_id"], 0)}
		cfg.Features.Voicemails[strings.ToLower(vm.ID+"@"+vm.Domain)] = vm
		byUUID[strings.ToLower(vm.UUID)] = vm
	}
	if s.HasTable("v_voicemail_messages") {
		mrows, err := s.pool.Query(ctx, `select voicemail_uuid::text,
			count(*) filter (where coalesce(message_status,'') <> 'saved'),
			count(*) filter (where message_status = 'saved')
			from v_voicemail_messages group by voicemail_uuid`)
		if err != nil {
			return err
		}
		defer mrows.Close()
		for mrows.Next() {
			var id string
			var n, o int
			if mrows.Scan(&id, &n, &o) == nil {
				if vm := byUUID[strings.ToLower(id)]; vm != nil {
					vm.NewMessages, vm.OldMessages = n, o
				}
			}
		}
	}
	return nil
}

func (s *Store) loadConferences(ctx context.Context, cfg *config.Config, domains map[string]dbDomain) error {
	if !s.HasTable("v_conferences") {
		return nil
	}
	rows, err := s.jsonRows(ctx, `select to_jsonb(c) from v_conferences c`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		d, ok := domains[r["domain_uuid"]]
		if !ok || !enabled(r["conference_enabled"]) {
			continue
		}
		c := &config.Conference{UUID: r["conference_uuid"], Domain: d.name, Name: firstNonEmpty(r["conference_name"], r["conference_extension"]),
			Extension: r["conference_extension"], PIN: r["conference_pin_number"], Flags: r["conference_flags"], Enabled: true}
		c.Record = strings.Contains(c.Flags, "record")
		cfg.Features.Conferences[strings.ToLower(c.UUID)] = c
	}
	return nil
}

// hostnameFilter matches rows for this host (or for any host).
func hostnameMatch(row, host string) bool {
	return row == "" || strings.EqualFold(row, host)
}

func (s *Store) loadVars(ctx context.Context, cfg *config.Config) error {
	if !s.HasTable("v_vars") {
		return nil
	}
	rows, err := s.jsonRows(ctx, `select to_jsonb(v) from v_vars v order by var_order nulls last, var_name`)
	if err != nil {
		return err
	}
	added := map[string]bool{}
	for _, r := range rows {
		name := strings.TrimSpace(r["var_name"])
		if name == "" || !enabled(r["var_enabled"]) || !hostnameMatch(r["var_hostname"], s.cfg.Hostname) {
			continue
		}
		if cmd := r["var_command"]; cmd != "" && cmd != "set" {
			continue // exec-set and stun-set run programs: not supported
		}
		if _, defined := cfg.Vars[name]; defined && !added[name] {
			continue // ringqswitch.xml wins
		}
		cfg.Vars[name] = r["var_value"]
		added[name] = true
	}
	// Resolve references between variables ($${us-ring} inside ringback).
	for i := 0; i < 3; i++ {
		for name := range added {
			v, _ := config.ExpandGlobals([]byte(cfg.Vars[name]), cfg.Vars)
			cfg.Vars[name] = unescapeXML(string(v))
		}
	}
	return nil
}

// unescapeXML undoes the escaping ExpandGlobals applies for XML documents.
func unescapeXML(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&#34;", `"`, "&#39;", "'", "&quot;", `"`, "&apos;", "'")
	return r.Replace(s)
}

// dbDomain is a loaded domain.
type dbDomain struct {
	uuid string
	name string
}

func (s *Store) loadDomains(ctx context.Context, cfg *config.Config) (map[string]dbDomain, error) {
	rows, err := s.jsonRows(ctx, `select to_jsonb(d) from v_domains d`)
	if err != nil {
		return nil, err
	}
	out := map[string]dbDomain{} // by uuid
	for _, r := range rows {
		name := strings.ToLower(strings.TrimSpace(r["domain_name"]))
		if name == "" || !enabled(r["domain_enabled"]) {
			continue
		}
		out[r["domain_uuid"]] = dbDomain{uuid: r["domain_uuid"], name: name}
		cfg.Directory.AddDomain(name, r["domain_uuid"])
	}
	return out, nil
}

func (s *Store) chooseDefaultDomain(cfg *config.Config, domains map[string]dbDomain) {
	dir := cfg.Directory
	if want := s.cfg.DefaultDomain; want != "" {
		if dir.Find(want) != nil {
			dir.SetDefault(want)
		} else {
			cfg.Warnings = append(cfg.Warnings, "database: default-domain "+want+" is not in v_domains")
		}
		return
	}
	// The XML default domain (typically the server IP) has no users when
	// everything lives in the database: with a single database domain,
	// phones configured with the IP address register to it.
	if def := dir.Default(); len(def.Users) == 0 && len(domains) == 1 {
		for _, d := range domains {
			old := def.Name
			dir.SetDefault(d.name)
			dir.Remove(old)
		}
	}
}

// Extension columns that are not directory variables.
var skipExtensionColumns = map[string]bool{
	"password": true, "insert_date": true, "insert_user": true, "update_date": true, "update_user": true,
	"dial_string": true, "unique_id": true,
}

func (s *Store) loadExtensions(ctx context.Context, cfg *config.Config, domains map[string]dbDomain, warn func(string, ...any)) (int, error) {
	rows, err := s.jsonRows(ctx, `select to_jsonb(e) from v_extensions e order by extension`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		d, ok := domains[r["domain_uuid"]]
		ext := strings.TrimSpace(r["extension"])
		if !ok || ext == "" {
			continue
		}
		if r["password"] == "" {
			warn("extension %s@%s has no password; skipped", ext, d.name)
			continue
		}
		u := &config.User{
			ID: ext, UUID: r["extension_uuid"], Password: r["password"],
			Disabled: !enabled(r["enabled"]),
			Vars:     map[string]string{}, Params: map[string]string{"password": r["password"]},
		}
		if n, err := strconv.Atoi(r["max_registrations"]); err == nil && n > 0 {
			u.MaxRegistrations = n
		}
		for k, v := range r {
			if !skipExtensionColumns[k] && v != "" {
				u.Vars[k] = v
			}
		}
		// The variables the RingQ portal's directory handler sets.
		u.Vars["domain_name"] = d.name
		u.Vars["domain_uuid"] = d.uuid
		u.Vars["extension_uuid"] = r["extension_uuid"]
		u.Vars["user_context"] = firstNonEmpty(r["user_context"], d.name)
		u.Vars["effective_caller_id_name"] = firstNonEmpty(r["effective_caller_id_name"], ext)
		u.Vars["effective_caller_id_number"] = firstNonEmpty(r["effective_caller_id_number"], ext)
		if full := strings.TrimSpace(r["directory_first_name"] + " " + r["directory_last_name"]); full != "" {
			u.Vars["directory_full_name"] = full
		}
		if lang := r["extension_language"]; lang != "" {
			u.Vars["default_language"] = lang
		}
		if alias := r["number_alias"]; alias != "" {
			u.Params["number-alias"] = alias
		}
		if mr := r["max_registrations"]; mr != "" {
			u.Params["max-registrations-per-extension"] = mr
		}
		dom := cfg.Directory.AddDomain(d.name, d.uuid)
		dom.Put(u)
		if alias := r["number_alias"]; alias != "" {
			dom.AddAlias(alias, u)
		}
		n++
	}
	return n, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

func (s *Store) loadGateways(ctx context.Context, cfg *config.Config, warn func(string, ...any)) (int, error) {
	if !s.HasTable("v_gateways") {
		return 0, nil
	}
	rows, err := s.jsonRows(ctx, `select to_jsonb(g) from v_gateways g order by gateway`)
	if err != nil {
		return 0, err
	}
	byName := map[string]int{}
	for i, g := range cfg.Gateways {
		byName[g.Name] = i
	}
	n := 0
	for _, r := range rows {
		if !enabled(r["enabled"]) || !hostnameMatch(r["hostname"], s.cfg.Hostname) {
			continue
		}
		g := config.Gateway{
			Name:           r["gateway_uuid"],
			UUID:           r["gateway_uuid"],
			Label:          r["gateway"],
			Proxy:          strings.TrimPrefix(r["proxy"], "sip:"),
			OutboundProxy:  strings.TrimPrefix(r["outbound_proxy"], "sip:"),
			RegisterProxy:  strings.TrimPrefix(r["register_proxy"], "sip:"),
			AuthUsername:   r["auth_username"],
			Transport:      strings.ToLower(firstNonEmpty(r["register_transport"], "udp")),
			Username:       r["username"],
			Password:       r["password"],
			Register:       truthy(r["register"]),
			Expire:         atoi(r["expire_seconds"], 800),
			RetrySeconds:   atoi(r["retry_seconds"], 30),
			Context:        firstNonEmpty(r["context"], "public"),
			Extension:      r["extension"],
			CallerIDInFrom: truthy(r["caller_id_in_from"]),
			Profile:        r["profile"],
		}
		if g.Proxy == "" {
			g.Proxy = strings.TrimPrefix(r["realm"], "sip:")
		}
		g.Realm = firstNonEmpty(r["realm"], hostOnly(g.Proxy))
		g.FromUser = firstNonEmpty(r["from_user"], g.Username)
		g.FromDomain = firstNonEmpty(r["from_domain"], g.Realm)
		label := firstNonEmpty(g.Label, g.Name)
		switch {
		case g.Name == "" || g.Proxy == "":
			warn("gateway %q has no proxy; skipped", label)
			continue
		case g.Transport != "udp" && g.Transport != "tcp":
			warn("gateway %q: transport %s is not supported yet; using udp", label, g.Transport)
			g.Transport = "udp"
		}
		if g.Register && g.Username == "" {
			warn("gateway %q: register=true without username; not registering", label)
			g.Register = false
		}
		if g.RetrySeconds < 5 {
			g.RetrySeconds = 5
		}
		if g.Expire < 60 {
			g.Expire = 60
		}
		if i, dup := byName[g.Name]; dup {
			cfg.Gateways[i] = g
		} else {
			byName[g.Name] = len(cfg.Gateways)
			cfg.Gateways = append(cfg.Gateways, g)
		}
		n++
	}
	return n, nil
}

// Default ports of the RingQ portal's vars.xml, for profiles that refer to them.
var profilePortVars = map[string]string{"internal_sip_port": "5060", "external_sip_port": "5080", "internal_tls_port": "5061", "external_tls_port": "5081"}

// loadSIPProfiles reads the RingQ portal's SIP profiles (v_sip_profiles and their
// settings): name, address, port, external addresses.
func (s *Store) loadSIPProfiles(ctx context.Context, cfg *config.Config, warn func(string, ...any)) error {
	if !s.HasTable("v_sip_profiles") || !s.HasTable("v_sip_profile_settings") {
		return nil
	}
	rows, err := s.jsonRows(ctx, `select to_jsonb(p) from v_sip_profiles p order by sip_profile_name`)
	if err != nil {
		return err
	}
	sets, err := s.jsonRows(ctx, `select to_jsonb(x) from v_sip_profile_settings x order by sip_profile_setting_name`)
	if err != nil {
		return err
	}
	vars := map[string]string{}
	for k, v := range profilePortVars {
		vars[k] = v
	}
	for k, v := range cfg.Vars {
		vars[k] = v
	}
	value := func(v string) string {
		out, _ := config.ExpandGlobals([]byte(v), vars)
		v = strings.TrimSpace(string(out))
		if strings.Contains(v, "$${") || strings.Contains(v, "${") {
			return ""
		}
		return v
	}
	for _, r := range rows {
		if !hostnameMatch(r["sip_profile_hostname"], s.cfg.Hostname) {
			continue
		}
		p := config.SIPProfile{UUID: r["sip_profile_uuid"], Name: r["sip_profile_name"], Description: r["sip_profile_description"],
			Enabled: enabled(r["sip_profile_enabled"]), TCP: true}
		for _, x := range sets {
			if x["sip_profile_uuid"] != p.UUID || !enabled(x["sip_profile_setting_enabled"]) {
				continue
			}
			v := value(x["sip_profile_setting_value"])
			switch x["sip_profile_setting_name"] {
			case "sip-ip":
				if ip := net.ParseIP(v); ip != nil {
					p.IP = v
				}
			case "sip-port":
				p.Port = atoi(v, 0)
			case "ext-sip-ip":
				if net.ParseIP(v) != nil {
					p.ExtSIPIP = v
				}
			case "ext-rtp-ip":
				if net.ParseIP(v) != nil {
					p.ExtRTPIP = v
				}
			case "tls":
				p.TLS = truthy(v)
			case "bind-params":
				if strings.Contains(v, "transport=udp") && !strings.Contains(v, "tcp") {
					p.TCP = false
				}
			}
		}
		if p.Port == 0 {
			p.Port = atoi(profilePortVars[p.Name+"_sip_port"], 0)
		}
		if p.Name == "" || p.Port <= 0 || p.Port > 65535 {
			if p.Enabled {
				warn("SIP profile %q has no usable sip-port; it is not opened", p.Name)
			}
			continue
		}
		cfg.Profiles = append(cfg.Profiles, p)
	}
	return nil
}

func hostOnly(hostport string) string {
	if i := strings.LastIndexByte(hostport, ':'); i > 0 && !strings.Contains(hostport[i:], "]") {
		return hostport[:i]
	}
	return hostport
}

func (s *Store) loadDialplans(ctx context.Context, cfg *config.Config, domains map[string]dbDomain, warn func(string, ...any)) (int, error) {
	if !s.HasTable("v_dialplans") {
		return 0, nil
	}
	rows, err := s.jsonRows(ctx, `select to_jsonb(d) from v_dialplans d
		order by dialplan_order nulls last, dialplan_name, dialplan_uuid`)
	if err != nil {
		return 0, err
	}
	allDomains := make([]string, 0, len(domains))
	for _, d := range domains {
		allDomains = append(allDomains, d.name)
	}
	sort.Strings(allDomains)
	type entry struct {
		contexts []string
		xml      string
		source   string
	}
	var entries []entry
	replaced := map[string]bool{}
	for _, r := range rows {
		if !enabled(r["dialplan_enabled"]) || !hostnameMatch(r["hostname"], s.cfg.Hostname) {
			continue
		}
		xml := strings.TrimSpace(r["dialplan_xml"])
		name := firstNonEmpty(r["dialplan_name"], r["dialplan_uuid"])
		if xml == "" {
			warn("dialplan %q has no dialplan_xml; skipped (save it again in the UI to generate it)", name)
			continue
		}
		ctxName := strings.TrimSpace(r["dialplan_context"])
		var contexts []string
		switch {
		case ctxName == "${domain_name}" || ctxName == "global":
			if d, ok := domains[r["domain_uuid"]]; ok {
				contexts = []string{d.name}
			} else {
				contexts = allDomains
			}
		case ctxName == "":
			if d, ok := domains[r["domain_uuid"]]; ok {
				contexts = []string{d.name}
			}
		default:
			contexts = []string{ctxName}
		}
		for _, c := range contexts {
			replaced[c] = true
		}
		entries = append(entries, entry{contexts: contexts, xml: xml, source: "v_dialplans " + name + " (" + r["dialplan_uuid"] + ")"})
	}
	// Database contexts are authoritative: drop XML contexts with the same
	// name (the sample "public" context, for instance).
	for c := range replaced {
		delete(cfg.Dialplan.Contexts, c)
	}
	n := 0
	for _, e := range entries {
		data, _ := config.ExpandGlobals([]byte(e.xml), cfg.Vars)
		for _, c := range e.contexts {
			if _, err := cfg.Dialplan.LoadExtensions(c, strings.NewReader(string(data)), e.source); err != nil {
				warn("%v", err)
				continue
			}
		}
		n++
	}
	return n, nil
}

func (s *Store) loadRingGroups(ctx context.Context, cfg *config.Config, domains map[string]dbDomain) error {
	if !s.HasTable("v_ring_groups") {
		return nil
	}
	groups, err := s.jsonRows(ctx, `select to_jsonb(r) from v_ring_groups r`)
	if err != nil {
		return err
	}
	var dests []map[string]string
	if s.HasTable("v_ring_group_destinations") {
		if dests, err = s.jsonRows(ctx, `select to_jsonb(d) from v_ring_group_destinations d
			order by destination_delay nulls first, destination_number`); err != nil {
			return err
		}
	}
	byUUID := map[string]*config.RingGroup{}
	for _, r := range groups {
		d, ok := domains[r["domain_uuid"]]
		if !ok {
			continue
		}
		rg := &config.RingGroup{
			UUID: r["ring_group_uuid"], Domain: d.name, Name: r["ring_group_name"],
			Extension: r["ring_group_extension"], Context: firstNonEmpty(r["ring_group_context"], d.name),
			Strategy:    strings.ToLower(firstNonEmpty(r["ring_group_strategy"], "simultaneous")),
			CallTimeout: atoi(r["ring_group_call_timeout"], 0), Greeting: r["ring_group_greeting"],
			ForwardEnabled: truthy(r["ring_group_forward_enabled"]), ForwardDestination: r["ring_group_forward_destination"],
			CallerIDName: r["ring_group_caller_id_name"], CallerIDNumber: r["ring_group_caller_id_number"],
			CIDNamePrefix: r["ring_group_cid_name_prefix"], CIDNumberPrefix: r["ring_group_cid_number_prefix"],
			TimeoutApp: r["ring_group_timeout_app"], TimeoutData: r["ring_group_timeout_data"],
			Ringback: r["ring_group_ringback"], Enabled: enabled(r["ring_group_enabled"]),
		}
		byUUID[rg.UUID] = rg
		cfg.Features.RingGroups[strings.ToLower(rg.UUID)] = rg
	}
	for _, r := range dests {
		rg := byUUID[r["ring_group_uuid"]]
		if rg == nil || !enabled(r["destination_enabled"]) || strings.TrimSpace(r["destination_number"]) == "" {
			continue
		}
		rg.Destinations = append(rg.Destinations, config.RingGroupDestination{
			Number: strings.TrimSpace(r["destination_number"]), Delay: atoi(r["destination_delay"], 0),
			Timeout: atoi(r["destination_timeout"], 30), Prompt: truthy(r["destination_prompt"]),
		})
	}
	return nil
}

func (s *Store) loadIVRMenus(ctx context.Context, cfg *config.Config, domains map[string]dbDomain) error {
	if !s.HasTable("v_ivr_menus") {
		return nil
	}
	menus, err := s.jsonRows(ctx, `select to_jsonb(m) from v_ivr_menus m`)
	if err != nil {
		return err
	}
	var opts []map[string]string
	if s.HasTable("v_ivr_menu_options") {
		if opts, err = s.jsonRows(ctx, `select to_jsonb(o) from v_ivr_menu_options o
			order by ivr_menu_option_order nulls last, ivr_menu_option_digits`); err != nil {
			return err
		}
	}
	ms := func(v string, def int) time.Duration { return time.Duration(atoi(v, def)) * time.Millisecond }
	byUUID := map[string]*config.IVRMenu{}
	for _, r := range menus {
		d, ok := domains[r["domain_uuid"]]
		if !ok {
			continue
		}
		m := &config.IVRMenu{
			UUID: r["ivr_menu_uuid"], ParentUUID: r["ivr_menu_parent_uuid"], Domain: d.name, Name: r["ivr_menu_name"],
			Extension: r["ivr_menu_extension"], Context: firstNonEmpty(r["ivr_menu_context"], d.name),
			GreetLong: r["ivr_menu_greet_long"], GreetShort: r["ivr_menu_greet_short"],
			InvalidSound: r["ivr_menu_invalid_sound"], ExitSound: r["ivr_menu_exit_sound"],
			Timeout: ms(r["ivr_menu_timeout"], 3000), InterDigitTimeout: ms(r["ivr_menu_inter_digit_timeout"], 2000),
			MaxFailures: atoi(r["ivr_menu_max_failures"], 3), MaxTimeouts: atoi(r["ivr_menu_max_timeouts"], 3),
			DigitLen: atoi(r["ivr_menu_digit_len"], 5), DirectDial: truthy(r["ivr_menu_direct_dial"]),
			ExitApp: r["ivr_menu_exit_app"], ExitData: r["ivr_menu_exit_data"], CIDPrefix: r["ivr_menu_cid_prefix"],
			Ringback: r["ivr_menu_ringback"], Enabled: enabled(r["ivr_menu_enabled"]),
		}
		if !m.Enabled {
			continue
		}
		byUUID[m.UUID] = m
		cfg.Features.IVRMenus[strings.ToLower(m.UUID)] = m
	}
	for _, r := range opts {
		m := byUUID[r["ivr_menu_uuid"]]
		if m == nil || !enabled(r["ivr_menu_option_enabled"]) {
			continue
		}
		m.Options = append(m.Options, config.IVROption{
			Digits: strings.TrimSpace(r["ivr_menu_option_digits"]), Action: strings.TrimSpace(r["ivr_menu_option_action"]),
			Param: strings.TrimSpace(r["ivr_menu_option_param"]),
		})
	}
	return nil
}

// loadACL trusts the allow nodes of the RingQ portal's "providers" access list:
// calls from carriers there go to the public context without
// authentication, as on the standard external profile.
// ParseCIDR accepts an address or a network ("203.0.113.7", "203.0.113.0/24").
func ParseCIDR(v string) (netip.Prefix, error) {
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "/") {
		a, err := netip.ParseAddr(v)
		if err != nil {
			return netip.Prefix{}, err
		}
		return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
	}
	p, err := netip.ParsePrefix(v)
	if err != nil {
		return netip.Prefix{}, err
	}
	return p.Masked(), nil
}

func (s *Store) loadSecurity(ctx context.Context, cfg *config.Config, warn func(string, ...any)) error {
	if !s.HasTable("rq_security_rules") {
		return nil
	}
	rows, err := s.pool.Query(ctx, `select rule_type, rule_value from rq_security_rules where enabled order by insert_date`)
	if err != nil {
		return err
	}
	defer rows.Close()
	sec := &cfg.Security
	for rows.Next() {
		var typ, val string
		if err := rows.Scan(&typ, &val); err != nil {
			return err
		}
		if typ == "ua-deny" {
			if v := strings.ToLower(strings.TrimSpace(val)); v != "" {
				sec.UADeny = append(sec.UADeny, v)
			}
			continue
		}
		p, err := ParseCIDR(val)
		if err != nil {
			warn("security rule %s %q: %v", typ, val, err)
			continue
		}
		switch typ {
		case "ip-deny":
			sec.Deny = append(sec.Deny, p)
		case "ip-allow":
			sec.Allow = append(sec.Allow, p)
		case "web-allow":
			sec.WebAllow = append(sec.WebAllow, p)
		case "web-deny":
			sec.WebDeny = append(sec.WebDeny, p)
		}
	}
	return rows.Err()
}

func (s *Store) loadACL(ctx context.Context, cfg *config.Config, warn func(string, ...any)) error {
	if !s.HasTable("v_access_controls") || !s.HasTable("v_access_control_nodes") {
		return nil
	}
	rows, err := s.pool.Query(ctx, `select coalesce(n.node_type,''), coalesce(n.node_cidr,''), coalesce(n.node_description,'')
		from v_access_control_nodes n join v_access_controls a using (access_control_uuid)
		where lower(a.access_control_name) = 'providers'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, cidr, desc string
		if err := rows.Scan(&typ, &cidr, &desc); err != nil {
			return err
		}
		if !strings.EqualFold(typ, "allow") || cidr == "" {
			continue
		}
		if !strings.Contains(cidr, "/") {
			if a, err := netip.ParseAddr(cidr); err == nil {
				cidr = fmt.Sprintf("%s/%d", cidr, a.BitLen())
			}
		}
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			warn("providers acl node %q: %v", cidr, err)
			continue
		}
		cfg.ACL = append(cfg.ACL, config.ACLEntry{Name: firstNonEmpty(desc, "providers"), Prefix: p.Masked(), Context: "public"})
	}
	return rows.Err()
}
