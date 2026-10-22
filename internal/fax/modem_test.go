package fax

import (
	"bytes"
	"math"
	"math/rand"
	"testing"
)

// ulaw sends a sample through G.711 mu-law and back, as a call does.
func ulaw(x float64) float64 {
	const bias, clip = 0x84, 32635
	s := int(math.Round(x))
	sign := 0
	if s < 0 {
		sign, s = 0x80, -s
	}
	if s > clip {
		s = clip
	}
	s += bias
	exp := 7
	for m := 0x4000; s&m == 0 && exp > 0; m >>= 1 {
		exp--
	}
	man := (s >> uint(exp+3)) & 0x0F
	u := ^(sign | exp<<4 | man) & 0xFF
	// decode
	u = ^u & 0xFF
	t := ((u&0x0F)<<3 + bias) << uint((u&0x70)>>4)
	if u&0x80 != 0 {
		return float64(bias - t)
	}
	return float64(t - bias)
}

func testPage(lines int) *Page {
	p := newPage(lines, resFine)
	rng := rand.New(rand.NewSource(1))
	for y := range p.Rows {
		for b := 0; b < 6; b++ { // some black blocks and text-like noise
			x0 := rng.Intn(PageWidth - 200)
			w := 1 + rng.Intn(150)
			for x := x0; x < x0+w; x++ {
				p.Rows[y][x>>3] |= 0x80 >> uint(x&7)
			}
		}
	}
	p.Header("2026-10-09 12:00 RINGQSWITCH TEST P.1/1")
	return p
}

func TestT4RoundTrip(t *testing.T) {
	p := testPage(300)
	bits := EncodePage(p, 96)
	d := newPageDecoder(PageWidth)
	for _, b := range bits {
		d.putBit(int(b))
	}
	if !d.Done || d.BadRows != 0 || len(d.Rows) != len(p.Rows) {
		t.Fatalf("decoded %d rows (%d bad, done %v), want %d", len(d.Rows), d.BadRows, d.Done, len(p.Rows))
	}
	for i := range p.Rows {
		if !bytes.Equal(p.Rows[i], d.Rows[i]) {
			t.Fatalf("row %d differs", i)
		}
	}
}

func TestHDLCOverV21(t *testing.T) {
	frames := [][]byte{{0xFF, 0x03, 0x40, 'A', 'B', 'C'}, {0xFF, 0x13, 0x80, 0x00, 0xCE, 0xF4}}
	bits := hdlcBits(30, frames)
	i := 0
	tx := newFSKTx(dbm0ToAmplitude(-12), func() int {
		if i >= len(bits) {
			return -1
		}
		i++
		return int(bits[i-1])
	})
	var got [][]byte
	h := &hdlcRx{onFrame: func(f []byte, ok bool) {
		if ok {
			got = append(got, f)
		}
	}}
	rx := newFSKRx(h.putBit, nil)
	for k := 0; k < 400; k++ {
		rx.put(0)
	}
	for {
		x, done := tx.sample()
		if done {
			break
		}
		rx.put(ulaw(x))
	}
	for k := 0; k < 800; k++ {
		rx.put(0)
	}
	if len(got) != 2 || !bytes.Equal(got[0], frames[0]) || !bytes.Equal(got[1], frames[1]) {
		t.Fatalf("frames: %x", got)
	}
}

// channel adds what a call does to a modem signal.
type channel struct {
	noise   float64 // RMS
	freq    float64 // carrier offset, Hz (applied as a frequency shift)
	drift   float64 // sample clock error (1e-4 = 100 ppm)
	rng     *rand.Rand
	in      []float64
	pos     float64
	shiftPh float64
	hilbert []float64
	hBuf    []float64
	hPos    int
}

func newChannel(noise, freq, drift float64) *channel {
	c := &channel{noise: noise, freq: freq, drift: drift, rng: rand.New(rand.NewSource(7))}
	// A Hilbert transformer for the frequency shift.
	n := 63
	c.hilbert = make([]float64, n)
	for k := 0; k < n; k++ {
		m := k - n/2
		if m%2 != 0 {
			c.hilbert[k] = 2 / (math.Pi * float64(m)) * (0.54 + 0.46*math.Cos(2*math.Pi*float64(m)/float64(n)))
		}
	}
	c.hBuf = make([]float64, n)
	return c
}

func (c *channel) process(x []float64) []float64 {
	var out []float64
	for _, s := range x {
		// frequency shift via the analytic signal
		if c.freq != 0 {
			c.hBuf[c.hPos] = s
			var q float64
			n := len(c.hilbert)
			for k := 0; k < n; k++ {
				q += c.hilbert[k] * c.hBuf[(c.hPos-k+n)%n]
			}
			d := c.hBuf[(c.hPos-n/2+n)%n]
			c.hPos = (c.hPos + 1) % n
			s = d*math.Cos(c.shiftPh) - q*math.Sin(c.shiftPh)
			c.shiftPh += 2 * math.Pi * c.freq / sampleRate
		}
		c.in = append(c.in, s)
	}
	// resample for clock drift (windowed sinc, as a real clock difference)
	const half = 16
	for c.pos+half+1 < float64(len(c.in)) {
		i := int(c.pos)
		f := c.pos - float64(i)
		var v float64
		for k := -half + 1; k <= half; k++ {
			j := i + k
			if j < 0 {
				continue
			}
			d := float64(k) - f
			w := 0.5 + 0.5*math.Cos(math.Pi*d/half)
			sinc := 1.0
			if math.Abs(d) > 1e-9 {
				sinc = math.Sin(math.Pi*d) / (math.Pi * d)
			}
			v += c.in[j] * sinc * w
		}
		v += c.rng.NormFloat64() * c.noise
		out = append(out, ulaw(v))
		c.pos += 1 + c.drift
	}
	return out
}

func runV29(t *testing.T, rate int, ch *channel, nbits int) (int, int) {
	rng := rand.New(rand.NewSource(int64(rate)))
	sent := make([]int, nbits)
	for i := range sent {
		sent[i] = rng.Intn(2)
	}
	i := 0
	tx := newV29Tx(rate, -12, func() int {
		if i >= len(sent) {
			return -1
		}
		i++
		return sent[i-1]
	})
	var got []int
	trained := false
	failed := false
	rx := newV29Rx(rate, func(b int) { got = append(got, b) }, func(e RxEvent) {
		switch e {
		case RxTrained:
			trained = true
		case RxTrainFailed:
			failed = true
		}
	})
	var audio []float64
	for k := 0; k < 800; k++ {
		audio = append(audio, 0)
	}
	for {
		x, done := tx.sample()
		if done {
			break
		}
		audio = append(audio, x)
	}
	for k := 0; k < 800; k++ {
		audio = append(audio, 0)
	}
	for _, x := range ch.process(audio) {
		rx.put(x)
	}
	if !trained { // a failed attempt on line noise before the signal is fine
		t.Fatalf("V.29 %d: trained %v failed %v (training MSE %.3f)", rate, trained, failed, rx.MSE)
	}
	// The data follows the segment 4 ones: find it.
	best, at := -1, 0
	for off := 0; off < 400 && off+200 < len(got); off++ {
		m := 0
		for k := 0; k < 200; k++ {
			if got[off+k] == sent[k] {
				m++
			}
		}
		if m > best {
			best, at = m, off
		}
	}
	errs := 0
	n := 0
	for k := 0; k+at < len(got) && k < len(sent); k++ {
		if got[k+at] != sent[k] {
			errs++
		}
		n++
	}
	return errs, n
}

func TestV29Loopback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rate  int
		ch    *channel
		maxER float64
	}{
		{"9600 clean", 9600, newChannel(0, 0, 0), 0},
		{"7200 clean", 7200, newChannel(0, 0, 0), 0},
		{"4800 clean", 4800, newChannel(0, 0, 0), 0},
		{"9600 noise 25dB", 9600, newChannel(dbm0ToAmplitude(-12)/math.Sqrt2/math.Pow(10, 25.0/20), 0, 0), 1e-4},
		{"9600 offset 3Hz", 9600, newChannel(0, 3, 0), 0},
		{"9600 drift 100ppm", 9600, newChannel(0, 0, 1e-4), 0},
		{"7200 noise 20dB", 7200, newChannel(dbm0ToAmplitude(-12)/math.Sqrt2/math.Pow(10, 20.0/20), 0, 0), 1e-4},
		{"4800 noise 15dB", 4800, newChannel(dbm0ToAmplitude(-12)/math.Sqrt2/math.Pow(10, 15.0/20), 0, 0), 1e-4},
		{"9600 all", 9600, newChannel(dbm0ToAmplitude(-12)/math.Sqrt2/math.Pow(10, 28.0/20), 2, -5e-5), 1e-4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs, n := runV29(t, tc.rate, tc.ch, 60000)
			t.Logf("%d errors in %d bits", errs, n)
			if n < 59000 || float64(errs)/float64(n) > tc.maxER {
				t.Fatalf("bit errors %d of %d", errs, n)
			}
		})
	}
}
