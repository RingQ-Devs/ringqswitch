package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Live transcription: while a call goes on, each party's audio is cut into
// utterances as it arrives (a Segmenter per party) and each utterance is
// recognised at once, by a live recogniser that keeps the model loaded
// (LiveServer), or by the recogniser when the live recogniser is not installed.

const (
	liveRate      = 8000
	liveFrameMs   = 20
	liveStartMs   = 60   // this much speech starts an utterance
	liveMinMs     = 300  // shorter bursts are clicks and noise
	livePreMs     = 300  // audio kept from before the speech started
	liveSilenceMs = 600  // this much silence ends an utterance
	liveMaxMs     = 8000 // longer speech is cut, so text keeps coming
)

// LiveAudioCtx is how much of whisper's 30-second window to compute for an
// utterance of n samples (in 20 ms steps): the utterance and a second more,
// at least 5 s. The encoder's work follows it, so a 3-second sentence is
// recognised about 7 times faster than with the whole window.
func LiveAudioCtx(n, rate int) int {
	ctx := (n*50/rate + 50 + 31) / 32 * 32
	return min(max(ctx, 256), 1500)
}

// LiveEvent is what a Segmenter reports: speech began (Start), or an
// utterance to recognise. From and To are sample offsets in the stream.
type LiveEvent struct {
	Start     bool
	Utterance []int16
	From, To  int64
}

// Segmenter cuts one party's 8 kHz audio into utterances as it arrives.
// Speech is what stands 10 dB above the line's noise floor, so a steady
// background (a noisy mobile line, a fan) is followed up and down instead
// of being taken for one long utterance. The floor is the higher of the
// quietest frames of the last 1.5 s (it learns a new background quickly)
// and of the last 8 s (a short dip in the background does not make the
// usual noise look loud).
type Segmenter struct {
	hp       biquad
	noise    float64
	recent   []float64 // the last 8 s of frame levels (dB), a ring
	ring     int
	frameN   int
	pre      [][]int16
	preDB    []float64
	lv       levels // the levels of the utterance's loud frames
	going    bool   // the last utterance was cut at liveMaxMs: this one goes on with the same speech
	buf      []int16
	speaking bool
	told     bool // the Start event was sent
	loudRun  int
	quietRun int
	voiced   int
	start    int64
	pos      int64
}

const (
	liveFloorFrames = 400 // 8 s of 20 ms frames
	liveShortFrames = 75  // 1.5 s
	liveDropoutDB   = -85 // digital silence (packet loss): not the line's noise
)

// NewSegmenter starts a segmenter.
func NewSegmenter() *Segmenter {
	return &Segmenter{hp: newHighPass(liveRate, highPassHz), noise: -60}
}

// levels gathers frame levels: speech rises and falls with its syllables
// (6 to 16 dB apart), a steady noise does not (1 to 2 dB).
type levels struct{ sum, sq, n float64 }

func (l *levels) add(db float64) {
	if db > liveDropoutDB {
		l.sum, l.sq, l.n = l.sum+db, l.sq+db*db, l.n+1
	}
}

func (l *levels) steady() bool {
	if l.n < 2 {
		return true
	}
	m := l.sum / l.n
	return math.Sqrt(max(0, l.sq/l.n-m*m)) < liveSteadyDB
}

const liveSteadyDB = 3

// floor updates the noise floor from the recent frame levels.
func (s *Segmenter) floor(db float64) {
	if db > liveDropoutDB {
		if len(s.recent) < liveFloorFrames {
			s.recent = append(s.recent, db)
		} else {
			s.recent[s.ring] = db
			s.ring = (s.ring + 1) % liveFloorFrames
		}
	}
	s.frameN++
	if len(s.recent) < 25 || s.frameN%5 != 0 { // every 100 ms, after half a second
		return
	}
	pct := func(xs []float64, p int) float64 {
		sorted := append([]float64(nil), xs...)
		sort.Float64s(sorted)
		return sorted[len(sorted)*p/100]
	}
	// The last 1.5 s, in time order around the ring.
	n := len(s.recent)
	last := make([]float64, 0, liveShortFrames)
	for i := 0; i < min(liveShortFrames, n); i++ {
		last = append(last, s.recent[((s.ring-1-i)%n+n)%n])
	}
	if n < liveFloorFrames {
		last = s.recent[max(0, n-liveShortFrames):]
	}
	s.noise = min(max(max(pct(last, 10), pct(s.recent, 20)), -90), -20)
}

// Feed takes the next frame of audio.
func (s *Segmenter) Feed(frame []int16) []LiveEvent {
	if len(frame) == 0 {
		return nil
	}
	f := s.hp.apply(frame)
	db := frameDB(f)
	s.floor(db)
	thr := max(s.noise+10, floorDB)
	if s.speaking {
		thr -= 3 // the ends of words are quieter
	}
	loud := db > thr
	frames := func(ms int) int { return ms / liveFrameMs }
	var out []LiveEvent
	if !s.speaking {
		s.pre = append(s.pre, frame)
		s.preDB = append(s.preDB, db)
		if len(s.pre) > frames(livePreMs) {
			s.pre, s.preDB = s.pre[1:], s.preDB[1:]
		}
		if loud {
			s.loudRun++
		} else {
			s.loudRun = 0
		}
		if s.loudRun >= frames(liveStartMs) {
			s.speaking, s.voiced, s.quietRun = true, s.loudRun, 0
			s.buf = s.buf[:0]
			for _, p := range s.pre {
				s.buf = append(s.buf, p...)
			}
			s.start = s.pos + int64(len(frame)) - int64(len(s.buf))
			s.lv = levels{}
			for _, d := range s.preDB[len(s.preDB)-s.loudRun:] {
				s.lv.add(d)
			}
			s.pre, s.preDB = nil, nil
			s.told = false
		}
	} else {
		s.buf = append(s.buf, frame...)
		if loud {
			s.lv.add(db)
			s.voiced++
			s.quietRun = 0
		} else {
			s.quietRun++
		}
		if !s.told && s.voiced*liveFrameMs >= liveMinMs && (s.going || !s.lv.steady()) { // speech, not a click or a steady noise
			s.told = true
			out = append(out, LiveEvent{Start: true, From: s.start})
		}
		if long := len(s.buf) >= liveRate*liveMaxMs/1000; s.quietRun >= frames(liveSilenceMs) || long {
			e, ok := s.end()
			if ok {
				out = append(out, e)
			}
			s.going = long && ok
		}
	}
	s.pos += int64(len(frame))
	return out
}

// Spoken is the length of the utterance being spoken (0 when none, or not
// yet known to be speech).
func (s *Segmenter) Spoken() int {
	if !s.speaking || !s.told {
		return 0
	}
	return len(s.buf)
}

// Current is the utterance being spoken, once it is speech (for text
// shown while the speaker goes on).
func (s *Segmenter) Current() (pcm []int16, from int64, ok bool) {
	if !s.speaking || !s.told {
		return nil, 0, false
	}
	return append([]int16(nil), s.buf...), s.start, true
}

// Flush ends the utterance in progress (the call ended).
func (s *Segmenter) Flush() []LiveEvent {
	if !s.speaking {
		return nil
	}
	if e, ok := s.end(); ok {
		return []LiveEvent{e}
	}
	return nil
}

func (s *Segmenter) end() (LiveEvent, bool) {
	fl := liveRate * liveFrameMs / 1000
	// Keep 300 ms of the silence after the speech.
	if extra := s.quietRun - livePreMs/liveFrameMs; extra > 0 {
		s.buf = s.buf[:max(0, len(s.buf)-extra*fl)]
	}
	e := LiveEvent{Utterance: append([]int16(nil), s.buf...), From: s.start, To: s.start + int64(len(s.buf))}
	ok := s.told && s.voiced*liveFrameMs >= liveMinMs
	s.speaking, s.loudRun, s.quietRun, s.voiced = false, 0, 0, 0
	s.buf = s.buf[:0]
	return e, ok
}

// biquad is a second-order filter that keeps its state between frames.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
}

func newHighPass(rate int, hz float64) biquad {
	w0 := 2 * math.Pi * hz / float64(rate)
	alpha := math.Sin(w0) / math.Sqrt2
	c := math.Cos(w0)
	a0 := 1 + alpha
	return biquad{b0: (1 + c) / 2 / a0, b1: -(1 + c) / a0, b2: (1 + c) / 2 / a0, a1: -2 * c / a0, a2: (1 - alpha) / a0}
}

func (q *biquad) apply(in []int16) []float64 {
	out := make([]float64, len(in))
	for i, v := range in {
		x := float64(v)
		y := q.b0*x + q.b1*q.x1 + q.b2*q.x2 - q.a1*q.y1 - q.a2*q.y2
		q.x2, q.x1, q.y2, q.y1 = q.x1, x, q.y1, y
		out[i] = y
	}
	return out
}

// LiveRecognizer recognises one utterance. language is a code ("en"), a
// whisper language name ("english") or "auto" (then the result says which
// language it heard, and how sure it is). Text the recogniser is unsure of
// is left out (liveText).
type LiveRecognizer interface {
	RecognizeLive(ctx context.Context, pcm []int16, rate int, language string) (LiveResult, error)
}

// speechServerCommands are where install-transcription.sh (and the
// recogniser's own build, or an install before 0.10.1) put the live
// recogniser.
var speechServerCommands = []string{"/usr/local/bin/rq-speech-server", "/usr/local/bin/whisper-server",
	"/opt/whisper.cpp/build-ringq/bin/whisper-server", "/opt/whisper.cpp/build/bin/whisper-server", "rq-speech-server", "whisper-server"}

// FindServerCommand finds the live recogniser next to the recogniser or
// where it is usually installed ("" when it is not installed).
func FindServerCommand(cli string) string {
	if cli != "" {
		for _, name := range []string{"rq-speech-server", "whisper-server"} {
			if p := filepath.Join(filepath.Dir(cli), name); fileExists(p) {
				return p
			}
		}
	}
	for _, c := range speechServerCommands {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// LiveServer runs the live recogniser on this machine (127.0.0.1, a free port)
// with one model, started on first use and kept while it is used. With URL
// set, it uses that live recogniser instead (started elsewhere).
type LiveServer struct {
	Command, Model string
	Threads        int
	URL            string

	starting sync.Mutex // one start at a time (loading a model takes a while)
	mu       sync.Mutex // the fields below
	url      string
	proc     *exec.Cmd
	exited   chan struct{}
	stderr   *tailBuffer
	lastUse  time.Time
	warmed   bool
	warming  bool
	inflight atomic.Int32
}

// running is the server's address while it runs.
func (s *LiveServer) running() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUse = time.Now()
	if s.proc == nil {
		return "", false
	}
	select {
	case <-s.exited:
		s.proc = nil // it died: start it again
		return "", false
	default:
		return s.url, true
	}
}

func (s *LiveServer) ensure(ctx context.Context) (string, error) {
	if s.URL != "" {
		return strings.TrimSuffix(s.URL, "/"), nil
	}
	if url, ok := s.running(); ok {
		return url, nil
	}
	s.starting.Lock()
	defer s.starting.Unlock()
	if url, ok := s.running(); ok {
		return url, nil // started meanwhile
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	args := []string{"-m", s.Model, "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	if s.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(s.Threads))
	}
	cmd := exec.Command(s.Command, args...)
	stderr := &tailBuffer{}
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("live recogniser: %w", err)
	}
	livePriority(cmd.Process.Pid) // ahead of transcripts after calls, behind calls
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	url := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(90 * time.Second) // loading a large model takes a while
	for {
		select {
		case <-exited:
			return "", fmt.Errorf("live recogniser stopped: %s", lastLine(stderr.String()))
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return "", ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		if resp, err := http.Get(url + "/"); err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return "", errors.New("the live recogniser did not start in time")
		}
	}
	s.mu.Lock()
	s.proc, s.exited, s.url, s.stderr, s.lastUse = cmd, exited, url, stderr, time.Now()
	s.mu.Unlock()
	return url, nil
}

// RecognizeLive sends an utterance to the live recogniser. Canceling ctx stops
// the recognition (the live recogniser gives up when the request goes away).
func (s *LiveServer) RecognizeLive(ctx context.Context, pcm []int16, rate int, language string) (LiveResult, error) {
	url, err := s.ensure(ctx)
	if err != nil {
		return LiveResult{}, err
	}
	s.inflight.Add(1)
	defer s.inflight.Add(-1)
	detect := language == "" || language == "auto"
	if detect {
		language = "auto"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "utterance.wav")
	_, _ = fw.Write(wavBytes(pcm, rate))
	_ = mw.WriteField("language", language)
	// verbose_json: each segment's scores (to leave out what it is unsure
	// of), and the language heard with its probability when asked to detect
	// it (that costs one more pass, so only then).
	_ = mw.WriteField("response_format", "verbose_json")
	_ = mw.WriteField("no_language_probabilities", strconv.FormatBool(!detect))
	_ = mw.WriteField("temperature", "0")
	// Live, an answer now beats a better one later: only the part of the
	// window the utterance needs, no retries at higher temperatures (each
	// one decodes again; loops are cut by liveText), no timestamps, no
	// "[music]" and the like.
	_ = mw.WriteField("temperature_inc", "0")
	_ = mw.WriteField("audio_ctx", strconv.Itoa(LiveAudioCtx(len(pcm), rate)))
	_ = mw.WriteField("no_timestamps", "true")
	_ = mw.WriteField("suppress_nst", "true")
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/inference", &body)
	if err != nil {
		return LiveResult{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return LiveResult{}, fmt.Errorf("live recogniser: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return LiveResult{}, fmt.Errorf("live recogniser: %s: %s", resp.Status, lastLine(string(data)))
	}
	var r struct {
		Text     string             `json:"text"`
		Language string             `json:"language"`
		Probs    map[string]float64 `json:"language_probabilities"`
		Error    string             `json:"error"`
		Segments []struct {
			Text       string   `json:"text"`
			AvgLogprob *float64 `json:"avg_logprob"`
			NoSpeech   *float64 `json:"no_speech_prob"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return LiveResult{}, fmt.Errorf("live recogniser answer: %w", err)
	}
	if r.Error != "" {
		return LiveResult{}, errors.New("live recogniser: " + r.Error)
	}
	var segs []liveSeg
	for _, sg := range r.Segments {
		ls := liveSeg{Text: sg.Text}
		if sg.AvgLogprob != nil {
			ls.AvgLogprob, ls.Scored = *sg.AvgLogprob, true
		}
		if sg.NoSpeech != nil {
			ls.NoSpeech = *sg.NoSpeech
		}
		segs = append(segs, ls)
	}
	if len(segs) == 0 { // an older live recogniser: the text alone
		segs = []liveSeg{{Text: r.Text}}
	}
	out := LiveResult{Text: liveText(segs)}
	if detect {
		out.Language, out.LangProb = r.Language, 1
		best := 0.0
		for code, p := range r.Probs {
			if p > best {
				best, out.Language, out.LangProb = p, code, p
			}
		}
	}
	return out, nil
}

// InFlight is the number of utterances being recognised now.
func (s *LiveServer) InFlight() int { return int(s.inflight.Load()) }

// Warm starts the server and runs it once on a second of silence, so the
// first words of the next call are not kept waiting for the model to load.
func (s *LiveServer) Warm(ctx context.Context) error {
	s.mu.Lock()
	skip := s.warming || s.warmed && s.proc != nil
	s.warming = !skip
	s.mu.Unlock()
	if skip {
		return nil
	}
	_, err := s.RecognizeLive(ctx, make([]int16, liveRate), liveRate, "en")
	s.mu.Lock()
	s.warmed, s.warming = err == nil, false
	s.mu.Unlock()
	return err
}

// Running reports whether the server's process is running.
func (s *LiveServer) Running() bool {
	_, ok := s.running()
	return ok
}

// IdleFor reports how long the server has not been used (0 when it is not
// running).
func (s *LiveServer) IdleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return 0
	}
	return time.Since(s.lastUse)
}

// Stop stops the server.
func (s *LiveServer) Stop() {
	s.mu.Lock()
	proc, exited := s.proc, s.exited
	s.proc, s.warmed = nil, false
	s.mu.Unlock()
	if proc != nil {
		_ = proc.Process.Kill()
		<-exited
	}
}

// LiveCLI recognises utterances with the recogniser (the model is loaded for
// each one: slower than the live recogniser).
type LiveCLI struct {
	CLI     *SpeechCLI
	TempDir string
}

// RecognizeLive runs the recogniser on an utterance.
func (l *LiveCLI) RecognizeLive(ctx context.Context, pcm []int16, rate int, language string) (LiveResult, error) {
	dir, err := os.MkdirTemp(l.TempDir, "rq-live-")
	if err != nil {
		return LiveResult{}, err
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "utterance.wav")
	if err := writeWAV(f, pcm, rate); err != nil {
		return LiveResult{}, err
	}
	w := *l.CLI
	w.Extra = []string{"-ac", strconv.Itoa(LiveAudioCtx(len(pcm), rate)), "-nf"}
	recs, err := w.Recognize(ctx, []string{f}, language)
	if err != nil || len(recs) == 0 {
		return LiveResult{}, err
	}
	var segs []liveSeg
	for _, sg := range recs[0].Segments {
		segs = append(segs, liveSeg{Text: sg.Text})
	}
	out := LiveResult{Text: liveText(segs)}
	if language == "" || language == "auto" {
		out.Language, out.LangProb = recs[0].Language, 1
	}
	return out, nil
}

// tailBuffer keeps the last few kilobytes written to it.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 8192 {
		t.b = t.b[len(t.b)-8192:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
