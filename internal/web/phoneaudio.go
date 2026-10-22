package web

import (
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// silentDB is the level below which a stream counts as silence (dBFS of the
// mean amplitude: speech is around -40 to -15, a muted microphone below -70).
const silentDB = -60

// flow counts one direction of the dialer's audio and how loud it is, so the
// audio check can say where sound stops.
type flow struct {
	mu      sync.Mutex
	packets uint64
	sum     float64 // |sample| sum in the current second
	n       int
	sec     int64
	level   float64 // the last full second, dBFS
	loudest float64 // the loudest second since reset
	recent  float64 // the loudest of the last few seconds
	recentT int64
}

func dbfs(meanAbs float64) float64 {
	if meanAbs < 1 {
		return -90
	}
	return math.Max(-90, 20*math.Log10(meanAbs/32768))
}

// add counts a packet of PCMU.
func (f *flow) add(ulaw []byte) {
	now := time.Now().Unix()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.packets++
	if now != f.sec {
		f.closeSecond()
		f.sec = now
	}
	for _, b := range ulaw {
		f.sum += math.Abs(float64(media.UlawToLinear(b)))
	}
	f.n += len(ulaw)
}

func (f *flow) closeSecond() {
	f.level = -90
	if f.n > 0 {
		f.level = dbfs(f.sum / float64(f.n))
	}
	f.loudest = math.Max(f.loudest, f.level)
	if f.level >= f.recent || f.sec-f.recentT >= 5 {
		f.recent, f.recentT = f.level, f.sec
	}
	f.sum, f.n = 0, 0
}

type flowStats struct {
	Packets uint64  `json:"packets"`
	Level   float64 `json:"level"`   // dBFS over the last second (-90 = nothing)
	Recent  float64 `json:"recent"`  // the loudest of the last few seconds
	Loudest float64 `json:"loudest"` // since the call was answered
}

func (f *flow) stats() flowStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	level, recent := f.level, f.recent
	if now := time.Now().Unix(); now-f.sec > 1 {
		level = -90 // nothing arrived lately
		if now-f.sec > 5 {
			recent = -90
		}
	}
	loudest := f.loudest
	if f.n > 0 {
		loudest = math.Max(loudest, dbfs(f.sum/float64(f.n)))
	}
	return flowStats{Packets: f.packets, Level: math.Round(level), Recent: math.Round(recent), Loudest: math.Round(loudest)}
}

// mark starts a new call: the loudest level restarts.
func (f *flow) mark() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loudest, f.recent = -90, -90
	return f.packets
}

// phoneAudio is the four directions of a dialer call's audio:
//
//	browser -> server -> switch (the other side)
//	browser <- server <- switch
type phoneAudio struct {
	fromBrowser, toCall, fromCall, toBrowser flow
}

type callAudio struct {
	base     [4]uint64
	answered time.Time
}

func (a *phoneAudio) flows() [4]*flow {
	return [4]*flow{&a.fromBrowser, &a.toCall, &a.fromCall, &a.toBrowser}
}

func (a *phoneAudio) start() callAudio {
	ca := callAudio{answered: time.Now()}
	for i, f := range a.flows() {
		ca.base[i] = f.mark()
	}
	return ca
}

// audioReport is the audio check of the current call.
type audioReport struct {
	FromBrowser flowStats `json:"from_browser"`
	ToCall      flowStats `json:"to_call"`
	FromCall    flowStats `json:"from_call"`
	ToBrowser   flowStats `json:"to_browser"`
	Seconds     float64   `json:"seconds"`
	Problems    []string  `json:"problems"`
}

func (a *phoneAudio) report(ca callAudio) audioReport {
	st := [4]flowStats{}
	for i, f := range a.flows() {
		st[i] = f.stats()
		st[i].Packets -= min(st[i].Packets, ca.base[i])
	}
	r := audioReport{FromBrowser: st[0], ToCall: st[1], FromCall: st[2], ToBrowser: st[3],
		Seconds: math.Round(time.Since(ca.answered).Seconds())}
	r.Problems = r.diagnose()
	return r
}

// diagnose names where the sound stops. It needs a few seconds of call.
func (r audioReport) diagnose() []string {
	if r.Seconds < 4 {
		return []string{}
	}
	enough := uint64(r.Seconds * 10) // 50 packets a second when it works
	var out []string
	switch {
	case r.FromBrowser.Packets < enough:
		out = append(out, "No sound reaches the server from this browser: the browser is not sending its microphone (check the microphone permission, or restart the dialer).")
	case r.FromBrowser.Loudest < silentDB:
		out = append(out, "This browser sends only silence: the microphone is muted, the wrong one is selected, or it is switched off in the computer's sound settings.")
	case r.ToCall.Packets < enough:
		out = append(out, "The server does not pass the browser's sound to the call (is the call on hold or muted?).")
	}
	switch {
	case r.FromCall.Packets < enough:
		out = append(out, "No sound reaches the server from the other side: its RTP does not arrive. Open UDP 16384-32768 in the server firewall (ufw and the cloud firewall); a phone behind a router may need SIP ALG turned off.")
	case r.FromCall.Loudest < silentDB:
		out = append(out, "The other side sends only silence (its microphone is muted or not working).")
	case r.ToBrowser.Packets < enough:
		out = append(out, "The server does not send the other side's sound to this browser.")
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func (r audioReport) summary() string {
	return strings.Join(r.Problems, " ")
}
