package fax

// HDLC framing of the T.30 messages on V.21: flags (0x7E), zero-bit
// insertion and a CRC-16 frame check sequence; bits go least significant
// first.

func crc16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// hdlcBits builds the bits of a T.30 message: preamble flags, then the
// frames (each with its FCS), then closing flags.
func hdlcBits(preambleFlags int, frames [][]byte) []byte {
	var bits []byte
	flag := func() {
		for i := 0; i < 8; i++ {
			bits = append(bits, byte(0x7E>>uint(i))&1)
		}
	}
	for i := 0; i < preambleFlags; i++ {
		flag()
	}
	for _, f := range frames {
		fcs := ^crc16(f)
		data := append(append([]byte(nil), f...), byte(fcs), byte(fcs>>8))
		ones := 0
		for _, b := range data {
			for i := 0; i < 8; i++ {
				bit := (b >> uint(i)) & 1
				bits = append(bits, bit)
				if bit == 1 {
					if ones++; ones == 5 {
						bits = append(bits, 0)
						ones = 0
					}
				} else {
					ones = 0
				}
			}
		}
		flag()
	}
	flag()
	return bits
}

// hdlcRx collects frames from bits.
type hdlcRx struct {
	bits    []byte
	ones    int
	inFrame bool
	onFrame func(frame []byte, ok bool)
}

func (h *hdlcRx) reset() {
	h.bits, h.ones, h.inFrame = h.bits[:0], 0, false
}

func (h *hdlcRx) putBit(bit int) {
	if bit == 1 {
		h.ones++
		if h.ones >= 7 { // abort / idle
			h.reset()
			return
		}
		h.bits = append(h.bits, 1)
		return
	}
	switch h.ones {
	case 6: // flag: the 0 and six 1s before were the flag, not data
		n := len(h.bits) - 7
		if h.inFrame && n >= 8*5 {
			if n%8 == 0 {
				frame := make([]byte, n/8)
				for i := 0; i < n; i++ {
					frame[i/8] |= h.bits[i] << uint(i%8)
				}
				ok := crc16(frame) == 0xF0B8
				if ok {
					frame = frame[:len(frame)-2]
				}
				h.onFrame(frame, ok)
			} else {
				h.onFrame(nil, false)
			}
		}
		h.bits = h.bits[:0]
		h.inFrame = true
	case 5: // stuffed zero
	default:
		h.bits = append(h.bits, 0)
	}
	h.ones = 0
}
