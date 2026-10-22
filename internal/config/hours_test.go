package config

import (
	"testing"
	"time"
)

func TestScheduleFormat(t *testing.T) {
	// RingQ's defaults: open 9:00 to 18:01 every day; lunch 12:00 to 13:01
	// (Friday 13:31).
	work := ParseSchedule("1-1081-540,2-1081-540,3-1081-540,4-1081-540,5-1081-540,6-1081-540,7-1081-540", true)
	if w := work[1]; !w.Set || w.From != 540 || w.To != 1081 {
		t.Fatalf("Monday: %+v", w)
	}
	brk := ParseSchedule("1-720-781,2-720-781,3-720-781,4-720-781,5-720-781,6-720-811,7-720-781", false)
	if b := brk[5]; b.From != 720 || b.To != 811 {
		t.Fatalf("Friday break: %+v", b)
	}
	// Monday open all day, Sunday closed: written back as RingQ reads it.
	work[1] = DayHours{}
	work[0] = DayHours{Set: true, From: 0, To: 0}
	s := FormatSchedule(work, true)
	if s != "1-0-0,3-1081-540,4-1081-540,5-1081-540,6-1081-540,7-1081-540" {
		t.Fatalf("formatted: %s", s)
	}
	back := ParseSchedule(s, true)
	if back[1].Set || !back[0].Set || back[0].From != back[0].To || back[3] != work[3] {
		t.Fatalf("round trip: %+v", back)
	}
	if ParseSchedule("1-1440-0", true)[0].Set {
		t.Fatal("0 to 1440 is open all day")
	}
}

func TestOfficeHoursCheck(t *testing.T) {
	oh := &OfficeHours{
		Work:        ParseSchedule("2-1080-540,3-1080-540,4-1080-540,5-1080-540,6-1080-540,1-0-0", true), // Mon-Fri 9-18, Sunday closed
		WorkAction:  "transfer:*991001 XML d",
		Break:       ParseSchedule("2-720-780", false),
		BreakAction: "hangup:NORMAL_CLEARING",
		Holidays: []Holiday{
			{Name: "Christmas", Start: Date{0, 12, 25}, End: Date{0, 12, 25}, Recurring: true, FromMin: -1},
			{Name: "Year end", Start: Date{2026, 12, 31}, End: Date{2027, 1, 2}, FromMin: -1, Greeting: "prompts/ny.wav"},
			{Name: "Half day", Start: Date{2026, 11, 3}, End: Date{2026, 11, 3}, FromMin: 13 * 60, ToMin: 24 * 60},
		},
		Exempt: map[string]bool{"5000": true},
	}
	at := func(s string) time.Time {
		tm, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	for _, c := range []struct {
		when, dest, why string
	}{
		{"2026-10-19 10:00", "1001", ""},        // Monday morning
		{"2026-10-19 12:30", "1001", "break"},   // Monday lunch
		{"2026-10-20 12:30", "1001", ""},        // no break on Tuesday
		{"2026-10-19 18:00", "1001", "closed"},  // closing time
		{"2026-10-19 08:59", "1001", "closed"},  // before opening
		{"2026-10-24 11:00", "1001", ""},        // Saturday: open all day (not set)
		{"2026-10-25 11:00", "1001", "closed"},  // Sunday: closed all day
		{"2027-12-25 10:00", "1001", "holiday"}, // every year
		{"2027-01-01 10:00", "1001", "holiday"}, // a range over the new year
		{"2027-01-04 10:00", "1001", ""},
		{"2026-12-25 10:00", "5000", ""}, // exempt
		{"2026-11-03 12:00", "1001", ""}, // half day: morning open
		{"2026-11-03 14:00", "1001", "holiday"},
	} {
		got := oh.Check(at(c.when), c.dest)
		why := ""
		if got != nil {
			why = got.Why
		}
		if why != c.why {
			t.Errorf("%s to %s: %q, want %q", c.when, c.dest, why, c.why)
		}
	}
	if cl := oh.Check(at("2027-01-01 10:00"), "1001"); cl.Greeting != "prompts/ny.wav" || cl.Name != "Year end" {
		t.Fatalf("holiday: %+v", cl)
	}
	// Limited to some numbers (RingQ's work_extensions, a holiday's
	// extensions): other numbers stay open.
	oh.WorkOnly = map[string]bool{"5000": true}
	if cl := oh.Check(at("2026-10-25 11:00"), "1001"); cl != nil {
		t.Fatalf("office hours limited to 5000 closed 1001: %+v", cl)
	}
	if cl := oh.Check(at("2026-10-25 11:00"), "5000"); cl == nil || cl.Why != "closed" {
		t.Fatalf("5000: %+v", cl)
	}
	oh.WorkOnly = nil
	oh.Holidays[0].Only = map[string]bool{"7000": true}
	if cl := oh.Check(at("2027-12-25 10:00"), "1001"); cl != nil { // a Saturday: open all day
		t.Fatalf("a holiday of 7000 only closed 1001: %+v", cl)
	}
	if cl := oh.Check(at("2027-12-25 10:00"), "7000"); cl == nil || cl.Why != "holiday" {
		t.Fatalf("7000 on its holiday: %+v", cl)
	}
	oh.Holidays[0].Only = nil
	// Without an action outside office hours, calls go through.
	oh.WorkAction = ""
	if cl := oh.Check(at("2026-10-25 11:00"), "1001"); cl != nil {
		t.Fatalf("no action: %+v", cl)
	}
	var none *OfficeHours
	if none.Check(time.Now(), "1") != nil {
		t.Fatal("nil schedule")
	}
}

func TestCountries(t *testing.T) {
	for num, want := range map[string]string{
		"+6591234567": "SG", "006591234567": "SG", "01144207946000": "GB", "+12684601234": "AG", "+14165550100": "CA",
		"+12125550100": "US", "+77012345678": "KZ", "+74951234567": "RU", "+441481123456": "GG", "+8821234567": "INTL882",
		"+5999123456": "CW", "91234567": "", "+999": "",
	} {
		if got := CountryOfNumber(num); got != want {
			t.Errorf("%s: %q, want %q", num, got, want)
		}
	}
	n := 0
	for _, c := range CountryList {
		if c.Continent == "North America" {
			n++
		}
		if len(c.Codes) == 0 || c.Name == "" {
			t.Errorf("country %+v", c)
		}
	}
	if n < 39 {
		t.Fatalf("North America: %d countries", n)
	}
	if d := CountryByISO("AG").Display(); d != "+1-268" {
		t.Fatalf("display: %s", d)
	}
	if d := CountryByISO("CA").Display(); d != "+1" {
		t.Fatalf("display: %s", d)
	}
}

func TestNeedsPIN(t *testing.T) {
	o := &OutboundRules{PINMode: "prefix", PINPrefixes: []string{"00"}, PINTrusted: map[string]bool{"1001": true}}
	if !o.NeedsPIN("1002", "0065123") || !o.NeedsPIN("1002", "+65123") || o.NeedsPIN("1002", "61234567") || o.NeedsPIN("1001", "0065123") {
		t.Fatal("prefix mode")
	}
	o.PINMode = "all"
	if !o.NeedsPIN("1002", "61234567") {
		t.Fatal("all mode")
	}
	o.PINMode = ""
	if o.NeedsPIN("1002", "0065") {
		t.Fatal("off")
	}
}
