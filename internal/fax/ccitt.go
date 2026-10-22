package fax

import "errors"

// Decoding the CCITT compressions a TIFF may hold: modified Huffman RLE
// (TIFF compression 2), T.4 one or two dimensional (3) and T.6 (4, "G4",
// what the PDF converter's tiffg4 device writes). Rows come out packed, MSB first,
// 1 = a pixel of a black run.

var errCCITT = errors.New("broken CCITT image data")

type ccittReader struct {
	data []byte
	pos  int // bit position
}

func (r *ccittReader) bit() int {
	if r.pos >= len(r.data)*8 {
		return -1
	}
	b := int(r.data[r.pos>>3]>>(7-uint(r.pos&7))) & 1
	r.pos++
	return b
}

func (r *ccittReader) peek(n int) int { // the next n bits as a number (missing bits are 0)
	v := 0
	for i := 0; i < n; i++ {
		p := r.pos + i
		b := 0
		if p < len(r.data)*8 {
			b = int(r.data[p>>3]>>(7-uint(p&7))) & 1
		}
		v = v<<1 | b
	}
	return v
}

func (r *ccittReader) align() { r.pos = (r.pos + 7) &^ 7 }

// eol skips an EOL (and any fill zeros before it), reporting whether one was there.
func (r *ccittReader) eol() bool {
	p := r.pos
	zeros := 0
	for {
		b := r.bit()
		if b < 0 {
			r.pos = p
			return false
		}
		if b == 1 {
			if zeros >= 11 {
				return true
			}
			r.pos = p
			return false
		}
		zeros++
	}
}

// run reads one colour's run (make-up codes, then a terminating code).
func (r *ccittReader) run(color int) (int, error) {
	total := 0
	for {
		n := whiteTrie
		if color == 1 {
			n = blackTrie
		}
		for n.run < 0 {
			b := r.bit()
			if b < 0 {
				return 0, errCCITT
			}
			if n = n.next[b]; n == nil {
				return 0, errCCITT
			}
		}
		total += n.run
		if n.run < 64 {
			return total, nil
		}
	}
}

// mode codes of two-dimensional coding
const (
	modePass = iota
	modeHoriz
	modeV0
	modeVR1
	modeVR2
	modeVR3
	modeVL1
	modeVL2
	modeVL3
	modeEOL
)

func (r *ccittReader) mode() (int, error) {
	switch {
	case r.peek(1) == 1:
		r.pos++
		return modeV0, nil
	case r.peek(3) == 0b011:
		r.pos += 3
		return modeVR1, nil
	case r.peek(3) == 0b010:
		r.pos += 3
		return modeVL1, nil
	case r.peek(3) == 0b001:
		r.pos += 3
		return modeHoriz, nil
	case r.peek(4) == 0b0001:
		r.pos += 4
		return modePass, nil
	case r.peek(6) == 0b000011:
		r.pos += 6
		return modeVR2, nil
	case r.peek(6) == 0b000010:
		r.pos += 6
		return modeVL2, nil
	case r.peek(7) == 0b0000011:
		r.pos += 7
		return modeVR3, nil
	case r.peek(7) == 0b0000010:
		r.pos += 7
		return modeVL3, nil
	case r.peek(12) == 1:
		r.pos += 12
		return modeEOL, nil
	}
	return 0, errCCITT
}

// line1D decodes a modified Huffman line into its changing elements.
func (r *ccittReader) line1D(width int) ([]int, error) {
	var tr []int
	x, color := 0, 0
	for x < width {
		n, err := r.run(color)
		if err != nil {
			return nil, err
		}
		x += n
		if x > width {
			return nil, errCCITT
		}
		if x < width {
			tr = append(tr, x)
		}
		color ^= 1
	}
	return tr, nil
}

// line2D decodes a two-dimensional line against the reference line's
// changing elements.
func (r *ccittReader) line2D(width int, ref []int) ([]int, error) {
	var tr []int
	a0, color := -1, 0
	for a0 < width {
		// b1: the first change on the reference line right of a0 to the
		// colour opposite a0's; b2 the one after it.
		b1, b2 := width, width
		for i, p := range ref {
			if p > a0 && i%2 == color { // even changes turn black, odd white
				b1 = p
				if i+1 < len(ref) {
					b2 = ref[i+1]
				}
				break
			}
		}
		m, err := r.mode()
		if err != nil {
			return nil, err
		}
		switch m {
		case modePass:
			a0 = b2
		case modeHoriz:
			start := max(a0, 0)
			r1, err := r.run(color)
			if err != nil {
				return nil, err
			}
			r2, err := r.run(color ^ 1)
			if err != nil {
				return nil, err
			}
			a1, a2 := start+r1, start+r1+r2
			if a2 > width {
				return nil, errCCITT
			}
			if a1 < width {
				tr = append(tr, a1)
			}
			if a2 < width {
				tr = append(tr, a2)
			}
			a0 = a2
		case modeEOL:
			return nil, errCCITT
		default:
			d := map[int]int{modeV0: 0, modeVR1: 1, modeVR2: 2, modeVR3: 3, modeVL1: -1, modeVL2: -2, modeVL3: -3}[m]
			a1 := b1 + d
			if a1 < 0 || a1 > width || a1 < a0 {
				return nil, errCCITT
			}
			if a1 < width {
				tr = append(tr, a1)
			}
			a0 = a1
			color ^= 1
		}
	}
	return tr, nil
}

func paintRow(width int, tr []int) []byte {
	row := make([]byte, (width+7)/8)
	color, x := 0, 0
	for _, p := range append(tr, width) {
		if color == 1 {
			for ; x < p; x++ {
				row[x>>3] |= 0x80 >> uint(x&7)
			}
		}
		x = p
		color ^= 1
	}
	return row
}

// decodeCCITT decodes one TIFF strip of rows.
func decodeCCITT(data []byte, width, rows int, compression, t4opts uint32) ([][]byte, error) {
	r := &ccittReader{data: data}
	var out [][]byte
	var ref []int // an all-white line before the first
	for y := 0; y < rows; y++ {
		var tr []int
		var err error
		switch compression {
		case 2:
			tr, err = r.line1D(width)
			r.align()
		case 3:
			r.eol() // with any fill bits before it (byte-aligned EOLs)
			twoD := false
			if t4opts&1 != 0 {
				twoD = r.bit() == 0
			}
			if twoD {
				tr, err = r.line2D(width, ref)
			} else {
				tr, err = r.line1D(width)
			}
		case 4:
			tr, err = r.line2D(width, ref)
		default:
			return nil, errors.New("not a CCITT compression")
		}
		if err != nil {
			if len(out) > 0 { // keep what decoded
				return out, nil
			}
			return nil, err
		}
		out = append(out, paintRow(width, tr))
		ref = tr
	}
	return out, nil
}
