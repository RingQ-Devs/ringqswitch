package core

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// SIP Guard: protections ("jails") that ban an address after repeated
// failures within a time window, inside the switch, so
// bans take effect at once and need no firewall. SIP protections close SIP
// to the address, web protections the web interface. Bans are announced
// with ringq::guard events (the database keeps them in sip_security_blocks
// and security_audit_log) and restored when the switch starts.

// Ban is an address banned by a protection.
type Ban struct {
	IP       netip.Addr
	Jail     string
	Since    time.Time
	Until    time.Time
	Failures int
	Reason   string
	Source   string // auto, manual
}

// guardSlots counts failures over a sliding window cut in six slots.
type guardSlots struct {
	n  [6]int
	at [6]int64
}

func (s *guardSlots) add(now time.Time, window time.Duration) int {
	w := int64(window / 6)
	if w <= 0 {
		w = int64(time.Second)
	}
	idx := now.UnixNano() / w
	i := idx % 6
	if s.at[i] != idx {
		s.at[i], s.n[i] = idx, 0
	}
	s.n[i]++
	total := 0
	for k := range s.n {
		if idx-s.at[k] < 6 {
			total += s.n[k]
		}
	}
	return total
}

func (s *guardSlots) stale(now time.Time, window time.Duration) bool {
	w := int64(window / 6)
	if w <= 0 {
		w = int64(time.Second)
	}
	idx := now.UnixNano() / w
	for k := range s.at {
		if idx-s.at[k] < 6 {
			return false
		}
	}
	return true
}

type guardNote struct {
	action string // ban, unban, expire
	ban    Ban
	actor  string
}

type guard struct {
	mu      sync.Mutex
	cfg     config.Guard
	counts  map[string]map[netip.Addr]*guardSlots
	bans    map[netip.Addr]map[string]*Ban
	noticed map[netip.Addr]time.Time // last time a drop was logged per address
	notify  func([]guardNote)
}

func newGuard(cfg config.Guard) *guard {
	return &guard{cfg: cfg, counts: map[string]map[netip.Addr]*guardSlots{}, bans: map[netip.Addr]map[string]*Ban{},
		noticed: map[netip.Addr]time.Time{}}
}

func (g *guard) send(notes []guardNote) {
	if len(notes) > 0 && g.notify != nil {
		g.notify(notes)
	}
}

// jail returns a protection's settings.
func (g *guard) jail(name string) config.Jail {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.Jails[name]
}

// configure applies new settings. Turning SIP Guard off lifts every ban.
func (g *guard) configure(cfg config.Guard) {
	g.mu.Lock()
	var notes []guardNote
	if !cfg.Enabled {
		for ip, m := range g.bans {
			for _, b := range m {
				notes = append(notes, guardNote{"unban", *b, "SIP Guard turned off"})
			}
			delete(g.bans, ip)
		}
		g.counts = map[string]map[netip.Addr]*guardSlots{}
	}
	for name, j := range cfg.Jails {
		if !j.Enabled {
			delete(g.counts, name)
		}
	}
	g.cfg = cfg
	g.mu.Unlock()
	g.send(notes)
}

// fail records a failure for a protection and reports whether the address
// is now banned and the failures counted in the window.
func (g *guard) fail(jail string, ip netip.Addr, reason string) (bool, int) {
	ip = ip.Unmap()
	g.mu.Lock()
	j, ok := g.cfg.Jails[jail]
	if !g.cfg.Enabled || !ok || !j.Enabled || j.MaxFailures <= 0 {
		g.mu.Unlock()
		return false, 0
	}
	now := time.Now()
	m := g.counts[jail]
	if m == nil {
		m = map[netip.Addr]*guardSlots{}
		g.counts[jail] = m
	}
	sl := m[ip]
	if sl == nil {
		sl = &guardSlots{}
		m[ip] = sl
	}
	n := sl.add(now, j.Window)
	if n < j.MaxFailures {
		g.mu.Unlock()
		return false, n
	}
	delete(m, ip)
	b := &Ban{IP: ip, Jail: jail, Since: now, Until: now.Add(j.BanTime), Failures: n, Reason: reason, Source: "auto"}
	g.putLocked(b)
	g.mu.Unlock()
	g.send([]guardNote{{"ban", *b, "SIP Guard"}})
	return true, n
}

func (g *guard) putLocked(b *Ban) {
	m := g.bans[b.IP]
	if m == nil {
		m = map[string]*Ban{}
		g.bans[b.IP] = m
	}
	m[b.Jail] = b
}

// ban bans an address now (manual bans, scanners).
func (g *guard) ban(ip netip.Addr, jail string, d time.Duration, reason, source, actor string) Ban {
	ip = ip.Unmap()
	now := time.Now()
	b := &Ban{IP: ip, Jail: jail, Since: now, Until: now.Add(d), Reason: reason, Source: source}
	g.mu.Lock()
	g.putLocked(b)
	g.mu.Unlock()
	g.send([]guardNote{{"ban", *b, actor}})
	return *b
}

// banned returns the ban closing area ("sip" or "web") to ip, if any.
func (g *guard) banned(ip netip.Addr, area string) *Ban {
	ip = ip.Unmap()
	g.mu.Lock()
	m := g.bans[ip]
	if m == nil || !g.cfg.Enabled {
		g.mu.Unlock()
		return nil
	}
	now := time.Now()
	var notes []guardNote
	var found *Ban
	for name, b := range m {
		if now.After(b.Until) {
			notes = append(notes, guardNote{"expire", *b, "SIP Guard"})
			delete(m, name)
			continue
		}
		if config.JailArea(name) == area && found == nil {
			c := *b
			found = &c
		}
	}
	if len(m) == 0 {
		delete(g.bans, ip)
	}
	g.mu.Unlock()
	g.send(notes)
	return found
}

// unban lifts the bans of ip (of one protection, or all with jail "").
func (g *guard) unban(ip netip.Addr, jail, actor string) bool {
	ip = ip.Unmap()
	g.mu.Lock()
	var notes []guardNote
	for name, b := range g.bans[ip] {
		if jail == "" || name == jail {
			notes = append(notes, guardNote{"unban", *b, actor})
			delete(g.bans[ip], name)
		}
	}
	if len(g.bans[ip]) == 0 {
		delete(g.bans, ip)
	}
	delete(g.noticed, ip)
	g.mu.Unlock()
	g.send(notes)
	return len(notes) > 0
}

// list returns the bans in force, by address and protection.
func (g *guard) list() []Ban {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	var out []Ban
	for _, m := range g.bans {
		for _, b := range m {
			if now.Before(b.Until) {
				out = append(out, *b)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IP != out[j].IP {
			return out[i].IP.Less(out[j].IP)
		}
		return out[i].Jail < out[j].Jail
	})
	return out
}

// addresses returns each banned address with the end of its last ban.
func (g *guard) addresses() map[netip.Addr]time.Time {
	out := map[netip.Addr]time.Time{}
	for _, b := range g.list() {
		if b.Until.After(out[b.IP]) {
			out[b.IP] = b.Until
		}
	}
	return out
}

// restore puts back bans kept in the database (when the switch starts).
func (g *guard) restore(list []config.StoredBan) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for _, s := range list {
		if !s.IP.IsValid() || now.After(s.Until) {
			continue
		}
		if _, ok := g.bans[s.IP.Unmap()][s.Jail]; ok {
			continue
		}
		g.putLocked(&Ban{IP: s.IP.Unmap(), Jail: s.Jail, Since: s.Since, Until: s.Until, Failures: s.Failures, Reason: s.Reason,
			Source: firstNonEmpty(s.Source, "auto")})
	}
}

// dropNotice reports whether a dropped packet from ip should be logged (at
// most once a minute per address).
func (g *guard) dropNotice(ip netip.Addr) bool {
	ip = ip.Unmap()
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if last, ok := g.noticed[ip]; ok && now.Sub(last) < time.Minute {
		return false
	}
	g.noticed[ip] = now
	return true
}

// sweep ends expired bans and forgets old failures.
func (g *guard) sweep() {
	g.mu.Lock()
	now := time.Now()
	var notes []guardNote
	for ip, m := range g.bans {
		for name, b := range m {
			if now.After(b.Until) {
				notes = append(notes, guardNote{"expire", *b, "SIP Guard"})
				delete(m, name)
			}
		}
		if len(m) == 0 {
			delete(g.bans, ip)
		}
	}
	for name, m := range g.counts {
		w := g.cfg.Jails[name].Window
		for ip, sl := range m {
			if sl.stale(now, w) {
				delete(m, ip)
			}
		}
	}
	for ip, t := range g.noticed {
		if now.Sub(t) > time.Minute {
			delete(g.noticed, ip)
		}
	}
	g.mu.Unlock()
	g.send(notes)
}

// guardNotify publishes the bans as events and in the event log.
func (c *Core) guardNotify(notes []guardNote) {
	for _, n := range notes {
		b := n.ban
		e := NewEvent(EvCustom, "ringq::guard")
		e.Add("Action", n.action)
		e.Add("Network-IP", b.IP.String())
		e.Add("Jail", b.Jail)
		e.Add("Reason", b.Reason)
		e.Add("Failures", strconv.Itoa(b.Failures))
		e.Add("Ban-Since", strconv.FormatInt(b.Since.Unix(), 10))
		e.Add("Ban-Until", strconv.FormatInt(b.Until.Unix(), 10))
		e.Add("Source", b.Source)
		e.Add("Actor", n.actor)
		c.events.Publish(e)
		switch n.action {
		case "ban":
			c.log.Warn("SIP Guard: address banned", "ip", b.IP, "protection", b.Jail, "until", b.Until.Format(time.RFC3339), "reason", b.Reason)
			c.recordEvent("security", fmt.Sprintf("banned %s (%s) until %s: %s", b.IP, b.Jail, b.Until.Format("2006-01-02 15:04"), b.Reason))
		case "unban":
			c.log.Info("SIP Guard: ban lifted", "ip", b.IP, "protection", b.Jail, "by", n.actor)
			c.recordEvent("security", fmt.Sprintf("%s unbanned (%s) by %s", b.IP, b.Jail, n.actor))
		}
	}
}

// GuardFail records a failure of a protection for ip (the web interface
// reports its own) and reports whether the address is now banned.
func (c *Core) GuardFail(jail, ip, reason string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil || c.neverBan(a, config.JailArea(jail)) {
		return false
	}
	banned, _ := c.guard.fail(jail, a, reason)
	return banned
}

// GuardBanned returns the ban closing area ("sip" or "web") to ip.
func (c *Core) GuardBanned(ip, area string) *Ban {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	return c.guard.banned(a, area)
}

// GuardBan bans ip by hand for a protection's ban time.
func (c *Core) GuardBan(ip, jail, reason, actor string) (Ban, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return Ban{}, fmt.Errorf("not an IP address: %s", ip)
	}
	j := c.guard.jail(jail)
	if j.BanTime <= 0 {
		return Ban{}, fmt.Errorf("unknown protection %q", jail)
	}
	return c.guard.ban(a, jail, j.BanTime, firstNonEmpty(reason, "banned by hand"), "manual", actor), nil
}

// GuardUnban lifts the bans of ip (one protection, or all with jail "").
func (c *Core) GuardUnban(ip, jail, actor string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && c.guard.unban(a, jail, actor)
}

// GuardBans returns the bans in force.
func (c *Core) GuardBans() []Ban { return c.guard.list() }

// noRoute counts calls from outside (no sign-in, not a trunk) to numbers
// that do not exist: dial plan probing (SIP Request Protection).
func (c *Core) noRoute(ch *Channel, dest string) {
	if ch.Direction != DirectionInbound || ch.Var("sip_authorized") == "true" || ch.Var("gateway_name") != "" || ch.Var("acl_name") != "" {
		return
	}
	ip, err := netip.ParseAddr(ch.Var("network_addr"))
	if err != nil || c.neverBan(ip, "sip") {
		return
	}
	c.guard.fail(config.JailSIPRequest, ip, "call to "+dest+", a number that does not exist")
}

// neverBan reports whether an address must not be banned: this server,
// allow rules, and for SIP the web dialers, gateways and trusted carriers.
func (c *Core) neverBan(ip netip.Addr, area string) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip == netip.IPv6Loopback() || ip == netip.AddrFrom4([4]byte{127, 0, 0, 1}) {
		return true
	}
	cfg := c.config()
	if _, ok := prefixMatch(cfg.Security.Allow, ip); ok {
		return true
	}
	if area == "sip" {
		return c.gatewayBySource(ip) != nil || cfg.MatchACL(ip) != nil
	}
	return false
}
