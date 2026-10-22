package db

import (
	"context"
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// Per-domain settings of the Language & office hours and Security pages,
// in the tables RingQ keeps them in, and SIP Guard.

// SplitList reads a list stored as JSON (["a","b"]) or separated by commas,
// semicolons or spaces.
func SplitList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "[") {
		var list []any
		if json.Unmarshal([]byte(v), &list) == nil {
			var out []string
			for _, x := range list {
				if s := strings.TrimSpace(strings.Trim(jsonScalar(x), `"`)); s != "" {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '|' })
}

func jsonScalar(x any) string {
	switch t := x.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	b, _ := json.Marshal(x)
	return string(b)
}

// CountryCodes turns stored countries (ISO codes, or calling codes as some
// tools keep them) into ISO codes.
func CountryCodes(v string) map[string]bool {
	out := map[string]bool{}
	for _, c := range SplitList(v) {
		c = strings.TrimPrefix(strings.TrimSpace(c), "+")
		if c == "" {
			continue
		}
		if c[0] >= '0' && c[0] <= '9' {
			if iso := config.CountryOfNumber("+" + strings.ReplaceAll(c, "-", "")); iso != "" {
				out[iso] = true
			}
			continue
		}
		if cc := config.CountryByISO(c); cc != nil {
			out[cc.ISO] = true
		}
	}
	return out
}

func (s *Store) loadDomainRules(ctx context.Context, cfg *config.Config, domains map[string]dbDomain, warn func(string, ...any)) error {
	dom := func(uuid string) *config.Domain {
		if d, ok := domains[uuid]; ok {
			return cfg.Directory.Find(d.name)
		}
		return nil
	}
	if s.HasTable("v_domain_settings") {
		rows, err := s.jsonRows(ctx, `select jsonb_build_object('d', domain_uuid, 'sub', lower(domain_setting_subcategory),
			'name', lower(domain_setting_name), 'value', domain_setting_value) from v_domain_settings
			where lower(domain_setting_category) = 'domain' and lower(domain_setting_subcategory) in ('time_zone', 'language')
			and coalesce(domain_setting_enabled, true) order by domain_setting_order nulls last`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			d := dom(r["d"])
			v := strings.TrimSpace(r["value"])
			if d == nil || v == "" {
				continue
			}
			switch {
			case r["sub"] == "time_zone" && r["name"] == "name" && d.TimeZone == "":
				loc, err := time.LoadLocation(v)
				if err != nil {
					warn("domain %s: time zone %q: %v", d.Name, v, err)
					continue
				}
				d.TimeZone, d.Loc = v, loc
			case r["sub"] == "language" && r["name"] == "code" && d.Language == "":
				d.Language = strings.ToLower(v)
			}
		}
	}
	hours := func(d *config.Domain) *config.OfficeHours {
		if d.Hours == nil {
			d.Hours = &config.OfficeHours{Exempt: map[string]bool{}}
		}
		return d.Hours
	}
	if s.HasTable("q_time_schedules") {
		rows, err := s.jsonRows(ctx, `select to_jsonb(t) from (select distinct on (domain_uuid) * from q_time_schedules
			order by domain_uuid, update_date desc nulls last, id desc) t`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			d := dom(r["domain_uuid"])
			if d == nil {
				continue
			}
			oh := hours(d)
			oh.Work = config.ParseSchedule(r["work_schedule"], true)
			oh.Break = config.ParseSchedule(r["break_schedule"], false)
			oh.WorkAction = scheduleAction(r["work_default"])
			oh.BreakAction = scheduleAction(r["break_default"])
			oh.HolidaySound = strings.TrimSpace(r["special_filename"])
			oh.HolidayAction = scheduleAction(r["special_default"])
			oh.WorkOnly, oh.BreakOnly, oh.HolidayOnly = NumberSet(r["work_extensions"]), NumberSet(r["break_extensions"]), NumberSet(r["special_extensions"])
		}
	}
	if s.HasTable("q_holidays") {
		rows, err := s.jsonRows(ctx, `select to_jsonb(h) from q_holidays h order by year nulls first, month, day, id`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			d := dom(r["domain_uuid"])
			if d == nil {
				continue
			}
			h, ok := ParseHoliday(r)
			if !ok {
				warn("domain %s: holiday %q has no valid date", d.Name, r["holiday_name"])
				continue
			}
			oh := hours(d)
			oh.Holidays = append(oh.Holidays, h)
		}
	}
	if s.HasTable("q_holiday_exemptions") {
		rows, err := s.jsonRows(ctx, `select jsonb_build_object('d', domain_uuid, 'n', extension) from q_holiday_exemptions`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if d := dom(r["d"]); d != nil && strings.TrimSpace(r["n"]) != "" {
				hours(d).Exempt[strings.TrimSpace(r["n"])] = true
			}
		}
	}
	out := func(d *config.Domain) *config.OutboundRules {
		if d.Outbound == nil {
			d.Outbound = &config.OutboundRules{Blocked: map[string]bool{}, PINTrusted: map[string]bool{}, PINs: map[string]string{}}
		}
		return d.Outbound
	}
	if s.HasTable("q_security_restrictions") {
		rows, err := s.jsonRows(ctx, `select jsonb_build_object('d', domain_uuid, 'c', blocked_countries) from q_security_restrictions`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if d := dom(r["d"]); d != nil {
				for iso := range CountryCodes(r["c"]) {
					out(d).Blocked[iso] = true
				}
			}
		}
	}
	if s.HasTable("q_security_rules") {
		rows, err := s.jsonRows(ctx, `select to_jsonb(t) from (select distinct on (domain_uuid) * from q_security_rules
			order by domain_uuid, id desc) t`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			d := dom(r["domain_uuid"])
			if d == nil {
				continue
			}
			o := out(d)
			o.PINMode = PINMode(r)
			o.PINPrefixes = SplitList(firstNonEmpty(r["prefixes"], "00"))
			for _, e := range SplitList(r["trusted_device"]) {
				o.PINTrusted[e] = true
			}
		}
	}
	if s.HasTable("v_pin_numbers") {
		rows, err := s.jsonRows(ctx, `select jsonb_build_object('d', domain_uuid, 'pin', pin_number, 'acct', accountcode)
			from v_pin_numbers where coalesce(enabled, 'true') <> 'false'`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if d := dom(r["d"]); d != nil && strings.TrimSpace(r["pin"]) != "" {
				out(d).PINs[strings.TrimSpace(r["pin"])] = r["acct"]
			}
		}
	}
	return nil
}

// PINMode reads which calls need a PIN from a q_security_rules row.
func PINMode(r map[string]string) string {
	switch {
	case truthy(r["pin_all_outbound"]):
		return "all"
	case truthy(r["pin_prefix_outbound"]):
		return "prefix"
	}
	return ""
}

// scheduleAction reads the action of a schedule: "app:data" as the other
// destinations are kept ("hangup" alone is accepted).
func scheduleAction(v string) string {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "":
		return ""
	case "hangup", "hang up":
		return "hangup:NORMAL_CLEARING"
	}
	return v
}

// ParseHoliday reads a q_holidays row.
func ParseHoliday(r map[string]string) (config.Holiday, bool) {
	start := config.Date{Year: atoi(r["year"], 0), Month: atoi(r["month"], 0), Day: atoi(r["day"], 0)}
	end := config.Date{Year: atoi(r["year2"], 0), Month: atoi(r["month2"], 0), Day: atoi(r["day2"], 0)}
	valid := func(d config.Date) bool { return d.Month >= 1 && d.Month <= 12 && d.Day >= 1 && d.Day <= 31 }
	if !valid(start) {
		return config.Holiday{}, false
	}
	if !valid(end) {
		end = start
	}
	if end.Year == 0 {
		end.Year = start.Year
	}
	h := config.Holiday{Name: r["holiday_name"], Greeting: strings.TrimSpace(r["holiday_greeting"]), Start: start, End: end,
		Recurring: truthy(r["recurring"]) || start.Year == 0, FromMin: -1, Exempt: map[string]bool{}}
	if a, b, ok := config.ParseHHMMRange(r["time"]); ok && a != b {
		h.FromMin, h.ToMin = a, b
	}
	for _, n := range SplitList(r["exemptions"]) {
		h.Exempt[n] = true
	}
	h.Only = NumberSet(r["extensions"])
	return h, true
}

// NumberSet reads a list of numbers (nil when empty: no limit).
func NumberSet(v string) map[string]bool {
	list := SplitList(v)
	if len(list) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, n := range list {
		out[n] = true
	}
	return out
}

// GuardSettings is SIP Guard as stored in rq_settings (server, sip_guard).
type GuardSettings struct {
	Enabled bool                    `json:"enabled"`
	Jails   map[string]JailSettings `json:"jails"`
}

// JailSettings is one protection (times in seconds).
type JailSettings struct {
	Enabled bool `json:"enabled"`
	Max     int  `json:"max"`
	Window  int  `json:"window"`
	Ban     int  `json:"ban"`
}

// Setting returns a value of rq_settings ("" when missing).
func (s *Store) Setting(ctx context.Context, scope, name string) string {
	if !s.HasTable("rq_settings") {
		return ""
	}
	var v string
	_ = s.pool.QueryRow(ctx, `select value from rq_settings where scope = $1 and name = $2`, scope, name).Scan(&v)
	return v
}

// SetSetting stores a value of rq_settings.
func (s *Store) SetSetting(ctx context.Context, scope, name, value string) error {
	_, err := s.pool.Exec(ctx, `insert into rq_settings (scope, name, value, updated_at) values ($1, $2, $3, now())
		on conflict (scope, name) do update set value = excluded.value, updated_at = now()`, scope, name, value)
	return err
}

func (s *Store) loadGuard(ctx context.Context, cfg *config.Config, warn func(string, ...any)) error {
	g := config.DefaultGuard(cfg.Settings)
	if v := s.Setting(ctx, "server", "sip_guard"); v != "" {
		var gs GuardSettings
		if err := json.Unmarshal([]byte(v), &gs); err != nil {
			warn("SIP Guard settings: %v", err)
		} else {
			g.Enabled = gs.Enabled
			for name, j := range gs.Jails {
				if _, known := g.Jails[name]; !known || j.Max <= 0 || j.Window <= 0 || j.Ban <= 0 {
					continue
				}
				g.Jails[name] = config.Jail{Enabled: j.Enabled, MaxFailures: j.Max, Window: time.Duration(j.Window) * time.Second,
					BanTime: time.Duration(j.Ban) * time.Second}
			}
		}
	}
	cfg.Security.Guard = &g
	cfg.Security.RestrictSIP = truthy(s.Setting(ctx, "server", "restrict_sip"))
	if !s.HasTable("sip_security_blocks") {
		return nil
	}
	rows, err := s.jsonRows(ctx, `select jsonb_build_object('ip', host(ip_address), 'jail', coalesce(jail_name, category),
		'since', extract(epoch from blocked_at), 'until', metadata->>'until', 'n', failure_count, 'reason', reason, 'source', source)
		from sip_security_blocks where status = 'banned'`)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, r := range rows {
		ip, err := netip.ParseAddr(r["ip"])
		until, err2 := strconv.ParseFloat(r["until"], 64)
		if err != nil || err2 != nil || time.Unix(int64(until), 0).Before(now) {
			continue
		}
		since, _ := strconv.ParseFloat(r["since"], 64)
		cfg.Security.Bans = append(cfg.Security.Bans, config.StoredBan{IP: ip, Jail: r["jail"], Since: time.Unix(int64(since), 0),
			Until: time.Unix(int64(until), 0), Failures: atoi(r["n"], 0), Reason: r["reason"], Source: r["source"]})
	}
	return nil
}
