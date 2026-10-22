package dialplan

import (
	"strings"
	"testing"
	"time"
)

type testVars map[string]string

func (v testVars) Var(n string) string  { return v[n] }
func (v testVars) SetVar(n, val string) { v[n] = val }
func (v testVars) Expand(s string) string {
	for k, val := range v {
		s = strings.ReplaceAll(s, "${"+k+"}", val)
	}
	return s
}

const doc = `<?xml version="1.0"?>
<include>
  <context name="default">
    <extension name="tag_all" continue="true">
      <condition field="caller_id_number" expression="^(\d+)$">
        <action application="set" data="caller_tag=$1" inline="true"/>
        <action application="log" data="INFO call from $1"/>
      </condition>
    </extension>
    <extension name="business_hours">
      <condition field="destination_number" expression="^5000$" break="on-false"/>
      <condition wday="mon-fri" time-of-day="09:00-17:00">
        <action application="transfer" data="1000"/>
        <anti-action application="playback" data="closed.wav"/>
      </condition>
    </extension>
    <extension name="local">
      <condition field="destination_number" expression="^(1\d{3})$">
        <action application="bridge" data="user/$1@${domain}"/>
      </condition>
    </extension>
    <extension name="tagged">
      <condition field="${caller_tag}" expression="^42$">
        <action application="answer"/>
      </condition>
    </extension>
  </context>
</include>`

func load(t *testing.T) *Dialplan {
	t.Helper()
	d := New()
	if err := d.Load(strings.NewReader(doc), "test.xml"); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHuntLocalExtension(t *testing.T) {
	d := load(t)
	v := testVars{"destination_number": "1001", "caller_id_number": "1000"}
	acts, matched, err := d.Hunt("default", v, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(matched, ",") != "tag_all,local" {
		t.Fatalf("matched %v", matched)
	}
	if len(acts) != 2 || acts[0].App != "log" || acts[0].Data != "INFO call from 1000" {
		t.Fatalf("unexpected actions %+v", acts)
	}
	if acts[1].App != "bridge" || acts[1].Data != "user/1001@${domain}" {
		t.Fatalf("captures not substituted or variables expanded too early: %+v", acts[1])
	}
	if v["caller_tag"] != "1000" {
		t.Fatalf("inline set did not run: %v", v)
	}
}

func TestTimeConditionsAndAntiActions(t *testing.T) {
	d := load(t)
	tuesdayNoon := time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
	saturday := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		now  time.Time
		want string
	}{{tuesdayNoon, "transfer"}, {saturday, "playback"}} {
		v := testVars{"destination_number": "5000", "caller_id_number": "abc"}
		acts, _, err := d.Hunt("default", v, tc.now)
		if err != nil {
			t.Fatal(err)
		}
		if len(acts) != 1 || acts[0].App != tc.want {
			t.Fatalf("%v: got %+v, want %s", tc.now, acts, tc.want)
		}
	}
}

func TestExpandedFieldAndMissingContext(t *testing.T) {
	d := load(t)
	v := testVars{"destination_number": "9999", "caller_id_number": "42"}
	acts, _, _ := d.Hunt("default", v, time.Now())
	if len(acts) != 2 || acts[1].App != "answer" {
		t.Fatalf("expanded field match failed: %+v", acts)
	}
	if _, _, err := d.Hunt("nope", v, time.Now()); err == nil {
		t.Fatal("expected missing context error")
	}
}

func TestTimeRules(t *testing.T) {
	r, err := parseTimeRule("time-of-day", "22:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	if !r.match(time.Date(2026, 1, 1, 23, 30, 0, 0, time.UTC)) || r.match(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("overnight range")
	}
	r, _ = parseTimeRule("wday", "1,7")
	if !r.match(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)) { // Sunday
		t.Fatal("weekend rule")
	}
	for _, bad := range [][2]string{{"hour", "x"}, {"time-of-day", "25"}, {"wday", ""}} {
		if _, err := parseTimeRule(bad[0], bad[1]); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	for _, bad := range []string{
		`<context><extension/></context>`,
		`<context name="x"><extension name="e"><condition field="a" expression="("/></extension></context>`,
		`<context name="x"><extension name="e"><condition break="sometimes"/></extension></context>`,
		`<context name="x"`,
	} {
		if err := New().Load(strings.NewReader(bad), "bad.xml"); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
	if got := SubstituteCaptures("$1-$2-$9-$$x", []string{"all", "a", "b"}); got != "a-b--$$x" {
		t.Fatalf("captures %q", got)
	}
}

type inlineVars struct {
	testVars
	ran []string
}

func (v *inlineVars) RunInline(app, data string) bool {
	v.ran = append(v.ran, app+" "+data)
	if app == "export" {
		name, val, _ := strings.Cut(data, "=")
		v.testVars[name] = val
		return true
	}
	return false
}

func (v *inlineVars) Field(name string) string {
	if name == "ani" {
		return v.testVars["caller_id_number"]
	}
	return v.testVars[name]
}

// RingQ stores <extension> fragments with nested conditions, regex
// children, empty "always true" conditions and inline exports.
func TestPortalConstructs(t *testing.T) {
	d := New()
	n, err := d.LoadExtensions("a.example", strings.NewReader(`
<extension name="call-direction" continue="true">
  <condition field="${call_direction}" expression="^(inbound|outbound|local)$" break="never">
    <anti-action application="export" data="call_direction=local" inline="true"/>
  </condition>
</extension>
<extension name="user_exists" continue="true">
  <condition field="" expression="">
    <action application="set" data="user_exists=true" inline="true"/>
  </condition>
</extension>
<extension name="business-hours" continue="true">
  <condition regex="any">
    <regex field="ani" expression="^555"/>
    <regex field="destination_number" expression="^9(\d+)$"/>
    <action application="set" data="matched=$1"/>
  </condition>
</extension>
<extension name="local_extension">
  <condition field="${user_exists}" expression="^true$">
    <action application="set" data="hangup_after_bridge=true"/>
    <condition field="destination_number" expression="^(10\d\d)$">
      <action application="bridge" data="user/$1@${domain_name}"/>
    </condition>
  </condition>
</extension>`), "frag")
	if err != nil || n != 4 {
		t.Fatalf("loaded %d: %v", n, err)
	}
	v := &inlineVars{testVars: testVars{"destination_number": "1001", "caller_id_number": "5550000"}}
	acts, matched, err := d.Hunt("a.example", v, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v.testVars["call_direction"] != "local" || v.testVars["user_exists"] != "true" {
		t.Fatalf("inline actions: %v", v.testVars)
	}
	var apps []string
	for _, a := range acts {
		apps = append(apps, a.App+"="+a.Data)
	}
	want := "set=matched=|set=hangup_after_bridge=true|bridge=user/1001@${domain_name}"
	if strings.Join(apps, "|") != want {
		t.Fatalf("actions %v (matched %v)", apps, matched)
	}
	// regex="all" needs every child; xor exactly one.
	d2 := New()
	_, err = d2.LoadExtensions("c", strings.NewReader(`
<extension name="all"><condition regex="all">
  <regex field="destination_number" expression="^1"/><regex field="caller_id_number" expression="^9"/>
  <action application="log" data="all"/>
</condition></extension>
<extension name="xor"><condition regex="xor">
  <regex field="destination_number" expression="^1"/><regex field="caller_id_number" expression="^5"/>
  <action application="log" data="xor"/>
</condition></extension>`), "x")
	if err != nil {
		t.Fatal(err)
	}
	acts, _, _ = d2.Hunt("c", testVars{"destination_number": "1", "caller_id_number": "5"}, time.Now())
	if len(acts) != 0 {
		t.Fatalf("all and xor should both fail: %+v", acts)
	}
	acts, _, _ = d2.Hunt("c", testVars{"destination_number": "1", "caller_id_number": "7"}, time.Now())
	if len(acts) != 1 || acts[0].Data != "xor" {
		t.Fatalf("xor: %+v", acts)
	}
	if _, err := New().LoadExtensions("c", strings.NewReader(`<extension name="bad"><condition regex="some"/></extension>`), "x"); err == nil {
		t.Fatal("invalid regex mode accepted")
	}
}

func TestDateTimeRule(t *testing.T) {
	r, err := parseTimeRule("date-time", "2026-12-25 00:00~2026-12-26 00:00")
	if err != nil {
		t.Fatal(err)
	}
	if !r.match(time.Date(2026, 12, 25, 10, 0, 0, 0, time.Local)) || r.match(time.Date(2026, 12, 26, 0, 0, 1, 0, time.Local)) {
		t.Fatal("date-time range")
	}
	r, _ = parseTimeRule("mweek", "1")
	if !r.match(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("first week of the month")
	}
}
