package fax

import (
	"os"
	"testing"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// TestAnalyzeTrace decodes a fax call's audio kept by RingQswitch (the
// "-heard.wav" of a failed fax): FAX_TRACE=/path/to/file-heard.wav go test
// -run TestAnalyzeTrace -v ./internal/fax. It lists the V.21 messages in it,
// with the time each ended.
func TestAnalyzeTrace(t *testing.T) {
	path := os.Getenv("FAX_TRACE")
	if path == "" {
		t.Skip("FAX_TRACE not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := media.DecodeWAV(data)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	h := &hdlcRx{onFrame: func(f []byte, ok bool) {
		at := float64(n) / sampleRate
		if !ok {
			t.Logf("%7.2f s  damaged frame (%d bytes)", at, len(f))
			return
		}
		name := "?"
		if len(f) >= 3 {
			name = fcfName(fcfOf(f))
		}
		t.Logf("%7.2f s  %s % x", at, name, f)
	}}
	rx := newFSKRx(h.putBit, nil)
	var power powerMeter
	power.alpha = 1.0 / 800
	for i, v := range pcm {
		n = i
		rx.put(float64(v))
		power.put(float64(v))
		if i%8000 == 0 {
			t.Logf("%7.2f s  level %.1f dBm0, V.21 carriers so far %d", float64(i)/sampleRate, powerDBm0(power.p), rx.bursts)
		}
	}
	t.Logf("%d samples (%.1f s), V.21 carriers %d", len(pcm), float64(len(pcm))/sampleRate, rx.bursts)
}
