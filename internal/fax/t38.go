package fax

import (
	"errors"
	"fmt"
)

// T.38: fax over IP. Instead of modem audio, the session's signals travel
// as IFP packets: indicators (a tone or a modem has started) and data
// (the HDLC frames and the page bits a modem would have carried), sent in
// UDPTL datagrams. The carrier's gateway does the modem work towards the
// fax machine. IFP is ASN.1 PER (aligned), as T.38 clause 9 defines it.

// Indicators.
const (
	t38NoSignal    = 0
	t38CNG         = 1
	t38CED         = 2
	t38V21Preamble = 3
	t38V27_2400    = 4
	t38V27_4800    = 5
	t38V29_7200    = 6
	t38V29_9600    = 7
	t38V17First    = 8  // V.17 7200 short training
	t38V17Last     = 15 // V.17 14400 long training
)

// Data types.
const (
	t38DataV21      = 0
	t38DataV27_2400 = 1
	t38DataV27_4800 = 2
	t38DataV29_7200 = 3
	t38DataV29_9600 = 4
)

// Data field types.
const (
	t38HDLCData         = 0
	t38HDLCSigEnd       = 1
	t38HDLCFCSOK        = 2
	t38HDLCFCSBad       = 3
	t38HDLCFCSOKSigEnd  = 4
	t38HDLCFCSBadSigEnd = 5
	t38T4Data           = 6
	t38T4SigEnd         = 7
)

var t38IndNames = [...]string{"no-signal", "cng", "ced", "v21-preamble", "v27-2400-training",
	"v27-4800-training", "v29-7200-training", "v29-9600-training", "v17-7200-short-training",
	"v17-7200-long-training", "v17-9600-short-training", "v17-9600-long-training",
	"v17-12000-short-training", "v17-12000-long-training", "v17-14400-short-training", "v17-14400-long-training"}

func t38IndName(ind int) string {
	if ind >= 0 && ind < len(t38IndNames) {
		return t38IndNames[ind]
	}
	return fmt.Sprintf("indicator-%d", ind)
}

type t38Field struct {
	typ  int
	data []byte
}

// ifp is a decoded IFP packet: an indicator, or data.
type ifp struct {
	indicator int // -1 for data
	dataType  int
	fields    []t38Field
}

func encodeIndicator(ind int) []byte { return []byte{byte(ind << 1)} }

// encodeData builds a data IFP. T.38 version 0 encodes the field type
// without the extension bit later versions added.
func encodeData(version, dataType int, fields []t38Field) []byte {
	b := []byte{0xC0 | byte(dataType<<1)}
	b = appendLength(b, len(fields))
	for _, f := range fields {
		var present byte
		if len(f.data) > 0 {
			present = 0x80
		}
		if version == 0 {
			b = append(b, present|byte(f.typ<<4))
		} else {
			b = append(b, present|byte(f.typ<<3))
		}
		if len(f.data) > 0 {
			n := len(f.data) - 1
			b = append(b, byte(n>>8), byte(n))
			b = append(b, f.data...)
		}
	}
	return b
}

var errIFP = errors.New("t38: bad IFP packet")

func decodeIFP(version int, b []byte) (ifp, error) {
	p := ifp{indicator: -1}
	if len(b) == 0 {
		return p, errIFP
	}
	if b[0]&0x40 == 0 { // t30-indicator
		if b[0]&0x20 != 0 { // V.8 / V.34 (later versions): not for us
			p.indicator = 16
			return p, nil
		}
		p.indicator = int(b[0]>>1) & 0xF
		return p, nil
	}
	if b[0]&0x20 != 0 { // V.8 / V.34 data
		p.dataType = 16
		return p, nil
	}
	p.dataType = int(b[0]>>1) & 0xF
	if b[0]&0x80 == 0 {
		return p, nil
	}
	i := 1
	count, i, err := decodeLength(b, i)
	if err != nil {
		return p, err
	}
	for k := 0; k < count; k++ {
		if i >= len(b) {
			return p, errIFP
		}
		present := b[i]&0x80 != 0
		var f t38Field
		if version == 0 {
			f.typ = int(b[i]>>4) & 7
			i++
		} else if b[i]&0x40 != 0 { // extension (V.34 fields)
			if i+1 >= len(b) {
				return p, errIFP
			}
			f.typ = 8 + int(b[i+1])
			i += 2
		} else {
			f.typ = int(b[i]>>3) & 7
			i++
		}
		if present {
			if i+2 > len(b) {
				return p, errIFP
			}
			n := int(b[i])<<8 | int(b[i+1]) + 1
			i += 2
			if i+n > len(b) {
				return p, errIFP
			}
			f.data = b[i : i+n]
			i += n
		}
		p.fields = append(p.fields, f)
	}
	return p, nil
}

// appendLength writes a PER length determinant (1 or 2 octets).
func appendLength(b []byte, n int) []byte {
	if n < 0x80 {
		return append(b, byte(n))
	}
	return append(b, 0x80|byte(n>>8), byte(n))
}

func decodeLength(b []byte, i int) (int, int, error) {
	if i >= len(b) {
		return 0, i, errIFP
	}
	if b[i]&0x80 == 0 {
		return int(b[i]), i + 1, nil
	}
	if b[i]&0x40 == 0 && i+1 < len(b) {
		return int(b[i]&0x3F)<<8 | int(b[i+1]), i + 2, nil
	}
	return 0, i, errIFP // fragments: never this large
}

// ---- UDPTL (T.38 clause 9.1): sequence number, the IFP, and the
// previous IFPs again (redundancy), so a lost datagram is recovered from
// the next one.

// UDPTL error correction schemes (SDP T38FaxUdpEC).
const (
	UDPTLNone       = 0
	UDPTLRedundancy = 1
	UDPTLFEC        = 2
)

// UDPTL frames IFP packets into datagrams and back.
type UDPTL struct {
	// Redundancy is how many previous packets each datagram repeats.
	Redundancy int
	// MaxDatagram is the far end's T38FaxMaxDatagram (0 = no limit).
	MaxDatagram int

	seq  uint16
	hist [][]byte

	rxNext  int // the sequence number expected next; -1 before the first
	Lost    int // packets neither received nor recovered
	Recover int // packets recovered from redundancy
}

// NewUDPTL makes a UDPTL session.
func NewUDPTL(redundancy, maxDatagram int) *UDPTL {
	return &UDPTL{Redundancy: redundancy, MaxDatagram: maxDatagram, rxNext: -1}
}

// Packet wraps one IFP for sending.
func (u *UDPTL) Packet(ifp []byte) []byte {
	b := []byte{byte(u.seq >> 8), byte(u.seq)}
	b = appendLength(b, len(ifp))
	b = append(b, ifp...)
	b = append(b, 0x00) // secondary-ifp-packets
	n := min(u.Redundancy, len(u.hist))
	if u.MaxDatagram > 0 { // keep within what the far end accepts
		size := len(b) + 1
		k := 0
		for ; k < n; k++ {
			e := u.hist[len(u.hist)-1-k]
			if size+len(e)+2 > u.MaxDatagram {
				break
			}
			size += len(e) + 2
		}
		n = k
	}
	b = appendLength(b, n)
	for k := 0; k < n; k++ { // the most recent first
		e := u.hist[len(u.hist)-1-k]
		b = appendLength(b, len(e))
		b = append(b, e...)
	}
	if u.Redundancy > 0 {
		u.hist = append(u.hist, append([]byte(nil), ifp...))
		if len(u.hist) > u.Redundancy {
			u.hist = u.hist[len(u.hist)-u.Redundancy:]
		}
	}
	u.seq++
	return b
}

// Receive unwraps a datagram: the IFPs it brings, in order, including
// any it recovers. Duplicates and late datagrams bring nothing.
func (u *UDPTL) Receive(d []byte) ([][]byte, error) {
	if len(d) < 3 {
		return nil, errIFP
	}
	seq := int(d[0])<<8 | int(d[1])
	n, i, err := decodeLength(d, 2)
	if err != nil || i+n > len(d) {
		return nil, errIFP
	}
	primary := d[i : i+n]
	i += n
	var secondary [][]byte
	if i < len(d) && d[i]&0x80 == 0 { // redundancy (FEC is not decoded)
		i++
		count, j, err := decodeLength(d, i)
		if err == nil {
			i = j
			for k := 0; k < count; k++ {
				m, j, err := decodeLength(d, i)
				if err != nil || j+m > len(d) {
					break
				}
				secondary = append(secondary, d[j:j+m])
				i = j + m
			}
		}
	}
	if u.rxNext < 0 {
		u.rxNext = seq
	}
	gap := int(uint16(seq - u.rxNext))
	if gap >= 0x8000 { // older than expected: a duplicate or late
		return nil, nil
	}
	var out [][]byte
	if gap > 1000 { // the far end started over
		gap = 0
	}
	for k := gap - 1; k >= 0; k-- { // seq-1-k, oldest first
		if k < len(secondary) {
			out = append(out, secondary[k])
			u.Recover++
		} else {
			u.Lost++
		}
	}
	out = append(out, primary)
	u.rxNext = int(uint16(seq + 1))
	return out, nil
}
