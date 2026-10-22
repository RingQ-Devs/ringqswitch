package fax

import "math"

// V.21 channel 2: 300 bit/s FSK (1650 Hz = 1, 1850 Hz = 0), the
// signalling channel of T.30.

const (
	v21Baud  = 300.0
	v21Mark  = 1650.0
	v21Space = 1850.0
)

// fskTx sends bits as FSK.
type fskTx struct {
	phase   float64
	bitPos  float64 // 0..1 through the current bit
	bit     int
	amp     float64
	nextBit func() int // -1 = no more
	done    bool
}

func newFSKTx(amp float64, nextBit func() int) *fskTx {
	t := &fskTx{amp: amp, nextBit: nextBit, bitPos: 1}
	return t
}

func (t *fskTx) sample() (float64, bool) {
	if t.done {
		return 0, true
	}
	if t.bitPos >= 1 {
		t.bitPos--
		t.bit = t.nextBit()
		if t.bit < 0 {
			t.done = true
			return 0, true
		}
	}
	f := v21Space
	if t.bit == 1 {
		f = v21Mark
	}
	t.phase += 2 * math.Pi * f / sampleRate
	if t.phase > 2*math.Pi {
		t.phase -= 2 * math.Pi
	}
	t.bitPos += v21Baud / sampleRate
	return t.amp * math.Sin(t.phase), false
}

// fskRx demodulates V.21 by correlating with both tones over one bit.
type fskRx struct {
	n          int
	ring0      []complex128
	ring1      []complex128
	acc0, acc1 complex128
	i          int
	ph0, ph1   float64
	power      powerMeter
	carrier    bool
	onFor      int
	offFor     int
	last       int
	baudPhase  float64
	putBit     func(int)
	carrierOff func()
	bursts     int // times a V.21 carrier was heard
}

func newFSKRx(putBit func(int), carrierOff func()) *fskRx {
	n := int(math.Round(sampleRate / v21Baud))
	return &fskRx{n: n, ring0: make([]complex128, n), ring1: make([]complex128, n), putBit: putBit, carrierOff: carrierOff,
		power: powerMeter{alpha: 1.0 / 64}}
}

func (r *fskRx) reset() {
	for i := range r.ring0 {
		r.ring0[i], r.ring1[i] = 0, 0
	}
	r.acc0, r.acc1 = 0, 0
	r.carrier, r.onFor, r.offFor = false, 0, 0
}

func (r *fskRx) put(x float64) {
	c0 := complex(x*math.Cos(r.ph0), -x*math.Sin(r.ph0))
	c1 := complex(x*math.Cos(r.ph1), -x*math.Sin(r.ph1))
	r.ph0 += 2 * math.Pi * v21Space / sampleRate
	r.ph1 += 2 * math.Pi * v21Mark / sampleRate
	if r.ph0 > 2*math.Pi {
		r.ph0 -= 2 * math.Pi
	}
	if r.ph1 > 2*math.Pi {
		r.ph1 -= 2 * math.Pi
	}
	r.acc0 += c0 - r.ring0[r.i]
	r.acc1 += c1 - r.ring1[r.i]
	r.ring0[r.i], r.ring1[r.i] = c0, c1
	if r.i++; r.i == r.n {
		r.i = 0
	}
	p := powerDBm0(r.power.put(x))
	e0 := real(r.acc0)*real(r.acc0) + imag(r.acc0)*imag(r.acc0)
	e1 := real(r.acc1)*real(r.acc1) + imag(r.acc1)*imag(r.acc1)
	// Carrier: enough power, mostly in the two tones.
	tone := (e0 + e1) / float64(r.n*r.n) * 4 // ~ amplitude^2
	inBand := tone > 0.5*r.power.p*2
	if p > -45 && inBand {
		r.offFor = 0
		if !r.carrier {
			if r.onFor++; r.onFor > 40 {
				r.carrier = true
				r.bursts++
				r.baudPhase = 0
			}
		}
	} else {
		r.onFor = 0
		if r.carrier {
			if r.offFor++; r.offFor > 80 {
				r.carrier = false
				if r.carrierOff != nil {
					r.carrierOff()
				}
			}
		}
	}
	if !r.carrier {
		return
	}
	d := 0
	if e1 > e0 {
		d = 1
	}
	if d != r.last { // a bit edge: should be half way between samples
		r.baudPhase -= (r.baudPhase - 0.5) * 0.4
		r.last = d
	}
	r.baudPhase += v21Baud / sampleRate
	if r.baudPhase >= 1 {
		r.baudPhase--
		r.putBit(d)
	}
}
