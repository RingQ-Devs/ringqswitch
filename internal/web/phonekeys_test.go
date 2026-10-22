package web_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

func TestPhoneKeys(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"

	// The desk phone codes are in the dialplans.
	for _, code := range []string{"*78", "*79", "*77", "*69", "*870", "*591", "*26", "*88", "*8[ext]"} {
		if n := r.count(`select count(*) from v_dialplans where dialplan_number = $1`, code); n != 1 {
			t.Errorf("dial code %s: %d dialplans", code, n)
		}
	}

	var got struct {
		Keys []map[string]string
		Aux  []string
	}
	r.call("GET", d+"phonekeys/1001", nil, &got)
	if len(got.Keys) != 0 || len(got.Aux) < 2 || got.Aux[0] != "Available" {
		t.Fatalf("empty keys: %+v", got)
	}
	lunch := got.Aux[1]
	if code := r.call("PUT", d+"phonekeys/1001", map[string]any{"keys": []map[string]string{{"type": "park", "value": "9"}}}, nil); code != 400 {
		t.Fatalf("parking slot 9: %d", code)
	}
	r.mustCall("PUT", d+"phonekeys/1001", map[string]any{"keys": []map[string]string{
		{"type": "blf", "value": "1002"}, {"type": "Shared Parking", "value": "Parking Slot 2"}, {"type": "agent_status", "value": lunch},
		{"type": "change_status", "value": "in"}, {"type": "speed_dial", "value": "6591234567"}, {"type": "blank"},
	}})
	var types, values string
	if err := r.conn.QueryRow(context.Background(), `select types, "values" from q_blfs where extension = '1001'`).Scan(&types, &values); err != nil {
		t.Fatal(err)
	}
	if types != `["blf","park","agent_status","change_status","speed_dial","blank"]` || values != fmt.Sprintf(`["1002","2","%s","IN","6591234567",""]`, lunch) {
		t.Fatalf("stored keys: %s %s", types, values)
	}
	// Saving again keeps one row set.
	r.mustCall("PUT", d+"phonekeys/1001", map[string]any{"keys": []map[string]string{{"type": "blf", "value": "1002"}, {"type": "park", "value": "2"}}})
	if n := r.count(`select count(*) from q_blfs where extension = '1001'`); n != 1 {
		t.Fatalf("q_blfs rows: %d", n)
	}
	r.mustCall("PUT", d+"phonekeys", map[string]any{"keys": []map[string]string{{"type": "speed_dial", "value": "1000"}}, "show_dialer": true})

	// The dialer of 1001: its keys and the company's with their lamps.
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1001"), map[string]any{"web_role": "user", "web_password": "Pass-1001-x"})
	u := r.session("1001", "Pass-1001-x")
	var keys struct {
		Keys  []map[string]any
		Parks []map[string]any
		Dnd   bool
	}
	u.call("GET", "keys", nil, &keys)
	if len(keys.Keys) != 3 || keys.Keys[0]["label"] != "1002" || keys.Keys[0]["state"] != "idle" || keys.Keys[1]["dial"] != "*592" ||
		keys.Keys[2]["group"] != "company" || len(keys.Parks) != 5 || keys.Parks[0]["dial"] != "*591" {
		t.Fatalf("dialer keys: %+v", keys)
	}
	// 1000 calls 1002: the BLF key of 1002 rings.
	p0, p2 := r.phone("1000"), r.phone("1002")
	done := make(chan *testua.Call, 1)
	go func() { c, _ := p0.Dial("1002", nil, 10*time.Second, false); done <- c }()
	in, err := p2.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in.Ring()
	deadline := time.Now().Add(5 * time.Second)
	for {
		u.call("GET", "keys", nil, &keys)
		if keys.Keys[0]["state"] == "ringing" && keys.Keys[0]["number"] == "1000" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the BLF key did not ring: %+v", keys.Keys[0])
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = in.Reject(486)
	<-done

	// DND from the dialer applies at once (and the phones' DND keys).
	u.mustCall("POST", "dnd", map[string]bool{"on": true})
	if v := r.sw.UserVar("1001", domain, "do_not_disturb"); v != "true" {
		t.Fatalf("DND in the switch: %q", v)
	}
	// *79 from the phone turns it off; the database follows.
	p1 := r.phone("1001")
	if c, err := p1.Dial("*79", nil, 5*time.Second, false); err != nil || c.Final.StatusCode != 200 {
		t.Fatalf("*79: %+v %v", c, err)
	} else {
		time.Sleep(300 * time.Millisecond)
		_, _ = c.Bye()
	}
	r.waitCount("DND off saved", 1, `select count(*) from v_extensions where extension = '1001' and do_not_disturb = 'false'`)
	u.call("GET", "keys", nil, &keys)
	if keys.Dnd {
		t.Fatal("the dialer still shows DND")
	}
	var dir []map[string]any
	u.call("GET", d+"directory", nil, &dir)
	for _, e := range dir {
		if e["extension"] == "1001" && e["dnd"] != false {
			t.Fatalf("directory DND: %v", e)
		}
	}

	// *26N with the numbers of the Aux codes page.
	a := r.mustCall("POST", d+"agents", map[string]any{"agent_name": "Agent 1001", "extension": "1001"})
	var aux []map[string]any
	r.call("GET", d+"auxcodes", nil, &aux)
	dial := ""
	for _, x := range aux {
		if x["aux_code_name"] == lunch {
			dial, _ = x["dial"].(string)
		}
	}
	if dial != "*262" {
		t.Fatalf("Aux codes page: %s dials %q (%v)", lunch, dial, aux)
	}
	if c, err := p1.Dial(dial, nil, 5*time.Second, false); err != nil || c.Final.StatusCode != 200 {
		t.Fatalf("%s: %+v %v", dial, c, err)
	} else {
		time.Sleep(300 * time.Millisecond)
		_, _ = c.Bye()
	}
	agentID := a["call_center_agent_uuid"].(string)
	deadline = time.Now().Add(5 * time.Second)
	for {
		ok := false
		for _, x := range r.sw.Agents(domain) {
			ok = ok || x.UUID == agentID && x.Status == "On Break" && strings.EqualFold(x.Aux, lunch)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("*262 did not set %s: %+v", lunch, r.sw.Agents(domain))
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Reboot: check-sync to the registered phone.
	var rb map[string]int
	r.call("POST", d+"phonekeys/1001/reboot", nil, &rb)
	if rb["phones"] != 1 {
		t.Fatalf("reboot: %v", rb)
	}
	m, err := p1.WaitRequest("NOTIFY", 3*time.Second)
	for err == nil && m.Get("Event") != "check-sync;reboot=true" {
		_ = p1.Respond(m, 200, "", nil, "")
		m, err = p1.WaitRequest("NOTIFY", 3*time.Second)
	}
	if err != nil {
		t.Fatal("no check-sync NOTIFY:", err)
	}
	_ = p1.Respond(m, 200, "", nil, "")
}
