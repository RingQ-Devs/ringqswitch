package fax

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests run a call against an independent fax implementation (spandsp),
// through testdata/faxpeer.c. They are skipped where spandsp's headers or
// a C compiler are missing (apt install libspandsp-dev).

func buildPeer(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "faxpeer")
	cmd := exec.Command("cc", "-O2", "-o", out, "testdata/faxpeer.c", "-lspandsp")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("no spandsp peer (%v): %s", err, firstLine(string(msg)))
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type peerResult struct {
	code         int
	text         string
	pages, rate  int
	badRows      int
	stderr       string
	finishedPeer bool
}

var reResult = regexp.MustCompile(`RESULT (\d+) (.*) pages=(\d+) rate=(\d+) bad_rows=(\d+)`)

// runPeer runs a call between our terminal and faxpeer.
func runPeer(t *testing.T, peer string, env []string, args []string, term *Terminal, ab, ba *channel, maxSec int) peerResult {
	t.Helper()
	cmd := exec.Command(peer, args...)
	cmd.Env = append(os.Environ(), env...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	pa, pb := &pipe{ch: ab, fifo: make([]int16, 80)}, &pipe{ch: ba, fifo: make([]int16, 80)}
	in, out := make([]int16, 160), make([]int16, 160)
	peerIn := make([]int16, 160)
	peerOut := make([]int16, 160)
	buf := make([]byte, 320)
	for step := 0; step < maxSec*50; step++ {
		term.Process(in, out)
		peerIn = pa.carry(out)
		for i, v := range peerIn {
			binary.LittleEndian.PutUint16(buf[2*i:], uint16(v))
		}
		if _, err := stdin.Write(buf); err != nil {
			break
		}
		if _, err := io.ReadFull(r, buf); err != nil {
			break
		}
		for i := range peerOut {
			peerOut[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
		}
		in = pb.carry(peerOut)
		if term.Done() && strings.Contains(stderr.String(), "RESULT") {
			break
		}
	}
	stdin.Close()
	cmd.Wait()
	res := peerResult{stderr: stderr.String()}
	if m := reResult.FindStringSubmatch(res.stderr); m != nil {
		res.finishedPeer = true
		res.code = atoi(m[1])
		res.text = m[2]
		res.pages = atoi(m[3])
		res.rate = atoi(m[4])
		res.badRows = atoi(m[5])
	}
	return res
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// lineChannels is a long call: noise at -55 dBm0 (spandsp's own carrier
// detect sits at -48), 1 Hz carrier offset, 30 ppm clock drift.
func lineChannels() (*channel, *channel) {
	n := dbm0ToAmplitude(-55) / math.Sqrt2
	return newChannel(n, 1, 3e-5), newChannel(n, -1, -3e-5)
}

func TestSpandspReceives(t *testing.T) {
	peer := buildPeer(t)
	for _, tc := range []struct {
		name   string
		modems string
		line   bool
	}{
		{"v29 clean", "2", false},
		{"v27ter+v29 line", "3", true},
		{"v17 and ECM offered", "7", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "rx.tif")
			tx := NewSender(testDoc(), Options{Ident: "+6561234567", Header: "RINGQ TEST", Now: fixedNow,
				Log: func(f string, a ...any) { t.Logf(f, a...) }})
			ab, ba := newChannel(0, 0, 0), newChannel(0, 0, 0)
			if tc.line {
				ab, ba = lineChannels()
			}
			env := []string{"FAXPEER_MODEMS=" + tc.modems, "FAXPEER_IDENT=+6567654321"}
			if tc.modems == "7" {
				env = append(env, "FAXPEER_ECM=1")
			}
			res := runPeer(t, peer, env,
				[]string{"receive", out}, tx, ab, ba, 300)
			tr := tx.Result()
			t.Logf("ours: %s, %d pages at %d; spandsp: %d %s pages=%d rate=%d bad_rows=%d",
				tr.Text, tr.Pages, tr.Rate, res.code, res.text, res.pages, res.rate, res.badRows)
			if !tr.Success || tr.Pages != 2 || res.code != 0 || res.pages != 2 || res.badRows != 0 {
				t.Fatalf("spandsp log:\n%s", tail(res.stderr, 40))
			}
			if tr.RemoteID != "+6567654321" {
				t.Fatalf("remote ident %q", tr.RemoteID)
			}
			// Compare what spandsp stored with what we sent.
			if _, err := exec.LookPath("tiffcp"); err != nil {
				return
			}
			raw := filepath.Join(t.TempDir(), "raw.tif")
			if msg, err := exec.Command("tiffcp", "-c", "none", out, raw).CombinedOutput(); err != nil {
				t.Fatalf("tiffcp: %v %s", err, msg)
			}
			data, err := os.ReadFile(raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := readTIFF(data)
			if err != nil {
				t.Fatal(err)
			}
			checkPages(t, tx, got)
		})
	}
}

func TestSpandspSends(t *testing.T) {
	peer := buildPeer(t)
	doc := testDoc()
	in := filepath.Join(t.TempDir(), "tx.tif")
	f, err := os.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTIFF(f, doc); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, tc := range []struct {
		name   string
		modems string
		line   bool
	}{
		{"v29 clean", "2", false},
		{"v27ter+v29 line", "3", true},
		{"v17 and ECM offered", "7", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rx := NewReceiver(Options{Ident: "+6567654321", Log: func(f string, a ...any) { t.Logf(f, a...) }})
			ab, ba := newChannel(0, 0, 0), newChannel(0, 0, 0)
			if tc.line {
				ab, ba = lineChannels()
			}
			env := []string{"FAXPEER_MODEMS=" + tc.modems, "FAXPEER_IDENT=+6561234567"}
			if tc.modems == "7" {
				env = append(env, "FAXPEER_ECM=1")
			}
			res := runPeer(t, peer, env,
				[]string{"send", in}, rx, ab, ba, 300)
			rr := rx.Result()
			t.Logf("ours: %s, %d pages at %d, remote %q; spandsp: %d %s pages=%d rate=%d",
				rr.Text, rr.Pages, rr.Rate, rr.RemoteID, res.code, res.text, res.pages, res.rate)
			if !rr.Success || rr.Pages != 2 || res.code != 0 || res.pages != 2 {
				t.Fatalf("spandsp log:\n%s", tail(res.stderr, 40))
			}
			if rr.RemoteID != "+6561234567" {
				t.Fatalf("remote ident %q", rr.RemoteID)
			}
			for i, p := range doc {
				g := rr.Received[i]
				if len(g.Rows) != len(p.Rows) {
					t.Fatalf("page %d: %d rows, want %d", i+1, len(g.Rows), len(p.Rows))
				}
				for y := range p.Rows {
					if !bytes.Equal(g.Rows[y], p.Rows[y]) {
						t.Fatalf("page %d row %d differs", i+1, y)
					}
				}
			}
		})
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
