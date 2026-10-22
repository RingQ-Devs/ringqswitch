// Package core is the RingQswitch switching core: registrar, call control
// (B2BUA), dialplan execution, applications, gateways and events.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Version is the RingQswitch version string.
var Version = "0.10.1"

const allowHeader = "INVITE, ACK, BYE, CANCEL, OPTIONS, INFO, UPDATE, REFER, NOTIFY, SUBSCRIBE"

// Core is the running switch.
type Core struct {
	prof     profileState
	log      *slog.Logger
	cfg      atomic.Pointer[config.Config]
	stack    *sip.Stack
	media    *media.Allocator
	reg      *Registrar
	events   *EventBus
	nonces   *sip.NonceManager
	guard    *guard
	apps     map[string]App
	coreUUID string
	started  time.Time
	cdr      *cdrWriter

	// transcribe says which calls are transcribed (set by the web server).
	transcribe atomic.Pointer[TranscribeFunc]
	// aiAgent runs the calls handed to AI agents (set by the web server).
	aiAgent atomic.Pointer[AIAgentFunc]

	mu       sync.RWMutex
	channels map[string]*Channel
	dialogs  map[string]*Channel
	gateways map[string]*Gateway

	reloadMu sync.Mutex
	loader   Loader

	reloadReqMu   sync.Mutex
	reloadPending bool

	cc      *callCenter
	sec     secHits
	spy     spyRooms
	trusted sync.Map // netip.AddrPort of web dialers
	elog    eventLog
	mwi     mwiState
	park    parkLot
	pres    presence // BLF lamps, subscriptions, settings changed from phones
	vmStore atomic.Pointer[VoicemailStore]
	conf    conferences

	globals globalVars // set_global / global_setvar at runtime
	hashes  kvStore    // hash and db applications

	sessionsTotal atomic.Uint64
	stopping      atomic.Bool
	stop          chan struct{}
	wg            sync.WaitGroup
}

// New creates a switch from a loaded configuration. Call Start to begin
// processing traffic.
func New(cfg *config.Config, log *slog.Logger) (*Core, error) {
	if log == nil {
		log = slog.Default()
	}
	s := cfg.Settings
	c := &Core{
		log:      log,
		reg:      newRegistrar(),
		nonces:   sip.NewNonceManager(5 * time.Minute),
		guard:    newGuard(guardConfig(cfg)),
		coreUUID: sip.NewUUID(),
		channels: map[string]*Channel{},
		dialogs:  map[string]*Channel{},
		gateways: map[string]*Gateway{},
		stop:     make(chan struct{}),
	}
	c.cfg.Store(cfg)
	c.guard.notify = c.guardNotify
	c.guard.restore(cfg.Security.Bans)
	c.cc = newCallCenter(c)
	host := cfg.Database.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	c.events = newEventBus(c.coreUUID, host, s.SIPIP)
	c.apps = builtinApps()
	alloc, err := media.NewAllocator(s.RTPBindIP, s.RTPStartPort, s.RTPEndPort, log)
	if err != nil {
		return nil, err
	}
	c.media = alloc
	for _, w := range cfg.Warnings {
		log.Warn("config", "warning", w)
	}
	return c, nil
}

// Start opens the SIP listeners and starts background tasks.
func (c *Core) Start() error {
	cfg := c.config()
	s := cfg.Settings
	stack, err := sip.NewStack(sip.Config{
		Transport:     sip.TransportConfig{ListenIP: s.SIPBindIP, Port: s.SIPPort, EnableTCP: s.EnableTCP},
		Advertise:     c.sipAdvertise,
		UserAgent:     s.UserAgent,
		Logger:        c.log,
		Accept:        c.acceptSource,
		ConnClosed:    c.connClosed,
		TraceMessages: s.SIPTrace,
	}, c)
	if err != nil {
		return err
	}
	c.stack = stack
	c.started = time.Now()
	if s.CDRPath != "" {
		w, err := newCDRWriter(s.CDRPath, c.events, c.log)
		if err != nil {
			stack.Close()
			return err
		}
		c.cdr = w
	}
	c.guard.configure(guardConfig(cfg))
	c.syncProfiles(cfg.Profiles)
	c.syncGateways(cfg.Gateways)
	c.cc.sync(cfg.Features)
	c.syncMWI(cfg.Features)
	c.wg.Add(4)
	go c.housekeeping()
	go c.heartbeat()
	go c.presenceLoop()
	go func() { defer c.wg.Done(); c.cc.run(c.stop) }()
	c.recordEvent("system", "RingQswitch "+Version+" started")
	c.log.Info("RingQswitch started", "version", Version, "sip", fmt.Sprintf("%s:%d", s.SIPIP, stack.Port()),
		"tcp", s.EnableTCP, "rtp", fmt.Sprintf("%d-%d", s.RTPStartPort, s.RTPEndPort), "domain", s.Domain)
	return nil
}

// Stop hangs up all calls, unregisters gateways and closes listeners.
func (c *Core) Stop() {
	if !c.stopping.CompareAndSwap(false, true) {
		return
	}
	c.events.Publish(NewEvent(EvShutdown, ""))
	for _, ch := range c.Channels() {
		ch.Hangup(CauseSystemShutdown)
	}
	var wg sync.WaitGroup
	for _, gw := range c.gatewayList() {
		wg.Add(1)
		go func() { defer wg.Done(); gw.stop() }()
	}
	// A provider that does not answer would hold a restart for a whole SIP
	// transaction (32 s): wait at most 5 seconds for the gateways.
	gwDone := make(chan struct{})
	go func() { wg.Wait(); close(gwDone) }()
	select {
	case <-gwDone:
	case <-time.After(5 * time.Second):
		c.log.Warn("a gateway's provider did not answer; stopping without waiting for it")
	}
	close(c.stop)
	c.wg.Wait()
	// Give BYEs a moment to leave.
	time.Sleep(200 * time.Millisecond)
	if c.stack != nil {
		c.stack.Close()
	}
	if c.cdr != nil {
		c.cdr.close()
	}
}

func (c *Core) config() *config.Config { return c.cfg.Load() }

// Config returns the current configuration (read-only).
func (c *Core) Config() *config.Config { return c.cfg.Load() }

// Events returns the event bus.
func (c *Core) Events() *EventBus { return c.events }

// SIPPort returns the bound SIP port.
func (c *Core) SIPPort() int { return c.stack.Port() }

// Registrar returns the registrar.
func (c *Core) Registrar() *Registrar { return c.reg }

// Loader builds a configuration from a configuration directory. The
// default reads the XML files; the database integration adds its data.
type Loader func(dir string) (*config.Config, error)

// SetLoader replaces the loader used by Reload.
func (c *Core) SetLoader(l Loader) {
	c.reloadMu.Lock()
	c.loader = l
	c.reloadMu.Unlock()
}

// Reload rebuilds the configuration: directory, dialplan, gateways, ACLs,
// variables and features. Listener and RTP settings require a restart.
func (c *Core) Reload() error {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	old := c.config()
	load := c.loader
	if load == nil {
		load = config.Load
	}
	cfg, err := load(old.Dir)
	if err != nil {
		return err
	}
	// Keep settings that cannot change at runtime.
	cfg.Settings.SIPBindIP, cfg.Settings.SIPPort, cfg.Settings.EnableTCP = old.Settings.SIPBindIP, old.Settings.SIPPort, old.Settings.EnableTCP
	cfg.Settings.RTPBindIP, cfg.Settings.RTPStartPort, cfg.Settings.RTPEndPort = old.Settings.RTPBindIP, old.Settings.RTPStartPort, old.Settings.RTPEndPort
	cfg.Database = old.Database
	c.cfg.Store(cfg)
	c.guard.configure(guardConfig(cfg))
	c.syncProfiles(cfg.Profiles)
	c.syncGateways(cfg.Gateways)
	c.cc.sync(cfg.Features)
	c.syncMWI(cfg.Features)
	c.reconcileUserVars(cfg)
	c.recordEvent("system", "configuration reloaded")
	seen := map[string]bool{}
	for _, w := range old.Warnings {
		seen[w] = true
	}
	for _, w := range cfg.Warnings {
		if !seen[w] { // repeated warnings were logged at the previous load
			c.log.Warn("config", "warning", w)
		}
	}
	c.events.Publish(NewEvent(EvReloadXML, ""))
	c.log.Info("configuration reloaded", "users", cfg.Directory.UserCount(), "contexts", len(cfg.Dialplan.Contexts))
	return nil
}

func isPublic(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return false
	}
	return !netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// sipAdvertise picks the address for Via/Contact towards dst.
func (c *Core) sipAdvertise(dst sip.Addr) (string, int) {
	s := c.config().Settings
	host := s.SIPIP
	if s.ExtSIPIP != "" && isPublic(dst.AP.Addr()) {
		host = s.ExtSIPIP
	}
	if dst.AP.Addr().IsLoopback() && s.SIPBindIP == "" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if dst.Local != 0 { // a second SIP profile's port
		return host, int(dst.Local)
	}
	return host, c.stack.Port()
}

// rtpAdvertise picks the address for SDP towards a peer.
func (c *Core) rtpAdvertise(peer netip.Addr) string {
	s := c.config().Settings
	if s.ExtRTPIP != "" && isPublic(peer) {
		return s.ExtRTPIP
	}
	if peer.IsLoopback() && s.RTPBindIP == "" {
		return "127.0.0.1"
	}
	return s.RTPIP
}

func (c *Core) contactFor(user string, dst sip.Addr) string {
	host, port := c.sipAdvertise(dst)
	u := &sip.URI{Scheme: "sip", User: user, Host: host, Port: port}
	if dst.Transport == "tcp" {
		u.Params.Set("transport", "tcp")
	}
	return "<" + u.String() + ">"
}

// guardConfig is SIP Guard's configuration (the defaults until changed).
func guardConfig(cfg *config.Config) config.Guard {
	if cfg.Security.Guard != nil {
		return *cfg.Security.Guard
	}
	return config.DefaultGuard(cfg.Settings)
}

// acceptSource drops traffic from denied and banned addresses (and, with
// the global IP restriction, from addresses not allowed), logging it so a
// blocked phone is easy to spot.
func (c *Core) acceptSource(src sip.Addr) bool {
	ip := src.AP.Addr()
	if c.deniedSource(ip) {
		return false
	}
	if c.config().Security.RestrictSIP && !c.allowedSource(src) {
		if c.guard.dropNotice(ip) {
			c.log.Warn("ignoring SIP from an address that is not allowed (global IP restriction, Security settings → Anti-hacking)", "ip", ip)
		}
		return false
	}
	b := c.guard.banned(ip, "sip")
	if b == nil {
		return true
	}
	if c.guard.dropNotice(ip) {
		c.log.Warn("ignoring SIP from banned address (SIP Guard)", "ip", ip, "protection", b.Jail,
			"banned_until", b.Until.Format(time.RFC3339), "unban", "rq_cli -x \"unblock "+ip.String()+"\"")
	}
	return false
}

// allowedSource reports whether an address may send SIP when the global
// IP restriction is on: allow rules, gateways, trusted carriers, this
// server and the web dialers.
func (c *Core) allowedSource(src sip.Addr) bool {
	ip := src.AP.Addr().Unmap()
	if ip.IsLoopback() {
		return true
	}
	if _, ok := c.trusted.Load(src.AP); ok {
		return true
	}
	cfg := c.config()
	if _, ok := prefixMatch(cfg.Security.Allow, ip); ok {
		return true
	}
	return c.gatewayBySource(ip) != nil || cfg.MatchACL(ip) != nil
}

func (c *Core) connClosed(a sip.Addr) {
	for _, b := range c.reg.removeBySource(a) {
		c.fireRegEvent("ringq::unregister", b, "tcp connection closed")
	}
}

// ---------------------------------------------------------------------------
// Channel bookkeeping

func (c *Core) addChannel(ch *Channel) {
	c.mu.Lock()
	c.channels[ch.UUID] = ch
	ch.mu.Lock()
	if ch.dlg.localTag != "" {
		c.dialogs[ch.dlg.key()] = ch
	}
	ch.mu.Unlock()
	c.mu.Unlock()
	c.sessionsTotal.Add(1)
}

func (c *Core) removeChannel(ch *Channel) {
	ch.notice(fmt.Sprintf("Close Channel %s [CS_DESTROY]", ch.name()))
	c.mu.Lock()
	delete(c.channels, ch.UUID)
	ch.mu.Lock()
	key := ch.dlg.key()
	ch.mu.Unlock()
	if c.dialogs[key] == ch {
		delete(c.dialogs, key)
	}
	c.mu.Unlock()
}

func (c *Core) findDialog(callID, localTag string) *Channel {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dialogs[callID+"|"+localTag]
}

// Channel returns a channel by UUID.
func (c *Core) Channel(uuid string) *Channel {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.channels[uuid]
}

// Channels returns all live channels ordered by creation time.
func (c *Core) Channels() []*Channel {
	c.mu.RLock()
	out := make([]*Channel, 0, len(c.channels))
	for _, ch := range c.channels {
		out = append(out, ch)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (c *Core) channelCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.channels)
}

// ---------------------------------------------------------------------------
// SIP handler

// OnRequest implements sip.Handler.
func (c *Core) OnRequest(tx *sip.ServerTx) {
	req := tx.Request
	if c.stopping.Load() && req.Method != "BYE" && req.Method != "ACK" {
		_ = tx.Respond(sip.NewResponse(req, 503, ""))
		return
	}
	if c.scannerUA(req) {
		return // no answer: scanners learn nothing
	}
	to, err := req.To()
	if err != nil {
		_ = tx.Respond(sip.NewResponse(req, 400, "Bad To Header"))
		return
	}
	inDialog := to.Tag() != ""
	switch req.Method {
	case "REGISTER":
		c.handleRegister(tx)
	case "INVITE":
		if inDialog {
			c.handleInDialog(tx)
		} else {
			c.handleInvite(tx)
		}
	case "CANCEL":
		c.handleCancel(tx)
	case "OPTIONS":
		if inDialog && c.findDialog(req.CallID(), to.Tag()) != nil {
			c.handleInDialog(tx)
			return
		}
		resp := sip.NewResponse(req, 200, "")
		resp.Add("Allow", allowHeader)
		resp.Add("Accept", "application/sdp")
		resp.Add("Supported", "replaces")
		_ = tx.Respond(resp)
	case "BYE", "INFO", "UPDATE", "REFER", "PRACK":
		if !inDialog {
			_ = tx.Respond(sip.NewResponse(req, 481, ""))
			return
		}
		c.handleInDialog(tx)
	case "NOTIFY":
		if inDialog {
			c.handleInDialog(tx)
			return
		}
		// Some trunks send out-of-dialog NOTIFYs (keep-alives, check-sync).
		_ = tx.Respond(sip.NewResponse(req, 200, ""))
	case "SUBSCRIBE":
		c.handleSubscribe(tx)
	case "PUBLISH":
		resp := sip.NewResponse(req, 489, "")
		resp.Add("Allow-Events", allowEvents)
		_ = tx.Respond(resp)
	default:
		resp := sip.NewResponse(req, 405, "")
		resp.Add("Allow", allowHeader)
		_ = tx.Respond(resp)
	}
}

// OnAck implements sip.Handler.
func (c *Core) OnAck(req *sip.Message) {
	to, err := req.To()
	if err != nil {
		return
	}
	if ch := c.findDialog(req.CallID(), to.Tag()); ch != nil {
		ch.onAck(req)
	}
}

// OnStrayResponse implements sip.Handler.
func (c *Core) OnStrayResponse(resp *sip.Message) {
	c.log.Debug("stray response", "resp", resp.Short(), "call-id", resp.CallID())
}

// ---------------------------------------------------------------------------
// Background tasks

func (c *Core) housekeeping() {
	defer c.wg.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
		for _, b := range c.reg.expire(time.Now()) {
			c.log.Info("registration expired", "user", b.User, "contact", b.Contact)
			c.recordEvent("registration", b.AOR()+" registration expired")
			c.fireRegEvent("ringq::expire", b, "expired")
		}
		c.guard.sweep()
		c.checkMediaTimeouts()
	}
}

// noRTPAfter is how long an answered call may go without receiving any
// audio from a leg before the log says so (the classic one-way audio).
var noRTPAfter = 8 * time.Second

func (c *Core) checkMediaTimeouts() {
	timeout := c.config().Settings.RTPTimeout
	now := time.Now()
	for _, ch := range c.Channels() {
		ch.mu.Lock()
		answered, since, ep, remote, warned := ch.answered, ch.answeredAt, ch.ep, ch.remoteSDP, ch.noRTPWarn
		ch.mu.Unlock()
		if !answered || ep == nil || now.Sub(since) < noRTPAfter {
			continue
		}
		if remote != nil {
			if a, _ := remote.Audio(); a == nil || remote.Direction(a) != "sendrecv" {
				continue // held or one-way streams legitimately go quiet
			}
		}
		last := ep.LastRx()
		if last.IsZero() && !warned {
			ch.mu.Lock()
			ch.noRTPWarn = true
			ch.mu.Unlock()
			sent := ep.Remote()
			ch.log.Warn("no audio received on this call leg: its RTP does not reach the server (one-way audio)",
				"from", ch.Var("channel_name"), "sending_to", sent.String(), "rtp_port", ep.Port,
				"check", "open UDP "+c.rtpRange()+" in the server and cloud firewall; set ext-rtp-ip when the server is behind NAT; turn off SIP ALG on the phone's router")
			c.recordEvent("call", fmt.Sprintf("one-way audio: no RTP from %s (we send to %s, our port %d): check the firewall for UDP %s",
				ch.Var("channel_name"), sent, ep.Port, c.rtpRange()))
		}
		if timeout <= 0 || now.Sub(since) < timeout {
			continue
		}
		if last.IsZero() {
			last = since
		}
		if now.Sub(last) > timeout {
			ch.log.Warn("media timeout", "silent_for", now.Sub(last).Round(time.Second))
			ch.Hangup(CauseMediaTimeout)
		}
	}
}

func (c *Core) rtpRange() string {
	s := c.config().Settings
	return fmt.Sprintf("%d-%d", s.RTPStartPort, s.RTPEndPort)
}

func (c *Core) heartbeat() {
	defer c.wg.Done()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
		e := NewEvent(EvHeartbeat, "")
		e.Add("Up-Time", time.Since(c.started).Round(time.Second).String())
		e.Add("Session-Count", fmt.Sprint(c.channelCount()))
		e.Add("Session-Since-Startup", fmt.Sprint(c.sessionsTotal.Load()))
		e.Add("Registrations", fmt.Sprint(c.reg.count()))
		c.events.Publish(e)
	}
}

// Wait blocks until ctx is done (helper for embedding).
func (c *Core) Wait(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-c.stop:
	}
}
