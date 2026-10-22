package core

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Gateway states.
const (
	GwNoReg    = "NOREG"
	GwTrying   = "TRYING"
	GwReged    = "REGED"
	GwFailed   = "FAIL_WAIT"
	GwUnreged  = "UNREGED"
	gwCacheTTL = time.Minute
)

// Gateway is a running SIP trunk.
type Gateway struct {
	core *Core
	cfg  config.Gateway

	mu        sync.Mutex
	state     string
	lastErr   string
	addrs     map[string]cachedAddr // by host:port
	callID    string
	fromTag   string
	cseq      uint32
	expiresAt time.Time

	stopCh chan struct{}
	done   chan struct{}
}

type cachedAddr struct {
	addr sip.Addr
	at   time.Time
}

func newGateway(c *Core, cfg config.Gateway) *Gateway {
	return &Gateway{core: c, cfg: cfg, state: GwNoReg, callID: sip.NewCallID(), fromTag: sip.NewTag(),
		addrs: map[string]cachedAddr{}, stopCh: make(chan struct{}), done: make(chan struct{})}
}

// authUser is the digest username.
func (g *Gateway) authUser() string { return firstNonEmpty(g.cfg.AuthUsername, g.cfg.Username) }

// contactUser is the user part of our registration contact:
// "gw+<name>" lets calls sent to the contact be recognised as
// coming from this gateway whatever address they come from.
func (g *Gateway) contactUser() string { return "gw+" + g.cfg.Name }

func (g *Gateway) start() {
	if _, err := g.resolve(); err != nil {
		g.core.log.Warn("gateway address unresolved", "gateway", g.cfg.Name, "err", err)
	}
	if g.cfg.Proxy != g.sendTo() {
		_, _ = g.resolveHost(g.cfg.Proxy) // to recognise calls from the proxy too
	}
	if !g.cfg.Register {
		close(g.done)
		return
	}
	go g.registerLoop()
}

func (g *Gateway) stop() {
	close(g.stopCh)
	<-g.done
	g.mu.Lock()
	reged := g.state == GwReged
	g.mu.Unlock()
	if reged {
		if _, err := g.register(0); err == nil {
			g.core.log.Info("gateway unregistered", "gateway", g.cfg.Name)
		}
	}
}

// hostPort is the request-URI host of calls through the gateway.
func (g *Gateway) hostPort() (string, int) { return splitHostPort(g.cfg.Proxy) }

// sendTo is where calls go: the outbound proxy, else the proxy.
func (g *Gateway) sendTo() string { return firstNonEmpty(g.cfg.OutboundProxy, g.cfg.Proxy) }

// registerTo is where REGISTERs go.
func (g *Gateway) registerTo() string {
	return firstNonEmpty(g.cfg.RegisterProxy, g.cfg.OutboundProxy, g.cfg.Proxy)
}

// resolve returns the address calls are sent to.
func (g *Gateway) resolve() (sip.Addr, error) { return g.resolveHost(g.sendTo()) }

// fromProfile sends from the port of the gateway's SIP profile.
func (g *Gateway) fromProfile(a sip.Addr) sip.Addr {
	if a.Transport == "udp" {
		a.Local = g.core.profileLocal(g.cfg.Profile)
	}
	return a
}

// resolveHost resolves host[:port], cached for a minute.
func (g *Gateway) resolveHost(hostport string) (sip.Addr, error) {
	g.mu.Lock()
	if c, ok := g.addrs[hostport]; ok && c.addr.IsValid() && time.Since(c.at) < gwCacheTTL {
		g.mu.Unlock()
		return g.fromProfile(c.addr), nil
	}
	g.mu.Unlock()
	host, port := splitHostPort(hostport)
	a, err := sip.Resolve(host, port, g.cfg.Transport)
	if err != nil {
		return sip.Addr{}, err
	}
	g.mu.Lock()
	g.addrs[hostport] = cachedAddr{a, time.Now()}
	g.mu.Unlock()
	return g.fromProfile(a), nil
}

func splitHostPort(hostport string) (string, int) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.TrimPrefix(hostport, "sip:"), 0
	}
	port, _ := strconv.Atoi(portStr)
	return strings.TrimPrefix(host, "sip:"), port
}

func (g *Gateway) matches(ip netip.Addr) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.addrs {
		if c.addr.IsValid() && c.addr.AP.Addr() == ip.Unmap() {
			return true
		}
	}
	return false
}

func (g *Gateway) registerLoop() {
	defer close(g.done)
	prevState := ""
	for {
		g.mu.Lock()
		g.state = GwTrying
		g.mu.Unlock()
		granted, err := g.register(g.cfg.Expire)
		wait := time.Duration(g.cfg.RetrySeconds) * time.Second
		g.mu.Lock()
		if err != nil {
			g.state, g.lastErr = GwFailed, err.Error()
			g.core.log.Warn("gateway registration failed", "gateway", g.cfg.Name, "err", err)
		} else {
			if g.state != GwReged {
				g.core.log.Info("gateway registered", "gateway", g.cfg.Name, "expires", granted)
			}
			g.state, g.lastErr = GwReged, ""
			g.expiresAt = time.Now().Add(time.Duration(granted) * time.Second)
			wait = max(time.Duration(float64(granted)*0.8)*time.Second, 10*time.Second)
		}
		g.mu.Unlock()
		if st := g.State(); st != prevState {
			c := g.core
			label := firstNonEmpty(g.cfg.Label, g.cfg.Name)
			if st == GwReged {
				c.recordEvent("gateway", "gateway "+label+" registered")
			} else if g.lastErrText() != "" {
				c.recordEvent("gateway", "gateway "+label+" "+st+": "+g.lastErrText())
			}
			prevState = st
		}
		e := NewEvent(EvCustom, "ringq::gateway_state")
		e.Add("Gateway", g.cfg.Name)
		e.Add("State", g.State())
		g.core.events.Publish(e)
		select {
		case <-g.stopCh:
			return
		case <-time.After(wait):
		}
	}
}

// register sends a REGISTER (expires=0 unregisters) and returns the granted
// expiry.
func (g *Gateway) register(expires int) (int, error) {
	dst, err := g.resolveHost(g.registerTo())
	if err != nil {
		return 0, err
	}
	aor := &sip.Address{URI: &sip.URI{Scheme: "sip", User: g.cfg.Username, Host: g.cfg.Realm}}
	contact := g.core.contactFor(g.contactUser(), dst)
	var authName, authValue string
	for attempt := 0; attempt < 3; attempt++ {
		g.mu.Lock()
		g.cseq++
		seq := g.cseq
		g.mu.Unlock()
		req := sip.NewRequest("REGISTER", &sip.URI{Scheme: "sip", Host: g.cfg.Realm})
		from := aor.Clone()
		from.Params.Set("tag", g.fromTag)
		req.Add("From", from.String())
		req.Add("To", aor.String())
		req.Add("Call-ID", g.callID)
		req.Add("CSeq", fmt.Sprintf("%d REGISTER", seq))
		req.Add("Contact", contact)
		req.Add("Expires", strconv.Itoa(expires))
		req.Add("Allow", allowHeader)
		if authValue != "" {
			req.Add(authName, authValue)
		}
		tx, err := g.core.stack.Request(req, dst)
		if err != nil {
			return 0, err
		}
		var final *sip.Message
		for r := range tx.Responses() {
			final = r
		}
		if final == nil {
			return 0, errors.New("no response")
		}
		switch code := final.StatusCode; {
		case code >= 200 && code < 300:
			return grantedExpiry(final, contact, expires), nil
		case (code == 401 || code == 407) && attempt < 2:
			hdr, authHdr := "WWW-Authenticate", "Authorization"
			if code == 407 {
				hdr, authHdr = "Proxy-Authenticate", "Proxy-Authorization"
			}
			chal, ok := sip.ParseDigest(final.Get(hdr))
			if !ok {
				return 0, fmt.Errorf("unsupported challenge %q", final.Get(hdr))
			}
			if authValue != "" && chal["stale"] != "true" {
				return 0, fmt.Errorf("authentication rejected (%d)", code)
			}
			authName = authHdr
			authValue = sip.BuildAuthorization(chal, "REGISTER", req.RequestURI.String(), g.authUser(), g.cfg.Password)
		default:
			return 0, fmt.Errorf("registrar answered %d %s", final.StatusCode, final.Reason)
		}
	}
	return 0, errors.New("authentication loop")
}

func grantedExpiry(resp *sip.Message, ourContact string, requested int) int {
	ours, _ := sip.ParseAddress(ourContact)
	for _, c := range resp.GetAll("Contact") {
		a, err := sip.ParseAddress(c)
		if err != nil || ours == nil {
			continue
		}
		if a.URI.User == ours.URI.User && a.URI.Host == ours.URI.Host {
			if v, ok := a.Params.Get("expires"); ok {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	if n, err := strconv.Atoi(resp.Get("Expires")); err == nil && n > 0 {
		return n
	}
	return requested
}

func (g *Gateway) lastErrText() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastErr
}

// State returns the registration state.
func (g *Gateway) State() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

// gateway finds a gateway by name or by the RingQ portal gateway_uuid.
func (c *Core) gateway(name string) *Gateway {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if g := c.gateways[name]; g != nil {
		return g
	}
	for _, g := range c.gateways {
		if g.cfg.UUID != "" && strings.EqualFold(g.cfg.UUID, name) {
			return g
		}
	}
	return nil
}

// gatewayByContact recognises calls sent to a gateway registration contact
// (gw+<name>).
func (c *Core) gatewayByContact(user string) *Gateway {
	name, ok := strings.CutPrefix(unescapeUser(user), "gw+")
	if !ok || name == "" {
		return nil
	}
	return c.gateway(name)
}

// syncGateways applies a new gateway list: new gateways start, removed ones
// stop (unregistering), changed ones restart, unchanged ones keep running.
func (c *Core) syncGateways(list []config.Gateway) {
	want := map[string]config.Gateway{}
	for _, g := range list {
		want[g.Name] = g
	}
	var stop []*Gateway
	var start []*Gateway
	c.mu.Lock()
	for name, gw := range c.gateways {
		ng, ok := want[name]
		if ok && reflect.DeepEqual(ng, gw.cfg) {
			delete(want, name)
			continue
		}
		stop = append(stop, gw)
		delete(c.gateways, name)
	}
	for _, g := range list {
		if _, ok := want[g.Name]; !ok {
			continue
		}
		gw := newGateway(c, g)
		c.gateways[g.Name] = gw
		start = append(start, gw)
	}
	c.mu.Unlock()
	for _, gw := range stop {
		c.log.Info("gateway removed or changed", "gateway", gw.cfg.Name)
		go gw.stop()
	}
	for _, gw := range start {
		c.log.Info("gateway added", "gateway", gw.cfg.Name, "proxy", gw.cfg.Proxy, "register", gw.cfg.Register)
		gw.start()
	}
}

func (c *Core) gatewayList() []*Gateway {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Gateway, 0, len(c.gateways))
	for _, g := range c.gateways {
		out = append(out, g)
	}
	return out
}

func (c *Core) gatewayBySource(ip netip.Addr) *Gateway {
	for _, g := range c.gatewayList() {
		if g.matches(ip) {
			return g
		}
	}
	return nil
}
