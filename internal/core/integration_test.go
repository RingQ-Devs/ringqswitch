package core_test

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/esl"
	"github.com/Cal4Care-Developers/RingQswitch/internal/logbus"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

const testPassword = "pw-1234"

var rtpBase atomic.Int32

const testDialplan = `<include><context name="default">
  <extension name="echo"><condition field="destination_number" expression="^9196$">
    <action application="answer"/><action application="echo"/>
  </condition></extension>
  <extension name="ivr"><condition field="destination_number" expression="^5000$">
    <action application="answer"/>
    <action application="play_and_get_digits" data="1 1 2 3000 # silence_stream://200 silence_stream://0 choice ^[0-9]$"/>
    <action application="transfer" data="ivr-${choice}"/>
  </condition></extension>
  <extension name="ivr7"><condition field="destination_number" expression="^ivr-7$">
    <action application="hangup" data="CALL_REJECTED"/>
  </condition></extension>
  <extension name="group"><condition field="destination_number" expression="^2000$">
    <action application="bridge" data="user/1001,user/1002"/>
  </condition></extension>
  <extension name="local"><condition field="destination_number" expression="^(10\d\d)$">
    <action application="set" data="call_timeout=10"/>
    <action application="bridge" data="user/$1"/>
  </condition></extension>
</context></include>`

type harness struct {
	t    *testing.T
	sw   *core.Core
	addr netip.AddrPort
	dir  string
	logs *logbus.Bus // what rq_cli follows ("log 7")
}

func newHarness(t *testing.T, extraSettings string) *harness {
	t.Helper()
	users := "<include>"
	for _, u := range []string{"1000", "1001", "1002", "1003"} {
		users += fmt.Sprintf(`<user id="%s"><params><param name="password" value="%s"/></params>
      <variables><variable name="effective_caller_id_number" value="%s"/><variable name="effective_caller_id_name" value="Ext %s"/></variables></user>`,
			u, testPassword, u, u)
	}
	users += "</include>"
	return newHarnessWith(t, extraSettings, users, testDialplan, nil)
}

// newHarnessWith starts a switch with the given directory and dialplan
// documents; mutate may adjust the loaded configuration before start.
func newHarnessWith(t *testing.T, extraSettings, users, dp string, mutate func(*config.Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	base := 41000 + int(rtpBase.Add(1)%100)*200 // stays below 61000 with -count=N
	main := fmt.Sprintf(`<configuration>
  <vars><var name="domain" value="127.0.0.1"/></vars>
  <settings>
    <param name="sip-ip" value="127.0.0.1"/>
    <param name="sip-port" value="0"/>
    <param name="enable-tcp" value="false"/>
    <param name="rtp-ip" value="127.0.0.1"/>
    <param name="rtp-start-port" value="%d"/>
    <param name="rtp-end-port" value="%d"/>
    <param name="event-socket-listen" value=""/>
    <param name="cdr-csv" value="%s"/>
    <param name="sounds-dir" value="%s"/>
    %s
  </settings>
</configuration>`, base, base+198, filepath.Join(dir, "cdr.csv"), dir, extraSettings)
	for path, content := range map[string]string{
		"ringqswitch.xml":      main,
		"directory/users.xml":  users,
		"dialplan/default.xml": dp,
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(cfg)
	}
	level := slog.LevelWarn
	if os.Getenv("RQ_TEST_DEBUG") != "" {
		level = slog.LevelDebug
	}
	logs := logbus.New()
	log := slog.New(logbus.NewHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}), logs))
	sw, err := core.New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sw.Stop)
	return &harness{t: t, sw: sw, dir: dir, logs: logs,
		addr: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(sw.SIPPort()))}
}

func (h *harness) phone(user string) *testua.UA {
	h.t.Helper()
	ua, err := testua.New(user, testPassword, h.addr)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(ua.Close)
	resp, err := ua.Register(300)
	if err != nil || resp.StatusCode != 200 {
		h.t.Fatalf("register %s failed: %v %v", user, resp, err)
	}
	return ua
}

func (h *harness) waitChannels(n int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.sw.Channels()) == n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("expected %d channels, have %d", n, len(h.sw.Channels()))
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func ulaw(v int16, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = media.LinearToUlaw(v)
	}
	return b
}

func TestRegistration(t *testing.T) {
	h := newHarness(t, "")
	ua, _ := testua.New("1000", "wrong", h.addr)
	defer ua.Close()
	resp, err := ua.Register(300)
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("wrong password should get 403, got %v %v", resp, err)
	}
	ua.Password = testPassword
	resp, err = ua.Register(30)
	if err != nil || resp.StatusCode != 423 || resp.Get("Min-Expires") != "60" {
		t.Fatalf("expected 423 Interval Too Brief, got %v %v", resp, err)
	}
	resp, err = ua.Register(300)
	if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Get("Contact"), "expires=") {
		t.Fatalf("register failed: %v %v", resp, err)
	}
	if out := h.sw.API("show registrations"); !strings.Contains(out, "1000") || !strings.Contains(out, "1 total") {
		t.Fatalf("registration not listed:\n%s", out)
	}
	// The same device re-registering with another contact replaces its
	// registration instead of appearing twice.
	ua.ContactParams = ";rinstance=second"
	if resp, err = ua.Register(300); err != nil || resp.StatusCode != 200 {
		t.Fatalf("re-register failed: %v %v", resp, err)
	}
	if all := h.sw.Registrar().All(); len(all) != 1 || !strings.Contains(all[0].Contact, "rinstance=second") {
		t.Fatalf("expected one binding with the new contact, have %d", len(all))
	}
	resp, err = ua.Register(0)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("unregister failed: %v %v", resp, err)
	}
	if n := len(h.sw.Registrar().All()); n != 0 {
		t.Fatalf("expected no bindings, have %d", n)
	}
	// Another user may not register someone else's extension.
	other, _ := testua.New("1001", testPassword, h.addr)
	defer other.Close()
	other.User = "1000"
	other.Password = "nope"
	if resp, _ := other.Register(300); resp == nil || resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %v", resp)
	}
}

func TestBasicCallWithMediaRelay(t *testing.T) {
	h := newHarness(t, "")
	alice, bob := h.phone("1000"), h.phone("1001")

	type result struct {
		call *testua.Call
		err  error
	}
	done := make(chan result, 1)
	go func() {
		c, err := alice.Dial("1001", nil, 10*time.Second, false)
		done <- result{c, err}
	}()
	in, err := bob.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.Invite.Get("From"), "1000") || !strings.Contains(in.Invite.Get("From"), "Ext 1000") {
		t.Fatalf("caller id not presented: %s", in.Invite.Get("From"))
	}
	if in.Remote.Addr().String() != "127.0.0.1" || in.Remote.Port() < 41000 {
		t.Fatalf("callee SDP should point at the switch, got %v", in.Remote)
	}
	if err := in.Ring(); err != nil {
		t.Fatal(err)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	call := r.call
	if call.Final.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", call.Final.StatusCode)
	}
	if len(call.Prov) == 0 || call.Prov[len(call.Prov)-1] != 180 {
		t.Fatalf("expected 180 to be relayed, got %v", call.Prov)
	}
	if call.RemoteRTP == in.Remote {
		t.Fatal("each leg must get its own media port")
	}
	h.waitChannels(2)
	// The caller's 200 OK is sent just before the channels are marked bridged.
	h.waitFor("channels bridged", func() bool {
		return strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2
	})

	// Alice -> Bob
	payload := ulaw(1000, 160)
	var got []byte
	for i := 0; i < 5 && got == nil; i++ {
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, uint16(i), uint32(i*160), payload)
		if _, p, err := testua.RecvRTP(in.RTP, 300*time.Millisecond, nil); err == nil {
			got = p
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("alice->bob media not relayed intact (%d bytes)", len(got))
	}
	// Bob -> Alice
	back := ulaw(-3000, 160)
	got = nil
	for i := 0; i < 5 && got == nil; i++ {
		_ = testua.SendRTP(in.RTP, in.Remote, 0, uint16(i), uint32(i*160), back)
		if _, p, err := testua.RecvRTP(call.RTP, 300*time.Millisecond, nil); err == nil {
			got = p
		}
	}
	if !bytes.Equal(got, back) {
		t.Fatal("bob->alice media not relayed")
	}

	resp, err := call.Bye()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("BYE failed: %v %v", resp, err)
	}
	bye, err := bob.WaitRequest("BYE", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = bob.Respond(bye, 200, "", nil, "")
	h.waitChannels(0)

	// Two CDR rows (one per leg) plus the header.
	var rows [][]string
	for i := 0; i < 50; i++ {
		f, _ := os.ReadFile(filepath.Join(h.dir, "cdr.csv"))
		rows, _ = csv.NewReader(bytes.NewReader(f)).ReadAll()
		if len(rows) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rows) != 3 || rows[0][0] != "uuid" {
		t.Fatalf("expected header + 2 CDRs, got %v", rows)
	}
	if rows[1][11] != "NORMAL_CLEARING" || rows[1][4] != "1001" {
		t.Fatalf("unexpected cdr %v", rows[1])
	}
}

func TestCodecPassthrough(t *testing.T) {
	h := newHarness(t, "")
	alice, bob := h.phone("1000"), h.phone("1001")
	done := make(chan *testua.Call, 1)
	go func() {
		c, _ := alice.Dial("1001", []int{9, 0}, 10*time.Second, false)
		done <- c
	}()
	in, err := bob.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Codecs) < 2 || in.Codecs[0] != "9" {
		t.Fatalf("caller codecs should be offered unchanged, got %v", in.Codecs)
	}
	if err := in.Answer(9); err != nil {
		t.Fatal(err)
	}
	call := <-done
	if call == nil || call.Final.StatusCode != 200 {
		t.Fatal("call failed")
	}
	_, fmts, _ := testua.SDPAddr(call.Final.Body)
	if len(fmts) == 0 || fmts[0] != "9" {
		t.Fatalf("caller should be answered with G722, got %v", fmts)
	}
	g722 := []byte{1, 2, 3, 4, 5}
	var got []byte
	for i := 0; i < 5 && got == nil; i++ {
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 9, uint16(i), uint32(i*160), g722)
		if pt, p, err := testua.RecvRTP(in.RTP, 300*time.Millisecond, nil); err == nil && pt == 9 {
			got = p
		}
	}
	if !bytes.Equal(got, g722) {
		t.Fatal("G722 payload not passed through")
	}
	_, _ = call.Bye()
}

func TestHoldReinvite(t *testing.T) {
	h := newHarness(t, "")
	alice, bob := h.phone("1000"), h.phone("1001")
	done := make(chan *testua.Call, 1)
	go func() {
		c, _ := alice.Dial("1001", []int{0}, 10*time.Second, false)
		done <- c
	}()
	in, err := bob.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	call := <-done
	if call == nil || call.Final.StatusCode != 200 {
		t.Fatal("call failed")
	}
	port := call.RTP.LocalAddr().(*net.UDPAddr).Port
	hold := bytes.Replace(testua.SDP(port, 0), []byte("a=sendrecv"), []byte("a=sendonly"), 1)
	resp, err := call.Request("INVITE", "application/sdp", hold, "Contact", "<"+alice.ContactURI()+">")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("re-INVITE failed: %v %v", resp, err)
	}
	if !bytes.Contains(resp.Body, []byte("a=recvonly")) {
		t.Fatalf("hold should be answered recvonly:\n%s", resp.Body)
	}
	addr, _, _ := testua.SDPAddr(resp.Body)
	if addr != call.RemoteRTP {
		t.Fatal("our media address must not change on re-INVITE")
	}
	if !bytes.Contains(resp.Body, []byte("o=RingQswitch")) || bytes.Contains(resp.Body, []byte(" 1 IN IP4")) {
		t.Fatalf("SDP version must increase when the answer changes:\n%s", resp.Body)
	}
	_ = call.Ack()
	h.waitChannels(2)
	_, _ = call.Bye()
}

func TestEcho(t *testing.T) {
	h := newHarness(t, "")
	alice := h.phone("1000")
	call, err := alice.Dial("9196", []int{0}, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatalf("echo call failed: %v", err)
	}
	payload := ulaw(1234, 160)
	ok := false
	for i := 0; i < 20 && !ok; i++ {
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, uint16(i), uint32(i*160), payload)
		_, _, err := testua.RecvRTP(call.RTP, 200*time.Millisecond, func(pt uint8, p []byte) bool {
			return pt == 0 && bytes.Equal(p, payload)
		})
		ok = err == nil
	}
	if !ok {
		t.Fatal("no echo")
	}
	if resp, err := call.Bye(); err != nil || resp.StatusCode != 200 {
		t.Fatalf("bye: %v %v", resp, err)
	}
	h.waitChannels(0)
}

func TestFailures(t *testing.T) {
	h := newHarness(t, "")
	alice, bob := h.phone("1000"), h.phone("1001")

	// Unregistered extension.
	call, err := alice.Dial("1002", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 480 {
		t.Fatalf("expected 480 for unregistered user, got %v %v", call, err)
	}
	if !strings.Contains(call.Final.Get("Reason"), "USER_NOT_REGISTERED") {
		t.Fatalf("missing Q.850 reason: %q", call.Final.Get("Reason"))
	}
	// No route.
	call, err = alice.Dial("777", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 404 {
		t.Fatalf("expected 404 for unknown destination, got %v %v", call, err)
	}
	// Busy callee.
	done := make(chan *testua.Call, 1)
	go func() {
		c, _ := alice.Dial("1001", nil, 5*time.Second, false)
		done <- c
	}()
	in, err := bob.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Reject(486); err != nil {
		t.Fatalf("switch did not ACK the 486: %v", err)
	}
	c := <-done
	if c == nil || c.Final.StatusCode != 486 {
		t.Fatalf("busy not propagated: %v", c)
	}
	h.waitChannels(0)
}

func TestCancelWhileRinging(t *testing.T) {
	h := newHarness(t, "")
	alice, bob := h.phone("1000"), h.phone("1001")
	type res struct {
		c   *testua.Call
		err error
	}
	done := make(chan res, 1)
	go func() {
		c, err := alice.Dial("1001", nil, 5*time.Second, true)
		done <- res{c, err}
	}()
	in, err := bob.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in.Ring()
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	resp, err := r.c.Cancel()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CANCEL not accepted: %v %v", resp, err)
	}
	if err := r.c.WaitFinal(5 * time.Second); err != nil || r.c.Final.StatusCode != 487 {
		t.Fatalf("expected 487, got %v %v", r.c.Final, err)
	}
	cancel, err := bob.WaitRequest("CANCEL", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = bob.Respond(cancel, 200, "", nil, "")
	if err := in.Reject(487); err != nil {
		t.Fatalf("no ACK for 487: %v", err)
	}
	h.waitChannels(0)
}

func TestRingGroupFirstAnswerWins(t *testing.T) {
	h := newHarness(t, "")
	alice, b1, b2 := h.phone("1000"), h.phone("1001"), h.phone("1002")
	done := make(chan *testua.Call, 1)
	go func() {
		c, _ := alice.Dial("2000", nil, 10*time.Second, false)
		done <- c
	}()
	in1, err := b1.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	in2, err := b2.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in1.Ring()
	if err := in2.Answer(0); err != nil {
		t.Fatal(err)
	}
	c := <-done
	if c == nil || c.Final.StatusCode != 200 {
		t.Fatal("ring group call not answered")
	}
	cancel, err := b1.WaitRequest("CANCEL", 5*time.Second)
	if err != nil {
		t.Fatal("losing leg was not cancelled")
	}
	if !strings.Contains(cancel.Get("Reason"), "LOSE_RACE") {
		t.Fatalf("unexpected cancel reason %q", cancel.Get("Reason"))
	}
	_ = b1.Respond(cancel, 200, "", nil, "")
	_ = in1.Reject(487)
	h.waitChannels(2)
	_, _ = c.Bye()
}

func TestIVRDigitsAndTransfer(t *testing.T) {
	h := newHarness(t, "")
	alice := h.phone("1000")
	call, err := alice.Dial("5000", []int{0}, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatalf("ivr call failed: %v", err)
	}
	// Let the (200 ms) prompt start, then send RFC 4733 digit 7 (end x3).
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 101, uint16(100+i), 8000, []byte{7, 0x80, 0, 160})
	}
	bye, err := alice.WaitRequest("BYE", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = alice.Respond(bye, 200, "", nil, "")
	if !strings.Contains(bye.Get("Reason"), "CALL_REJECTED") {
		t.Fatalf("digit did not route to ivr-7: Reason %q", bye.Get("Reason"))
	}
}

func TestBlindTransfer(t *testing.T) {
	h := newHarness(t, "")
	alice, bob, carol := h.phone("1000"), h.phone("1001"), h.phone("1002")
	done := make(chan *testua.Call, 1)
	go func() {
		c, _ := alice.Dial("1001", []int{0}, 10*time.Second, false)
		done <- c
	}()
	in, err := bob.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	aliceCall := <-done
	if aliceCall == nil || aliceCall.Final.StatusCode != 200 {
		t.Fatal("initial call failed")
	}
	h.waitChannels(2)
	h.waitFor("channels bridged", func() bool {
		return strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2
	})

	// Bob transfers Alice to Carol.
	bobDialog := in.Dialog()
	referDone := make(chan int, 1)
	go func() {
		resp, err := bobDialog.Request("REFER", "", nil, "Refer-To", "<sip:1002@127.0.0.1>", "Referred-By", "<sip:1001@127.0.0.1>")
		if err != nil {
			referDone <- 0
			return
		}
		referDone <- resp.StatusCode
	}()
	if code := <-referDone; code != 202 {
		t.Fatalf("REFER not accepted: %d", code)
	}
	notify, err := bob.WaitRequest("NOTIFY", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = bob.Respond(notify, 200, "", nil, "")
	if !strings.Contains(string(notify.Body), "200 OK") {
		t.Fatalf("unexpected sipfrag %q", notify.Body)
	}
	bye, err := bob.WaitRequest("BYE", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = bob.Respond(bye, 200, "", nil, "")

	cin, err := carol.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("transfer target never rang")
	}
	if err := cin.Answer(0); err != nil {
		t.Fatal(err)
	}
	h.waitChannels(2)
	// Alice's media now reaches Carol through Alice's original port.
	payload := ulaw(777, 160)
	var got []byte
	for i := 0; i < 10 && got == nil; i++ {
		_ = testua.SendRTP(aliceCall.RTP, aliceCall.RemoteRTP, 0, uint16(i), uint32(i*160), payload)
		if _, p, err := testua.RecvRTP(cin.RTP, 300*time.Millisecond, nil); err == nil {
			got = p
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("transferred call has no media path")
	}
	_, _ = aliceCall.Bye()
}

func TestEventSocket(t *testing.T) {
	h := newHarness(t, "")
	srv, err := esl.Listen("127.0.0.1:0", "secret", h.sw, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if _, err := esl.Dial(srv.Addr(), "bad"); err == nil {
		t.Fatal("bad password accepted")
	}
	c, err := esl.Dial(srv.Addr(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out, err := c.API("status")
	if err != nil || !strings.HasPrefix(out, "UP ") {
		t.Fatalf("status: %q %v", out, err)
	}
	if out, _ := c.API("nosuchcommand"); !strings.HasPrefix(out, "-ERR") {
		t.Fatalf("expected error, got %q", out)
	}
	m, err := c.Send("event plain CHANNEL_CREATE CUSTOM ringq::register")
	if err != nil || !strings.HasPrefix(m.Headers["Reply-Text"], "+OK") {
		t.Fatalf("event subscribe failed: %v %v", m, err)
	}
	h.phone("1000")
	select {
	case e := <-c.Events():
		if h := e.EventHeaders(); h["Event-Subclass"] != "ringq::register" || h["User"] != "1000" {
			t.Fatalf("unexpected event %v", h)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no register event")
	}
	out, _ = c.API("originate error/USER_BUSY 1000")
	if !strings.Contains(out, "USER_BUSY") {
		t.Fatalf("originate error path: %q", out)
	}

	// Client compatibility: JSON shape, client supplied Job-UUID, filters.
	out, _ = c.API("show registrations as json")
	if !strings.Contains(out, `"row_count": 1`) || !strings.Contains(out, `"reg_user": "1000"`) || !strings.Contains(out, `"network_proto": "udp"`) {
		t.Fatalf("registrations json not in the stable row shape:\n%s", out)
	}
	if m, err := c.Send("event plain BACKGROUND_JOB"); err != nil || !strings.HasPrefix(m.Headers["Reply-Text"], "+OK") {
		t.Fatalf("subscribe background jobs: %v %v", m, err)
	}
	m, err = c.Send("bgapi version\nJob-UUID: client-job-42")
	if err != nil || m.Headers["Job-UUID"] != "client-job-42" {
		t.Fatalf("bgapi must honour the client's Job-UUID: %v %v", m, err)
	}
	deadline := time.After(3 * time.Second)
	for got := false; !got; {
		select {
		case e := <-c.Events():
			h := e.EventHeaders()
			got = h["Event-Name"] == "BACKGROUND_JOB" && h["Job-UUID"] == "client-job-42"
		case <-deadline:
			t.Fatal("no BACKGROUND_JOB event for the client's Job-UUID")
		}
	}
	if m, _ := c.Send("filter Unique-ID abc"); !strings.Contains(m.Headers["Reply-Text"], "filter added") {
		t.Fatalf("filter: %v", m.Headers)
	}
	if m, _ := c.Send("sendmsg no-such-uuid\ncall-command: hangup"); !strings.HasPrefix(m.Headers["Reply-Text"], "-ERR invalid session id") {
		t.Fatalf("sendmsg: %v", m.Headers)
	}
}

func TestBruteForceBlocking(t *testing.T) {
	h := newHarness(t, `<param name="auth-fail-threshold" value="3"/>`)
	ua, _ := testua.New("1000", "wrong", h.addr)
	defer ua.Close()
	for i := 0; i < 3; i++ {
		if resp, err := ua.Register(300); err != nil || resp.StatusCode != 403 {
			t.Fatalf("attempt %d: %v %v", i, resp, err)
		}
	}
	ua.Password = testPassword
	if _, err := ua.Register(300); err == nil {
		t.Fatal("blocked address should be ignored")
	}
	if out := h.sw.API("status"); !strings.Contains(out, "1 address(es) blocked") {
		t.Fatalf("status should report the block:\n%s", out)
	}
	if out := h.sw.API("blocked"); !strings.Contains(out, "127.0.0.1") {
		t.Fatalf("block list: %s", out)
	}
	if out := h.sw.API("unblock 127.0.0.1"); !strings.HasPrefix(out, "+OK") {
		t.Fatalf("unblock: %s", out)
	}
	if resp, err := ua.Register(300); err != nil || resp.StatusCode != 200 {
		t.Fatalf("register after unblock: %v %v", resp, err)
	}
}

// TestOneWayAudioWarning: a leg that sends no RTP after the answer is
// reported once in the log and the event log (the other leg is not).
func TestOneWayAudioWarning(t *testing.T) {
	t.Cleanup(core.SetNoRTPAfter(300 * time.Millisecond))
	h := newHarness(t, "")
	alice, bob := h.phone("1000"), h.phone("1001")
	done := make(chan *testua.Call, 1)
	go func() {
		c, err := alice.Dial("1001", nil, 10*time.Second, false)
		if err != nil {
			t.Error(err)
		}
		done <- c
	}()
	in, err := bob.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	call := <-done
	if call == nil {
		t.FailNow()
	}
	h.waitFor("channels bridged", func() bool {
		return strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2
	})
	// Alice talks; Bob's phone sends nothing (its RTP is "blocked").
	for i := 0; i < 15; i++ {
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, uint16(i), uint32(i*160), ulaw(1000, 160))
		time.Sleep(40 * time.Millisecond)
	}
	h.sw.CheckMediaNow()
	h.sw.CheckMediaNow()
	got := h.sw.EventLog("call", "one-way audio", 10)
	if len(got) != 1 {
		t.Fatalf("expected one one-way audio entry, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "1001") {
		t.Fatalf("the entry should name the silent leg (1001): %s", got[0].Text)
	}
	_, _ = call.Bye()
}

// TestOutboundFromAndCallerIDType: an outbound rule's From user, display
// name and host and its caller ID header reach the carrier, and a nolocal:
// export wins over the caller's own variable of the same name.
func TestOutboundFromAndCallerIDType(t *testing.T) {
	carrier, err := testua.NewAt("carrier", "", netip.AddrPort{}, netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	defer carrier.Close()
	cases := []struct{ cid, header string }{{"rpid", "Remote-Party-ID"}, {"pid", "P-Asserted-Identity"}, {"none", ""}}
	var exts strings.Builder
	for i, tc := range cases {
		fmt.Fprintf(&exts, `
  <extension name="out%[1]d"><condition field="destination_number" expression="^(77%[1]d\d+)$">
    <action application="set" data="effective_caller_id_number=6561234567"/>
    <action application="set" data="effective_caller_id_name=Acme"/>
    <action application="export" data="nolocal:sip_from_user=${effective_caller_id_number}"/>
    <action application="export" data="nolocal:sip_from_display=Acme Sales"/>
    <action application="export" data="nolocal:sip_from_host=example.com"/>
    <action application="export" data="nolocal:sip_cid_type=%[2]s"/>
    <action application="bridge" data="sip:$1@%[3]s"/>
  </condition></extension>`, i, tc.cid, carrier.Local)
	}
	dp := strings.Replace(testDialplan, `</context></include>`, exts.String()+"\n</context></include>", 1)
	users := "<include>"
	for _, u := range []string{"1000", "1001"} {
		users += fmt.Sprintf(`<user id="%s"><params><param name="password" value="%s"/></params></user>`, u, testPassword)
	}
	users += "</include>"
	h := newHarnessWith(t, "", users, dp, nil)
	alice := h.phone("1000")
	seen := map[string]bool{}
	for i, tc := range cases {
		done := make(chan error, 1)
		go func() {
			_, err := alice.Dial(fmt.Sprintf("77%d1234", i), nil, 5*time.Second, false)
			done <- err
		}()
		var in *testua.Incoming
		for in == nil || seen[in.Invite.CallID()] { // skip retransmissions of the last call
			if in, err = carrier.WaitInvite(5 * time.Second); err != nil {
				t.Fatal(err)
			}
		}
		seen[in.Invite.CallID()] = true
		from := in.Invite.Get("From")
		if !strings.Contains(from, `"Acme Sales"`) || !strings.Contains(from, "sip:6561234567@example.com") {
			t.Fatalf("From: %s", from)
		}
		for _, hd := range []string{"Remote-Party-ID", "P-Asserted-Identity"} {
			got := in.Invite.Get(hd)
			if (hd == tc.header) != (got != "") {
				t.Fatalf("cid type %s: %s = %q", tc.cid, hd, got)
			}
			if got != "" && !strings.Contains(got, "6561234567") {
				t.Fatalf("%s: %s", hd, got)
			}
		}
		_ = in.Reject(486)
	}
}

// TestSIPProfilePort: a second SIP profile opens its own port; phones get
// their answers from it, and a gateway of that profile registers from it.
func TestSIPProfilePort(t *testing.T) {
	carrier, err := testua.NewAt("carrier", "", netip.AddrPort{}, netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(carrier.Close) // after the switch stopped (and unregistered)
	free, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	extra := free.LocalAddr().(*net.UDPAddr).Port
	free.Close()
	users := fmt.Sprintf(`<include><user id="1000"><params><param name="password" value="%s"/></params></user></include>`, testPassword)
	h := newHarnessWith(t, "", users, testDialplan, func(cfg *config.Config) {
		cfg.Profiles = []config.SIPProfile{{Name: "external", Enabled: true, Port: extra, IP: "203.0.113.9", ExtSIPIP: "203.0.113.9"}}
		cfg.Gateways = append(cfg.Gateways, config.Gateway{Name: "carrier1", Proxy: carrier.Local.String(), Transport: "udp", Username: "acct",
			Password: "secret", Register: true, Expire: 120, RetrySeconds: 5, Profile: "external", FromUser: "acct", FromDomain: "127.0.0.1",
			Realm: "127.0.0.1", Context: "public"})
	})
	reg, err := carrier.WaitRequest("REGISTER", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Src.AP.Port() != uint16(extra) || !strings.Contains(reg.Get("Contact"), fmt.Sprintf(":%d", extra)) {
		t.Fatalf("the gateway should register from port %d: from %v, Contact %s", extra, reg.Src, reg.Get("Contact"))
	}
	if strings.Contains(reg.Get("Contact"), "203.0.113.9") {
		t.Fatalf("a profile address that is not this server's was advertised: %s", reg.Get("Contact"))
	}
	carrier.Server = reg.Src.AP // answer where the REGISTER came from
	_ = carrier.Respond(reg, 200, "gwtag", nil, "")
	go func() { // and every later one (the unregister at the end)
		for {
			r, err := carrier.WaitRequest("REGISTER", time.Minute)
			if err != nil {
				return
			}
			_ = carrier.Respond(r, 200, "gwtag", nil, "")
		}
	}()

	phone, err := testua.New("1000", testPassword, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(extra)))
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	resp, err := phone.Register(60)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("register on the profile port: %v %v", resp, err)
	}
	if resp.Src.AP.Port() != uint16(extra) {
		t.Fatalf("the answer came from port %d, not the profile's %d", resp.Src.AP.Port(), extra)
	}
	var ext *core.ProfileStatus
	for _, p := range h.sw.SIPProfiles() {
		if p.Name == "external" {
			p := p
			ext = &p
		}
	}
	if ext == nil || !ext.Listening || ext.BindIP == "203.0.113.9" || ext.Main {
		t.Fatalf("profile status: %+v (all: %+v)", ext, h.sw.SIPProfiles())
	}
}

// TestEventSocketLog: "log 7" streams console log lines (what
// rq_cli shows): the new channel, the dialplan trace, the
// applications, the hangup; "log 5" leaves out debug; "nolog" stops.
func TestEventSocketLog(t *testing.T) {
	h := newHarness(t, "")
	srv, err := esl.Listen("127.0.0.1:0", "secret", h.sw, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.SetLogBus(h.logs)
	c, err := esl.Dial(srv.Addr(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if m, err := c.Send("log 7"); err != nil || m.Headers["Reply-Text"] != "+OK log level DEBUG [7]" {
		t.Fatalf("log 7: %v %v", m, err)
	}
	alice := h.phone("1000")
	call, err := alice.Dial("9196", []int{0}, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatalf("echo call failed: %v", err)
	}
	_, _ = call.Bye()
	h.waitChannels(0)
	want := []string{"[NOTICE] inbound.go", "New Channel", "Processing ", "Dialplan: ", "Regex (PASS) [echo] destination_number(9196) =~ /^9196$/",
		"EXECUTE [depth=0] ", "answer(", "Callstate Change", "Hangup ", "Close Channel"}
	var all strings.Builder
	deadline := time.After(3 * time.Second)
	for missing := want; len(missing) > 0; {
		select {
		case m := <-c.Logs():
			all.WriteString(m.Body)
			if m.Headers["Log-Level"] == "" {
				t.Fatalf("log line without Log-Level: %v", m.Headers)
			}
			var left []string
			for _, w := range missing {
				if !strings.Contains(all.String(), w) {
					left = append(left, w)
				}
			}
			missing = left
		case <-deadline:
			t.Fatalf("log lines missing %q in:\n%s", missing, all.String())
		}
	}

	// From notice up: no debug lines (no dialplan trace).
	if m, _ := c.Send("log notice"); m.Headers["Reply-Text"] != "+OK log level NOTICE [5]" {
		t.Fatalf("log notice: %v", m.Headers)
	}
	for len(c.Logs()) > 0 {
		<-c.Logs()
	}
	call, err = alice.Dial("9196", []int{0}, 5*time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = call.Bye()
	h.waitChannels(0)
	time.Sleep(200 * time.Millisecond)
	for len(c.Logs()) > 0 {
		m := <-c.Logs()
		if lv := m.Headers["Log-Level"]; lv == "7" || lv == "6" {
			t.Fatalf("level %s line at log 5: %s", lv, m.Body)
		}
	}
	if m, _ := c.Send("nolog"); !strings.HasPrefix(m.Headers["Reply-Text"], "+OK") {
		t.Fatalf("nolog: %v", m.Headers)
	}
	if h.logs.Listening() {
		t.Fatal("still following the log after nolog")
	}
}
