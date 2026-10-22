package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/Cal4Care-Developers/RingQswitch/internal/webphone"
)

// webPhone is the browser dialer of one tab: a WebRTC connection to the
// browser (G.711 audio) and a SIP user agent registered as the signed-in
// extension. The audio of the active call (answered, not on hold) flows
// between them.
type webPhone struct {
	s     *Server
	c     *wsClient
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample
	ua    *webphone.UA

	mu      sync.Mutex
	calls   map[string]webphone.CallInfo
	active  string
	muted   bool
	closed  bool
	offered []string // our ICE candidates, for the log when audio fails
	via     string   // udp or tcp, once connected

	audio     phoneAudio
	callAudio map[string]callAudio // answered calls, for the audio check
}

func (s *Server) phoneWS(c *wsClient, m wsMsg) {
	if c.p.Kind != "extension" {
		c.fail(m, errors.New("sign in with an extension to use the dialer"))
		return
	}
	if s.rtc == nil {
		c.fail(m, errors.New("the dialer is off (webrtc-port is 0 or its UDP port could not be opened)"))
		return
	}
	c.mu.Lock()
	ph := c.phone
	c.mu.Unlock()
	if m.T == "phone.start" {
		if ph != nil {
			ph.close()
		}
		ph, err := s.startPhone(c, m.SDP, m.Gen)
		if err != nil {
			c.fail(m, err)
			return
		}
		c.mu.Lock()
		c.phone = ph
		c.mu.Unlock()
		return
	}
	if ph == nil {
		c.fail(m, errors.New("the dialer is not started"))
		return
	}
	var err error
	switch m.T {
	case "phone.ice":
		var cand webrtc.ICECandidateInit
		if json.Unmarshal(m.Candidate, &cand) == nil && cand.Candidate != "" {
			err = ph.pc.AddICECandidate(cand)
		}
	case "phone.dial":
		if !reNumber.MatchString(m.Number) && !reDID.MatchString(m.Number) {
			err = errors.New("dial digits, * or # only")
			break
		}
		var id string
		id, err = ph.ua.Dial(m.Number)
		if err == nil {
			c.reply(m, map[string]any{"t": "phone.dialing", "call": id})
		}
	case "phone.answer":
		// Answering puts the other calls on hold.
		ph.holdOthers(m.Call)
		err = ph.ua.Answer(m.Call)
	case "phone.hangup":
		err = ph.ua.Hangup(m.Call)
	case "phone.hold":
		if !m.On {
			ph.holdOthers(m.Call)
		}
		err = ph.ua.Hold(m.Call, m.On)
	case "phone.mute":
		ph.mu.Lock()
		ph.muted = m.On
		ph.mu.Unlock()
	case "phone.stats":
		c.reply(m, ph.audioCheck())
	case "phone.dtmf":
		err = ph.ua.DTMF(m.Call, m.Digits)
	case "phone.transfer":
		if !reNumber.MatchString(m.Target) && !reDID.MatchString(m.Target) {
			err = errors.New("transfer to: a number")
			break
		}
		err = ph.ua.Transfer(m.Call, m.Target)
	case "phone.attxfer":
		err = ph.ua.AttendedTransfer(m.Call, m.Target)
	case "phone.stop":
		ph.close()
		c.mu.Lock()
		c.phone = nil
		c.mu.Unlock()
	}
	if err != nil {
		c.fail(m, err)
	}
}

// startPhone answers the browser's offer. gen is echoed in the signalling
// messages, so a browser that restarted the dialer ignores answers and
// candidates of an earlier start.
func (s *Server) startPhone(c *wsClient, offerRaw json.RawMessage, gen int64) (*webPhone, error) {
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(offerRaw, &offer); err != nil || offer.Type != webrtc.SDPTypeOffer {
		return nil, errors.New("phone.start needs the browser's SDP offer")
	}
	cfg := s.sw.Config()
	u := cfg.Directory.LookupIn(c.p.Domain, c.p.Extension)
	if u == nil || u.Password == "" {
		return nil, errors.New("this extension has no SIP password")
	}
	pc, err := s.rtc.phoneAPI.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, "audio", "ringqswitch")
	if err != nil {
		pc.Close()
		return nil, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return nil, err
	}
	go func() { // RTCP from the browser
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	ph := &webPhone{s: s, c: c, pc: pc, track: track, calls: map[string]webphone.CallInfo{}, callAudio: map[string]callAudio{}}
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand != nil {
			ph.mu.Lock()
			ph.offered = append(ph.offered, fmt.Sprintf("%s %s:%d", cand.Protocol, cand.Address, cand.Port))
			ph.mu.Unlock()
			c.json(map[string]any{"t": "phone.ice", "candidate": cand.ToJSON(), "gen": gen})
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		c.json(map[string]any{"t": "phone.media", "state": st.String(), "gen": gen})
		switch st {
		case webrtc.PeerConnectionStateConnected:
			if pair, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
				ph.mu.Lock()
				ph.via = pair.Local.Protocol.String()
				ph.mu.Unlock()
				s.log.Info("dialer: audio connected", "user", c.p.key(), "via", pair.Local.Protocol.String(), "browser", fmt.Sprintf("%s:%d", pair.Remote.Address, pair.Remote.Port))
			}
		case webrtc.PeerConnectionStateFailed:
			ph.mu.Lock()
			offered := append([]string(nil), ph.offered...)
			ph.mu.Unlock()
			s.log.Warn("dialer: audio could not connect: the browser's packets did not reach these addresses (open webrtc-port UDP in the firewall)",
				"user", c.p.key(), "browser", c.conn.RemoteAddr().String(), "offered", strings.Join(offered, ", "))
		}
	})
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			if len(pkt.Payload) == 0 || pkt.PayloadType != uint8(remote.PayloadType()) {
				continue // the PCMU stream only
			}
			ph.audio.fromBrowser.add(pkt.Payload)
			ph.mu.Lock()
			active, muted := ph.active, ph.muted
			ua := ph.ua
			ph.mu.Unlock()
			if active == "" || ua == nil {
				continue
			}
			payload := pkt.Payload
			if muted { // keep the stream going (NAT, rtp-timeout) with silence
				payload = bytes.Repeat([]byte{0xff}, len(payload))
			}
			if ua.SendAudio(active, payload) {
				ph.audio.toCall.add(payload)
			}
		}
	})
	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		return nil, err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return nil, err
	}
	c.json(map[string]any{"t": "phone.sdp", "sdp": pc.LocalDescription(), "gen": gen})
	mid := ""
	if tr := pc.GetTransceivers(); len(tr) > 0 {
		mid = tr[0].Mid()
	}
	for _, cand := range s.rtc.pageCandidates(c.host, mid) {
		ph.mu.Lock()
		ph.offered = append(ph.offered, cand.Candidate)
		ph.mu.Unlock()
		c.json(map[string]any{"t": "phone.ice", "candidate": cand, "gen": gen})
	}

	st := cfg.Settings
	local := netip.MustParseAddr("127.0.0.1")
	if a, err := netip.ParseAddr(st.SIPBindIP); err == nil && !a.IsUnspecified() {
		local = a
	}
	domain, ext := c.p.Domain, c.p.Extension
	ua, err := webphone.Start(webphone.Config{
		User: ext, Domain: domain, Server: netip.AddrPortFrom(local, uint16(s.sw.SIPPort())), LocalIP: local,
		UserAgent: s.cfg.Brand + " Web Dialer", Expires: 300, Log: s.log,
		Password: func() string {
			if u := s.sw.Config().Directory.LookupIn(domain, ext); u != nil {
				return u.Password
			}
			return ""
		},
	}, ph)
	if err != nil {
		pc.Close()
		return nil, err
	}
	s.sw.TrustSource(ua.LocalAddr()) // a stale password must not block the server's own address
	ph.mu.Lock()
	ph.ua = ua
	ph.mu.Unlock()
	return ph, nil
}

// Registration implements webphone.Handler.
func (ph *webPhone) Registration(ok bool, reason string) {
	ph.c.json(map[string]any{"t": "phone.reg", "ok": ok, "reason": reason, "now": time.Now().UnixMilli()})
}

// CallUpdate implements webphone.Handler.
func (ph *webPhone) CallUpdate(info webphone.CallInfo) {
	ph.mu.Lock()
	if info.State == webphone.StateEnded {
		delete(ph.calls, info.ID)
	} else {
		ph.calls[info.ID] = info
	}
	ph.pickActive()
	ca, had := ph.callAudio[info.ID]
	switch {
	case info.State == webphone.StateAnswered && !had:
		ph.callAudio[info.ID] = ph.audio.start()
	case info.State == webphone.StateEnded && had:
		delete(ph.callAudio, info.ID)
	}
	ph.mu.Unlock()
	if info.State == webphone.StateEnded && had {
		ph.logAudio(info, ph.audio.report(ca))
	}
	ph.c.json(map[string]any{"t": "phone.call", "call": info, "now": time.Now().UnixMilli()})
}

// pickActive chooses the call whose audio flows: the most recently
// answered call that is not on hold (or one with early media).
func (ph *webPhone) pickActive() {
	best, bestAt := "", int64(-1)
	for id, c := range ph.calls {
		switch {
		case c.State == webphone.StateAnswered && !c.Held:
			if c.Answered > bestAt {
				best, bestAt = id, c.Answered
			}
		case c.State == webphone.StateEarly && bestAt < 0:
			best = id
		}
	}
	ph.active = best
}

func (ph *webPhone) holdOthers(except string) {
	ph.mu.Lock()
	var ids []string
	for id, c := range ph.calls {
		if id != except && c.State == webphone.StateAnswered && !c.Held {
			ids = append(ids, id)
		}
	}
	ua := ph.ua
	ph.mu.Unlock()
	for _, id := range ids {
		_ = ua.Hold(id, true)
	}
}

// Audio implements webphone.Handler.
func (ph *webPhone) Audio(callID string, payload []byte) {
	ph.mu.Lock()
	active := ph.active
	ph.mu.Unlock()
	if callID != active {
		return
	}
	ph.audio.fromCall.add(payload)
	if ph.track.WriteSample(media.Sample{Data: payload, Duration: time.Duration(len(payload)) * time.Second / 8000}) == nil {
		ph.audio.toBrowser.add(payload)
	}
}

// audioCheck reports the audio of the call being heard, for the dialer's
// audio check.
func (ph *webPhone) audioCheck() map[string]any {
	ph.mu.Lock()
	active, via, ua := ph.active, ph.via, ph.ua
	ca, ok := ph.callAudio[active]
	ph.mu.Unlock()
	out := map[string]any{"t": "phone.stats", "via": via, "connection": ph.pc.ConnectionState().String()}
	if !ok {
		out["call"] = ""
		return out
	}
	out["call"] = active
	out["audio"] = ph.audio.report(ca)
	if ua != nil {
		if mi, ok := ua.Media(active); ok {
			out["rtp"] = mi
		}
	}
	return out
}

// logAudio records how a call's audio went, naming the direction that
// failed, so one-way audio can be traced from the log.
func (ph *webPhone) logAudio(info webphone.CallInfo, r audioReport) {
	if r.Seconds < 4 {
		return
	}
	ph.mu.Lock()
	via := ph.via
	ph.mu.Unlock()
	args := []any{"user", ph.c.p.key(), "number", info.Number, "seconds", r.Seconds, "via", via,
		"from_browser", r.FromBrowser.Packets, "to_call", r.ToCall.Packets, "from_call", r.FromCall.Packets, "to_browser", r.ToBrowser.Packets,
		"browser_level_db", r.FromBrowser.Loudest, "call_level_db", r.FromCall.Loudest}
	if len(r.Problems) > 0 {
		ph.s.log.Warn("dialer: one-way or no audio", append(args, "problem", r.summary())...)
		return
	}
	ph.s.log.Info("dialer: call audio ok", args...)
}

// MWI implements webphone.Handler.
func (ph *webPhone) MWI(newMsgs, oldMsgs int) {
	ph.c.json(map[string]any{"t": "phone.mwi", "new": newMsgs, "old": oldMsgs})
}

func (ph *webPhone) close() {
	ph.mu.Lock()
	if ph.closed {
		ph.mu.Unlock()
		return
	}
	ph.closed = true
	ua := ph.ua
	ph.mu.Unlock()
	if ua != nil {
		ua.Close()
		ph.s.sw.UntrustSource(ua.LocalAddr())
	}
	ph.pc.Close()
}
