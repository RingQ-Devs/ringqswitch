package core

import (
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Security rules from the database (Security page of the web interface):
// addresses whose SIP is dropped, addresses never blocked for failed
// authentications, and the User-Agents of SIP scanners (blocked at once).

type secHits struct {
	mu   sync.Mutex
	hits map[string]uint64
	last map[string]time.Time
}

func (h *secHits) add(rule string) (first bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hits == nil {
		h.hits, h.last = map[string]uint64{}, map[string]time.Time{}
	}
	h.hits[rule]++
	first = time.Since(h.last[rule]) > time.Minute
	if first {
		h.last[rule] = time.Now()
	}
	return first
}

// SecurityHits returns how often each rule matched since the start.
func (c *Core) SecurityHits() map[string]uint64 {
	c.sec.mu.Lock()
	defer c.sec.mu.Unlock()
	out := make(map[string]uint64, len(c.sec.hits))
	for k, v := range c.sec.hits {
		out[k] = v
	}
	return out
}

func prefixMatch(list []netip.Prefix, ip netip.Addr) (netip.Prefix, bool) {
	ip = ip.Unmap()
	for _, p := range list {
		if p.Contains(ip) {
			return p, true
		}
	}
	return netip.Prefix{}, false
}

// deniedSource reports whether SIP from ip is dropped by a deny rule.
func (c *Core) deniedSource(ip netip.Addr) bool {
	p, ok := prefixMatch(c.config().Security.Deny, ip)
	if !ok {
		return false
	}
	if c.sec.add("ip-deny " + p.String()) {
		c.recordEvent("security", fmt.Sprintf("dropping SIP from %s (deny rule %s)", ip, p))
	}
	return true
}

// neverBlock reports whether a source must not be blocked for failed
// authentications: the web dialers' own sockets and allow rules.
func (c *Core) neverBlock(src sip.Addr) bool {
	if _, ok := c.trusted.Load(src.AP); ok {
		return true
	}
	_, ok := prefixMatch(c.config().Security.Allow, src.AP.Addr())
	return ok
}

// TrustSource exempts a local socket (a web dialer) from blocking.
func (c *Core) TrustSource(ap netip.AddrPort) { c.trusted.Store(ap, struct{}{}) }

// UntrustSource removes an exemption made with TrustSource.
func (c *Core) UntrustSource(ap netip.AddrPort) { c.trusted.Delete(ap) }

// scannerUA blocks the source of a request whose User-Agent matches a
// ua-deny rule (SIP scanners) and reports whether to drop it.
func (c *Core) scannerUA(req *sip.Message) bool {
	rules := c.config().Security.UADeny
	if len(rules) == 0 {
		return false
	}
	ua := strings.ToLower(req.Get("User-Agent"))
	if ua == "" {
		return false
	}
	for _, r := range rules {
		if strings.Contains(ua, r) {
			ip := req.Src.AP.Addr()
			if c.neverBlock(req.Src) {
				return false
			}
			j := c.guard.jail(config.JailSIPRequest)
			c.guard.ban(ip, config.JailSIPRequest, max(j.BanTime, time.Hour), fmt.Sprintf("SIP scanner %q", req.Get("User-Agent")), "auto", "SIP Guard")
			if c.sec.add("ua-deny " + r) {
				c.log.Warn("blocked a SIP scanner", "ip", ip, "user_agent", req.Get("User-Agent"))
			}
			return true
		}
	}
	return false
}
