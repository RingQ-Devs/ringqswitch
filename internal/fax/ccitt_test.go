package fax

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCCITTTIFF reads pages libtiff compressed every CCITT way.
func TestCCITTTIFF(t *testing.T) {
	if _, err := exec.LookPath("tiffcp"); err != nil {
		t.Skip("no tiffcp (apt install libtiff-tools)")
	}
	dir := t.TempDir()
	doc := testDoc()
	src := filepath.Join(dir, "src.tif")
	f, _ := os.Create(src)
	if err := WriteTIFF(f, doc); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, c := range []string{"g4", "g3:1d", "g3:2d", "g3:2d:fill", "g3:1d:fill"} {
		out := filepath.Join(dir, "out.tif")
		args := []string{"-c", c, src, out}
		if c == "g4" {
			args = []string{"-c", c, "-r", "100", src, out} // several strips
		}
		if msg, err := exec.Command("tiffcp", args...).CombinedOutput(); err != nil {
			t.Fatalf("tiffcp %s: %v %s", c, err, msg)
		}
		data, _ := os.ReadFile(out)
		got, err := readTIFF(data)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if len(got) != len(doc) {
			t.Fatalf("%s: %d pages", c, len(got))
		}
		for i := range doc {
			for y := range doc[i].Rows {
				if !bytes.Equal(got[i].Rows[y], doc[i].Rows[y]) {
					t.Fatalf("%s: page %d row %d differs", c, i+1, y)
				}
			}
		}
	}
}

// TestGhostscriptTIFFG4 reads what the RingQ fax worker hands txfax.
func TestGhostscriptTIFFG4(t *testing.T) {
	if _, err := exec.LookPath("gs"); err != nil {
		t.Skip("no ghostscript")
	}
	dir := t.TempDir()
	pdf := filepath.Join(dir, "doc.pdf")
	f, _ := os.Create(pdf)
	if err := WritePDF(f, testDoc()); err != nil {
		t.Fatal(err)
	}
	f.Close()
	tif := filepath.Join(dir, "doc.tif")
	if msg, err := exec.Command("gs", "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=tiffg4", "-r204x196",
		"-sOutputFile="+tif, pdf).CombinedOutput(); err != nil {
		t.Fatalf("gs: %v %s", err, msg)
	}
	pages, err := LoadDocument(tif)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("%d pages", len(pages))
	}
	black := 0
	for _, r := range pages[0].Rows {
		for _, b := range r {
			for ; b != 0; b &= b - 1 {
				black++
			}
		}
	}
	if black < 10000 {
		t.Fatalf("page 1 has %d black pixels", black)
	}
}
