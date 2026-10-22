package web_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
	"github.com/Cal4Care-Developers/RingQswitch/internal/webphone"
)

// session is a second browser (its own cookies).
func (r *rig) session(user, pass string) *rig {
	r.t.Helper()
	jar, _ := cookiejar.New(nil)
	s := *r
	s.client = &http.Client{Jar: jar, Timeout: 10 * time.Second}
	if code := s.call("POST", "login", map[string]string{"user": user, "password": pass}, nil); code != 200 {
		r.t.Fatalf("login %s: HTTP %d", user, code)
	}
	return &s
}

func (r *rig) extensionUUID(ext string) string {
	r.t.Helper()
	var rows []map[string]any
	r.call("GET", "d/"+domain+"/extensions", nil, &rows)
	for _, x := range rows {
		if x["extension"] == ext {
			return x["extension_uuid"].(string)
		}
	}
	r.t.Fatalf("extension %s not found", ext)
	return ""
}

func TestRolesChatSecurityMeetings(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	for ext, role := range map[string]string{"1001": "owner", "1002": "user"} {
		x := r.mustCall("PUT", d+"extensions/"+r.extensionUUID(ext), map[string]any{"web_role": role, "web_password": "Pass-" + ext + "-x"})
		// Owners see the web password (kept encrypted with the server's key).
		if x["web_role"] != role || x["web_password_set"] != "true" || x["web_password"] != "Pass-"+ext+"-x" {
			t.Fatalf("web access of %s: %v", ext, x)
		}
	}
	var out map[string]any
	if code := r.call("PUT", d+"extensions/"+r.extensionUUID("1000"), map[string]any{"web_role": "user", "web_password": "short"}, &out); code != 400 {
		t.Fatalf("short web password accepted: %d", code)
	}
	owner, user := r.session("1001", "Pass-1001-x"), r.session("1002@"+domain, "Pass-1002-x")

	// Users: no configuration pages, their own calls only.
	if code := user.call("GET", d+"extensions", nil, &out); code != 403 {
		t.Fatalf("user listed extensions: %d", code)
	}
	if code := user.call("GET", "security", nil, &out); code != 403 {
		t.Fatalf("user opened security: %d", code)
	}
	if code := user.call("GET", d+"cdr", nil, &out); code != 200 {
		t.Fatalf("user call history: %d", code)
	}
	if code := owner.call("GET", d+"extensions", nil, nil); code != 200 {
		t.Fatalf("owner listed extensions: %d", code)
	}
	var me map[string]any
	user.call("GET", "me", nil, &me)
	if me["role"] != "user" || me["extension"] != "1002" || me["server_access"] != false {
		t.Fatalf("me: %v", me)
	}
	// Agents: a queue makes 1001 and 1002 agents; a user changes only their own status.
	q := owner.mustCall("POST", d+"queues", map[string]any{"queue_name": "Q", "queue_extension": "3001",
		"agents": []map[string]string{{"extension": "1001"}, {"extension": "1002"}}})
	agents := map[string]string{}
	for _, a := range q["agents"].([]any) {
		m := a.(map[string]any)
		agents[m["extension"].(string)] = m["call_center_agent_uuid"].(string)
	}
	if len(agents) != 2 {
		t.Fatalf("queue members: %v", q["agents"])
	}
	if code := user.call("POST", "agents/"+agents["1001"]+"/status", map[string]string{"status": "Available"}, &out); code != 403 {
		t.Fatalf("user changed another agent: %d", code)
	}
	user.mustCall("POST", "agents/"+agents["1002"]+"/status", map[string]string{"status": "On Break", "aux": "Lunch"})
	// The extension form shows the agent.
	ext := owner.mustCall("GET", d+"extensions/"+r.extensionUUID("1002"), nil)
	if ext["agent_enabled"] != "true" {
		t.Fatalf("1002 is not shown as an agent: %v", ext["agent_enabled"])
	}

	// Password change.
	if code := user.call("POST", "password", map[string]string{"current": "wrong", "new": "New-Pass-1002"}, &out); code != 400 {
		t.Fatalf("password changed with a wrong current password: %d", code)
	}
	user.mustCall("POST", "password", map[string]string{"current": "Pass-1002-x", "new": "New-Pass-1002"})
	user = r.session("1002", "New-Pass-1002")

	// Chat.
	room := owner.mustCall("POST", "chat/direct", map[string]string{"extension": "1002"})["room"].(string)
	owner.mustCall("POST", "chat/rooms/"+room+"/messages", map[string]string{"body": "hello 1002"})
	var rooms []map[string]any
	user.call("GET", "chat/rooms", nil, &rooms)
	if len(rooms) != 1 || rooms[0]["unread"] != float64(1) {
		t.Fatalf("rooms of 1002: %v", rooms)
	}
	req, _ := http.NewRequest("POST", owner.base+"chat/rooms/"+room+"/files?name=note.txt", bytes.NewReader([]byte("file body")))
	req.Header.Set("X-RQ", "1")
	resp, err := owner.client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("file upload: %v %v", resp, err)
	}
	var fileMsg map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&fileMsg)
	resp.Body.Close()
	resp, err = user.client.Get(user.base + "chat/files/" + fileMsg["uuid"].(string))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("file download: %v %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "file body" || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("file: %q %v", body, resp.Header)
	}
	var msgs []map[string]any
	user.call("GET", "chat/rooms/"+room+"/messages", nil, &msgs)
	if len(msgs) != 2 || msgs[0]["body"] != "hello 1002" {
		t.Fatalf("messages: %v", msgs)
	}
	if code := r.call("GET", "chat/rooms", nil, &out); code != 400 {
		t.Fatalf("the administrator (no extension) used chat: %d", code)
	}

	// Meetings.
	m := owner.mustCall("POST", "meetings", map[string]any{"title": "Weekly", "passcode": "4321", "lobby": true})
	code := m["code"].(string)
	anon := &rig{t: t, base: r.base, client: &http.Client{Timeout: 5 * time.Second}}
	info := anon.mustCall("GET", "guest/meeting/"+code, nil)
	if info["title"] != "Weekly" || info["passcode"] != true || info["lobby"] != true {
		t.Fatalf("guest meeting info: %v", info)
	}
	if c := user.call("DELETE", "meetings/"+code, nil, &out); c != 403 {
		t.Fatalf("a user deleted someone else's meeting: %d", c)
	}
	owner.mustCall("DELETE", "meetings/"+code, nil)

	// Security rules.
	owner.mustCall("POST", "security/rules", map[string]string{"type": "ip-deny", "value": "192.0.2.66", "description": "test"})
	if c := owner.call("POST", "security/rules", map[string]string{"type": "ip-deny", "value": "10.0.0.0/4"}, &out); c != 400 {
		t.Fatalf("huge deny network accepted: %d", c)
	}
	if deny := r.sw.Config().Security.Deny; len(deny) != 1 || deny[0].String() != "192.0.2.66/32" {
		t.Fatalf("deny rules not loaded: %v", deny)
	}
	sec := owner.mustCall("GET", "security", nil)
	if len(sec["rules"].([]any)) < 2 { // the default scanner rules too
		t.Fatalf("security rules: %v", sec["rules"])
	}
	// A scanner: no answer, and its address is blocked.
	owner.mustCall("POST", "security/rules", map[string]string{"type": "ua-deny", "value": "testua"})
	scanner, err := testua.NewAt("100", "x", r.sipAddr, netip.MustParseAddr("127.0.0.3"))
	if err != nil {
		t.Skip("127.0.0.3:", err)
	}
	defer scanner.Close()
	if resp, err := scanner.Register(60); err == nil {
		t.Fatalf("scanner got an answer: %v", resp)
	}
	if _, ok := r.sw.Blocked()["127.0.0.3"]; !ok {
		t.Fatalf("scanner not blocked: %v", r.sw.Blocked())
	}
}

// phoneEvents records what the dialer's SIP phone reports.
type phoneEvents struct {
	mu    sync.Mutex
	reg   chan bool
	calls chan webphone.CallInfo
	audio int
}

func (p *phoneEvents) Registration(ok bool, _ string) {
	select {
	case p.reg <- ok:
	default:
	}
}
func (p *phoneEvents) CallUpdate(c webphone.CallInfo) { p.calls <- c }
func (p *phoneEvents) Audio(string, []byte) {
	p.mu.Lock()
	p.audio++
	p.mu.Unlock()
}
func (p *phoneEvents) MWI(int, int) {}

func (p *phoneEvents) wait(t *testing.T, state string) webphone.CallInfo {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case c := <-p.calls:
			if c.State == state {
				return c
			}
		case <-deadline:
			t.Fatalf("dialer: no %s call", state)
		}
	}
}

// TestDialerSIPPhone drives the browser dialer's SIP side against the
// switch: registration, incoming and outgoing calls, audio both ways, hold.
func TestDialerSIPPhone(t *testing.T) {
	r := newRig(t)
	ev := &phoneEvents{reg: make(chan bool, 4), calls: make(chan webphone.CallInfo, 32)}
	lo := netip.MustParseAddr("127.0.0.1")
	ua, err := webphone.Start(webphone.Config{User: "1001", Domain: domain, Server: r.sipAddr, LocalIP: lo, UserAgent: "test dialer",
		Password: func() string { return password }}, ev)
	if err != nil {
		t.Fatal(err)
	}
	defer ua.Close()
	select {
	case ok := <-ev.reg:
		if !ok {
			t.Fatal("dialer registration failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dialer did not register")
	}
	desk := r.phone("1002")

	// 1. 1002 calls the dialer; it answers; audio both ways.
	done := make(chan *testua.Call, 1)
	go func() { c, _ := desk.Dial("1001", nil, 10*time.Second, false); done <- c }()
	in := ev.wait(t, webphone.StateRinging)
	if in.Direction != "in" || in.Number != "1002" {
		t.Fatalf("incoming: %+v", in)
	}
	if err := ua.Answer(in.ID); err != nil {
		t.Fatal(err)
	}
	call := <-done
	if call == nil || call.Final == nil || call.Final.StatusCode != 200 {
		t.Fatalf("desk call: %+v", call)
	}
	for i := 0; i < 20; i++ {
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, uint16(i), uint32(i*160), bytes.Repeat([]byte{0x55}, 160))
		ua.SendAudio(in.ID, bytes.Repeat([]byte{0x7f}, 160))
		time.Sleep(20 * time.Millisecond)
	}
	if _, payload, err := testua.RecvRTP(call.RTP, 2*time.Second, nil); err != nil || len(payload) != 160 {
		t.Fatalf("desk got no audio from the dialer: %v", err)
	}
	ev.mu.Lock()
	got := ev.audio
	ev.mu.Unlock()
	if got == 0 {
		t.Fatal("the dialer got no audio from the desk phone")
	}
	if err := ua.Hold(in.ID, true); err != nil {
		t.Fatal(err)
	}
	for c := ev.wait(t, webphone.StateAnswered); !c.Held; c = ev.wait(t, webphone.StateAnswered) {
	}
	if err := ua.Hangup(in.ID); err != nil {
		t.Fatal(err)
	}
	ev.wait(t, webphone.StateEnded)

	// 2. The dialer calls 1002, which answers and hangs up.
	id, err := ua.Dial("1002")
	if err != nil {
		t.Fatal(err)
	}
	inc, err := desk.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := inc.Answer(); err != nil {
		t.Fatal(err)
	}
	if c := ev.wait(t, webphone.StateAnswered); c.ID != id {
		t.Fatalf("answered call: %+v", c)
	}
	if _, err := inc.Dialog().Bye(); err != nil {
		t.Fatal(err)
	}
	if c := ev.wait(t, webphone.StateEnded); c.ID != id {
		t.Fatalf("ended call: %+v", c)
	}
}
