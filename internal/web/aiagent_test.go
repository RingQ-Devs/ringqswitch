package web_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

const testAIKey = "sk-test-0123456789abcdefghijklmnop"

// fakeRealtime stands in for the voice AI service's Realtime API: it greets with three
// seconds of tone, hears the caller (loud audio, then half a second of
// quiet), and answers "Sure, let me connect you" with a transfer_call.
type fakeRealtime struct {
	srv       *httptest.Server
	search    bool // answer the caller with search_knowledge instead
	mu        sync.Mutex
	model     string
	sessions  []map[string]any
	creates   []map[string]any
	truncates []float64
	outputs   []map[string]any
}

func newFakeRealtime(t *testing.T) *fakeRealtime {
	f := &fakeRealtime{}
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testAIKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`))
			return
		}
		f.mu.Lock()
		f.model = r.URL.Query().Get("model")
		f.mu.Unlock()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go f.serve(c)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRealtime) url() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/v1/realtime"
}

func tone(hz float64, d time.Duration) []byte {
	n := int(d.Seconds() * 8000)
	b := make([]byte, n)
	for i := range b {
		b[i] = media.LinearToUlaw(int16(8000 * math.Sin(2*math.Pi*hz*float64(i)/8000)))
	}
	return b
}

func (f *fakeRealtime) serve(c *websocket.Conn) {
	defer c.Close()
	var wmu sync.Mutex
	send := func(v map[string]any) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = c.WriteJSON(v)
	}
	n := 0
	speak := func(item string, audio []byte, transcript string, then ...map[string]any) {
		n++
		id := fmt.Sprintf("resp_%d", n)
		send(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress"}})
		for len(audio) > 0 { // faster than real time, like the service
			k := min(1600, len(audio))
			send(map[string]any{"type": "response.output_audio.delta", "response_id": id, "item_id": item, "delta": base64.StdEncoding.EncodeToString(audio[:k])})
			audio = audio[k:]
		}
		send(map[string]any{"type": "response.output_audio_transcript.done", "response_id": id, "item_id": item, "transcript": transcript})
		for _, e := range then {
			e["response_id"] = id
			send(e)
		}
		send(map[string]any{"type": "response.done", "response": map[string]any{"id": id, "status": "completed"}})
	}
	send(map[string]any{"type": "session.created"})
	loud, quiet, speaking := 0, 0, false
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m["type"] {
		case "session.update":
			f.mu.Lock()
			f.sessions = append(f.sessions, m["session"].(map[string]any))
			f.mu.Unlock()
			send(map[string]any{"type": "session.updated"})
		case "response.create":
			r, _ := m["response"].(map[string]any)
			instr, _ := r["instructions"].(string)
			f.mu.Lock()
			f.creates = append(f.creates, r)
			f.mu.Unlock()
			if _, said, ok := strings.Cut(instr, "word for word, and nothing else: "); ok {
				speak("item_greeting", tone(440, 3*time.Second), said)
			} else {
				speak(fmt.Sprintf("item_%d", n+1), tone(500, 300*time.Millisecond), "Okay.")
			}
		case "input_audio_buffer.append":
			b, _ := base64.StdEncoding.DecodeString(m["audio"].(string))
			for len(b) >= 160 {
				var sum float64
				for _, u := range b[:160] {
					v := float64(media.UlawToLinear(u))
					sum += v * v
				}
				b = b[160:]
				if math.Sqrt(sum/160) > 2000 {
					loud, quiet = loud+1, 0
				} else {
					quiet++
					if !speaking && quiet > 2 {
						loud = 0
					}
				}
				if !speaking && loud >= 5 {
					speaking = true
					send(map[string]any{"type": "input_audio_buffer.speech_started", "item_id": "item_caller"})
				}
				if speaking && quiet >= 25 {
					speaking, loud = false, 0
					send(map[string]any{"type": "input_audio_buffer.speech_stopped"})
					send(map[string]any{"type": "conversation.item.input_audio_transcription.completed", "item_id": "item_caller",
						"transcript": "Can I talk to sales please?"})
					if f.search {
						speak("item_fc", nil, "", map[string]any{"type": "response.function_call_arguments.done", "item_id": "item_fc",
							"call_id": "call_s", "name": "search_knowledge", "arguments": `{"query":"warranty years"}`})
						continue
					}
					speak("item_answer", tone(660, 300*time.Millisecond), "Sure, let me connect you.",
						map[string]any{"type": "response.function_call_arguments.done", "item_id": "item_fc", "call_id": "call_1",
							"name": "transfer_call", "arguments": `{"destination":"sales","reason":"asked for sales"}`})
				}
			}
		case "conversation.item.truncate":
			f.mu.Lock()
			f.truncates = append(f.truncates, m["audio_end_ms"].(float64))
			f.mu.Unlock()
		case "conversation.item.create":
			f.mu.Lock()
			f.outputs = append(f.outputs, m["item"].(map[string]any))
			f.mu.Unlock()
		}
	}
}

func rms(payload []byte) float64 {
	var sum float64
	for _, u := range payload {
		v := float64(media.UlawToLinear(u))
		sum += v * v
	}
	return math.Sqrt(sum / float64(max(1, len(payload))))
}

func TestAIAgentCall(t *testing.T) {
	r := newRig(t)
	fake := newFakeRealtime(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"

	// The service: a wrong key fails the connection test, the right one passes.
	var test map[string]any
	r.call("POST", d+"ai/settings/test", map[string]string{"api_key": "sk-wrong-0123456789abcdefghij", "base_url": fake.url()}, &test)
	if test["ok"] != false || !strings.Contains(fmt.Sprint(test["error"]), "Incorrect API key") {
		t.Fatalf("test with a wrong key: %v", test)
	}
	r.mustCall("PUT", d+"ai/settings", map[string]string{"api_key": testAIKey, "base_url": fake.url(), "model": "gpt-realtime"})
	var st map[string]any
	r.call("GET", d+"ai/settings", nil, &st)
	if st["has_key"] != true || strings.Contains(fmt.Sprint(st), testAIKey) || st["key_hint"] != "sk-…mnop" {
		t.Fatalf("settings: %v", st)
	}
	var enc string
	if err := r.conn.QueryRow(context.Background(), `select api_key_enc from rq_ai_settings`).Scan(&enc); err != nil || !strings.HasPrefix(enc, "v1:") ||
		strings.Contains(enc, "mnop") {
		t.Fatalf("stored key %q (%v)", enc, err)
	}
	test = nil
	r.call("POST", d+"ai/settings/test", map[string]string{}, &test)
	if test["ok"] != true {
		t.Fatalf("connection test: %v", test)
	}

	// The agent, reached from a DID.
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	ag := r.mustCall("POST", d+"aiagents", map[string]any{"agent_name": "RingQ Assistant", "agent_extension": "5000", "agent_voice": "cedar",
		"agent_languages": "en,ta", "agent_greetings": []string{"Hi there! Welcome to RingQ."}, "agent_prompt": "You help RingQ callers.",
		"knowledge_text": "Q: When does the office open?\nA: Our office opens at 9am.",
		"keywords":       []map[string]string{{"keyword": "sales", "destination": "transfer:1001 XML " + domain}}})
	agentID := ag["ai_agent_uuid"].(string)
	if kw, _ := ag["keywords"].([]any); len(kw) != 1 {
		t.Fatalf("keywords: %v", ag["keywords"])
	}
	if code := r.call("POST", d+"aiagents", map[string]any{"agent_name": "Clash", "agent_extension": "1001"}, nil); code != http.StatusBadRequest {
		t.Fatalf("an AI agent on an extension's number: %d", code)
	}
	var meta struct{ Destinations []map[string]string }
	r.call("GET", d+"meta", nil, &meta)
	offered := false
	for _, x := range meta.Destinations {
		offered = offered || x["group"] == "AI agent" && x["value"] == "transfer:5000 XML "+domain
	}
	if !offered {
		t.Fatalf("the AI agent is not offered as a destination: %v", meta.Destinations)
	}
	if n := r.count(`select count(*) from v_dialplans where dialplan_number = '5000' and dialplan_xml like '%application="ai_agent" data="` +
		agentID + `"%'`); n != 1 {
		t.Fatal("no dialplan for the AI agent's number")
	}
	r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6569140000", "gateway_uuid": gw["gateway_uuid"],
		"action": "transfer:5000 XML " + domain})
	p1 := r.phone("1001")

	call, err := r.carrier.Dial("6569140000", nil, 10*time.Second, false)
	if err != nil || call.Final == nil || call.Final.StatusCode != 200 {
		t.Fatalf("the AI agent did not answer: %v %+v", err, call)
	}
	// The greeting plays.
	if _, _, err := testua.RecvRTP(call.RTP, 5*time.Second, func(_ uint8, p []byte) bool { return rms(p) > 1500 }); err != nil {
		t.Fatal("no greeting heard:", err)
	}
	var loudHeard atomic.Int32
	stop := make(chan struct{})
	defer close(stop)
	go func() { // what the caller hears
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, p, err := testua.RecvRTP(call.RTP, 100*time.Millisecond, nil); err == nil && rms(p) > 1500 {
				loudHeard.Add(1)
			}
		}
	}()
	// The caller speaks over the greeting (600 ms), then waits.
	speech, quiet := tone(300, 600*time.Millisecond), make([]byte, 160)
	for i := range quiet {
		quiet[i] = 0xFF
	}
	go func() {
		seq := uint16(1)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			p := quiet
			if (i+1)*160 <= len(speech) {
				p = speech[i*160 : (i+1)*160]
			}
			_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, seq, uint32(seq)*160, p)
			seq++
		}
	}()
	in, err := p1.WaitInvite(10 * time.Second)
	if err != nil {
		t.Fatal("the agent did not connect the caller to sales (1001):", err)
	}
	_ = in.Answer()
	time.Sleep(300 * time.Millisecond)
	_, _ = call.Bye()

	fake.mu.Lock()
	sess, truncates, model := fake.sessions, fake.truncates, fake.model
	fake.mu.Unlock()
	// Speaking over the greeting stopped it.
	if len(truncates) == 0 || truncates[0] < 100 || truncates[0] > 2500 {
		t.Fatalf("barge-in: truncated at %v ms", truncates)
	}
	if n := loudHeard.Load(); n > 110 { // 3 s of greeting would be 150 frames
		t.Fatalf("the greeting kept playing over the caller (%d loud frames)", n)
	}
	if model != "gpt-realtime" || len(sess) != 2 { // the connection test, the call
		t.Fatalf("model %q, sessions %d", model, len(sess))
	}
	b, _ := json.Marshal(sess[1])
	js := string(b)
	for _, want := range []string{`"type":"realtime"`, `"format":{"type":"audio/pcmu"}`, `"voice":"cedar"`, `"near_field"`,
		"Our office opens at 9am", "English, Tamil", "You help RingQ callers.", `"name":"transfer_call"`, `"enum":["sales"]`,
		`"name":"end_call"`, `"server_vad"`, "gpt-4o-mini-transcribe"} {
		if !strings.Contains(js, want) {
			t.Errorf("session.update lacks %s: %s", want, js)
		}
	}
	if strings.Contains(js, "search_knowledge") {
		t.Error("search_knowledge offered although all knowledge is in the instructions")
	}

	// The call log.
	r.waitCount("AI call log", 1, `select count(*) from rq_ai_calls where outcome = 'transferred' and transferred_to = 'sales (1001)'
		and caller_number <> '' and destination_number = '6569140000' and turns = 1 and ended_at is not null`)
	var list struct {
		Rows  []map[string]any
		Total int
	}
	r.call("GET", d+"ai/calls?agent="+agentID, nil, &list)
	if list.Total != 1 || len(list.Rows) != 1 || list.Rows[0]["agent_name"] != "RingQ Assistant" || list.Rows[0]["transcript"] != nil {
		t.Fatalf("calls: %+v", list)
	}
	var one struct {
		Transcript []map[string]any
	}
	r.call("GET", d+"ai/calls/"+list.Rows[0]["ai_call_uuid"].(string), nil, &one)
	var said []string
	for _, tr := range one.Transcript {
		said = append(said, fmt.Sprint(tr["role"], ": ", tr["text"]))
	}
	all := strings.Join(said, " | ")
	for _, want := range []string{"agent: Hi there! Welcome to RingQ.", "caller: Can I talk to sales please?", "agent: Sure, let me connect you.",
		"event: Transferred to sales (1001)"} {
		if !strings.Contains(all, want) {
			t.Errorf("transcript lacks %q: %s", want, all)
		}
	}
	var status struct {
		Configured bool
		Agents     []map[string]any
	}
	r.call("GET", d+"ai/status", nil, &status)
	if !status.Configured || len(status.Agents) != 1 || fmt.Sprint(status.Agents[0]["calls"]) != "1" ||
		fmt.Sprint(status.Agents[0]["transferred"]) != "1" {
		t.Fatalf("status: %+v", status)
	}
}

// Without the service (no key) the agent's fallback takes the call.
func TestAIAgentFallback(t *testing.T) {
	t.Setenv("RQ_AI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	r.mustCall("POST", d+"aiagents", map[string]any{"agent_name": "Helper", "agent_extension": "5001",
		"fallback_action": "transfer:1001 XML " + domain})
	caller, p1 := r.phone("1000"), r.phone("1001")
	go func() { _, _ = caller.Dial("5001", nil, 10*time.Second, false) }()
	in, err := p1.WaitInvite(8 * time.Second)
	if err != nil {
		t.Fatal("the fallback did not ring 1001:", err)
	}
	_ = in.Reject(486)
	r.waitCount("failed AI call", 1, `select count(*) from rq_ai_calls where outcome = 'failed' and error like '%no API key%'`)
}

// The agent's web pages are fetched when it is saved and searched when the
// caller asks.
func TestAIAgentKnowledge(t *testing.T) {
	site := http.NewServeMux()
	site.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<html><head><title>Products</title></head><body><h1>Our phones</h1>`+
				strings.Repeat("<p>The desk phone has a colour screen and six lines for busy offices.</p>", 150)+
				`<a href="/warranty">Warranty</a></body></html>`)
		case "/warranty":
			fmt.Fprint(w, `<html><head><title>Warranty</title></head><body><p>Every phone's warranty lasts 5 years.</p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	})
	web := httptest.NewServer(site)
	defer web.Close()
	r := newRig(t)
	fake := newFakeRealtime(t)
	fake.search = true
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	r.mustCall("PUT", d+"ai/settings", map[string]string{"api_key": testAIKey, "base_url": fake.url()})
	ag := r.mustCall("POST", d+"aiagents", map[string]any{"agent_name": "Shop", "agent_extension": "5002", "knowledge_urls": web.URL + "/"})
	id := ag["ai_agent_uuid"].(string)
	var kn struct {
		Pages  []map[string]any
		Status map[string]any
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.call("GET", d+"ai/knowledge/"+id, nil, &kn)
		if kn.Status["running"] == false && len(kn.Pages) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("knowledge: %+v", kn)
		}
		time.Sleep(100 * time.Millisecond)
	}

	caller := r.phone("1000")
	call, err := caller.Dial("5002", nil, 10*time.Second, false)
	if err != nil || call.Final == nil || call.Final.StatusCode != 200 {
		t.Fatalf("the AI agent did not answer: %v %+v", err, call)
	}
	defer call.Bye() //nolint:errcheck
	speech := tone(300, 400*time.Millisecond)
	for i := 0; i < 60; i++ { // speech, then quiet
		p := []byte(strings.Repeat("\xff", 160))
		if (i+1)*160 <= len(speech) {
			p = speech[i*160 : (i+1)*160]
		}
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, uint16(i+1), uint32(i+1)*160, p)
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		fake.mu.Lock()
		outs, sess := fake.outputs, fake.sessions
		fake.mu.Unlock()
		if len(outs) > 0 {
			if out := fmt.Sprint(outs[0]["output"]); outs[0]["call_id"] != "call_s" || !strings.Contains(out, "warranty lasts 5 years") {
				t.Fatalf("search result: %v", outs[0])
			}
			if b, _ := json.Marshal(sess[0]); !strings.Contains(string(b), `"name":"search_knowledge"`) {
				t.Fatalf("no search tool: %s", b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent's search was not answered")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
