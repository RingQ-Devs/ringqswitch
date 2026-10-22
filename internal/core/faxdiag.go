package core

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/fax"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// Troubleshooting a fax call: what the far end sends (how loud, which fax
// tones, whether audio arrives at all) goes to the log every few seconds,
// and the call's audio is kept as WAV files when the fax fails (or always
// with fax_trace=true) in <fax-dir>/<domain uuid>/trace/.

const (
	faxReportEvery = 250                // frames (5 s)
	faxTraceMax    = 10 * 60 * 8000     // samples kept per direction (10 minutes)
	faxTraceKeep   = 7 * 24 * time.Hour // trace files older than this are removed
)

var faxTones = [...]struct {
	hz   float64
	name string
}{{1100, "1100 Hz (CNG)"}, {2100, "2100 Hz (CED)"}, {1650, "1650 Hz (V.21)"}, {1850, "1850 Hz (V.21)"}}

type faxListener struct {
	// T.38: datagrams received (and how many of those were unreadable),
	// sent, and IFPs lost or recovered from redundancy
	t38                   bool
	t38In, t38Bad, t38Out int
	t38Lost, t38Recovered int
	t38Window             int

	frames, total int
	sum           float64 // sum of squares this window
	n             int
	tones         [len(faxTones)]float64
	heard, sent   []int16
}

// goertzel returns the mean-square power of x at hz.
func goertzel(x []int16, hz float64) float64 {
	w := 2 * math.Pi * hz / 8000
	c := 2 * math.Cos(w)
	var s1, s2 float64
	for _, v := range x {
		s0 := float64(v) + c*s1 - s2
		s2, s1 = s1, s0
	}
	p := s1*s1 + s2*s2 - c*s1*s2
	n := float64(len(x))
	return 2 * p / (n * n)
}

func dbm0(meanSquare float64) float64 {
	if meanSquare < 1e-9 {
		return -99
	}
	return 10*math.Log10(meanSquare/(32767.0*32767.0/2)) + 3.14
}

func (l *faxListener) hear(fr []int16) {
	l.frames++
	l.total++
	for _, v := range fr {
		f := float64(v)
		l.sum += f * f
	}
	l.n += len(fr)
	for i, t := range faxTones {
		l.tones[i] += goertzel(fr, t.hz)
	}
	if len(l.heard) < faxTraceMax {
		l.heard = append(l.heard, fr...)
	}
}

func (l *faxListener) send(fr []int16) {
	if len(l.sent) < faxTraceMax {
		l.sent = append(l.sent, fr...)
	}
}

// report describes the last window and starts a new one.
func (l *faxListener) report() string {
	if l.t38 {
		in := l.t38In - l.t38Window
		l.t38Window = l.t38In
		switch {
		case l.t38In == 0:
			return fmt.Sprintf("T.38: no packets are arriving from the far end (%d sent)", l.t38Out)
		case in == 0: // normal while a page goes out
			return fmt.Sprintf("T.38: the far end is quiet (%d packets received in all), %d sent", l.t38In, l.t38Out)
		}
		return fmt.Sprintf("T.38: %d packets received in 5 s (%d in all), %d sent", in, l.t38In, l.t38Out)
	}
	defer func() { l.frames, l.sum, l.n, l.tones = 0, 0, 0, [len(faxTones)]float64{} }()
	if l.frames == 0 {
		return "no audio is arriving from the far end (no RTP)"
	}
	ms := l.sum / float64(l.n)
	var heard []string
	for i, t := range faxTones {
		if l.tones[i]/float64(l.frames) > 0.2*ms && dbm0(ms) > -50 {
			heard = append(heard, t.name)
		}
	}
	what := "no fax tones"
	if len(heard) > 0 {
		what = strings.Join(heard, ", ")
	}
	return fmt.Sprintf("hearing %d packets, level %.1f dBm0, %s", l.frames, dbm0(ms), what)
}

// keepTrace writes the call's audio for troubleshooting.
func (c *Core) keepTrace(ch *Channel, l *faxListener) {
	dir := filepath.Join(c.config().Settings.FaxDir, firstNonEmpty(ch.Var("domain_uuid"), "shared"), "trace")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		ch.log.Warn("fax: cannot keep the call's audio", "err", err)
		return
	}
	if old, err := os.ReadDir(dir); err == nil {
		for _, e := range old {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > faxTraceKeep {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	for _, f := range []struct {
		name string
		pcm  []int16
	}{{"heard", l.heard}, {"sent", l.sent}} {
		path := filepath.Join(dir, ch.UUID+"-"+f.name+".wav")
		w, err := media.CreateWAV(path)
		if err != nil {
			ch.log.Warn("fax: cannot keep the call's audio", "err", err)
			return
		}
		_ = w.Write(f.pcm)
		_ = w.Close()
	}
	ch.log.Info("fax: the call's audio is kept for troubleshooting", "dir", dir, "files", ch.UUID+"-heard.wav, "+ch.UUID+"-sent.wav")
}

func (l *faxListener) t38Summary() string {
	if !l.t38 {
		return ""
	}
	return fmt.Sprintf("; T.38 datagrams received %d (unreadable %d), sent %d, packets lost %d, recovered %d",
		l.t38In, l.t38Bad, l.t38Out, l.t38Lost, l.t38Recovered)
}

func faxStatsLine(s fax.Stats) string {
	return fmt.Sprintf("V.21 signals heard %d, messages received %d, damaged %d", s.V21Bursts, s.FramesOK, s.FramesBad)
}
