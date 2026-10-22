package web

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// connectAudio connects a client PeerConnection (limited to the given
// networks) to a dialer PeerConnection of the engine and returns how many
// audio packets the client received and the protocol of the selected pair.
func connectAudio(t *testing.T, e *rtcEngine, networks []webrtc.NetworkType) (int, string) {
	n, proto, _ := connectAudioFrom(t, e, networks, nil)
	return n, proto
}

// connectAudioFrom is connectAudio with the client limited to one local
// address (only; nil: loopback included). It also returns the server
// address the client's selected pair uses.
func connectAudioFrom(t *testing.T, e *rtcEngine, networks []webrtc.NetworkType, only net.IP) (int, string, string) {
	t.Helper()
	cse := webrtc.SettingEngine{}
	cse.SetNetworkTypes(networks)
	cse.SetIncludeLoopbackCandidate(only == nil)
	if only != nil {
		cse.SetIPFilter(func(ip net.IP) bool { return ip.Equal(only) })
	}
	cli, err := webrtc.NewAPI(webrtc.WithSettingEngine(cse)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	got := make(chan int, 1)
	cli.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		n := 0
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
			if n++; n == 20 {
				got <- n
			}
		}
	})
	offer, _ := cli.CreateOffer(nil)
	gc := webrtc.GatheringCompletePromise(cli)
	_ = cli.SetLocalDescription(offer)
	<-gc

	srv, err := e.phoneAPI.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	track, _ := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, "audio", "t")
	if _, err := srv.AddTrack(track); err != nil {
		t.Fatal(err)
	}
	if err := srv.SetRemoteDescription(*cli.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	ans, _ := srv.CreateAnswer(nil)
	gs := webrtc.GatheringCompletePromise(srv)
	_ = srv.SetLocalDescription(ans)
	<-gs
	if err := cli.SetRemoteDescription(*srv.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				_ = track.WriteSample(media.Sample{Data: make([]byte, 160), Duration: 20 * time.Millisecond})
			}
		}
	}()
	select {
	case n := <-got:
		proto, remote := "", ""
		if pair, err := cli.SCTP().Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
			proto, remote = pair.Local.Protocol.String(), pair.Remote.Address
		}
		return n, proto, remote
	case <-time.After(10 * time.Second):
		return 0, "", ""
	}
}

func TestRTCOverUDPAndWebPortTCP(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	split := newSplitListener(ln)
	web := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "web ok") })}
	go func() { _ = web.Serve(split.Web()) }()
	defer web.Close()

	e, err := newRTCEngine(0, "", split.ICE())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	// The web interface still answers on the shared port.
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "web ok" {
		t.Fatalf("web through the split port: %q", body)
	}

	if n, proto := connectAudio(t, e, []webrtc.NetworkType{webrtc.NetworkTypeUDP4}); n == 0 || proto != "udp" {
		t.Fatalf("UDP: %d packets via %q", n, proto)
	}
	// A browser on a network that blocks UDP: audio over TCP on the web port.
	if n, proto := connectAudio(t, e, []webrtc.NetworkType{webrtc.NetworkTypeTCP4}); n == 0 || proto != "tcp" {
		t.Fatalf("TCP: %d packets via %q", n, proto)
	}
}

// With a public IP set every candidate carries that IP; the media must still
// reach the session whichever local address the packets arrive at.
func TestRTCWithPublicIP(t *testing.T) {
	primary := primaryIPv4()
	if primary == nil || primary.IsLoopback() {
		t.Skip("no non-loopback address")
	}
	e, err := newRTCEngine(0, primary.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	// The client uses only the real interface, as a browser elsewhere would,
	// and the server must answer from that interface: the 0.4.1 engine ran
	// the session on its loopback socket, which works on one machine but
	// can never reach a browser on another.
	n, _, remote := connectAudioFrom(t, e, []webrtc.NetworkType{webrtc.NetworkTypeUDP4}, primary)
	if n == 0 {
		t.Fatal("no audio with public-ip set")
	}
	if ip := net.ParseIP(remote); ip == nil || ip.IsLoopback() {
		t.Fatalf("the server answered from %q, not from its public address", remote)
	}
}

func TestPageCandidates(t *testing.T) {
	e := &rtcEngine{port: 8443, tcpPort: 8443}
	c := e.pageCandidates("203.0.113.7:8443", "0")
	if len(c) != 2 || c[0].Candidate != "candidate:3982143001 1 udp 2113937151 203.0.113.7 8443 typ host" ||
		c[1].Candidate != "candidate:3982143002 1 tcp 1518280447 203.0.113.7 8443 typ host tcptype passive" || *c[0].SDPMid != "0" {
		t.Fatalf("page candidates: %+v", c)
	}
	for _, host := range []string{"10.1.2.3:8443", "127.0.0.1", "192.168.1.5", "100.64.0.9"} {
		if c := e.pageCandidates(host, "0"); len(c) != 0 {
			t.Fatalf("%s: private address offered: %+v", host, c)
		}
	}
	e.publicIP = "198.51.100.1"
	if c := e.pageCandidates("203.0.113.7", "0"); len(c) != 0 {
		t.Fatal("public-ip set: no extra candidates expected")
	}
}

func TestSplitListenerClose(t *testing.T) {
	ln, _ := net.Listen("tcp4", "127.0.0.1:0")
	split := newSplitListener(ln)
	split.ICE().Close() // ICE stops; the web side keeps working
	go func() {
		c, err := net.Dial("tcp4", ln.Addr().String())
		if err == nil {
			_, _ = c.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
			time.Sleep(200 * time.Millisecond)
			c.Close()
		}
	}()
	c, err := split.Web().Accept()
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 3)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "GET" {
		t.Fatalf("first bytes: %q %v", b, err)
	}
	c.Close()
	split.Web().Close()
	if _, err := split.Web().Accept(); err == nil {
		t.Fatal("accept after close")
	}
	if _, err := net.DialTimeout("tcp4", ln.Addr().String(), time.Second); err == nil {
		t.Fatal("port still open after close")
	}
}
