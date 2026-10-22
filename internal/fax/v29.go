package fax

import (
	"math"
	"math/cmplx"
)

// V.29: 9600, 7200 and 4800 bit/s QAM at 2400 baud on a 1700 Hz carrier.

const (
	v29Baud    = 2400.0
	v29Carrier = 1700.0
)

// v29Points are the 16 constellation points: 8 phases (0, 45, ... 315
// degrees) at a low amplitude, then the same at a high amplitude.
var v29Points = [16]complex128{
	3, 1 + 1i, 3i, -1 + 1i, -3, -1 - 1i, -3i, 1 - 1i,
	5, 3 + 3i, 5i, -3 + 3i, -5, -3 - 3i, -5i, 3 - 3i,
}

// The training symbols per rate (9600, 7200, 4800): segment 2 alternates
// A and B, segment 3 sends C or D as its pseudo-random bits say.
var (
	v29A = [3]complex128{-3, -3, -3}
	v29B = [3]complex128{3 - 3i, 1 - 1i, -3i}
	v29C = [3]complex128{3, 3, 3}
	v29D = [3]complex128{-3 + 3i, -1 + 1i, 3i}
)

// Bits to phase steps (in 45 degree units), and back.
var (
	v29Steps8  = [8]int{1, 0, 2, 3, 6, 7, 5, 4}
	v29Steps4  = [4]int{0, 2, 6, 4}
	v29Bits8   [8]int // phase step -> the 3 bits
	v29Bits4   [8]int // phase step -> the 2 bits (even steps)
	v29RateIdx = map[int]int{9600: 0, 7200: 1, 4800: 2}
)

func init() {
	for b, s := range v29Steps8 {
		v29Bits8[s] = b
	}
	for b, s := range v29Steps4 {
		v29Bits4[s] = b
	}
}

const (
	v29Seg1 = 48  // silence
	v29Seg2 = 128 // ABAB
	v29Seg3 = 384 // CDCD
	v29Seg4 = 48  // scrambled ones
)

// trainingBits is the segment 3 sequence (1 + x^-6 + x^-7, from 0101010).
type trainingBits struct{ reg uint8 }

func (t *trainingBits) next() int {
	bit := int(t.reg & 1)
	t.reg >>= 1
	if (bit ^ int(t.reg&1)) != 0 {
		t.reg |= 0x40
	}
	return bit
}

// scrambler is the data scrambler of V.29 (1 + x^-18 + x^-23).
type scrambler struct{ reg uint32 }

func (s *scrambler) scramble(bit int) int {
	out := (bit ^ int(s.reg>>17) ^ int(s.reg>>22)) & 1
	s.reg = s.reg<<1 | uint32(out)
	return out
}

func (s *scrambler) descramble(in int) int {
	out := (in ^ int(s.reg>>17) ^ int(s.reg>>22)) & 1
	s.reg = s.reg<<1 | uint32(in)
	return out
}

// ---- transmitter

type v29Tx struct {
	rate     int
	ri       int
	step     int
	train    trainingBits
	scr      scrambler
	state    int
	bits     func() int // data bits; -1 at the end
	ended    bool
	trailing int
	shaper   *pulseShaper
}

func newV29Tx(rate int, levelDBm0 float64, bits func() int) *v29Tx {
	t := &v29Tx{rate: rate, ri: v29RateIdx[rate], bits: bits, train: trainingBits{reg: 0x2A}}
	var avg float64
	switch rate {
	case 9600:
		avg = (9 + 2 + 25 + 18) / 4.0
	case 7200:
		avg = (9 + 2) / 2.0
	default:
		avg = 9
	}
	gain := dbm0ToAmplitude(levelDBm0) / math.Sqrt2 / math.Sqrt(avg/2)
	t.shaper = newPulseShaper(v29Baud, v29Carrier, 0.25, gain, t.symbol)
	return t
}

func (t *v29Tx) bit() int {
	if t.step <= v29Seg1+v29Seg2+v29Seg3+v29Seg4 { // segment 4: ones
		return t.scr.scramble(1)
	}
	if !t.ended {
		if b := t.bits(); b >= 0 {
			return t.scr.scramble(b)
		}
		t.ended = true
	}
	t.trailing++
	return t.scr.scramble(1)
}

func (t *v29Tx) symbol() (complex128, bool) {
	t.step++
	switch {
	case t.step <= v29Seg1:
		return 0, true
	case t.step <= v29Seg1+v29Seg2:
		if (t.step-v29Seg1)%2 == 1 {
			return v29A[t.ri], true
		}
		return v29B[t.ri], true
	case t.step <= v29Seg1+v29Seg2+v29Seg3:
		if t.train.next() == 1 {
			return v29D[t.ri], true
		}
		return v29C[t.ri], true
	}
	if t.ended && t.trailing >= 32*4 {
		return 0, false
	}
	amp := 0
	if t.rate == 9600 && t.bit() == 1 {
		amp = 8
	}
	b := t.bit()<<1 | t.bit()
	if t.rate == 4800 {
		t.state = (t.state + v29Steps4[b]) & 7
	} else {
		b = b<<1 | t.bit()
		t.state = (t.state + v29Steps8[b]) & 7
	}
	return v29Points[amp|t.state], true
}

func (t *v29Tx) sample() (float64, bool) { return t.shaper.sample() }

// ---- receiver

type rxStage int

const (
	stIdle rxStage = iota
	stAGC
	stABAB
	stTrain
	stData
	stFailed
)

// RxEvent is what a high-speed receiver reports.
type RxEvent int

const (
	RxTrained RxEvent = iota + 1
	RxTrainFailed
	RxCarrierDown
)

const (
	eqLen    = 31 // T/2 spaced taps
	eqCentre = 14 // an even number of half symbols before the newest (an on-time sample)
)

// v29Rx receives V.29: a matched filter read at the receiver's own symbol
// timing (Gardner), a T/2 spaced equalizer trained on segment 3, and a
// carrier loop started from segment 2.
type v29Rx struct {
	rate     int
	ri       int
	putBit   func(int)
	event    func(RxEvent)
	mf       *matchedFilter
	half     float64
	sps      float64
	nextT    float64
	n        int
	power    powerMeter
	on       bool
	onFor    int
	offFor   int
	stage    rxStage
	agcSum   float64
	agcN     int
	agcPrev  float64 // the last AGC window's mean power (0 = none yet)
	level    float64 // dBm0 of the signal the AGC settled on
	gain     float64
	pin      float64 // equalizer input power
	eqBuf    [2 * eqLen]complex128
	eqPos    int
	eq       [eqLen]complex128
	halfTick bool
	prev     complex128 // the last on-time sample
	mid      complex128 // the sample between
	gTED     float64
	tInt     float64 // timing loop integrator (clock drift)
	carrPh   float64
	carrFq   float64 // radians per symbol
	abab     []complex128
	aParity  int
	train    trainingBits
	trainN   int
	trainErr float64
	scr      scrambler
	state    int
	testN    int
	testErr  float64
	maxErr   float64
	// Quality, for the caller.
	MSE float64
}

func newV29Rx(rate int, putBit func(int), event func(RxEvent)) *v29Rx {
	r := &v29Rx{rate: rate, ri: v29RateIdx[rate], putBit: putBit, event: event}
	r.mf = newMatchedFilter(v29Baud, v29Carrier, 0.5)
	r.half = float64(r.mf.taps-1) / 2
	r.sps = sampleRate / v29Baud
	r.power = powerMeter{alpha: 1.0 / 128}
	r.maxErr = map[int]float64{9600: 0.45, 7200: 0.6, 4800: 1.5}[rate]
	r.reset()
	return r
}

func (r *v29Rx) reset() {
	r.stage, r.on, r.onFor, r.offFor = stIdle, false, 0, 0
	r.agcSum, r.agcN, r.agcPrev, r.gain, r.pin = 0, 0, 0, 1, 0
	r.eq = [eqLen]complex128{}
	r.eq[eqCentre] = 1
	r.eqBuf = [2 * eqLen]complex128{}
	r.halfTick, r.prev, r.mid = false, 0, 0
	r.gTED, r.tInt = 0.05, 0
	r.carrPh, r.carrFq = 0, 0
	r.abab = r.abab[:0]
	r.train = trainingBits{reg: 0x2A}
	r.trainN, r.trainErr = 0, 0
	r.scr = scrambler{}
	r.state, r.testN, r.testErr = 0, 0, 0
}

// put takes one audio sample.
func (r *v29Rx) put(x float64) {
	r.mf.put(x)
	r.n++
	p := powerDBm0(r.power.put(x))
	if !r.on {
		if p > -43 {
			if r.onFor++; r.onFor > 20 {
				r.restart()
			}
		} else {
			r.onFor = 0
		}
		return
	}
	// A noisy line can raise carrier detect before the signal: when the
	// power then jumps, the real signal has started.
	if r.stage != stAGC && r.stage != stData && p > r.level+6 {
		r.restart()
	}
	off := -48.0
	if r.stage >= stTrain && r.level-12 > off {
		off = r.level - 12
	}
	if p < off {
		if r.offFor++; r.offFor > 40 {
			r.on = false
			stage := r.stage
			r.stage = stIdle
			switch stage {
			case stData:
				r.event(RxCarrierDown)
			case stFailed:
			default:
				r.event(RxTrainFailed)
			}
			return
		}
	} else {
		r.offFor = 0
	}
	base := float64(r.n-1) - r.half
	for r.nextT < base+1 {
		mu := r.nextT - base
		if mu < 0 {
			mu = 0
		}
		r.halfSymbol(r.mf.at(mu))
		r.nextT += r.sps / 2
	}
}

func (r *v29Rx) restart() {
	r.reset()
	r.on, r.stage = true, stAGC
	r.nextT = float64(r.n-1) - r.half
}

func (r *v29Rx) halfSymbol(y complex128) {
	if r.stage == stIdle || r.stage == stFailed {
		return
	}
	y *= complex(r.gain, 0)
	r.pin += (cmplxPow(y) - r.pin) / 64
	r.eqBuf[r.eqPos] = y
	r.eqBuf[r.eqPos+eqLen] = y
	r.eqPos = (r.eqPos + 1) % eqLen
	r.halfTick = !r.halfTick
	if r.halfTick {
		r.mid = y
		return
	}
	// Symbol timing (Gardner): late sampling gives a positive error.
	e := real((y - r.prev) * cmplx.Conj(r.mid))
	pw := cmplxPow(y) + cmplxPow(r.prev) + 1e-9
	te := e / pw * r.sps / math.Pi
	if r.stage == stData {
		r.tInt += 0.00001 * te
	}
	r.nextT -= r.gTED*te + r.tInt
	r.prev = y
	if r.stage == stAGC {
		// The signal starts with segment 2: the gain puts it on the
		// constellation, once two windows of 12 symbols agree.
		r.agcSum += cmplxPow(y)
		if r.agcN++; r.agcN == 12 {
			m := r.agcSum / 12
			prev := r.agcPrev
			r.agcSum, r.agcN, r.agcPrev = 0, 0, m
			if prev == 0 || math.Abs(10*math.Log10(m/prev)) > 1.5 {
				return
			}
			want := (cmplxPow(v29A[r.ri]) + cmplxPow(v29B[r.ri])) / 2
			r.gain = math.Sqrt(want / ((m + prev) / 2))
			r.pin *= r.gain * r.gain
			r.level = powerDBm0(r.power.p)
			r.stage = stABAB
		}
		return
	}
	r.symbol()
}

func cmplxPow(z complex128) float64 { return real(z)*real(z) + imag(z)*imag(z) }

// equalized is the equalizer output for the symbol at the centre tap, with
// the carrier phase taken off.
func (r *v29Rx) equalized() complex128 {
	var z complex128
	b := r.eqBuf[r.eqPos : r.eqPos+eqLen] // oldest .. newest
	for k := 0; k < eqLen; k++ {
		z += b[k] * r.eq[k]
	}
	return z * cmplx.Rect(1, -r.carrPh)
}

// adapt moves the equalizer towards target (normalised LMS, step alpha).
func (r *v29Rx) adapt(z, target complex128, alpha float64) {
	mu := alpha / (eqLen * (r.pin + 1e-9))
	e := (target - z) * cmplx.Rect(1, r.carrPh) * complex(mu, 0)
	b := r.eqBuf[r.eqPos : r.eqPos+eqLen]
	for k := 0; k < eqLen; k++ {
		r.eq[k] += e * cmplx.Conj(b[k])
	}
}

// trackCarrier corrects the carrier from a decision (a PI loop).
func (r *v29Rx) trackCarrier(z, target complex128, kp, ki float64) {
	pt := cmplxPow(target)
	if pt == 0 {
		return
	}
	err := (imag(z)*real(target) - real(z)*imag(target)) / pt
	r.carrFq += ki * err
	r.carrPh += kp * err
}

func (r *v29Rx) symbol() {
	z := r.equalized()
	r.carrPh += r.carrFq
	switch r.stage {
	case stABAB:
		r.abab = append(r.abab, z)
		i := len(r.abab) - 1
		switch {
		case i < 40:
			if i == 24 {
				r.gTED = 0.02
			}
			return
		case i == 40:
			r.lockABAB()
			return
		}
		want := v29B[r.ri]
		if i%2 == r.aParity {
			want = v29A[r.ri]
			// Segment 3 starts with C where the next A would be.
			if cmplxPow(z-v29C[r.ri]) < cmplxPow(z-want) {
				r.train.next() // C: the first training bit (0)
				r.stage = stTrain
				r.trainN = 1
				r.gTED = 0.01
				r.trackCarrier(z, v29C[r.ri], 0.05, 0.002)
				r.adapt(z, v29C[r.ri], 0.05)
				return
			}
		}
		r.trackCarrier(z, want, 0.05, 0.002)
		r.adapt(z, want, 0.02)
		if i > v29Seg2 {
			r.fail()
		}
	case stTrain:
		target := v29C[r.ri]
		if r.train.next() == 1 {
			target = v29D[r.ri]
		}
		alpha := 0.08
		if r.trainN > 200 {
			alpha = 0.03
		}
		r.adapt(z, target, alpha)
		r.trackCarrier(z, target, 0.03, 0.0008)
		if r.trainN >= v29Seg3-64 {
			r.trainErr += cmplxPow(z - target)
		}
		if r.trainN++; r.trainN >= v29Seg3 {
			r.MSE = r.trainErr / 64
			if r.MSE > r.maxErr {
				r.fail()
				return
			}
			r.stage = stData
			r.state = 0
			r.gTED = 0.004
			r.testN, r.testErr = 0, 0
		}
	case stData:
		r.decode(z)
	}
}

// lockABAB decides which symbols are A and finds the carrier phase and
// frequency from segment 2.
func (r *v29Rx) lockABAB() {
	fit := func(zs []complex128, off, par int) (float64, float64) {
		var acc complex128
		for i, z := range zs {
			ref := v29B[r.ri]
			if (off+i)%2 == par {
				ref = v29A[r.ri]
			}
			acc += z * cmplx.Conj(ref)
		}
		ph := cmplx.Phase(acc)
		var score float64
		for i, z := range zs {
			ref := v29B[r.ri]
			if (off+i)%2 == par {
				ref = v29A[r.ri]
			}
			score += cmplxPow(z*cmplx.Rect(1, -ph) - ref)
		}
		return ph, score
	}
	_, s0 := fit(r.abab[16:40], 16, 0)
	_, s1 := fit(r.abab[16:40], 16, 1)
	r.aParity = 0
	if s1 < s0 {
		r.aParity, s0 = 1, s1
	}
	// Segment 2 alternates two points: anything else (noise) is not V.29.
	if s0/24 > (cmplxPow(v29A[r.ri])+cmplxPow(v29B[r.ri]))/4 {
		r.fail()
		return
	}
	p1, _ := fit(r.abab[16:28], 16, r.aParity)
	p2, _ := fit(r.abab[28:40], 28, r.aParity)
	d := math.Remainder(p2-p1, 2*math.Pi)
	r.carrFq = d / 12
	// p2 is the phase at symbol 33.5; the next symbol is 41.
	r.carrPh += p2 + r.carrFq*7.5
}

func (r *v29Rx) fail() {
	r.stage = stFailed
	r.event(RxTrainFailed)
}

func (r *v29Rx) putBits(bits int, n int) {
	for i := n - 1; i >= 0; i-- {
		r.emit(r.scr.descramble((bits >> uint(i)) & 1))
	}
}

// emit passes a data bit on; segment 4 (ones) only syncs the descrambler.
func (r *v29Rx) emit(bit int) {
	if r.testN >= v29Seg4 {
		r.putBit(bit)
	}
}

// decode slices a data symbol to the nearest point and sends its bits.
func (r *v29Rx) decode(z complex128) {
	best, bd := 0, math.Inf(1)
	for i, p := range v29Points {
		if r.rate != 9600 && i >= 8 {
			break
		}
		if r.rate == 4800 && i%2 == 1 {
			continue
		}
		if d := cmplxPow(z - p); d < bd {
			best, bd = i, d
		}
	}
	step := (best - r.state) & 7
	if r.rate == 9600 {
		r.emit(r.scr.descramble(best >> 3))
	}
	if r.rate == 4800 {
		r.putBits(v29Bits4[step&6], 2)
	} else {
		r.putBits(v29Bits8[step], 3)
	}
	r.state = best & 7
	target := v29Points[best]
	r.trackCarrier(z, target, 0.01, 0.0001)
	r.adapt(z, target, 0.01)
	if r.testN < v29Seg4 {
		r.testErr += bd
		if r.testN++; r.testN == v29Seg4 {
			if r.testErr/v29Seg4 > r.maxErr*1.5 {
				r.fail()
				return
			}
			r.event(RxTrained)
		}
	}
}
