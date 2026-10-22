package web_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// TestOfficeHours: the Language & office hours page applies to calls
// coming in on a DID: outside office hours, during a break, on holidays
// (with exemptions).
func TestOfficeHours(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6551000", "gateway_uuid": gw["gateway_uuid"], "action": "transfer:1001 XML " + domain})
	p1, p2 := r.phone("1001"), r.phone("1002")

	var oh map[string]any
	r.call("GET", d+"officehours", nil, &oh)
	if oh["language"] != "en-us" || len(oh["work"].([]any)) != 7 || oh["saved"] != false {
		t.Fatalf("defaults: %v", oh)
	}
	loc, _ := time.LoadLocation("Asia/Singapore")
	now := time.Now().In(loc)
	week := func(mode, from, to string) []map[string]string {
		out := make([]map[string]string, 7)
		for i := range out {
			out[i] = map[string]string{"mode": mode, "from": from, "to": to}
		}
		return out
	}
	open, noBreak := week("open", "", ""), week("none", "", "")
	put := func(body map[string]any) {
		t.Helper()
		full := map[string]any{"language": "en-us", "timezone": "Asia/Singapore", "work": open, "work_action": "", "break": noBreak,
			"break_action": "", "holiday_sound": "", "exempt": []string{}}
		for k, v := range body {
			full[k] = v
		}
		r.mustCall("PUT", d+"officehours", full)
	}
	// dial calls the DID from outside and reports the final answer and
	// whether 1001 or 1002 rang.
	dial := func() (int, string) {
		t.Helper()
		done := make(chan *testua.Call, 1)
		go func() { c, _ := r.carrier.Dial("6551000", nil, 10*time.Second, false); done <- c }()
		rang := ""
		for _, p := range []struct {
			ua   *testua.UA
			name string
		}{{p1, "1001"}, {p2, "1002"}} {
			if in, err := p.ua.WaitInvite(1500 * time.Millisecond); err == nil {
				rang = p.name
				_ = in.Reject(486)
				break
			}
		}
		c := <-done
		if c == nil || c.Final == nil {
			t.Fatal("no answer to the call")
		}
		if c.Final.StatusCode == 200 {
			_, _ = c.Bye()
		}
		return c.Final.StatusCode, rang
	}

	// Closed all day, busy signal outside office hours.
	put(map[string]any{"work": week("closed", "", ""), "work_action": "hangup:USER_BUSY"})
	if dom := r.sw.Config().Directory.Find(domain); dom.TimeZone != "Asia/Singapore" || dom.Hours == nil {
		t.Fatalf("not loaded: %+v", dom)
	}
	if code, rang := dial(); code != 486 || rang != "" {
		t.Fatalf("closed: %d, rang %q", code, rang)
	}
	// Open now: the call reaches 1001.
	put(map[string]any{"work": week("hours", now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04")), "work_action": "hangup:USER_BUSY"})
	if _, rang := dial(); rang != "1001" {
		t.Fatalf("open: rang %q", rang)
	}
	// A break now sends calls to 1002.
	put(map[string]any{"break": week("hours", now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04")), "break_action": "transfer:1002 XML " + domain})
	if _, rang := dial(); rang != "1002" {
		t.Fatalf("break: rang %q", rang)
	}
	// A holiday today: the greeting, then the call ends.
	put(nil)
	h := r.mustCall("POST", d+"officehours/holidays", map[string]any{"name": "Founders Day", "start": now.Format("2006-01-02"), "recurring": true})
	if n := r.count(`select count(*) from q_holidays where holiday_name = 'Founders Day' and recurring = 1 and type = 1`); n != 1 {
		t.Fatalf("holiday rows: %d", n)
	}
	done := make(chan *testua.Call, 1)
	go func() { c, _ := r.carrier.Dial("6551000", nil, 10*time.Second, false); done <- c }()
	if _, err := p1.WaitInvite(1500 * time.Millisecond); err == nil {
		t.Fatal("1001 rang on a holiday")
	}
	c := <-done
	if c == nil || c.Final.StatusCode != 200 {
		t.Fatalf("holiday greeting not answered: %+v", c)
	}
	if bye, err := r.carrier.WaitRequest("BYE", 12*time.Second); err != nil {
		t.Fatal("the call did not end after the greeting:", err)
	} else {
		_ = r.carrier.Respond(bye, 200, "", nil, "")
	}
	// 1001 is exempt from holidays: its calls go through.
	put(map[string]any{"exempt": []string{"1001"}})
	if n := r.count(`select count(*) from q_holiday_exemptions where extension = '1001'`); n != 1 {
		t.Fatalf("exemptions: %d", n)
	}
	if _, rang := dial(); rang != "1001" {
		t.Fatalf("exempt: rang %q", rang)
	}
	// The page shows what was saved; the holiday is deleted.
	r.call("GET", d+"officehours", nil, &oh)
	if oh["timezone"] != "Asia/Singapore" || len(oh["holidays"].([]any)) != 1 || oh["exempt"].([]any)[0] != "1001" {
		t.Fatalf("saved: %v", oh)
	}
	r.mustCall("DELETE", fmt.Sprintf("%sofficehours/holidays/%v", d, h["id"]), nil)
	if code := r.call("PUT", d+"officehours", map[string]any{"work": week("hours", "25:00", "26:00")}, nil); code != 400 {
		t.Fatalf("bad times: %d", code)
	}
}

// TestDialSecurity: allowed countries and PIN-based dialing on calls out
// through trunks.
func TestDialSecurity(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	r.mustCall("POST", d+"routes", map[string]any{"route_name": "International", "route_pattern": `^00(\d{6,15})$`, "route_strip": "2", "route_prepend": "+",
		"gateway_uuids": gw["gateway_uuid"]})
	caller := r.phone("1000")
	var ds map[string]any
	r.call("GET", d+"dialsecurity", nil, &ds)
	if len(ds["countries"].([]any)) < 200 || len(ds["blocked"].([]any)) != 0 {
		t.Fatalf("countries: %d %v", len(ds["countries"].([]any)), ds["blocked"])
	}
	call := func(num string) (*testua.Call, *testua.Incoming) {
		t.Helper()
		done := make(chan *testua.Call, 1)
		go func() { c, _ := caller.Dial(num, nil, 10*time.Second, false); done <- c }()
		out, _ := r.carrier.WaitInvite(2 * time.Second)
		if out != nil {
			_ = out.Reject(486)
		}
		return <-done, out
	}
	// The United Kingdom is not allowed.
	r.mustCall("PUT", d+"dialsecurity/countries", map[string]any{"blocked": []string{"GB"}})
	if n := r.count(`select count(*) from q_security_restrictions where blocked_countries = 'GB'`); n != 1 {
		t.Fatal("blocked_countries not saved")
	}
	if c, out := call("00442079460000"); out != nil || c == nil || c.Final.StatusCode < 400 {
		t.Fatalf("UK call: %+v, carrier saw %v", c, out != nil)
	}
	if _, out := call("006561234567"); out == nil {
		t.Fatal("Singapore call did not go out")
	}
	// PIN for numbers starting with 00.
	r.mustCall("PUT", d+"dialsecurity/pin", map[string]any{"mode": "prefix", "prefixes": []string{"00"}, "trusted": []string{"1002"}})
	r.mustCall("POST", d+"dialsecurity/pins", map[string]any{"pin": "4821", "accountcode": "SALES", "description": "Sales"})
	if code := r.call("POST", d+"dialsecurity/pins", map[string]any{"pin": "4821"}, nil); code != 400 {
		t.Fatalf("duplicate PIN: %d", code)
	}
	done := make(chan *testua.Call, 1)
	go func() { c, _ := caller.Dial("006561234567", nil, 10*time.Second, false); done <- c }()
	c := <-done
	if c == nil || c.Final.StatusCode != 200 {
		t.Fatalf("the PIN prompt did not answer: %+v", c)
	}
	time.Sleep(500 * time.Millisecond)
	for _, digit := range "4821#" {
		if resp, err := c.Request("INFO", "application/dtmf-relay", []byte("Signal="+string(digit)+"\r\nDuration=160\r\n")); err != nil || resp.StatusCode != 200 {
			t.Fatalf("INFO %c: %v %v", digit, resp, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	out, err := r.carrier.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("no call out after the PIN:", err)
	}
	if !strings.Contains(out.Invite.RequestURI.String(), "+6561234567") {
		t.Fatalf("number: %s", out.Invite.RequestURI)
	}
	_ = out.Reject(486)
	time.Sleep(300 * time.Millisecond)
	_, _ = c.Bye()
	r.waitCount("PIN and account code in the call record", 1, `select count(*) from v_xml_cdr where pin_number = '4821' and accountcode = 'SALES'`)
	// Auto logout.
	r.mustCall("PUT", d+"dialsecurity/autologout", map[string]any{"minutes": 15})
	if code := r.call("PUT", d+"dialsecurity/autologout", map[string]any{"minutes": 7}, nil); code != 400 {
		t.Fatalf("7 minutes: %d", code)
	}
	var me map[string]any
	r.call("GET", "me", nil, &me)
	if me["idle_logout"] != float64(15) {
		t.Fatalf("me: %v", me["idle_logout"])
	}
}

// TestSIPGuard: protections set from the page, bans by hand, web bans from
// failed sign-ins (behind a proxy, as X-Real-IP), history.
func TestSIPGuard(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	sec := r.mustCall("GET", "security", nil)
	g := sec["guard"].(map[string]any)
	if g["enabled"] != true || len(g["jails"].([]any)) != 7 {
		t.Fatalf("guard: %v", g)
	}
	r.mustCall("PUT", "security/guard", map[string]any{"enabled": true, "jails": map[string]any{
		"web-login": map[string]any{"enabled": true, "max": 3, "window": 60, "ban": 3600}}})
	if n := r.count(`select count(*) from rq_settings where scope = 'server' and name = 'sip_guard'`); n != 1 {
		t.Fatal("settings not saved")
	}
	login := func(ip string) int {
		req, _ := http.NewRequest("POST", r.base+"login", bytes.NewReader([]byte(`{"user":"admin","password":"wrong"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Real-IP", ip)
		resp, err := r.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 3; i++ {
		if code := login("203.0.113.9"); code != 401 {
			t.Fatalf("failed sign-in %d: %d", i, code)
		}
	}
	if code := login("203.0.113.9"); code != 403 {
		t.Fatalf("banned address: %d", code)
	}
	if code := login("198.51.100.4"); code != 401 {
		t.Fatalf("another address: %d", code)
	}
	r.waitCount("the ban in the database", 1, `select count(*) from sip_security_blocks where ip_address = '203.0.113.9' and category = 'web-login' and status = 'banned'`)
	// By hand, then lifted.
	r.mustCall("POST", "security/ban", map[string]any{"ip": "192.0.2.77", "jail": "sip-request"})
	if b := r.sw.GuardBanned("192.0.2.77", "sip"); b == nil || b.Source != "manual" {
		t.Fatalf("manual ban: %+v", b)
	}
	if code := r.call("POST", "security/ban", map[string]any{"ip": "127.0.0.1", "jail": "sip-request"}, nil); code != 400 {
		t.Fatalf("banning this server: %d", code)
	}
	r.mustCall("POST", "security/unban", map[string]any{"ip": "203.0.113.9", "jail": "web-login"})
	if code := login("203.0.113.9"); code != 401 {
		t.Fatalf("after unban: %d", code)
	}
	r.waitCount("unban recorded", 1, `select count(*) from security_audit_log where ip_address = '203.0.113.9' and action = 'unban'`)
	var hist map[string]any
	r.call("GET", "security/history?days=7", nil, &hist)
	if hist["bans"].(float64) < 2 || len(hist["events"].([]any)) < 3 {
		t.Fatalf("history: %v", hist)
	}
	// Web deny rules.
	r.mustCall("POST", "security/rules", map[string]any{"type": "web-deny", "value": "198.51.100.0/24", "description": "test"})
	if code := login("198.51.100.4"); code != 403 {
		t.Fatalf("denied address: %d", code)
	}
	// Bans survive a restart: they are restored from the database.
	ctx := context.Background()
	var n int
	if err := r.conn.QueryRow(ctx, `select count(*) from sip_security_blocks where status = 'banned'`).Scan(&n); err != nil || n < 1 {
		t.Fatalf("stored bans: %d %v", n, err)
	}
	// The global IP restriction.
	r.mustCall("PUT", "security/restrict", map[string]any{"on": true})
	if !r.sw.Config().Security.RestrictSIP {
		t.Fatal("restriction not loaded")
	}
	r.mustCall("PUT", "security/restrict", map[string]any{"on": false})
	// Turning SIP Guard off lifts the bans.
	r.mustCall("PUT", "security/guard", map[string]any{"enabled": false, "jails": map[string]any{}})
	if len(r.sw.GuardBans()) != 0 {
		t.Fatalf("bans with SIP Guard off: %v", r.sw.GuardBans())
	}
}
