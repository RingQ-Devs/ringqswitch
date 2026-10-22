package core_test

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// A RingQ-style dialplan for one domain (context = domain name).
func portalContext(domain string) string {
	return fmt.Sprintf(`<context name="%s">
  <extension name="call-direction" continue="true">
    <condition field="${call_direction}" expression="^(inbound|outbound|local)$" break="never">
      <anti-action application="export" data="call_direction=local" inline="true"/>
    </condition>
  </extension>
  <extension name="user_exists" continue="true">
    <condition field="" expression="">
      <action application="set" data="user_exists=${user_exists id ${destination_number} ${domain_name}}" inline="true"/>
    </condition>
    <condition field="${user_exists}" expression="^true$">
      <action application="set" data="extension_uuid=${user_data ${destination_number}@${domain_name} var extension_uuid}" inline="true"/>
    </condition>
  </extension>
  <extension name="ring_group">
    <condition field="destination_number" expression="^2000$">
      <action application="set" data="ring_group_uuid=rg-%[1]s"/>
      <action application="lua" data="app.lua ring_groups"/>
    </condition>
  </extension>
  <extension name="ivr">
    <condition field="destination_number" expression="^5000$">
      <action application="answer"/>
      <action application="ivr" data="ivr-%[1]s"/>
      <action application="hangup"/>
    </condition>
  </extension>
  <extension name="recorded">
    <condition field="destination_number" expression="^7(10\d\d)$">
      <action application="set" data="record_stereo=true"/>
      <action application="record_session" data="${recordings_dir}/${domain_name}/${uuid}.wav"/>
      <action application="bridge" data="user/$1@${domain_name}"/>
    </condition>
  </extension>
  <extension name="queue">
    <condition field="destination_number" expression="^(30\d\d)$">
      <action application="set" data="hangup_after_bridge=true"/>
      <action application="callcenter" data="$1@${domain_name}"/>
      <action application="transfer" data="1003 XML ${domain_name}"/>
    </condition>
  </extension>
  <extension name="agent-toggle">
    <condition field="destination_number" expression="^\*22$">
      <action application="rq_agent_status" data="toggle"/>
    </condition>
  </extension>
  <extension name="park">
    <condition field="destination_number" expression="^(\*59\d\d)$">
      <action application="valet_park" data="${domain_name} $1"/>
    </condition>
  </extension>
  <extension name="pickup">
    <condition field="destination_number" expression="^\*\*(\d+)$">
      <action application="rq_pickup" data="$1"/>
    </condition>
  </extension>
  <extension name="conference">
    <condition field="destination_number" expression="^(40\d\d)$">
      <action application="conference" data="room$1@default"/>
    </condition>
  </extension>
  <extension name="local_extension" continue="true">
    <condition field="${user_exists}" expression="true">
      <action application="export" data="dialed_extension=${destination_number}" inline="true"/>
      <action application="limit" data="hash ${domain_name} ${destination_number} ${limit_max} ${limit_destination}" inline="false"/>
    </condition>
    <condition field="" expression="">
      <action application="set" data="hangup_after_bridge=true"/>
      <action application="set" data="continue_on_fail=true"/>
      <action application="hash" data="insert/${domain_name}-call_return/${dialed_extension}/${caller_id_number}"/>
      <action application="bridge" data="user/${destination_number}@${domain_name}"/>
      <action application="lua" data="app.lua failure_handler"/>
    </condition>
  </extension>
</context>`, domain)
}

func tenantUsers(extra map[string]string) string {
	var b strings.Builder
	b.WriteString("<include>")
	for _, d := range []string{"a.example", "b.example"} {
		fmt.Fprintf(&b, `<domain name="%s"><users>`, d)
		for _, u := range []string{"1000", "1001", "1002", "1003"} {
			pw := testPassword
			if d == "b.example" {
				pw = "b-" + testPassword
			}
			fmt.Fprintf(&b, `<user id="%s"><params><param name="password" value="%s"/></params><variables>
				<variable name="user_context" value="%s"/><variable name="extension_uuid" value="ext-%s-%s"/>
				<variable name="effective_caller_id_number" value="%s"/>%s</variables></user>`,
				u, pw, d, d, u, u, extra[u+"@"+d])
		}
		b.WriteString("</users></domain>")
	}
	b.WriteString("</include>")
	return b.String()
}

func tenantHarness(t *testing.T, extra map[string]string, mutate func(*config.Config)) *harness {
	dp := "<include>" + portalContext("a.example") + portalContext("b.example") + "</include>"
	return newHarnessWith(t, "", tenantUsers(extra), dp, mutate)
}

func (h *harness) tenantPhone(user, domain string) *testua.UA {
	h.t.Helper()
	pw := testPassword
	if domain == "b.example" {
		pw = "b-" + testPassword
	}
	ua, err := testua.New(user, pw, h.addr)
	if err != nil {
		h.t.Fatal(err)
	}
	ua.Domain = domain
	h.t.Cleanup(ua.Close)
	resp, err := ua.Register(300)
	if err != nil || resp.StatusCode != 200 {
		h.t.Fatalf("register %s@%s failed: %v %v", user, domain, resp, err)
	}
	return ua
}

func answerCall(t *testing.T, done chan *testua.Call, in *testua.Incoming) *testua.Call {
	t.Helper()
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-done:
		if c == nil || c.Final == nil || c.Final.StatusCode != 200 {
			t.Fatalf("call not answered: %+v", c)
		}
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("caller never got the answer")
	}
	return nil
}

func dial(ua *testua.UA, dest string) chan *testua.Call {
	done := make(chan *testua.Call, 1)
	go func() {
		c, _ := ua.Dial(dest, nil, 15*time.Second, false)
		done <- c
	}()
	return done
}

func TestMultiTenantRouting(t *testing.T) {
	h := tenantHarness(t, nil, nil)
	a1000, a1001 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example")
	b1001 := h.tenantPhone("1001", "b.example")

	// Same extension number, different tenants, different passwords.
	wrong, _ := testua.New("1000", testPassword, h.addr)
	defer wrong.Close()
	wrong.Domain = "b.example"
	if resp, _ := wrong.Register(300); resp == nil || resp.StatusCode != 403 {
		t.Fatalf("a.example password must not work for b.example: %v", resp)
	}
	out := h.sw.API("show registrations")
	if !strings.Contains(out, "1001@a.example") || !strings.Contains(out, "1001@b.example") {
		t.Fatalf("registrations should be per domain:\n%s", out)
	}

	done := dial(a1000, "1001")
	in, err := a1001.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b1001.WaitInvite(300 * time.Millisecond); err == nil {
		t.Fatal("call leaked into another tenant")
	}
	call := answerCall(t, done, in)
	h.waitChannels(2)
	var aleg string
	for _, ch := range h.sw.Channels() {
		if ch.Direction == "inbound" {
			aleg = ch.UUID
			v := ch.Vars()
			if v["domain_name"] != "a.example" || v["call_direction"] != "local" || v["extension_uuid"] != "ext-a.example-1001" {
				t.Fatalf("A-leg variables: domain=%q direction=%q extension_uuid=%q", v["domain_name"], v["call_direction"], v["extension_uuid"])
			}
		} else if v := ch.Vars(); v["call_direction"] != "local" || v["dialed_extension"] != "1001" {
			t.Fatalf("exported variables missing on B leg: %q %q", v["call_direction"], v["dialed_extension"])
		}
	}
	if got := h.sw.API("hash select/a.example-call_return/1001"); got != "1000" {
		t.Fatalf("hash insert from dialplan: %q", got)
	}
	if aleg == "" {
		t.Fatal("no A leg")
	}
	_, _ = call.Bye()
	h.waitChannels(0)
}

func TestFailureHandlerForwardsOnBusy(t *testing.T) {
	h := tenantHarness(t, map[string]string{
		"1001@a.example": `<variable name="forward_busy_enabled" value="true"/><variable name="forward_busy_destination" value="1002"/>`,
	}, nil)
	a1000, a1001, a1002 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")
	done := dial(a1000, "1001")
	in, err := a1001.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in.Reject(486)
	in2, err := a1002.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("busy forwarding did not ring 1002:", err)
	}
	call := answerCall(t, done, in2)
	_, _ = call.Bye()
	h.waitChannels(0)
}

func TestForwardAllUsesLoopback(t *testing.T) {
	h := tenantHarness(t, map[string]string{
		"1001@a.example": `<variable name="forward_all_enabled" value="true"/><variable name="forward_all_destination" value="1002"/>`,
	}, nil)
	a1000, a1001, a1002 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")
	done := dial(a1000, "1001")
	in, err := a1002.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("forward all did not ring 1002:", err)
	}
	if _, err := a1001.WaitInvite(200 * time.Millisecond); err == nil {
		t.Fatal("forwarded extension should not ring")
	}
	call := answerCall(t, done, in)
	_, _ = call.Bye()
	h.waitChannels(0)
}

func TestDoNotDisturb(t *testing.T) {
	h := tenantHarness(t, map[string]string{
		"1001@a.example": `<variable name="do_not_disturb" value="true"/>`,
	}, nil)
	a1000 := h.tenantPhone("1000", "a.example")
	h.tenantPhone("1001", "a.example")
	c, err := a1000.Dial("1001", nil, 5*time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Final.StatusCode != 486 {
		t.Fatalf("DND should give busy, got %d", c.Final.StatusCode)
	}
}

func features(cfg *config.Config) {
	for _, d := range []string{"a.example", "b.example"} {
		cfg.Features.RingGroups["rg-"+d] = &config.RingGroup{
			UUID: "rg-" + d, Domain: d, Name: "Sales", Extension: "2000", Strategy: "sequence", Enabled: true,
			Destinations: []config.RingGroupDestination{{Number: "1001", Timeout: 10}, {Number: "1002", Timeout: 10}},
		}
		cfg.Features.IVRMenus["ivr-"+d] = &config.IVRMenu{
			UUID: "ivr-" + d, Domain: d, Name: "Main", Extension: "5000", Enabled: true,
			Timeout: 3 * time.Second, InterDigitTimeout: time.Second, MaxFailures: 2, MaxTimeouts: 2, DigitLen: 4,
			Options: []config.IVROption{
				{Digits: "1", Action: "menu-exec-app", Param: "transfer 2000 XML " + d},
				{Digits: "2", Action: "menu-exec-app", Param: "hangup CALL_REJECTED"},
			},
		}
	}
}

func TestIVRToSequentialRingGroup(t *testing.T) {
	h := tenantHarness(t, nil, features)
	a1000, a1001, a1002 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")
	call, err := a1000.Dial("5000", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatalf("IVR should answer: %v %v", call, err)
	}
	if resp, err := call.Request("INFO", "application/dtmf-relay", []byte("Signal=1\r\nDuration=160\r\n")); err != nil || resp.StatusCode != 200 {
		t.Fatalf("INFO: %v %v", resp, err)
	}
	in, err := a1001.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("ring group did not ring 1001:", err)
	}
	_ = in.Reject(486) // busy: sequence moves on
	in2, err := a1002.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("ring group did not move on to 1002:", err)
	}
	if err := in2.Answer(0); err != nil {
		t.Fatal(err)
	}
	h.waitFor("ring group bridged", func() bool {
		return strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2
	})
	for _, ch := range h.sw.Channels() {
		if ch.Direction == "inbound" && ch.Var("ring_group_uuid") != "rg-a.example" {
			t.Fatalf("ring_group_uuid not set: %q", ch.Var("ring_group_uuid"))
		}
	}
	_, _ = call.Bye()
	h.waitChannels(0)
}

func TestRecordSessionStereo(t *testing.T) {
	recDir := ""
	h := tenantHarness(t, nil, func(cfg *config.Config) {
		recDir = filepath.Join(cfg.Dir, "recordings")
		cfg.Settings.RecordingsDir = recDir
		cfg.Vars["recordings_dir"] = recDir
	})
	a1000, a1001 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example")
	done := dial(a1000, "71001")
	in, err := a1001.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	call := answerCall(t, done, in)
	h.waitFor("bridged", func() bool { return strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2 })
	var aleg string
	for _, ch := range h.sw.Channels() {
		if ch.Direction == "inbound" {
			aleg = ch.UUID
		}
	}
	for i := 0; i < 50; i++ {
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, uint16(i), uint32(i*160), ulaw(8000, 160))
		_ = testua.SendRTP(in.RTP, in.Remote, 0, uint16(i), uint32(i*160), ulaw(-8000, 160))
		time.Sleep(20 * time.Millisecond)
	}
	_, _ = call.Bye()
	h.waitChannels(0)
	path := filepath.Join(recDir, "a.example", aleg+".wav")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("recording missing:", err)
	}
	if len(data) < 44+8000 || string(data[22:24]) != "\x02\x00" {
		t.Fatalf("expected a stereo recording with audio, got %d bytes", len(data))
	}
	pcm, err := media.DecodeWAV(data)
	if err != nil || len(pcm) == 0 {
		t.Fatalf("recording does not decode: %v", err)
	}
}

func TestPortalAPICompat(t *testing.T) {
	h := tenantHarness(t, nil, nil)
	a1000, a1001 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example")
	var doc struct {
		Regs []struct {
			User  string `xml:"user"`
			Realm string `xml:"sip-auth-realm"`
		} `xml:"registrations>registration"`
	}
	out := h.sw.API("sofia xmlstatus profile internal reg")
	if err := xml.Unmarshal([]byte(out), &doc); err != nil || len(doc.Regs) != 2 || doc.Regs[0].Realm != "a.example" {
		t.Fatalf("xmlstatus: %v %+v\n%s", err, doc, out)
	}
	done := dial(a1000, "1001")
	in, err := a1001.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	call := answerCall(t, done, in)
	h.waitChannels(2)
	var uuid string
	for _, ch := range h.sw.Channels() {
		if ch.Direction == "inbound" {
			uuid = ch.UUID
		}
	}
	if out := h.sw.API("uuid_setvar " + uuid + " my_var hello world"); !strings.HasPrefix(out, "+OK") {
		t.Fatal(out)
	}
	if got := h.sw.API("uuid_getvar " + uuid + " my_var"); got != "hello world" {
		t.Fatalf("uuid_getvar: %q", got)
	}
	if got := h.sw.API("show channels count"); !strings.Contains(got, "2 total") {
		t.Fatalf("count: %q", got)
	}
	if got := h.sw.API("uuid_exists " + uuid); got != "true" {
		t.Fatalf("uuid_exists: %q", got)
	}
	if out := h.sw.API("uuid_kill " + uuid); !strings.HasPrefix(out, "+OK") {
		t.Fatal(out)
	}
	_ = call
	h.waitChannels(0)
	if got := h.sw.API("user_exists id 1001 a.example"); got != "true" {
		t.Fatalf("user_exists api: %q", got)
	}
}

// TestTranscriptionRecording: with transcription on, a plain bridged call
// is recorded in stereo, caller left and callee right, and the A leg
// carries what the transcript needs.
func TestTranscriptionRecording(t *testing.T) {
	recDir := ""
	h := tenantHarness(t, nil, func(cfg *config.Config) {
		recDir = filepath.Join(cfg.Dir, "recordings")
		cfg.Settings.RecordingsDir = recDir
	})
	var asked []string
	var mu sync.Mutex
	h.sw.SetTranscription(func(domain string, external bool) bool {
		mu.Lock()
		asked = append(asked, fmt.Sprintf("%s %v", domain, external))
		mu.Unlock()
		return domain == "a.example"
	})
	defer h.sw.SetTranscription(nil)
	sub := h.sw.Events().Subscribe(16, func(e *core.Event) bool {
		return e.Name == core.EvChannelHangupComplete && e.Get("variable_transcribe") == "true"
	})
	defer sub.Close()
	a1000, a1001 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example")
	done := dial(a1000, "1001")
	in, err := a1001.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	call := answerCall(t, done, in)
	h.waitFor("bridged", func() bool { return strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2 })
	for i := 0; i < 50; i++ {
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, uint16(i), uint32(i*160), ulaw(8000, 160))
		_ = testua.SendRTP(in.RTP, in.Remote, 0, uint16(i), uint32(i*160), ulaw(-3000, 160))
		time.Sleep(20 * time.Millisecond)
	}
	_, _ = call.Bye()
	h.waitChannels(0)
	var e *core.Event
	select {
	case e = <-sub.C:
	case <-time.After(5 * time.Second):
		t.Fatalf("no hangup event with transcribe=true (asked: %v)", asked)
	}
	if e.Get("variable_transcribe_a") == "" || !strings.Contains(e.Get("variable_transcribe_b"), "1001") || e.Get("variable_transcribe_customer") != "" {
		t.Fatalf("speakers: a=%q b=%q customer=%q", e.Get("variable_transcribe_a"), e.Get("variable_transcribe_b"), e.Get("variable_transcribe_customer"))
	}
	mu.Lock()
	if len(asked) == 0 || asked[0] != "a.example false" {
		t.Errorf("asked %v", asked)
	}
	mu.Unlock()
	path := filepath.Join(e.Get("variable_record_path"), e.Get("variable_record_name"))
	if !strings.HasPrefix(path, filepath.Join(recDir, "a.example", "archive")) {
		t.Fatalf("recording path %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("recording missing:", err)
	}
	if string(data[22:24]) != "\x02\x00" || len(data) < 44+4*8000/2 {
		t.Fatalf("expected a stereo recording with audio, got %d bytes, %d channels", len(data), data[22])
	}
	// Caller (A) on the left, callee (B) on the right: the levels tell.
	var left, right float64
	for i := 44; i+3 < len(data); i += 4 {
		l, r := float64(int16(binary.LittleEndian.Uint16(data[i:]))), float64(int16(binary.LittleEndian.Uint16(data[i+2:])))
		left += math.Abs(l)
		right += math.Abs(r)
	}
	if left < 1.5*right {
		t.Fatalf("left (caller, louder) %.0f, right %.0f", left, right)
	}
}
