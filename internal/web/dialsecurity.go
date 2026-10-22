package web

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/db"
)

// A domain's part of Security settings, kept where RingQ keeps it:
// allowed countries (q_security_restrictions.blocked_countries), PIN-based
// dialing (q_security_rules, the RingQ portal's v_pin_numbers) and the web auto
// logout (rq_settings).
//
//	GET  /api/d/{domain}/dialsecurity
//	PUT  /api/d/{domain}/dialsecurity/countries   {"blocked": ["CU", ...]}
//	PUT  /api/d/{domain}/dialsecurity/pin         {"mode": "prefix", "prefixes": ["00"], "trusted": ["1001"]}
//	POST /api/d/{domain}/dialsecurity/pins        {"pin", "accountcode", "description", "enabled"}; PUT, DELETE .../pins/{uuid}
//	PUT  /api/d/{domain}/dialsecurity/autologout  {"minutes": 30}

// IdleLogoutChoices are the web auto logout times offered (minutes; 0 = off).
var IdleLogoutChoices = []int{0, 5, 10, 15, 30, 60, 120, 240, 480}

var rePIN = regexp.MustCompile(`^[0-9]{3,16}$`)

func (s *Server) dialSecurityAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	for _, t := range []string{"q_security_restrictions", "q_security_rules", "v_pin_numbers", "rq_settings"} {
		if !s.store.HasTable(t) {
			return badRequest("the database has no %s table (RingQswitch creates it when manage-schema is on)", t)
		}
	}
	what := part(parts, 0)
	switch {
	case what == "" && r.Method == http.MethodGet:
		return s.dialSecurityGet(ctx, w, dom)
	case what == "countries" && r.Method == http.MethodPut:
		var body struct{ Blocked []string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		var list []string
		for _, iso := range body.Blocked {
			c := config.CountryByISO(iso)
			if c == nil {
				return badRequest("unknown country %q", iso)
			}
			list = append(list, c.ISO)
		}
		sort.Strings(list)
		v := strings.Join(list, ",")
		tag, err := s.store.Pool().Exec(ctx, `update q_security_restrictions set blocked_countries = $2 where id = (select id
			from q_security_restrictions where domain_uuid = $1::uuid order by id desc limit 1)`, dom.uuid, v)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.store.Pool().Exec(ctx, `insert into q_security_restrictions (domain_uuid, security_uuid, blocked_countries)
				values ($1::uuid, $2, $3)`, dom.uuid, newUUID(), v); err != nil {
				return err
			}
		}
		s.reload()
		s.sw.RecordEvent("security", "allowed countries of "+dom.name+" changed by "+p.key()+": "+strconv.Itoa(len(list))+" blocked")
		writeJSON(w, map[string]any{"ok": "saved", "blocked": len(list)})
		return nil
	case what == "pin" && r.Method == http.MethodPut:
		var body struct {
			Mode     string
			Prefixes []string
			Trusted  []string
		}
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if body.Mode != "" && body.Mode != "all" && body.Mode != "prefix" {
			return badRequest("mode: all, prefix or none")
		}
		var prefixes, trusted []string
		for _, x := range body.Prefixes {
			if x = strings.TrimSpace(x); x != "" {
				if !reDigitsStar.MatchString(x) || len(x) > 10 {
					return badRequest("prefix %q: digits", x)
				}
				prefixes = append(prefixes, x)
			}
		}
		if body.Mode == "prefix" && len(prefixes) == 0 {
			return badRequest("give the prefixes that need a PIN (e.g. 00)")
		}
		for _, x := range body.Trusted {
			if x = strings.TrimSpace(x); x != "" {
				if !reDigitsStar.MatchString(x) || len(x) > 30 {
					return badRequest("extension %q", x)
				}
				trusted = append(trusted, x)
			}
		}
		yes := func(b bool) string { return map[bool]string{true: "true", false: "false"}[b] }
		args := []any{dom.uuid, yes(body.Mode == "all"), yes(body.Mode == "prefix"), yes(body.Mode == ""),
			firstNonEmpty(strings.Join(prefixes, ","), "00"), strings.Join(trusted, ",")}
		tag, err := s.store.Pool().Exec(ctx, `update q_security_rules set pin_all_outbound = $2, pin_prefix_outbound = $3,
			pin_no_outbound = $4, prefixes = $5, trusted_device = $6 where id = (select id from q_security_rules
			where domain_uuid = $1::uuid order by id desc limit 1)`, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.store.Pool().Exec(ctx, `insert into q_security_rules (domain_uuid, security_uuid, pin_all_outbound,
				pin_prefix_outbound, pin_no_outbound, prefixes, trusted_device) values ($1::uuid, $7, $2, $3, $4, $5, $6)`,
				append(args, newUUID())...); err != nil {
				return err
			}
		}
		s.reload()
		s.sw.RecordEvent("security", "PIN-based dialing of "+dom.name+" changed by "+p.key())
		writeJSON(w, map[string]string{"ok": "saved"})
		return nil
	case what == "pins":
		return s.pinsAPI(ctx, w, r, dom, part(parts, 1))
	case what == "autologout" && r.Method == http.MethodPut:
		var body struct{ Minutes int }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		ok := false
		for _, m := range IdleLogoutChoices {
			ok = ok || m == body.Minutes
		}
		if !ok {
			return badRequest("auto logout: one of the times offered")
		}
		if err := s.store.SetSetting(ctx, dom.uuid, "web_idle_logout", strconv.Itoa(body.Minutes)); err != nil {
			return err
		}
		s.idleLogout.Delete(dom.name)
		writeJSON(w, map[string]string{"ok": "saved"})
		return nil
	}
	return errMethod
}

func (s *Server) dialSecurityGet(ctx context.Context, w http.ResponseWriter, dom domainRow) error {
	var blocked string
	_ = s.store.Pool().QueryRow(ctx, `select coalesce(blocked_countries, '') from q_security_restrictions where domain_uuid = $1::uuid
		order by id desc limit 1`, dom.uuid).Scan(&blocked)
	list := []string{}
	for iso := range db.CountryCodes(blocked) {
		list = append(list, iso)
	}
	sort.Strings(list)
	rules, err := s.store.JSONRows(ctx, `select to_jsonb(t) from q_security_rules t where domain_uuid = $1::uuid order by id desc limit 1`, dom.uuid)
	if err != nil {
		return err
	}
	pin := map[string]any{"mode": "", "prefixes": []string{"00"}, "trusted": []string{}}
	if len(rules) > 0 {
		pin["mode"] = db.PINMode(rules[0])
		pin["prefixes"] = db.SplitList(firstNonEmpty(rules[0]["prefixes"], "00"))
		if t := db.SplitList(rules[0]["trusted_device"]); t != nil {
			pin["trusted"] = t
		}
	}
	pins, err := s.store.JSONRows(ctx, `select jsonb_build_object('uuid', pin_number_uuid, 'pin', coalesce(pin_number, ''),
		'accountcode', coalesce(accountcode, ''), 'enabled', coalesce(enabled, 'true'), 'description', coalesce(description, ''))
		from v_pin_numbers where domain_uuid = $1::uuid order by pin_number`, dom.uuid)
	if err != nil {
		return err
	}
	if pins == nil {
		pins = []map[string]string{}
	}
	countries := []map[string]any{}
	for _, c := range config.CountryList {
		countries = append(countries, map[string]any{"iso": c.ISO, "name": c.Name, "continent": c.Continent, "code": c.Display()})
	}
	writeJSON(w, map[string]any{"countries": countries, "continents": config.Continents, "blocked": list, "pin": pin, "pins": pins,
		"idle_logout": s.domainIdleLogout(ctx, dom.uuid), "idle_choices": IdleLogoutChoices})
	return nil
}

func (s *Server) domainIdleLogout(ctx context.Context, domainUUID string) int {
	return atoi(s.store.Setting(ctx, domainUUID, "web_idle_logout"), 0)
}

func (s *Server) pinsAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow, id string) error {
	if id != "" && !reUUID.MatchString(id) {
		return notFound("PIN")
	}
	if r.Method == http.MethodDelete {
		tag, err := s.store.Pool().Exec(ctx, `delete from v_pin_numbers where domain_uuid = $1::uuid and pin_number_uuid = $2::uuid`, dom.uuid, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("PIN")
		}
		s.reload()
		writeJSON(w, map[string]string{"ok": "deleted"})
		return nil
	}
	if (r.Method != http.MethodPost || id != "") && (r.Method != http.MethodPut || id == "") {
		return errMethod
	}
	var body struct {
		PIN         string `json:"pin"`
		AccountCode string `json:"accountcode"`
		Description string `json:"description"`
		Enabled     *bool  `json:"enabled"`
	}
	if err := decodeBody(r, &body); err != nil {
		return err
	}
	body.PIN = strings.TrimSpace(body.PIN)
	if !rePIN.MatchString(body.PIN) {
		return badRequest("PIN: 3 to 16 digits")
	}
	if utf8.RuneCountInString(body.Description) > 64 || utf8.RuneCountInString(body.AccountCode) > 40 {
		return badRequest("description: 64 characters at most")
	}
	enabled := body.Enabled == nil || *body.Enabled
	var n int
	if err := s.store.Pool().QueryRow(ctx, `select count(*) from v_pin_numbers where domain_uuid = $1::uuid and pin_number = $2
		and pin_number_uuid::text <> $3`, dom.uuid, body.PIN, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return badRequest("that PIN exists already")
	}
	en := map[bool]string{true: "true", false: "false"}[enabled]
	if r.Method == http.MethodPost {
		id = newUUID()
		if _, err := s.store.Pool().Exec(ctx, `insert into v_pin_numbers (pin_number_uuid, domain_uuid, pin_number, accountcode, enabled,
			description, insert_date) values ($1, $2::uuid, $3, $4, $5, $6, now())`, id, dom.uuid, body.PIN, body.AccountCode, en, body.Description); err != nil {
			return err
		}
	} else {
		tag, err := s.store.Pool().Exec(ctx, `update v_pin_numbers set pin_number = $3, accountcode = $4, enabled = $5, description = $6,
			update_date = now() where domain_uuid = $1::uuid and pin_number_uuid = $2::uuid`, dom.uuid, id, body.PIN, body.AccountCode, en, body.Description)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("PIN")
		}
	}
	s.reload()
	writeJSON(w, map[string]string{"ok": "saved", "uuid": id})
	return nil
}
