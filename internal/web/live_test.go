package web_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// TestLiveTranscription: with live transcription on, an extension whose
// web page is open sees its call written down as it goes on: who speaks,
// then what they said, split by side, then the end of the call.
func TestLiveTranscription(t *testing.T) {
	dir := t.TempDir()
	cmd, model := filepath.Join(dir, "whisper-cli"), filepath.Join(dir, "ggml-base.bin")
	if err := os.WriteFile(cmd, []byte(fakeRecogniser), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stand-in for the live recogniser: it hears the caller in phase 1, then 1001.
	var phase atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/inference" {
			http.NotFound(w, req)
			return
		}
		f, _, err := req.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		wav, _ := io.ReadAll(f)
		// The domain's language is given: no detection pass.
		if !bytes.HasPrefix(wav, []byte("RIFF")) || len(wav) < 44+8000 || req.FormValue("language") != "en" || req.FormValue("no_language_probabilities") != "true" ||
			req.FormValue("response_format") != "verbose_json" || req.FormValue("audio_ctx") == "" || req.FormValue("temperature_inc") != "0" {
			http.Error(w, "bad request", 400)
			return
		}
		text := " Hi, I would like to book a table for two tonight."
		if phase.Load() > 1 {
			text = " Of course, what time would you like?"
		}
		// With a guess it is unsure of: left out.
		json.NewEncoder(w).Encode(map[string]any{"text": text, "language": "english", "segments": []map[string]any{
			{"text": text, "avg_logprob": -0.2, "no_speech_prob": 0.01},
			{"text": " Je suis très très.", "avg_logprob": -1.6, "no_speech_prob": 0.4}}})
	}))
	defer server.Close()
	r := newRigWith(t, `<param name="transcribe-command" value="`+cmd+`"/><param name="transcribe-model" value="`+model+`"/>
    <param name="transcribe-live-url" value="`+server.URL+`"/>`)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	st := r.mustCall("GET", d+"transcription", nil)
	if st["live_server"] != true || st["live_model_in_use"] != "base" || st["settings"].(map[string]any)["live"] != false {
		t.Fatalf("settings: %v", st)
	}
	var out map[string]any
	if code := r.call("PUT", d+"transcription", map[string]any{"live": true, "live_model": "small"}, &out); code != 400 {
		t.Fatalf("a live model that is not installed: HTTP %d %v", code, out)
	}
	r.mustCall("PUT", d+"transcription", map[string]any{"live": true, "language": "en"})
	if st := r.mustCall("GET", d+"transcription", nil); st["settings"].(map[string]any)["live"] != true {
		t.Fatalf("live not saved: %v", st["settings"])
	}
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6531590078", "gateway_uuid": gw["gateway_uuid"],
		"action": "transfer:1001 XML " + domain})
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1001"), map[string]any{"web_role": "user", "web_password": "Pass-1001-x"})
	page := r.meetWS("ws", r.session("1001", "Pass-1001-x").client.Jar)
	agent := r.phone("1001")

	done := make(chan *testua.Call, 1)
	go func() { c, _ := r.carrier.Dial("6531590078", []int{0}, 10*time.Second, false); done <- c }()
	in, err := agent.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	var call *testua.Call
	select {
	case call = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the call was not answered")
	}
	start := page.expect("live.start")
	sides, _ := start["sides"].(map[string]any)
	id, _ := start["id"].(string)
	if start["me"] != "b" || id == "" || sides == nil || !strings.Contains(jsonText(sides["b"]), "1001") {
		t.Fatalf("live.start: %v", start)
	}

	side := in.Dialog()
	var s1, s2 uint16
	// A second of sound from both sides (loud: who speaks).
	second := func(callerLoud, agentLoud bool) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); voice(nil, side, &s2, agentLoud, rand.New(rand.NewSource(int64(s2)))) }()
		voice(nil, call, &s1, callerLoud, rand.New(rand.NewSource(int64(s1)+1)))
		wg.Wait()
	}
	quiet := func() { second(false, false) }
	quiet()
	phase.Store(1)
	second(true, false) // the caller speaks, for two seconds
	second(true, false)
	quiet()
	speak := page.expect("live.speak")
	if speak["side"] != "a" || speak["on"] != true || speak["id"] != id {
		t.Fatalf("live.speak: %v", speak)
	}
	// What they said so far while they speak, then the whole utterance.
	if p := page.expect("live.partial"); p["side"] != "a" || p["seq"].(float64) != 1 || !strings.HasPrefix(p["text"].(string), "Hi,") {
		t.Fatalf("live.partial: %v", p)
	}
	text := page.expect("live.text")
	if text["side"] != "a" || text["text"] != "Hi, I would like to book a table for two tonight." || text["seq"].(float64) != 1 {
		t.Fatalf("live.text: %v", text)
	}
	// A page opened during the call gets what was said so far.
	late := r.meetWS("ws", r.session("1001", "Pass-1001-x").client.Jar)
	if m := late.expect("live.start"); m["id"] != id || m["me"] != "b" {
		t.Fatalf("live.start on a page opened later: %v", m)
	}
	if m := late.expect("live.text"); m["seq"] != text["seq"] || m["text"] != text["text"] {
		t.Fatalf("live.text on a page opened later: %v", m)
	}
	phase.Store(2)
	second(false, true) // 1001 answers
	quiet()
	if speak := page.expect("live.speak"); speak["side"] != "b" || speak["on"] != true {
		t.Fatalf("live.speak: %v", speak)
	}
	text2 := page.expect("live.text")
	if text2["side"] != "b" || !strings.HasPrefix(text2["text"].(string), "Of course") || text2["at"].(float64) <= text["at"].(float64) {
		t.Fatalf("live.text: %v (after %v)", text2, text)
	}
	_, _ = call.Bye()
	if end := page.expect("live.end"); end["id"] != id {
		t.Fatalf("live.end: %v", end)
	}

	// Turned off, the next call is not written down.
	r.mustCall("PUT", d+"transcription", map[string]any{"live": false})
	go func() { c, _ := r.carrier.Dial("6531590078", []int{0}, 10*time.Second, false); done <- c }()
	in, err = agent.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	call = <-done
	if call == nil {
		t.Fatal("the second call was not answered")
	}
	time.Sleep(500 * time.Millisecond)
	_, _ = call.Bye()
	_ = page.ws.SetReadDeadline(time.Now().Add(time.Second))
	for {
		var msg map[string]any
		if err := page.ws.ReadJSON(&msg); err != nil {
			break
		}
		if t2, _ := msg["t"].(string); strings.HasPrefix(t2, "live.") {
			t.Fatalf("live transcription while off: %v", msg)
		}
	}
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }

// TestLiveTranscriptionReal plays a two-channel call recording (left: the
// caller, right: 1001) through a call with the real live recogniser:
//
//	LIVE_WAV=call.wav WHISPER_CLI=/usr/local/bin/rq-speech WHISPER_MODEL=ggml-base.bin go test -run LiveTranscriptionReal -v ./internal/web
func TestLiveTranscriptionReal(t *testing.T) {
	wav, cli, model := os.Getenv("LIVE_WAV"), os.Getenv("WHISPER_CLI"), os.Getenv("WHISPER_MODEL")
	if wav == "" || cli == "" || model == "" {
		t.Skip("LIVE_WAV, WHISPER_CLI and WHISPER_MODEL are not set")
	}
	left, right := stereoWAV(t, wav)
	r := newRigWith(t, `<param name="transcribe-command" value="`+cli+`"/><param name="transcribe-model" value="`+model+`"/>`)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	if st := r.mustCall("GET", d+"transcription", nil); st["live_server"] != true {
		t.Fatalf("live recogniser not found next to %s: %v", cli, st)
	}
	r.mustCall("PUT", d+"transcription", map[string]any{"live": true, "language": firstEnv("LIVE_LANG", "auto")})
	// the live recogniser starts and loads the model before any call.
	for deadline := time.Now().Add(90 * time.Second); ; {
		st := r.mustCall("GET", d+"transcription", nil)
		if st["live_running"] == true {
			break
		}
		if time.Now().After(deadline) || st["live_error"] != "" {
			t.Fatalf("live recogniser not running: %v", st["live_error"])
		}
		time.Sleep(200 * time.Millisecond)
	}
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6531590079", "gateway_uuid": gw["gateway_uuid"],
		"action": "transfer:1001 XML " + domain})
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1001"), map[string]any{"web_role": "user", "web_password": "Pass-1001-x"})
	page := r.meetWS("ws", r.session("1001", "Pass-1001-x").client.Jar)
	agent := r.phone("1001")
	done := make(chan *testua.Call, 1)
	go func() { c, _ := r.carrier.Dial("6531590079", []int{0}, 10*time.Second, false); done <- c }()
	in, err := agent.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	call := <-done
	if call == nil {
		t.Fatal("the call was not answered")
	}
	side := in.Dialog()
	start := page.expect("live.start")
	began := time.Now()
	texts := make(chan map[string]any, 100)
	go func() {
		for {
			_ = page.ws.SetReadDeadline(time.Now().Add(2 * time.Minute))
			var m map[string]any
			if err := page.ws.ReadJSON(&m); err != nil {
				close(texts)
				return
			}
			if m["t"] == "live.text" || m["t"] == "live.end" {
				texts <- m
			}
			if m["t"] != "live.end" {
				t.Logf("%6.1fs %s side %v seq %v on %v %.40v", time.Since(began).Seconds(), m["t"], m["side"], m["seq"], m["on"], m["text"])
			}
		}
	}()
	tick := time.NewTicker(20 * time.Millisecond)
	var seq uint16
	for off := 0; off < len(left)+8000*3; off += 160 { // the recording, then 3 s of silence
		a, b := make([]byte, 160), make([]byte, 160)
		for i := range a {
			var l, rr int16
			if off+i < len(left) {
				l, rr = left[off+i], right[off+i]
			}
			a[i], b[i] = media.LinearToUlaw(l), media.LinearToUlaw(rr)
		}
		seq++
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, seq, uint32(seq)*160, a)
		_ = testua.SendRTP(side.RTP, side.RemoteRTP, 0, seq, uint32(seq)*160, b)
		<-tick.C
	}
	tick.Stop()
	_, _ = call.Bye()
	got := map[string]int{}
	for m := range texts {
		if m["t"] == "live.end" {
			break
		}
		who := "1001"
		if m["side"] != start["me"] {
			who = "caller"
		}
		got[who]++
	}
	if got["caller"] == 0 || got["1001"] == 0 {
		t.Fatalf("live text from both sides expected: %v", got)
	}
}

func firstEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// stereoWAV reads a two-channel 8 kHz 16-bit WAV file.
func stereoWAV(t *testing.T, path string) (left, right []int16) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for off := 12; off+8 <= len(data); {
		size := int(binary.LittleEndian.Uint32(data[off+4:]))
		body := off + 8
		if string(data[off:off+4]) == "fmt " && (binary.LittleEndian.Uint16(data[body+2:]) != 2 ||
			binary.LittleEndian.Uint32(data[body+4:]) != 8000 || binary.LittleEndian.Uint16(data[body+14:]) != 16) {
			t.Fatal("LIVE_WAV: a two-channel 8 kHz 16-bit WAV file is needed")
		}
		if string(data[off:off+4]) == "data" {
			pcm := data[body:min(len(data), body+size)]
			for i := 0; i+3 < len(pcm); i += 4 {
				left = append(left, int16(binary.LittleEndian.Uint16(pcm[i:])))
				right = append(right, int16(binary.LittleEndian.Uint16(pcm[i+2:])))
			}
			return left, right
		}
		off = body + size + size%2
	}
	t.Fatal("LIVE_WAV: no audio")
	return nil, nil
}
