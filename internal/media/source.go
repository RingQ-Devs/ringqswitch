package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// Source produces 8 kHz mono linear audio.
type Source interface {
	// Read fills buf and returns the number of samples written, or io.EOF.
	Read(buf []int16) (int, error)
}

// SampleSource plays a fixed buffer of samples.
type SampleSource struct {
	samples []int16
	pos     int
}

// NewSampleSource wraps samples.
func NewSampleSource(s []int16) *SampleSource { return &SampleSource{samples: s} }

func (s *SampleSource) Read(buf []int16) (int, error) {
	if s.pos >= len(s.samples) {
		return 0, io.EOF
	}
	n := copy(buf, s.samples[s.pos:])
	s.pos += n
	return n, nil
}

// Silence returns a source producing ms milliseconds of silence.
func Silence(ms int) Source { return NewSampleSource(make([]int16, ms*8)) }

// ---------------------------------------------------------------------------
// WAV files

// LoadWAV reads a RIFF/WAVE file (PCM 8/16 bit, mu-law or A-law, any sample
// rate, mono or stereo) and converts it to 8 kHz mono.
func LoadWAV(path string) ([]int16, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodeWAV(data)
}

// DecodeWAV decodes a WAV file held in memory.
func DecodeWAV(data []byte) ([]int16, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("wav: not a RIFF/WAVE file")
	}
	var format, channels, bits uint16
	var rate uint32
	var pcm []byte
	haveFmt := false
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
			if len(chunk) < 16 {
				return nil, errors.New("wav: short fmt chunk")
			}
			format = binary.LittleEndian.Uint16(chunk[0:])
			channels = binary.LittleEndian.Uint16(chunk[2:])
			rate = binary.LittleEndian.Uint32(chunk[4:])
			bits = binary.LittleEndian.Uint16(chunk[14:])
			if format == 0xFFFE && len(chunk) >= 26 {
				format = binary.LittleEndian.Uint16(chunk[24:])
			}
			haveFmt = true
		case "data":
			pcm = chunk
		}
		off = body + size + size%2
	}
	if !haveFmt || pcm == nil {
		return nil, errors.New("wav: missing fmt or data chunk")
	}
	if channels == 0 || rate == 0 {
		return nil, errors.New("wav: invalid format")
	}
	var samples []int16
	switch {
	case format == 1 && bits == 16:
		for i := 0; i+1 < len(pcm); i += 2 {
			samples = append(samples, int16(binary.LittleEndian.Uint16(pcm[i:])))
		}
	case format == 1 && bits == 8:
		for _, b := range pcm {
			samples = append(samples, int16(int(b)-128)<<8)
		}
	case format == 7 && bits == 8:
		for _, b := range pcm {
			samples = append(samples, ulawDecode[b])
		}
	case format == 6 && bits == 8:
		for _, b := range pcm {
			samples = append(samples, alawDecode[b])
		}
	default:
		return nil, fmt.Errorf("wav: unsupported format %d/%d-bit", format, bits)
	}
	if channels > 1 {
		mono := make([]int16, 0, len(samples)/int(channels))
		for i := 0; i+int(channels) <= len(samples); i += int(channels) {
			sum := 0
			for c := 0; c < int(channels); c++ {
				sum += int(samples[i+c])
			}
			mono = append(mono, int16(sum/int(channels)))
		}
		samples = mono
	}
	return Resample(samples, int(rate), 8000), nil
}

// Resample converts between sample rates using a box filter when
// downsampling and linear interpolation otherwise.
func Resample(in []int16, from, to int) []int16 {
	if from == to || len(in) == 0 {
		return in
	}
	n := int(int64(len(in)) * int64(to) / int64(from))
	out := make([]int16, n)
	ratio := float64(from) / float64(to)
	for i := range out {
		pos := float64(i) * ratio
		if ratio > 1 {
			start, end := int(pos), int(pos+ratio)
			if end > len(in) {
				end = len(in)
			}
			sum := 0
			for j := start; j < end; j++ {
				sum += int(in[j])
			}
			if end > start {
				out[i] = int16(sum / (end - start))
			}
			continue
		}
		j := int(pos)
		frac := pos - float64(j)
		a := float64(in[j])
		b := a
		if j+1 < len(in) {
			b = float64(in[j+1])
		}
		out[i] = int16(a + (b-a)*frac)
	}
	return out
}

// WAVWriter writes 8 kHz 16-bit WAV files (mono, or stereo with
// interleaved samples).
type WAVWriter struct {
	f        *os.File
	channels int
	samples  int // total samples written (all channels)
}

// CreateWAV creates a mono WAV file for writing.
func CreateWAV(path string) (*WAVWriter, error) { return CreateWAVChannels(path, 1) }

// CreateWAVChannels creates a WAV file with 1 or 2 channels.
func CreateWAVChannels(path string, channels int) (*WAVWriter, error) {
	if channels != 2 {
		channels = 1
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &WAVWriter{f: f, channels: channels}
	if _, err := f.Write(wavHeader(0, channels)); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

func wavHeader(samples, channels int) []byte {
	h := make([]byte, 44)
	dataLen := uint32(samples * 2)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+dataLen)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], 8000)
	binary.LittleEndian.PutUint32(h[28:], uint32(16000*channels))
	binary.LittleEndian.PutUint16(h[32:], uint16(2*channels))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataLen)
	return h
}

// Write appends samples (interleaved for stereo files).
func (w *WAVWriter) Write(pcm []int16) error {
	b := make([]byte, 2*len(pcm))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	_, err := w.f.Write(b)
	w.samples += len(pcm)
	return err
}

// Samples returns the number of samples written per channel.
func (w *WAVWriter) Samples() int { return w.samples / max(1, w.channels) }

// Close finalises the header and closes the file.
func (w *WAVWriter) Close() error {
	if _, err := w.f.WriteAt(wavHeader(w.samples, max(1, w.channels)), 0); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

// ---------------------------------------------------------------------------
// Tone generation

type toneSegment struct {
	onMs, offMs int
	freqs       []float64
}

// ToneSource generates tones described in teletone syntax:
//
//	%(on_ms,off_ms,freq1[,freq2...])[;%(...)][;loops=N][;v=-dB]
//
// loops=-1 repeats forever. Example: "%(2000,4000,440,480)" is US ringback.
type ToneSource struct {
	segs   []toneSegment
	loops  int
	amp    float64
	seg    int
	pos    int // sample position inside the current segment
	played int // completed loops
	phase  []float64
}

// ParseTone parses a tone specification.
func ParseTone(spec string) (*ToneSource, error) {
	t := &ToneSource{loops: 1, amp: dbToAmp(-10)}
	for _, part := range strings.Split(spec, ";") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.HasPrefix(part, "loops="):
			n, err := strconv.Atoi(part[6:])
			if err != nil {
				return nil, fmt.Errorf("tone: bad loops %q", part)
			}
			t.loops = n
		case strings.HasPrefix(part, "v="):
			db, err := strconv.ParseFloat(part[2:], 64)
			if err != nil || db > 0 {
				return nil, fmt.Errorf("tone: bad volume %q", part)
			}
			t.amp = dbToAmp(db)
		case strings.HasPrefix(part, "%(") && strings.HasSuffix(part, ")"):
			fields := strings.Split(part[2:len(part)-1], ",")
			if len(fields) < 2 {
				return nil, fmt.Errorf("tone: bad segment %q", part)
			}
			var nums []float64
			for _, f := range fields {
				v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
				if err != nil || v < 0 {
					return nil, fmt.Errorf("tone: bad segment %q", part)
				}
				nums = append(nums, v)
			}
			t.segs = append(t.segs, toneSegment{onMs: int(nums[0]), offMs: int(nums[1]), freqs: nums[2:]})
		default:
			return nil, fmt.Errorf("tone: unexpected %q", part)
		}
	}
	if len(t.segs) == 0 {
		return nil, errors.New("tone: no segments")
	}
	t.phase = make([]float64, 8)
	return t, nil
}

func dbToAmp(db float64) float64 { return 32767 * math.Pow(10, db/20) }

func (t *ToneSource) Read(buf []int16) (int, error) {
	n := 0
	for n < len(buf) {
		if t.loops >= 0 && t.played >= t.loops {
			break
		}
		s := t.segs[t.seg]
		onN, total := s.onMs*8, (s.onMs+s.offMs)*8
		if t.pos >= total {
			t.pos = 0
			t.seg++
			if t.seg == len(t.segs) {
				t.seg = 0
				t.played++
			}
			continue
		}
		var v float64
		if t.pos < onN && len(s.freqs) > 0 {
			for i, f := range s.freqs {
				if i >= len(t.phase) {
					break
				}
				v += math.Sin(t.phase[i])
				t.phase[i] += 2 * math.Pi * f / 8000
				if t.phase[i] > 2*math.Pi {
					t.phase[i] -= 2 * math.Pi
				}
			}
			v = v / float64(len(s.freqs)) * t.amp
		}
		buf[n] = int16(v)
		n++
		t.pos++
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// OpenSource resolves a playback target: "tone_stream://spec",
// "silence_stream://ms" or a WAV file path (relative paths are resolved
// against soundsDir).
func OpenSource(target, soundsDir string) (Source, error) {
	switch {
	case strings.HasPrefix(target, "tone_stream://"):
		return ParseTone(strings.TrimPrefix(target, "tone_stream://"))
	case strings.HasPrefix(target, "silence_stream://"):
		arg, _, _ := strings.Cut(strings.TrimPrefix(target, "silence_stream://"), ",")
		ms, err := strconv.Atoi(arg)
		if err != nil {
			return nil, fmt.Errorf("bad silence_stream %q", target)
		}
		if ms < 0 {
			return &ToneSource{segs: []toneSegment{{onMs: 0, offMs: 1000}}, loops: -1, phase: make([]float64, 8)}, nil
		}
		return Silence(ms), nil
	}
	path := target
	if !strings.HasPrefix(path, "/") && soundsDir != "" {
		path = strings.TrimRight(soundsDir, "/") + "/" + path
	}
	samples, err := LoadWAV(path)
	if err != nil {
		return nil, err
	}
	return NewSampleSource(samples), nil
}
