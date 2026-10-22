package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
)

// API layout (JSON unless noted):
//
//	POST /api/login, /api/logout          GET /api/me
//	/api/domains[/{uuid}]                 list, create, update, delete
//	/api/d/{domain}/meta                  choices for the forms
//	/api/d/{domain}/{resource}[/{id}]     extensions, gateways, dids, routes, ringgroups, ivrs,
//	                                      queues, agents, auxcodes, timeconditions, conferences, voicemails
//	/api/d/{domain}/cdr[/{uuid}/recording]
//	/api/d/{domain}/reports/{agent-status|agent-log|queues|agents|queue-calls}
//	/api/d/{domain}/voicemail-messages[/{uuid}[/audio|/read]]
//	/api/d/{domain}/dialplans             (read only)
//	GET /api/live[?domain=]               wallboard: calls, phones, gateways, agents, queues...
//	POST /api/agents/{uuid}/status        {"status": "On Break", "aux": "Lunch"}
//	POST /api/calls/{uuid}/hangup|transfer, POST /api/originate
//	/api/sounds                           list, upload (WAV), delete; GET /api/sounds/file?path=
//	GET /api/logs?kind=&q=&limit=         GET /api/system, POST /api/system/{reload|unblock|repair}
//
// Every request except login needs the session cookie, and every request
// that changes something needs the header "X-RQ: 1" (the cookie is
// SameSite=Strict; the header stops cross-site form posts as well).

func (s *Server) reload() {
	if err := s.sw.Reload(); err != nil {
		s.log.Warn("web: reload after change failed", "err", err)
	}
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	if !s.webAllowed(r) {
		httpError(w, http.StatusForbidden, "access from your address is not allowed (Security settings)")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	switch {
	case path == "login":
		if r.Method != http.MethodPost {
			httpError(w, http.StatusMethodNotAllowed, "use POST")
			return
		}
		s.login(w, r)
		return
	case path == "logout":
		s.logout(w, r)
		return
	case strings.HasPrefix(path, "sso/"):
		// Single sign-on: before anyone is signed in.
		s.ssoPublic(w, r, strings.Split(path, "/")[1:])
		return
	case strings.HasPrefix(path, "guest/"):
		// Meeting guests (join links) need no account.
		if err := s.guestAPI(w, r, strings.Split(path, "/")[1:]); err != nil {
			s.fail(w, err)
		}
		return
	}
	p := s.sessionPrincipal(r)
	if p == nil {
		s.sw.GuardFail(config.JailWebRequest, clientIP(r), "request without signing in: "+truncate(r.URL.Path, 80))
		httpError(w, http.StatusUnauthorized, "please log in")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-RQ") != "1" {
		httpError(w, http.StatusForbidden, "missing X-RQ header")
		return
	}
	parts := strings.Split(path, "/")
	if err := s.dispatch(w, r, p, parts); err != nil {
		s.fail(w, err)
	}
}

var errMethod = &apiError{http.StatusMethodNotAllowed, "method not allowed"}

func (s *Server) needDB() error {
	if s.store == nil {
		return &apiError{http.StatusServiceUnavailable, "this page needs database mode (see docs/DATABASE.md)"}
	}
	return nil
}

func part(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, p *principal, parts []string) error {
	ctx := r.Context()
	needOwner := func() error {
		if !p.owner() {
			return errForbidden
		}
		return nil
	}
	needServer := func() error {
		if !s.serverAccess(p) {
			return errForbidden
		}
		return nil
	}
	needExtension := func() error {
		if p.Kind != "extension" {
			return badRequest("sign in with an extension to use this (the administrator account has no extension)")
		}
		return s.needDB()
	}
	switch parts[0] {
	case "me":
		// The name as it is now: first and last name (else the caller ID name).
		name := p.Name
		if p.Kind == "extension" && s.store != nil && reUUID.MatchString(p.ExtensionUUID) {
			_ = s.store.Pool().QueryRow(ctx, `select coalesce(nullif(trim(coalesce(directory_first_name, '') || ' ' ||
				coalesce(directory_last_name, '')), ''), effective_caller_id_name, '') from v_extensions where extension_uuid = $1::uuid`,
				p.ExtensionUUID).Scan(&name)
		}
		writeJSON(w, map[string]any{"user": p.User, "name": name, "role": p.Role, "kind": p.Kind, "domain": p.Domain,
			"extension": p.Extension, "server_access": s.serverAccess(p), "brand": s.cfg.Brand, "version": core.Version,
			"database": s.store != nil, "hostname": s.sw.Stats().Hostname, "webrtc": s.rtc != nil, "idle_logout": s.idleMinutes(p)})
		return nil
	case "password":
		if err := needExtension(); err != nil {
			return err
		}
		return s.changePassword(ctx, w, r, p)
	case "domains":
		if err := s.needDB(); err != nil {
			return err
		}
		if r.Method != http.MethodGet {
			if err := needServer(); err != nil {
				return err
			}
		}
		return s.domains(ctx, w, r, p, part(parts, 1))
	case "d":
		if err := s.needDB(); err != nil {
			return err
		}
		if len(parts) < 3 {
			return notFound("page")
		}
		dom, err := s.domain(ctx, parts[1])
		if err != nil {
			return err
		}
		if !p.canDomain(dom.name) {
			return errForbidden
		}
		return s.domainAPI(ctx, w, r, p, dom, parts[2:])
	case "live":
		domain := r.URL.Query().Get("domain")
		if !p.admin() {
			domain = p.Domain
		}
		writeJSON(w, s.live(domain))
		return nil
	case "agents":
		if len(parts) != 3 || parts[2] != "status" {
			return notFound("page")
		}
		if r.Method != http.MethodPost {
			return errMethod
		}
		a := s.sw.Config().Features.Agent(parts[1], "")
		switch {
		case a == nil:
			return notFound("agent")
		case !p.canDomain(a.Domain):
			return errForbidden
		case !p.supervisor() && !(a.Extension == p.Extension && strings.EqualFold(a.Domain, p.Domain)):
			return errForbidden // users change only their own status
		}
		var body struct{ Status, Aux string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if err := s.sw.SetAgentStatus(core.AgentStatusChange{Agent: a.UUID, Status: body.Status, Aux: body.Aux, Source: "web:" + p.key()}); err != nil {
			return badRequest("%v", err)
		}
		writeJSON(w, map[string]string{"ok": "status changed"})
		return nil
	case "calls":
		if !p.supervisor() {
			return errForbidden
		}
		return s.calls(w, r, p, parts)
	case "originate":
		return s.originate(w, r, p)
	case "sounds":
		if err := needOwner(); err != nil {
			return err
		}
		return s.sounds(w, r, part(parts, 1))
	case "logs":
		if err := needServer(); err != nil {
			return err
		}
		q := r.URL.Query()
		writeJSON(w, s.sw.EventLog(q.Get("kind"), q.Get("q"), atoi(q.Get("limit"), 500)))
		return nil
	case "system":
		if err := needServer(); err != nil {
			return err
		}
		return s.system(ctx, w, r, p.key(), part(parts, 1))
	case "security":
		if err := needServer(); err != nil {
			return err
		}
		if err := s.needDB(); err != nil {
			return err
		}
		return s.security(ctx, w, r, p, parts[1:])
	case "ws":
		s.serveWS(w, r, p)
		return nil
	case "chat":
		if err := needExtension(); err != nil {
			return err
		}
		return s.chatAPI(ctx, w, r, p, parts[1:])
	case "keys":
		// The dialer's phone keys with their lamps, the parking slots.
		if r.Method != http.MethodGet {
			return errMethod
		}
		return s.myKeys(ctx, w, p)
	case "dnd":
		// Do not disturb for the signed-in extension (dialer).
		if err := needExtension(); err != nil {
			return err
		}
		if r.Method != http.MethodPost {
			return errMethod
		}
		var body struct{ On bool }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if _, err := s.store.Pool().Exec(ctx, `update v_extensions set do_not_disturb = $2 where extension_uuid = $1::uuid`,
			p.ExtensionUUID, strconv.FormatBool(body.On)); err != nil {
			return err
		}
		// At once, and on the phones' DND keys.
		_ = s.sw.SetUserVars(p.Extension, p.Domain, map[string]string{"do_not_disturb": strconv.FormatBool(body.On)}, "web:"+p.key())
		s.reload()
		s.presenceChanged(p.Domain)
		writeJSON(w, map[string]bool{"dnd": body.On})
		return nil
	case "meetings":
		if err := needExtension(); err != nil {
			return err
		}
		return s.meetingsAPI(ctx, w, r, p, parts[1:])
	case "cache":
		// POST /api/cache/clear: the top bar's reload button.
		if err := needOwner(); err != nil {
			return err
		}
		if part(parts, 1) != "clear" || r.Method != http.MethodPost {
			return errMethod
		}
		rep, err := s.sw.ClearCache()
		if err != nil {
			return badRequest("clearing the cache failed: %v", err)
		}
		s.log.Info("web: cache cleared", "user", p.key(), "took_ms", rep.TookMS)
		writeJSON(w, rep)
		return nil
	case "devices":
		// GET /api/devices: the phones and apps signed in as my extension.
		out := []regInfo{}
		if p.Kind == "extension" {
			for _, b := range s.sw.Registrar().All() {
				if strings.EqualFold(b.User, p.Extension) && strings.EqualFold(b.Domain, p.Domain) {
					out = append(out, regInfo{User: b.User, Domain: b.Domain, Contact: b.Contact, Source: b.Src.String(), UserAgent: b.UserAgent,
						Expires: b.Expires.Unix(), Since: b.RegisteredAt.Unix()})
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Since > out[j].Since })
		writeJSON(w, out)
		return nil
	case "sipprofiles":
		if err := needServer(); err != nil {
			return err
		}
		return s.sipProfilesAPI(ctx, w, r, part(parts, 1))
	}
	return notFound("page")
}

// ---------------------------------------------------------------------------
// Domains

func (s *Server) domain(ctx context.Context, key string) (domainRow, error) {
	var d domainRow
	err := s.store.Pool().QueryRow(ctx, `select domain_uuid::text, domain_name from v_domains
		where lower(domain_name) = lower($1) or domain_uuid::text = $1 limit 1`, key).Scan(&d.uuid, &d.name)
	if err != nil {
		return d, notFound("domain " + key)
	}
	return d, nil
}

var reDomain = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

func (s *Server) domains(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, id string) error {
	switch r.Method {
	case http.MethodGet:
		mine := ""
		if !p.admin() {
			mine = p.DomainUUID
		}
		rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('domain_uuid', d.domain_uuid, 'domain_name', d.domain_name,
			'domain_enabled', coalesce(d.domain_enabled::text, 'true'), 'domain_description', coalesce(d.domain_description, ''),
			'extensions', (select count(*) from v_extensions e where e.domain_uuid = d.domain_uuid))
			from v_domains d where $1 = '' or d.domain_uuid::text = $1 order by d.domain_name`, mine)
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []map[string]string{}
		}
		writeJSON(w, rows)
		return nil
	case http.MethodPost, http.MethodPut:
		var body map[string]any
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		name := strings.ToLower(strings.TrimSpace(toStr(body["domain_name"])))
		if !reDomain.MatchString(name) || len(name) > 200 {
			return badRequest("domain name: letters, digits, dots and dashes (e.g. office.example.com)")
		}
		tx, err := s.store.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from v_domains where lower(domain_name) = $1 and domain_uuid::text <> $2`, name, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return badRequest("domain %s already exists", name)
		}
		row := map[string]string{"domain_name": name, "domain_description": toStr(body["domain_description"]),
			"domain_enabled": strconv.FormatBool(body["domain_enabled"] == nil || truthy(toStr(body["domain_enabled"])))}
		if r.Method == http.MethodPost {
			id = newUUID()
			row["domain_uuid"] = id
			row["insert_date"] = time.Now().Format(time.RFC3339)
			if err := insertRow(ctx, tx, s.store, "v_domains", row); err != nil {
				return err
			}
		} else {
			if !reUUID.MatchString(id) {
				return notFound("domain")
			}
			var old string
			if err := tx.QueryRow(ctx, `select domain_name from v_domains where domain_uuid = $1::uuid`, id).Scan(&old); err != nil {
				return notFound("domain")
			}
			if old != name {
				return badRequest("a domain cannot be renamed (phones register with it); create a new one instead")
			}
			if err := updateRow(ctx, tx, s.store, "v_domains", "domain_uuid", id, row); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if _, err := InstallDefaults(ctx, s.store); err != nil {
			s.log.Warn("web: adding defaults for the new domain failed", "err", err)
		}
		s.reload()
		writeJSON(w, map[string]string{"domain_uuid": id, "domain_name": name})
		return nil
	case http.MethodDelete:
		if !reUUID.MatchString(id) {
			return notFound("domain")
		}
		var exts int
		if err := s.store.Pool().QueryRow(ctx, `select count(*) from v_extensions where domain_uuid = $1::uuid`, id).Scan(&exts); err != nil {
			return err
		}
		if exts > 0 {
			return badRequest("delete the domain's %d extension(s) first", exts)
		}
		tx, err := s.store.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		for _, t := range []string{"v_dialplans", "v_destinations", "v_ring_group_destinations", "v_ring_groups", "v_ivr_menu_options",
			"v_ivr_menus", "v_call_center_tiers", "v_call_center_queues", "v_call_center_agents", "v_conferences", "v_voicemail_messages",
			"v_voicemails", "v_gateways", "rq_aux_codes", "rq_outbound_routes", "rq_time_conditions", "rq_agent_state",
			"rq_transcripts", "rq_transcription_settings", "rq_ai_agent_keywords", "rq_ai_knowledge", "rq_ai_agents", "rq_ai_calls",
			"rq_ai_settings"} {
			if s.store.HasTable(t) && s.store.HasColumn(t, "domain_uuid") {
				if _, err := tx.Exec(ctx, fmt.Sprintf(`delete from %s where domain_uuid = $1::uuid`, t), id); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `delete from v_domains where domain_uuid = $1::uuid`, id); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		s.reload()
		writeJSON(w, map[string]string{"ok": "deleted"})
		return nil
	}
	return errMethod
}

// ---------------------------------------------------------------------------
// Domain pages

func (s *Server) domainAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	// Users see their own calls, voicemail and agent status only; managers
	// see every call and report but only their own voicemail.
	mine := ownScope{on: true, ext: p.Extension, extUUID: p.ExtensionUUID}
	if a := s.sw.Config().Features.AgentByExtension(p.Extension, dom.name); a != nil {
		mine.agent = a.UUID
	}
	own, vmOwn := ownScope{}, ownScope{}
	if !p.supervisor() {
		own = mine
	}
	if !p.owner() {
		vmOwn = mine
	}
	switch parts[0] {
	case "meta":
		m, err := s.meta(ctx, dom)
		if err != nil {
			return err
		}
		writeJSON(w, m)
		return nil
	case "directory":
		return s.directory(ctx, w, dom)
	case "cdr":
		if len(parts) == 3 && parts[2] == "recording" {
			return s.recording(ctx, w, r, dom, parts[1], own)
		}
		if len(parts) == 3 && parts[2] == "flow" {
			return s.cdrFlow(ctx, w, dom, parts[1], own)
		}
		return s.cdr(ctx, w, r, dom, own)
	case "reports":
		return s.report(ctx, w, r, dom, part(parts, 1), own)
	case "voicemail-messages":
		return s.voicemailMessages(ctx, w, r, dom, part(parts, 1), part(parts, 2), vmOwn)
	case "contacts":
		return s.contactsAPI(ctx, w, r, p, dom, parts[1:])
	case "sso":
		if !p.owner() {
			return errForbidden
		}
		return s.ssoAdminAPI(ctx, w, r, p, dom, parts[1:])
	case "transcription":
		// The call transcription settings: owners.
		if !p.owner() {
			return errForbidden
		}
		return s.transcriptionAPI(ctx, w, r, p, dom, parts[1:])
	case "transcripts":
		return s.transcriptsAPI(ctx, w, r, p, dom, part(parts, 1), own)
	case "fax":
		// eFax: owners and managers.
		if !p.supervisor() {
			return errForbidden
		}
		return s.faxAPI(ctx, w, r, p, dom, parts[1:])
	case "ai":
		// AI agents' settings, knowledge and calls (owners; managers see calls).
		return s.aiAPI(ctx, w, r, p, dom, parts[1:])
	case "phonekeys":
		// Phone keys (BLF) of extensions and of the company: owners.
		if !p.owner() {
			return errForbidden
		}
		return s.phoneKeysAPI(ctx, w, r, p, dom, parts[1:])
	case "hotel":
		// The front desk: owners and managers.
		if !p.supervisor() {
			return errForbidden
		}
		return s.hotelAPI(ctx, w, r, p, dom, parts[1:])
	}
	if !p.owner() {
		return errForbidden
	}
	user := p.key()
	switch parts[0] {
	case "officehours":
		return s.officeHoursAPI(ctx, w, r, p, dom, parts[1:])
	case "dialsecurity":
		return s.dialSecurityAPI(ctx, w, r, p, dom, parts[1:])
	case "roles":
		return s.rolesAPI(ctx, w, r, p, dom, parts[1:])
	case "dialplans":
		rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('dialplan_uuid', dialplan_uuid, 'dialplan_name', dialplan_name,
			'dialplan_number', coalesce(dialplan_number, ''), 'dialplan_context', dialplan_context, 'dialplan_order', dialplan_order,
			'dialplan_enabled', coalesce(dialplan_enabled::text, 'true'), 'dialplan_description', coalesce(dialplan_description, ''),
			'dialplan_xml', coalesce(dialplan_xml, ''), 'global', domain_uuid is null)
			from v_dialplans where domain_uuid = $1::uuid or domain_uuid is null order by dialplan_context, dialplan_order, dialplan_name`, dom.uuid)
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []map[string]string{}
		}
		writeJSON(w, rows)
		return nil
	}
	res := resourceByName(parts[0])
	if res == nil {
		return notFound("page")
	}
	id := part(parts, 1)
	switch r.Method {
	case http.MethodGet:
		if id == "" {
			rows, err := s.list(ctx, res, dom)
			if err != nil {
				return err
			}
			writeJSON(w, rows)
			return nil
		}
		row, err := s.get(ctx, res, dom, id)
		if err != nil {
			return err
		}
		writeJSON(w, row)
		return nil
	case http.MethodPost, http.MethodPut:
		if (r.Method == http.MethodPost) != (id == "") {
			return errMethod
		}
		if !s.store.HasTable(res.table) {
			return badRequest("the database has no %s table", res.table)
		}
		var body map[string]any
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		row, err := s.save(ctx, res, dom, id, body)
		if err != nil {
			return err
		}
		s.log.Info("web: saved", "what", res.name, "name", row[res.nameCol], "domain", dom.name, "user", user)
		writeJSON(w, row)
		return nil
	case http.MethodDelete:
		if id == "" {
			return errMethod
		}
		if err := s.remove(ctx, res, dom, id); err != nil {
			return err
		}
		s.log.Info("web: deleted", "what", res.name, "id", id, "domain", dom.name, "user", user)
		writeJSON(w, map[string]string{"ok": "deleted"})
		return nil
	}
	return errMethod
}

type choice struct {
	Group string `json:"group"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// meta returns what the forms offer: destinations (for DIDs, IVR options,
// timeouts...), gateways, agents, AUX codes, strategies, sounds and music.
func (s *Server) meta(ctx context.Context, dom domainRow) (map[string]any, error) {
	to := func(n string) string { return "transfer:" + n + " XML " + dom.name }
	dests := []choice{}
	add := func(resName, group, label string, value func(m map[string]string) string) error {
		res := resourceByName(resName)
		if res == nil || !s.store.HasTable(res.table) {
			return nil
		}
		rows, err := s.list(ctx, res, dom)
		if err != nil {
			return err
		}
		for _, m := range rows {
			if res.enabledCol != "" && m[res.enabledCol] != "" && !truthy(m[res.enabledCol]) {
				continue
			}
			lbl := m[label]
			if res.numberCol != "" && m[res.numberCol] != "" && m[res.numberCol] != lbl {
				lbl = m[res.numberCol] + " " + lbl
			}
			dests = append(dests, choice{group, lbl, value(m)})
		}
		return nil
	}
	extName := func(m map[string]string) string {
		n := strings.TrimSpace(m["directory_first_name"] + " " + m["directory_last_name"])
		return firstNonEmpty(n, m["effective_caller_id_name"], m["description"])
	}
	var exts []map[string]string
	if s.store.HasTable("v_extensions") {
		var err error
		if exts, err = s.list(ctx, resourceByName("extensions"), dom); err != nil {
			return nil, err
		}
	}
	extList := []map[string]string{}
	for _, m := range exts {
		name := extName(m)
		extList = append(extList, map[string]string{"extension": m["extension"], "name": name})
		if !truthy(firstNonEmpty(m["enabled"], "true")) {
			continue
		}
		dests = append(dests, choice{"Extension", strings.TrimSpace(m["extension"] + " " + name), to(m["extension"])})
	}
	// eFax: a DID that answers as a fax machine for an extension (Call Flow
	// → Others); the extension's own calls are not involved.
	for _, m := range exts {
		if truthy(firstNonEmpty(m["enabled"], "true")) {
			dests = append(dests, choice{"eFax", strings.TrimSpace(m["extension"] + " " + extName(m)), "efax:" + m["extension"] + " XML " + dom.name})
		}
	}
	for _, m := range exts {
		if truthy(m["voicemail_enabled"]) {
			dests = append(dests, choice{"Voicemail", "Voicemail " + m["extension"] + " " + extName(m), to("*99" + m["extension"])})
		}
	}
	steps := []struct{ res, group, label string }{
		{"ringgroups", "Ring group", "ring_group_name"}, {"ivrs", "IVR menu", "ivr_menu_name"}, {"queues", "Queue", "queue_name"},
		{"conferences", "Conference", "conference_name"}, {"timeconditions", "Time condition", "condition_name"},
		{"aiagents", "AI agent", "agent_name"},
	}
	for _, st := range steps {
		res := resourceByName(st.res)
		if err := add(st.res, st.group, st.label, func(m map[string]string) string { return to(m[res.numberCol]) }); err != nil {
			return nil, err
		}
	}
	if err := add("faxes", "Fax", "fax_name", func(m map[string]string) string { return to(m["fax_extension"]) }); err != nil {
		return nil, err
	}
	dests = append(dests,
		choice{"Other", "Hang up", "hangup:NORMAL_CLEARING"},
		choice{"Other", "Busy signal", "hangup:USER_BUSY"},
		choice{"Other", "Echo test (9196)", to("9196")})

	gateways := []map[string]string{}
	if s.store.HasTable("v_gateways") {
		rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('gateway_uuid', gateway_uuid, 'gateway', gateway, 'proxy', proxy,
			'enabled', coalesce(enabled::text, 'true')) from v_gateways where domain_uuid = $1::uuid or domain_uuid is null order by gateway`, dom.uuid)
		if err != nil {
			return nil, err
		}
		gateways = append(gateways, rows...)
	}
	agents := []map[string]string{}
	if res := resourceByName("agents"); s.store.HasTable(res.table) {
		rows, err := s.list(ctx, res, dom)
		if err != nil {
			return nil, err
		}
		for _, m := range rows {
			agents = append(agents, map[string]string{"uuid": m["call_center_agent_uuid"], "name": m["agent_name"], "extension": m["extension"]})
		}
	}
	aux := []map[string]string{}
	if res := resourceByName("auxcodes"); s.store.HasTable(res.table) {
		rows, err := s.list(ctx, res, dom)
		if err != nil {
			return nil, err
		}
		for _, m := range rows {
			if truthy(firstNonEmpty(m["aux_code_enabled"], "true")) {
				aux = append(aux, map[string]string{"name": m["aux_code_name"], "block_direct_calls": m["block_direct_calls"]})
			}
		}
	}
	sounds, music := s.soundList()
	moh := []string{"local_stream://default"}
	for folder := range music {
		if folder != "default" {
			moh = append(moh, "local_stream://"+folder)
		}
	}
	sort.Strings(moh[1:])
	prompts := []string{}
	for _, f := range sounds {
		prompts = append(prompts, "prompts/"+f)
	}
	return map[string]any{
		"domain": map[string]string{"uuid": dom.uuid, "name": dom.name}, "destinations": dests, "extensions": extList,
		"gateways": gateways, "agents": agents, "auxcodes": aux, "strategies": core.CCStrategies, "prompts": prompts, "moh": moh,
		"statuses": []string{"Available", "Available (On Demand)", "On Break", "Logged Out"},
	}, nil
}

// ---------------------------------------------------------------------------
// Live view

type regInfo struct {
	User      string `json:"user"`
	Domain    string `json:"domain"`
	Contact   string `json:"contact"`
	Source    string `json:"source"`
	UserAgent string `json:"user_agent"`
	Expires   int64  `json:"expires"`
	Since     int64  `json:"since"`
}

func (s *Server) live(domain string) map[string]any {
	chans := []core.ChannelInfo{}
	for _, ch := range s.sw.Channels() {
		info := ch.Snapshot()
		if domain == "" || strings.EqualFold(info.Domain, domain) || strings.EqualFold(info.Context, domain) {
			chans = append(chans, info)
		}
	}
	sort.Slice(chans, func(i, j int) bool { return chans[i].CreatedEpoch < chans[j].CreatedEpoch })
	regs := []regInfo{}
	for _, b := range s.sw.Registrar().All() {
		if domain != "" && !strings.EqualFold(b.Domain, domain) {
			continue
		}
		regs = append(regs, regInfo{User: b.User, Domain: b.Domain, Contact: b.Contact, Source: b.Src.String(), UserAgent: b.UserAgent,
			Expires: b.Expires.Unix(), Since: b.RegisteredAt.Unix()})
	}
	agents := s.sw.Agents(domain)
	if agents == nil {
		agents = []core.AgentInfo{}
	}
	queues := s.sw.Queues(domain)
	if queues == nil {
		queues = []core.QueueInfo{}
	}
	confs := s.sw.Conferences(domain)
	if confs == nil {
		confs = []core.ConferenceRoom{}
	}
	parked := s.sw.ParkedCalls(domain)
	gws := s.sw.Gateways()
	if gws == nil {
		gws = []core.GatewayInfo{}
	}
	// Extensions: how many exist and how many have a device registered.
	total, online := 0, map[string]bool{}
	for _, dom := range s.sw.Config().Directory.List() {
		if domain != "" && !strings.EqualFold(dom.Name, domain) {
			continue
		}
		for _, u := range dom.Users {
			if !u.Disabled {
				total++
			}
		}
	}
	for _, r := range regs {
		online[strings.ToLower(r.User+"@"+r.Domain)] = true
	}
	var health healthInfo
	if s.health != nil {
		health = s.health.snapshot()
	}
	return map[string]any{"time": time.Now().Unix(), "stats": s.sw.Stats(), "channels": chans, "registrations": regs,
		"gateways": gws, "agents": agents, "queues": queues, "conferences": confs, "parked": parked,
		"extensions": map[string]int{"total": total, "registered": len(online)}, "health": health}
}

// calls: actions on a call in progress (owners).
//
//	POST /api/calls/{uuid}/hangup
//	POST /api/calls/{uuid}/transfer {"destination": "1002"}  (divert the caller)
//	POST /api/calls/{uuid}/listen|whisper|barge {"extension": "1005"}
//	     rings that extension (default: yours) and connects it to the call:
//	     listen = hear both sides; whisper = talk to the agent only;
//	     barge = three-way. On the phone, 1/2/3 switch between them.
func (s *Server) calls(w http.ResponseWriter, r *http.Request, p *principal, parts []string) error {
	if r.Method != http.MethodPost || len(parts) != 3 {
		return errMethod
	}
	ch := s.sw.Channel(parts[1])
	if ch == nil {
		return notFound("call")
	}
	info := ch.Snapshot()
	domain := firstNonEmpty(info.Domain, info.Context)
	if !p.canDomain(domain) {
		return errForbidden
	}
	var body struct{ Destination, Domain, Extension string }
	if r.ContentLength != 0 {
		if err := decodeBody(r, &body); err != nil {
			return err
		}
	}
	switch parts[2] {
	case "hangup":
		ch.Hangup(core.CauseNormalClearing)
		s.sw.RecordEvent("call", fmt.Sprintf("%s hung up the call %s → %s from the web", p.key(), info.CIDNum, info.Destination))
	case "transfer", "divert":
		if !reNumber.MatchString(body.Destination) && !reDID.MatchString(body.Destination) {
			return badRequest("destination: a number")
		}
		if out := s.sw.API(fmt.Sprintf("uuid_transfer %s %s XML %s", ch.UUID, body.Destination, domain)); strings.HasPrefix(out, "-ERR") {
			return badRequest("%s", strings.TrimSpace(strings.TrimPrefix(out, "-ERR")))
		}
		s.sw.RecordEvent("call", fmt.Sprintf("%s diverted the call from %s to %s", p.key(), info.CIDNum, body.Destination))
	case "listen", "whisper", "barge":
		ext := firstNonEmpty(body.Extension, p.Extension)
		if !reNumber.MatchString(ext) {
			return badRequest("which extension should ring for monitoring? (the administrator account has no extension)")
		}
		if err := s.sw.Monitor(ch.UUID, parts[2], ext, domain, p.key()); err != nil {
			return badRequest("%v", err)
		}
	default:
		return notFound("action")
	}
	writeJSON(w, map[string]string{"ok": "done"})
	return nil
}

// originate rings an extension and, when answered, calls the number
// (click-to-call). Users can only ring their own extension.
func (s *Server) originate(w http.ResponseWriter, r *http.Request, p *principal) error {
	if r.Method != http.MethodPost {
		return errMethod
	}
	var body struct{ Extension, Destination, Domain string }
	if err := decodeBody(r, &body); err != nil {
		return err
	}
	if !p.admin() {
		body.Domain = p.Domain
		if !p.owner() {
			body.Extension = p.Extension
		}
	}
	if !reNumber.MatchString(body.Extension) || !(reNumber.MatchString(body.Destination) || reDID.MatchString(body.Destination)) {
		return badRequest("extension and number: digits only")
	}
	if !reDomain.MatchString(body.Domain) {
		return badRequest("choose a domain")
	}
	cmd := fmt.Sprintf("originate {origination_caller_id_name=Call,origination_caller_id_number=%s}user/%s@%s %s XML %s",
		body.Destination, body.Extension, body.Domain, body.Destination, body.Domain)
	go func() {
		if out := s.sw.API(cmd); !strings.HasPrefix(out, "+OK") {
			s.log.Info("web: click-to-call failed", "extension", body.Extension, "to", body.Destination, "result", strings.TrimSpace(out))
		}
	}()
	writeJSON(w, map[string]string{"ok": "ringing " + body.Extension})
	return nil
}

// ---------------------------------------------------------------------------
// System

func (s *Server) system(ctx context.Context, w http.ResponseWriter, r *http.Request, user, action string) error {
	if action == "" {
		if r.Method != http.MethodGet {
			return errMethod
		}
		cfg := s.sw.Config()
		blocked := []map[string]any{}
		for ip, until := range s.sw.Blocked() {
			blocked = append(blocked, map[string]any{"ip": ip, "until": until.Unix()})
		}
		sort.Slice(blocked, func(i, j int) bool { return blocked[i]["ip"].(string) < blocked[j]["ip"].(string) })
		info := map[string]any{"stats": s.sw.Stats(), "blocked": blocked, "database": s.store != nil,
			"sounds_dir": cfg.Settings.SoundsDir, "recordings_dir": cfg.Settings.RecordingsDir, "voicemail_dir": cfg.Settings.VoicemailDir,
			"config_dir": cfg.Dir}
		st := cfg.Settings
		netw := map[string]any{"sip_ip": st.SIPIP, "sip_bind_ip": st.SIPBindIP, "ext_sip_ip": st.ExtSIPIP, "sip_port": s.sw.SIPPort(), "sip_tcp": st.EnableTCP,
			"rtp_ip": st.RTPIP, "ext_rtp_ip": st.ExtRTPIP, "rtp_start": st.RTPStartPort, "rtp_end": st.RTPEndPort,
			"web_port": 0, "webrtc_port": s.cfg.WebRTCPort, "public_ip": s.cfg.PublicIP, "webrtc_on": s.rtc != nil, "webrtc_tcp": 0}
		if a, ok := s.addr.(*net.TCPAddr); ok {
			netw["web_port"] = a.Port
		}
		if s.rtc != nil {
			netw["webrtc_tcp"] = s.rtc.tcpPort
		}
		var extra []int
		for _, p := range s.sw.SIPProfiles() {
			if p.Listening && !p.Main {
				extra = append(extra, p.Port)
			}
		}
		netw["profile_ports"] = extra
		info["network"] = netw
		if s.store != nil {
			info["database_name"] = s.store.Pool().Config().ConnConfig.Database
			info["database_host"] = s.store.Pool().Config().ConnConfig.Host
			info["default_domain"] = cfg.Database.DefaultDomain
		}
		writeJSON(w, info)
		return nil
	}
	if r.Method != http.MethodPost {
		return errMethod
	}
	switch action {
	case "reload":
		if err := s.sw.Reload(); err != nil {
			return badRequest("reload failed: %v", err)
		}
		s.log.Info("web: reload", "user", user)
		writeJSON(w, map[string]string{"ok": "reloaded"})
	case "unblock":
		var body struct{ IP string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if !s.sw.Unblock(body.IP) {
			return notFound("blocked address")
		}
		s.log.Info("web: unblocked", "ip", body.IP, "user", user)
		writeJSON(w, map[string]string{"ok": "unblocked"})
	case "repair":
		if err := s.needDB(); err != nil {
			return err
		}
		n, err := InstallDefaults(ctx, s.store)
		if err != nil {
			return err
		}
		s.reload()
		writeJSON(w, map[string]any{"ok": "done", "added": n})
	default:
		return notFound("action")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Files

// inside reports whether path is within dir (both cleaned, absolute).
func inside(dir, path string) bool {
	dir, err1 := filepath.Abs(dir)
	path, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func serveAudio(w http.ResponseWriter, r *http.Request, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return notFound("audio file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return notFound("audio file")
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	}
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
	return nil
}
