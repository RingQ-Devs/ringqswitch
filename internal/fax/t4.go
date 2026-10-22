package fax

// T.4 one-dimensional (modified Huffman) coding of fax pages, as used
// without ECM: every scan line is coded as alternating white and black
// runs, followed by EOL; a page starts with EOL and ends with RTC (six
// EOLs).

const (
	// PageWidth is the width of an A4 fax line in pixels (215 mm at 8 pels/mm).
	PageWidth = 1728
	eolCode   = "000000000001"
)

type mhCode struct {
	run  int
	bits string
}

// mhTrie decodes the codes of one colour bit by bit.
type mhTrie struct {
	next [2]*mhTrie
	run  int // >= 0 at a leaf
}

var (
	whiteEnc, blackEnc   map[int]string
	whiteTrie, blackTrie *mhTrie
)

func init() {
	whiteEnc, whiteTrie = buildMH(whiteCodes)
	blackEnc, blackTrie = buildMH(blackCodes)
}

func buildMH(codes []mhCode) (map[int]string, *mhTrie) {
	enc := map[int]string{}
	root := &mhTrie{run: -1}
	for _, c := range codes {
		enc[c.run] = c.bits
		n := root
		for _, ch := range c.bits {
			b := int(ch - '0')
			if n.next[b] == nil {
				n.next[b] = &mhTrie{run: -1}
			}
			n = n.next[b]
		}
		n.run = c.run
	}
	return enc, root
}

// bitBuf collects bits, one per byte (simple, and pages are small).
type bitBuf struct{ bits []byte }

func (b *bitBuf) putString(s string) {
	for i := 0; i < len(s); i++ {
		b.bits = append(b.bits, s[i]-'0')
	}
}

func (b *bitBuf) putRun(run int, enc map[int]string) {
	for run >= 64 {
		m := run / 64 * 64
		if m > 2560 {
			m = 2560
		}
		b.putString(enc[m])
		run -= m
	}
	b.putString(enc[run])
}

// pixel reports whether pixel x of a packed row (MSB first, 1 = black) is black.
func pixel(row []byte, x int) int {
	return int(row[x>>3]>>(7-uint(x&7))) & 1
}

// EncodePage codes a page as T.4 MH bits, each line taking at least
// minLineBits bits (fill bits before EOL; the receiver's minimum scan
// line time at the bit rate).
func EncodePage(p *Page, minLineBits int) []byte {
	var b bitBuf
	b.bits = make([]byte, 0, len(p.Rows)*64)
	b.putString(eolCode)
	for _, row := range p.Rows {
		start := len(b.bits)
		color, x := 0, 0
		for x < p.Width {
			run := 0
			for x+run < p.Width && pixel(row, x+run) == color {
				run++
			}
			if color == 0 {
				b.putRun(run, whiteEnc)
			} else {
				b.putRun(run, blackEnc)
			}
			x += run
			color ^= 1
		}
		for len(b.bits)-start < minLineBits {
			b.bits = append(b.bits, 0)
		}
		b.putString(eolCode)
	}
	for i := 0; i < 5; i++ { // with the last line's EOL: RTC
		b.putString(eolCode)
	}
	return b.bits
}

// pageDecoder rebuilds a page from the T.4 MH bits of a non-ECM page.
type pageDecoder struct {
	width    int
	zeros    int     // consecutive 0 bits
	synced   bool    // the first EOL was seen
	node     *mhTrie // position in the current colour's codes
	color    int
	x        int
	run      int  // make-up run waiting for its terminating code
	data     bool // the line has coded bits
	bad      bool // the line could not be decoded
	complete bool // the line reached the page width
	row      []byte
	eols     int // EOLs in a row without line data
	Rows     [][]byte
	BadRows  int
	Done     bool // RTC was seen
}

func newPageDecoder(width int) *pageDecoder {
	d := &pageDecoder{width: width}
	d.startLine()
	return d
}

func (d *pageDecoder) startLine() {
	d.node, d.color, d.x, d.run = whiteTrie, 0, 0, 0
	d.data, d.bad, d.complete = false, false, false
	d.row = make([]byte, (d.width+7)/8)
}

func (d *pageDecoder) endLine() {
	switch {
	case !d.data:
		d.eols++
		if d.eols >= 6 { // RTC
			d.Done = true
		}
		return
	case d.bad || !d.complete:
		d.BadRows++
		if n := len(d.Rows); n > 0 { // repeat the previous line, as fax machines do
			d.Rows = append(d.Rows, append([]byte(nil), d.Rows[n-1]...))
		} else {
			d.Rows = append(d.Rows, make([]byte, (d.width+7)/8))
		}
	default:
		d.Rows = append(d.Rows, d.row)
	}
	d.eols = 1
}

func (d *pageDecoder) fill(n int) {
	if d.color == 1 {
		for i := 0; i < n && d.x+i < d.width; i++ {
			x := d.x + i
			d.row[x>>3] |= 0x80 >> uint(x&7)
		}
	}
	d.x += n
}

// putBit takes the page's bits in order.
func (d *pageDecoder) putBit(bit int) {
	if d.Done {
		return
	}
	if bit == 0 {
		d.zeros++
	} else {
		if d.zeros >= 11 { // EOL
			if d.synced {
				d.endLine()
			}
			d.synced = true
			d.zeros = 0
			d.startLine()
			return
		}
		d.zeros = 0
	}
	if !d.synced || d.bad {
		return
	}
	if d.complete {
		if bit == 1 { // only fill bits may follow a complete line
			d.bad = true
		}
		return
	}
	if bit == 1 {
		d.data = true
	}
	d.node = d.node.next[bit]
	if d.node == nil { // not a code: a corrupt (or short) line
		d.bad = true
		return
	}
	if d.node.run < 0 {
		return
	}
	r := d.node.run
	d.data = true
	if r >= 64 {
		d.run += r
		d.node = d.trie()
		return
	}
	d.fill(d.run + r)
	d.run = 0
	d.color ^= 1
	d.node = d.trie()
	if d.x >= d.width {
		if d.x > d.width {
			d.bad = true
		}
		d.complete = true
	}
}

func (d *pageDecoder) trie() *mhTrie {
	if d.color == 0 {
		return whiteTrie
	}
	return blackTrie
}

// finish closes the page when the carrier ends without RTC.
func (d *pageDecoder) finish() {
	if d.synced && d.data {
		d.endLine()
	}
}
