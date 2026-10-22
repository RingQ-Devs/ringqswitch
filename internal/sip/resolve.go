package sip

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// Resolve turns a host (and optional port) into a transport address. Literal
// IPs are used directly; names without a port are looked up via DNS SRV
// (_sip._udp / _sip._tcp) and fall back to A/AAAA records on port 5060.
func Resolve(host string, port int, transport string) (Addr, error) {
	if transport == "" {
		transport = "udp"
	}
	host = strings.Trim(host, "[]")
	if a, err := netip.ParseAddr(host); err == nil {
		if port == 0 {
			port = 5060
		}
		return Addr{Transport: transport, AP: netip.AddrPortFrom(a.Unmap(), uint16(port))}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := net.DefaultResolver
	if port == 0 {
		if _, srvs, err := r.LookupSRV(ctx, "sip", transport, host); err == nil && len(srvs) > 0 {
			for _, srv := range srvs {
				target := strings.TrimSuffix(srv.Target, ".")
				if ips, err := r.LookupNetIP(ctx, "ip4", target); err == nil && len(ips) > 0 {
					return Addr{Transport: transport, AP: netip.AddrPortFrom(ips[0].Unmap(), srv.Port)}, nil
				}
			}
		}
		port = 5060
	}
	ips, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return Addr{}, fmt.Errorf("sip: cannot resolve %q: %v", host, err)
	}
	best := ips[0]
	for _, ip := range ips {
		if ip.Unmap().Is4() {
			best = ip
			break
		}
	}
	return Addr{Transport: transport, AP: netip.AddrPortFrom(best.Unmap(), uint16(port))}, nil
}
