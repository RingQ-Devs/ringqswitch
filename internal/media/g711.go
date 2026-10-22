// Package media implements RTP endpoints, relaying, G.711 coding and simple
// audio sources for RingQswitch.
package media

// G.711 mu-law and A-law conversion (ITU-T G.711), after the public domain
// reference implementation by Sun Microsystems.

const (
	ulawBias = 0x84
	ulawClip = 32635
)

var (
	ulawDecode [256]int16
	alawDecode [256]int16
	ulawToAlaw [256]byte
	alawToUlaw [256]byte
	ulawExp    [256]byte
)

func init() {
	for i := 0; i < 256; i++ {
		e := byte(0)
		for v := i >> 1; v > 0; v >>= 1 {
			e++
		}
		ulawExp[i] = e
	}
	for i := 0; i < 256; i++ {
		ulawDecode[i] = decodeUlaw(byte(i))
		alawDecode[i] = decodeAlaw(byte(i))
	}
	for i := 0; i < 256; i++ {
		ulawToAlaw[i] = LinearToAlaw(ulawDecode[i])
		alawToUlaw[i] = LinearToUlaw(alawDecode[i])
	}
}

// LinearToUlaw encodes one 16-bit sample.
func LinearToUlaw(s int16) byte {
	sample := int(s)
	sign := 0
	if sample < 0 {
		sample = -sample
		sign = 0x80
	}
	if sample > ulawClip {
		sample = ulawClip
	}
	sample += ulawBias
	exp := int(ulawExp[(sample>>7)&0xFF])
	mant := (sample >> (exp + 3)) & 0x0F
	return ^byte(sign | exp<<4 | mant)
}

func decodeUlaw(u byte) int16 {
	u = ^u
	sign := u & 0x80
	exp := (u >> 4) & 0x07
	mant := int(u & 0x0F)
	sample := ((mant << 3) + ulawBias) << exp
	sample -= ulawBias
	if sign != 0 {
		return int16(-sample)
	}
	return int16(sample)
}

var alawSegEnd = [8]int{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

// LinearToAlaw encodes one 16-bit sample.
func LinearToAlaw(s int16) byte {
	pcm := int(s) >> 3
	var mask int
	if pcm >= 0 {
		mask = 0xD5
	} else {
		mask = 0x55
		pcm = -pcm - 1
	}
	seg := 0
	for seg < 8 && pcm > alawSegEnd[seg] {
		seg++
	}
	if seg >= 8 {
		return byte(0x7F ^ mask)
	}
	aval := seg << 4
	if seg < 2 {
		aval |= (pcm >> 1) & 0x0F
	} else {
		aval |= (pcm >> seg) & 0x0F
	}
	return byte(aval ^ mask)
}

func decodeAlaw(a byte) int16 {
	a ^= 0x55
	t := int(a&0x0F) << 4
	seg := int(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= seg - 1
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

// UlawToLinear decodes one mu-law byte.
func UlawToLinear(u byte) int16 { return ulawDecode[u] }

// AlawToLinear decodes one A-law byte.
func AlawToLinear(a byte) int16 { return alawDecode[a] }

// Decode converts a G.711 payload to linear samples. ok is false for
// unsupported codecs.
func Decode(codec string, payload []byte, out []int16) ([]int16, bool) {
	out = out[:0]
	switch codec {
	case "PCMU":
		for _, b := range payload {
			out = append(out, ulawDecode[b])
		}
	case "PCMA":
		for _, b := range payload {
			out = append(out, alawDecode[b])
		}
	default:
		return nil, false
	}
	return out, true
}

// Encode converts linear samples to a G.711 payload.
func Encode(codec string, pcm []int16, out []byte) ([]byte, bool) {
	out = out[:0]
	switch codec {
	case "PCMU":
		for _, s := range pcm {
			out = append(out, LinearToUlaw(s))
		}
	case "PCMA":
		for _, s := range pcm {
			out = append(out, LinearToAlaw(s))
		}
	default:
		return nil, false
	}
	return out, true
}

// Transcode converts a G.711 payload in place between mu-law and A-law.
func Transcode(from, to string, payload []byte) bool {
	switch {
	case from == to:
		return true
	case from == "PCMU" && to == "PCMA":
		for i, b := range payload {
			payload[i] = ulawToAlaw[b]
		}
	case from == "PCMA" && to == "PCMU":
		for i, b := range payload {
			payload[i] = alawToUlaw[b]
		}
	default:
		return false
	}
	return true
}

// CanTerminate reports whether we can encode/decode the codec locally.
func CanTerminate(codec string) bool { return codec == "PCMU" || codec == "PCMA" }
