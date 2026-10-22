package db_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
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
)

// These tests need PostgreSQL: RINGQ_TEST_DSN=postgres://user:pass@host/db
// Each run works in a fresh schema that is dropped afterwards.

const (
	domainUUID  = "a0000000-0000-4000-8000-000000000001"
	gatewayUUID = "b0000000-0000-4000-8000-000000000001"
	rgUUID      = "c0000000-0000-4000-8000-000000000001"
	ivrUUID     = "d0000000-0000-4000-8000-000000000001"
	password    = "Secret-1000"
)

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// testSchema creates a schema with the RingQ tables and returns a DSN
// that uses it.
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
	schema := "rqtest_" + strings.ReplaceAll(uuid()[:13], "-", "")
	ddl := []byte(db.RingQSchema())
	if _, err := conn.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err == nil {
			_, _ = c.Exec(context.Background(), "drop schema "+schema+" cascade")
			c.Close(context.Background())
		}
	})
	if like := os.Getenv("RINGQ_TEST_LIKE_SCHEMA"); like != "" {
		// Copy the real table definitions (types, defaults, constraints)
		// from an existing RingQ schema instead.
		for _, tbl := range likeTables {
			if _, err := conn.Exec(ctx, fmt.Sprintf("create table %s.%s (like %s.%s including all)", schema, tbl, like, tbl)); err != nil {
				t.Fatalf("copy %s.%s: %v", like, tbl, err)
			}
		}
	} else if _, err := conn.Exec(ctx, "set search_path to "+schema+";\n"+string(ddl)); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

var likeTables = []string{"v_domains", "v_extensions", "v_gateways", "v_dialplans", "v_vars", "v_ring_groups",
	"v_ring_group_destinations", "v_ivr_menus", "v_ivr_menu_options", "v_access_controls", "v_access_control_nodes",
	"registrations", "channels", "calls", "v_xml_cdr", "q_ringq_cdr"}

func cdrTables() string {
	if os.Getenv("RINGQ_TEST_LIKE_SCHEMA") != "" {
		return "v_xml_cdr,q_ringq_cdr"
	}
	return "v_xml_cdr"
}

// RingQ default dialplans (abridged), stored as in v_dialplans.
const (
	dpCallDirection = `<extension name="call-direction" continue="true" uuid="x">
	<condition field="${call_direction}" expression="^(inbound|outbound|local)$" break="never">
		<anti-action application="export" data="call_direction=local" inline="true"/>
	</condition>
</extension>`
	dpUserExists = `<extension name="user_exists" continue="true" uuid="x">
	<condition field="" expression="">
		<action application="set" data="user_exists=${user_exists id ${destination_number} ${domain_name}}" inline="true"/>
	</condition>
	<condition field="${user_exists}" expression="^true$">
		<action application="set" data="extension_uuid=${user_data ${destination_number}@${domain_name} var extension_uuid}" inline="true"/>
		<action application="set" data="hold_music=${user_data ${destination_number}@${domain_name} var hold_music}" inline="true"/>
	</condition>
</extension>`
	dpVariables = `<extension name="variables" continue="true" uuid="x">
	<condition field="" expression="">
		<action application="set" data="ringback=$${us-ring}"/>
		<action application="set" data="transfer_ringback=$${hold_music}"/>
		<action application="set" data="call_timeout=30"/>
		<action application="set" data="default_language=en" inline="true"/>
		<action application="set" data="record_stereo=true"/>
		<action application="set" data="RFC2822_DATE=${strftime(%a, %d %b %Y %T %z)}"/>
	</condition>
</extension>`
	dpLocalExtension = `<extension name="local_extension" continue="true" uuid="x">
	<condition field="${user_exists}" expression="true">
		<action application="export" data="dialed_extension=${destination_number}" inline="true"/>
		<action application="limit" data="hash ${domain_name} ${destination_number} ${limit_max} ${limit_destination}" inline="false"/>
	</condition>
	<condition field="" expression="">
		<action application="set" data="hangup_after_bridge=true"/>
		<action application="set" data="continue_on_fail=true"/>
		<action application="hash" data="insert/${domain_name}-call_return/${dialed_extension}/${caller_id_number}"/>
		<action application="hash" data="insert/${domain_name}-last_dial_ext/${dialed_extension}/${uuid}"/>
		<action application="set" data="called_party_call_group=${user_data ${dialed_extension}@${domain_name} var call_group}"/>
		<action application="bridge" data="user/${destination_number}@${domain_name}"/>
		<action application="lua" data="app.lua failure_handler"/>
	</condition>
</extension>`
	dpOutbound = `<extension name="outbound.9" continue="false" uuid="x">
	<condition field="${user_exists}" expression="false"/>
	<condition field="destination_number" expression="^9(\d{6,15})$">
		<action application="set" data="sip_h_X-accountcode=${accountcode}"/>
		<action application="export" data="call_direction=outbound" inline="true"/>
		<action application="unset" data="call_timeout"/>
		<action application="set" data="hangup_after_bridge=true"/>
		<action application="set" data="effective_caller_id_name=${outbound_caller_id_name}"/>
		<action application="set" data="effective_caller_id_number=${outbound_caller_id_number}"/>
		<action application="set" data="inherit_codec=true"/>
		<action application="set" data="ignore_display_updates=true"/>
		<action application="set" data="callee_id_number=$1"/>
		<action application="set" data="continue_on_fail=1,2,3,6,25,34,38,41,42,44,58,63,88,111,403,501,602,607"/>
		<action application="bridge" data="sofia/gateway/` + gatewayUUID + `/$1"/>
	</condition>
</extension>`
	dpRingGroup = `<extension name="Sales" continue="" uuid="x">
	<condition field="destination_number" expression="^2000$">
		<action application="ring_ready" data=""/>
		<action application="set" data="ring_group_uuid=` + rgUUID + `"/>
		<action application="lua" data="app.lua ring_groups"/>
	</condition>
</extension>`
	dpIVR = `<extension name="Main IVR" continue="false" uuid="x">
	<condition field="destination_number" expression="^5000$">
		<action application="ring_ready" data=""/>
		<action application="answer" data=""/>
		<action application="sleep" data="200"/>
		<action application="set" data="hangup_after_bridge=true"/>
		<action application="set" data="ringback=${us-ring}"/>
		<action application="set" data="presence_id=5000@${domain_name}"/>
		<action application="set" data="ivr_menu_uuid=` + ivrUUID + `"/>
		<action application="ivr" data="` + ivrUUID + `"/>
		<action application="hangup" data=""/>
	</condition>
</extension>`
	dpPublicDID = `<extension name="5551000" continue="false" uuid="x">
	<condition field="destination_number" expression="^\+?(5551000)$">
		<action application="export" data="call_direction=inbound" inline="true"/>
		<action application="set" data="domain_uuid=` + domainUUID + `" inline="true"/>
		<action application="set" data="domain_name=test.example" inline="true"/>
		<action application="transfer" data="2000 XML test.example"/>
	</condition>
</extension>`
)

func seed(t *testing.T, dsn string, carrier netip.AddrPort) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`insert into v_domains (domain_uuid, domain_name, domain_enabled) values ($1, 'test.example', true)`, domainUUID)
	for _, e := range []string{"1000", "1001", "1002"} {
		exec(`insert into v_extensions (extension_uuid, domain_uuid, extension, password, effective_caller_id_name,
			effective_caller_id_number, outbound_caller_id_name, outbound_caller_id_number, user_context, call_timeout,
			call_group, accountcode, enabled, max_registrations, limit_max, limit_destination)
			values ($1, $2, $3, $4, 'Ext '||$3, $3, 'Test Co', '6590001111', 'test.example', 30, 'sales', 'acct1', 'true', '3', '5', 'error/user_busy')`,
			uuid(), domainUUID, e, password)
	}
	exec(`insert into v_extensions (extension_uuid, domain_uuid, extension, password, enabled) values ($1, $2, '1009', 'x', 'false')`, uuid(), domainUUID)
	exec(`insert into v_gateways (gateway_uuid, domain_uuid, gateway, username, password, proxy, register, expire_seconds,
		retry_seconds, context, profile, enabled, caller_id_in_from) values ($1, $2, 'Carrier One', 'acct', 'pw', $3, 'false', 800, 30, 'public', 'external', 'true', 'false')`,
		gatewayUUID, domainUUID, carrier.String())
	exec(`insert into v_vars (var_uuid, var_category, var_name, var_value, var_enabled, var_order) values
		($1, 'Tones', 'us-ring', '%(2000,4000,440,480)', 'true', 1),
		($2, 'Music', 'hold_music', 'local_stream://default', 'true', 2),
		($3, 'Tones', 'ringback', '$${us-ring}', 'true', 3)`, uuid(), uuid(), uuid())
	type dp struct {
		domain  any
		context string
		name    string
		order   int
		xml     string
	}
	for _, d := range []dp{
		{nil, "${domain_name}", "call-direction", 15, dpCallDirection},
		{nil, "${domain_name}", "user_exists", 20, dpUserExists},
		{nil, "${domain_name}", "variables", 30, dpVariables},
		{domainUUID, "test.example", "outbound.9", 100, dpOutbound},
		{domainUUID, "test.example", "Sales", 101, dpRingGroup},
		{domainUUID, "test.example", "Main IVR", 102, dpIVR},
		{nil, "${domain_name}", "local_extension", 999, dpLocalExtension},
		{domainUUID, "public", "5551000", 100, dpPublicDID},
		{domainUUID, "test.example", "broken", 500, `<extension name="broken"><condition field="x" expression="(">`},
	} {
		exec(`insert into v_dialplans (dialplan_uuid, domain_uuid, dialplan_context, dialplan_name, dialplan_order,
			dialplan_enabled, dialplan_xml) values ($1, $2, $3, $4, $5, 'true', $6)`, uuid(), d.domain, d.context, d.name, d.order, d.xml)
	}
	exec(`insert into v_ring_groups (ring_group_uuid, domain_uuid, ring_group_name, ring_group_extension, ring_group_context,
		ring_group_strategy, ring_group_call_timeout, ring_group_enabled, ring_group_cid_name_prefix) values ($1, $2, 'Sales', '2000', 'test.example', 'simultaneous', 20, 'true', 'Sales')`,
		rgUUID, domainUUID)
	for _, n := range []string{"1001", "1002"} {
		exec(`insert into v_ring_group_destinations (ring_group_destination_uuid, domain_uuid, ring_group_uuid, destination_number,
			destination_delay, destination_timeout, destination_enabled) values ($1, $2, $3, $4, 0, 20, true)`, uuid(), domainUUID, rgUUID, n)
	}
	exec(`insert into v_ivr_menus (ivr_menu_uuid, domain_uuid, ivr_menu_name, ivr_menu_extension, ivr_menu_timeout,
		ivr_menu_inter_digit_timeout, ivr_menu_max_failures, ivr_menu_max_timeouts, ivr_menu_digit_len, ivr_menu_direct_dial,
		ivr_menu_enabled, ivr_menu_greet_long, ivr_menu_invalid_sound, ivr_menu_exit_sound)
		values ($1, $2, 'Main', '5000', 3000, 1000, 3, 3, 4, 'true', 'true', 'ivr/ivr-welcome.wav', 'ivr/ivr-that_was_an_invalid_entry.wav', 'voicemail/vm-goodbye.wav')`, ivrUUID, domainUUID)
	exec(`insert into v_ivr_menu_options (ivr_menu_option_uuid, ivr_menu_uuid, domain_uuid, ivr_menu_option_digits,
		ivr_menu_option_action, ivr_menu_option_param, ivr_menu_option_order, ivr_menu_option_enabled)
		values ($1, $2, $3, '1', 'menu-exec-app', 'transfer 2000 XML test.example', 10, true)`, uuid(), ivrUUID, domainUUID)
}

type rig struct {
	t       *testing.T
	sw      *core.Core
	store   *db.Store
	dsn     string
	addr    netip.AddrPort
	conn    *pgx.Conn
	carrier *testua.UA // listens at the gateway's proxy address (127.0.0.2)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dsn := testSchema(t)
	carrier, err := testua.NewAt("carrier", "", netip.AddrPort{}, netip.MustParseAddr("127.0.0.2"))
	if err != nil {
		t.Skip("127.0.0.2 not usable:", err)
	}
	t.Cleanup(carrier.Close)
	seed(t, dsn, carrier.Local)

	dir := t.TempDir()
	main := fmt.Sprintf(`<configuration>
  <settings>
    <param name="sip-ip" value="127.0.0.1"/>
    <param name="sip-port" value="0"/>
    <param name="enable-tcp" value="false"/>
    <param name="rtp-ip" value="127.0.0.1"/>
    <param name="rtp-start-port" value="47000"/>
    <param name="rtp-end-port" value="47998"/>
    <param name="event-socket-listen" value=""/>
    <param name="cdr-csv" value=""/>
    <param name="sounds-dir" value="%s"/>
  </settings>
  <database>
    <param name="dsn" value="%s"/>
    <param name="hostname" value="rqtest-host"/>
    <param name="poll-seconds" value="1"/>
    <param name="cdr-tables" value="%s"/>
  </database>
</configuration>`, dir, strings.ReplaceAll(dsn, "&", "&amp;"), cdrTables())
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
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := db.Open(ctx, cfg.Database, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
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
	go store.Watch(ctx, cfg.Database.PollInterval, sw.Reload)
	t.Cleanup(func() { sw.Stop(); w.Close() })
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	r := &rig{t: t, sw: sw, store: store, dsn: dsn, conn: conn, carrier: carrier,
		addr: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(sw.SIPPort()))}
	carrier.Server = r.addr
	return r
}

func (r *rig) phone(ext string) *testua.UA {
	r.t.Helper()
	ua, err := testua.New(ext, password, r.addr)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(ua.Close)
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
		if n = r.count(sql, args...); n == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("%s: have %d rows, want %d", what, n, want)
}

func (r *rig) waitChannels(n int) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(r.sw.Channels()) == n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("expected %d channels, have %d", n, len(r.sw.Channels()))
}

func dial(ua *testua.UA, dest string) chan *testua.Call {
	done := make(chan *testua.Call, 1)
	go func() {
		c, _ := ua.Dial(dest, nil, 15*time.Second, false)
		done <- c
	}()
	return done
}

func answered(t *testing.T, done chan *testua.Call) *testua.Call {
	t.Helper()
	select {
	case c := <-done:
		if c == nil || c.Final == nil || c.Final.StatusCode != 200 {
			t.Fatalf("call not answered: %+v", c)
		}
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
	}
	return nil
}

func TestOverlayLoadsRingQ(t *testing.T) {
	r := newRig(t)
	out := r.sw.API("domains")
	if !strings.Contains(out, "test.example (default)") {
		t.Fatalf("single database domain should become the default:\n%s", out)
	}
	if got := r.sw.API("user_exists id 1001 test.example"); got != "true" {
		t.Fatalf("user_exists: %q", got)
	}
	if got := r.sw.API("user_data 1001@test.example var call_group"); got != "sales" {
		t.Fatalf("user_data: %q", got)
	}
	if out := r.sw.API("show gateways"); !strings.Contains(out, gatewayUUID) || !strings.Contains(out, "Carrier One") {
		t.Fatalf("gateway not loaded:\n%s", out)
	}
	if got := r.sw.API("global_getvar ringback"); got != "%(2000,4000,440,480)" {
		t.Fatalf("v_vars reference not resolved: %q", got)
	}
	if rep := r.sw.CompatReport(); !strings.Contains(rep, "+OK") {
		t.Fatalf("the RingQ defaults should be fully supported:\n%s", rep)
	}
	// A disabled extension cannot register.
	ua, _ := testua.New("1009", "x", r.addr)
	defer ua.Close()
	if resp, _ := ua.Register(300); resp == nil || resp.StatusCode != 403 {
		t.Fatalf("disabled extension registered: %v", resp)
	}
}

func TestExtensionCallWritesStateAndCDR(t *testing.T) {
	r := newRig(t)
	a, b := r.phone("1000"), r.phone("1001")
	r.waitCount("registrations", 2, `select count(*) from registrations where realm = 'test.example' and hostname = 'rqtest-host'`)

	done := dial(a, "1001")
	in, err := b.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.Invite.Get("From"), "Ext 1000") {
		t.Fatalf("effective caller id from v_extensions: %s", in.Invite.Get("From"))
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	call := answered(t, done)
	r.waitCount("channels", 2, `select count(*) from channels where hostname = 'rqtest-host'`)
	r.waitCount("calls", 1, `select count(*) from calls where hostname = 'rqtest-host'`)
	time.Sleep(1100 * time.Millisecond)
	if _, err := call.Bye(); err != nil {
		t.Fatal(err)
	}
	r.waitChannels(0)
	r.waitCount("channels cleared", 0, `select count(*) from channels`)
	r.waitCount("cdr", 1, `select count(*) from v_xml_cdr`)
	var dir, dest, cause, leg, status, domain string
	var billsec int
	var missed bool
	err = r.conn.QueryRow(context.Background(), `select direction, destination_number, hangup_cause, leg, status,
		domain_uuid::text, billsec, missed_call from v_xml_cdr`).Scan(&dir, &dest, &cause, &leg, &status, &domain, &billsec, &missed)
	if err != nil {
		t.Fatal(err)
	}
	if dir != "local" || dest != "1001" || cause != "NORMAL_CLEARING" || leg != "a" || status != "answered" ||
		domain != domainUUID || billsec < 1 || missed {
		t.Fatalf("cdr: direction=%s dest=%s cause=%s leg=%s status=%s domain=%s billsec=%d missed=%v",
			dir, dest, cause, leg, status, domain, billsec, missed)
	}
	if n := r.count(`select count(*) from v_xml_cdr where json->'variables'->>'dialed_extension' = '1001'`); n != 1 {
		t.Fatal("cdr json should hold the channel variables")
	}
	if os.Getenv("RINGQ_TEST_LIKE_SCHEMA") != "" {
		r.waitCount("q_ringq_cdr", 1, `select count(*) from q_ringq_cdr where destination_number = '1001' and leg = 'a'`)
	}
}

func TestOutboundThroughGatewayAndInboundDID(t *testing.T) {
	r := newRig(t)
	a, b, c := r.phone("1000"), r.phone("1001"), r.phone("1002")
	carrier := r.carrier

	// Outbound: 9 + number through sofia/gateway/<uuid>.
	done := dial(a, "96591234567")
	in, err := carrier.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("carrier did not get the call:", err)
	}
	if in.Invite.RequestURI.User != "6591234567" {
		t.Fatalf("request uri %s", in.Invite.RequestURI)
	}
	if pai := in.Invite.Get("P-Asserted-Identity"); !strings.Contains(pai, "6590001111") {
		t.Fatalf("outbound caller id should be presented: PAI=%q From=%q", pai, in.Invite.Get("From"))
	}
	if in.Invite.Get("X-accountcode") != "acct1" {
		t.Fatalf("sip_h_ header from the route: %q", in.Invite.Get("X-accountcode"))
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	call := answered(t, done)
	_, _ = call.Bye()
	_, _ = carrier.WaitRequest("BYE", 5*time.Second)
	r.waitChannels(0)
	r.waitCount("outbound cdr", 1, `select count(*) from v_xml_cdr where direction = 'outbound'`)

	// Inbound DID from the carrier (recognised by its address) goes
	// through the public context to the ring group (1001 + 1002).
	done = dial(carrier, "5551000")
	in1, err := b.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("DID did not reach the ring group (1001):", err)
	}
	in2, err := c.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("DID did not reach the ring group (1002):", err)
	}
	if !strings.Contains(in1.Invite.Get("From"), "Sales#") {
		t.Fatalf("ring group caller id prefix: %s", in1.Invite.Get("From"))
	}
	_ = in1.Ring()
	_ = in2.Ring()
	if err := in2.Answer(0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WaitRequest("CANCEL", 5*time.Second); err != nil {
		t.Fatal("losing ring group member not cancelled:", err)
	}
	cl := answered(t, done)
	_, _ = cl.Bye()
	r.waitChannels(0)
	r.waitCount("inbound cdr", 1, `select count(*) from v_xml_cdr where direction = 'inbound' and ring_group_uuid = $1`, rgUUID)
}

func TestLiveReloadOnDatabaseChange(t *testing.T) {
	r := newRig(t)
	ua, _ := testua.New("1003", password, r.addr)
	defer ua.Close()
	if resp, _ := ua.Register(300); resp == nil || resp.StatusCode != 403 {
		t.Fatalf("1003 should not exist yet: %v", resp)
	}
	if _, err := r.conn.Exec(context.Background(), `insert into v_extensions (extension_uuid, domain_uuid, extension, password, enabled)
		values ($1, $2, '1003', $3, 'true')`, uuid(), domainUUID, password); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if r.sw.API("user_exists id 1003 test.example") == "true" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	ua.Password = password
	if resp, _ := ua.Register(300); resp == nil || resp.StatusCode != 200 {
		t.Fatalf("new extension not picked up: %v", resp)
	}
	// memcache delete (the RingQ portal's cache invalidation) reloads too.
	if _, err := r.conn.Exec(context.Background(), `update v_extensions set enabled = 'false' where extension = '1003'`); err != nil {
		t.Fatal(err)
	}
	if out := r.sw.API("memcache delete directory:1003@test.example"); !strings.HasPrefix(out, "+OK") {
		t.Fatal(out)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if resp, _ := ua.Register(300); resp != nil && resp.StatusCode == 403 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("disabling the extension was not applied")
}

func TestCreateMissingTables(t *testing.T) {
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
	schema := "rqempty_" + strings.ReplaceAll(uuid()[:13], "-", "")
	if _, err := conn.Exec(ctx, "create schema "+schema+"; create table "+schema+".v_domains (domain_uuid uuid primary key, domain_name text, domain_enabled text)"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "drop schema "+schema+" cascade") //nolint:errcheck
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store, err := db.Open(ctx, config.Database{DSN: u.String()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// RingQswitch's own tables (and the fax extensions) are made at start
	// (manage-schema, on by default).
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"v_fax", "fax_jobs", "fax_domain_settings", "rq_hotel_stays"} {
		if !store.HasTable(tbl) {
			t.Fatalf("%s was not created on start", tbl)
		}
	}
	created, err := store.CreateMissingTables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Count(strings.ToLower(db.RingQSchema()), "create table ") - 1 // v_domains existed
	if len(created) != want || !store.HasTable("v_call_center_queues") || !store.HasTable("q_auxcodes_list") {
		t.Fatalf("created %d tables (want %d): %v", len(created), want, created)
	}
	for _, c := range created {
		if c == "v_domains" {
			t.Fatal("an existing table was created again")
		}
	}
	if again, err := store.CreateMissingTables(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second run: %v %v", again, err)
	}
}
