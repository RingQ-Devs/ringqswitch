package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/db"
)

// The Security page: blocked addresses, permanent block / allow rules,
// scanner User-Agents, the addresses allowed to open the web interface,
// and the carriers' trusted addresses (RingQ "providers" ACL).

var ruleTypes = map[string]string{
	"ip-deny":   "Block SIP from this address or network",
	"ip-allow":  "Never block this address (your office, your phones)",
	"web-allow": "Only these addresses may open the web interface",
	"web-deny":  "These addresses may not open the web interface",
	"ua-deny":   "Block SIP scanners with this User-Agent",
}

// defaultScanners are User-Agents of common SIP attack tools.
var defaultScanners = []string{"friendly-scanner", "sipvicious", "sipcli", "sip-scan", "vaxsipuseragent", "pplsip", "sundayddr", "iwar", "sipscan", "smap"}

// webAllowed applies the web-allow rules (none = everyone may connect).
func (s *Server) webAllowed(r *http.Request) bool {
	list := s.sw.Config().Security.WebAllow
	if len(list) == 0 {
		return true
	}
	ip, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return true
	}
	for _, p := range list {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// webDenied applies the web-deny rules.
func (s *Server) webDenied(ip string) bool {
	list := s.sw.Config().Security.WebDeny
	if len(list) == 0 {
		return false
	}
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Unmap().IsLoopback() {
		return false
	}
	for _, p := range list {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// webGuard closes the web interface to banned and denied addresses and
// counts every request (Web Traffic Protection).
func (s *Server) webGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if b := s.sw.GuardBanned(ip, "web"); b != nil {
			httpError(w, http.StatusForbidden, "your address is banned until "+b.Until.Format("2006-01-02 15:04")+" (SIP Guard)")
			return
		}
		if s.webDenied(ip) {
			httpError(w, http.StatusForbidden, "access from your address is not allowed (Security settings)")
			return
		}
		s.sw.GuardFail(config.JailWebTraffic, ip, "too many web requests")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// count404 counts pages that do not exist (Web Request Protection).
func (s *Server) count404(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.code == http.StatusNotFound {
			s.sw.GuardFail(config.JailWebRequest, clientIP(r), "page not found: "+truncate(r.URL.Path, 80))
		}
	})
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

// jailInfo describes the protections as RingQ's SIP Guard page does.
var jailInfo = map[string][2]string{
	config.JailSIPRequest:      {"SIP Request Protection", "Blocks IPs sending SIP requests to non-existent extensions or routes — typically dial plan probing for valid numbers."},
	config.JailSIPRegistration: {"SIP Registration Protection", "Blocks IPs that repeatedly fail SIP registration — protects against brute force attempts to compromise extension credentials."},
	config.JailWebRequest:      {"Web Request Protection", "Blocks IPs hammering the web server for non-existent paths — typically vulnerability scanners probing for known exploits."},
	config.JailWebTraffic:      {"Web Traffic Protection", "Blocks IPs sending excessive HTTP traffic in a short window — protects the web tier from denial of service."},
	config.JailSIPPassword:     {"SIP Password Protection", "Blocks IPs repeatedly registering an existing extension with the wrong password — password brute force against SIP accounts."},
	config.JailWebLogin:        {"Admin Login Protection", "Blocks IPs failing to sign in to this web interface — protects the administrator and user accounts from brute force."},
	config.JailSIPInvite:       {"SIP Invite Protection", "Blocks IPs flooding the system with SIP INVITE requests — typically toll fraud or call spam attacks."},
}

func (s *Server) guardState() map[string]any {
	cfg := s.sw.Config()
	g := config.DefaultGuard(cfg.Settings)
	if cfg.Security.Guard != nil {
		g = *cfg.Security.Guard
	}
	byJail := map[string][]map[string]any{}
	ips := map[string]bool{}
	for _, b := range s.sw.GuardBans() {
		byJail[b.Jail] = append(byJail[b.Jail], map[string]any{"ip": b.IP.String(), "since": b.Since.Unix(), "until": b.Until.Unix(),
			"reason": b.Reason, "source": b.Source, "failures": b.Failures})
		ips[b.IP.String()] = true
	}
	jails := []map[string]any{}
	for _, name := range config.JailOrder {
		j := g.Jails[name]
		banned := byJail[name]
		if banned == nil {
			banned = []map[string]any{}
		}
		jails = append(jails, map[string]any{"name": name, "title": jailInfo[name][0], "description": jailInfo[name][1],
			"area": config.JailArea(name), "enabled": j.Enabled, "max": j.MaxFailures, "window": int(j.Window.Seconds()),
			"ban": int(j.BanTime.Seconds()), "banned": banned})
	}
	return map[string]any{"enabled": g.Enabled, "jails": jails, "banned_ips": len(ips), "ssh": sshGuardSettings("/etc/fail2ban")}
}

func (s *Server) security(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, parts []string) error {
	what, id := part(parts, 0), part(parts, 1)
	switch {
	case what == "guard" && r.Method == http.MethodPut:
		var body db.GuardSettings
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		for name, j := range body.Jails {
			if _, ok := jailInfo[name]; !ok {
				return badRequest("unknown protection %q", name)
			}
			if j.Max < 1 || j.Max > 1000000 || j.Window < 1 || j.Window > 7*86400 || j.Ban < 60 || j.Ban > 365*86400 {
				return badRequest("%s: failures at least 1, window 1 second to 7 days, ban 1 minute to 1 year", jailInfo[name][0])
			}
		}
		// Keep the protections not sent as they are.
		cur := s.guardState()
		full := db.GuardSettings{Enabled: body.Enabled, Jails: map[string]db.JailSettings{}}
		for _, j := range cur["jails"].([]map[string]any) {
			name := j["name"].(string)
			full.Jails[name] = db.JailSettings{Enabled: j["enabled"].(bool), Max: j["max"].(int), Window: j["window"].(int), Ban: j["ban"].(int)}
		}
		for name, j := range body.Jails {
			full.Jails[name] = j
		}
		b, _ := json.Marshal(full)
		if err := s.store.SetSetting(ctx, "server", "sip_guard", string(b)); err != nil {
			return err
		}
		s.reload()
		s.sw.RecordEvent("security", "SIP Guard settings changed by "+p.key())
		writeJSON(w, s.guardState())
		return nil
	case what == "restrict" && r.Method == http.MethodPut:
		var body struct{ On bool }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if err := s.store.SetSetting(ctx, "server", "restrict_sip", strconv.FormatBool(body.On)); err != nil {
			return err
		}
		s.reload()
		s.sw.RecordEvent("security", "global IP restriction turned "+map[bool]string{true: "on", false: "off"}[body.On]+" by "+p.key())
		writeJSON(w, map[string]bool{"on": body.On})
		return nil
	case what == "ban" && r.Method == http.MethodPost:
		var body struct{ IP, Jail, Reason string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		ip, err := netip.ParseAddr(strings.TrimSpace(body.IP))
		if err != nil {
			return badRequest("IP address: e.g. 203.0.113.7")
		}
		if _, ok := jailInfo[body.Jail]; !ok {
			return badRequest("choose the protection area")
		}
		if own, err := netip.ParseAddr(clientIP(r)); err == nil && own.Unmap() == ip.Unmap() {
			return badRequest("that is your own address")
		}
		if ip.Unmap().IsLoopback() {
			return badRequest("this server cannot be banned")
		}
		if _, err := s.sw.GuardBan(ip.String(), body.Jail, firstNonEmpty(body.Reason, "banned by "+p.key()), p.key()); err != nil {
			return badRequest("%v", err)
		}
		writeJSON(w, s.guardState())
		return nil
	case what == "unban" && r.Method == http.MethodPost:
		var body struct{ IP, Jail string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if !s.sw.GuardUnban(body.IP, body.Jail, p.key()) {
			return notFound("ban")
		}
		writeJSON(w, s.guardState())
		return nil
	case what == "history" && r.Method == http.MethodGet:
		return s.guardHistory(ctx, w, r)
	case what == "" && r.Method == http.MethodGet:
		rules, err := s.store.JSONRows(ctx, `select jsonb_build_object('uuid', rule_uuid, 'type', rule_type, 'value', rule_value,
			'description', coalesce(description, ''), 'enabled', enabled, 'created', insert_date) from rq_security_rules
			order by rule_type, insert_date`)
		if err != nil {
			return err
		}
		if rules == nil {
			rules = []map[string]string{}
		}
		hits := s.sw.SecurityHits()
		for _, rl := range rules {
			key := rl["type"] + " " + strings.ToLower(rl["value"])
			if rl["type"] != "ua-deny" {
				if pf, err := db.ParseCIDR(rl["value"]); err == nil {
					key = rl["type"] + " " + pf.String()
				}
			}
			rl["hits"] = toStr(float64(hits[key]))
		}
		blocked := []map[string]any{}
		for ip, until := range s.sw.Blocked() {
			blocked = append(blocked, map[string]any{"ip": ip, "until": until.Unix()})
		}
		sort.Slice(blocked, func(i, j int) bool { return blocked[i]["ip"].(string) < blocked[j]["ip"].(string) })
		acl := []map[string]string{}
		if s.store.HasTable("v_access_control_nodes") {
			acl, err = s.store.JSONRows(ctx, `select jsonb_build_object('uuid', n.access_control_node_uuid, 'type', coalesce(n.node_type, ''),
				'cidr', coalesce(n.node_cidr, ''), 'description', coalesce(n.node_description, ''))
				from v_access_control_nodes n join v_access_controls a using (access_control_uuid)
				where lower(a.access_control_name) = 'providers' order by n.node_cidr`)
			if err != nil {
				return err
			}
			if acl == nil {
				acl = []map[string]string{}
			}
		}
		st := s.sw.Config().Settings
		writeJSON(w, map[string]any{"rules": rules, "types": ruleTypes, "blocked": blocked, "acl": acl, "your_ip": clientIP(r),
			"events": s.sw.EventLog("security", "", 200), "guard": s.guardState(), "restrict_sip": s.sw.Config().Security.RestrictSIP,
			"settings": map[string]any{"auth_fail_threshold": st.AuthFailLimit, "auth_fail_window": int(st.AuthFailWindow.Seconds()), "auth_block_seconds": int(st.AuthBlockTime.Seconds())}})
		return nil
	case what == "rules" && id == "" && r.Method == http.MethodPost:
		var body struct{ Type, Value, Description string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if err := s.addRule(ctx, r, body.Type, body.Value, body.Description); err != nil {
			return err
		}
		s.log.Info("web: security rule added", "type", body.Type, "value", body.Value, "user", p.key())
		s.sw.RecordEvent("security", "rule added by "+p.key()+": "+body.Type+" "+body.Value)
		writeJSON(w, map[string]string{"ok": "added"})
		return nil
	case what == "rules" && id != "" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		if !reUUID.MatchString(id) {
			return notFound("rule")
		}
		var typ, val string
		if err := s.store.Pool().QueryRow(ctx, `select rule_type, rule_value from rq_security_rules where rule_uuid = $1::uuid`, id).Scan(&typ, &val); err != nil {
			return notFound("rule")
		}
		enable := false
		if r.Method == http.MethodPut {
			var body struct{ Enabled bool }
			if err := decodeBody(r, &body); err != nil {
				return err
			}
			enable = body.Enabled
		}
		if typ == "web-allow" && !enable {
			if err := s.checkWebLockout(ctx, r, id, ""); err != nil {
				return err
			}
		}
		var err error
		if r.Method == http.MethodDelete {
			_, err = s.store.Pool().Exec(ctx, `delete from rq_security_rules where rule_uuid = $1::uuid`, id)
		} else {
			_, err = s.store.Pool().Exec(ctx, `update rq_security_rules set enabled = $2 where rule_uuid = $1::uuid`, id, enable)
		}
		if err != nil {
			return err
		}
		s.reload()
		s.sw.RecordEvent("security", "rule "+map[bool]string{true: "changed", false: "deleted"}[r.Method == http.MethodPut]+" by "+p.key()+": "+typ+" "+val)
		writeJSON(w, map[string]string{"ok": "saved"})
		return nil
	case what == "unblock" && r.Method == http.MethodPost:
		var body struct{ IP string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if !s.sw.Unblock(body.IP) {
			return notFound("blocked address")
		}
		s.sw.RecordEvent("security", body.IP+" unblocked by "+p.key())
		writeJSON(w, map[string]string{"ok": "unblocked"})
		return nil
	case what == "acl" && id == "" && r.Method == http.MethodPost:
		var body struct{ CIDR, Description string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		pf, err := db.ParseCIDR(body.CIDR)
		if err != nil {
			return badRequest("address: an IP address or network such as 203.0.113.0/24")
		}
		if pf.Bits() < 8 {
			return badRequest("that network is too large to trust")
		}
		if err := s.addProvider(ctx, pf.String(), body.Description); err != nil {
			return err
		}
		s.reload()
		s.sw.RecordEvent("security", "carrier address "+pf.String()+" trusted by "+p.key())
		writeJSON(w, map[string]string{"ok": "added"})
		return nil
	case what == "acl" && id != "" && r.Method == http.MethodDelete:
		if !reUUID.MatchString(id) {
			return notFound("address")
		}
		if _, err := s.store.Pool().Exec(ctx, `delete from v_access_control_nodes where access_control_node_uuid = $1::uuid`, id); err != nil {
			return err
		}
		s.reload()
		writeJSON(w, map[string]string{"ok": "deleted"})
		return nil
	}
	return errMethod
}

func (s *Server) addRule(ctx context.Context, r *http.Request, typ, value, desc string) error {
	if _, ok := ruleTypes[typ]; !ok {
		return badRequest("unknown rule type")
	}
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(desc) > 200 {
		return badRequest("description too long")
	}
	if typ == "ua-deny" {
		if len(value) < 3 || len(value) > 100 {
			return badRequest("User-Agent text: 3 to 100 characters")
		}
		value = strings.ToLower(value)
	} else {
		pf, err := db.ParseCIDR(value)
		if err != nil {
			return badRequest("address: an IP address or network such as 203.0.113.0/24")
		}
		if typ == "ip-deny" && pf.Bits() < 8 {
			return badRequest("that network is too large to block")
		}
		if typ == "ip-deny" || typ == "web-deny" {
			if ip, err := netip.ParseAddr(clientIP(r)); err == nil && pf.Contains(ip.Unmap()) {
				return badRequest("that rule would block your own address (%s)", ip)
			}
		}
		if typ == "web-deny" && pf.Bits() < 8 {
			return badRequest("that network is too large to block")
		}
		value = pf.String()
		if typ == "web-allow" {
			if err := s.checkWebLockout(ctx, r, "", value); err != nil {
				return err
			}
		}
	}
	var n int
	if err := s.store.Pool().QueryRow(ctx, `select count(*) from rq_security_rules where rule_type = $1 and lower(rule_value) = lower($2)`, typ, value).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return badRequest("that rule exists already")
	}
	if _, err := s.store.Pool().Exec(ctx, `insert into rq_security_rules (rule_uuid, rule_type, rule_value, description, enabled, insert_date)
		values ($1, $2, $3, $4, true, now())`, newUUID(), typ, value, desc); err != nil {
		return err
	}
	if typ == "ip-deny" {
		if pf, err := db.ParseCIDR(value); err == nil && pf.IsSingleIP() {
			s.sw.Unblock(pf.Addr().String()) // now blocked permanently instead
		}
	}
	s.reload()
	return nil
}

// checkWebLockout refuses web-allow changes that would shut out the
// person making them.
func (s *Server) checkWebLockout(ctx context.Context, r *http.Request, removing, adding string) error {
	rows, err := s.store.Pool().Query(ctx, `select rule_uuid::text, rule_value from rq_security_rules where rule_type = 'web-allow' and enabled`)
	if err != nil {
		return err
	}
	var list []netip.Prefix
	for rows.Next() {
		var id, v string
		if rows.Scan(&id, &v) == nil && id != removing {
			if pf, err := db.ParseCIDR(v); err == nil {
				list = append(list, pf)
			}
		}
	}
	rows.Close()
	if adding != "" {
		if pf, err := db.ParseCIDR(adding); err == nil {
			list = append(list, pf)
		}
	}
	if len(list) == 0 {
		return nil // nobody restricted
	}
	ip, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return badRequest("cannot tell your address")
	}
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return nil
	}
	for _, pf := range list {
		if pf.Contains(ip) {
			return nil
		}
	}
	return badRequest("this would lock you out: add your own address (%s) first", ip)
}

func (s *Server) addProvider(ctx context.Context, cidr, desc string) error {
	if !s.store.HasTable("v_access_controls") || !s.store.HasTable("v_access_control_nodes") {
		return badRequest("the database has no access control tables")
	}
	var acl string
	err := s.store.Pool().QueryRow(ctx, `select access_control_uuid::text from v_access_controls where lower(access_control_name) = 'providers' limit 1`).Scan(&acl)
	if err != nil {
		acl = newUUID()
		if _, err := s.store.Pool().Exec(ctx, `insert into v_access_controls (access_control_uuid, access_control_name, access_control_default,
			access_control_description, insert_date) values ($1, 'providers', 'deny', 'Carriers trusted without authentication', now())`, acl); err != nil {
			return err
		}
	}
	_, err = s.store.Pool().Exec(ctx, `insert into v_access_control_nodes (access_control_node_uuid, access_control_uuid, node_type, node_cidr,
		node_description, insert_date) values ($1, $2, 'allow', $3, $4, now())`, newUUID(), acl, cidr, desc)
	return err
}

// installDefaultScanners adds the scanner User-Agent rules once.
func installDefaultScanners(ctx context.Context, st *db.Store) (int, error) {
	if !st.HasTable("rq_security_rules") {
		return 0, nil
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `select count(*) from rq_security_rules where rule_type = 'ua-deny'`).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	for _, ua := range defaultScanners {
		if _, err := st.Pool().Exec(ctx, `insert into rq_security_rules (rule_uuid, rule_type, rule_value, description, enabled, insert_date)
			values ($1, 'ua-deny', $2, 'Known SIP scanner', true, $3)`, newUUID(), ua, time.Now()); err != nil {
			return 0, err
		}
	}
	return len(defaultScanners), nil
}

// guardHistory: SIP Guard's bans over the last days (security_audit_log).
func (s *Server) guardHistory(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	days := atoi(r.URL.Query().Get("days"), 7)
	if days < 1 || days > 365 {
		days = 7
	}
	out := map[string]any{"days": days, "bans": 0, "bans_24h": 0, "unique_ips": 0, "by_jail": map[string]int{},
		"by_day": []map[string]any{}, "top_ips": []map[string]string{}, "events": []map[string]string{}}
	if !s.store.HasTable("security_audit_log") {
		writeJSON(w, out)
		return nil
	}
	since := time.Now().AddDate(0, 0, -days)
	var bans, bans24, ips int
	if err := s.store.Pool().QueryRow(ctx, `select count(*) filter (where action = 'ban'),
		count(*) filter (where action = 'ban' and occurred_at > now() - interval '1 day'),
		count(distinct ip_address) filter (where action = 'ban') from security_audit_log where occurred_at >= $1`, since).Scan(&bans, &bans24, &ips); err != nil {
		return err
	}
	out["bans"], out["bans_24h"], out["unique_ips"] = bans, bans24, ips
	rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('jail', coalesce(category, ''), 'n', count(*)) from security_audit_log
		where action = 'ban' and occurred_at >= $1 group by category`, since)
	if err != nil {
		return err
	}
	byJail := map[string]int{}
	for _, r := range rows {
		byJail[r["jail"]] = atoi(r["n"], 0)
	}
	out["by_jail"] = byJail
	day, err := s.store.JSONRows(ctx, `select jsonb_build_object('day', day, 'n', n) from (select to_char(date_trunc('day', occurred_at), 'YYYY-MM-DD') as day,
		count(*) as n from security_audit_log where action = 'ban' and occurred_at >= $1 group by 1 order by 1) t`, since)
	if err != nil {
		return err
	}
	byDay := []map[string]any{}
	for _, d := range day {
		byDay = append(byDay, map[string]any{"day": d["day"], "bans": atoi(d["n"], 0)})
	}
	out["by_day"] = byDay
	top, err := s.store.JSONRows(ctx, `select jsonb_build_object('ip', host(ip_address), 'bans', count(*),
		'last', extract(epoch from max(occurred_at))::bigint, 'jails', string_agg(distinct category, ', '))
		from security_audit_log where action = 'ban' and occurred_at >= $1 group by ip_address order by count(*) desc, max(occurred_at) desc limit 10`, since)
	if err != nil {
		return err
	}
	if top != nil {
		out["top_ips"] = top
	}
	ev, err := s.store.JSONRows(ctx, `select jsonb_build_object('time', extract(epoch from occurred_at)::bigint, 'action', action,
		'ip', host(ip_address), 'jail', coalesce(category, ''), 'reason', coalesce(reason, ''), 'actor', coalesce(actor, ''))
		from security_audit_log where occurred_at >= $1 order by occurred_at desc limit 200`, since)
	if err != nil {
		return err
	}
	if ev != nil {
		out["events"] = ev
	}
	writeJSON(w, out)
	return nil
}

// sshGuardSettings reads the server's SSH protection settings (sshd, in
// dir), when it is installed: the switch runs without root, so it shows
// them and does not change them.
func sshGuardSettings(dir string) map[string]any {
	out := map[string]any{"installed": false}
	if _, err := os.Stat(filepath.Join(dir, "jail.conf")); err != nil {
		return out
	}
	out["installed"] = true
	vals := map[string]map[string]string{"default": {}, "sshd": {}}
	read := func(path string) {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		section := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.ToLower(strings.Trim(line, "[] "))
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if m := vals[section]; ok && m != nil {
				m[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
	}
	read(filepath.Join(dir, "jail.conf"))
	for _, pat := range []string{"jail.d/*.conf", "jail.local", "jail.d/*.local"} {
		files, _ := filepath.Glob(filepath.Join(dir, pat))
		sort.Strings(files)
		for _, f := range files {
			read(f)
		}
	}
	get := func(k string) string { return firstNonEmpty(vals["sshd"][k], vals["default"][k]) }
	out["enabled"] = strings.EqualFold(get("enabled"), "true")
	out["max"] = atoi(get("maxretry"), 0)
	out["window"] = f2bSeconds(get("findtime"))
	out["ban"] = f2bSeconds(get("bantime"))
	return out
}

// f2bSeconds reads a time of the SSH protection: 600, 10m, 1h, 1d, 1w.
func f2bSeconds(v string) int {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return 0
	}
	mult := 1
	switch v[len(v)-1] {
	case 's':
		v = v[:len(v)-1]
	case 'm':
		mult, v = 60, v[:len(v)-1]
	case 'h':
		mult, v = 3600, v[:len(v)-1]
	case 'd':
		mult, v = 86400, v[:len(v)-1]
	case 'w':
		mult, v = 7*86400, v[:len(v)-1]
	}
	return atoi(v, 0) * mult
}
