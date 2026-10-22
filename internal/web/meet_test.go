package web_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// meetConn is one participant's meeting WebSocket.
type meetConn struct {
	t  *testing.T
	ws *websocket.Conn
}

func (r *rig) meetWS(path string, jar http.CookieJar) *meetConn {
	r.t.Helper()
	u, _ := url.Parse(r.base)
	origin := u.Scheme + "://" + u.Host
	d := websocket.Dialer{Jar: jar, HandshakeTimeout: 5 * time.Second}
	ws, resp, err := d.Dial(strings.Replace(origin, "http", "ws", 1)+"/api/"+path, http.Header{"Origin": {origin}})
	if err != nil {
		body := ""
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			body = string(b)
		}
		r.t.Fatalf("websocket %s: %v %s", path, err, body)
	}
	r.t.Cleanup(func() { ws.Close() })
	return &meetConn{t: r.t, ws: ws}
}

func (m *meetConn) send(msg map[string]any) {
	m.t.Helper()
	if err := m.ws.WriteJSON(msg); err != nil {
		m.t.Fatal(err)
	}
}

// expect reads messages until one of type typ arrives.
func (m *meetConn) expect(typ string) map[string]any {
	m.t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for {
		_ = m.ws.SetReadDeadline(deadline)
		var msg map[string]any
		if err := m.ws.ReadJSON(&msg); err != nil {
			m.t.Fatalf("waiting for %s: %v", typ, err)
		}
		if msg["t"] == typ {
			return msg
		}
		if msg["t"] == "error" {
			m.t.Fatalf("waiting for %s: error %v", typ, msg["error"])
		}
	}
}

// expectLobby reads waiting room lists until one with n people arrives.
func (m *meetConn) expectLobby(n int) []any {
	m.t.Helper()
	for {
		if l, _ := m.expect("meet.lobby")["waiting"].([]any); len(l) == n {
			return l
		}
	}
}

// TestMeetingContent: a host and a guest in a meeting: transcription,
// presenting a file (only with a participant's token), the whiteboard,
// the waiting room switch, and the history left when the meeting ends.
func TestMeetingContent(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	for ext, role := range map[string]string{"1001": "owner", "1002": "user"} {
		r.mustCall("PUT", d+"extensions/"+r.extensionUUID(ext), map[string]any{"web_role": role, "web_password": "Pass-" + ext + "-x"})
	}
	owner, user := r.session("1001", "Pass-1001-x"), r.session("1002", "Pass-1002-x")
	m := owner.mustCall("POST", "meetings", map[string]any{"title": "Planning", "instant": true})
	code := m["code"].(string)
	if len(code) != 14 || m["link"] != "/meet/m/"+code {
		t.Fatalf("code and link: %v", m)
	}
	var planned []map[string]any
	owner.call("GET", "meetings", nil, &planned)
	if len(planned) != 0 {
		t.Fatalf("an instant meeting is not a planned one: %v", planned)
	}
	// The meeting page answers on its link.
	resp, err := http.Get(strings.TrimSuffix(r.base, "/api/") + "/meet/m/" + code)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("meeting page: %v %v", err, resp)
	}
	resp.Body.Close()

	host := r.meetWS("ws", owner.client.Jar)
	host.send(map[string]any{"t": "meet.join", "room": code})
	joined := host.expect("meet.joined")
	hostToken := joined["token"].(string)
	host.expect("meet.admitted")

	guest := r.meetWS("guest/ws?code="+code+"&name=Ravi+Tan&email=ravi%40example.com", nil)
	guest.send(map[string]any{"t": "meet.join", "room": code})
	gj := guest.expect("meet.joined")
	guestToken := gj["token"].(string)
	// The waiting room is on by default: the organizer lets the guest in.
	guest.expect("meet.waiting")
	if l := host.expectLobby(1); l[0].(map[string]any)["name"] != "Ravi Tan" {
		t.Fatalf("waiting room: %v", l)
	}
	host.send(map[string]any{"t": "meet.admit", "peer": gj["me"]})
	guest.expect("meet.admitted")

	// Transcription: the guest's browser sends its lines.
	host.send(map[string]any{"t": "meet.transcribe", "on": true})
	guest.expect("meet.transcribe")
	guest.send(map[string]any{"t": "meet.caption", "body": "let us start with the numbers", "on": true})
	if c := host.expect("meet.caption"); c["final"] != true || c["line"].(map[string]any)["name"] != "Ravi Tan" {
		t.Fatalf("caption: %v", c)
	}

	// Present a picture: upload with the host's token, the guest fetches it with its own.
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	upload := func(token, name string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", r.base+"guest/file?token="+token+"&name="+name, bytes.NewReader(png))
		req.Header.Set("X-RQ", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if c, _ := upload("not-a-token", "slide.png"); c != 403 {
		t.Fatalf("upload without a token: %d", c)
	}
	if c, _ := upload(hostToken, "slide.exe"); c != 400 {
		t.Fatalf("a program was accepted for presenting: %d", c)
	}
	c, f := upload(hostToken, "slide.png")
	if c != 200 {
		t.Fatalf("upload: %d %v", c, f)
	}
	host.send(map[string]any{"t": "meet.present", "data": map[string]any{"file": f["id"], "page": 1}})
	p := guest.expect("meet.present")["present"].(map[string]any)
	if p["file"] != f["id"] || p["type"] != "image/png" || p["title"] != "slide.png" {
		t.Fatalf("present: %v", p)
	}
	get := func(token string) *http.Response {
		resp, err := http.Get(r.base + "guest/file/" + f["id"].(string) + "?token=" + token)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if g := get(guestToken); g.StatusCode != 200 || g.Header.Get("Content-Type") != "image/png" || g.Header.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("guest download: %d %v", g.StatusCode, g.Header)
	}
	if g := get("x" + guestToken); g.StatusCode != 403 {
		t.Fatalf("download with a wrong token: %d", g.StatusCode)
	}

	// Whiteboard.
	host.send(map[string]any{"t": "meet.board", "on": true})
	guest.expect("meet.board")
	host.send(map[string]any{"t": "meet.draw", "data": map[string]any{"c": "#1f2937", "w": 3, "p": []int{10, 10, 500, 500}}})
	if s := guest.expect("meet.draw")["stroke"].(map[string]any); s["c"] != "#1f2937" {
		t.Fatalf("stroke: %v", s)
	}

	// Only hosts switch the waiting room.
	guest.send(map[string]any{"t": "meet.lobby", "on": true})
	if e := guest.expect("error"); !strings.Contains(e["error"].(string), "host") {
		t.Fatalf("a guest switched the waiting room: %v", e)
	}
	host.send(map[string]any{"t": "meet.lobby", "on": true})
	for {
		if pm := guest.expect("meet.peers"); pm["lobby"] == true {
			break
		}
	}

	// When the last one leaves, the meeting's content is kept for its members.
	guest.send(map[string]any{"t": "meet.leave"})
	host.send(map[string]any{"t": "meet.leave"})
	var hist []map[string]any
	for i := 0; i < 50 && len(hist) == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		owner.call("GET", "meetings/history", nil, &hist)
	}
	if len(hist) != 1 || hist[0]["title"] != "Planning" || hist[0]["transcript_count"] != float64(1) {
		t.Fatalf("history: %v", hist)
	}
	detail := owner.mustCall("GET", "meetings/history/"+hist[0]["uuid"].(string), nil)
	people, _ := json.Marshal(detail["participants"])
	lines, _ := json.Marshal(detail["transcript"])
	files, _ := json.Marshal(detail["files"])
	if !strings.Contains(string(people), "ravi@example.com") || !strings.Contains(string(lines), "numbers") || !strings.Contains(string(files), "slide.png") {
		t.Fatalf("history detail: %s %s %s", people, lines, files)
	}
	var none []map[string]any
	user.call("GET", "meetings/history", nil, &none)
	if len(none) != 0 {
		t.Fatalf("someone who was not there sees the meeting: %v", none)
	}
}

// TestMeetingOrganizerAdmits: only the organizer is the host; everybody
// else, colleagues and owners included, waits until the organizer is there
// and lets them in.
func TestMeetingOrganizerAdmits(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	for ext, role := range map[string]string{"1001": "owner", "1002": "user"} {
		r.mustCall("PUT", d+"extensions/"+r.extensionUUID(ext), map[string]any{"web_role": role, "web_password": "Pass-" + ext + "-x"})
	}
	owner, organizer := r.session("1001", "Pass-1001-x"), r.session("1002", "Pass-1002-x")
	code := organizer.mustCall("POST", "meetings", map[string]any{"title": "Weekly", "instant": true})["code"].(string)
	if info := organizer.mustCall("GET", "guest/meeting/"+code, nil); info["lobby"] != true || info["organizer"] != true {
		t.Fatalf("meeting info for the organizer: %v", info)
	}
	if info := owner.mustCall("GET", "guest/meeting/"+code, nil); info["organizer"] != false {
		t.Fatalf("meeting info for a colleague: %v", info)
	}

	// An owner who did not make the meeting waits, even before the organizer is there.
	colleague := r.meetWS("ws", owner.client.Jar)
	colleague.send(map[string]any{"t": "meet.join", "room": code})
	cj := colleague.expect("meet.joined")
	if cj["host"] != false || cj["waiting"] != true {
		t.Fatalf("an owner is host of someone else's meeting: %v", cj)
	}
	if w := colleague.expect("meet.waiting"); !strings.Contains(w["message"].(string), "not joined yet") {
		t.Fatalf("waiting message: %v", w)
	}

	host := r.meetWS("ws", organizer.client.Jar)
	host.send(map[string]any{"t": "meet.join", "room": code})
	if hj := host.expect("meet.joined"); hj["host"] != true || hj["waiting"] != false {
		t.Fatalf("the organizer: %v", hj)
	}
	host.expect("meet.admitted")
	host.expectLobby(1)
	// A colleague cannot let people in.
	colleague.send(map[string]any{"t": "meet.admit", "peer": cj["me"]})
	colleague.expect("error")
	host.send(map[string]any{"t": "meet.admit", "peer": cj["me"]})
	colleague.expect("meet.admitted")
}
