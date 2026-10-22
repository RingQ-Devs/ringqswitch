// Package transcribe turns a call recording into a conversation: who
// said what and when (Speaker A, Speaker B), with the sentiment of each
// utterance, of each speaker and of the call.
//
// Speakers are told apart by the recording itself, not guessed: calls are
// recorded in stereo with each party on its own channel (Speaker A, the
// caller, on the left; Speaker B, who answered, on the right). Each
// channel's speech is found and cut into turns (what a party says until the
// other one speaks); each turn is recognised on its own on this server
// (the speech recogniser), so what was said stays where it was said, and the two
// sides are interleaved by time.
package transcribe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Options configures Transcribe.
type Options struct {
	Recognizer Recognizer
	Language   string // "auto", "en", "ms"...
	TempDir    string
	// Customer is the speaker who is the customer (the outside party): "A"
	// for an incoming call, "B" for an outgoing one, "" when internal.
	Customer string
}

// Segment is one utterance.
type Segment struct {
	Speaker   string  `json:"speaker"` // "A", "B"; "" for a mono recording
	Start     float64 `json:"start"`   // seconds into the recording
	End       float64 `json:"end"`
	Text      string  `json:"text"`
	Sentiment float64 `json:"sentiment"` // -1 to +1
}

// Speaker sums up one party.
type Speaker struct {
	Speaker   string  `json:"speaker"`
	Talk      float64 `json:"talk_seconds"`
	Words     int     `json:"words"`
	Sentiment float64 `json:"sentiment"`
	Label     string  `json:"label"`
	Ending    float64 `json:"ending"` // the last third of what they said
	Positive  int     `json:"positive"`
	Neutral   int     `json:"neutral"`
	Negative  int     `json:"negative"`
}

// Result is a transcribed call.
type Result struct {
	Language  string    `json:"language"`
	Duration  float64   `json:"duration"`
	Stereo    bool      `json:"stereo"`
	Segments  []Segment `json:"segments"`
	Speakers  []Speaker `json:"speakers"`
	Sentiment float64   `json:"sentiment"`
	Label     string    `json:"label"`
	Customer  string    `json:"customer,omitempty"`
}

// Text is the transcript as plain text.
func (r *Result) Text(names map[string]string) string {
	var b strings.Builder
	for _, s := range r.Segments {
		who := names[s.Speaker]
		if who == "" {
			who = "Speaker " + s.Speaker
			if s.Speaker == "" {
				who = "Speaker"
			}
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", clock(s.Start), who, s.Text)
	}
	return b.String()
}

func clock(sec float64) string {
	s := int(sec)
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// ErrNoSpeech: the recording holds no speech.
var ErrNoSpeech = errors.New("no speech in the recording")

const joinGap = 1.0 // seconds: one speaker's pieces closer than this are one utterance

// Transcribe transcribes a recording.
func Transcribe(ctx context.Context, path string, opt Options) (*Result, error) {
	chans, rate, err := readChannels(path)
	if err != nil {
		return nil, err
	}
	res := &Result{Stereo: len(chans) == 2, Customer: opt.Customer}
	if len(chans) > 0 {
		res.Duration = float64(len(chans[0])) / float64(rate)
	}
	names := []string{""}
	if res.Stereo {
		names = []string{"A", "B"}
	} else {
		res.Customer = ""
	}
	talk := map[string]float64{}
	regs := make([][]region, len(chans))
	for c, pcm := range chans {
		var voiced float64
		regs[c], voiced = speechRegions(pcm, rate)
		talk[names[c]] = voiced
	}
	turns := makeTurns(regs, rate)
	if len(turns) == 0 {
		return res, ErrNoSpeech
	}
	dir, err := os.MkdirTemp(opt.TempDir, "rq-transcribe-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	files := make([]string, len(turns))
	for i, t := range turns {
		files[i] = filepath.Join(dir, fmt.Sprintf("turn%04d.wav", i))
		if err := writeWAV(files[i], turnAudio(chans[t.channel], t, rate), rate); err != nil {
			return nil, err
		}
	}
	recs, err := recognizeTurns(ctx, opt, turns, files)
	if err != nil {
		return nil, err
	}
	votes := map[string]float64{}
	var pieces []Segment
	for i, t := range turns {
		if recs[i] == nil {
			continue
		}
		votes[recs[i].Language] += float64(t.end - t.start)
		for _, s := range recs[i].Segments {
			if p, ok := place(s, t, rate, names[t.channel]); ok {
				pieces = append(pieces, p)
			}
		}
	}
	best := 0.0
	for l, v := range votes {
		if l != "" && l != "auto" && v > best {
			res.Language, best = l, v
		}
	}
	res.Segments = interleave(pieces)
	for i := range res.Segments {
		res.Segments[i].Sentiment = Sentiment(res.Segments[i].Text)
	}
	if len(res.Segments) == 0 {
		return res, ErrNoSpeech
	}
	res.summarize(names, talk)
	return res, nil
}

// recognizeTurns recognises the turns' files. When the language is to be
// detected, each speaker's language is found on their longest turn, and
// their other turns are recognised in it: a word or two alone ("Hello?")
// is easily taken for another language.
func recognizeTurns(ctx context.Context, opt Options, turns []turn, files []string) ([]*Recognition, error) {
	recs := make([]*Recognition, len(turns))
	run := func(idx []int, language string) error {
		if len(idx) == 0 {
			return nil
		}
		in := make([]string, len(idx))
		for k, i := range idx {
			in[k] = files[i]
		}
		out, err := opt.Recognizer.Recognize(ctx, in, language)
		if err != nil {
			return err
		}
		if len(out) != len(idx) {
			return fmt.Errorf("the recogniser returned %d results for %d files", len(out), len(idx))
		}
		for k, i := range idx {
			recs[i] = out[k]
		}
		return nil
	}
	language := strings.ToLower(strings.TrimSpace(opt.Language))
	if language == "" {
		language = "auto"
	}
	if language != "auto" {
		all := make([]int, len(turns))
		for i := range all {
			all[i] = i
		}
		return recs, run(all, language)
	}
	longest := map[int]int{} // channel: turn
	for i, t := range turns {
		if j, ok := longest[t.channel]; !ok || t.end-t.start > turns[j].end-turns[j].start {
			longest[t.channel] = i
		}
	}
	var first []int
	for _, i := range longest {
		first = append(first, i)
	}
	sort.Ints(first)
	if err := run(first, "auto"); err != nil {
		return nil, err
	}
	spoken := map[int]string{} // channel: language
	for c, i := range longest {
		spoken[c] = "auto"
		if l := recs[i].Language; l != "" {
			spoken[c] = l
		}
	}
	rest := map[string][]int{}
	for i, t := range turns {
		if recs[i] == nil {
			rest[spoken[t.channel]] = append(rest[spoken[t.channel]], i)
		}
	}
	langs := make([]string, 0, len(rest))
	for l := range rest {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		if err := run(rest[l], l); err != nil {
			return nil, err
		}
	}
	return recs, nil
}

var (
	annotation = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\*[^*]*\*|♪+`)
	spaces     = regexp.MustCompile(`\s+`)
)

// cleanText drops what the recogniser adds that nobody said:
// [BLANK_AUDIO], (music), *laughs*.
func cleanText(s string) string {
	s = annotation.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return s
		}
	}
	return ""
}

// place puts a segment recognised in a turn at its time in the recording.
// It stays inside the turn whatever the recogniser's timing.
func place(s RecSegment, t turn, rate int, speaker string) (Segment, bool) {
	text := cleanText(s.Text)
	if text == "" {
		return Segment{}, false
	}
	sec := func(n int) float64 { return float64(n) / float64(rate) }
	from, to := sec(t.start), sec(t.end)
	start := min(max(from+s.From, from), to)
	end := min(max(from+s.To, start), to)
	// Recognisers invent words over silence ("Thank you."): keep only what
	// falls on (or next to) this party's speech.
	const slack = 0.5
	for _, r := range t.regs {
		if sec(r.start)-slack <= end && sec(r.end)+slack >= start {
			return Segment{Speaker: speaker, Start: round2(start), End: round2(end), Text: text}, true
		}
	}
	return Segment{}, false
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// interleave orders both speakers' pieces by time, joining a speaker's
// consecutive pieces into one utterance.
func interleave(pieces []Segment) []Segment {
	sort.SliceStable(pieces, func(i, j int) bool { return pieces[i].Start < pieces[j].Start })
	var out []Segment
	for _, p := range pieces {
		if k := len(out); k > 0 && out[k-1].Speaker == p.Speaker && p.Start-out[k-1].End < joinGap {
			out[k-1].Text += " " + p.Text
			out[k-1].End = max(out[k-1].End, p.End)
			continue
		}
		out = append(out, p)
	}
	return out
}

func words(s string) int {
	n := len(strings.Fields(s))
	// Chinese, Japanese, Thai... have no spaces: count characters.
	if r := len([]rune(s)); n <= 1 && r > 8 {
		n = r / 2
	}
	return max(n, 1)
}

// weighted is the word-weighted mean sentiment of segments.
func weighted(segs []Segment) float64 {
	var sum, n float64
	for _, s := range segs {
		w := float64(words(s.Text))
		sum += s.Sentiment * w
		n += w
	}
	if n == 0 {
		return 0
	}
	return round4(sum / n)
}

func (r *Result) summarize(names []string, talk map[string]float64) {
	for _, name := range names {
		var mine []Segment
		sp := Speaker{Speaker: name, Talk: round2(talk[name])}
		for _, s := range r.Segments {
			if s.Speaker != name {
				continue
			}
			mine = append(mine, s)
			sp.Words += words(s.Text)
			switch Label(s.Sentiment) {
			case "positive":
				sp.Positive++
			case "negative":
				sp.Negative++
			default:
				sp.Neutral++
			}
		}
		sp.Sentiment = weighted(mine)
		sp.Label = Label(sp.Sentiment)
		if n := len(mine); n > 0 {
			sp.Ending = weighted(mine[n-(n+2)/3:])
		}
		r.Speakers = append(r.Speakers, sp)
	}
	r.Sentiment = weighted(r.Segments)
	r.Label = Label(r.Sentiment)
}
