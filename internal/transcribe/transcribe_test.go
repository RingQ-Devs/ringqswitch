package transcribe

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// speechy is a voice-like burst: noise shaped by a 4 Hz syllable rhythm.
func speechy(rnd *rand.Rand, n int, level float64) []int16 {
	out := make([]int16, n)
	amp := 32768 * math.Pow(10, level/20)
	for i := range out {
		env := 0.55 + 0.45*math.Sin(2*math.Pi*4*float64(i)/8000)
		out[i] = int16(amp * env * (rnd.Float64()*2 - 1))
	}
	return out
}

// voiced is a vowel-like sound: harmonics of a pitch that moves the way a
// voice's does (±12% at 3 Hz), in 4 Hz syllables.
func voiced(n int, f0, level float64) []int16 {
	out := make([]int16, n)
	amp := 32768 * math.Pow(10, level/20) / 2
	phase := 0.0
	for i := range out {
		t := float64(i) / 8000
		f := f0 * (1 + 0.12*math.Sin(2*math.Pi*3*t))
		phase += 2 * math.Pi * f / 8000
		var v float64
		for k := 1; k <= 12; k++ {
			v += math.Sin(float64(k)*phase) / float64(k)
		}
		env := math.Max(0, math.Sin(2*math.Pi*4*t))
		out[i] = int16(amp * env * v)
	}
	return out
}

// buzz is a steady 104 Hz hum switched on and off every second, like the
// phone vibrating next to a microphone in a recorded test call.
func buzz(n int, level float64) []int16 {
	out := make([]int16, n)
	amp := 32768 * math.Pow(10, level/20)
	for i := range out {
		t := float64(i) / 8000
		if int(t)%2 == 1 {
			continue
		}
		saw := 2*math.Mod(t*104, 1) - 1
		out[i] = int16(amp * saw)
	}
	return out
}

func writeStereo(t *testing.T, a, b []int16) string {
	t.Helper()
	pcm := make([]int16, 2*len(a))
	for i := range a {
		pcm[2*i], pcm[2*i+1] = a[i], b[i]
	}
	path := filepath.Join(t.TempDir(), "call.wav")
	if err := writeWAV(path, pcm, 8000); err != nil {
		t.Fatal(err)
	}
	// writeWAV writes mono: patch the header to two channels.
	data, _ := os.ReadFile(path)
	data[22], data[28], data[29], data[30], data[31], data[32] = 2, 0, 0x7D, 0, 0, 4 // 32000 bytes/s, block 4
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// stereoCall writes a call: A speaks at 1-3 s and 7-8.5 s, B at 4-6 s,
// over faint line noise.
func stereoCall(t *testing.T) string {
	t.Helper()
	rnd := rand.New(rand.NewSource(1))
	const rate, secs = 8000, 10
	a, b := speechy(rnd, rate*secs, -75), speechy(rnd, rate*secs, -75)
	put := func(ch []int16, from, to float64) { copy(ch[int(from*rate):], speechy(rnd, int((to-from)*rate), -18)) }
	put(a, 1, 3)
	put(a, 7, 8.5)
	put(b, 4, 6)
	return writeStereo(t, a, b)
}

// fakeEar "recognises" a turn file by its name (turn0000.wav...) as one
// sentence. Like whisper on the call in the bug report, it times every
// sentence from the start of its file.
type fakeEar struct {
	text  map[string]string // file name: what was said
	calls []string          // "language: files"
}

func (f *fakeEar) Recognize(_ context.Context, wavs []string, language string) ([]*Recognition, error) {
	var names []string
	var out []*Recognition
	for _, w := range wavs {
		name := filepath.Base(w)
		names = append(names, name)
		chans, rate, err := readChannels(w)
		if err != nil {
			return nil, err
		}
		dur := float64(len(chans[0])) / float64(rate)
		r := &Recognition{Language: "en"}
		if text, ok := f.text[name]; ok {
			r.Segments = []RecSegment{{From: 0, To: dur, Text: " " + text}}
		} else {
			r.Segments = []RecSegment{{From: 0, To: 1, Text: " [BLANK_AUDIO]"}}
		}
		out = append(out, r)
	}
	f.calls = append(f.calls, language+": "+strings.Join(names, " "))
	return out, nil
}

func TestTranscribeStereoCall(t *testing.T) {
	ear := &fakeEar{text: map[string]string{
		"turn0000.wav": "Hello, I was overcharged on my bill and I am not happy.", // A, 1-3 s
		"turn0001.wav": "Thank you so much, that was really helpful!",             // A, 7-8.5 s
		"turn0002.wav": "Good morning, sorry about that, let me fix it for you.",  // B, 4-6 s
	}}
	res, err := Transcribe(context.Background(), stereoCall(t), Options{Recognizer: ear, Customer: "A", TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Each speaker's language is detected on their longest turn, then the
	// other turns are recognised in it.
	if fmt.Sprint(ear.calls) != "[auto: turn0000.wav turn0002.wav en: turn0001.wav]" {
		t.Fatalf("recogniser calls %q", ear.calls)
	}
	if !res.Stereo || res.Language != "en" || math.Abs(res.Duration-10) > 0.01 {
		t.Fatalf("result %+v", res)
	}
	want := []struct {
		speaker  string
		from, to float64
		label    string
	}{{"A", 1, 3, "negative"}, {"B", 4, 6, "neutral"}, {"A", 7, 8.5, "positive"}}
	if len(res.Segments) != len(want) {
		t.Fatalf("segments: %+v", res.Segments)
	}
	for i, w := range want {
		s := res.Segments[i]
		if s.Speaker != w.speaker || math.Abs(s.Start-w.from) > 0.35 || math.Abs(s.End-w.to) > 0.45 {
			t.Errorf("segment %d: %+v, want %s %.1f-%.1f", i, s, w.speaker, w.from, w.to)
		}
		if got := Label(s.Sentiment); got != w.label && !(w.label == "neutral" && got != "negative") {
			t.Errorf("segment %d sentiment %v (%s), want %s", i, s.Sentiment, got, w.label)
		}
	}
	if len(res.Speakers) != 2 || res.Speakers[0].Speaker != "A" || res.Speakers[0].Negative != 1 || res.Speakers[0].Positive != 1 ||
		math.Abs(res.Speakers[0].Talk-3.5) > 0.8 || res.Speakers[0].Ending <= 0 {
		t.Fatalf("speakers: %+v", res.Speakers)
	}
	txt := res.Text(map[string]string{"A": "Caller"})
	if !strings.Contains(txt, "] Caller: Hello,") || !strings.Contains(txt, "] Speaker B: Good morning") {
		t.Fatalf("text:\n%s", txt)
	}
}

// TestTurnsKeepOrder is the call from the bug report: a phone buzzes next
// to B's microphone at 9-14 s, A speaks at 13.5-22.5 s, B answers at
// 34.5-40.5 s. The recogniser timed B's answer from the start of what it
// was given, which put it before A's question. Now the buzz is not taken
// for speech and B's answer stays at 34.5 s.
func TestTurnsKeepOrder(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	const rate = 8000
	a, b := speechy(rnd, 43*rate, -60), speechy(rnd, 43*rate, -75)
	copy(b[9*rate:], buzz(5*rate, -33))
	for _, at := range [][2]float64{{13.5, 14.2}, {16, 16.6}, {18, 19.6}, {21, 22.6}} {
		copy(a[int(at[0]*rate):], voiced(int((at[1]-at[0])*rate), 210, -20))
	}
	copy(b[int(34.5*rate):], voiced(3*rate, 120, -15))
	copy(b[int(38.4*rate):], voiced(2*rate, 120, -15))
	regs, _ := speechRegions(b, rate)
	if len(regs) != 1 || regs[0].start < 34*rate {
		t.Fatalf("B's speech regions %v: the buzz at 9-14 s is not speech", regs)
	}
	ear := &fakeEar{text: map[string]string{
		"turn0000.wav": "Hello. Hello. My name is Thilak. What is your nature of job?",
		"turn0001.wav": "Yes, I am a software developer. Yes, I am a software developer.",
	}}
	res, err := Transcribe(context.Background(), writeStereo(t, a, b), Options{Recognizer: ear, Language: "en", TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 2 || res.Segments[0].Speaker != "A" || res.Segments[1].Speaker != "B" ||
		math.Abs(res.Segments[0].Start-13.25) > 0.3 || math.Abs(res.Segments[1].Start-34.25) > 0.3 {
		t.Fatalf("segments %+v (calls %q)", res.Segments, ear.calls)
	}
}

func TestSpeechNotTone(t *testing.T) {
	const rate = 8000
	rnd := rand.New(rand.NewSource(4))
	quiet := speechy(rnd, 6*rate, -70)
	tone := append([]int16(nil), quiet...)
	copy(tone[rate:], buzz(4*rate, -25)) // on at 1-2 s and 3-4 s... (switches every second)
	if regs, _ := speechRegions(tone, rate); len(regs) != 0 {
		t.Errorf("a buzz gave speech regions %v", regs)
	}
	voice := append([]int16(nil), quiet...)
	copy(voice[rate:], voiced(3*rate, 140, -25))
	regs, talk := speechRegions(voice, rate)
	if len(regs) != 1 || talk < 1.2 {
		t.Errorf("a voice gave speech regions %v (%.2f s talking)", regs, talk)
	}
}

func TestMakeTurns(t *testing.T) {
	const rate = 1000
	r := func(from, to float64) region { return region{start: int(from * rate), end: int(to * rate)} }
	regs := [][]region{
		{r(1, 2), r(3, 4), r(10, 11)}, // A: B speaks between the first two; then a long silence
		{r(2.4, 2.8)},                 // B
	}
	got := fmt.Sprint(makeTurns(regs, rate))
	if got != "[{0 1000 2000 [{1000 2000}]} {0 3000 4000 [{3000 4000}]} {0 10000 11000 [{10000 11000}]} {1 2400 2800 [{2400 2800}]}]" {
		t.Errorf("turns %s", got)
	}
	// A monologue is cut where the recogniser's window ends.
	var long []region
	for s := 0.0; s < 60; s += 2 {
		long = append(long, r(s, s+1.5))
	}
	turns := makeTurns([][]region{long}, rate)
	if len(turns) != 3 {
		t.Fatalf("%d turns for a 60 s monologue", len(turns))
	}
	for _, tu := range turns {
		if tu.end-tu.start > turnMaxMs {
			t.Errorf("turn %v longer than %d ms", tu, turnMaxMs)
		}
	}
}

func TestPlaceInTurn(t *testing.T) {
	const rate = 1000
	// A turn of two speech regions, 1-2 s and 4.5-5.5 s, with a silence between.
	tu := turn{start: 1000, end: 5500, regs: []region{{1000, 2000}, {4500, 5500}}}
	for _, c := range []struct {
		seg      RecSegment
		ok       bool
		from, to float64
	}{
		{RecSegment{From: 0.1, To: 0.9, Text: " Hello there"}, true, 1.1, 1.9},
		{RecSegment{From: 1.7, To: 2.3, Text: " Thank you."}, false, 0, 0}, // over the silence: invented
		{RecSegment{From: 3.6, To: 9, Text: " Bye"}, true, 4.6, 5.5},       // past the end: kept inside the turn
		{RecSegment{From: 0, To: 1, Text: " [BLANK_AUDIO]"}, false, 0, 0},
	} {
		s, ok := place(c.seg, tu, rate, "B")
		if ok != c.ok || (ok && (math.Abs(s.Start-c.from) > 0.01 || math.Abs(s.End-c.to) > 0.01 || s.Speaker != "B")) {
			t.Errorf("%+v: %+v %v", c.seg, s, ok)
		}
	}
}

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		" [BLANK_AUDIO]":               "",
		" (music) Hello there":         "Hello there",
		" *laughs* ok  then ":          "ok then",
		" ♪♪ ":                         "",
		" Price is 20 dollars [noise]": "Price is 20 dollars",
	} {
		if got := cleanText(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestParseWhisperJSON(t *testing.T) {
	data := []byte(`{"systeminfo":"x","result":{"language":"en"},"transcription":[
	 {"timestamps":{"from":"00:00:00,000","to":"00:00:02,500"},"offsets":{"from":0,"to":2500},"text":" Hello there."},
	 {"offsets":{"from":3000,"to":4000},"text":" Bye."}]}`)
	r, err := parseSpeechJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.Language != "en" || len(r.Segments) != 2 || r.Segments[0].To != 2.5 || r.Segments[1].From != 3 || r.Segments[1].Text != " Bye." {
		t.Fatalf("%+v", r)
	}
}

// TestSpeechCLIFiles runs SpeechCLI.Recognize against a stand-in recogniser
// that answers for every -f file.
func TestSpeechCLIFiles(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "whisper-cli")
	script := `#!/bin/sh
echo "$@" > "` + dir + `/args"
lang=en
while [ $# -gt 0 ]; do
  case "$1" in
    -l) lang="$2"; shift 2 ;;
    -f) printf '{"result":{"language":"%s"},"transcription":[{"offsets":{"from":0,"to":900},"text":" file %s"}]}' "$lang" "$(basename "$2")" > "$2.json"; shift 2 ;;
    *) shift ;;
  esac
done
`
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, n := range []string{"one.wav", "two.wav"} {
		f := filepath.Join(dir, n)
		os.WriteFile(f, []byte("RIFF"), 0o600)
		files = append(files, f)
	}
	w := &SpeechCLI{Command: cli, Model: "m.bin", Threads: 3}
	recs, err := w.Recognize(context.Background(), files, "ms")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Language != "ms" || recs[1].Segments[0].Text != " file two.wav" || recs[1].Segments[0].To != 0.9 {
		t.Fatalf("%+v %+v", recs[0], recs[1])
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "-m m.bin -l ms -oj -np -nt -t 3 -f "+files[0]+" -f "+files[1]) {
		t.Fatalf("args %s", args)
	}
	if _, err := os.Stat(files[0] + ".json"); !os.IsNotExist(err) {
		t.Fatal("the result file was not removed")
	}
}

// TestWhisperCLI runs the real recogniser when WHISPER_CLI and
// WHISPER_MODEL point at it (WHISPER_WAV: a call recording to transcribe).
func TestWhisperCLI(t *testing.T) {
	cmd, model := os.Getenv("WHISPER_CLI"), os.Getenv("WHISPER_MODEL")
	if cmd == "" || model == "" {
		t.Skip("WHISPER_CLI and WHISPER_MODEL not set")
	}
	cmd, err := FindCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	wav := os.Getenv("WHISPER_WAV")
	if wav == "" {
		wav = stereoCall(t)
	}
	res, err := Transcribe(context.Background(), wav, Options{Recognizer: &SpeechCLI{Command: cmd, Model: model, Threads: 2}, Language: os.Getenv("WHISPER_LANGUAGE")})
	if err != nil && err != ErrNoSpeech {
		t.Fatal(err)
	}
	t.Logf("\n%s", res.Text(nil))
}

func TestInstalledModels(t *testing.T) {
	sys, mine := t.TempDir(), t.TempDir()
	put := func(dir, name string, size int) {
		os.WriteFile(filepath.Join(dir, "ggml-"+name+".bin"), make([]byte, size), 0o644)
	}
	put(sys, "base", 2<<20)
	put(sys, "for-tests-tiny", 100) // too small: not a model
	put(mine, "large-v3", 2<<20)
	put(mine, "base", 2<<20) // the system one comes first
	put(mine, "custom-q8", 2<<20)
	got := InstalledModels([]string{sys, mine}, mine)
	var names []string
	for _, m := range got {
		names = append(names, fmt.Sprintf("%s:%v", m.Name, m.Removable))
	}
	if fmt.Sprint(names) != "[base:false large-v3:true custom-q8:true]" {
		t.Fatalf("%v", names)
	}
	if m, ok := AutomaticModel(got); !ok || m.Name != "base" {
		t.Fatalf("automatic %v", m)
	}
}

func TestDownloadModel(t *testing.T) {
	model := append([]byte("lmgg"), make([]byte, 11e6)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ggml-tiny.bin":
			w.Header().Set("Content-Length", strconv.Itoa(len(model)))
			w.Write(model)
		case "/ggml-base.bin":
			w.Write([]byte("<html>not found</html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	var last int64
	if err := DownloadModel(context.Background(), srv.URL, "tiny", dir, func(done, total int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, "ggml-tiny.bin")); err != nil || st.Size() != int64(len(model)) || last != int64(len(model)) {
		t.Fatalf("downloaded: %v %v (progress %d)", st, err, last)
	}
	if err := DownloadModel(context.Background(), srv.URL, "base", dir, nil); err == nil || !strings.Contains(err.Error(), "not a speech model") {
		t.Fatalf("an HTML page was taken for a model: %v", err)
	}
	if err := DownloadModel(context.Background(), srv.URL, "small", dir, nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing model: %v", err)
	}
	if err := DownloadModel(context.Background(), srv.URL, "../evil", dir, nil); err == nil {
		t.Fatal("a name outside the catalog was downloaded")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(left) != 0 {
		t.Fatalf("partial files left: %v", left)
	}
}
