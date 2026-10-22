package fax

import (
	"math"
	"math/cmplx"
)

// Signal processing shared by the modems. Everything runs at 8000
// samples a second on 16-bit linear audio (what G.711 carries).

const sampleRate = 8000.0

// dBm0 of a full-scale sine is +3.14 (G.711).
func dbm0ToAmplitude(dbm float64) float64 {
	return 32767 * math.Pow(10, (dbm-3.14)/20)
}

// powerDBm0 converts a mean square sample value to dBm0.
func powerDBm0(meanSquare float64) float64 {
	if meanSquare <= 1e-9 {
		return -99
	}
	return 10*math.Log10(meanSquare/(32767.0*32767.0/2)) + 3.14
}

// powerMeter is a running mean square.
type powerMeter struct {
	alpha float64
	p     float64
}

func (m *powerMeter) put(x float64) float64 {
	m.p += (x*x - m.p) * m.alpha
	return m.p
}

// rrc is the root raised cosine pulse at t symbol periods (roll-off beta).
func rrc(t, beta float64) float64 {
	if math.Abs(t) < 1e-9 {
		return 1 - beta + 4*beta/math.Pi
	}
	if q := 1 / (4 * beta); math.Abs(math.Abs(t)-q) < 1e-9 {
		return beta / math.Sqrt2 * ((1+2/math.Pi)*math.Sin(math.Pi/(4*beta)) + (1-2/math.Pi)*math.Cos(math.Pi/(4*beta)))
	}
	return (math.Sin(math.Pi*t*(1-beta)) + 4*beta*t*math.Cos(math.Pi*t*(1+beta))) /
		(math.Pi * t * (1 - 16*beta*beta*t*t))
}

// pulseShaper builds a transmit signal from symbols: each symbol is a
// root raised cosine pulse, summed, on a complex carrier.
type pulseShaper struct {
	sps    float64 // samples per symbol
	span   int     // symbols each side
	table  []float64
	res    float64 // table points per sample
	syms   []cplxAt
	clock  float64 // time of the next symbol, in samples
	n      float64 // current sample time
	phase  float64 // carrier phase (radians)
	dphase float64
	gain   float64
	next   func() (complex128, bool) // the next symbol; false = no more
	ended  bool
}

type cplxAt struct {
	t float64
	v complex128
}

func newPulseShaper(baud, carrier, beta float64, gain float64, next func() (complex128, bool)) *pulseShaper {
	s := &pulseShaper{sps: sampleRate / baud, span: 6, res: 32, gain: gain, next: next}
	s.dphase = 2 * math.Pi * carrier / sampleRate
	n := int(float64(s.span)*s.sps*s.res) + 2
	s.table = make([]float64, n)
	for i := range s.table {
		s.table[i] = rrc(float64(i)/s.res/s.sps, beta)
	}
	return s
}

func (s *pulseShaper) pulse(dt float64) float64 {
	x := math.Abs(dt) * s.res
	i := int(x)
	if i+1 >= len(s.table) {
		return 0
	}
	f := x - float64(i)
	return s.table[i]*(1-f) + s.table[i+1]*f
}

// sample returns the next audio sample; done once the last symbol has died away.
func (s *pulseShaper) sample() (float64, bool) {
	horizon := float64(s.span) * s.sps
	for !s.ended && s.clock <= s.n+horizon {
		v, ok := s.next()
		if !ok {
			s.ended = true
			break
		}
		s.syms = append(s.syms, cplxAt{s.clock, v})
		s.clock += s.sps
	}
	k := 0
	for k < len(s.syms) && s.syms[k].t < s.n-horizon {
		k++
	}
	s.syms = s.syms[k:]
	if s.ended && len(s.syms) == 0 {
		return 0, true
	}
	var z complex128
	for _, sy := range s.syms {
		z += sy.v * complex(s.pulse(s.n-sy.t), 0)
	}
	c := complex(math.Cos(s.phase), math.Sin(s.phase))
	s.phase += s.dphase
	if s.phase > 2*math.Pi {
		s.phase -= 2 * math.Pi
	}
	s.n++
	return real(z*c) * s.gain, false
}

// matchedFilter is the receive side: the audio is shifted down by the
// carrier, root raised cosine filtered, and read at any fractional time.
type matchedFilter struct {
	phases int
	taps   int
	coef   [][]float64
	buf    []complex128 // the last taps baseband samples (doubled ring)
	pos    int
	phase  float64 // carrier phase
	dphase float64
	n      int // samples put so far
}

func newMatchedFilter(baud, carrier, beta float64) *matchedFilter {
	sps := sampleRate / baud
	f := &matchedFilter{phases: 64}
	f.taps = int(math.Ceil(sps*8)) | 1 // 8 symbols
	half := float64(f.taps-1) / 2
	f.coef = make([][]float64, f.phases)
	for p := 0; p < f.phases; p++ {
		f.coef[p] = make([]float64, f.taps)
		mu := float64(p) / float64(f.phases)
		var sum float64
		for k := 0; k < f.taps; k++ {
			v := rrc((float64(k)-half+mu)/sps, beta)
			f.coef[p][k] = v
			sum += v
		}
		for k := range f.coef[p] { // unity gain at DC
			f.coef[p][k] /= sum
		}
	}
	f.buf = make([]complex128, 2*f.taps)
	f.dphase = 2 * math.Pi * carrier / sampleRate
	return f
}

// put adds one audio sample.
func (f *matchedFilter) put(x float64) {
	c := complex(x*math.Cos(f.phase), -x*math.Sin(f.phase))
	f.phase += f.dphase
	if f.phase > 2*math.Pi {
		f.phase -= 2 * math.Pi
	}
	f.buf[f.pos] = c
	f.buf[f.pos+f.taps] = c
	f.pos++
	if f.pos == f.taps {
		f.pos = 0
	}
	f.n++
}

// at returns the filtered baseband at mu (0..1) samples after the
// filter's current output time (the newest sample less half the filter).
func (f *matchedFilter) at(mu float64) complex128 {
	p := int(mu * float64(f.phases))
	if p >= f.phases {
		p = f.phases - 1
	}
	if p < 0 {
		p = 0
	}
	c := f.coef[p]
	// buf[pos .. pos+taps) is oldest .. newest
	var z complex128
	b := f.buf[f.pos : f.pos+f.taps]
	for k := 0; k < f.taps; k++ {
		z += b[f.taps-1-k] * complex(c[k], 0)
	}
	return z
}

// rotate turns z by a phase in radians.
func rotate(z complex128, ph float64) complex128 {
	return z * cmplx.Rect(1, ph)
}
