package core

import (
	"net"
	"net/netip"
	"sort"
	"sync"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// SIP profiles: the main port of ringqswitch.xml plus one more port per
// enabled profile in the database (the RingQ portal's "external" on 5080...).
// Requests that arrive on a profile's port are answered from it, and
// gateways of that profile send from it. Every profile advertises this
// server's own address (ext-sip-ip / sip-ip): addresses copied from another
// server's database must not send phones and carriers elsewhere.

// ProfileStatus is a SIP profile and whether its port is open.
type ProfileStatus struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	IP          string `json:"sip_ip"`     // as configured
	Port        int    `json:"port"`       // as configured
	TCP         bool   `json:"tcp"`        // as configured
	ExtSIPIP    string `json:"ext_sip_ip"` // as configured
	TLS         bool   `json:"tls"`
	Main        bool   `json:"main"` // the port of ringqswitch.xml
	Listening   bool   `json:"listening"`
	BindIP      string `json:"bind_ip"`   // where it listens ("" = all addresses)
	Advertise   string `json:"advertise"` // the address it gives out
	Error       string `json:"error,omitempty"`
	FromConfig  bool   `json:"from_config"` // the main port, not a database profile
}

type profileState struct {
	mu     sync.Mutex
	open   map[int]config.SIPProfile // extra ports open now
	status []ProfileStatus
}

func localIP(ip string) bool {
	want, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Unmap() == want.Unmap() {
			return true
		}
	}
	return false
}

// syncProfiles opens and closes the extra ports for the configured profiles.
func (c *Core) syncProfiles(list []config.SIPProfile) {
	s := c.config().Settings
	main := c.stack.Port()
	adv := firstNonEmpty(s.ExtSIPIP, s.SIPIP)
	c.prof.mu.Lock()
	defer c.prof.mu.Unlock()
	if c.prof.open == nil {
		c.prof.open = map[int]config.SIPProfile{}
	}
	var status []ProfileStatus
	want := map[int]int{} // port -> index in status
	mainSeen := false
	for _, p := range list {
		st := ProfileStatus{UUID: p.UUID, Name: p.Name, Description: p.Description, Enabled: p.Enabled, IP: p.IP, Port: p.Port,
			TCP: p.TCP, ExtSIPIP: p.ExtSIPIP, TLS: p.TLS, Advertise: adv, BindIP: s.SIPBindIP}
		switch {
		case !p.Enabled:
		case p.Port == main:
			st.Main, st.Listening, mainSeen = true, true, true
		default:
			if _, dup := want[p.Port]; dup {
				st.Error = "another profile uses this port"
				break
			}
			if p.IP != "" && localIP(p.IP) {
				st.BindIP = p.IP
			}
			want[p.Port] = len(status)
		}
		status = append(status, st)
	}
	if !mainSeen {
		status = append([]ProfileStatus{{Name: "internal", Enabled: true, Port: main, TCP: s.EnableTCP, Main: true, Listening: true,
			IP: s.SIPIP, ExtSIPIP: s.ExtSIPIP, Advertise: adv, BindIP: s.SIPBindIP, FromConfig: true}}, status...)
		for port, i := range want {
			want[port] = i + 1
		}
	}
	for port := range c.prof.open {
		if _, ok := want[port]; !ok {
			c.stack.RemoveListener(port)
			delete(c.prof.open, port)
			c.log.Info("SIP profile port closed", "port", port)
		}
	}
	for port, i := range want {
		st := &status[i]
		if _, ok := c.prof.open[port]; ok {
			st.Listening = true
			continue
		}
		if err := c.stack.AddListener(st.BindIP, port, s.EnableTCP && st.TCP); err != nil {
			st.Error = err.Error()
			c.log.Warn("SIP profile: could not open its port", "profile", st.Name, "port", port, "err", err)
			continue
		}
		c.prof.open[port] = config.SIPProfile{Name: st.Name, Port: port}
		st.Listening = true
		c.log.Info("SIP profile listening", "profile", st.Name, "port", port, "tcp", s.EnableTCP && st.TCP)
	}
	sort.SliceStable(status, func(i, j int) bool { return status[i].Main && !status[j].Main })
	c.prof.status = status
}

// SIPProfiles lists the profiles and whether their ports are open.
func (c *Core) SIPProfiles() []ProfileStatus {
	c.prof.mu.Lock()
	defer c.prof.mu.Unlock()
	return append([]ProfileStatus(nil), c.prof.status...)
}

// profileLocal is the extra port of a profile (0 = the main port, or the
// profile is unknown or closed), for gateways that belong to it.
func (c *Core) profileLocal(name string) uint16 {
	if name == "" {
		return 0
	}
	c.prof.mu.Lock()
	defer c.prof.mu.Unlock()
	for port, p := range c.prof.open {
		if p.Name == name {
			return uint16(port)
		}
	}
	return 0
}
