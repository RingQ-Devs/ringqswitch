package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sdp"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Channel states (reported in events).
const (
	CSNew             = "CS_NEW"
	CSRouting         = "CS_ROUTING"
	CSExecute         = "CS_EXECUTE"
	CSExchangeMedia   = "CS_EXCHANGE_MEDIA"
	CSHangup          = "CS_HANGUP"
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// dialog is the SIP dialog state of a channel.
type dialog struct {
	callID       string
	localTag     string
	remoteTag    string
	localURI     *sip.Address // From (outbound) / To (inbound), without tag
	remoteURI    *sip.Address
	remoteTarget *sip.URI
	routeSet     []string
	localSeq     uint32
	remoteSeq    uint32
	dst          sip.Addr // where in-dialog requests are sent (NAT friendly)
	contact      string   // our Contact header value
}

func (d *dialog) key() string { return d.callID + "|" + d.localTag }

// request builds an in-dialog request. The caller must hold the channel lock.
func (d *dialog) request(method string) *sip.Message {
	req := sip.NewRequest(method, d.remoteTarget.Clone())
	from := d.localURI.Clone()
	from.Params.Set("tag", d.localTag)
	to := d.remoteURI.Clone()
	if d.remoteTag != "" {
		to.Params.Set("tag", d.remoteTag)
	}
	req.Add("From", from.String())
	req.Add("To", to.String())
	req.Add("Call-ID", d.callID)
	d.localSeq++
	req.Add("CSeq", fmt.Sprintf("%d %s", d.localSeq, method))
	for _, r := range d.routeSet {
		req.Add("Route", r)
	}
	switch method {
	case "INVITE", "UPDATE", "REFER", "NOTIFY":
		req.Add("Contact", d.contact)
	}
	return req
}

// ack builds the ACK for a 2xx to the INVITE with sequence number seq.
func (d *dialog) ack(seq uint32) *sip.Message {
	req := sip.NewRequest("ACK", d.remoteTarget.Clone())
	from := d.localURI.Clone()
	from.Params.Set("tag", d.localTag)
	to := d.remoteURI.Clone()
	if d.remoteTag != "" {
		to.Params.Set("tag", d.remoteTag)
	}
	req.Add("From", from.String())
	req.Add("To", to.String())
	req.Add("Call-ID", d.callID)
	req.Add("CSeq", fmt.Sprintf("%d ACK", seq))
	for _, r := range d.routeSet {
		req.Add("Route", r)
	}
	return req
}

// transferReq redirects a channel's executor: to a dialplan destination,
// to a list of actions, or out of the channel altogether (adopt: another
// channel's bridge takes it over).
type transferReq struct {
	dest    string
	context string
	actions []dialplan.Action
	adopt   bool
}

type bridgeCtl struct {
	detach chan struct{}
	once   sync.Once
}

func (b *bridgeCtl) Detach() { b.once.Do(func() { close(b.detach) }) }

func (b *bridgeCtl) detached() bool {
	select {
	case <-b.detach:
		return true
	default:
		return false
	}
}

type hangupHow int

const (
	hangupLocal       hangupHow = iota // we decided: send BYE / CANCEL / error response
	hangupRemoteBye                    // peer sent BYE
	hangupRemoteFinal                  // outbound leg got a failure response
	hangupCancelled                    // inbound leg got CANCEL (487 already queued)
)

// Channel is one call leg.
type Channel struct {
	core      *Core
	UUID      string
	Direction string
	Created   time.Time
	log       *slog.Logger

	mu              sync.Mutex
	vars            map[string]string
	state           string
	callState       string
	loggedCallState string // the last one written to the log
	answered        bool
	early           bool
	ringing         bool
	answeredAt      time.Time
	progressAt      time.Time
	hungupAt        time.Time
	cause           Cause
	hungup          bool
	dlg             dialog

	// identity of the bridge partner, copied when the bridge starts
	peerName, peerCIDName, peerCIDNum, peerDest string

	// inbound
	inviteTx  *sip.ServerTx
	offer     *sdp.Session
	acked     chan struct{}
	ackOnce   sync.Once
	lateOffer bool

	// outbound
	inviteReq      *sip.Message
	inviteCli      *sip.ClientTx
	inviteSeq      uint32
	gotProvisional bool
	cancelPending  bool
	gateway        *Gateway
	authTries      int

	// media
	ep         *media.Endpoint
	noRTPWarn  bool // "no audio received" was logged
	localSDP   *sdp.Session
	remoteSDP  *sdp.Session
	sdpVersion uint64
	sessID     string
	// faxT38 is set while rxfax/txfax runs: it takes a re-INVITE to T.38
	// (the far end's UDPTL address and T.38 offer) and returns our answer.
	faxT38   func(remote netip.AddrPort, offer sdp.T38) (sdp.T38, bool)
	t38Media bool // the media is T.38 (UDPTL) now

	// execution and bridging
	hasExecutor     bool
	execDone        chan struct{} // closed when the running dialplan executor returns
	appCancel       context.CancelFunc
	pendingTransfer *transferReq
	queuedActions   []dialplan.Action
	transfers       int
	partner         *Channel
	bctl            *bridgeCtl
	dtmf            chan rune
	app             string
	rec             *sessionRecorder
	held            bool
	holdCancel      context.CancelFunc
	holdDone        <-chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	steps  []flowStep     // the call's path (flow.go)
	hears  []chan []int16 // listeners of what this party says (Hear)
}

func (c *Core) newChannel(direction string) *Channel {
	ctx, cancel := context.WithCancel(context.Background())
	ch := &Channel{
		core: c, UUID: sip.NewUUID(), Direction: direction, Created: time.Now(),
		vars: map[string]string{}, state: CSNew, callState: "DOWN",
		acked: make(chan struct{}), dtmf: make(chan rune, 64),
		sessID: strconv.FormatInt(time.Now().UnixNano()%1e9, 10), sdpVersion: 1,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	ch.log = c.log.With("uuid", ch.UUID)
	s := c.config().Settings
	ch.vars["uuid"] = ch.UUID
	ch.vars["direction"] = direction
	ch.vars["domain_name"] = s.Domain
	ch.vars["sounds_dir"] = s.SoundsDir
	ch.vars["recordings_dir"] = s.RecordingsDir
	ch.vars["fax_dir"] = s.FaxDir
	ch.vars["core_uuid"] = c.coreUUID
	return ch
}

func (ch *Channel) markProgressLocked() {
	if ch.progressAt.IsZero() {
		ch.progressAt = time.Now()
	}
}

// identity returns name, caller ID name/number and destination.
func (ch *Channel) identity() [4]string {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return [4]string{ch.vars["channel_name"], ch.vars["caller_id_name"], ch.vars["caller_id_number"], ch.vars["destination_number"]}
}

// Var returns a channel variable.
func (ch *Channel) Var(name string) string {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.vars[name]
}

// SetVar sets a channel variable; an empty value removes it.
func (ch *Channel) SetVar(name, value string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if value == "" {
		delete(ch.vars, name)
		return
	}
	ch.vars[name] = value
}

// Vars returns a copy of all variables.
func (ch *Channel) Vars() map[string]string {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	out := make(map[string]string, len(ch.vars))
	for k, v := range ch.vars {
		out[k] = v
	}
	return out
}

// Expand substitutes ${name} references, $${globals} and ${api args}
// calls (see expand.go).
func (ch *Channel) Expand(s string) string { return ch.core.expand(s, ch.Var) }

// Field returns a dialplan condition field: caller profile names
// (ani, chan_name, username...) map to their variables, anything else is a
// channel variable falling back to a global one.
func (ch *Channel) Field(name string) string {
	switch name {
	case "ani":
		name = "caller_id_number"
	case "chan_name":
		name = "channel_name"
	case "username":
		name = "user_name"
	case "dialplan":
		return "XML"
	case "source":
		return "mod_sofia"
	}
	return ch.core.lookupVar(name, ch.Var)
}

// RunInline executes an application while the dialplan is being hunted
// (inline="true"), so later conditions see its effect.
func (ch *Channel) RunInline(app, data string) bool {
	fn := ch.core.apps[app]
	if fn == nil || !inlineApps[app] {
		return false
	}
	if err := fn(ch.ctx, ch.core, ch, ch.Expand(data)); err != nil {
		ch.log.Debug("inline application failed", "app", app, "err", err)
	}
	return true
}

// Done is closed when the channel has hung up.
func (ch *Channel) Done() <-chan struct{} { return ch.done }

// Context is cancelled when the channel hangs up.
func (ch *Channel) Context() context.Context { return ch.ctx }

// IsHungUp reports whether the channel has hung up.
func (ch *Channel) IsHungUp() bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.hungup
}

// IsAnswered reports whether the channel is answered.
func (ch *Channel) IsAnswered() bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.answered
}

// Cause returns the hangup cause.
func (ch *Channel) Cause() Cause {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.cause
}

// logCallState writes "Callstate Change RINGING -> EARLY" lines.
func (ch *Channel) logCallState() {
	ch.mu.Lock()
	prev, cur := ch.loggedCallState, ch.callState
	ch.loggedCallState = cur
	ch.mu.Unlock()
	if prev == "" {
		prev = "DOWN"
	}
	if prev != cur {
		ch.debugf("(%s) Callstate Change %s -> %s", ch.name(), prev, cur)
	}
}

func (ch *Channel) setState(s string) {
	ch.mu.Lock()
	ch.state = s
	ch.mu.Unlock()
}

// Partner returns the bridged channel.
func (ch *Channel) Partner() *Channel {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.partner
}

func (ch *Channel) name() string { return ch.Var("channel_name") }

// fire publishes a channel event. Channel events carry
// the channel variables (as variable_<name> headers), except DTMF.
func (ch *Channel) fire(name string, extra ...string) {
	if name == EvChannelCallState {
		ch.logCallState()
	}
	e := NewEvent(name, "")
	ch.eventHeaders(e, name != EvDTMF)
	for i := 0; i+1 < len(extra); i += 2 {
		e.Add(extra[i], extra[i+1])
	}
	ch.core.events.Publish(e)
}

func (ch *Channel) eventHeaders(e *Event, withVars bool) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	answer := "ringing"
	switch {
	case ch.hungup:
		answer = "hangup"
	case ch.answered:
		answer = "answered"
	case ch.early:
		answer = "early"
	}
	v := ch.vars
	us := func(t time.Time) string {
		if t.IsZero() {
			return "0"
		}
		return strconv.FormatInt(t.UnixMicro(), 10)
	}
	e.Add("Channel-State", ch.state)
	e.Add("Channel-Call-State", ch.callState)
	e.Add("Channel-Name", v["channel_name"])
	e.Add("Unique-ID", ch.UUID)
	e.Add("Call-Direction", ch.Direction)
	e.Add("Presence-Call-Direction", ch.Direction)
	e.Add("Channel-HIT-Dialplan", "true")
	if v["presence_id"] != "" {
		e.Add("Channel-Presence-ID", v["presence_id"])
	}
	if ch.ep != nil {
		if sc, ok := ch.ep.SendCodec(); ok {
			rate := strconv.Itoa(sc.Rate)
			e.Add("Channel-Read-Codec-Name", sc.Name)
			e.Add("Channel-Read-Codec-Rate", rate)
			e.Add("Channel-Write-Codec-Name", sc.Name)
			e.Add("Channel-Write-Codec-Rate", rate)
		}
	}
	e.Add("Answer-State", answer)
	e.Add("Caller-Direction", ch.Direction)
	e.Add("Caller-Logical-Direction", ch.Direction)
	e.Add("Caller-Username", v["user_name"])
	e.Add("Caller-Dialplan", "XML")
	e.Add("Caller-Caller-ID-Name", v["caller_id_name"])
	e.Add("Caller-Caller-ID-Number", v["caller_id_number"])
	e.Add("Caller-Orig-Caller-ID-Name", v["caller_id_name"])
	e.Add("Caller-Orig-Caller-ID-Number", v["caller_id_number"])
	if v["callee_id_number"] != "" {
		e.Add("Caller-Callee-ID-Name", v["callee_id_name"])
		e.Add("Caller-Callee-ID-Number", v["callee_id_number"])
	}
	e.Add("Caller-Network-Addr", v["network_addr"])
	e.Add("Caller-ANI", v["caller_id_number"])
	e.Add("Caller-Destination-Number", v["destination_number"])
	e.Add("Caller-Unique-ID", ch.UUID)
	e.Add("Caller-Source", "mod_sofia")
	e.Add("Caller-Context", v["context"])
	e.Add("Caller-Channel-Name", v["channel_name"])
	e.Add("Caller-Profile-Index", "1")
	e.Add("Caller-Profile-Created-Time", us(ch.Created))
	e.Add("Caller-Channel-Created-Time", us(ch.Created))
	e.Add("Caller-Channel-Answered-Time", us(ch.answeredAt))
	e.Add("Caller-Channel-Progress-Time", us(ch.progressAt))
	e.Add("Caller-Channel-Hangup-Time", us(ch.hungupAt))
	if ch.partner != nil {
		p := ch.partner
		other := "originatee"
		if ch.Direction == DirectionOutbound {
			other = "originator"
		}
		e.Add("Other-Type", other)
		e.Add("Other-Leg-Unique-ID", p.UUID)
		e.Add("Other-Leg-Direction", p.Direction)
		e.Add("Other-Leg-Channel-Name", ch.peerName)
		e.Add("Other-Leg-Caller-ID-Name", ch.peerCIDName)
		e.Add("Other-Leg-Caller-ID-Number", ch.peerCIDNum)
		e.Add("Other-Leg-Destination-Number", ch.peerDest)
	}
	if ch.hungup {
		e.Add("Hangup-Cause", ch.cause.String())
	}
	if withVars {
		keys := make([]string, 0, len(ch.vars))
		for k := range ch.vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			e.Add("variable_"+k, ch.vars[k])
		}
	}
}

// ---------------------------------------------------------------------------
// Media negotiation helpers

func mediaCodecs(cs []sdp.Codec) ([]media.Codec, media.Codec, int) {
	var all []media.Codec
	var send media.Codec
	te := -1
	for _, c := range cs {
		name := c.Name
		if !c.IsDTMF() {
			name = strings.ToUpper(name)
		}
		mc := media.Codec{PT: uint8(c.PT), Name: name, Rate: c.Rate}
		all = append(all, mc)
		switch {
		case c.IsDTMF():
			if te < 0 {
				te = c.PT
			}
		case send.Name == "" && !strings.EqualFold(c.Name, "CN"):
			send = mc
		}
	}
	return all, send, te
}

func (ch *Channel) peerIP() netip.Addr { return ch.dlg.dst.AP.Addr() }

// local describes our media address for this leg. Caller holds ch.mu.
func (ch *Channel) localLocked() sdp.Local {
	return sdp.Local{
		IP:        ch.core.rtpAdvertise(ch.peerIP()),
		Port:      ch.ep.Port,
		SessionID: ch.sessID,
		Version:   ch.sdpVersion,
	}
}

func (ch *Channel) local() sdp.Local {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.localLocked()
}

func (ch *Channel) setCodecs(cs []sdp.Codec) {
	all, send, te := mediaCodecs(cs)
	ch.ep.SetCodecs(all, send, te)
}

// applyRemote points our endpoint at the media address in s.
func (ch *Channel) applyRemote(s *sdp.Session) {
	a, _ := s.Audio()
	if a == nil {
		return
	}
	rtp, ok := ch.mediaAddr(s, a)
	if !ok {
		return
	}
	ch.ep.SetRemote(rtp, netip.AddrPortFrom(rtp.Addr(), uint16(a.RTCPPort())))
	ch.mu.Lock()
	ch.remoteSDP = s
	ch.mu.Unlock()
}

// mediaAddr is where the media of section m goes.
func (ch *Channel) mediaAddr(s *sdp.Session, m *sdp.Media) (netip.AddrPort, bool) {
	addr, err := netip.ParseAddr(s.Conn(m))
	if err != nil || addr.IsUnspecified() {
		return netip.AddrPort{}, false
	}
	addr = addr.Unmap()
	sig := ch.peerIP()
	// Phones behind NAT often put their private address in SDP; if the
	// signalling came from a public address, send media there instead until
	// symmetric RTP latching learns the real port.
	if (addr.IsPrivate() || addr.IsLinkLocalUnicast()) && sig.IsValid() && isPublic(sig) {
		addr = sig
	}
	return netip.AddrPortFrom(addr, uint16(m.Port)), true
}

// localOfferCodecs returns the codecs we offer when terminating media
// ourselves, with prefer first when set.
func (c *Core) localOfferCodecs(prefer string) []sdp.Codec {
	static := map[string]sdp.Codec{
		"PCMU": {PT: 0, Name: "PCMU", Rate: 8000, Channels: 1},
		"PCMA": {PT: 8, Name: "PCMA", Rate: 8000, Channels: 1},
	}
	var out []sdp.Codec
	if cd, ok := static[prefer]; ok {
		out = append(out, cd)
	}
	for _, name := range c.config().Settings.CodecPrefs {
		if name != prefer {
			out = append(out, static[name])
		}
	}
	return append(out, sdp.DTMFCodec)
}

var errIncompatible = errors.New("no compatible codec")

// negotiateLocal prepares an answer that terminates media locally (G.711).
func (ch *Channel) negotiateLocal() (*sdp.Session, error) {
	ch.mu.Lock()
	offer := ch.offer
	ch.mu.Unlock()
	if offer == nil {
		// Late offer: we offer in the 200 OK, the answer arrives in the ACK.
		codecs := ch.core.localOfferCodecs("")
		ch.setCodecs(codecs)
		ch.mu.Lock()
		ch.lateOffer = true
		s := sdp.NewOffer(ch.localLocked(), codecs, "sendrecv")
		ch.mu.Unlock()
		return s, nil
	}
	a, _ := offer.Audio()
	if a == nil {
		return nil, errIncompatible
	}
	chosen := sdp.Select(a.Codecs(), ch.core.config().Settings.CodecPrefs)
	if chosen == nil {
		return nil, errIncompatible
	}
	ch.setCodecs(chosen)
	return sdp.Answer(offer, ch.local(), chosen, sdp.ReverseDirection(offer.Direction(a))), nil
}

// ---------------------------------------------------------------------------
// Inbound leg signalling (we are UAS)

func (ch *Channel) inviteResponse(code int, reason string) *sip.Message {
	resp := sip.NewResponse(ch.inviteTx.Request, code, reason)
	if code > 100 {
		ch.mu.Lock()
		resp.SetToTag(ch.dlg.localTag)
		contact := ch.dlg.contact
		ch.mu.Unlock()
		if code < 300 {
			resp.Add("Contact", contact)
		}
	}
	return resp
}

func (ch *Channel) respondSDP(code int, s *sdp.Session) error {
	resp := ch.inviteResponse(code, "")
	resp.Add("Allow", allowHeader)
	resp.Add("Supported", "replaces")
	if s != nil {
		resp.Add("Content-Type", "application/sdp")
		resp.Body = s.Marshal()
	}
	return ch.inviteTx.Respond(resp)
}

// RingReady sends 180 Ringing on an inbound leg (once).
func (ch *Channel) RingReady() {
	ch.mu.Lock()
	if ch.Direction != DirectionInbound || ch.answered || ch.early || ch.ringing || ch.hungup {
		ch.mu.Unlock()
		return
	}
	ch.ringing = true
	ch.callState = "RINGING"
	ch.markProgressLocked()
	ch.mu.Unlock()
	_ = ch.respondSDP(180, nil)
	ch.fire(EvChannelProgress)
	ch.fire(EvChannelCallState)
}

// PreAnswer sends 183 Session Progress with SDP (early media).
func (ch *Channel) PreAnswer(s *sdp.Session) error {
	ch.mu.Lock()
	if ch.Direction != DirectionInbound || ch.hungup {
		ch.mu.Unlock()
		return errors.New("cannot pre-answer")
	}
	if ch.answered || ch.early {
		ch.mu.Unlock()
		return nil
	}
	ch.early = true
	ch.callState = "EARLY"
	ch.markProgressLocked()
	ch.localSDP = s
	ch.mu.Unlock()
	if err := ch.respondSDP(183, s); err != nil {
		return err
	}
	ch.fire(EvChannelProgressMedia)
	ch.fire(EvChannelCallState)
	return nil
}

// Answer sends 200 OK with SDP on an inbound leg.
func (ch *Channel) Answer(s *sdp.Session) error {
	ch.mu.Lock()
	if ch.hungup {
		ch.mu.Unlock()
		return errors.New("channel hung up")
	}
	if ch.answered {
		ch.mu.Unlock()
		return nil
	}
	if ch.localSDP != nil {
		s = ch.localSDP // the answer given in 183 is final
	}
	ch.answered = true
	ch.answeredAt = time.Now()
	ch.callState = "ACTIVE"
	ch.localSDP = s
	ch.mu.Unlock()
	ch.inviteTx.OnNoAck(func() {
		ch.log.Warn("no ACK for 200 OK, hanging up")
		ch.Hangup(CauseRecoveryOnTimerExpire)
	})
	if err := ch.respondSDP(200, s); err != nil {
		return err
	}
	ch.fire(EvChannelAnswer)
	ch.fire(EvChannelCallState)
	ch.runAnswerHooks()
	return nil
}

// AnswerLocal answers (or pre-answers) the channel with media terminated by
// the switch, as needed by media applications.
func (ch *Channel) AnswerLocal(early bool) error {
	ch.mu.Lock()
	answered, isEarly, dir := ch.answered, ch.early, ch.Direction
	ch.mu.Unlock()
	if dir == DirectionOutbound || answered || (early && isEarly) {
		return nil
	}
	var s *sdp.Session
	if !isEarly {
		var err error
		if s, err = ch.negotiateLocal(); err != nil {
			return err
		}
	}
	if early {
		return ch.PreAnswer(s)
	}
	return ch.Answer(s)
}

// onAck handles the ACK for our 2xx.
func (ch *Channel) onAck(req *sip.Message) {
	if ch.inviteTx != nil {
		ch.inviteTx.Acked()
	}
	ch.ackOnce.Do(func() { close(ch.acked) })
	ch.mu.Lock()
	late := ch.lateOffer
	ch.lateOffer = false
	ch.mu.Unlock()
	if late && req.HasSDP() {
		if s, err := sdp.Parse(req.Body); err == nil {
			ch.applyRemote(s)
			if a, _ := s.Audio(); a != nil {
				ch.setCodecs(a.Codecs())
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Hangup

// Hangup terminates the channel.
func (ch *Channel) Hangup(cause Cause) { ch.hangup(cause, hangupLocal) }

func reasonHeader(c Cause) string {
	return fmt.Sprintf("Q.850;cause=%d;text=\"%s\"", c.Q850(), c.String())
}

func (ch *Channel) hangup(cause Cause, how hangupHow) {
	ch.mu.Lock()
	if ch.hungup {
		ch.mu.Unlock()
		return
	}
	ch.hungup = true
	if cause == CauseNone {
		cause = CauseNormalClearing
	}
	ch.cause = cause
	ch.hungupAt = time.Now()
	prevState := ch.state
	ch.state = CSHangup
	ch.callState = "HANGUP"
	answered := ch.answered
	gotProv := ch.gotProvisional
	if ch.Direction == DirectionOutbound && !answered && !gotProv && how == hangupLocal {
		ch.cancelPending = true
	}
	ch.setHangupVarsLocked(cause, how)
	execDone := ch.execDone
	ch.mu.Unlock()
	ch.cancel()
	ch.notice(fmt.Sprintf("Hangup %s [%s] [%s]", ch.name(), prevState, cause))
	ch.logCallState()
	if ch.Direction == DirectionInbound {
		v := ch.Vars()
		c := ch.core
		c.recordEvent("call", fmt.Sprintf("call from %s to %s ended: %s, %ss talk", v["caller_id_number"], v["destination_number"], cause, v["billsec"]))
	}

	c := ch.core
	switch {
	case ch.Direction == DirectionInbound && !answered:
		if how != hangupCancelled && ch.inviteTx != nil && !ch.inviteTx.FinalSent() {
			resp := ch.inviteResponse(cause.SIPCode(), "")
			resp.Add("Reason", reasonHeader(cause))
			_ = ch.inviteTx.Respond(resp)
		}
	case ch.Direction == DirectionOutbound && !answered:
		if how == hangupLocal && gotProv && ch.inviteCli != nil {
			_ = c.stack.Cancel(ch.inviteCli, reasonHeader(cause))
		}
	case answered && how == hangupLocal:
		go ch.sendBye(cause)
	}
	ch.stopRecording()
	if ch.ep != nil {
		ch.ep.Close()
	}
	ch.fire(EvChannelCallState)
	ch.fire(EvChannelHangup)
	c.removeChannel(ch)
	complete := func() {
		ch.fire(EvChannelHangupComplete)
		ch.fire(EvChannelDestroy)
	}
	if execDone != nil {
		// The call's record (made from this event) waits
		// for its dialplan to finish: a queue still ringing an agent notes
		// why the caller left only once the ringing is cancelled. Not more
		// than 3 seconds, whatever the application is doing.
		go func() {
			select {
			case <-execDone:
			case <-time.After(3 * time.Second):
			}
			complete()
		}()
	} else {
		complete()
	}
	close(ch.done)
}

// setHangupVarsLocked records the variables CDRs are made of.
func (ch *Channel) setHangupVarsLocked(cause Cause, how hangupHow) {
	v := ch.vars
	v["hangup_cause"] = cause.String()
	v["hangup_cause_q850"] = strconv.Itoa(cause.Q850())
	const stamp = "2006-01-02 15:04:05"
	epoch := func(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }
	uepoch := func(t time.Time) string { return strconv.FormatInt(t.UnixMicro(), 10) }
	v["start_stamp"], v["start_epoch"], v["start_uepoch"] = ch.Created.Format(stamp), epoch(ch.Created), uepoch(ch.Created)
	v["end_stamp"], v["end_epoch"], v["end_uepoch"] = ch.hungupAt.Format(stamp), epoch(ch.hungupAt), uepoch(ch.hungupAt)
	dur := ch.hungupAt.Sub(ch.Created)
	v["duration"] = strconv.Itoa(int(dur.Seconds()))
	v["mduration"] = strconv.FormatInt(dur.Milliseconds(), 10)
	v["billsec"], v["billmsec"], v["answer_epoch"] = "0", "0", "0"
	if ch.answered && !ch.answeredAt.IsZero() {
		bill := ch.hungupAt.Sub(ch.answeredAt)
		v["answer_stamp"], v["answer_epoch"], v["answer_uepoch"] = ch.answeredAt.Format(stamp), epoch(ch.answeredAt), uepoch(ch.answeredAt)
		v["billsec"] = strconv.Itoa(int(bill.Seconds()))
		v["billmsec"] = strconv.FormatInt(bill.Milliseconds(), 10)
	}
	if !ch.progressAt.IsZero() {
		v["progress_epoch"] = epoch(ch.progressAt)
		v["pdd_ms"] = strconv.FormatInt(ch.progressAt.Sub(ch.Created).Milliseconds(), 10)
	}
	if ch.ep != nil {
		if sc, ok := ch.ep.SendCodec(); ok {
			v["read_codec"], v["write_codec"] = sc.Name, sc.Name
			v["read_rate"], v["write_rate"] = strconv.Itoa(sc.Rate), strconv.Itoa(sc.Rate)
		}
		if r := ch.ep.Remote(); r.IsValid() {
			v["remote_media_ip"] = r.Addr().String()
		}
	}
	disp := "send_bye"
	switch {
	case how == hangupRemoteBye:
		disp = "recv_bye"
	case how == hangupCancelled:
		disp = "recv_cancel"
	case how == hangupRemoteFinal:
		disp = "recv_refuse"
	case !ch.answered && ch.Direction == DirectionInbound:
		disp = "send_refuse"
	case !ch.answered:
		disp = "send_cancel"
	}
	v["sip_hangup_disposition"] = disp
	ch.flowEndLocked(cause, how)
}

func (ch *Channel) sendBye(cause Cause) {
	if ch.Direction == DirectionInbound {
		// RFC 3261 15.1.1: wait for the ACK before sending BYE.
		select {
		case <-ch.acked:
		case <-time.After(32 * time.Second):
		}
	}
	ch.mu.Lock()
	req := ch.dlg.request("BYE")
	dst := ch.dlg.dst
	ch.mu.Unlock()
	req.Add("Reason", reasonHeader(cause))
	tx, err := ch.core.stack.Request(req, dst)
	if err != nil {
		ch.log.Warn("sending BYE failed", "err", err)
		return
	}
	for range tx.Responses() {
	}
}

// sendInDialog sends a request inside the dialog and returns the final
// response (nil on transport error).
func (ch *Channel) sendInDialog(method string, headers [][2]string, contentType string, body []byte) *sip.Message {
	ch.mu.Lock()
	req := ch.dlg.request(method)
	dst := ch.dlg.dst
	ch.mu.Unlock()
	for _, h := range headers {
		req.Add(h[0], h[1])
	}
	if contentType != "" {
		req.Add("Content-Type", contentType)
		req.Body = body
	}
	tx, err := ch.core.stack.Request(req, dst)
	if err != nil {
		return nil
	}
	var final *sip.Message
	for r := range tx.Responses() {
		final = r
	}
	return final
}

// ---------------------------------------------------------------------------
// DTMF

func (ch *Channel) queueDTMF(d rune) {
	select {
	case ch.dtmf <- d:
	default:
	}
	ch.fire(EvDTMF, "DTMF-Digit", string(d))
}

// FlushDTMF discards queued digits.
func (ch *Channel) FlushDTMF() {
	for {
		select {
		case <-ch.dtmf:
		default:
			return
		}
	}
}

// SendDTMF delivers a digit to the far end via SIP INFO.
func (ch *Channel) SendDTMF(d rune, durationMs int) {
	body := fmt.Sprintf("Signal=%c\r\nDuration=%d\r\n", d, durationMs)
	go ch.sendInDialog("INFO", nil, "application/dtmf-relay", []byte(body))
}

// Answered reports whether the channel has been answered.
func (ch *Channel) Answered() bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.answered
}
