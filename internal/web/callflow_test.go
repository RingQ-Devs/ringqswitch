package web_test

import (
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// TestCallFlowDIDs: Call Flow adds a DID before it goes anywhere (calls to
// it are not answered), links it by changing only its action (the trunk
// stays), and unlinks it again.
func TestCallFlowDIDs(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"

	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	did := r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6557777", "gateway_uuid": gw["gateway_uuid"], "destination_enabled": "true"})
	id := did["destination_uuid"].(string)
	if did["action"] != "" {
		t.Fatalf("new DID action: %v", did["action"])
	}
	enabledPlans := `select count(*) from v_dialplans where dialplan_context = 'public' and dialplan_enabled = 'true'`
	if n := r.count(enabledPlans); n != 0 {
		t.Fatalf("a DID that goes nowhere has %d enabled dialplans", n)
	}
	dial := func() *testua.Call {
		t.Helper()
		c, _ := r.carrier.Dial("6557777", nil, 3*time.Second, false)
		return c
	}
	if c := dial(); c != nil && c.Final != nil && c.Final.StatusCode == 200 {
		t.Fatal("a call to an unlinked DID was answered")
	}

	// Dropped on Extension: only the action changes.
	linked := r.mustCall("PUT", d+"dids/"+id, map[string]any{"action": "transfer:1001 XML " + domain})
	if linked["gateway_uuid"] != gw["gateway_uuid"] || linked["destination_number"] != "6557777" || linked["action"] != "transfer:1001 XML "+domain {
		t.Fatalf("linked DID: %v", linked)
	}
	if n := r.count(enabledPlans); n != 1 {
		t.Fatalf("linked DID dialplans enabled: %d", n)
	}
	phone := r.phone("1001")
	done := make(chan *testua.Call, 1)
	go func() { c, _ := r.carrier.Dial("6557777", nil, 10*time.Second, false); done <- c }()
	in, err := phone.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatalf("the linked DID did not ring 1001: %v", err)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-done:
		if c == nil || c.Final == nil || c.Final.StatusCode != 200 {
			t.Fatalf("carrier call: %+v", c)
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = c.Bye()
	case <-time.After(5 * time.Second):
		t.Fatal("carrier call not answered")
	}

	// Unlinked (the trash on the chip): back to Available DID Numbers.
	un := r.mustCall("PUT", d+"dids/"+id, map[string]any{"action": ""})
	if un["action"] != "" || un["gateway_uuid"] != gw["gateway_uuid"] {
		t.Fatalf("unlinked DID: %v", un)
	}
	if n := r.count(enabledPlans); n != 0 {
		t.Fatalf("unlinked DID dialplans enabled: %d", n)
	}
	// With its country (Singapore, +65) the number also answers without the
	// country code, as many carriers send it.
	sg := r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6588887777", "destination_prefix": "+65",
		"gateway_uuid": gw["gateway_uuid"], "action": "transfer:1001 XML " + domain})
	if sg["destination_prefix"] != "65" {
		t.Fatalf("DID country: %v", sg["destination_prefix"])
	}
	var bad map[string]any
	if c := r.call("POST", d+"dids", map[string]any{"destination_number": "6599990000", "destination_prefix": "SG"}, &bad); c != 400 {
		t.Fatalf("a country that is not a calling code was accepted: %d %v", c, bad)
	}
	for _, dialed := range []string{"88887777", "+6588887777"} {
		go func() { c, _ := r.carrier.Dial(dialed, nil, 10*time.Second, false); done <- c }()
		in, err := phone.WaitInvite(5 * time.Second)
		if err != nil {
			t.Fatalf("%s did not ring 1001: %v", dialed, err)
		}
		if err := in.Answer(0); err != nil {
			t.Fatal(err)
		}
		c := <-done
		if c == nil || c.Final == nil || c.Final.StatusCode != 200 {
			t.Fatalf("carrier call to %s: %+v", dialed, c)
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = c.Bye()
		time.Sleep(200 * time.Millisecond)
	}

	// A description edit leaves the action alone.
	r.mustCall("PUT", d+"dids/"+id, map[string]any{"action": "transfer:1001 XML " + domain})
	ed := r.mustCall("PUT", d+"dids/"+id, map[string]any{"destination_description": "Front desk"})
	if ed["action"] != "transfer:1001 XML "+domain || ed["destination_description"] != "Front desk" {
		t.Fatalf("edited DID: %v", ed)
	}
}
