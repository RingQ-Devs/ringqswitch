package core_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// nextNotify answers NOTIFYs until one of the subscription matches.
func nextNotify(t *testing.T, ua *testua.UA, s *testua.Subscription, match func(*sip.Message) bool) *sip.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m, err := ua.WaitRequest("NOTIFY", time.Until(deadline))
		if err != nil {
			break
		}
		_ = ua.Respond(m, 200, "", nil, "")
		if m.CallID() != s.CallID {
			continue
		}
		if match == nil || match(m) {
			return m
		}
	}
	t.Fatalf("no matching NOTIFY for %s %s", s.Event, s.Resource)
	return nil
}

func body(m *sip.Message) string { return string(m.Body) }

func TestBLFSubscriptions(t *testing.T) {
	park := `<extension name="park"><condition field="destination_number" expression="^(\*59[1-5])$">
    <action application="valet_park" data="${domain_name} $1"/>
  </condition></extension>
  <extension name="local">`
	dp := strings.Replace(testDialplan, `<extension name="local">`, park, 1)
	users := "<include>"
	for _, u := range []string{"1000", "1001", "1002", "1003"} {
		users += fmt.Sprintf(`<user id="%s"><params><param name="password" value="%s"/></params>
      <variables><variable name="effective_caller_id_number" value="%s"/><variable name="effective_caller_id_name" value="Ext %s"/>
      <variable name="user_context" value="default"/></variables></user>`, u, testPassword, u, u)
	}
	users += "</include>"
	h := newHarnessWith(t, "", users, dp, nil)
	caller, callee, other := h.phone("1000"), h.phone("1001"), h.phone("1002")
	watcher := h.phone("1003")

	// Unknown event packages are refused.
	if _, resp, _ := watcher.Subscribe("1001", "presence.winfo", 600); resp == nil || resp.StatusCode != 489 {
		t.Fatalf("presence.winfo: %v", resp)
	}
	// A wrong password is refused.
	bad, _ := testua.New("1003", "nope", h.addr)
	defer bad.Close()
	if _, resp, _ := bad.Subscribe("1001", "dialog", 600); resp == nil || resp.StatusCode != 403 {
		t.Fatalf("wrong password: %v", resp)
	}

	blf, resp, err := watcher.Subscribe("1001", "dialog", 600)
	if err != nil || resp.StatusCode != 200 || blf.ToTag == "" || resp.Get("Expires") != "600" {
		t.Fatalf("subscribe: %v %v", resp, err)
	}
	m := nextNotify(t, watcher, blf, nil)
	if m.Get("Content-Type") != "application/dialog-info+xml" || !strings.HasPrefix(m.Get("Subscription-State"), "active") ||
		strings.Contains(body(m), "<dialog ") || !strings.Contains(body(m), `entity="sip:1001@127.0.0.1"`) {
		t.Fatalf("idle state: %s\n%s", m.Short(), body(m))
	}

	// 1000 calls 1001: ringing, then on the call, then idle.
	done := make(chan *testua.Call, 1)
	go func() { c, _ := caller.Dial("1001", nil, 10*time.Second, false); done <- c }()
	in, err := callee.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in.Ring()
	m = nextNotify(t, watcher, blf, func(m *sip.Message) bool { return strings.Contains(body(m), "<state>early</state>") })
	for _, want := range []string{`direction="recipient"`, `call-id="`, "sip:1000@127.0.0.1", `display="Ext 1000"`} {
		if !strings.Contains(body(m), want) {
			t.Fatalf("ringing state lacks %s:\n%s", want, body(m))
		}
	}
	states := h.sw.BLFStates("127.0.0.1")
	found := false
	for _, s := range states {
		found = found || s.Resource == "1001" && s.State == "ringing" && s.Number == "1000"
	}
	if !found {
		t.Fatalf("BLFStates: %+v", states)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	call := <-done
	m = nextNotify(t, watcher, blf, func(m *sip.Message) bool { return strings.Contains(body(m), "<state>confirmed</state>") })
	_, _ = call.Bye()
	nextNotify(t, watcher, blf, func(m *sip.Message) bool { return !strings.Contains(body(m), "<dialog ") })

	// A park slot: lit while a call is parked there.
	slot, resp, err := watcher.Subscribe("*591", "dialog", 600)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("subscribe *591: %v %v", resp, err)
	}
	nextNotify(t, watcher, slot, nil)
	go func() { c, _ := caller.Dial("*591", nil, 10*time.Second, false); done <- c }()
	parked := <-done
	if parked == nil || parked.Final == nil || parked.Final.StatusCode != 200 {
		t.Fatalf("park: %+v", parked)
	}
	m = nextNotify(t, watcher, slot, func(m *sip.Message) bool { return strings.Contains(body(m), "<state>confirmed</state>") })
	if !strings.Contains(body(m), "sip:1000@") {
		t.Fatalf("parked caller not shown:\n%s", body(m))
	}
	go func() { c, _ := other.Dial("*591", nil, 10*time.Second, false); done <- c }()
	picked := <-done
	if picked == nil || picked.Final == nil || picked.Final.StatusCode != 200 {
		t.Fatalf("retrieve: %+v", picked)
	}
	nextNotify(t, watcher, slot, func(m *sip.Message) bool { return !strings.Contains(body(m), "<dialog ") })
	_, _ = picked.Bye()

	// The message lamp.
	mwi, resp, err := watcher.Subscribe("1003", "message-summary", 600)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("subscribe message-summary: %v %v", resp, err)
	}
	m = nextNotify(t, watcher, mwi, nil)
	if !strings.Contains(body(m), "Messages-Waiting: no") {
		t.Fatalf("mwi: %s", body(m))
	}

	// DND kept in step: set from the apps, and from the phone's key.
	dnd, resp, err := watcher.Subscribe("1003", "as-feature-event", 600)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("subscribe as-feature-event: %v %v", resp, err)
	}
	m = nextNotify(t, watcher, dnd, func(m *sip.Message) bool { return strings.Contains(body(m), "DoNotDisturbEvent") })
	if !strings.Contains(body(m), "<doNotDisturbOn>false</doNotDisturbOn>") {
		t.Fatalf("dnd: %s", body(m))
	}
	if err := h.sw.SetUserVars("1003", "127.0.0.1", map[string]string{"do_not_disturb": "true"}, "test"); err != nil {
		t.Fatal(err)
	}
	nextNotify(t, watcher, dnd, func(m *sip.Message) bool {
		return strings.Contains(body(m), "<doNotDisturbOn>true</doNotDisturbOn>")
	})
	// Calls to an extension in DND get busy.
	if c, _ := caller.Dial("1003", nil, 5*time.Second, false); c == nil || c.Final == nil || c.Final.StatusCode != 486 {
		t.Fatalf("call to DND extension: %+v", c)
	}
	off := `<?xml version="1.0" encoding="ISO-8859-1"?><SetDoNotDisturb xmlns="http://www.ecma-international.org/standards/ecma-323/csta/ed3"><device>1003</device><doNotDisturbOn>false</doNotDisturbOn></SetDoNotDisturb>`
	if resp, err := dnd.Refresh(600, []byte(off), "application/x-as-feature-event+xml"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("SetDoNotDisturb: %v %v", resp, err)
	}
	h.waitFor("DND off from the phone", func() bool { return h.sw.UserVar("1003", "127.0.0.1", "do_not_disturb") == "false" })
	fwd := `<SetForwarding xmlns="http://www.ecma-international.org/standards/ecma-323/csta/ed3"><device>1003</device><forwardingType>forwardImmediate</forwardingType><activateForward>true</activateForward><forwardDN>1002</forwardDN></SetForwarding>`
	if resp, err := dnd.Refresh(600, []byte(fwd), "application/x-as-feature-event+xml"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("SetForwarding: %v %v", resp, err)
	}
	go func() { c, _ := caller.Dial("1003", nil, 10*time.Second, false); done <- c }()
	if in, err := other.WaitInvite(5 * time.Second); err != nil {
		t.Fatal("forwarding set from the phone did not ring 1002:", err)
	} else {
		_ = in.Reject(486)
	}
	<-done

	// Ending a subscription.
	if resp, err := blf.Refresh(0, nil, ""); err != nil || resp.StatusCode != 200 {
		t.Fatalf("unsubscribe: %v %v", resp, err)
	}
	nextNotify(t, watcher, blf, func(m *sip.Message) bool { return strings.HasPrefix(m.Get("Subscription-State"), "terminated") })
	if n := len(h.sw.Subscriptions()); n != 3 {
		t.Fatalf("subscriptions left: %d %v", n, h.sw.Subscriptions())
	}
}
