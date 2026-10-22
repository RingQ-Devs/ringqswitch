package transcribe

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func feedAll(s *Segmenter, pcm []int16) []LiveEvent {
	var out []LiveEvent
	for i := 0; i+160 <= len(pcm); i += 160 {
		out = append(out, s.Feed(pcm[i:i+160])...)
	}
	return out
}

func TestSegmenter(t *testing.T) {
	rnd := rand.New(rand.NewSource(5))
	const rate = 8000
	var pcm []int16
	add := func(secs float64, level float64) { pcm = append(pcm, speechy(rnd, int(secs*rate), level)...) }
	add(1, -70)   // 0-1 s: quiet
	add(1.5, -18) // 1-2.5 s: speech
	add(1, -70)   // 2.5-3.5 s: quiet
	add(0.2, -18) // a click: not speech
	add(1, -70)   // 3.7-4.7 s
	add(12, -18)  // 4.7-16.7 s: a long speech, cut at 8 s
	add(1, -70)
	s := NewSegmenter()
	if _, _, ok := s.Current(); ok {
		t.Fatal("an utterance before any speech")
	}
	ev := feedAll(s, pcm[:2*rate]) // into the first speech
	if cur, from, ok := s.Current(); !ok || len(cur) < rate/2 || float64(from)/rate > 1.05 {
		t.Fatalf("the utterance being spoken: %v, %d samples from %d", ok, len(cur), from)
	}
	ev = append(ev, feedAll(s, pcm[2*rate:])...)
	ev = append(ev, s.Flush()...)
	var got []string
	for _, e := range ev {
		if e.Start {
			got = append(got, "start")
		} else {
			got = append(got, "utterance")
		}
	}
	if strings.Join(got, " ") != "start utterance start utterance start utterance" {
		t.Fatalf("events: %v", got)
	}
	sec := func(n int64) float64 { return float64(n) / rate }
	first := ev[1]
	if sec(first.From) < 0.6 || sec(first.From) > 1.05 || sec(first.To) < 2.5 || sec(first.To) > 3.0 {
		t.Errorf("first utterance %.2f-%.2f s", sec(first.From), sec(first.To))
	}
	if long := ev[3]; sec(long.To-long.From) < 7.9 || sec(long.To-long.From) > 8.1 {
		t.Errorf("a long speech is cut at 8 s: %.2f s", sec(long.To-long.From))
	}
	if rest := ev[5]; sec(rest.To) < 16.6 {
		t.Errorf("the rest of the long speech: %.2f-%.2f s", sec(rest.From), sec(rest.To))
	}
}

// TestSegmenterNoisyLine: a mobile line with a steady background (here
// -43 dBFS, quiet for the first 2 s, with a 1 s dip) is not taken for
// speech: only the two sentences said over it are utterances (a real call
// had 8-second "utterances" of background, which the recogniser filled
// with invented words).
func TestSegmenterNoisyLine(t *testing.T) {
	rnd := rand.New(rand.NewSource(9))
	const rate = 8000
	noise := func(secs, level float64) []int16 {
		out := make([]int16, int(secs*rate))
		amp := 32768 * math.Pow(10, level/20) * math.Sqrt(3)
		for i := range out {
			out[i] = int16(amp * (rnd.Float64()*2 - 1))
		}
		return out
	}
	over := func(bg, speech []int16) []int16 {
		for i := range speech {
			speech[i] = int16(max(-32768, min(32767, int(speech[i])+int(bg[i]))))
		}
		return speech
	}
	var pcm []int16
	pcm = append(pcm, noise(2, -70)...)                                      // 0-2 s: quiet
	pcm = append(pcm, noise(8, -43)...)                                      // 2-10 s: the background
	pcm = append(pcm, over(noise(1.5, -43), speechy(rnd, 1.5*rate, -15))...) // 10-11.5 s: speech
	pcm = append(pcm, noise(3, -43)...)                                      // 11.5-14.5 s
	pcm = append(pcm, noise(1, -55)...)                                      // 14.5-15.5 s: a dip
	pcm = append(pcm, noise(9, -43)...)                                      // 15.5-24.5 s
	pcm = append(pcm, over(noise(1.5, -43), speechy(rnd, 1.5*rate, -18))...) // 24.5-26 s: speech
	pcm = append(pcm, noise(4, -43)...)
	s := NewSegmenter()
	var got []string
	for _, e := range append(feedAll(s, pcm), s.Flush()...) {
		if !e.Start {
			got = append(got, fmt.Sprintf("%.1f-%.1f", float64(e.From)/rate, float64(e.To)/rate))
		}
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "9.") && !strings.HasPrefix(got[0], "10.") || !strings.HasPrefix(got[1], "24.") {
		t.Fatalf("utterances %v, want the two sentences (10-11.5 s and 24.5-26 s)", got)
	}
}

func TestLiveAudioCtx(t *testing.T) {
	for _, c := range []struct {
		secs float64
		want int
	}{{0.5, 256}, {3, 256}, {4.5, 288}, {8, 480}, {40, 1500}} {
		if got := LiveAudioCtx(int(c.secs*8000), 8000); got != c.want {
			t.Errorf("%.1f s: audio_ctx %d, want %d", c.secs, got, c.want)
		}
	}
}

func TestLiveServerURL(t *testing.T) {
	var lang, format string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			t.Errorf("path %s", r.URL.Path)
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		data, _ := io.ReadAll(f)
		if string(data[:4]) != "RIFF" || len(data) != 44+2*1600 {
			t.Errorf("file: %d bytes", len(data))
		}
		lang, format = r.FormValue("language"), r.FormValue("response_format")
		if r.FormValue("audio_ctx") != "256" || r.FormValue("temperature_inc") != "0" || r.FormValue("no_timestamps") != "true" {
			t.Errorf("options: audio_ctx %q temperature_inc %q no_timestamps %q", r.FormValue("audio_ctx"), r.FormValue("temperature_inc"), r.FormValue("no_timestamps"))
		}
		if format != "verbose_json" || (r.FormValue("no_language_probabilities") == "true") != (lang != "auto") {
			t.Errorf("format %q, no_language_probabilities %q for %q", format, r.FormValue("no_language_probabilities"), lang)
		}
		if lang == "auto" {
			io.WriteString(w, `{"task":"transcribe","language":"english","detected_language":"english","detected_language_probability":0.93,
				"language_probabilities":{"en":0.93,"fr":0.04},"text":" [BLANK_AUDIO] Hello there.",
				"segments":[{"id":0,"text":" [BLANK_AUDIO] Hello there.","avg_logprob":-0.21,"no_speech_prob":0.02}]}`)
			return
		}
		// What it is unsure of is left out: a guess over noise, a
		// "no speech" segment, and a loop cut short.
		io.WriteString(w, `{"language":"english","text":"...","segments":[
			{"text":" Good morning.","avg_logprob":-0.3,"no_speech_prob":0.01},
			{"text":" C'est le bon québéctite.","avg_logprob":-1.4,"no_speech_prob":0.2},
			{"text":" Thank you.","avg_logprob":-0.7,"no_speech_prob":0.8},
			{"text":" How can I help you, you, you, you, you, you?","avg_logprob":-0.4,"no_speech_prob":0.01}]}`)
	}))
	defer srv.Close()
	s := &LiveServer{URL: srv.URL}
	res, err := s.RecognizeLive(context.Background(), make([]int16, 1600), 8000, "auto")
	if err != nil || res.Text != "Hello there." || res.Language != "en" || res.LangProb != 0.93 || lang != "auto" {
		t.Fatalf("auto: %+v %v (%s %s)", res, err, lang, format)
	}
	res, err = s.RecognizeLive(context.Background(), make([]int16, 1600), 8000, "english")
	if err != nil || res.Text != "Good morning. How can I help you…" || res.Language != "" || lang != "english" {
		t.Fatalf("english: %+v %v (%s %s)", res, err, lang, format)
	}
}

// TestLiveText: what the recogniser writes over noise or in a loop (from a
// real call) is cut or left out; ordinary speech stays.
func TestLiveText(t *testing.T) {
	loop := "Je suis" + strings.Repeat(" très", 200)
	for _, c := range []struct {
		in   liveSeg
		want string
	}{
		{liveSeg{Text: " Hello, can you hear me?", AvgLogprob: -0.25, Scored: true}, "Hello, can you hear me?"},
		{liveSeg{Text: loop, AvgLogprob: -0.3, Scored: true}, "Je suis très…"},
		{liveSeg{Text: " No, no, no, that is not right.", AvgLogprob: -0.3, Scored: true}, "No, no, no, that is not right."},
		{liveSeg{Text: " The", AvgLogprob: -0.8, Scored: true}, ""},
		{liveSeg{Text: " Yes.", AvgLogprob: -0.2, Scored: true}, "Yes."},
		{liveSeg{Text: " Thanks for watching!", AvgLogprob: -0.2, Scored: true}, ""},
		{liveSeg{Text: " please, right and see", AvgLogprob: -1.3, Scored: true}, ""},
		{liveSeg{Text: " silence?", AvgLogprob: -0.7, NoSpeech: 0.9, Scored: true}, ""},
		{liveSeg{Text: " Unscored text stays."}, "Unscored text stays."},
		{liveSeg{Text: strings.Repeat("好的", 60)}, ""},
	} {
		if got := liveText([]liveSeg{c.in}); got != c.want {
			t.Errorf("%.40q: %q, want %q", c.in.Text, got, c.want)
		}
	}
}

// TestLiveServerReal starts the real live recogniser when WHISPER_SERVER and
// WHISPER_MODEL point at it.
func TestLiveServerReal(t *testing.T) {
	cmd, model := os.Getenv("WHISPER_SERVER"), os.Getenv("WHISPER_MODEL")
	if cmd == "" || model == "" {
		t.Skip("WHISPER_SERVER and WHISPER_MODEL not set")
	}
	s := &LiveServer{Command: cmd, Model: model, Threads: 2}
	defer s.Stop()
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 2; i++ {
		res, err := s.RecognizeLive(context.Background(), speechy(rnd, 16000, -20), 8000, "auto")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("heard %q (%s %.2f)", res.Text, res.Language, res.LangProb)
	}
	if s.IdleFor() <= 0 {
		t.Fatal("the server is not running")
	}
}
