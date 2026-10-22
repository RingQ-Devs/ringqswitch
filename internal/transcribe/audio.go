package transcribe

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"sort"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// readChannels reads a call recording: each channel's samples and the
// sample rate. A stereo recording (record_stereo) has the caller on the
// left and the other party on the right.
func readChannels(path string) ([][]int16, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, errors.New("not a WAV file")
	}
	var format, channels, bits uint16
	var rate uint32
	var pcm []byte
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4:]))
		body := off + 8
		if size < 0 || body+size > len(data) {
			size = len(data) - body
		}
		chunk := data[body : body+size]
		switch id {
		case "fmt ":
			if len(chunk) >= 16 {
				format = binary.LittleEndian.Uint16(chunk[0:])
				channels = binary.LittleEndian.Uint16(chunk[2:])
				rate = binary.LittleEndian.Uint32(chunk[4:])
				bits = binary.LittleEndian.Uint16(chunk[14:])
				if format == 0xFFFE && len(chunk) >= 26 {
					format = binary.LittleEndian.Uint16(chunk[24:])
				}
			}
		case "data":
			pcm = chunk
		}
		off = body + size + size%2
	}
	if format == 1 && bits == 16 && (channels == 1 || channels == 2) && rate > 0 {
		n := len(pcm) / 2 / int(channels)
		out := make([][]int16, channels)
		for c := range out {
			out[c] = make([]int16, n)
		}
		for i := 0; i < n; i++ {
			for c := 0; c < int(channels); c++ {
				out[c][i] = int16(binary.LittleEndian.Uint16(pcm[(i*int(channels)+c)*2:]))
			}
		}
		return out, int(rate), nil
	}
	// Anything else (A-law, mu-law, 8 bit...): one mixed channel at 8 kHz.
	mono, err := media.DecodeWAV(data)
	if err != nil {
		return nil, 0, err
	}
	return [][]int16{mono}, 8000, nil
}

// writeWAV writes mono 16-bit PCM.
func writeWAV(path string, pcm []int16, rate int) error {
	return os.WriteFile(path, wavBytes(pcm, rate), 0o600)
}

// wavBytes is mono 16-bit PCM as a WAV file.
func wavBytes(pcm []int16, rate int) []byte {
	b := make([]byte, 44+2*len(pcm))
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(pcm)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(pcm)))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(v))
	}
	return b
}

// ---- speech detection

const (
	frameMs     = 20
	hangoverMs  = 400 // speech continues through pauses this short
	minSpeechMs = 160 // shorter bursts are clicks and noise
	padBeforeMs = 250 // context kept around speech
	padAfterMs  = 300
	mergeGapMs  = 700 // regions this close become one
	floorDB     = -50.0
	highPassHz  = 250 // telephone speech is 300-3400 Hz: hum and rumble are not
	toneMs      = 300 // a pitch this steady for this long is a tone, not a voice
)

// region is a stretch of speech in the recording (samples).
type region struct{ start, end int }

func frameDB(x []float64) float64 {
	var sum float64
	for _, f := range x {
		sum += f * f
	}
	if len(x) == 0 || sum == 0 {
		return -120
	}
	return 10 * math.Log10(sum/float64(len(x))/(32768*32768))
}

// highPass filters out what is below the telephone band (second-order
// Butterworth, Q = 1/√2).
func highPass(pcm []int16, rate int, hz float64) []float64 {
	w0 := 2 * math.Pi * hz / float64(rate)
	alpha := math.Sin(w0) / math.Sqrt2
	c := math.Cos(w0)
	a0 := 1 + alpha
	b0, b1, b2 := (1+c)/2/a0, -(1+c)/a0, (1+c)/2/a0
	a1, a2 := -2*c/a0, (1-alpha)/a0
	out := make([]float64, len(pcm))
	var x1, x2, y1, y2 float64
	for i, v := range pcm {
		x := float64(v)
		y := b0*x + b1*x1 + b2*x2 - a1*y1 - a2*y2
		x2, x1, y2, y1 = x1, x, y1, y
		out[i] = y
	}
	return out
}

// pitch estimates the fundamental (80-400 Hz) of the w samples at x by
// normalised autocorrelation: the period in samples and how periodic they
// are (0-1). x holds w samples plus the longest period.
func pitch(x []float64, rate, w int) (int, float64) {
	minLag, maxLag := rate/400, rate/80
	if len(x) < w+maxLag {
		return 0, 0
	}
	var e0 float64
	for _, v := range x[:w] {
		e0 += v * v
	}
	if e0 == 0 {
		return 0, 0
	}
	best, lag := 0.0, 0
	for l := minLag; l <= maxLag; l++ {
		var c, e1 float64
		for i := 0; i < w; i++ {
			c += x[i] * x[i+l]
			e1 += x[i+l] * x[i+l]
		}
		if e1 > 0 {
			if r := c / math.Sqrt(e0*e1); r > best {
				best, lag = r, l
			}
		}
	}
	return lag, best
}

// tonal marks frames inside a steady tone: a buzz, a hum, a vibrating
// phone. A voice is periodic too, but within a few hundred milliseconds
// its pitch moves and its loudness rises and falls with the syllables; a
// tone keeps both. A window is a tone when nearly all its frames are loud
// and periodic, at about the same pitch (±10%) and level (10 dB).
func tonal(hp []float64, rate int, dbs []float64, loud []bool) []bool {
	fl := rate * frameMs / 1000
	lags := make([]int, len(loud))
	for i := range loud {
		if !loud[i] {
			continue
		}
		end := min(len(hp), i*fl+fl+rate/80)
		if lag, c := pitch(hp[i*fl:end], rate, fl); c > 0.5 {
			lags[i] = lag
		}
	}
	span := toneMs / frameMs
	need := span * 4 / 5
	out := make([]bool, len(loud))
	var win []int
	for i := 0; i+span <= len(lags); i++ {
		win = win[:0]
		for _, l := range lags[i : i+span] {
			if l > 0 {
				win = append(win, l)
			}
		}
		if len(win) < need {
			continue
		}
		sort.Ints(win)
		med := float64(win[len(win)/2])
		steady, lo, hi := 0, math.Inf(1), math.Inf(-1)
		for k := i; k < i+span; k++ {
			if l := float64(lags[k]); l > 0 && math.Abs(l-med) <= 0.1*med {
				steady++
				lo, hi = min(lo, dbs[k]), max(hi, dbs[k])
			}
		}
		if steady >= need && hi-lo <= 10 {
			for k := i; k < i+span; k++ {
				out[k] = out[k] || loud[k]
			}
		}
	}
	return out
}

// speechRegions finds where someone speaks on a channel: frames well above
// the channel's noise floor in the telephone band, held through short
// pauses, without steady tones. It also returns how long they spoke (the
// voiced frames).
func speechRegions(pcm []int16, rate int) ([]region, float64) {
	fl := rate * frameMs / 1000
	if fl <= 0 || len(pcm) < fl {
		return nil, 0
	}
	hp := highPass(pcm, rate, highPassHz)
	n := len(pcm) / fl
	dbs := make([]float64, n)
	for i := range dbs {
		dbs[i] = frameDB(hp[i*fl : (i+1)*fl])
	}
	sorted := append([]float64(nil), dbs...)
	sort.Float64s(sorted)
	noise := sorted[len(sorted)*15/100]
	thr := max(noise+10, floorDB)
	loud := make([]bool, n)
	for i, d := range dbs {
		loud[i] = d > thr
	}
	tone := tonal(hp, rate, dbs, loud)
	var out []region
	hang := hangoverMs / frameMs
	start, last, voiced, talk := -1, -1, 0, 0
	flush := func() {
		if start >= 0 && voiced*frameMs >= minSpeechMs {
			out = append(out, region{start: start * fl, end: (last + 1) * fl})
			talk += voiced
		}
		start, voiced = -1, 0
	}
	for i := range dbs {
		if loud[i] && !tone[i] {
			if start < 0 {
				start = i
			}
			last = i
			voiced++
		} else if start >= 0 && i-last > hang {
			flush()
		}
	}
	flush()
	// pad and merge
	pb, pa, mg := rate*padBeforeMs/1000, rate*padAfterMs/1000, rate*mergeGapMs/1000
	var merged []region
	for _, r := range out {
		r.start = max(0, r.start-pb)
		r.end = min(len(pcm), r.end+pa)
		if k := len(merged); k > 0 && r.start-merged[k-1].end <= mg {
			merged[k-1].end = max(merged[k-1].end, r.end)
			continue
		}
		merged = append(merged, r)
	}
	return merged, float64(talk*frameMs) / 1000
}

// ---- turns

const (
	turnGapMs = 2500  // a longer silence ends a turn
	turnMaxMs = 28000 // what the recogniser takes in one go
	tailMs    = 300   // silence after a turn, so its last word is heard out
)

// A turn is a stretch of one channel that is recognised on its own: its
// speech regions, with no speech of the other party between them. What is
// heard in a turn stays in the turn, so the speakers' lines keep their
// order however the recogniser times its words.
type turn struct {
	channel    int
	start, end int // samples
	regs       []region
}

// makeTurns groups each channel's speech regions into turns.
func makeTurns(regs [][]region, rate int) []turn {
	gap, most := rate*turnGapMs/1000, rate*turnMaxMs/1000
	var out []turn
	for c, rs := range regs {
		var cur *turn
		for _, r := range rs {
			if cur != nil && r.start-cur.end <= gap && r.end-cur.start <= most && !speaksBetween(regs, c, cur.end, r.start) {
				cur.end = r.end
				cur.regs = append(cur.regs, r)
				continue
			}
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &turn{channel: c, start: r.start, end: r.end, regs: []region{r}}
		}
		if cur != nil {
			out = append(out, *cur)
		}
	}
	return out
}

// speaksBetween reports whether a channel other than c has speech between
// from and to.
func speaksBetween(regs [][]region, c, from, to int) bool {
	for o, rs := range regs {
		if o == c {
			continue
		}
		for _, r := range rs {
			if r.start < to && r.end > from {
				return true
			}
		}
	}
	return false
}

// turnAudio is a turn's audio, with a short silence after it.
func turnAudio(pcm []int16, t turn, rate int) []int16 {
	out := make([]int16, 0, t.end-t.start+rate*tailMs/1000)
	out = append(out, pcm[t.start:t.end]...)
	return append(out, make([]int16, rate*tailMs/1000)...)
}
