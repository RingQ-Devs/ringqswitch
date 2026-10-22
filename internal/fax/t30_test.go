package fax

import (
	"bytes"
	"math"
	"testing"
	"time"
)

// pipe carries one direction of a call through a channel, in 160 sample
// blocks (a clock drift makes the channel's output length vary).
type pipe struct {
	ch   *channel
	fifo []int16
}

func (p *pipe) carry(in []int16) []int16 {
	x := make([]float64, len(in))
	for i, v := range in {
		x[i] = float64(v)
	}
	for _, v := range p.ch.process(x) {
		p.fifo = append(p.fifo, clip16(v))
	}
	out := make([]int16, len(in))
	n := copy(out, p.fifo)
	p.fifo = p.fifo[n:]
	return out
}

func fixedNow() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }

func testDoc() []*Page {
	p2 := testPage(600)
	return []*Page{testPage(a4Lines), p2}
}

// runCall connects two terminals until both finish (or maxSec of call time).
func runCall(t *testing.T, a, b *Terminal, ab, ba *channel, maxSec int) {
	t.Helper()
	// 10 ms of latency each way, so a clock drift never runs the pipe dry.
	pa, pb := &pipe{ch: ab, fifo: make([]int16, 80)}, &pipe{ch: ba, fifo: make([]int16, 80)}
	inA, inB := make([]int16, 160), make([]int16, 160)
	outA, outB := make([]int16, 160), make([]int16, 160)
	for step := 0; step < maxSec*50; step++ {
		a.Process(inA, outA)
		b.Process(inB, outB)
		if a.Done() && b.Done() {
			return
		}
		inB = pa.carry(outA)
		inA = pb.carry(outB)
	}
	t.Fatalf("the call ran over %d s: sender done %v, receiver done %v", maxSec, a.Done(), b.Done())
}

func checkPages(t *testing.T, sender *Terminal, got []*Page) {
	t.Helper()
	if len(got) != len(sender.pages) {
		t.Fatalf("received %d pages, want %d", len(got), len(sender.pages))
	}
	for i := range sender.pages {
		sender.page = i
		want := sender.pageImage()
		if len(got[i].Rows) != len(want.Rows) {
			t.Fatalf("page %d: %d rows, want %d", i+1, len(got[i].Rows), len(want.Rows))
		}
		bad := 0
		for y := range want.Rows {
			if !bytes.Equal(got[i].Rows[y], want.Rows[y]) {
				bad++
			}
		}
		if bad > 0 {
			t.Fatalf("page %d: %d rows differ", i+1, bad)
		}
	}
}

func TestT30GoToGo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ab, ba *channel
	}{
		{"clean", newChannel(0, 0, 0), newChannel(0, 0, 0)},
		{"noise", newChannel(dbm0ToAmplitude(-13)/math.Sqrt2/math.Pow(10, 30.0/20), 0, 0), newChannel(dbm0ToAmplitude(-13)/math.Sqrt2/math.Pow(10, 30.0/20), 0, 0)},
		{"freq", newChannel(0, 1, 0), newChannel(0, -1, 0)},
		{"drift", newChannel(0, 0, 4e-5), newChannel(0, 0, -4e-5)},
		{"line", newChannel(dbm0ToAmplitude(-13)/math.Sqrt2/math.Pow(10, 30.0/20), 1, 4e-5),
			newChannel(dbm0ToAmplitude(-13)/math.Sqrt2/math.Pow(10, 30.0/20), -1, -4e-5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := func(f string, a ...any) { t.Logf(f, a...) }
			tx := NewSender(testDoc(), Options{Ident: "+6561234567", Header: "RINGQ TEST", Now: fixedNow,
				Log: func(f string, a ...any) { log("TX "+f, a...) }})
			rx := NewReceiver(Options{Ident: "+6567654321", Log: func(f string, a ...any) { log("RX "+f, a...) }})
			runCall(t, tx, rx, tc.ab, tc.ba, 240)
			tr, rr := tx.Result(), rx.Result()
			t.Logf("sender: %+v", tr)
			t.Logf("receiver: %s, %d pages, remote %q, rate %d", rr.Text, rr.Pages, rr.RemoteID, rr.Rate)
			if !tr.Success || !rr.Success || tr.Pages != 2 || rr.Pages != 2 {
				t.Fatalf("sender %s (%d pages), receiver %s (%d pages)", tr.Text, tr.Pages, rr.Text, rr.Pages)
			}
			if rr.RemoteID != "+6561234567" || tr.RemoteID != "+6567654321" {
				t.Fatalf("idents: %q %q", rr.RemoteID, tr.RemoteID)
			}
			checkPages(t, tx, rr.Received)
		})
	}
}
