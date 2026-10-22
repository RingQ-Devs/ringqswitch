package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/db"
)

// The Language & office hours page of a domain: its language and time
// zone (the RingQ portal's v_domain_settings), the global office hours and breaks
// (RingQ's q_time_schedules), holidays (q_holidays) and the numbers exempt
// from them (q_holiday_exemptions).
//
//	GET  /api/d/{domain}/officehours           everything on the page
//	PUT  /api/d/{domain}/officehours           language, time zone, schedules, actions, sound, exemptions
//	POST /api/d/{domain}/officehours/holidays  a holiday; PUT, DELETE .../holidays/{id}

// dayJSON is one day of a schedule as the page shows it.
type dayJSON struct {
	Mode string `json:"mode"` // office: open, hours, closed; breaks: none, hours
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

func hhmm(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func daysJSON(days [7]config.DayHours, office bool) []dayJSON {
	out := make([]dayJSON, 7)
	for i, d := range days {
		switch {
		case !d.Set:
			out[i] = dayJSON{Mode: map[bool]string{true: "open", false: "none"}[office]}
		case d.From == d.To:
			out[i] = dayJSON{Mode: map[bool]string{true: "closed", false: "none"}[office]}
		default:
			out[i] = dayJSON{Mode: "hours", From: hhmm(d.From), To: hhmm(d.To)}
		}
	}
	return out
}

func parseDays(in []dayJSON, office bool) ([7]config.DayHours, error) {
	var out [7]config.DayHours
	if len(in) != 7 {
		return out, badRequest("a schedule has 7 days")
	}
	for i, d := range in {
		switch d.Mode {
		case "open", "none", "":
		case "closed":
			if office {
				out[i] = config.DayHours{Set: true}
			}
		case "hours":
			from, to, ok := config.ParseHHMMRange(d.From + "-" + d.To)
			if !ok {
				return out, badRequest("%s: times as HH:MM", time.Weekday(i))
			}
			if from == to {
				return out, badRequest("%s: the start and end times are the same", time.Weekday(i))
			}
			out[i] = config.DayHours{Set: true, From: from, To: to}
		default:
			return out, badRequest("unknown schedule %q", d.Mode)
		}
	}
	return out, nil
}

// holidayJSON is a holiday as the page shows it.
type holidayJSON struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Greeting  string   `json:"greeting"`
	Start     string   `json:"start"` // YYYY-MM-DD (the year is ignored when recurring)
	End       string   `json:"end"`
	Recurring bool     `json:"recurring"`
	From      string   `json:"from"` // closed only from..to ("" = all day)
	To        string   `json:"to"`
	Only      []string `json:"only,omitempty"` // set in RingQ: the numbers it closes
}

var reAction = regexp.MustCompile(`^(transfer|hangup):[^<>"]{0,200}$`)

func validAction(v string) error {
	if v == "" || reAction.MatchString(v) {
		return nil
	}
	return badRequest("unknown action %q", v)
}

// soundLanguages are the call languages: English and the sound packs
// installed in the standard sound pack layout (sounds/fr/ca/<voice>).
func soundLanguages(soundsDir string) []map[string]string {
	names := map[string]string{"en": "English", "fr": "French", "de": "German", "es": "Spanish", "it": "Italian", "pt": "Portuguese",
		"ru": "Russian", "zh": "Chinese", "ja": "Japanese", "he": "Hebrew", "sv": "Swedish", "nl": "Dutch", "pl": "Polish", "ar": "Arabic",
		"hi": "Hindi", "ms": "Malay", "th": "Thai", "id": "Indonesian", "ko": "Korean", "tr": "Turkish", "vi": "Vietnamese"}
	out := []map[string]string{{"code": "en-us", "name": "English"}}
	langs, _ := os.ReadDir(soundsDir)
	for _, l := range langs {
		if !l.IsDir() || len(l.Name()) != 2 || l.Name() == "en" {
			continue
		}
		regions, _ := os.ReadDir(filepath.Join(soundsDir, l.Name()))
		for _, r := range regions {
			if r.IsDir() && len(r.Name()) == 2 {
				name := firstNonEmpty(names[l.Name()], l.Name())
				out = append(out, map[string]string{"code": l.Name() + "-" + r.Name(), "name": name + " (" + strings.ToUpper(r.Name()) + ")"})
			}
		}
	}
	return out
}

func (s *Server) officeHoursAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	for _, t := range []string{"q_time_schedules", "q_holidays", "q_holiday_exemptions", "v_domain_settings"} {
		if !s.store.HasTable(t) {
			return badRequest("the database has no %s table (RingQswitch creates it when manage-schema is on)", t)
		}
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		return s.officeHoursGet(ctx, w, dom)
	case len(parts) == 0 && r.Method == http.MethodPut:
		return s.officeHoursPut(ctx, w, r, p, dom)
	case len(parts) >= 1 && parts[0] == "holidays":
		return s.holidaysAPI(ctx, w, r, p, dom, part(parts, 1))
	}
	return errMethod
}

func (s *Server) domainSetting(ctx context.Context, dom domainRow, sub, name string) string {
	var v string
	_ = s.store.Pool().QueryRow(ctx, `select coalesce(domain_setting_value, '') from v_domain_settings where domain_uuid = $1::uuid
		and lower(domain_setting_category) = 'domain' and lower(domain_setting_subcategory) = $2 and lower(domain_setting_name) = $3
		and coalesce(domain_setting_enabled, true) order by domain_setting_order nulls last limit 1`, dom.uuid, sub, name).Scan(&v)
	return v
}

func setDomainSetting(ctx context.Context, tx pgx.Tx, dom domainRow, sub, name, value, desc string) error {
	tag, err := tx.Exec(ctx, `update v_domain_settings set domain_setting_value = $4, domain_setting_enabled = $5, update_date = now()
		where domain_uuid = $1::uuid and lower(domain_setting_category) = 'domain' and lower(domain_setting_subcategory) = $2
		and lower(domain_setting_name) = $3`, dom.uuid, sub, name, value, value != "")
	if err != nil || tag.RowsAffected() > 0 || value == "" {
		return err
	}
	_, err = tx.Exec(ctx, `insert into v_domain_settings (domain_setting_uuid, domain_uuid, domain_setting_category,
		domain_setting_subcategory, domain_setting_name, domain_setting_value, domain_setting_enabled, domain_setting_description,
		insert_date) values ($1, $2::uuid, 'domain', $3, $4, $5, true, $6, now())`, newUUID(), dom.uuid, sub, name, value, desc)
	return err
}

func (s *Server) officeHoursGet(ctx context.Context, w http.ResponseWriter, dom domainRow) error {
	rows, err := s.store.JSONRows(ctx, `select to_jsonb(t) from q_time_schedules t where domain_uuid = $1::uuid
		order by update_date desc nulls last, id desc limit 1`, dom.uuid)
	if err != nil {
		return err
	}
	sched := map[string]string{}
	if len(rows) > 0 {
		sched = rows[0]
	}
	hol, err := s.store.JSONRows(ctx, `select to_jsonb(h) from q_holidays h where domain_uuid = $1::uuid order by year nulls first, month, day, id`, dom.uuid)
	if err != nil {
		return err
	}
	holidays := []holidayJSON{}
	for _, r := range hol {
		h, ok := db.ParseHoliday(r)
		if !ok {
			continue
		}
		hj := holidayJSON{ID: atoi(r["id"], 0), Name: h.Name, Greeting: h.Greeting, Recurring: h.Recurring,
			Start: fmt.Sprintf("%04d-%02d-%02d", h.Start.Year, h.Start.Month, h.Start.Day),
			End:   fmt.Sprintf("%04d-%02d-%02d", h.End.Year, h.End.Month, h.End.Day)}
		if h.FromMin >= 0 {
			hj.From, hj.To = hhmm(h.FromMin), hhmm(h.ToMin)
		}
		hj.Only = db.SplitList(r["extensions"])
		holidays = append(holidays, hj)
	}
	ex, err := s.store.JSONRows(ctx, `select jsonb_build_object('n', extension) from q_holiday_exemptions where domain_uuid = $1::uuid`, dom.uuid)
	if err != nil {
		return err
	}
	exempt := []string{}
	for _, r := range ex {
		if n := strings.TrimSpace(r["n"]); n != "" {
			exempt = append(exempt, n)
		}
	}
	choices, err := s.exemptChoices(ctx, dom)
	if err != nil {
		return err
	}
	work := config.ParseSchedule(firstNonEmpty(sched["work_schedule"], defaultWork), true)
	brk := config.ParseSchedule(firstNonEmpty(sched["break_schedule"], defaultBreak), false)
	if len(rows) > 0 {
		work = config.ParseSchedule(sched["work_schedule"], true)
		brk = config.ParseSchedule(sched["break_schedule"], false)
	}
	prompts, _ := s.soundList()
	sounds := []string{}
	for _, f := range prompts {
		sounds = append(sounds, "prompts/"+f)
	}
	serverZone := time.Local.String()
	if serverZone == "Local" {
		serverZone = ""
	}
	writeJSON(w, map[string]any{
		"language": firstNonEmpty(strings.ToLower(s.domainSetting(ctx, dom, "language", "code")), "en-us"), "languages": soundLanguages(s.sw.Config().Settings.SoundsDir),
		"timezone": s.domainSetting(ctx, dom, "time_zone", "name"), "server_timezone": serverZone, "now": time.Now().Unix(),
		"work": daysJSON(work, true), "work_action": strings.TrimSpace(sched["work_default"]),
		"break": daysJSON(brk, false), "break_action": strings.TrimSpace(sched["break_default"]),
		"holiday_sound": strings.TrimSpace(sched["special_filename"]), "holidays": holidays, "exempt": exempt,
		"exempt_choices": choices, "sounds": sounds, "saved": len(rows) > 0,
		// Set in RingQ: office hours, breaks or holidays only for these numbers.
		"work_only": orEmpty(db.SplitList(sched["work_extensions"])), "break_only": orEmpty(db.SplitList(sched["break_extensions"])),
		"holiday_only": orEmpty(db.SplitList(sched["special_extensions"])),
	})
	return nil
}

// RingQ's default schedules: open 9:00 to 18:01, lunch 12:00 to 13:01.
const (
	defaultWork  = "1-1081-540,2-1081-540,3-1081-540,4-1081-540,5-1081-540,6-1081-540,7-1081-540"
	defaultBreak = "1-720-781,2-720-781,3-720-781,4-720-781,5-720-781,6-720-811,7-720-781"
)

// exemptChoices are what may be exempt from holidays: IVR menus, queues
// and agents (by number).
func (s *Server) exemptChoices(ctx context.Context, dom domainRow) ([]choice, error) {
	out := []choice{}
	for _, st := range []struct{ res, group, name, num string }{
		{"ivrs", "IVR", "ivr_menu_name", "ivr_menu_extension"}, {"queues", "Queue", "queue_name", "queue_extension"},
		{"agents", "Agent", "agent_name", "extension"},
	} {
		res := resourceByName(st.res)
		if res == nil || !s.store.HasTable(res.table) {
			continue
		}
		rows, err := s.list(ctx, res, dom)
		if err != nil {
			return nil, err
		}
		for _, m := range rows {
			if n := m[st.num]; n != "" {
				out = append(out, choice{st.group, strings.TrimSpace(n + " " + m[st.name]), n})
			}
		}
	}
	return out, nil
}

func (s *Server) officeHoursPut(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow) error {
	var body struct {
		Language     string    `json:"language"`
		Timezone     string    `json:"timezone"`
		Work         []dayJSON `json:"work"`
		WorkAction   string    `json:"work_action"`
		Break        []dayJSON `json:"break"`
		BreakAction  string    `json:"break_action"`
		HolidaySound string    `json:"holiday_sound"`
		Exempt       []string  `json:"exempt"`
	}
	if err := decodeBody(r, &body); err != nil {
		return err
	}
	if body.Timezone != "" {
		if _, err := time.LoadLocation(body.Timezone); err != nil || body.Timezone == "Local" {
			return badRequest("unknown time zone %q", body.Timezone)
		}
	}
	lang := strings.ToLower(strings.TrimSpace(body.Language))
	if lang != "" && !regexp.MustCompile(`^[a-z]{2}(-[a-z]{2})?$`).MatchString(lang) {
		return badRequest("language: a code such as en-us")
	}
	work, err := parseDays(body.Work, true)
	if err != nil {
		return err
	}
	brk, err := parseDays(body.Break, false)
	if err != nil {
		return err
	}
	for _, a := range []string{body.WorkAction, body.BreakAction} {
		if err := validAction(a); err != nil {
			return err
		}
	}
	if err := s.validSound(body.HolidaySound); err != nil {
		return err
	}
	exempt := map[string]bool{}
	for _, n := range body.Exempt {
		n = strings.TrimSpace(n)
		if !reDigitsStar.MatchString(n) || len(n) > 30 {
			return badRequest("exempt number %q", n)
		}
		exempt[n] = true
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := setDomainSetting(ctx, tx, dom, "time_zone", "name", body.Timezone, "Time zone (Language & office hours)"); err != nil {
		return err
	}
	if err := setDomainSetting(ctx, tx, dom, "language", "code", lang, "Language (Language & office hours)"); err != nil {
		return err
	}
	ws, bs := config.FormatSchedule(work, true), config.FormatSchedule(brk, false)
	tag, err := tx.Exec(ctx, `update q_time_schedules set work_schedule = $2, work_default = $3, break_schedule = $4, break_default = $5,
		special_filename = $6, update_date = now() where id = (select id from q_time_schedules where domain_uuid = $1::uuid
		order by update_date desc nulls last, id desc limit 1)`, dom.uuid, ws, body.WorkAction, bs, body.BreakAction, body.HolidaySound)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := tx.Exec(ctx, `insert into q_time_schedules (domain_uuid, work_schedule, work_default, break_schedule, break_default,
			special_filename, insert_date, update_date) values ($1::uuid, $2, $3, $4, $5, $6, now(), now())`,
			dom.uuid, ws, body.WorkAction, bs, body.BreakAction, body.HolidaySound); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `delete from q_holiday_exemptions where domain_uuid = $1::uuid`, dom.uuid); err != nil {
		return err
	}
	nums := make([]string, 0, len(exempt))
	for n := range exempt {
		nums = append(nums, n)
	}
	sort.Strings(nums)
	for _, n := range nums {
		if _, err := tx.Exec(ctx, `insert into q_holiday_exemptions (exempt_uuid, domain_uuid, extension, insert_date, update_date)
			values ($1, $2::uuid, $3, now(), now())`, newUUID(), dom.uuid, n); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.reload()
	s.log.Info("web: office hours saved", "domain", dom.name, "user", p.key())
	writeJSON(w, map[string]string{"ok": "saved"})
	return nil
}

var reDigitsStar = regexp.MustCompile(`^[0-9*#+]+$`)

// validSound accepts "" or a prompt file that exists (prompts/x.wav).
func (s *Server) validSound(v string) error {
	if v == "" {
		return nil
	}
	p, err := s.soundPath(v)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		return badRequest("sound %s not found: upload it first", v)
	}
	return nil
}

func parseDay(v string) (config.Date, bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return config.Date{}, false
	}
	return config.Date{Year: t.Year(), Month: int(t.Month()), Day: t.Day()}, true
}

func (s *Server) holidaysAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, id string) error {
	if r.Method == http.MethodDelete {
		tag, err := s.store.Pool().Exec(ctx, `delete from q_holidays where domain_uuid = $1::uuid and id = $2`, dom.uuid, atoi(id, -1))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("holiday")
		}
		s.reload()
		writeJSON(w, map[string]string{"ok": "deleted"})
		return nil
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		return errMethod
	}
	var h holidayJSON
	if err := decodeBody(r, &h); err != nil {
		return err
	}
	h.Name = strings.TrimSpace(h.Name)
	if h.Name == "" || utf8.RuneCountInString(h.Name) > 80 {
		return badRequest("name: 1 to 80 characters")
	}
	start, ok := parseDay(h.Start)
	if !ok {
		return badRequest("date: YYYY-MM-DD")
	}
	end := start
	if h.End != "" {
		if end, ok = parseDay(h.End); !ok {
			return badRequest("end date: YYYY-MM-DD")
		}
	}
	if !h.Recurring && (end.Year*10000+end.Month*100+end.Day) < (start.Year*10000+start.Month*100+start.Day) {
		return badRequest("the end date is before the start date")
	}
	tm := ""
	if h.From != "" || h.To != "" {
		from, to, ok := config.ParseHHMMRange(h.From + "-" + h.To)
		if !ok || from == to {
			return badRequest("hours: from and to as HH:MM")
		}
		tm = hhmm(from) + "-" + hhmm(to)
	}
	if err := s.validSound(h.Greeting); err != nil {
		return err
	}
	typ := 1
	if end != start {
		typ = 2
	}
	rec := 0
	if h.Recurring {
		rec = 1
	}
	if r.Method == http.MethodPost {
		var newID int
		if err := s.store.Pool().QueryRow(ctx, `insert into q_holidays (domain_uuid, holiday_name, holiday_greeting, month, day, year,
			recurring, month2, day2, year2, time, type, holiday_uuid, insert_date, update_date)
			values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now(), now()) returning id`,
			dom.uuid, h.Name, h.Greeting, start.Month, start.Day, start.Year, rec, end.Month, end.Day, end.Year, tm, typ, newUUID()).Scan(&newID); err != nil {
			return err
		}
		h.ID = newID
	} else {
		tag, err := s.store.Pool().Exec(ctx, `update q_holidays set holiday_name = $3, holiday_greeting = $4, month = $5, day = $6, year = $7,
			recurring = $8, month2 = $9, day2 = $10, year2 = $11, time = $12, type = $13, update_date = now()
			where domain_uuid = $1::uuid and id = $2`, dom.uuid, atoi(id, -1), h.Name, h.Greeting, start.Month, start.Day, start.Year,
			rec, end.Month, end.Day, end.Year, tm, typ)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("holiday")
		}
		h.ID = atoi(id, 0)
	}
	s.reload()
	s.log.Info("web: holiday saved", "domain", dom.name, "holiday", h.Name, "user", p.key())
	writeJSON(w, map[string]any{"ok": "saved", "id": strconv.Itoa(h.ID)})
	return nil
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
