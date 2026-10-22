package fax

import "math/bits"

// The T.30 terminal over T.38: the same session, with the modem audio
// replaced by IFP packets, paced as the modems would have taken (as
// spandsp's T.38 terminal does), so the gateway's buffers never overflow.

// fastRx receives the high speed modem (TCF and pages): a V.29 demodulator
// on audio, a stand-in on T.38 (the data comes as packets).
type fastRx interface{ put(x float64) }

type t38FastRx struct{}

func (t38FastRx) put(float64) {}

func (t *Terminal) newFast(rate int) fastRx {
	if t.opt.T38 {
		t.t38.fastOn = false
		return t38FastRx{}
	}
	return newV29Rx(rate, t.fastBit, t.fastEvent)
}

type t38State struct {
	out    [][]byte // IFPs to send
	txInd  int      // the indicator we last sent
	rxInd  int      // the indicator we last received
	v21On  bool     // the far end is sending V.21 (a message)
	fastOn bool     // the far end's fast modem is on (trained)
	frame  []byte   // the HDLC frame coming in
}

// TakeIFP returns the IFP packets to send (T.38), oldest first.
func (t *Terminal) TakeIFP() [][]byte {
	out := t.t38.out
	t.t38.out = nil
	return out
}

func (t *Terminal) emit(p []byte) {
	t.t38.out = append(t.t38.out, p)
	t.stats.T38Out++
}

func (t *Terminal) emitIndicator(ind int) {
	if ind == t.t38.txInd {
		return // only changes go out
	}
	t.t38.txInd = ind
	for i := 0; i < t.opt.T38Repeat; i++ {
		t.emit(encodeIndicator(ind))
	}
}

// t38Sched is a transmit source that sends IFPs at set times.
type t38Sched struct {
	t       *Terminal
	items   []t38Item
	end     int // samples
	n, k    int
	forever bool
}

type t38Item struct {
	at  int
	ind int // -1: data
	ifp []byte
}

func (t *Terminal) newSched() *t38Sched { return &t38Sched{t: t} }

func (s *t38Sched) indicator(ind int) { s.items = append(s.items, t38Item{at: s.end, ind: ind}) }
func (s *t38Sched) data(p []byte)     { s.items = append(s.items, t38Item{at: s.end, ind: -1, ifp: p}) }
func (s *t38Sched) wait(samples int)  { s.end += samples }

func (s *t38Sched) sample() (float64, bool) {
	for s.k < len(s.items) && s.items[s.k].at <= s.n {
		it := s.items[s.k]
		s.k++
		if it.ind >= 0 {
			s.t.emitIndicator(it.ind)
		} else {
			s.t.emit(it.ifp)
		}
	}
	if s.n >= s.end && s.k >= len(s.items) && !s.forever {
		return 0, true
	}
	s.n++
	return 0, false
}

// cngSource is the caller's tone: CNG on audio, its indicator on T.38.
func (t *Terminal) cngSource() txSource {
	if !t.opt.T38 {
		return &cng{}
	}
	s := t.newSched()
	s.indicator(t38NoSignal)
	s.wait(200 * msSamples)
	s.indicator(t38CNG)
	s.forever = true
	return s
}

// t38CED is the answerer's start: 200 ms, then CED for 3 s.
func (t *Terminal) t38CED() txSource {
	s := t.newSched()
	t.t38.txInd = -1 // "no signal" goes out first, as is common
	s.indicator(t38NoSignal)
	s.wait(200 * msSamples)
	s.indicator(t38CED)
	s.wait(3000 * msSamples)
	return s
}

// t38HDLC sends a V.21 message: the preamble indicator, a second of
// flags' time, then each frame's octets at 300 bit/s, closed by its
// FCS indication (the gateway adds the FCS).
func (t *Terminal) t38HDLC(frames [][]byte) txSource {
	s := t.newSched()
	s.indicator(t38NoSignal)
	s.indicator(t38V21Preamble)
	s.wait(1000 * msSamples)
	const rate = 300
	for i, f := range frames {
		data := make([]byte, len(f))
		for j, b := range f { // T.38 octets carry the line's first bit as the MSB
			data[j] = bits.Reverse8(b)
		}
		for off := 0; off < len(data); off++ { // one octet a packet (about 27 ms)
			s.data(encodeData(t.opt.T38Version, t38DataV21, []t38Field{{t38HDLCData, data[off : off+1]}}))
			s.wait(8 * 8000 / rate)
		}
		s.wait(24 * 8000 / rate) // FCS and flag
		typ := t38HDLCFCSOK
		if i == len(frames)-1 {
			typ = t38HDLCFCSOKSigEnd
		}
		s.data(encodeData(t.opt.T38Version, t38DataV21, []t38Field{{typ, nil}}))
	}
	s.wait(8 * 8000 / rate)
	s.indicator(t38NoSignal)
	return s
}

func t38Training(rate int) (ind, dataType int) {
	if rate == 7200 {
		return t38V29_7200, t38DataV29_7200
	}
	return t38V29_9600, t38DataV29_9600
}

// fastSource sends bits on the fast modem: V.29 audio, or on T.38 the
// training indicator, its time, then the bits in 30 ms packets.
func (t *Terminal) fastSource(rate int, line []byte) txSource {
	if !t.opt.T38 {
		b := &bitSource{bits: line}
		return newV29Tx(rate, txLevel, b.next)
	}
	ind, dt := t38Training(rate)
	s := t.newSched()
	s.indicator(t38NoSignal)
	s.indicator(ind)
	s.wait(253 * msSamples) // V.29 training
	data := packLine(line)
	chunk := rate * 30 / 8000
	start := s.end
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		s.data(encodeData(t.opt.T38Version, dt, []t38Field{{t38T4Data, data[off:end]}}))
		s.end = start + end*8*8000/rate
	}
	s.data(encodeData(t.opt.T38Version, dt, []t38Field{{t38T4SigEnd, nil}}))
	s.indicator(t38NoSignal)
	return s
}

// packLine packs line bits (one per byte) into octets, first bit in the MSB.
func packLine(line []byte) []byte {
	out := make([]byte, (len(line)+7)/8)
	for i, b := range line {
		if b != 0 {
			out[i/8] |= 0x80 >> uint(i%8)
		}
	}
	return out
}

// t38Pending sends the reply waiting for the far end to finish its
// message (on audio, rxSample does this when the V.21 carrier drops).
func (t *Terminal) t38Pending() {
	if t.pending != nil && !t.t38.v21On && len(t.txq) == 0 {
		fn := t.pending
		t.pending = nil
		t.send(&silence{tGap}, fn)
	}
}

// ReceiveIFP takes a T.38 packet from the far end.
func (t *Terminal) ReceiveIFP(b []byte) {
	if t.state == sDone {
		return
	}
	p, err := decodeIFP(t.opt.T38Version, b)
	if err != nil {
		if t.stats.FramesBad++; t.stats.FramesBad <= 10 {
			t.logf("fax: T.38: a damaged packet (%d bytes)", len(b))
		}
		return
	}
	t.stats.T38In++
	if p.indicator >= 0 {
		t.t38Indicator(p.indicator)
		return
	}
	for _, f := range p.fields {
		t.t38Field(p.dataType, f)
	}
}

func (t *Terminal) t38Indicator(ind int) {
	if ind == t.t38.rxInd {
		return // a repeat
	}
	t.t38.rxInd = ind
	t.t38.v21On = false // whatever the far end sent before has ended
	t.t38.frame = nil
	t.t38FastDown()
	switch {
	case ind == t38V21Preamble:
		t.t38.v21On = true
		t.stats.V21Bursts++
	case ind >= t38V27_2400 && ind <= t38V17Last:
		t.t38FastUp()
	}
}

func (t *Terminal) t38FastUp() {
	if t.t38.fastOn {
		return
	}
	t.t38.fastOn = true
	if t.fast != nil {
		t.fastEvent(RxTrained)
	}
}

func (t *Terminal) t38FastDown() {
	if !t.t38.fastOn {
		return
	}
	t.t38.fastOn = false
	if t.fast != nil {
		t.fastEvent(RxCarrierDown)
	}
}

func (t *Terminal) t38Field(dataType int, f t38Field) {
	if dataType == t38DataV21 {
		switch f.typ {
		case t38HDLCData, t38HDLCFCSOK, t38HDLCFCSBad, t38HDLCFCSOKSigEnd, t38HDLCFCSBadSigEnd, t38HDLCSigEnd:
		default:
			return
		}
		t.t38.v21On = true
		for _, b := range f.data {
			t.t38.frame = append(t.t38.frame, bits.Reverse8(b))
		}
		switch f.typ {
		case t38HDLCFCSOK, t38HDLCFCSOKSigEnd, t38HDLCFCSBad, t38HDLCFCSBadSigEnd:
			frame := t.t38.frame
			t.t38.frame = nil
			ok := f.typ == t38HDLCFCSOK || f.typ == t38HDLCFCSOKSigEnd
			if len(frame) > 0 && !(t.busy && !t.listen) {
				t.onFrame(frame, ok)
			}
		}
		switch f.typ {
		case t38HDLCSigEnd, t38HDLCFCSOKSigEnd, t38HDLCFCSBadSigEnd:
			t.t38.v21On = false
			t.t38.frame = nil
		}
		return
	}
	if dataType > 8 { // V.8 / V.34
		return
	}
	switch f.typ {
	case t38T4Data, t38T4SigEnd:
	default:
		return // ECM (HDLC at the fast rate) is not offered
	}
	t.t38FastUp()
	for _, b := range f.data {
		for i := 7; i >= 0; i-- {
			if t.fast == nil || t.state == sDone {
				break
			}
			t.fastBit(int(b>>uint(i)) & 1)
		}
	}
	if f.typ == t38T4SigEnd {
		t.t38FastDown()
	}
}
