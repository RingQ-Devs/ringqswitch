package fax

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // image formats a fax can be sent from
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Page is one fax page: PageWidth pixels wide, packed rows (MSB first,
// 1 = black).
type Page struct {
	Width  int
	XRes   int // pixels per inch across (204)
	YRes   int // lines per inch (98 standard, 196 fine)
	Rows   [][]byte
	Height int // len(Rows), kept for callers
}

const (
	resStandard = 98
	resFine     = 196
	// a4Lines is the length of an A4 page in fine lines.
	a4Lines = 2292
)

func newPage(lines, yres int) *Page {
	p := &Page{Width: PageWidth, XRes: 204, YRes: yres, Rows: make([][]byte, lines), Height: lines}
	for i := range p.Rows {
		p.Rows[i] = make([]byte, PageWidth/8)
	}
	return p
}

// Standard halves a fine page's lines (for a receiver without fine
// resolution): each pair of lines is merged (black wins).
func (p *Page) Standard() *Page {
	if p.YRes != resFine {
		return p
	}
	q := &Page{Width: p.Width, XRes: p.XRes, YRes: resStandard}
	for i := 0; i < len(p.Rows); i += 2 {
		row := append([]byte(nil), p.Rows[i]...)
		if i+1 < len(p.Rows) {
			for j := range row {
				row[j] |= p.Rows[i+1][j]
			}
		}
		q.Rows = append(q.Rows, row)
	}
	q.Height = len(q.Rows)
	return q
}

// ---- reading documents to send

// ErrNoGhostscript: PDF needs the PDF converter (gs) on the server.
var ErrNoGhostscript = errors.New("sending a PDF needs the PDF converter: apt install ghostscript")

// LoadDocument reads a file to send: PDF (rendered with the PDF converter, gs),
// PBM, TIFF (as written by ReceiveFile: uncompressed or PackBits), or an
// image (PNG, JPEG, GIF). Pages come out fine (204 x 196).
func LoadDocument(path string) ([]*Page, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch {
	case bytes.HasPrefix(data, []byte("%PDF")) || bytes.HasPrefix(data, []byte("%!PS")):
		return renderPDF(path)
	case bytes.HasPrefix(data, []byte("P4")):
		p, _, err := readPBM(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return nil, err
		}
		return splitPage(p), nil
	case bytes.HasPrefix(data, []byte("II*\x00")) || bytes.HasPrefix(data, []byte("MM\x00*")):
		return readTIFF(data)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: not a PDF, TIFF or image the fax can send", filepath.Base(path))
	}
	return splitPage(fromImage(img)), nil
}

// renderPDF draws each PDF page onto a fax page with the PDF converter (gs).
func renderPDF(path string) ([]*Page, error) {
	gs, err := exec.LookPath("gs")
	if err != nil {
		return nil, ErrNoGhostscript
	}
	dir, err := os.MkdirTemp("", "rqfax")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "p%04d.pbm")
	cmd := exec.Command(gs, "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=pbmraw", "-r204x196",
		fmt.Sprintf("-g%dx%d", PageWidth, a4Lines), "-dFIXEDMEDIA", "-dPDFFitPage", "-sOutputFile="+out, path)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("the PDF converter could not read the document: %s", strings.TrimSpace(string(msg)))
	}
	files, _ := filepath.Glob(filepath.Join(dir, "p*.pbm"))
	sort.Strings(files)
	var pages []*Page
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		p, _, err := readPBM(bufio.NewReader(fh))
		fh.Close()
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	if len(pages) == 0 {
		return nil, errors.New("the document has no pages")
	}
	return pages, nil
}

// readPBM reads a raw PBM (P4) image as a fine page, centred or cut to the
// fax width.
func readPBM(r *bufio.Reader) (*Page, bool, error) {
	var magic string
	var w, h int
	if _, err := fmt.Fscan(r, &magic); err != nil || magic != "P4" {
		return nil, false, errors.New("not a PBM image")
	}
	nums := []*int{&w, &h}
	for i := 0; i < 2; {
		c, err := r.ReadByte()
		if err != nil {
			return nil, false, err
		}
		switch {
		case c == '#':
			_, _ = r.ReadString('\n')
		case c >= '0' && c <= '9':
			_ = r.UnreadByte()
			if _, err := fmt.Fscan(r, nums[i]); err != nil {
				return nil, false, err
			}
			i++
		}
	}
	if _, err := r.ReadByte(); err != nil { // the single whitespace before the data
		return nil, false, err
	}
	if w <= 0 || h <= 0 || w > 20000 || h > 40000 {
		return nil, false, errors.New("bad PBM size")
	}
	stride := (w + 7) / 8
	p := newPage(h, resFine)
	src := make([]byte, stride)
	off := 0
	if w < PageWidth {
		off = (PageWidth - w) / 2
	}
	for y := 0; y < h; y++ {
		if _, err := io.ReadFull(r, src); err != nil {
			return nil, false, err
		}
		for x := 0; x < w && x+off < PageWidth; x++ {
			if src[x>>3]&(0x80>>uint(x&7)) != 0 {
				X := x + off
				p.Rows[y][X>>3] |= 0x80 >> uint(X&7)
			}
		}
	}
	return p, true, nil
}

// fromImage scales an image to the fax width and dithers it to black and white.
func fromImage(img image.Image) *Page {
	b := img.Bounds()
	scale := float64(PageWidth) / float64(b.Dx())
	h := int(float64(b.Dy()) * scale * 196 / 204)
	if h < 1 {
		h = 1
	}
	gray := make([]float64, PageWidth*h)
	for y := 0; y < h; y++ {
		sy := b.Min.Y + int(float64(y)*204/196/scale)
		if sy >= b.Max.Y {
			sy = b.Max.Y - 1
		}
		for x := 0; x < PageWidth; x++ {
			sx := b.Min.X + int(float64(x)/scale)
			if sx >= b.Max.X {
				sx = b.Max.X - 1
			}
			r, g, bl, a := img.At(sx, sy).RGBA()
			l := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 65535
			l = l*float64(a)/65535 + (1 - float64(a)/65535) // transparent = white
			gray[y*PageWidth+x] = l
		}
	}
	p := newPage(h, resFine)
	for y := 0; y < h; y++ { // Floyd-Steinberg
		for x := 0; x < PageWidth; x++ {
			i := y*PageWidth + x
			v := 0.0
			if gray[i] >= 0.5 {
				v = 1
			} else {
				p.Rows[y][x>>3] |= 0x80 >> uint(x&7)
			}
			e := gray[i] - v
			if x+1 < PageWidth {
				gray[i+1] += e * 7 / 16
			}
			if y+1 < h {
				if x > 0 {
					gray[i+PageWidth-1] += e * 3 / 16
				}
				gray[i+PageWidth] += e * 5 / 16
				if x+1 < PageWidth {
					gray[i+PageWidth+1] += e * 1 / 16
				}
			}
		}
	}
	return p
}

// splitPage cuts a long page into A4 lengths.
func splitPage(p *Page) []*Page {
	var out []*Page
	for start := 0; start < len(p.Rows); start += a4Lines {
		end := start + a4Lines
		if end > len(p.Rows) {
			end = len(p.Rows)
		}
		q := &Page{Width: p.Width, XRes: p.XRes, YRes: p.YRes, Rows: p.Rows[start:end]}
		q.Height = len(q.Rows)
		out = append(out, q)
	}
	return out
}

// ---- the header line ("2026-10-09 14:05  ACME  +6561234567  P.1/3")

// Header draws text over the top of the page (its top margin), in a
// 5 x 7 font at three times the size.
func (p *Page) Header(text string) {
	scaleX, scaleY := 3, 3
	if p.YRes == resStandard {
		scaleY = 2
	}
	top := 6 * scaleY / 3
	rows := 7*scaleY + 2*top
	for y := 0; y < rows && y < len(p.Rows); y++ {
		for i := range p.Rows[y] {
			p.Rows[y][i] = 0
		}
	}
	x0 := 40
	for _, ch := range strings.ToUpper(text) {
		g, ok := font5x7[ch]
		if !ok {
			g = font5x7['?']
		}
		for gy := 0; gy < 7; gy++ {
			for gx := 0; gx < 5; gx++ {
				if g[gy]&(0x10>>uint(gx)) == 0 {
					continue
				}
				for sy := 0; sy < scaleY; sy++ {
					y := top + gy*scaleY + sy
					if y >= len(p.Rows) {
						continue
					}
					for sx := 0; sx < scaleX; sx++ {
						x := x0 + gx*scaleX + sx
						if x < PageWidth {
							p.Rows[y][x>>3] |= 0x80 >> uint(x&7)
						}
					}
				}
			}
		}
		x0 += 6 * scaleX
		if x0+5*scaleX >= PageWidth {
			break
		}
	}
}

// ---- writing received pages

// WritePDF writes pages as a PDF (one black-and-white image per page).
func WritePDF(w io.Writer, pages []*Page) error {
	var buf bytes.Buffer
	var offsets []int
	obj := func(body string, stream []byte) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s", len(offsets), body)
		if stream != nil {
			buf.WriteString("\nstream\n")
			buf.Write(stream)
			buf.WriteString("\nendstream")
		}
		buf.WriteString("\nendobj\n")
	}
	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	n := len(pages)
	// 1 catalog, 2 pages, then per page: page, image, contents
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+i*3)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>", nil)
	obj(fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", n, strings.Join(kids, " ")), nil)
	for i, p := range pages {
		xres, yres := float64(p.XRes), float64(p.YRes)
		if xres == 0 {
			xres = 204
		}
		if yres == 0 {
			yres = resFine
		}
		wPt := float64(p.Width) / xres * 72
		hPt := float64(len(p.Rows)) / yres * 72
		var raw bytes.Buffer
		for _, row := range p.Rows {
			raw.Write(row)
		}
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		_, _ = zw.Write(raw.Bytes())
		_ = zw.Close()
		content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", wPt, hPt)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			wPt, hPt, 4+i*3, 5+i*3), nil)
		obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 1 /Decode [1 0] /Filter /FlateDecode /Length %d >>",
			p.Width, len(p.Rows), z.Len()), z.Bytes())
		obj(fmt.Sprintf("<< /Length %d >>", len(content)), []byte(content))
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	_, err := w.Write(buf.Bytes())
	return err
}

// WriteTIFF writes pages as a multi-page bilevel TIFF (PackBits), which
// tiff2pdf and fax tools read.
func WriteTIFF(w io.Writer, pages []*Page) error {
	var buf bytes.Buffer
	le := binary.LittleEndian
	buf.WriteString("II*\x00")
	_ = binary.Write(&buf, le, uint32(0)) // first IFD, patched below
	prevNext := 4
	for _, p := range pages {
		var data bytes.Buffer
		for _, row := range p.Rows {
			data.Write(packBits(row))
		}
		dataOff := buf.Len()
		buf.Write(data.Bytes())
		if buf.Len()%2 == 1 {
			buf.WriteByte(0)
		}
		xresOff := buf.Len()
		_ = binary.Write(&buf, le, [4]uint32{uint32(p.XRes), 1, uint32(p.YRes), 1})
		ifd := buf.Len()
		le.PutUint32(buf.Bytes()[prevNext:], uint32(ifd))
		type ent struct {
			tag, typ uint16
			n, v     uint32
		}
		ents := []ent{
			{254, 4, 1, 2},                   // NewSubfileType: page of a multi-page document
			{256, 4, 1, uint32(p.Width)},     // ImageWidth
			{257, 4, 1, uint32(len(p.Rows))}, // ImageLength
			{258, 3, 1, 1},                   // BitsPerSample
			{259, 3, 1, 32773},               // Compression: PackBits
			{262, 3, 1, 0},                   // Photometric: 0 = white
			{266, 3, 1, 1},                   // FillOrder
			{273, 4, 1, uint32(dataOff)},     // StripOffsets
			{277, 3, 1, 1},                   // SamplesPerPixel
			{278, 4, 1, uint32(len(p.Rows))}, // RowsPerStrip
			{279, 4, 1, uint32(data.Len())},  // StripByteCounts
			{282, 5, 1, uint32(xresOff)},     // XResolution
			{283, 5, 1, uint32(xresOff + 8)}, // YResolution
			{296, 3, 1, 2},                   // ResolutionUnit: inch
		}
		_ = binary.Write(&buf, le, uint16(len(ents)))
		for _, e := range ents {
			_ = binary.Write(&buf, le, e.tag)
			_ = binary.Write(&buf, le, e.typ)
			_ = binary.Write(&buf, le, e.n)
			if e.typ == 3 {
				_ = binary.Write(&buf, le, uint16(e.v))
				_ = binary.Write(&buf, le, uint16(0))
			} else {
				_ = binary.Write(&buf, le, e.v)
			}
		}
		prevNext = buf.Len()
		_ = binary.Write(&buf, le, uint32(0))
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func packBits(row []byte) []byte {
	var out []byte
	for i := 0; i < len(row); {
		j := i + 1
		for j < len(row) && j-i < 128 && row[j] == row[i] {
			j++
		}
		if j-i >= 2 {
			out = append(out, byte(1-(j-i)), row[i])
			i = j
			continue
		}
		j = i + 1
		for j < len(row) && j-i < 128 && (j+1 >= len(row) || row[j] != row[j+1]) {
			j++
		}
		out = append(out, byte(j-i-1))
		out = append(out, row[i:j]...)
		i = j
	}
	return out
}

// readTIFF reads the bilevel TIFFs WriteTIFF writes (uncompressed or
// PackBits; fill order 1).
func readTIFF(data []byte) ([]*Page, error) {
	var bo binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		bo = binary.BigEndian
	}
	off := int(bo.Uint32(data[4:]))
	var pages []*Page
	for off > 0 && off+2 <= len(data) && len(pages) < 500 {
		n := int(bo.Uint16(data[off:]))
		tags := map[uint16][]uint32{}
		for i := 0; i < n; i++ {
			e := off + 2 + i*12
			if e+12 > len(data) {
				return nil, errors.New("broken TIFF")
			}
			tag, typ, cnt := bo.Uint16(data[e:]), bo.Uint16(data[e+2:]), int(bo.Uint32(data[e+4:]))
			var vals []uint32
			size := map[uint16]int{3: 2, 4: 4, 5: 8}[typ]
			if size == 0 {
				continue
			}
			p := e + 8
			if size*cnt > 4 {
				p = int(bo.Uint32(data[e+8:]))
			}
			for k := 0; k < cnt && p+size <= len(data); k++ {
				switch typ {
				case 3:
					vals = append(vals, uint32(bo.Uint16(data[p:])))
				case 4:
					vals = append(vals, bo.Uint32(data[p:]))
				case 5:
					vals = append(vals, bo.Uint32(data[p:])/max(bo.Uint32(data[p+4:]), 1))
				}
				p += size
			}
			tags[tag] = vals
		}
		get := func(t uint16, def uint32) uint32 {
			if v := tags[t]; len(v) > 0 {
				return v[0]
			}
			return def
		}
		w, h := int(get(256, 0)), int(get(257, 0))
		comp, photo := get(259, 1), get(262, 0)
		if get(258, 1) != 1 || w <= 0 || h <= 0 || w > 20000 || h > 40000 {
			return nil, errors.New("only black-and-white TIFF pages can be sent")
		}
		ccitt := comp >= 2 && comp <= 4
		if comp != 1 && comp != 32773 && !ccitt {
			return nil, fmt.Errorf("TIFF compression %d is not supported (send a PDF instead)", comp)
		}
		lsbFirst := get(266, 1) == 2 // FillOrder: spandsp writes its pages this way
		stride := (w + 7) / 8
		perStrip := int(get(278, uint32(h)))
		if perStrip <= 0 || perStrip > h {
			perStrip = h
		}
		var raw []byte
		offs, cnts := tags[273], tags[279]
		for i := range offs {
			if i >= len(cnts) || int(offs[i])+int(cnts[i]) > len(data) {
				return nil, errors.New("broken TIFF strip")
			}
			s := data[offs[i] : offs[i]+cnts[i]]
			switch {
			case comp == 32773:
				s = unpackBits(s)
			case ccitt:
				if lsbFirst {
					s = reverseBits(s)
				}
				rows := min(perStrip, h-i*perStrip)
				dec, err := decodeCCITT(s, w, rows, comp, get(292, 0))
				if err != nil {
					return nil, err
				}
				s = nil
				for _, row := range dec {
					s = append(s, row...)
				}
				for k := len(dec); k < rows; k++ { // a short strip: white
					s = append(s, make([]byte, stride)...)
				}
			}
			raw = append(raw, s...)
		}
		if ccitt {
			lsbFirst = false
		}
		if len(raw) < stride*h {
			return nil, errors.New("short TIFF image")
		}
		yres := int(get(283, resFine))
		if get(296, 2) == 3 { // per centimetre
			yres = yres * 254 / 100
		}
		if yres < 150 {
			yres = resStandard
		} else {
			yres = resFine
		}
		p := newPage(h, yres)
		for y := 0; y < h; y++ {
			src := raw[y*stride : (y+1)*stride]
			for x := 0; x < w && x < PageWidth; x++ {
				mask := byte(0x80 >> uint(x&7))
				if lsbFirst {
					mask = 1 << uint(x&7)
				}
				black := src[x>>3]&mask != 0
				if photo == 1 {
					black = !black
				}
				if black {
					p.Rows[y][x>>3] |= 0x80 >> uint(x&7)
				}
			}
		}
		pages = append(pages, p)
		next := off + 2 + n*12
		if next+4 > len(data) {
			break
		}
		off = int(bo.Uint32(data[next:]))
	}
	if len(pages) == 0 {
		return nil, errors.New("the TIFF has no pages")
	}
	return pages, nil
}

func unpackBits(s []byte) []byte {
	var out []byte
	for i := 0; i < len(s); {
		n := int(int8(s[i]))
		i++
		switch {
		case n >= 0:
			if i+n+1 > len(s) {
				return out
			}
			out = append(out, s[i:i+n+1]...)
			i += n + 1
		case n != -128:
			if i >= len(s) {
				return out
			}
			for k := 0; k < 1-n; k++ {
				out = append(out, s[i])
			}
			i++
		}
	}
	return out
}

func reverseBits(s []byte) []byte {
	out := make([]byte, len(s))
	for i, b := range s {
		var r byte
		for k := 0; k < 8; k++ {
			r = r<<1 | (b>>uint(k))&1
		}
		out[i] = r
	}
	return out
}
