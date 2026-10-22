package config

import (
	"strconv"
	"strings"
	"time"
)

// A domain's global office hours, breaks and holidays (RingQ's Language &
// office hours page: q_time_schedules, q_holidays, q_holiday_exemptions),
// applied to calls coming in from outside before they reach their DID's
// destination.

// DayHours is one day of a schedule, in minutes from midnight. For office
// hours: not Set = open all day, From == To = closed all day, else open
// from From until To (overnight when To < From). For breaks: not Set or
// From == To = no break.
type DayHours struct {
	Set      bool
	From, To int
}

// within reports whether minute m of the day is in From..To.
func (d DayHours) within(m int) bool {
	switch {
	case d.From == d.To:
		return false
	case d.From < d.To:
		return m >= d.From && m < d.To
	}
	return m >= d.From || m < d.To
}

// Holiday is a day (or days) the company is closed.
type Holiday struct {
	Name      string
	Greeting  string // sound played ("" = the default holiday sound)
	Start     Date
	End       Date // the same as Start for one day
	Recurring bool // every year (the year is ignored)
	FromMin   int  // closed only from FromMin to ToMin; -1 = all day
	ToMin     int
	Exempt    map[string]bool // numbers still open on this holiday
	Only      map[string]bool // when set, the holiday closes only these numbers
}

// Date is a calendar day.
type Date struct{ Year, Month, Day int }

func (d Date) key(withYear bool) int {
	k := d.Month*100 + d.Day
	if withYear {
		k += d.Year * 10000
	}
	return k
}

// active reports whether the holiday is on at now.
func (h Holiday) active(now time.Time) bool {
	day := Date{now.Year(), int(now.Month()), now.Day()}
	s, e, t := h.Start.key(!h.Recurring), h.End.key(!h.Recurring), day.key(!h.Recurring)
	in := false
	if s <= e {
		in = t >= s && t <= e
	} else { // every year from December into January
		in = t >= s || t <= e
	}
	if !in {
		return false
	}
	if h.FromMin < 0 {
		return true
	}
	return DayHours{Set: true, From: h.FromMin, To: h.ToMin}.within(now.Hour()*60 + now.Minute())
}

// OfficeHours is a domain's schedule.
type OfficeHours struct {
	Work          [7]DayHours // [0] is Sunday
	WorkAction    string      // outside office hours ("app:data"; "" = calls go through)
	Break         [7]DayHours
	BreakAction   string // during breaks
	HolidaySound  string // the default holiday greeting
	HolidayAction string // after the greeting ("" = hang up)
	Holidays      []Holiday
	Exempt        map[string]bool // numbers never closed on holidays (IVRs, queues, agents)
	// When set (RingQ's work_extensions, break_extensions,
	// special_extensions), office hours, breaks or holidays apply only to
	// calls to these numbers.
	WorkOnly, BreakOnly, HolidayOnly map[string]bool
}

// applies reports whether a schedule limited to only covers dest.
func applies(only map[string]bool, dest string) bool { return only == nil || only[dest] }

// Closure is what to do with a call that comes in while closed.
type Closure struct {
	Why      string // holiday, break, closed
	Name     string // the holiday's
	Action   string // "app:data"; "" with a greeting = hang up after it
	Greeting string
}

// Check returns why calls to dest are closed at now (in the domain's time),
// or nil when they go through.
func (oh *OfficeHours) Check(now time.Time, dest string) *Closure {
	if oh == nil {
		return nil
	}
	for _, h := range oh.Holidays {
		if !h.active(now) || oh.Exempt[dest] || h.Exempt[dest] || !applies(oh.HolidayOnly, dest) || !applies(h.Only, dest) {
			continue
		}
		return &Closure{Why: "holiday", Name: h.Name, Action: oh.HolidayAction, Greeting: firstNonEmptyStr(h.Greeting, oh.HolidaySound)}
	}
	wd, m := int(now.Weekday()), now.Hour()*60+now.Minute()
	if b := oh.Break[wd]; oh.BreakAction != "" && b.Set && b.within(m) && applies(oh.BreakOnly, dest) {
		return &Closure{Why: "break", Action: oh.BreakAction}
	}
	if w := oh.Work[wd]; oh.WorkAction != "" && w.Set && !w.within(m) && applies(oh.WorkOnly, dest) {
		return &Closure{Why: "closed", Action: oh.WorkAction}
	}
	return nil
}

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ParseSchedule reads RingQ's schedule columns: "day-a-b,..." with day 1 =
// Sunday ... 7 = Saturday and minutes from midnight. Office hours
// (work_schedule) are written day-close-open, breaks (break_schedule)
// day-start-end.
func ParseSchedule(s string, office bool) [7]DayHours {
	var out [7]DayHours
	for _, part := range strings.Split(s, ",") {
		f := strings.Split(strings.TrimSpace(part), "-")
		if len(f) != 3 {
			continue
		}
		d, err1 := strconv.Atoi(f[0])
		a, err2 := strconv.Atoi(f[1])
		b, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil || d < 1 || d > 7 || a < 0 || b < 0 || a > 1440 || b > 1440 {
			continue
		}
		if office {
			a, b = b, a // open, close
			if a == 0 && b == 1440 {
				continue // open all day
			}
		}
		out[d-1] = DayHours{Set: true, From: a, To: b}
	}
	return out
}

// FormatSchedule writes a schedule as ParseSchedule reads it. Open all day
// (office) and no break are left out.
func FormatSchedule(days [7]DayHours, office bool) string {
	var parts []string
	for i, d := range days {
		if !d.Set {
			continue
		}
		a, b := d.From, d.To
		if office {
			a, b = b, a
		} else if a == b {
			continue
		}
		parts = append(parts, strconv.Itoa(i+1)+"-"+strconv.Itoa(a)+"-"+strconv.Itoa(b))
	}
	return strings.Join(parts, ",")
}

// ParseHHMMRange reads "09:00-12:30" (or minutes, "540-750") into minutes.
func ParseHHMMRange(s string) (int, int, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, false
	}
	from, ok1 := parseMinute(a)
	to, ok2 := parseMinute(b)
	return from, to, ok1 && ok2
}

func parseMinute(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if h, m, ok := strings.Cut(s, ":"); ok {
		hh, err1 := strconv.Atoi(h)
		mm, err2 := strconv.Atoi(m)
		if err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 || hh*60+mm > 1440 {
			return 0, false
		}
		return hh*60 + mm, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0 && n <= 1440
}

// OutboundRules limit calls out through trunks (RingQ's Security settings:
// Allowed countries and PIN-based dialing).
type OutboundRules struct {
	Blocked     map[string]bool   // countries callers may not call (Country.ISO)
	PINMode     string            // "", "all" (every call out) or "prefix"
	PINPrefixes []string          // with "prefix": numbers dialled with these need a PIN
	PINTrusted  map[string]bool   // extensions that never need one
	PINs        map[string]string // valid PINs and their account codes
}

// NeedsPIN reports whether a call from extension ext dialling number needs
// a PIN.
func (o *OutboundRules) NeedsPIN(ext, number string) bool {
	if o == nil || o.PINTrusted[ext] {
		return false
	}
	switch o.PINMode {
	case "all":
		return true
	case "prefix":
		for _, p := range o.PINPrefixes {
			if p != "" && strings.HasPrefix(number, p) {
				return true
			}
			if p == "00" && strings.HasPrefix(number, "+") {
				return true
			}
		}
	}
	return false
}
