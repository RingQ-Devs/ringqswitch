package web_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// fakeRecogniser stands in for the recogniser: it "hears" a fixed sentence in
// each turn file (turn0000: the customer's, then the agent's), as
// the recogniser's -oj output next to each file.
const fakeRecogniser = `#!/bin/sh
while [ $# -gt 0 ]; do
  if [ "$1" = -f ]; then
    case "$2" in
    *turn0000*) t="Hello, I was overcharged on my bill and I am really not happy." ;;
    *) t="I am so sorry about that. I will fix it for you right away, thank you for waiting." ;;
    esac
    printf '{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":1100},"text":" %s"}]}' "$t" > "$2.json"
    shift
  fi
  shift
done
`

// voice sends a second of voice-like sound (or near silence) as PCMU.
func voice(conn interface{ Write([]byte) (int, error) }, ua *testua.Call, seq *uint16, loud bool, rnd *rand.Rand) {
	_ = conn
	for f := 0; f < 50; f++ {
		pl := make([]byte, 160)
		for i := range pl {
			amp := 120.0
			if loud {
				amp = 9000 * (0.55 + 0.45*math.Sin(2*math.Pi*4*float64(f*160+i)/8000))
			}
			pl[i] = media.LinearToUlaw(int16(amp * (rnd.Float64()*2 - 1)))
		}
		*seq++
		_ = testua.SendRTP(ua.RTP, ua.RemoteRTP, 0, *seq, uint32(*seq)*160, pl)
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTranscription(t *testing.T) {
	dir := t.TempDir()
	cmd, model := filepath.Join(dir, "whisper-cli"), filepath.Join(dir, "ggml-test.bin")
	// The stand-in also notes how it was run.
	script := strings.Replace(fakeRecogniser, "#!/bin/sh\n", "#!/bin/sh\necho \"$@\" >> "+dir+"/runs\n", 1)
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stand-in for the recogniser's model library.
	library := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/ggml-tiny.bin" {
			http.NotFound(w, req)
			return
		}
		w.Write(append([]byte("lmgg"), make([]byte, 11e6)...))
	}))
	defer library.Close()
	r := newRigWith(t, `<param name="transcribe-command" value="`+cmd+`"/><param name="transcribe-model" value="`+model+`"/>
    <param name="transcribe-min-seconds" value="1"/><param name="transcribe-model-url" value="`+library.URL+`"/>`)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	st := r.mustCall("GET", d+"transcription", nil)
	if eng := st["engine"].(map[string]any); eng["ok"] != true || st["settings"].(map[string]any)["enabled"] != false {
		t.Fatalf("settings: %v", st)
	}
	r.mustCall("PUT", d+"transcription", map[string]any{"enabled": true, "scope": "external", "language": "en"})
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6531590077", "gateway_uuid": gw["gateway_uuid"],
		"action": "transfer:1001 XML " + domain})
	agent := r.phone("1001")

	// The customer calls the DID; 1001 answers; they take turns.
	done := make(chan *testua.Call, 1)
	go func() { c, _ := r.carrier.Dial("6531590077", []int{0}, 10*time.Second, false); done <- c }()
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
	side := in.Dialog()
	rnd := rand.New(rand.NewSource(3))
	var s1, s2 uint16
	quiet := func() { // both lines stay alive
		go voice(nil, side, &s2, false, rnd)
		voice(nil, call, &s1, false, rnd)
	}
	quiet()
	go voice(nil, side, &s2, false, rand.New(rand.NewSource(4)))
	voice(nil, call, &s1, true, rnd) // the customer speaks
	quiet()
	go voice(nil, call, &s1, false, rand.New(rand.NewSource(5)))
	voice(nil, side, &s2, true, rnd) // the agent answers
	quiet()
	_, _ = call.Bye()

	ctx := context.Background()
	var id, status, failure string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_ = r.conn.QueryRow(ctx, `select call_uuid::text, status, coalesce(error, '') from rq_transcripts limit 1`).Scan(&id, &status, &failure)
		if status == "done" || status == "failed" || status == "empty" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status != "done" {
		t.Fatalf("transcript %s: %q %s", id, status, failure)
	}
	tr := r.mustCall("GET", d+"transcripts/"+id, nil)
	x := tr["transcript"].(map[string]any)
	segs := x["segments"].([]any)
	if len(segs) != 2 || x["customer"] != "A" || x["stereo"] != true || x["customer_label"] != "negative" || x["language"] != "en" {
		t.Fatalf("transcript: %v", x)
	}
	a, b := segs[0].(map[string]any), segs[1].(map[string]any)
	if a["speaker"] != "A" || !strings.HasPrefix(a["text"].(string), "Hello, I was overcharged") || a["sentiment"].(float64) >= 0 ||
		b["speaker"] != "B" || !strings.HasPrefix(b["text"].(string), "I am so sorry") || b["start"].(float64) < a["end"].(float64) {
		t.Fatalf("segments: %v", segs)
	}
	if !strings.Contains(x["speaker_b"].(string), "1001") || x["recording"] != nil {
		t.Fatalf("speakers: a=%v b=%v", x["speaker_a"], x["speaker_b"])
	}
	// Call history shows it.
	var cdr struct{ Rows []map[string]any }
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.call("GET", d+"cdr", nil, &cdr)
		if len(cdr.Rows) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	found := false
	for _, row := range cdr.Rows {
		if row["xml_cdr_uuid"] == id {
			found = row["transcript"] == "done" && row["sentiment"] == "negative"
		}
	}
	if !found {
		t.Fatalf("call history rows: %v", cdr.Rows)
	}
	// The call's path: from the carrier's trunk to the DID, routed to 1001,
	// rung, answered, and the caller hung up.
	var flow struct {
		Steps []struct {
			T    int64
			K, X string
		}
		Kept bool
	}
	r.call("GET", d+"cdr/"+id+"/flow", nil, &flow)
	var kinds []string
	for _, st := range flow.Steps {
		kinds = append(kinds, st.K+": "+st.X)
	}
	got := strings.Join(kinds, " | ")
	for _, want := range []string{"start: ", " called 6531590077 (trunk Carrier)", "route: Routed to 1001", "ring: Ringing 1001", "answer: Answered by ", "hangup: ", " hung up", "end: Call ended"} {
		if !strings.Contains(got, want) || !flow.Kept {
			t.Fatalf("call flow (kept %v): %s\nmissing %q", flow.Kept, got, want)
		}
	}
	for _, row := range cdr.Rows {
		if row["xml_cdr_uuid"] == id && (!strings.Contains(fmt.Sprint(row["answered_by"]), "1001") || row["ended_by"] == nil) {
			t.Fatalf("call history row summary: answered_by=%v ended_by=%v", row["answered_by"], row["ended_by"])
		}
	}

	// A user who was not on the call cannot read it.
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1002"), map[string]any{"web_role": "user", "web_password": "Pass-1002-x"})
	// Pages and the whole history.
	var page struct {
		Rows  []map[string]any
		Total int
	}
	r.call("GET", d+"cdr?all=1&limit=1&offset=0", nil, &page)
	if len(page.Rows) != 1 || page.Total < 1 {
		t.Fatalf("call history, all, a page of 1: %d rows of %d", len(page.Rows), page.Total)
	}
	page.Rows, page.Total = nil, -1
	r.call("GET", d+"reports/agent-log?all=1&limit=5", nil, &page)
	if page.Rows == nil || page.Total < 0 {
		t.Fatalf("agent status log page: %+v", page)
	}
	var whole []map[string]any
	if code := r.call("GET", d+"reports/queue-calls?all=1", nil, &whole); code != 200 {
		t.Fatalf("queue calls, every row: HTTP %d", code)
	}
	user := r.session("1002", "Pass-1002-x")
	// The signed-in name follows the extension's first and last name.
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1002"), map[string]any{"directory_first_name": "Priya", "directory_last_name": "Sales"})
	var me map[string]any
	if user.call("GET", "me", nil, &me); me["name"] != "Priya Sales" {
		t.Fatalf("me: %v", me)
	}
	if code := user.call("GET", d+"cdr/"+id+"/flow", nil, nil); code != 404 {
		t.Fatalf("another user read the call's path: HTTP %d", code)
	}
	if code := user.call("GET", d+"transcripts/"+id, nil, nil); code != 404 {
		t.Fatalf("another user read the transcript: HTTP %d", code)
	}
	if code := user.call("POST", d+"transcripts/"+id, nil, nil); code != 403 && code != 404 {
		t.Fatalf("a user asked for a transcript: HTTP %d", code)
	}
	// Transcribe it again on request.
	if out := r.mustCall("POST", d+"transcripts/"+id, nil); out["status"] != "queued" {
		t.Fatalf("again: %v", out)
	}
	r.waitCount("transcribed again", 1, `select count(*) from rq_transcripts where call_uuid = $1 and status = 'done' and updated_at > now() - interval '5 seconds'`, id)
	st = r.mustCall("GET", d+"transcription", nil)
	if c := st["counts"].(map[string]any); c["done"] != float64(1) {
		t.Fatalf("counts: %v", c)
	}

	// Speech models: download one, use it for the domain, delete it.
	modelRow := func(name string) map[string]any {
		st := r.mustCall("GET", d+"transcription", nil)
		for _, m := range st["models"].([]any) {
			if m := m.(map[string]any); m["name"] == name {
				return m
			}
		}
		t.Fatalf("model %s not listed: %v", name, st["models"])
		return nil
	}
	if m := modelRow("tiny"); m["installed"] != false || st["can_manage_models"] != true || st["model_in_use"] != "test" {
		t.Fatalf("before the download: %v, %v", m, st)
	}
	if code := user.call("POST", d+"transcription/models/tiny", nil, nil); code != 403 {
		t.Fatalf("a user downloaded a model: HTTP %d", code)
	}
	if code := r.call("PUT", d+"transcription", map[string]any{"enabled": true, "language": "en", "model": "tiny"}, nil); code != 400 {
		t.Fatalf("a model not installed was chosen: HTTP %d", code)
	}
	r.mustCall("POST", d+"transcription/models/tiny", nil)
	var tiny map[string]any
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if tiny = modelRow("tiny"); tiny["installed"] == true {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if tiny["installed"] != true || tiny["removable"] != true || tiny["download"] != nil {
		t.Fatalf("after the download: %v", tiny)
	}
	r.mustCall("PUT", d+"transcription", map[string]any{"enabled": true, "language": "en", "model": "tiny"})
	r.mustCall("POST", d+"transcripts/"+id, nil)
	r.waitCount("transcribed with tiny", 1, `select count(*) from rq_transcripts where call_uuid = $1 and status = 'done' and engine = 'rq-speech tiny'`, id)
	runs, _ := os.ReadFile(filepath.Join(dir, "runs"))
	if lines := strings.Split(strings.TrimSpace(string(runs)), "\n"); !strings.Contains(lines[len(lines)-1], "-m "+tiny["path"].(string)+" ") {
		t.Fatalf("recogniser runs:\n%s", runs)
	}
	r.mustCall("DELETE", d+"transcription/models/tiny", nil)
	if m := modelRow("tiny"); m["installed"] != false {
		t.Fatalf("after deleting: %v", m)
	}
	if st := r.mustCall("GET", d+"transcription", nil); st["model_in_use"] != "test" {
		t.Fatalf("the domain's model is gone, the default is used: %v", st["model_in_use"])
	}
}

// TestVoicemailTranscription: a message left in a voicemail box is
// transcribed (voicemail on, calls off), shows in the voicemail list with
// its text, lands in v_voicemail_messages.message_transcription, can be
// transcribed again, and its transcript goes when the message is deleted.
func TestVoicemailTranscription(t *testing.T) {
	dir := t.TempDir()
	cmd, model := filepath.Join(dir, "whisper-cli"), filepath.Join(dir, "ggml-test.bin")
	if err := os.WriteFile(cmd, []byte(fakeRecogniser), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, `<param name="transcribe-command" value="`+cmd+`"/><param name="transcribe-model" value="`+model+`"/>`)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	r.mustCall("PUT", d+"transcription", map[string]any{"enabled": false, "voicemail": true, "language": "en"})
	ctx := context.Background()
	var domUUID string
	if err := r.conn.QueryRow(ctx, `select domain_uuid::text from v_domains where domain_name = $1`, domain).Scan(&domUUID); err != nil {
		t.Fatal(err)
	}
	box := "c0ffee00-0000-4000-8000-000000001001"
	if _, err := r.conn.Exec(ctx, `insert into v_voicemails (voicemail_uuid, domain_uuid, voicemail_id, voicemail_enabled) values ($1, $2, '1001', 'true')`,
		box, domUUID); err != nil {
		t.Fatal(err)
	}
	// The message: 3 s, a second of speech in the middle.
	msg := "b0b0b0b0-1111-4222-8333-444455556666"
	path := filepath.Join(r.sw.Config().Settings.VoicemailDir, domain, "1001", "msg_"+msg+".wav")
	rnd := rand.New(rand.NewSource(7))
	pcm := make([]int16, 3*8000)
	for i := range pcm {
		amp := 40.0
		if i >= 8000 && i < 16000 {
			amp = 9000 * (0.55 + 0.45*math.Sin(2*math.Pi*4*float64(i)/8000))
		}
		pcm[i] = int16(amp * (rnd.Float64()*2 - 1))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, monoWAV(pcm), 0o644); err != nil {
		t.Fatal(err)
	}
	e := core.NewEvent(core.EvCustom, "vm::maintenance")
	for k, v := range map[string]string{"VM-Action": "leave-message", "VM-User": "1001", "VM-Domain": domain, "VM-UUID": box,
		"VM-Message-UUID": msg, "VM-Caller-ID-Name": "Tan Wei Ming", "VM-Caller-ID-Number": "6591234567", "VM-Message-Len": "3",
		"VM-File-Path": path, "VM-Created": fmt.Sprint(time.Now().Unix())} {
		e.Add(k, v)
	}
	r.sw.Events().Publish(e)
	r.waitCount("voicemail transcribed", 1, `select count(*) from rq_transcripts where call_uuid = $1 and kind = 'voicemail' and status = 'done'`, msg)

	var rows []map[string]any
	r.call("GET", d+"voicemail-messages", nil, &rows)
	if len(rows) != 1 || rows[0]["transcript"] != "done" || !strings.HasPrefix(fmt.Sprint(rows[0]["transcript_text"]), "Hello, I was overcharged") ||
		rows[0]["sentiment"] != "negative" {
		t.Fatalf("voicemail rows: %v", rows)
	}
	var text string
	_ = r.conn.QueryRow(ctx, `select coalesce(message_transcription, '') from v_voicemail_messages where voicemail_message_uuid = $1`, msg).Scan(&text)
	if !strings.HasPrefix(text, "Hello, I was overcharged") {
		t.Fatalf("message_transcription %q", text)
	}
	tr := r.mustCall("GET", d+"voicemail-messages/"+msg+"/transcript", nil)
	if x := tr["transcript"].(map[string]any); x["kind"] != "voicemail" || len(x["segments"].([]any)) != 1 || x["speaker_a"] != "Tan Wei Ming (6591234567)" {
		t.Fatalf("transcript: %v", x)
	}
	if out := r.mustCall("POST", d+"voicemail-messages/"+msg+"/transcript", nil); out["status"] != "queued" {
		t.Fatalf("again: %v", out)
	}
	r.waitCount("transcribed again", 1, `select count(*) from rq_transcripts where call_uuid = $1 and status = 'done' and updated_at > now() - interval '5 seconds'`, msg)
	r.mustCall("DELETE", d+"voicemail-messages/"+msg, nil)
	if n := r.count(`select count(*) from rq_transcripts where call_uuid = '` + msg + `'`); n != 0 {
		t.Fatalf("the transcript stayed after the message was deleted (%d)", n)
	}
}

// monoWAV is 8 kHz 16-bit mono PCM as a WAV file.
func monoWAV(pcm []int16) []byte {
	b := make([]byte, 44+2*len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(pcm)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 8000)
	binary.LittleEndian.PutUint32(b[28:], 16000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(pcm)))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(v))
	}
	return b
}
