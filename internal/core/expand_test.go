package core

import (
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
)

func testCore(t *testing.T) *Core {
	t.Helper()
	dir := config.NewDirectory("10.0.0.1")
	a := dir.AddDomain("a.example", "11111111-1111-1111-1111-111111111111")
	_ = a.Add(&config.User{ID: "1000", Password: "x", Vars: map[string]string{"call_group": "sales", "extension_uuid": "e1"}, Params: map[string]string{}})
	b := dir.AddDomain("b.example", "")
	_ = b.Add(&config.User{ID: "2000", Password: "y", Vars: map[string]string{}, Params: map[string]string{}})
	cfg := &config.Config{
		Vars:      map[string]string{"us-ring": "%(2000,4000,440,480)", "hold_music": "local_stream://default"},
		Directory: dir, Dialplan: dialplan.New(), Features: config.NewFeatures(),
		Settings: config.Settings{RTPStartPort: 20000, RTPEndPort: 20100},
	}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExpand(t *testing.T) {
	c := testCore(t)
	vars := map[string]string{"destination_number": "1000", "domain_name": "a.example", "x": "5", "name": "abcdef"}
	get := func(n string) string { return vars[n] }
	cases := map[string]string{
		"${destination_number}": "1000",
		"$${us-ring}":           "%(2000,4000,440,480)",
		"${us-ring}":            "%(2000,4000,440,480)", // global fallback
		"${user_exists id ${destination_number} ${domain_name}}":           "true",
		"${user_exists id 2000 ${domain_name}}":                            "false",
		"${user_exists id 2000 b.example}":                                 "true",
		"${user_data ${destination_number}@${domain_name} var call_group}": "sales",
		"${user_data 1000@a.example param password}":                       "x",
		"${cond(${x} > 3 ? yes : no)}":                                     "yes",
		"${cond(${x} == 4 ? yes : no)}":                                    "no",
		"${cond ${destination_number} == 1000 ? one : other}":              "one",
		"${regex(${destination_number}|^10(\\d+)$|%1)}":                    "00",
		"${regex(1234|^\\d+$)}":                                            "true",
		"${expr(2*(3+4))}":                                                 "14",
		"${name:2:3}":                                                      "cde",
		"${name:-2}":                                                       "ef",
		"${sofia_contact 1000@a.example}":                                  "error/user_not_registered",
		"${domain_exists b.example}":                                       "true",
		"pre ${undefined} post":                                            "pre  post",
		"{x=${x}}user/${destination_number}@${domain_name}":                "{x=5}user/1000@a.example",
	}
	for in, want := range cases {
		if got := c.expand(in, get); got != want {
			t.Errorf("expand(%q) = %q, want %q", in, got, want)
		}
	}
	if got := c.expand("${strftime(%Y-%m)}", get); got != time.Now().Format("2006-01") {
		t.Errorf("strftime: %q", got)
	}
	if got := c.expand("${create_uuid}", get); len(got) != 36 {
		t.Errorf("create_uuid: %q", got)
	}
	c.hashes.set("hash", "a.example-call_return", "1000", "1001")
	if got := c.expand("${hash(select/a.example-call_return/1000)}", get); got != "1001" {
		t.Errorf("hash select: %q", got)
	}
}

func TestStrftime(t *testing.T) {
	tm := time.Date(2024, 3, 5, 14, 7, 9, 0, time.UTC)
	if got := Strftime("%Y/%b/%d %H:%M:%S %j %a %%", tm); got != "2024/Mar/05 14:07:09 065 Tue %" {
		t.Fatalf("got %q", got)
	}
}

func TestDomainResolution(t *testing.T) {
	c := testCore(t)
	dir := c.config().Directory
	if dir.Resolve("a.example").Name != "a.example" || dir.Resolve("10.0.0.1").Name != "10.0.0.1" {
		t.Fatal("known domains")
	}
	if dir.Resolve("203.0.113.9").Name != "10.0.0.1" {
		t.Fatal("IP hosts select the default domain")
	}
	if dir.Resolve("typo.example") != nil {
		t.Fatal("unknown names must not resolve with several tenants")
	}
	if dir.LookupIn("b.example", "1000") != nil || dir.LookupIn("a.example", "1000") == nil {
		t.Fatal("users are per domain")
	}
}

func TestCompatReport(t *testing.T) {
	c := testCore(t)
	_, err := c.config().Dialplan.LoadExtensions("a.example", strings.NewReader(`
<extension name="x"><condition field="destination_number" expression="^1$">
  <action application="hash" data="insert/a/b/c"/>
  <action application="frobnicate" data=""/>
  <action application="lua" data="app.lua voicemail"/>
  <action application="set" data="v=${mystery_func arg}"/>
</condition></extension>`), "test")
	if err != nil {
		t.Fatal(err)
	}
	r := c.CompatReport()
	for _, want := range []string{"frobnicate", "lua app.lua voicemail", "mystery_func"} {
		if !strings.Contains(r, want) {
			t.Errorf("report misses %s:\n%s", want, r)
		}
	}
	if strings.Contains(r, "hash") {
		t.Errorf("hash is supported:\n%s", r)
	}
}

func TestRingGroupDialString(t *testing.T) {
	c := testCore(t)
	ch := c.newChannel(DirectionInbound)
	ch.vars["caller_id_name"] = "Bob"
	ch.vars["caller_id_number"] = "5551234"
	rg := &config.RingGroup{UUID: "rg1", Domain: "a.example", Strategy: "simultaneous", CIDNamePrefix: "Sales",
		Destinations: []config.RingGroupDestination{{Number: "1000", Timeout: 20}, {Number: "0123456789", Delay: 5, Timeout: 25}}}
	dial, timeout := c.ringGroupDialString(ch, rg, "a.example")
	if !strings.Contains(dial, "user/1000@a.example") || !strings.Contains(dial, "loopback/0123456789/a.example") ||
		!strings.Contains(dial, "leg_delay_start=5") || !strings.Contains(dial, "'Sales#Bob'") || timeout != 30 {
		t.Fatalf("simultaneous: %s (%d)", dial, timeout)
	}
	if _, err := parseDialString(dial); err != nil {
		t.Fatal(err)
	}
	rg.Strategy = "sequence"
	dial, timeout = c.ringGroupDialString(ch, rg, "a.example")
	if strings.Count(dial, "|") != 1 || timeout != 45 {
		t.Fatalf("sequence: %s (%d)", dial, timeout)
	}
}
