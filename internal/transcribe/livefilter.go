package transcribe

import (
	"bytes"
	"compress/flate"
	"strings"
	"unicode"
)

// What the recogniser writes is checked before it is shown live: over noise,
// or in the wrong language, whisper invents words ("Je suis très très
// très...", "Thanks for watching!") with low confidence, and a decoder caught
// in a loop repeats a word until it runs out of room.

// LiveResult is what was recognised in an utterance.
type LiveResult struct {
	Text string
	// Language is the language heard (a code such as "en" when the
	// recogniser reports one, else its name), with how sure it was (1 when
	// it does not say). Empty when the language was given.
	Language string
	LangProb float64
}

// liveSeg is one segment of the recogniser's answer, with its scores when
// it reports them.
type liveSeg struct {
	Text       string
	AvgLogprob float64
	NoSpeech   float64
	Scored     bool
}

const (
	liveMinLogprob   = -1.0 // below: whisper itself counts the decoding as failed
	liveNoSpeechProb = 0.6
	liveMaxRatio     = 2.4 // compression ratio of looping text (whisper's own limit)
)

// youTube-style captions whisper learnt from and writes over silence.
var livePhantoms = []string{"thanks for watching", "thank you for watching", "please subscribe", "like and subscribe",
	"subtitles by", "subtitled by", "captions by", "amara.org", "transcribed by"}

// liveText keeps what the recogniser is sure of: segments it scored low (or
// as no speech) go, loops are cut, and phantom captions dropped.
func liveText(segs []liveSeg) string {
	var parts []string
	for _, sg := range segs {
		t := cleanText(sg.Text)
		if t == "" {
			continue
		}
		if sg.Scored {
			if sg.AvgLogprob < liveMinLogprob || sg.NoSpeech > liveNoSpeechProb && sg.AvgLogprob < -0.5 {
				continue
			}
			// A word or two the recogniser doubts is noise more often than not.
			if len(strings.Fields(t)) <= 2 && sg.AvgLogprob < -0.6 {
				continue
			}
		}
		parts = append(parts, t)
	}
	text := cutRepeats(strings.Join(parts, " "))
	low := strings.ToLower(text)
	for _, p := range livePhantoms {
		if strings.Contains(low, p) {
			return ""
		}
	}
	if len(text) > 40 && compressionRatio(text) > liveMaxRatio {
		return "" // still looping
	}
	return text
}

// cutRepeats shortens a word or phrase repeated 4 or more times in a row to
// one ("très très très très..." → "très…"); "no, no, no" stays.
func cutRepeats(text string) string {
	words := strings.Fields(text)
	norm := make([]string, len(words))
	for i, w := range words {
		norm[i] = strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
	}
	same := func(a, b, n int) bool {
		for k := 0; k < n; k++ {
			if norm[a+k] != norm[b+k] || norm[a+k] == "" {
				return false
			}
		}
		return true
	}
	var out []string
	cut := false
	for i := 0; i < len(words); {
		done := false
		for n := 1; n <= 8 && i+4*n <= len(words); n++ {
			k := 1
			for i+(k+1)*n <= len(words) && same(i, i+k*n, n) {
				k++
			}
			if k >= 4 {
				out = append(out, words[i:i+n]...)
				i += k * n
				cut, done = true, true
				break
			}
		}
		if !done {
			out = append(out, words[i])
			i++
		}
	}
	s := strings.Join(out, " ")
	if cut {
		s = strings.TrimRight(s, " ,.") + "…"
	}
	return s
}

// compressionRatio is how much the text shrinks when compressed: looping
// text shrinks a lot.
func compressionRatio(s string) float64 {
	var b bytes.Buffer
	w, _ := flate.NewWriter(&b, flate.BestCompression)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	if b.Len() == 0 {
		return 0
	}
	return float64(len(s)) / float64(b.Len())
}
