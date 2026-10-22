package web

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// rtcEngine terminates WebRTC for the browser dialer (G.711 audio, bridged
// to SIP) and the meetings (Opus, VP8/H.264 video, forwarded between
// participants). All media uses one UDP port (webrtc-port, default 8443),
// so the firewall needs a single rule. When tcp is given (the web port, see
// splitListener), browsers that cannot use UDP fall back to ICE over TCP on
// the port the page itself came from.
type rtcEngine struct {
	phoneAPI *webrtc.API
	meetAPI  *webrtc.API
	mux      ice.UDPMux
	tcpMux   ice.TCPMux
	port     int
	tcpPort  int
	publicIP string
}

func newRTCEngine(port int, publicIP string, tcp net.Listener) (*rtcEngine, error) {
	// One socket on all addresses. (A socket per address breaks with a
	// public IP set: every candidate is rewritten to that IP, only the first
	// socket, the loopback one, gets the session, and packets arriving on
	// the real interface are dropped.)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("webrtc: UDP port %d: %w", port, err)
	}
	port = conn.LocalAddr().(*net.UDPAddr).Port // (0 in tests: any free port)
	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn})
	se := webrtc.SettingEngine{}
	se.SetICEUDPMux(mux)
	networks := []webrtc.NetworkType{webrtc.NetworkTypeUDP4}
	e := &rtcEngine{mux: mux, port: port, publicIP: publicIP}
	if tcp != nil {
		// A small write buffer: on a congested link old audio is dropped
		// instead of being queued (a 1 MB queue meant seconds of delay).
		e.tcpMux = ice.NewTCPMuxDefault(ice.TCPMuxParams{Listener: tcp, ReadBufferSize: 32, WriteBufferSize: 16 << 10,
			FirstStunBindTimeout: 15 * time.Second})
		e.tcpPort = tcp.Addr().(*net.TCPAddr).Port
		se.SetICETCPMux(e.tcpMux)
		networks = append(networks, webrtc.NetworkTypeTCP4)
	}
	se.SetNetworkTypes(networks)
	se.SetIncludeLoopbackCandidate(true)
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	if ip := net.ParseIP(strings.TrimSpace(publicIP)); ip != nil && ip.To4() != nil {
		// Behind NAT (AWS, GCP...): announce the public address.
		se.SetNAT1To1IPs([]string{ip.String()}, webrtc.ICECandidateTypeHost)
		// Every local address becomes that one public address. For TCP the
		// connections are found by the local address they arrive at, so
		// offer only the address traffic from outside comes in on.
		if primary := primaryIPv4(); primary != nil && tcp != nil {
			se.SetIPFilter(func(ip net.IP) bool { return ip.Equal(primary) })
		}
	}

	// Dialer: G.711 µ-law, so audio passes to and from SIP untouched
	// (the SIP side converts to A-law when a call uses it).
	pm := &webrtc.MediaEngine{}
	if err := pm.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, PayloadType: 0,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		e.Close()
		return nil, err
	}
	pi := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(pm, pi); err != nil {
		e.Close()
		return nil, err
	}

	// Meetings: the browsers' usual codecs, with NACK, PLI and REMB.
	mm := &webrtc.MediaEngine{}
	if err := mm.RegisterDefaultCodecs(); err != nil {
		e.Close()
		return nil, err
	}
	mi := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mm, mi); err != nil {
		e.Close()
		return nil, err
	}
	e.phoneAPI = webrtc.NewAPI(webrtc.WithMediaEngine(pm), webrtc.WithInterceptorRegistry(pi), webrtc.WithSettingEngine(se))
	e.meetAPI = webrtc.NewAPI(webrtc.WithMediaEngine(mm), webrtc.WithInterceptorRegistry(mi), webrtc.WithSettingEngine(se))
	return e, nil
}

func (e *rtcEngine) Close() {
	e.mux.Close()
	if e.tcpMux != nil {
		e.tcpMux.Close()
	}
}

// primaryIPv4 is the address this server uses for outgoing traffic (no
// packet is sent to find it).
func primaryIPv4() net.IP {
	c, err := net.Dial("udp4", "8.8.8.8:53")
	if err != nil {
		return nil
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP
	}
	return nil
}

func isPublicIPv4(ip net.IP) bool {
	ip = ip.To4()
	cgnat := ip != nil && ip[0] == 100 && ip[1]&0xc0 == 64
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat
}

var hostIPCache = struct {
	sync.Mutex
	m map[string]hostIPs
}{m: map[string]hostIPs{}}

type hostIPs struct {
	ips []net.IP
	at  time.Time
}

// resolveHost returns the IPv4 addresses of a host name (cached for 5
// minutes; an IP literal is returned as is).
func resolveHost(host string) []net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	hostIPCache.Lock()
	e, ok := hostIPCache.m[host]
	hostIPCache.Unlock()
	if ok && time.Since(e.at) < 5*time.Minute {
		return e.ips
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, _ := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	hostIPCache.Lock()
	hostIPCache.m[host] = hostIPs{ips: addrs, at: time.Now()}
	hostIPCache.Unlock()
	return addrs
}

// pageCandidates helps a server behind NAT (AWS, GCP...) on which public-ip
// is not set: the address the browser reached the page at (the web
// address's DNS name) is offered as an extra candidate, for UDP and for
// TCP, when it is a public address that is not on this server.
func (e *rtcEngine) pageCandidates(host, mid string) []webrtc.ICECandidateInit {
	if e == nil || strings.TrimSpace(e.publicIP) != "" || host == "" {
		return nil
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	local := map[string]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				local[n.IP.String()] = true
			}
		}
	}
	var out []webrtc.ICECandidateInit
	idx := uint16(0)
	for _, ip := range resolveHost(host) {
		if !isPublicIPv4(ip) || local[ip.String()] {
			continue
		}
		cands := []string{fmt.Sprintf("candidate:3982143001 1 udp 2113937151 %s %d typ host", ip, e.port)}
		if e.tcpPort > 0 {
			cands = append(cands, fmt.Sprintf("candidate:3982143002 1 tcp 1518280447 %s %d typ host tcptype passive", ip, e.tcpPort))
		}
		for _, c := range cands {
			m := mid
			out = append(out, webrtc.ICECandidateInit{Candidate: c, SDPMid: &m, SDPMLineIndex: &idx})
		}
	}
	return out
}
