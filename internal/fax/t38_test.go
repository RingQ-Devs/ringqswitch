package fax

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIFPCoding(t *testing.T) {
	if b := encodeIndicator(t38CED); !bytes.Equal(b, []byte{0x04}) {
		t.Fatalf("CED indicator % x", b)
	}
	if b := encodeIndicator(t38V29_9600); !bytes.Equal(b, []byte{0x0E}) {
		t.Fatalf("v29-9600 indicator % x", b)
	}
	// V.21 HDLC data 0xFF 0x13 (bit reversed on the wire), version 0:
	// C0 (data, type v21), 01 (one field), 80 (data present, hdlc-data),
	// 00 01 (length 2), the octets.
	b := encodeData(0, t38DataV21, []t38Field{{t38HDLCData, []byte{0xFF, 0xC8}}})
	if want := []byte{0xC0, 0x01, 0x80, 0x00, 0x01, 0xFF, 0xC8}; !bytes.Equal(b, want) {
		t.Fatalf("hdlc data % x, want % x", b, want)
	}
	// hdlc-fcs-OK-sig-end without data: version 0 puts the type in bits 6-4,
	// later versions in bits 5-3 (after the extension bit).
	if b := encodeData(0, t38DataV21, []t38Field{{t38HDLCFCSOKSigEnd, nil}}); !bytes.Equal(b, []byte{0xC0, 0x01, 0x40}) {
		t.Fatalf("v0 fcs-ok-sig-end % x", b)
	}
	if b := encodeData(2, t38DataV21, []t38Field{{t38HDLCFCSOKSigEnd, nil}}); !bytes.Equal(b, []byte{0xC0, 0x01, 0x20}) {
		t.Fatalf("v2 fcs-ok-sig-end % x", b)
	}
	for _, v := range []int{0, 2} {
		in := []t38Field{{t38T4Data, bytes.Repeat([]byte{0xA5}, 300)}, {t38T4SigEnd, nil}}
		p, err := decodeIFP(v, encodeData(v, t38DataV29_9600, in))
		if err != nil || p.indicator != -1 || p.dataType != t38DataV29_9600 || len(p.fields) != 2 ||
			p.fields[0].typ != t38T4Data || !bytes.Equal(p.fields[0].data, in[0].data) || p.fields[1].typ != t38T4SigEnd {
			t.Fatalf("version %d: %+v %v", v, p, err)
		}
	}
	if p, err := decodeIFP(0, []byte{0x06}); err != nil || p.indicator != t38V21Preamble {
		t.Fatalf("preamble: %+v %v", p, err)
	}
}

func TestUDPTLRedundancy(t *testing.T) {
	tx, rx := NewUDPTL(3, 0), NewUDPTL(3, 0)
	var got []byte
	for i := 0; i < 40; i++ {
		d := tx.Packet([]byte{byte(i)})
		if i%5 == 2 || i%5 == 3 { // two in a row lost
			continue
		}
		ifps, err := rx.Receive(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ifps {
			got = append(got, p[0])
		}
		if again, _ := rx.Receive(d); len(again) != 0 { // a duplicate
			t.Fatalf("duplicate delivered %v", again)
		}
	}
	for i := range got {
		if got[i] != byte(i) {
			t.Fatalf("got %v", got)
		}
	}
	if len(got) != 40 || rx.Lost != 0 || rx.Recover != 16 {
		t.Fatalf("%d packets, lost %d, recovered %d", len(got), rx.Lost, rx.Recover)
	}
	// A small T38FaxMaxDatagram leaves out what does not fit.
	small := NewUDPTL(3, 20)
	for i := 0; i < 5; i++ {
		if d := small.Packet(bytes.Repeat([]byte{1}, 8)); len(d) > 20 {
			t.Fatalf("datagram of %d bytes", len(d))
		}
	}
}

// runT38 connects two T.38 terminals through UDPTL, losing every
// lossEvery-th datagram (0: none), with 40 ms of latency.
func runT38(t *testing.T, a, b *Terminal, lossEvery, maxSec int) {
	t.Helper()
	ua, ub := NewUDPTL(3, 0), NewUDPTL(3, 0)
	const delay = 2
	var qab, qba [][][]byte
	n := 0
	out := make([]int16, 160)
	for step := 0; step < maxSec*50; step++ {
		a.Transmit(out)
		b.Transmit(out)
		var da, db [][]byte
		for _, p := range a.TakeIFP() {
			da = append(da, ua.Packet(p))
		}
		for _, p := range b.TakeIFP() {
			db = append(db, ub.Packet(p))
		}
		qab, qba = append(qab, da), append(qba, db)
		deliver := func(q *[][][]byte, u *UDPTL, to *Terminal) {
			if len(*q) <= delay {
				return
			}
			for _, d := range (*q)[0] {
				if n++; lossEvery > 0 && n%lossEvery == 0 {
					continue
				}
				ifps, err := u.Receive(d)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range ifps {
					to.ReceiveIFP(p)
				}
			}
			*q = (*q)[1:]
		}
		deliver(&qab, ub, b)
		deliver(&qba, ua, a)
		if a.Done() && b.Done() {
			return
		}
	}
	t.Fatalf("the call ran over %d s: sender done %v, receiver done %v", maxSec, a.Done(), b.Done())
}

func TestT38GoToGo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		loss    int
		version int
	}{{"clean", 0, 0}, {"loss", 7, 0}, {"version 2", 0, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			log := func(f string, a ...any) { t.Logf(f, a...) }
			tx := NewSender(testDoc(), Options{Ident: "+6561234567", Header: "RINGQ TEST", Now: fixedNow, T38: true,
				T38Version: tc.version, Log: func(f string, a ...any) { log("TX "+f, a...) }})
			rx := NewReceiver(Options{Ident: "+6567654321", T38: true, T38Version: tc.version,
				Log: func(f string, a ...any) { log("RX "+f, a...) }})
			runT38(t, tx, rx, tc.loss, 240)
			tr, rr := tx.Result(), rx.Result()
			if !tr.Success || !rr.Success || tr.Pages != 2 || rr.Pages != 2 {
				t.Fatalf("sender %s (%d pages), receiver %s (%d pages)", tr.Text, tr.Pages, rr.Text, rr.Pages)
			}
			if rr.RemoteID != "+6561234567" || tr.RemoteID != "+6567654321" {
				t.Fatalf("idents: %q %q", rr.RemoteID, tr.RemoteID)
			}
			checkPages(t, tx, rr.Received)
			t.Logf("sender stats %+v, receiver stats %+v", tx.Stats(), rx.Stats())
		})
	}
}

// runT38Peer runs a T.38 call between our terminal and spandsp's T.38
// terminal (faxpeer t38send / t38receive), datagrams in 20 ms steps.
func runT38Peer(t *testing.T, peer string, env, args []string, term *Terminal, maxSec int) peerResult {
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
	u := NewUDPTL(3, 0)
	out := make([]int16, 160)
	var lb [2]byte
	for step := 0; step < maxSec*50; step++ {
		term.Transmit(out)
		var w bytes.Buffer
		for _, p := range term.TakeIFP() {
			d := u.Packet(p)
			binary.Write(&w, binary.LittleEndian, uint16(len(d)))
			w.Write(d)
		}
		w.Write([]byte{0, 0})
		if _, err := stdin.Write(w.Bytes()); err != nil {
			break
		}
		failed := false
		for {
			if _, err := io.ReadFull(r, lb[:]); err != nil {
				failed = true
				break
			}
			n := int(binary.LittleEndian.Uint16(lb[:]))
			if n == 0 {
				break
			}
			d := make([]byte, n)
			if _, err := io.ReadFull(r, d); err != nil {
				failed = true
				break
			}
			ifps, err := u.Receive(d)
			if err != nil {
				t.Fatalf("bad datagram from spandsp: %v % x", err, d)
			}
			for _, p := range ifps {
				term.ReceiveIFP(p)
			}
		}
		if failed || (term.Done() && strings.Contains(stderr.String(), "RESULT")) {
			break
		}
	}
	stdin.Close()
	cmd.Wait()
	res := peerResult{stderr: stderr.String()}
	if m := reResult.FindStringSubmatch(res.stderr); m != nil {
		res.finishedPeer = true
		res.code, res.text = atoi(m[1]), m[2]
		res.pages, res.rate, res.badRows = atoi(m[3]), atoi(m[4]), atoi(m[5])
	}
	return res
}

func TestT38SpandspReceives(t *testing.T) {
	peer := buildPeer(t)
	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"version 0", nil},
		{"version 0 with loss", []string{"FAXPEER_DROP=9"}},
		{"v17 and ECM offered", []string{"FAXPEER_MODEMS=7", "FAXPEER_ECM=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "rx.tif")
			tx := NewSender(testDoc(), Options{Ident: "+6561234567", Header: "RINGQ TEST", Now: fixedNow, T38: true,
				Log: func(f string, a ...any) { t.Logf(f, a...) }})
			res := runT38Peer(t, peer, append([]string{"FAXPEER_IDENT=+6567654321"}, tc.env...), []string{"t38receive", out}, tx, 300)
			tr := tx.Result()
			t.Logf("ours: %s, %d pages at %d; spandsp: %d %s pages=%d rate=%d bad_rows=%d",
				tr.Text, tr.Pages, tr.Rate, res.code, res.text, res.pages, res.rate, res.badRows)
			if !tr.Success || tr.Pages != 2 || res.code != 0 || res.pages != 2 || res.badRows != 0 {
				t.Fatalf("spandsp log:\n%s", tail(res.stderr, 60))
			}
			if tr.RemoteID != "+6567654321" {
				t.Fatalf("remote ident %q", tr.RemoteID)
			}
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

func TestT38SpandspSends(t *testing.T) {
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
		name    string
		env     []string
		version int
	}{
		{"version 0", nil, 0},
		{"version 0 with loss", []string{"FAXPEER_DROP=9"}, 0},
		{"version 2", []string{"FAXPEER_T38_VERSION=2"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rx := NewReceiver(Options{Ident: "+6567654321", T38: true, T38Version: tc.version,
				Log: func(f string, a ...any) { t.Logf(f, a...) }})
			res := runT38Peer(t, peer, append([]string{"FAXPEER_IDENT=+6561234567"}, tc.env...), []string{"t38send", in}, rx, 300)
			rr := rx.Result()
			t.Logf("ours: %s, %d pages at %d, remote %q; spandsp: %d %s pages=%d rate=%d",
				rr.Text, rr.Pages, rr.Rate, rr.RemoteID, res.code, res.text, res.pages, res.rate)
			if !rr.Success || rr.Pages != 2 || res.code != 0 || res.pages != 2 {
				t.Fatalf("spandsp log:\n%s", tail(res.stderr, 60))
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
