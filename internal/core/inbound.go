package core

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sdp"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

func unescapeUser(u string) string {
	if v, err := url.PathUnescape(u); err == nil {
		return v
	}
	return u
}

// handleInvite processes a dialog-creating INVITE.
func (c *Core) handleInvite(tx *sip.ServerTx) {
	req := tx.Request
	if mf := req.Get("Max-Forwards"); mf != "" {
		if n, err := strconv.Atoi(mf); err == nil && n <= 0 {
			_ = tx.Respond(sip.NewResponse(req, 483, ""))
			return
		}
	}
	cfg := c.config()
	s := cfg.Settings
	if s.MaxSessions > 0 && c.channelCount() >= s.MaxSessions {
		c.log.Warn("rejecting call: max-sessions reached", "max", s.MaxSessions)
		_ = tx.Respond(sip.NewResponse(req, 503, "Maximum Calls In Progress"))
		return
	}
	from, err := req.From()
	if err != nil || from.Tag() == "" {
		_ = tx.Respond(sip.NewResponse(req, 400, "Bad From Header"))
		return
	}
	to, _ := req.To()
	_ = tx.Respond(sip.NewResponse(req, 100, ""))

	vars := map[string]string{}
	context := "public"
	src := req.Src.AP.Addr()
	gw := c.gatewayByContact(req.RequestURI.User)
	if gw == nil {
		gw = c.gatewayBySource(src)
	}
	if gw != nil {
		context = gw.cfg.Context
		vars["gateway_name"] = gw.cfg.Name
		vars["sip_gateway_name"] = gw.cfg.Name
		vars["gateway_label"] = firstNonEmpty(gw.cfg.Label, gw.cfg.Name)
		if gw.cfg.UUID != "" {
			vars["gateway_uuid"] = gw.cfg.UUID
		}
		switch {
		case gw.cfg.Extension != "":
			vars["destination_number"] = gw.cfg.Extension
		case strings.HasPrefix(req.RequestURI.User, "gw+"):
			// Called at our registration contact: the number is in To.
			vars["destination_number"] = unescapeUser(to.URI.User)
		}
	} else if acl := cfg.MatchACL(src); acl != nil {
		context = acl.Context
		vars["acl_name"] = acl.Name
	} else if s.AuthCalls {
		user, res := c.checkAuth(req, "Proxy-Authorization")
		switch res {
		case authMissing, authStale:
			c.challenge(tx, true, res == authStale)
			return
		case authBad, authUnknown:
			c.authFailed(tx, from.URI.User, res)
			return
		}
		context = "default"
		for k, v := range user.Vars {
			vars[k] = v
		}
		if uc := user.Vars["user_context"]; uc != "" {
			context = uc
		}
		vars["user_name"] = user.ID
		vars["domain_name"] = user.Domain
		vars["dialed_domain"] = user.Domain
		vars["sip_authorized"] = "true"
		vars["presence_id"] = user.ID + "@" + user.Domain
	}

	// SIP Invite Protection: too many calls from one address.
	if gw == nil && vars["acl_name"] == "" && !c.neverBlock(req.Src) && !c.neverBan(src, "sip") {
		if banned, _ := c.guard.fail(config.JailSIPInvite, src, "too many calls"); banned {
			_ = tx.Respond(sip.NewResponse(req, 403, ""))
			return
		}
	}

	var offer *sdp.Session
	if len(req.Body) > 0 {
		if req.ContentType() != "application/sdp" {
			_ = tx.Respond(sip.NewResponse(req, 415, ""))
			return
		}
		if offer, err = sdp.Parse(req.Body); err != nil {
			c.log.Debug("bad sdp", "err", err)
			_ = tx.Respond(sip.NewResponse(req, 400, "Bad SDP"))
			return
		}
		if a, _ := offer.Audio(); a == nil || strings.Contains(a.Proto, "SAVP") {
			_ = tx.Respond(sip.NewResponse(req, 488, ""))
			return
		}
	}

	ep, err := c.media.Allocate()
	if err != nil {
		c.log.Error("rtp allocation failed", "err", err)
		_ = tx.Respond(sip.NewResponse(req, 503, "No Media Ports"))
		return
	}

	ch := c.newChannel(DirectionInbound)
	ch.ep = ep
	ch.inviteTx = tx
	ch.offer = offer
	seq, _ := req.CSeq()
	localURI := to.Clone()
	localURI.Params = nil
	remoteURI := from.Clone()
	remoteURI.Params = nil
	target := from.URI.Clone()
	if ct, err := req.Contact(); err == nil && !ct.Wildcard {
		target = ct.URI.Clone()
	}
	destUser := req.RequestURI.User
	if destUser == "" {
		destUser = to.URI.User
	}
	ch.dlg = dialog{
		callID: req.CallID(), localTag: sip.NewTag(), remoteTag: from.Tag(),
		localURI: localURI, remoteURI: remoteURI, remoteTarget: target,
		routeSet: req.GetAll("Record-Route"), remoteSeq: seq, localSeq: 1,
		dst: req.Src, contact: c.contactFor(unescapeUser(destUser), req.Src),
	}
	cidName := from.Display
	if cidName == "" {
		cidName = from.URI.User
	}
	v := ch.vars
	v["destination_number"] = unescapeUser(destUser)
	v["caller_destination"] = unescapeUser(destUser)
	v["caller_id_number"] = unescapeUser(from.URI.User)
	v["caller_id_name"] = cidName
	v["context"] = context
	v["sip_from_user"] = from.URI.User
	v["sip_from_host"] = from.URI.Host
	v["sip_to_user"] = to.URI.User
	v["sip_to_host"] = to.URI.Host
	v["sip_req_uri"] = req.RequestURI.String()
	v["sip_call_id"] = req.CallID()
	v["sip_user_agent"] = req.Get("User-Agent")
	v["sip_transport"] = req.Src.Transport
	v["network_addr"] = src.String()
	v["network_port"] = strconv.Itoa(int(req.Src.AP.Port()))
	v["channel_name"] = fmt.Sprintf("sip/%s@%s", from.URI.User, src)
	for k, val := range vars {
		v[k] = val
	}
	ep.OnDTMF(ch.queueDTMF)
	if offer != nil {
		ch.applyRemote(offer)
	}
	c.addChannel(ch)
	ch.notice(fmt.Sprintf("New Channel %s [%s]", v["channel_name"], ch.UUID))
	logAt(ch.log, slog.LevelInfo, fmt.Sprintf("%s receiving invite from %s version: %s call-id: %s", v["channel_name"], req.Src.AP, v["sip_user_agent"], req.CallID()))
	ch.log.Info("new inbound call", "from", v["caller_id_number"], "to", v["destination_number"], "context", context, "src", req.Src.String())
	c.recordEvent("call", fmt.Sprintf("call from %s to %s (%s)", v["caller_id_number"], v["destination_number"], context))
	ch.fire(EvChannelCreate)
	// A BLF key picking up a ringing extension by its dialog (an INVITE with
	// Replaces, as some phones send it): the caller is connected here.
	if v["sip_authorized"] == "true" && req.Get("Replaces") != "" {
		if caller := c.ringingByDialog(req.Get("Replaces")); caller != nil {
			ch.log.Info("BLF pickup (Replaces)", "caller", caller.Var("caller_id_number"))
			c.runExecutorWith(ch, []dialplan.Action{{App: "intercept", Data: caller.UUID}})
			return
		}
	}
	c.runExecutor(ch)
}

// ringingByDialog finds the caller of a ringing leg named by a Replaces
// header value (call-id;to-tag=..;from-tag=..).
func (c *Core) ringingByDialog(value string) *Channel {
	rep, ok := parseReplaces("Replaces=" + value)
	if !ok {
		return nil
	}
	leg := c.findDialog(rep.callID, rep.toTag)
	if leg == nil {
		leg = c.findDialog(rep.callID, rep.fromTag)
	}
	if leg == nil || leg.Direction != DirectionOutbound || leg.IsAnswered() || leg.IsHungUp() {
		return nil
	}
	if caller := c.Channel(leg.Var("originator")); caller != nil && !caller.IsAnswered() && !caller.IsHungUp() {
		return caller
	}
	return nil
}

func (c *Core) handleCancel(tx *sip.ServerTx) {
	req := tx.Request
	inv := c.stack.FindInviteTx(req)
	if inv == nil {
		_ = tx.Respond(sip.NewResponse(req, 481, ""))
		return
	}
	_ = tx.Respond(sip.NewResponse(req, 200, ""))
	var ch *Channel
	c.mu.RLock()
	for _, candidate := range c.channels {
		if candidate.inviteTx == inv {
			ch = candidate
			break
		}
	}
	c.mu.RUnlock()
	if ch == nil {
		// The INVITE is still being authenticated/processed; reject it.
		resp := sip.NewResponse(inv.Request, 487, "")
		resp.SetToTag(sip.NewTag())
		_ = inv.Respond(resp)
		return
	}
	if ch.IsAnswered() {
		return
	}
	resp := ch.inviteResponse(487, "")
	_ = inv.Respond(resp)
	ch.hangup(CauseOriginatorCancel, hangupCancelled)
}

// handleInDialog processes requests inside an existing dialog.
func (c *Core) handleInDialog(tx *sip.ServerTx) {
	req := tx.Request
	to, _ := req.To()
	ch := c.findDialog(req.CallID(), to.Tag())
	if ch == nil {
		_ = tx.Respond(sip.NewResponse(req, 481, ""))
		return
	}
	seq, _ := req.CSeq()
	ch.mu.Lock()
	if seq != 0 && seq < ch.dlg.remoteSeq && req.Method != "ACK" {
		ch.mu.Unlock()
		_ = tx.Respond(sip.NewResponse(req, 500, "CSeq Out Of Order"))
		return
	}
	ch.dlg.remoteSeq = seq
	ch.mu.Unlock()

	switch req.Method {
	case "BYE":
		_ = tx.Respond(sip.NewResponse(req, 200, ""))
		cause := CauseNormalClearing
		if r := req.Get("Reason"); strings.Contains(r, "cause=") {
			_, rest, _ := strings.Cut(r, "cause=")
			num, _, _ := strings.Cut(rest, ";")
			if n, err := strconv.Atoi(strings.TrimSpace(num)); err == nil && n > 0 && n < 128 {
				cause = Cause(n)
			}
		}
		ch.log.Info("remote BYE", "name", ch.name())
		ch.hangup(cause, hangupRemoteBye)
	case "INVITE", "UPDATE":
		c.handleReinvite(ch, tx)
	case "INFO":
		c.handleInfo(ch, tx)
	case "REFER":
		c.handleRefer(ch, tx)
	default: // NOTIFY, OPTIONS, PRACK
		_ = tx.Respond(sip.NewResponse(req, 200, ""))
	}
}

// acceptT38 takes a re-INVITE to T.38 (a carrier's fax gateway heard the
// fax tones) while rxfax or txfax runs, unless fax_enable_t38=false: the
// fax goes on over UDPTL, on the RTP port. Otherwise the caller refuses
// it (488) and the fax stays audio.
func (c *Core) acceptT38(ch *Channel, tx *sip.ServerTx, resp *sip.Message, offer *sdp.Session, img *sdp.Media) bool {
	ch.mu.Lock()
	hook := ch.faxT38
	ch.mu.Unlock()
	switch v := ch.Var("fax_enable_t38"); {
	case hook == nil:
		ch.log.Info("re-INVITE to T.38 refused (488): no fax is running on this call")
		return false
	case v != "" && !truthy(v):
		ch.log.Info("re-INVITE to T.38 refused (488): fax_enable_t38=false, the fax continues as audio")
		return false
	}
	remote, ok := ch.mediaAddr(offer, img)
	if !ok {
		ch.log.Info("re-INVITE to T.38 refused (488): no address in the offer")
		return false
	}
	ans, ok := hook(remote, img.T38())
	if !ok {
		return false
	}
	ch.mu.Lock()
	ch.sdpVersion++
	ch.t38Media = true
	local := ch.localLocked()
	ch.mu.Unlock()
	resp.Add("Content-Type", "application/sdp")
	resp.Body = sdp.AnswerT38(offer, local, ans).Marshal()
	_ = tx.Respond(resp)
	return true
}

// handleReinvite answers target refreshes and hold/unhold locally, keeping
// our media address stable so the other leg is unaffected.
func (c *Core) handleReinvite(ch *Channel, tx *sip.ServerTx) {
	req := tx.Request
	if ct, err := req.Contact(); err == nil && !ct.Wildcard {
		ch.mu.Lock()
		ch.dlg.remoteTarget = ct.URI.Clone()
		ch.mu.Unlock()
	}
	resp := sip.NewResponse(req, 200, "")
	ch.mu.Lock()
	resp.Add("Contact", ch.dlg.contact)
	local := ch.localSDP
	ch.mu.Unlock()
	resp.Add("Allow", allowHeader)
	if !req.HasSDP() {
		if req.Method == "INVITE" && local != nil {
			// Offer-less re-INVITE: we offer our current SDP; the answer comes
			// in the ACK.
			resp.Add("Content-Type", "application/sdp")
			resp.Body = local.Marshal()
			ch.mu.Lock()
			ch.lateOffer = true
			ch.mu.Unlock()
		}
		_ = tx.Respond(resp)
		return
	}
	offer, err := sdp.Parse(req.Body)
	if err != nil {
		_ = tx.Respond(sip.NewResponse(req, 488, ""))
		return
	}
	img, _ := offer.Image()
	if img != nil && c.acceptT38(ch, tx, resp, offer, img) {
		return
	}
	a, _ := offer.Audio()
	if a == nil || local == nil {
		if img == nil {
			ch.log.Info("re-INVITE without audio refused (488)")
		}
		_ = tx.Respond(sip.NewResponse(req, 488, ""))
		return
	}
	la, _ := local.Audio()
	var codecs []sdp.Codec
	if la != nil {
		codecs = sdp.MapToOffer(a.Codecs(), la.Codecs())
	}
	if len(codecs) == 0 {
		_ = tx.Respond(sip.NewResponse(req, 488, ""))
		return
	}
	dir := sdp.ReverseDirection(offer.Direction(a))
	ch.mu.Lock()
	prevDir := ""
	if la != nil {
		prevDir = local.Direction(la)
	}
	wasT38 := ch.t38Media
	ch.t38Media = false
	if dir != prevDir || wasT38 {
		ch.sdpVersion++
	}
	answer := sdp.Answer(offer, ch.localLocked(), codecs, dir)
	ch.localSDP = answer
	ch.mu.Unlock()
	if wasT38 {
		ch.log.Info("the far end switched the media back from T.38 to audio")
		ch.ep.SetRaw(nil)
	}
	ch.applyRemote(offer)
	ch.setCodecs(codecs)
	if dir != prevDir {
		ch.log.Info("media direction changed", "direction", dir)
		// The phone put the call on hold (or resumed it): music on hold for
		// the other party.
		go c.setHold(ch, dir == "recvonly" || dir == "inactive")
	}
	resp.Add("Content-Type", "application/sdp")
	resp.Body = answer.Marshal()
	_ = tx.Respond(resp)
}

func (c *Core) handleInfo(ch *Channel, tx *sip.ServerTx) {
	req := tx.Request
	_ = tx.Respond(sip.NewResponse(req, 200, ""))
	var digit rune
	switch req.ContentType() {
	case "application/dtmf-relay":
		for _, line := range strings.Split(string(req.Body), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "Signal") {
				if v = strings.TrimSpace(v); v != "" {
					digit = rune(v[0])
				}
			}
		}
	case "application/dtmf":
		if b := strings.TrimSpace(string(req.Body)); b != "" {
			digit = rune(b[0])
			if b == "10" {
				digit = '*'
			} else if b == "11" {
				digit = '#'
			}
		}
	}
	if digit == 0 || !strings.ContainsRune("0123456789*#ABCDabcd", digit) {
		return
	}
	if p := ch.Partner(); p != nil {
		p.SendDTMF(digit, 250)
		return
	}
	ch.queueDTMF(digit)
}

// handleRefer implements blind transfer (RFC 3515). Attended transfer
// (Replaces) is not supported yet.
func (c *Core) handleRefer(ch *Channel, tx *sip.ServerTx) {
	req := tx.Request
	referTo, err := sip.ParseAddress(req.Get("Refer-To"))
	if err != nil || referTo.URI.User == "" {
		_ = tx.Respond(sip.NewResponse(req, 400, "Bad Refer-To"))
		return
	}
	if strings.Contains(strings.ToLower(referTo.URI.Headers), "replaces") {
		rep, ok := parseReplaces(referTo.URI.Headers)
		if !ok {
			_ = tx.Respond(sip.NewResponse(req, 400, "Bad Replaces"))
			return
		}
		c.attendedTransfer(ch, tx, rep, unescapeUser(referTo.URI.User))
		return
	}
	target := ch.Partner()
	if target == nil {
		_ = tx.Respond(sip.NewResponse(req, 603, "Nothing To Transfer"))
		return
	}
	resp := sip.NewResponse(req, 202, "")
	ch.mu.Lock()
	resp.Add("Contact", ch.dlg.contact)
	ch.mu.Unlock()
	_ = tx.Respond(resp)

	dest := unescapeUser(referTo.URI.User)
	context := ch.Var("user_context")
	if context == "" {
		context = ch.Var("context")
	}
	if context == "" || context == "public" {
		context = "default"
	}
	ch.log.Info("blind transfer", "transferee", target.UUID, "dest", dest, "context", context)
	ch.SetVar("transfer_to", dest)
	// Report success before moving the call so the transferor's BYE follows
	// the NOTIFY; do not let an unresponsive phone stall the transfer.
	notified := make(chan struct{})
	go func() {
		ch.sendInDialog("NOTIFY", [][2]string{{"Event", "refer"}, {"Subscription-State", "terminated;reason=noresource"}},
			"message/sipfrag;version=2.0", []byte("SIP/2.0 200 OK\r\n"))
		close(notified)
	}()
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
	}
	target.SetVar("transfer_source", ch.UUID)
	target.SetVar("transfer_source_user", ch.Var("user_name"))
	c.transfer(target, dest, context)
	ch.Hangup(CauseBlindTransfer)
}
