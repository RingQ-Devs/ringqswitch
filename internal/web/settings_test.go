package web_test

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/web"
)

// TestSIPProfilesAndOutboundRules: a SIP profile added on the page opens
// its port at once; an outbound rule built from a prefix, a length and an
// extension range becomes the right dialplan; BLOCK rules need no gateway.
func TestSIPProfilesAndOutboundRules(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	// A free port outside the rig's audio range (20000-20998, below the
	// ports the system hands out), which the switch refuses for a SIP profile.
	port := 0
	for port == 0 || port >= 20000 && port <= 20998 {
		free, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		port = free.LocalAddr().(*net.UDPAddr).Port
		free.Close()
	}
	var out map[string]any
	if c := r.call("POST", "sipprofiles", map[string]any{"name": "external", "port": 80}, &out); c != 400 {
		t.Fatalf("port 80 accepted: %d %v", c, out)
	}
	if c := r.call("POST", "sipprofiles", map[string]any{"name": "rtp", "port": 20100}, &out); c != 400 {
		t.Fatalf("a port in the RTP range accepted: %d %v", c, out)
	}
	p := r.mustCall("POST", "sipprofiles", map[string]any{"name": "external", "port": port, "protocol": "udp", "ext_sip_ip": "198.51.100.7"})
	list := r.mustCall("GET", "sipprofiles", nil)
	var ext map[string]any
	for _, x := range list["profiles"].([]any) {
		if m := x.(map[string]any); m["name"] == "external" {
			ext = m
		}
	}
	if ext == nil || ext["listening"] != true || ext["port"] != float64(port) || ext["protocol"] != "udp" || ext["ext_sip_ip"] != "198.51.100.7" {
		t.Fatalf("profile: %v (all %v)", ext, list)
	}
	r.mustCall("DELETE", "sipprofiles/"+p["uuid"].(string), nil)
	if r.sw.SIPProfiles()[0].Name != "internal" || len(r.sw.SIPProfiles()) != 1 {
		t.Fatalf("after delete: %+v", r.sw.SIPProfiles())
	}

	d := "d/" + domain + "/"
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": "192.0.2.50", "register": "false", "enabled": "true"})
	if c := r.call("POST", d+"routes", map[string]any{"route_name": "", "route_prefix": "9", "gateway_uuids": gw["gateway_uuid"]}, &out); c != 400 {
		t.Fatalf("a rule without a name was accepted: %d", c)
	}
	if c := r.call("POST", d+"routes", map[string]any{"route_name": "No route", "route_prefix": "9"}, &out); c != 400 {
		t.Fatalf("a rule without a gateway was accepted: %d", c)
	}
	rule := r.mustCall("POST", d+"routes", map[string]any{"route_name": "Mobiles", "route_prefix": "9", "route_length": "8",
		"route_from_ranges": "1000-1001", "gateway_uuids": gw["gateway_uuid"], "route_prepend": "+65", "sip_from_user": "cid",
		"sip_from_host": "domain", "cid_type": "rpid"})
	r.mustCall("POST", d+"routes", map[string]any{"route_name": "Block 1900", "route_prefix": "1900", "route_block": true})
	var dps []map[string]any
	r.call("GET", d+"dialplans", nil, &dps)
	var xml string
	for _, x := range dps {
		if x["dialplan_name"] == "Mobiles" || x["dialplan_name"] == "Block 1900" {
			xml += x["dialplan_xml"].(string)
		}
	}
	for _, want := range []string{`expression="^\+?(9\d{7})$"`, `field="${user_name}" expression="^(?:100[0-1])$"`,
		`nolocal:sip_cid_type=rpid`, `nolocal:sip_from_host=` + domain, `/+65${destination_number}`, `^\+?(1900\d*)$`, `CALL_REJECTED`} {
		if !strings.Contains(xml, want) {
			t.Fatalf("dialplan lacks %s:\n%s", want, xml)
		}
	}
	got := r.mustCall("GET", d+"routes/"+rule["outbound_route_uuid"].(string), nil)
	if got["route_prefix"] != "9" || got["route_length"] != "8" || got["route_from_ranges"] != "1000-1001" || got["route_block"] != "false" {
		t.Fatalf("rule read back: %v", got)
	}
}

// TestTopBarAPIs: clearing the cache (owners only), the phones signed in
// as my extension, and RingQ's standard SIP profiles on request.
func TestTopBarAPIs(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	for ext, role := range map[string]string{"1001": "owner", "1002": "user"} {
		r.mustCall("PUT", d+"extensions/"+r.extensionUUID(ext), map[string]any{"web_role": role, "web_password": "Pass-" + ext + "-x"})
	}
	owner, user := r.session("1001", "Pass-1001-x"), r.session("1002", "Pass-1002-x")

	rep := owner.mustCall("POST", "cache/clear", nil)
	if rep["domains"].(float64) < 1 || rep["extensions"].(float64) < 2 || rep["dialplans"].(float64) < 1 {
		t.Fatalf("clear the cache: %v", rep)
	}
	var out map[string]any
	if c := user.call("POST", "cache/clear", nil, &out); c != 403 {
		t.Fatalf("a user cleared the cache: %d %v", c, out)
	}

	r.phone("1001")
	var devs []map[string]any
	if c := owner.call("GET", "devices", nil, &devs); c != 200 || len(devs) != 1 || devs[0]["user"] != "1001" {
		t.Fatalf("my devices: %d %v", c, devs)
	}
	if c := user.call("GET", "devices", nil, &devs); c != 200 || len(devs) != 0 {
		t.Fatalf("someone else's devices: %d %v", c, devs)
	}

	// The standard profiles: internal on the main port, external on 5080.
	if before := r.mustCall("GET", "sipprofiles", nil); before["in_database"] != float64(0) {
		t.Fatalf("profiles in a new database: %v", before)
	}
	if added := r.mustCall("POST", "sipprofiles/defaults", nil)["added"].([]any); len(added) != 2 {
		t.Fatalf("standard profiles: %v", added)
	}
	if again := r.mustCall("POST", "sipprofiles/defaults", nil)["added"].([]any); len(again) != 0 {
		t.Fatalf("standard profiles added twice: %v", again)
	}
	names := map[string]float64{}
	for _, x := range r.mustCall("GET", "sipprofiles", nil)["profiles"].([]any) {
		m := x.(map[string]any)
		names[m["name"].(string)] = m["port"].(float64)
	}
	if names["external"] != 5080 || names["internal"] != float64(r.sw.SIPPort()) {
		t.Fatalf("profiles: %v", names)
	}
}

// TestAgentFollowsPortalSignIn: signing out of the portal logs an agent out
// of its queues when no phone is left signed in (so the wallboard's
// "Logged In" drops); signing in puts it back. A desk phone keeps it in.
func TestAgentFollowsPortalSignIn(t *testing.T) {
	web.SetAgentLogoutDelay(100 * time.Millisecond)
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1001"), map[string]any{"web_role": "user", "web_password": "Pass-1001-x"})
	q := r.mustCall("POST", d+"queues", map[string]any{"queue_name": "Q", "queue_extension": "3001", "agents": []map[string]string{{"extension": "1001"}}})
	agent := q["agents"].([]any)[0].(map[string]any)["call_center_agent_uuid"].(string)
	r.mustCall("POST", "agents/"+agent+"/status", map[string]string{"status": "Available"})
	status := func() string {
		for _, a := range r.sw.Agents(domain) {
			if a.Extension == "1001" {
				return a.Status
			}
		}
		return ""
	}
	waitStatus := func(want string) {
		t.Helper()
		for i := 0; i < 50 && status() != want; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if got := status(); got != want {
			t.Fatalf("agent status %q, want %q", got, want)
		}
	}

	sess := r.session("1001", "Pass-1001-x")
	sess.mustCall("POST", "logout", nil)
	waitStatus("Logged Out")
	sess = r.session("1001", "Pass-1001-x")
	waitStatus("Available")

	// Signed in in two browsers: signing out of one keeps the agent in.
	other := r.session("1001", "Pass-1001-x")
	other.mustCall("POST", "logout", nil)
	time.Sleep(400 * time.Millisecond)
	if got := status(); got != "Available" {
		t.Fatalf("signing out of one of two browsers logged the agent out: %s", got)
	}

	// Logged out some other way (a supervisor, the dialer, before a
	// restart): signing in logs the agent in all the same.
	r.mustCall("POST", "agents/"+agent+"/status", map[string]string{"status": "Logged Out"})
	waitStatus("Logged Out")
	sess = r.session("1001", "Pass-1001-x")
	waitStatus("Available")

	// With a desk phone signed in, signing out of the portal changes nothing.
	r.phone("1001")
	sess.mustCall("POST", "logout", nil)
	time.Sleep(400 * time.Millisecond)
	if got := status(); got != "Available" {
		t.Fatalf("an agent with a desk phone was logged out: %s", got)
	}
}
