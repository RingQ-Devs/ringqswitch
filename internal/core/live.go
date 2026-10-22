package core

import (
	"net/netip"
	"runtime"
	"sort"
	"time"
)

// Live state for dashboards and the web interface.

// GatewayInfo is the live state of a gateway.
type GatewayInfo struct {
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	Label     string `json:"label"`
	Proxy     string `json:"proxy"`
	Register  bool   `json:"register"`
	State     string `json:"state"`
	LastError string `json:"last_error"`
}

// Gateways returns the gateways and their registration state.
func (c *Core) Gateways() []GatewayInfo {
	var out []GatewayInfo
	for _, g := range c.gatewayList() {
		g.mu.Lock()
		out = append(out, GatewayInfo{Name: g.cfg.Name, UUID: g.cfg.UUID, Label: firstNonEmpty(g.cfg.Label, g.cfg.Name),
			Proxy: g.cfg.Proxy, Register: g.cfg.Register, State: g.state, LastError: g.lastErr})
		g.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// Stats summarises the switch.
type Stats struct {
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Channels      int    `json:"channels"`
	SessionsTotal uint64 `json:"sessions_total"`
	Registrations int    `json:"registrations"`
	Blocked       int    `json:"blocked"`
	RTPInUse      int    `json:"rtp_in_use"`
	MemoryMB      uint64 `json:"memory_mb"`
	Goroutines    int    `json:"goroutines"`
	SIPPort       int    `json:"sip_port"`
	SIPIP         string `json:"sip_ip"`
	Hostname      string `json:"hostname"`
}

// Stats returns live counters.
func (c *Core) Stats() Stats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := c.config().Settings
	st := Stats{Version: Version, UptimeSeconds: int64(time.Since(c.started).Seconds()), Channels: c.channelCount(),
		SessionsTotal: c.sessionsTotal.Load(), Registrations: c.reg.count(), Blocked: len(c.guard.addresses()),
		RTPInUse: c.media.InUse(), MemoryMB: ms.Alloc >> 20, Goroutines: runtime.NumGoroutine(), SIPIP: s.SIPIP,
		Hostname: c.events.hostname}
	if c.stack != nil {
		st.SIPPort = c.stack.Port()
	}
	return st
}

// Blocked returns the addresses blocked for failed authentication.
func (c *Core) Blocked() map[string]time.Time {
	out := map[string]time.Time{}
	for ip, t := range c.guard.addresses() {
		out[ip.String()] = t
	}
	return out
}

// Unblock removes an address from the block list.
func (c *Core) Unblock(ip string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && c.guard.unban(a, "", "web interface")
}
