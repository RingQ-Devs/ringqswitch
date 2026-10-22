package fax

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// T.30: the fax session. The caller sends (CNG, then TSI + DCS, TCF and
// the pages); the answerer receives (CED, CSI + DIS, CFR, MCF). Signalling
// is HDLC on V.21 channel 2, pages are T.4 MH on V.29, without ECM: the
// audio ("T.38 off") way that works through any G.711 path.

// Modems a terminal may use.
const (
	ModemV27ter = 1 // not yet: offered only when implemented
	ModemV29    = 2
)

// Result codes, numbered as fax_result_code reports them to dialplans.
const (
	ErrOK           = 0
	ErrT0Expired    = 2
	ErrT1Expired    = 3
	ErrCannotTrain  = 6
	ErrIncompatible = 8
	ErrRxIncapable  = 9
	ErrUnexpected   = 13
	ErrTxBadDCS     = 14
	ErrTxBadPage    = 15
	ErrTxGotDCN     = 17
	ErrTxInvalidRsp = 18
	ErrTxPhaseB     = 20
	ErrTxPhaseD     = 21
	ErrRxNoCarrier  = 26
	ErrRxT2ExpFax   = 31
	ErrRxT2ExpMPS   = 32
	ErrRxT2Exp      = 34
	ErrRxDCNWhy     = 35
	ErrRxDCNData    = 36
	ErrRxDCNFax     = 37
	ErrFile         = 42
	ErrNoPage       = 43
	ErrRetryDCN     = 49
	ErrCallDropped  = 50
)

var resultText = map[int]string{
	ErrOK:           "OK",
	ErrT0Expired:    "Timed out waiting for initial communication",
	ErrT1Expired:    "Timed out waiting for the first message",
	ErrCannotTrain:  "Failed to train with any of the compatible modems",
	ErrIncompatible: "Far end is not compatible",
	ErrRxIncapable:  "Far end is not able to receive",
	ErrUnexpected:   "Unexpected message received",
	ErrTxBadDCS:     "Received bad response to DCS or training",
	ErrTxBadPage:    "Received a DCN from remote after sending a page",
	ErrTxGotDCN:     "Received a DCN while waiting for a DIS",
	ErrTxInvalidRsp: "Invalid response after sending a page",
	ErrTxPhaseB:     "Received no response to DCS or TCF",
	ErrTxPhaseD:     "No response after sending a page",
	ErrRxNoCarrier:  "Carrier lost during fax receive",
	ErrRxT2ExpFax:   "Timer T2 expired while waiting for fax page",
	ErrRxT2ExpMPS:   "Timer T2 expired while waiting for next fax page",
	ErrRxT2Exp:      "Timer T2 expired while waiting for NSS, DCS or MCF",
	ErrRxDCNWhy:     "Unexpected DCN while waiting for DCS or DIS",
	ErrRxDCNData:    "Unexpected DCN while waiting for image data",
	ErrRxDCNFax:     "Unexpected DCN while waiting for EOM, EOP or MPS",
	ErrFile:         "TIFF/F file cannot be opened",
	ErrNoPage:       "TIFF/F page not found",
	ErrRetryDCN:     "Disconnected after permitted retries",
	ErrCallDropped:  "The call dropped prematurely",
}

// ResultText is the text of a result code.
func ResultText(code int) string {
	if s, ok := resultText[code]; ok {
		return s
	}
	return fmt.Sprintf("Fax error %d", code)
}

// T.30 frame control fields, as they arrive (the X bit, 0x01, masked off).
const (
	fcfDIS = 0x80
	fcfCSI = 0x40
	fcfNSF = 0x20
	fcfDTC = 0x81 // never masked: polling is not supported
	fcfDCS = 0x82
	fcfTSI = 0x42
	fcfCFR = 0x84
	fcfFTT = 0x44
	fcfEOM = 0x8E
	fcfMPS = 0x4E
	fcfEOP = 0x2E
	fcfMCF = 0x8C
	fcfRTP = 0xCC
	fcfRTN = 0x4C
	fcfPIP = 0xAC
	fcfPIN = 0x2C
	fcfDCN = 0xFA
	fcfCRP = 0x1A
)

var fcfNames = map[byte]string{
	fcfDIS: "DIS", fcfCSI: "CSI", fcfNSF: "NSF", fcfDCS: "DCS", fcfTSI: "TSI", fcfCFR: "CFR",
	fcfFTT: "FTT", fcfEOM: "EOM", fcfMPS: "MPS", fcfEOP: "EOP", fcfMCF: "MCF", fcfRTP: "RTP",
	fcfRTN: "RTN", fcfPIP: "PIP", fcfPIN: "PIN", fcfDCN: "DCN", fcfCRP: "CRP",
}

func fcfName(f byte) string {
	if s, ok := fcfNames[f]; ok {
		return s
	}
	return fmt.Sprintf("0x%02X", f)
}

// Timers, in samples.
const (
	msSamples = sampleRate / 1000
	tT0       = 60000 * msSamples // caller: waiting for the answerer
	tT1       = 35000 * msSamples // answerer: waiting for a DCS
	tT2       = 6000 * msSamples
	tT4       = 3000 * msSamples
	tGap      = 75 * msSamples // between a message and the next modem
	txLevel   = -13.0          // dBm0
)

// Options configures a fax terminal.
type Options struct {
	Ident  string // our station number (TSI / CSI), up to 20 characters
	Header string // the page header text (the company name); "" = no header line
	Modems int    // ModemV29 (the default)
	Log    func(format string, args ...any)
	Now    func() time.Time // the header's clock (tests)
	// OnPage reports each page the far end confirmed (sending) or that
	// came in good (receiving): its number and the pages known so far.
	OnPage func(page, total int)
	// T38 runs the session over T.38 (IFP packets: TakeIFP, ReceiveIFP)
	// instead of audio; T38Version is the negotiated T38FaxVersion and
	// T38Repeat how many times each indicator goes out (1 when UDPTL
	// redundancy protects them).
	T38        bool
	T38Version int
	T38Repeat  int
}

// Result is how a fax session ended.
type Result struct {
	Success    bool
	Code       int
	Text       string
	Pages      int // pages transferred
	TotalPages int // pages to send (sending) or received (receiving)
	RemoteID   string
	Rate       int // bit/s of the last page
	BadRows    int
	Fine       bool
	T38        bool    // the session ran over T.38
	Received   []*Page // the pages received
}

type txSource interface{ sample() (float64, bool) }

type txItem struct {
	src  txSource
	done func()
}

// silence and tone are simple transmit sources.
type silence struct{ n int }

func (s *silence) sample() (float64, bool) {
	if s.n <= 0 {
		return 0, true
	}
	s.n--
	return 0, false
}

type tone struct {
	amp, dph, ph float64
	n            int
}

func newTone(freq, dbm0 float64, ms int) *tone {
	return &tone{amp: dbm0ToAmplitude(dbm0), dph: 2 * math.Pi * freq / sampleRate, n: ms * msSamples}
}

func (t *tone) sample() (float64, bool) {
	if t.n <= 0 {
		return 0, true
	}
	t.n--
	t.ph += t.dph
	return t.amp * math.Sin(t.ph), false
}

// cng is the calling tone: 1100 Hz, 0.5 s on, 3 s off, until stopped.
type cng struct{ n int }

func (c *cng) sample() (float64, bool) {
	k := c.n % (3500 * msSamples)
	c.n++
	if k >= 500*msSamples {
		return 0, false
	}
	return dbm0ToAmplitude(txLevel) * math.Sin(2*math.Pi*1100*float64(k)/sampleRate), false
}

type bitSource struct {
	bits []byte
	i    int
}

func (b *bitSource) next() int {
	if b.i >= len(b.bits) {
		return -1
	}
	b.i++
	return int(b.bits[b.i-1])
}

type termState int

const (
	sCallWaitDIS termState = iota // caller: CNG, waiting for DIS
	sSendTraining
	sWaitCFR
	sSendPage
	sWaitMCF
	sAnswer // answerer: CED, then DIS
	sWaitDCS
	sTCF
	sWaitPage
	sPage
	sWaitPPM
	sWaitDCN
	sClosing // sending DCN
	sDone
)

// Terminal is one end of a fax call. Feed it the call's audio with
// Process; it writes the audio to send back.
type Terminal struct {
	opt     Options
	sending bool
	state   termState
	now     int // samples so far
	txq     []txItem
	stats   Stats
	listen  bool // receive while transmitting (CNG)
	busy    bool // transmitting
	fsk     *fskRx
	hdlc    *hdlcRx
	fast    fastRx
	t38     t38State
	frames  [][]byte
	pending func() // the reply, once the far end's carrier stops
	timer   int
	onTimer func()
	result  Result
	retries int

	// negotiated
	rates    []int // V.29 rates still to try
	rate     int
	fine     bool
	minScan  int // ms per line
	scanCode int
	remoteRx bool

	// sending
	pages    []*Page
	page     int // the page being sent
	pageTry  int
	lastPPM  byte
	pageSent int

	// receiving
	deadline  int
	gotDCS    bool
	tcfZeros  int
	tcfMost   int
	tcfOK     bool
	dec       *pageDecoder
	pageGood  bool
	lastReply byte
	inPage    bool
}

// NewSender makes the calling terminal that sends pages.
func NewSender(pages []*Page, opt Options) *Terminal {
	t := newTerminal(opt)
	t.sending = true
	t.pages = pages
	t.result.TotalPages = len(pages)
	t.state = sCallWaitDIS
	if len(pages) == 0 {
		t.finish(ErrNoPage)
		return t
	}
	t.listen = true
	t.send(t.cngSource(), nil)
	t.setTimer(tT0, func() { t.finish(ErrT0Expired) })
	return t
}

// NewReceiver makes the answering terminal that receives pages.
func NewReceiver(opt Options) *Terminal {
	t := newTerminal(opt)
	t.state = sAnswer
	t.deadline = tT1
	if opt.T38 {
		t.send(t.t38CED(), nil)
	} else {
		t.send(&silence{200 * msSamples}, nil)
		t.send(newTone(2100, txLevel, 3000), nil)
	}
	t.send(&silence{tGap}, t.sendDIS)
	return t
}

func newTerminal(opt Options) *Terminal {
	if opt.Modems == 0 {
		opt.Modems = ModemV29
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.T38Repeat < 1 {
		opt.T38Repeat = 1
	}
	t := &Terminal{opt: opt, timer: -1}
	t.hdlc = &hdlcRx{onFrame: t.onFrame}
	t.fsk = newFSKRx(t.hdlc.putBit, nil)
	return t
}

func (t *Terminal) logf(format string, args ...any) {
	if t.opt.Log != nil {
		t.opt.Log(format, args...)
	}
}

// Done reports whether the session is over.
func (t *Terminal) Done() bool { return t.state == sDone }

// Result is the outcome (final once Done).
func (t *Terminal) Result() Result { return t.result }

// Hangup ends the session because the call ended.
func (t *Terminal) Hangup() {
	if t.state == sDone {
		return
	}
	if !t.sending && t.state == sWaitDCN { // every page is in
		t.finish(ErrOK)
		return
	}
	t.finish(ErrCallDropped)
}

// Process takes a block of received audio and fills out (the same length)
// with the audio to send: both directions in step, as a test drives it.
func (t *Terminal) Process(in, out []int16) {
	for i := range out {
		y := t.txSample()
		if i < len(in) {
			t.rxSample(float64(in[i]))
		} else {
			t.rxSample(0)
		}
		out[i] = clip16(y)
	}
}

// Receive takes received audio. On a call it runs from the media
// receive path, while Transmit runs from the send clock: the caller
// serialises the two.
func (t *Terminal) Receive(in []int16) {
	for _, v := range in {
		t.rxSample(float64(v))
	}
}

// Transmit fills out with the next audio to send; the session's timers
// run on this clock.
func (t *Terminal) Transmit(out []int16) {
	for i := range out {
		out[i] = clip16(t.txSample())
	}
}

func clip16(v float64) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	}
	return int16(math.Round(v))
}

func (t *Terminal) txSample() float64 {
	t.now++
	if t.state == sDone {
		return 0
	}
	var y float64
	if t.busy = len(t.txq) > 0; t.busy {
		y = t.nextTx()
	}
	if t.opt.T38 {
		t.t38Pending()
	}
	if t.timer > 0 && t.state != sDone {
		if t.timer--; t.timer == 0 {
			fn := t.onTimer
			t.timer, t.onTimer = -1, nil
			fn()
		}
	}
	return y
}

func (t *Terminal) rxSample(x float64) {
	if t.state == sDone || (t.busy && !t.listen) {
		return // our own signal is on the line (half duplex)
	}
	t.fsk.put(x)
	if t.fast != nil {
		t.fast.put(x)
	}
	if t.pending != nil && !t.fsk.carrier && len(t.txq) == 0 {
		fn := t.pending
		t.pending = nil
		t.send(&silence{tGap}, fn)
	}
}

func (t *Terminal) nextTx() float64 {
	for len(t.txq) > 0 {
		x, end := t.txq[0].src.sample()
		if !end {
			return x
		}
		fn := t.txq[0].done
		t.txq = t.txq[1:]
		if len(t.txq) == 0 { // our own signal is over: listen afresh
			t.fsk.reset()
			t.hdlc.reset()
		}
		if fn != nil {
			fn()
		}
	}
	return 0
}

func (t *Terminal) send(src txSource, done func()) {
	t.txq = append(t.txq, txItem{src, done})
}

func (t *Terminal) setTimer(n int, fn func()) { t.timer, t.onTimer = n, fn }
func (t *Terminal) stopTimer()                { t.timer, t.onTimer = -1, nil }

// reply sends once the far end has stopped transmitting (and a gap).
func (t *Terminal) reply(fn func()) { t.pending = fn }

func (t *Terminal) finish(code int) {
	t.result.Code = code
	t.result.Text = ResultText(code)
	if t.sending {
		t.result.Success = code == ErrOK
		t.result.Pages = t.pageSent
	} else {
		t.result.Pages = len(t.result.Received)
		t.result.TotalPages = t.result.Pages
		t.result.Success = code == ErrOK && t.result.Pages > 0
	}
	t.result.Rate = t.rate
	t.result.Fine = t.fine
	t.result.T38 = t.opt.T38
	t.logf("fax: finished: %s (%d pages)", t.result.Text, t.result.Pages)
	t.state = sDone
	t.txq, t.fast, t.pending = nil, nil, nil
	t.stopTimer()
}

// fail ends the session, saying goodbye (DCN) first where that is polite.
func (t *Terminal) fail(code int, dcn bool) {
	t.stopTimer()
	t.fast = nil
	if !dcn {
		t.finish(code)
		return
	}
	t.state = sClosing
	t.reply(func() { t.sendFrames(func() { t.finish(code) }, frame(fcfDCN, t.sending)) })
}

// ---- HDLC messages

func frame(fcf byte, x bool, fif ...byte) []byte {
	f := []byte{0xFF, 0x03, fcf}
	if x {
		f[2] |= 1
	}
	return append(f, fif...)
}

func identFrame(fcf byte, x bool, ident string) []byte {
	id := []byte(fmt.Sprintf("%-20.20s", ident))
	fif := make([]byte, 20)
	for i := range id { // sent last character first
		fif[19-i] = id[i]
	}
	return frame(fcf, x, fif...)
}

func parseIdent(fif []byte) string {
	b := make([]byte, 0, len(fif))
	for i := len(fif) - 1; i >= 0; i-- {
		b = append(b, fif[i])
	}
	return strings.TrimSpace(string(b))
}

// sendFrames sends one V.21 message (the last frame marked final).
func (t *Terminal) sendFrames(done func(), frames ...[]byte) {
	var names []string
	for i, f := range frames {
		if i == len(frames)-1 {
			f[1] = 0x13
		}
		names = append(names, fcfName(f[2]&0xFE))
	}
	t.logf("fax: send %s", strings.Join(names, " "))
	if t.opt.T38 {
		t.send(t.t38HDLC(frames), done)
		return
	}
	bits := &bitSource{bits: hdlcBits(38, frames)}
	t.send(newFSKTx(dbm0ToAmplitude(txLevel), bits.next), done)
}

// Stats says what the terminal heard (for troubleshooting a failed fax).
type Stats struct {
	V21Bursts int // V.21 (fax signalling) carriers heard
	FramesOK  int // T.30 frames received intact
	FramesBad int // frames with a bad checksum (a damaged signal)
	T38In     int // T.38 packets received
	T38Out    int // T.38 packets sent
}

// Stats returns what was heard so far.
func (t *Terminal) Stats() Stats {
	s := t.stats
	if !t.opt.T38 {
		s.V21Bursts = t.fsk.bursts
	}
	return s
}

func (t *Terminal) onFrame(f []byte, ok bool) {
	if !ok || len(f) < 3 || f[0] != 0xFF || (f[1] != 0x03 && f[1] != 0x13) {
		if t.stats.FramesBad++; t.stats.FramesBad <= 10 {
			t.logf("fax: heard a damaged frame (%d bytes)", len(f))
		}
		return
	}
	t.stats.FramesOK++
	t.frames = append(t.frames, f)
	if f[1] == 0x13 {
		msg := t.frames
		t.frames = nil
		var names []string
		for _, m := range msg {
			names = append(names, fcfName(fcfOf(m)))
		}
		t.logf("fax: got %s", strings.Join(names, " "))
		t.onMessage(msg)
	}
}

func fcfOf(f []byte) byte {
	if f[2] == fcfDTC {
		return f[2]
	}
	return f[2] &^ 1
}

// ---- DIS / DCS bits (bit n is FIF octet (n-1)/8, mask 1<<((n-1)%8))

func setBit(fif []byte, n int) { fif[(n-1)/8] |= 1 << uint((n-1)%8) }

func bitSet(fif []byte, n int) bool {
	i := (n - 1) / 8
	return i < len(fif) && fif[i]&(1<<uint((n-1)%8)) != 0
}

// minimum scan times: the DIS code to what we send (normal, fine), and
// the DCS code in milliseconds.
var (
	scanNormal = [8]int{0, 1, 2, 0, 4, 4, 2, 7}
	scanFine   = [8]int{0, 1, 2, 2, 4, 0, 1, 7}
	scanMs     = [8]int{20, 5, 10, 0, 40, 0, 0, 0}
)

func (t *Terminal) ourDIS() []byte {
	fif := make([]byte, 3)
	setBit(fif, 10) // ready to receive
	if t.opt.Modems&ModemV29 != 0 {
		setBit(fif, 11)
	}
	setBit(fif, 15)  // fine
	setBit(fif, 20)  // unlimited length
	fif[2] |= 7 << 4 // 0 ms minimum scan time
	return frame(fcfDIS, false, fif...)
}

func rateCode(rate int) byte {
	switch rate {
	case 9600:
		return 1
	case 7200:
		return 3
	case 4800:
		return 2
	}
	return 0
}

func (t *Terminal) ourDCS(scanCode int) []byte {
	fif := make([]byte, 3)
	setBit(fif, 10) // receive
	fif[1] |= rateCode(t.rate) << 2
	if t.fine {
		setBit(fif, 15)
	}
	fif[2] |= byte(scanCode) << 4
	return frame(fcfDCS, true, fif...)
}

// ---- messages

func (t *Terminal) onMessage(msg [][]byte) {
	if t.state == sDone {
		return
	}
	last := msg[len(msg)-1]
	cmd := fcfOf(last)
	fif := last[3:]
	for _, f := range msg {
		switch fcfOf(f) {
		case fcfCSI, fcfTSI:
			t.result.RemoteID = parseIdent(f[3:])
		}
	}
	if t.sending {
		t.senderMessage(cmd, fif)
	} else {
		t.receiverMessage(cmd, fif)
	}
}

// ---- sending (the caller)

func (t *Terminal) senderMessage(cmd byte, fif []byte) {
	switch t.state {
	case sCallWaitDIS, sWaitCFR:
		switch cmd {
		case fcfDIS:
			if t.state == sWaitCFR { // our DCS was not heard: again
				if t.retries++; t.retries > 3 {
					t.fail(ErrTxPhaseB, true)
					return
				}
			} else {
				t.txq, t.listen = nil, false // stop CNG
				if !t.negotiate(fif) {
					return
				}
			}
			t.stopTimer()
			t.reply(t.sendTraining)
		case fcfCFR:
			if t.state != sWaitCFR {
				return
			}
			t.stopTimer()
			t.retries = 0
			t.reply(t.sendPage)
		case fcfFTT:
			if t.state != sWaitCFR {
				return
			}
			t.stopTimer()
			t.rates = t.rates[1:]
			if len(t.rates) == 0 {
				t.fail(ErrCannotTrain, true)
				return
			}
			t.rate = t.rates[0]
			t.reply(t.sendTraining)
		case fcfDCN:
			if t.state == sCallWaitDIS {
				t.finish(ErrTxGotDCN)
			} else {
				t.finish(ErrTxBadDCS)
			}
		}
	case sWaitMCF:
		switch cmd {
		case fcfMCF, fcfRTP, fcfPIP:
			t.stopTimer()
			t.retries = 0
			t.pageSent++
			t.page++
			t.pageTry = 0
			if t.opt.OnPage != nil {
				t.opt.OnPage(t.pageSent, len(t.pages))
			}
			if t.page >= len(t.pages) {
				t.reply(func() { t.sendFrames(func() { t.finish(ErrOK) }, frame(fcfDCN, true)) })
				return
			}
			if cmd != fcfMCF {
				t.reply(t.sendTraining)
				return
			}
			t.reply(t.sendPage)
		case fcfRTN, fcfPIN:
			t.stopTimer()
			t.retries = 0
			if t.pageTry++; t.pageTry > 2 {
				t.fail(ErrRetryDCN, true)
				return
			}
			if len(t.rates) > 1 { // a cleaner line at a lower rate
				t.rates = t.rates[1:]
				t.rate = t.rates[0]
			}
			t.reply(t.sendTraining)
		case fcfCRP:
			t.stopTimer()
			t.reply(t.sendPPM)
		case fcfDCN:
			t.finish(ErrTxBadPage)
		}
	}
}

// negotiate takes the far end's DIS.
func (t *Terminal) negotiate(fif []byte) bool {
	if !bitSet(fif, 10) {
		t.fail(ErrRxIncapable, true)
		return false
	}
	t.rates = nil
	if bitSet(fif, 11) && t.opt.Modems&ModemV29 != 0 {
		t.rates = []int{9600, 7200}
	}
	if len(t.rates) == 0 {
		t.fail(ErrIncompatible, true)
		return false
	}
	t.rate = t.rates[0]
	t.fine = bitSet(fif, 15) && t.pages[0].YRes == resFine
	code := 0
	if len(fif) > 2 {
		code = int(fif[2]>>4) & 7
	}
	if t.fine {
		t.scanCode = scanFine[code]
	} else {
		t.scanCode = scanNormal[code]
	}
	t.minScan = scanMs[t.scanCode]
	t.state = sSendTraining
	return true
}

// sendTraining sends TSI + DCS, then TCF (1.5 s of zeros at the rate).
func (t *Terminal) sendTraining() {
	t.state = sSendTraining
	t.sendFrames(nil, identFrame(fcfTSI, true, t.opt.Ident), t.ourDCS(t.scanCode))
	t.send(&silence{tGap}, nil)
	t.logf("fax: send TCF at %d", t.rate)
	t.send(t.fastSource(t.rate, make([]byte, t.rate*3/2)), func() {
		t.state = sWaitCFR
		t.setTimer(tT4, func() {
			if t.retries++; t.retries > 3 {
				t.fail(ErrTxPhaseB, true)
				return
			}
			t.sendTraining()
		})
	})
}

// pageImage is the page as it goes on the line: header drawn, and at the
// negotiated resolution.
func (t *Terminal) pageImage() *Page {
	src := t.pages[t.page]
	p := &Page{Width: src.Width, XRes: src.XRes, YRes: src.YRes, Height: len(src.Rows)}
	for _, r := range src.Rows {
		p.Rows = append(p.Rows, append([]byte(nil), r...))
	}
	if t.opt.Header != "" || t.opt.Ident != "" {
		h := t.opt.Now().Format("2006-01-02 15:04")
		if t.opt.Header != "" {
			h += "  " + t.opt.Header
		}
		if t.opt.Ident != "" {
			h += "  " + t.opt.Ident
		}
		h += fmt.Sprintf("  P.%d/%d", t.page+1, len(t.pages))
		p.Header(h)
	}
	if !t.fine {
		p = p.Standard()
	}
	return p
}

func (t *Terminal) sendPage() {
	t.state = sSendPage
	bits := EncodePage(t.pageImage(), t.rate*t.minScan/1000)
	t.logf("fax: send page %d at %d (%d bits)", t.page+1, t.rate, len(bits))
	t.send(t.fastSource(t.rate, bits), nil)
	t.send(&silence{tGap}, t.sendPPM)
}

// sendPPM sends the post-page message: MPS (more pages) or EOP.
func (t *Terminal) sendPPM() {
	t.lastPPM = fcfMPS
	if t.page == len(t.pages)-1 {
		t.lastPPM = fcfEOP
	}
	t.sendFrames(func() {
		t.state = sWaitMCF
		t.setTimer(tT4, func() {
			if t.retries++; t.retries > 3 {
				t.fail(ErrTxPhaseD, true)
				return
			}
			t.sendPPM()
		})
	}, frame(t.lastPPM, true))
}

// ---- receiving (the answerer)

func (t *Terminal) sendDIS() {
	t.state = sWaitDCS
	t.sendFrames(func() {
		t.setTimer(tT4, t.disTimeout)
	}, identFrame(fcfCSI, false, t.opt.Ident), t.ourDIS())
}

// disTimeout: no DCS yet. DIS again until T1 (or, once a DCS came, T2) is over.
func (t *Terminal) disTimeout() {
	if t.gotDCS {
		t.fail(ErrRxT2Exp, true)
		return
	}
	if t.now >= t.deadline {
		t.fail(ErrT1Expired, true)
		return
	}
	t.sendDIS()
}

func (t *Terminal) receiverMessage(cmd byte, fif []byte) {
	switch t.state {
	case sWaitDCS, sTCF, sWaitPage, sWaitPPM, sWaitDCN:
	default:
		return
	}
	switch cmd {
	case fcfDCS:
		if t.state == sWaitPPM || t.state == sWaitDCN {
			return
		}
		t.startTCF(fif)
	case fcfMPS, fcfEOP, fcfEOM:
		t.postPage(cmd)
	case fcfDCN:
		switch t.state {
		case sWaitDCN:
			t.finish(ErrOK)
		case sWaitDCS:
			t.finish(ErrRxDCNWhy)
		case sWaitPPM:
			t.finish(ErrRxDCNFax)
		default:
			if t.lastReply == fcfMCF && t.lastPPM == fcfEOP {
				t.finish(ErrOK)
				return
			}
			t.finish(ErrRxDCNData)
		}
	case fcfCRP:
		if t.lastReply != 0 {
			t.reply(func() { t.sendReply(t.lastReply) })
		}
	}
}

// startTCF takes a DCS and gets ready for the training check.
func (t *Terminal) startTCF(fif []byte) {
	rate := 0
	if len(fif) > 1 {
		switch (fif[1] >> 2) & 0xF {
		case 1:
			rate = 9600
		case 3:
			rate = 7200
		}
	}
	if rate == 0 || !bitSet(fif, 10) {
		t.fail(ErrIncompatible, true)
		return
	}
	t.stopTimer()
	t.gotDCS = true
	t.rate = rate
	t.fine = bitSet(fif, 15)
	t.state = sTCF
	t.tcfZeros, t.tcfMost, t.tcfOK = 0, 0, false
	t.fast = t.newFast(rate)
	t.setTimer(tT4+2000*msSamples, func() { // no training came
		t.fast = nil
		t.reply(func() { t.sendReply(fcfFTT) })
	})
}

func (t *Terminal) fastBit(bit int) {
	switch t.state {
	case sTCF:
		if bit == 0 {
			if t.tcfZeros++; t.tcfZeros > t.tcfMost {
				t.tcfMost = t.tcfZeros
			}
		} else {
			t.tcfZeros = 0
		}
	case sPage:
		t.dec.putBit(bit)
	}
}

func (t *Terminal) fastEvent(e RxEvent) {
	switch t.state {
	case sTCF:
		switch e {
		case RxTrained:
			t.tcfOK = true
		case RxCarrierDown:
			t.stopTimer()
			t.fast = nil
			// TCF is 1.5 s of zeros: most of a second clean is good.
			good := t.tcfOK && t.tcfMost >= t.rate*3/4
			t.logf("fax: TCF at %d: %d zeros in a row", t.rate, t.tcfMost)
			if good {
				t.reply(func() { t.sendReply(fcfCFR) })
			} else {
				t.reply(func() { t.sendReply(fcfFTT) })
			}
		}
	case sWaitPage:
		if e == RxTrained {
			t.stopTimer()
			t.state = sPage
			t.inPage = true
			t.dec = newPageDecoder(PageWidth)
		}
	case sPage:
		if e == RxCarrierDown {
			t.fast = nil
			t.endPage()
		}
	}
}

// endPage keeps the page just received and waits for what follows it.
func (t *Terminal) endPage() {
	t.dec.finish()
	rows := len(t.dec.Rows)
	t.pageGood = rows > 0 && t.dec.BadRows*10 <= rows
	t.logf("fax: page: %d rows, %d bad, RTC %v", rows, t.dec.BadRows, t.dec.Done)
	if t.pageGood {
		yres := resStandard
		if t.fine {
			yres = resFine
		}
		p := &Page{Width: PageWidth, XRes: 204, YRes: yres, Rows: t.dec.Rows, Height: rows}
		t.result.Received = append(t.result.Received, p)
		t.result.BadRows += t.dec.BadRows
		if t.opt.OnPage != nil {
			t.opt.OnPage(len(t.result.Received), len(t.result.Received))
		}
	}
	t.state = sWaitPPM
	t.setTimer(tT2, func() { t.fail(ErrRxT2ExpMPS, true) })
}

// postPage answers MPS / EOP / EOM.
func (t *Terminal) postPage(cmd byte) {
	switch t.state {
	case sWaitPPM:
	case sWaitPage, sWaitDCN, sWaitDCS: // our reply was lost: the same again
		if t.lastReply != 0 && cmd == t.lastPPM {
			t.stopTimer()
			t.reply(func() { t.sendReply(t.lastReply) })
			return
		}
		if t.state != sWaitPage {
			return
		}
		t.pageGood = false // the page never trained here
	default:
		return
	}
	t.stopTimer()
	t.lastPPM = cmd
	if !t.pageGood {
		t.reply(func() { t.sendReply(fcfRTN) })
		return
	}
	t.reply(func() { t.sendReply(fcfMCF) })
}

// sendReply sends a response and moves on to what it leads to.
func (t *Terminal) sendReply(fcf byte) {
	t.lastReply = fcf
	t.sendFrames(func() {
		switch fcf {
		case fcfCFR, fcfRTN:
			t.waitPage()
		case fcfFTT:
			t.state = sWaitDCS
			t.setTimer(tT2, t.disTimeout)
		case fcfMCF:
			switch t.lastPPM {
			case fcfEOP:
				t.state = sWaitDCN
				t.setTimer(tT2, func() { t.finish(ErrOK) })
			case fcfEOM: // back to phase B: a new DIS
				t.gotDCS = false
				t.deadline = t.now + tT1
				t.send(&silence{tGap}, t.sendDIS)
			default:
				t.waitPage()
			}
		}
	}, frame(fcf, false))
}

func (t *Terminal) waitPage() {
	t.state = sWaitPage
	t.inPage = false
	t.fast = t.newFast(t.rate)
	t.setTimer(tT2, func() { t.fail(ErrRxT2ExpFax, true) })
}
