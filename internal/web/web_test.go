package web_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/db"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
	"github.com/Cal4Care-Developers/RingQswitch/internal/web"
)

// These tests need PostgreSQL: RINGQ_TEST_DSN=postgres://user:pass@host/db
// Each run works in a fresh schema (internal/db/ringq-schema.sql) that
// is dropped afterwards.

const (
	domainUUID = "a0000000-0000-4000-8000-0000000000aa"
	domain     = "web.example"
	password   = "Secret-2000"
)

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func testSchema(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("RINGQ_TEST_DSN")
	if dsn == "" {
		t.Skip("RINGQ_TEST_DSN not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	schema := "rqweb_" + strings.ReplaceAll(uuid()[:13], "-", "")
	ddl := []byte(db.RingQSchema())
	if _, err := conn.Exec(ctx, "create schema "+schema+"; set search_path to "+schema+";\n"+string(ddl)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, err := pgx.Connect(context.Background(), dsn); err == nil {
			_, _ = c.Exec(context.Background(), "drop schema "+schema+" cascade")
			c.Close(context.Background())
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

type rig struct {
	t       *testing.T
	sw      *core.Core
	conn    *pgx.Conn
	base    string
	client  *http.Client
	sipAddr netip.AddrPort
	carrier *testua.UA
	srv     *web.Server
}

func newRig(t *testing.T) *rig { t.Helper(); return newRigWith(t, "") }

// newRigWith adds settings (<param .../> lines) to the configuration.
func newRigWith(t *testing.T, settings string) *rig {
	t.Helper()
	dsn := testSchema(t)
	carrier, err := testua.NewAt("carrier", "", netip.AddrPort{}, netip.MustParseAddr("127.0.0.3"))
	if err != nil {
		t.Skip("127.0.0.3 not usable:", err)
	}
	t.Cleanup(carrier.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, `insert into v_domains (domain_uuid, domain_name, domain_enabled) values ($1, $2, true)`, domainUUID, domain); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"1000", "1001", "1002"} {
		if _, err := conn.Exec(ctx, `insert into v_extensions (extension_uuid, domain_uuid, extension, password, user_context, enabled)
			values ($1, $2, $3, $4, $5, 'true')`, uuid(), domainUUID, e, password, domain); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	main := fmt.Sprintf(`<configuration>
  <settings>
    <param name="sip-ip" value="127.0.0.1"/>
    <param name="sip-port" value="0"/>
    <param name="enable-tcp" value="false"/>
    <param name="rtp-ip" value="127.0.0.1"/>
    <param name="rtp-start-port" value="20000"/>
    <param name="rtp-end-port" value="20998"/>
    <param name="event-socket-listen" value=""/>
    <param name="cdr-csv" value=""/>
    <param name="sounds-dir" value="%[1]s/sounds"/>
    <param name="recordings-dir" value="%[1]s/recordings"/>
    <param name="voicemail-dir" value="%[1]s/voicemail"/>
    %[3]s
  </settings>
  <database>
    <param name="dsn" value="%[2]s"/>
    <param name="poll-seconds" value="0"/>
  </database>
  <web>
    <param name="listen" value="127.0.0.1:0"/>
    <param name="tls" value="off"/>
    <param name="admin-password" value="Web-Test-Pass"/>
    <param name="data-dir" value="%[1]s"/>
  </web>
</configuration>`, dir, strings.ReplaceAll(dsn, "&", "&amp;"), settings)
	if err := os.WriteFile(filepath.Join(dir, "ringqswitch.xml"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if os.Getenv("RQ_TEST_DEBUG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	store, err := db.Open(ctx, cfg.Database, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := web.InstallDefaults(ctx, store); err != nil {
		t.Fatal(err)
	}
	if err := store.Overlay(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	sw, err := core.New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	sw.SetLoader(func(d string) (*config.Config, error) {
		c, err := config.Load(d)
		if err != nil {
			return nil, err
		}
		return c, store.Overlay(ctx, c)
	})
	if err := sw.Start(); err != nil {
		t.Fatal(err)
	}
	w := store.StartWriter(sw)
	srv, err := web.Start(cfg.Web, sw, store, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(); sw.Stop(); w.Close() })
	jar, _ := cookiejar.New(nil)
	r := &rig{t: t, sw: sw, conn: conn, base: "http://" + srv.Addr().String() + "/api/", client: &http.Client{Jar: jar, Timeout: 10 * time.Second},
		sipAddr: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(sw.SIPPort())), carrier: carrier, srv: srv}
	carrier.Server = r.sipAddr
	return r
}

// call sends an API request and decodes the JSON answer into out (when not
// nil). It returns the HTTP status.
func (r *rig) call(method, path string, body any, out any, header ...string) int {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, r.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if len(header) == 0 {
		req.Header.Set("X-RQ", "1")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			r.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	return resp.StatusCode
}

func (r *rig) mustCall(method, path string, body any) map[string]any {
	r.t.Helper()
	var out map[string]any
	if code := r.call(method, path, body, &out); code != 200 {
		r.t.Fatalf("%s %s: HTTP %d %v", method, path, code, out)
	}
	return out
}

func (r *rig) phone(ext string) *testua.UA {
	r.t.Helper()
	ua, err := testua.New(ext, password, r.sipAddr)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(ua.Close)
	ua.Domain = domain
	resp, err := ua.Register(300)
	if err != nil || resp.StatusCode != 200 {
		r.t.Fatalf("register %s: %v %v", ext, resp, err)
	}
	return ua
}

func (r *rig) count(sql string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.conn.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (r *rig) waitCount(what string, want int, sql string, args ...any) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	n := -1
	for time.Now().Before(deadline) {
		if n = r.count(sql, args...); n >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("%s: have %d rows, want %d", what, n, want)
}

func waitAny(timeout time.Duration, phones ...*testua.UA) (int, *testua.Incoming) {
	type res struct {
		i  int
		in *testua.Incoming
	}
	ch := make(chan res, len(phones))
	for i, p := range phones {
		go func(i int, p *testua.UA) {
			if in, err := p.WaitInvite(timeout); err == nil {
				ch <- res{i, in}
			}
		}(i, p)
	}
	select {
	case x := <-ch:
		return x.i, x.in
	case <-time.After(timeout):
		return -1, nil
	}
}

func TestLoginAndProtection(t *testing.T) {
	r := newRig(t)
	var out map[string]any
	if code := r.call("GET", "me", nil, &out); code != http.StatusUnauthorized {
		t.Fatalf("me without a session: HTTP %d", code)
	}
	if code := r.call("POST", "login", map[string]string{"user": "admin", "password": "wrong"}, &out); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: HTTP %d", code)
	}
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	me := r.mustCall("GET", "me", nil)
	if me["brand"] != "RingQswitch" || me["database"] != true {
		t.Fatalf("me: %v", me)
	}
	if code := r.call("POST", "system/reload", map[string]string{}, &out, "no X-RQ"); code != http.StatusForbidden {
		t.Fatalf("write without X-RQ: HTTP %d", code)
	}
	// Bad input is refused with a message, not stored.
	if code := r.call("POST", "d/"+domain+"/extensions", map[string]string{"extension": "12ab", "password": "secret12"}, &out); code != 400 {
		t.Fatalf("bad extension: HTTP %d %v", code, out)
	}
	if code := r.call("GET", "sounds/file?path=../../etc/passwd", nil, &out); code != 400 {
		t.Fatalf("path traversal: HTTP %d %v", code, out)
	}
	if code := r.call("POST", "d/"+domain+"/queues", map[string]string{"queue_name": "Q", "queue_extension": "1001"}, &out); code != 400 {
		t.Fatalf("queue on an extension's number: HTTP %d %v", code, out)
	}
}

// TestGatewayDIDQueueAndBreaks builds the call flow the way an administrator
// does in the web interface (gateway, agents, queue, DID on the gateway
// pointing at the queue) and checks that calls reach available agents only:
// an agent on a break (AUX code) or logged out gets no calls, and every
// status change is in the reports.
func TestGatewayDIDQueueAndBreaks(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"

	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	agent := map[string]string{}
	for _, ext := range []string{"1001", "1002"} {
		a := r.mustCall("POST", d+"agents", map[string]any{"agent_name": "Agent " + ext, "extension": ext, "agent_call_timeout": "5"})
		agent[ext] = a["call_center_agent_uuid"].(string)
	}
	q := r.mustCall("POST", d+"queues", map[string]any{"queue_name": "Support", "queue_extension": "3000", "queue_strategy": "ring-all",
		"queue_moh_sound": "silence", "queue_max_wait_time_with_no_agent": "0",
		"agents": []map[string]string{{"call_center_agent_uuid": agent["1001"]}, {"call_center_agent_uuid": agent["1002"], "tier_position": "2"}}})
	if len(q["agents"].([]any)) != 2 {
		t.Fatalf("queue agents: %v", q["agents"])
	}
	did := r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6551234", "gateway_uuid": gw["gateway_uuid"],
		"action": "transfer:3000 XML " + domain})
	if did["gateway_uuid"] != gw["gateway_uuid"] || did["action"] != "transfer:3000 XML "+domain {
		t.Fatalf("DID read back: %v", did)
	}
	meta := r.mustCall("GET", d+"meta", nil)
	found := false
	for _, x := range meta["destinations"].([]any) {
		m := x.(map[string]any)
		found = found || (m["group"] == "Queue" && m["value"] == "transfer:3000 XML "+domain)
	}
	if !found {
		t.Fatalf("queue 3000 is not offered as a destination: %v", meta["destinations"])
	}

	p1, p2 := r.phone("1001"), r.phone("1002")
	for _, ext := range []string{"1001", "1002"} {
		r.mustCall("POST", "agents/"+agent[ext]+"/status", map[string]string{"status": "Available"})
	}

	// 1. A call to the DID through the gateway reaches an agent.
	done := make(chan *testua.Call, 1)
	go func() { c, _ := r.carrier.Dial("6551234", nil, 15*time.Second, false); done <- c }()
	i, in := waitAny(5*time.Second, p1, p2)
	if in == nil {
		t.Fatal("no agent was offered the call")
	}
	if err := in.Answer(); err != nil {
		t.Fatal(err)
	}
	other := []*testua.UA{p1, p2}[1-i]
	if c, err := other.WaitRequest("CANCEL", 3*time.Second); err == nil {
		_ = c
	}
	var call *testua.Call
	select {
	case call = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("carrier call not answered")
	}
	if call == nil || call.Final == nil || call.Final.StatusCode != 200 {
		t.Fatalf("carrier call: %+v", call)
	}
	time.Sleep(300 * time.Millisecond)
	_, _ = call.Bye()

	// 2. Agent 1001 goes to lunch, 1002 logs out: the next caller reaches nobody.
	r.mustCall("POST", "agents/"+agent["1001"]+"/status", map[string]string{"status": "On Break", "aux": "Lunch"})
	r.mustCall("POST", "agents/"+agent["1002"]+"/status", map[string]string{"status": "Logged Out"})
	time.Sleep(200 * time.Millisecond)
	go func() { c, _ := r.carrier.Dial("6551234", nil, 3*time.Second, false); done <- c }()
	if _, in := waitAny(2500*time.Millisecond, p1, p2); in != nil {
		t.Fatalf("an agent on a break / logged out was offered a call: %s", in.UA.User)
	}
	// The queue answered (music on hold); the caller gives up.
	if c := <-done; c != nil && c.Final != nil && c.Final.StatusCode == 200 {
		_, _ = c.Bye()
	}
	live := r.mustCall("GET", "live?domain="+domain, nil)
	for _, x := range live["agents"].([]any) {
		a := x.(map[string]any)
		if a["uuid"] == agent["1001"] && (a["status"] != "On Break" || a["aux"] != "Lunch") {
			t.Fatalf("live agent: %v", a)
		}
	}

	// 3. Reports and logs.
	r.waitCount("status log", 4, `select count(*) from rq_agent_status_log`)
	r.waitCount("RingQ AUX history", 4, `select count(*) from q_auxcodes_list`)
	r.waitCount("answered queue call", 1, `select count(*) from rq_queue_calls where result = 'answered'`)
	r.waitCount("abandoned queue call", 1, `select count(*) from rq_queue_calls where result = 'abandoned'`)
	var rep []map[string]any
	r.call("GET", d+"reports/agent-status", nil, &rep)
	lunch := false
	for _, a := range rep {
		if a["agent_uuid"] == agent["1001"] {
			_, lunch = a["seconds"].(map[string]any)["On Break: Lunch"]
			if a["current"] != "On Break: Lunch" {
				t.Fatalf("agent-status current: %v", a)
			}
		}
	}
	if !lunch {
		t.Fatalf("agent-status report has no lunch time: %v", rep)
	}
	var logRows []map[string]any
	r.call("GET", d+"reports/agent-log?agent="+agent["1001"], nil, &logRows)
	if len(logRows) < 2 || logRows[0]["aux_code"] != "Lunch" || !strings.HasPrefix(logRows[0]["source"].(string), "web:") {
		t.Fatalf("agent log: %v", logRows)
	}
	var qrep []map[string]any
	r.call("GET", d+"reports/queues", nil, &qrep)
	if len(qrep) != 1 || fmt.Sprint(qrep[0]["offered"]) != "2" || fmt.Sprint(qrep[0]["answered"]) != "1" || fmt.Sprint(qrep[0]["abandoned"]) != "1" {
		t.Fatalf("queue report: %v", qrep)
	}
	// Call history: the queue picked both calls up (music on hold), but only
	// the one an agent answered is "answered"; the caller who gave up while
	// waiting missed the call.
	r.waitCount("answered call record", 1, `select count(*) from v_xml_cdr where status = 'answered' and cc_queue_joined_epoch > 0`)
	r.waitCount("missed queue call record", 1, `select count(*) from v_xml_cdr where status = 'missed' and missed_call and cc_queue_canceled_epoch > 0`)
	// Records written before 0.7.3 said "answered": Call history shows them
	// as missed (and finds them with the Missed filter) without changing them.
	if _, err := r.conn.Exec(context.Background(), `update v_xml_cdr set status = 'answered', missed_call = false where status = 'missed' and cc_queue_canceled_epoch > 0`); err != nil {
		t.Fatal(err)
	}
	var hist struct{ Rows []map[string]any }
	r.call("GET", d+"cdr?status=missed", nil, &hist)
	if len(hist.Rows) != 1 || hist.Rows[0]["status"] != "missed" || hist.Rows[0]["cc_queue_canceled_epoch"] == nil {
		t.Fatalf("missed queue call in Call history: %v", hist.Rows)
	}
	var answered struct{ Rows []map[string]any }
	r.call("GET", d+"cdr?status=answered", nil, &answered)
	if len(answered.Rows) != 1 || answered.Rows[0]["answered_by"] == nil {
		t.Fatalf("answered calls in Call history: %v", answered.Rows)
	}
	for _, row := range answered.Rows {
		if row["cc_queue_canceled_epoch"] != nil && row["cc_queue_canceled_epoch"] != "" {
			t.Fatalf("a queue call nobody answered listed as answered: %v", row)
		}
	}
	var events []map[string]any
	r.call("GET", "logs?kind=agent&q=Lunch", nil, &events)
	if len(events) == 0 {
		t.Fatal("event log has no agent break")
	}

	// 4. Deleting the DID removes its routing.
	if r.count(`select count(*) from v_dialplans where dialplan_context = 'public'`) != 1 {
		t.Fatal("DID dialplan missing")
	}
	r.mustCall("DELETE", d+"dids/"+did["destination_uuid"].(string), nil)
	if n := r.count(`select count(*) from v_dialplans where dialplan_context = 'public'`); n != 0 {
		t.Fatalf("DID dialplan left behind: %d", n)
	}
}

// TestOfficeHoursAndRoutes checks the generated dialplans of an office
// hours rule and an outbound route load and route calls.
// TestQueueCallerHangsUpWhileRinging: the caller gives up while the
// agent's phone rings. The queue picked the call up (music on hold), but
// nobody answered: the record is "missed", and it carries the queue's
// result (written once the queue has cancelled the ringing).
func TestQueueCallerHangsUpWhileRinging(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	a := r.mustCall("POST", d+"agents", map[string]any{"agent_name": "Agent 1001", "extension": "1001", "agent_call_timeout": "20"})
	agentID := a["call_center_agent_uuid"].(string)
	r.mustCall("POST", d+"queues", map[string]any{"queue_name": "TestQ", "queue_extension": "8000", "queue_strategy": "ring-all",
		"queue_moh_sound": "silence", "queue_max_wait_time_with_no_agent": "0", "agents": []map[string]string{{"call_center_agent_uuid": agentID}}})
	r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6569142668", "gateway_uuid": gw["gateway_uuid"], "action": "transfer:8000 XML " + domain})
	p1 := r.phone("1001")
	r.mustCall("POST", "agents/"+agentID+"/status", map[string]string{"status": "Available"})

	done := make(chan *testua.Call, 1)
	go func() { c, _ := r.carrier.Dial("6569142668", nil, 10*time.Second, false); done <- c }()
	in, err := p1.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Ring(); err != nil { // ringing, not answered
		t.Fatal(err)
	}
	call := <-done
	if call == nil || call.Final == nil || call.Final.StatusCode != 200 {
		t.Fatalf("the queue did not pick up: %+v", call)
	}
	time.Sleep(time.Second)
	_, _ = call.Bye() // gives up while the agent's phone rings
	if _, err := p1.WaitRequest("CANCEL", 3*time.Second); err != nil {
		t.Fatal("the agent's ringing was not cancelled:", err)
	}
	r.waitCount("missed queue call record", 1, `select count(*) from v_xml_cdr where status = 'missed' and missed_call
		and cc_queue_joined_epoch > 0 and cc_queue_canceled_epoch > 0 and cc_cancel_reason = 'NONE'`)
	var hist struct{ Rows []map[string]any }
	r.call("GET", d+"cdr", nil, &hist)
	if len(hist.Rows) != 1 || hist.Rows[0]["status"] != "missed" {
		t.Fatalf("Call history: %v", hist.Rows)
	}
	// Such a record written by 0.7.3 or earlier: "answered", without the
	// queue's result. Call history shows it as missed.
	if _, err := r.conn.Exec(context.Background(), `update v_xml_cdr set status = 'answered', missed_call = false,
		cc_queue_canceled_epoch = null, cc_cancel_reason = null`); err != nil {
		t.Fatal(err)
	}
	var old struct{ Rows []map[string]any }
	r.call("GET", d+"cdr?status=missed", nil, &old)
	if len(old.Rows) != 1 || old.Rows[0]["status"] != "missed" {
		t.Fatalf("an earlier record in Call history: %v", old.Rows)
	}
}

func TestOfficeHoursAndRoutes(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	all := "mon,tue,wed,thu,fri,sat,sun"
	r.mustCall("POST", d+"timeconditions", map[string]any{"condition_name": "Always open", "condition_extension": "6000", "weekdays": all,
		"time_from": "00:00", "time_to": "23:59", "match_action": "transfer:1001 XML " + domain, "nomatch_action": "hangup:USER_BUSY"})
	r.mustCall("POST", d+"routes", map[string]any{"route_name": "Out", "route_pattern": `^0(\d{6,15})$`, "route_strip": "1", "route_prepend": "+65",
		"gateway_uuids": gw["gateway_uuid"]})
	caller, p1 := r.phone("1000"), r.phone("1001")

	done := make(chan *testua.Call, 1)
	go func() { c, _ := caller.Dial("6000", nil, 5*time.Second, false); done <- c }()
	in, err := p1.WaitInvite(3 * time.Second)
	if err != nil {
		t.Fatalf("office hours did not ring 1001: %v", err)
	}
	_ = in.Reject(486)
	<-done

	go func() { c, _ := caller.Dial("0655512345", nil, 5*time.Second, false); done <- c }()
	out, err := r.carrier.WaitInvite(3 * time.Second)
	if err != nil {
		t.Fatalf("outbound route did not use the gateway: %v", err)
	}
	if !strings.Contains(out.Invite.RequestURI.String(), "+65655512345") {
		t.Fatalf("outbound number: %s", out.Invite.RequestURI.String())
	}
	_ = out.Reject(486)
	<-done
}
