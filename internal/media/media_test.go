package media

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

func TestG711(t *testing.T) {
	if LinearToUlaw(0) != 0xFF || UlawToLinear(0xFF) != 0 {
		t.Fatal("mu-law zero")
	}
	if UlawToLinear(0x00) != -32124 || UlawToLinear(0x80) != 32124 {
		t.Fatalf("mu-law extremes %d %d", UlawToLinear(0x00), UlawToLinear(0x80))
	}
	if LinearToAlaw(0) != 0xD5 {
		t.Fatalf("A-law zero = %#x", LinearToAlaw(0))
	}
	// Every code word must survive decode -> encode.
	for i := 0; i < 256; i++ {
		if got := LinearToUlaw(UlawToLinear(byte(i))); got != byte(i) && !(i == 0x7F && got == 0xFF) {
			t.Errorf("mu-law %#x -> %#x", i, got)
		}
		if got := LinearToAlaw(AlawToLinear(byte(i))); got != byte(i) {
			t.Errorf("A-law %#x -> %#x", i, got)
		}
	}
	// Quantisation error stays small.
	for _, s := range []int16{100, -100, 1000, -1000, 12345, -30000} {
		u := UlawToLinear(LinearToUlaw(s))
		a := AlawToLinear(LinearToAlaw(s))
		if abs(int(u)-int(s)) > int(abs(int(s)))/16+8 || abs(int(a)-int(s)) > int(abs(int(s)))/16+16 {
			t.Errorf("sample %d: ulaw %d alaw %d", s, u, a)
		}
	}
	p := []byte{LinearToUlaw(1000), LinearToUlaw(-1000)}
	if !Transcode("PCMU", "PCMA", p) || Transcode("PCMU", "G722", p) {
		t.Fatal("transcode support")
	}
	if abs(int(AlawToLinear(p[0]))-1000) > 80 {
		t.Fatalf("transcoded sample %d", AlawToLinear(p[0]))
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestTone(t *testing.T) {
	src, err := ParseTone("%(100,50,440,480);loops=2")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	buf := make([]int16, 160)
	var energyOn, energyOff int64
	for {
		n, err := src.Read(buf)
		if err == io.EOF {
			break
		}
		for i := 0; i < n; i++ {
			pos := (total + i) % 1200
			v := int64(buf[i]) * int64(buf[i])
			if pos < 800 {
				energyOn += v
			} else {
				energyOff += v
			}
		}
		total += n
	}
	if total != 2*150*8 {
		t.Fatalf("expected %d samples, got %d", 2*150*8, total)
	}
	if energyOn == 0 || energyOff != 0 {
		t.Fatalf("tone cadence wrong: on=%d off=%d", energyOn, energyOff)
	}
	for _, bad := range []string{"", "%(1)", "loops=x;%(1,1,1)", "%(a,b)", "v=3;%(1,1,1)"} {
		if _, err := ParseTone(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestWAVRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.wav")
	w, err := CreateWAV(path)
	if err != nil {
		t.Fatal(err)
	}
	in := make([]int16, 800)
	for i := range in {
		in[i] = int16(i * 10)
	}
	if err := w.Write(in); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := LoadWAV(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) || out[799] != in[799] {
		t.Fatalf("wav round trip mismatch: %d samples", len(out))
	}
	if r := Resample(make([]int16, 16000), 16000, 8000); len(r) != 8000 {
		t.Fatalf("resample length %d", len(r))
	}
	src, err := OpenSource("silence_stream://100", "")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := src.Read(make([]int16, 2000)); n != 800 {
		t.Fatalf("silence length %d", n)
	}
}

func rtpPacket(pt uint8, seq uint16, payload []byte) []byte {
	return rtpPacketTS(pt, seq, uint32(seq)*160, payload)
}

func rtpPacketTS(pt uint8, seq uint16, ts uint32, payload []byte) []byte {
	b := make([]byte, 12+len(payload))
	b[0] = 0x80
	b[1] = pt
	binary.BigEndian.PutUint16(b[2:], seq)
	binary.BigEndian.PutUint32(b[4:], ts)
	binary.BigEndian.PutUint32(b[8:], 0x1234)
	copy(b[12:], payload)
	return b
}

func udpPeer(t *testing.T) *net.UDPConn {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func apOf(c *net.UDPConn) netip.AddrPort { return c.LocalAddr().(*net.UDPAddr).AddrPort() }

func recv(t *testing.T, c *net.UDPConn) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2000)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("no packet: %v", err)
	}
	return buf[:n]
}

func TestRelayWithTranscodingAndDTMF(t *testing.T) {
	alloc, err := NewAllocator("127.0.0.1", 30000, 30100, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := alloc.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := alloc.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()
	if alloc.InUse() != 2 || a.Port == b.Port || a.Port%2 != 0 {
		t.Fatalf("bad allocation %d %d", a.Port, b.Port)
	}
	phoneA, phoneB := udpPeer(t), udpPeer(t)
	a.SetRemote(apOf(phoneA), netip.AddrPort{})
	b.SetRemote(apOf(phoneB), netip.AddrPort{})
	// A talks PCMU with DTMF PT 101, B talks PCMA with DTMF PT 96.
	a.SetCodecs([]Codec{{0, "PCMU", 8000}, {101, "telephone-event", 8000}}, Codec{0, "PCMU", 8000}, 101)
	b.SetCodecs([]Codec{{8, "PCMA", 8000}, {96, "telephone-event", 8000}}, Codec{8, "PCMA", 8000}, 96)
	Bridge(a, b)

	epA := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(a.Port))
	ulaw := []byte{LinearToUlaw(1000), LinearToUlaw(-2000)}
	if _, err := phoneA.WriteToUDPAddrPort(rtpPacket(0, 1, ulaw), epA); err != nil {
		t.Fatal(err)
	}
	got := recv(t, phoneB)
	h, ok := parseRTP(got)
	if !ok || h.pt != 8 {
		t.Fatalf("expected PCMA payload type 8, got %+v", h)
	}
	if v := AlawToLinear(got[12]); abs(int(v)-1000) > 80 {
		t.Fatalf("transcoded value %d", v)
	}
	dtmf := []byte{5, 0x80, 0, 160}
	_, _ = phoneA.WriteToUDPAddrPort(rtpPacket(101, 2, dtmf), epA)
	got = recv(t, phoneB)
	if h, _ := parseRTP(got); h.pt != 96 {
		t.Fatalf("telephone-event PT not rewritten: %d", h.pt)
	}

	// Symmetric latching: B sends from a different socket than the SDP said.
	other := udpPeer(t)
	epB := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(b.Port))
	b.SetRemote(apOf(phoneB), netip.AddrPort{}) // unchanged: keeps latch state
	_, _ = other.WriteToUDPAddrPort(rtpPacket(8, 1, []byte{0xD5, 0xD5}), epB)
	recv(t, phoneA)
	_, _ = phoneA.WriteToUDPAddrPort(rtpPacket(0, 3, ulaw), epA)
	recv(t, other)

	Unbridge(a, b)
	if a.Mode() != ModeIdle || b.Mode() != ModeIdle {
		t.Fatal("unbridge should idle both endpoints")
	}
}

func TestLocalModeAndEcho(t *testing.T) {
	alloc, _ := NewAllocator("127.0.0.1", 30200, 30300, nil)
	e, err := alloc.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	phone := udpPeer(t)
	e.SetRemote(apOf(phone), netip.AddrPort{})
	e.SetCodecs([]Codec{{0, "PCMU", 8000}, {101, "telephone-event", 8000}}, Codec{0, "PCMU", 8000}, 101)
	digits := make(chan rune, 4)
	e.OnDTMF(func(r rune) { digits <- r })
	e.SetMode(ModeLocal)

	frame := make([]int16, FrameSamples)
	for i := range frame {
		frame[i] = 4000
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.WriteFrame(ctx, frame); err != nil {
		t.Fatal(err)
	}
	// First packets may be silence; wait for the tone frame.
	found := false
	for i := 0; i < 10 && !found; i++ {
		p := recv(t, phone)
		h, _ := parseRTP(p)
		found = h.pt == 0 && len(p) == 12+FrameSamples && UlawToLinear(p[12]) > 3000
	}
	if !found {
		t.Fatal("queued frame never transmitted")
	}
	ep := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(e.Port))
	_, _ = phone.WriteToUDPAddrPort(rtpPacket(0, 10, []byte{LinearToUlaw(500), LinearToUlaw(500)}), ep)
	in, err := e.ReadFrame(ctx)
	if err != nil || len(in) != 2 || abs(int(in[0])-500) > 40 {
		t.Fatalf("decoded input %v %v", in, err)
	}
	// DTMF end packets are sent three times; one digit must be reported.
	for i := 0; i < 3; i++ {
		_, _ = phone.WriteToUDPAddrPort(rtpPacketTS(101, uint16(11+i), 99999, []byte{11, 0x80, 0, 160}), ep)
	}
	select {
	case d := <-digits:
		if d != '#' {
			t.Fatalf("digit %q", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no dtmf")
	}
	select {
	case d := <-digits:
		t.Fatalf("duplicate digit %q", d)
	case <-time.After(100 * time.Millisecond):
	}

	e.SetMode(ModeEcho)
	time.Sleep(50 * time.Millisecond)
	_ = phone.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	for {
		if _, err := phone.Read(make([]byte, 2000)); err != nil {
			break
		}
	}
	_, _ = phone.WriteToUDPAddrPort(rtpPacket(0, 20, []byte{1, 2, 3}), ep)
	p := recv(t, phone)
	if h, _ := parseRTP(p); h.seq != 20 || h.ssrc == 0x1234 || string(p[12:]) != "\x01\x02\x03" {
		t.Fatalf("bad echo %+v", h)
	}
}

// TestRelatch: once latched, packets from elsewhere are ignored, except a new
// port of the same host after the latched source went quiet (NAT rebinding).
// Another host never takes over the stream, unless Relearn was called.
func TestRelatch(t *testing.T) {
	alloc, _ := NewAllocator("127.0.0.1", 30400, 30500, nil)
	e, err := alloc.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	first, moved := udpPeer(t), udpPeer(t)
	stranger, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2)})
	if err != nil {
		t.Skip("no 127.0.0.2 on this system:", err)
	}
	defer stranger.Close()
	e.SetRemote(apOf(first), netip.AddrPort{})
	ep := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(e.Port))
	seq := uint16(0)
	send := func(c *net.UDPConn) uint64 {
		before := e.Stats().PacketsIn
		seq++
		_, _ = c.WriteToUDPAddrPort(rtpPacket(0, seq, []byte{0xff, 0xff}), ep)
		time.Sleep(60 * time.Millisecond)
		return e.Stats().PacketsIn - before
	}
	if send(first) != 1 || e.Remote() != apOf(first) {
		t.Fatal("first packet should latch")
	}
	if send(moved) != 0 || e.Remote() != apOf(first) {
		t.Fatal("another port while the source still talks must be ignored")
	}
	time.Sleep(relatchQuiet + 100*time.Millisecond)
	if send(stranger) != 0 {
		t.Fatal("another host must never take over")
	}
	if send(moved) != 1 || e.Remote() != apOf(moved) {
		t.Fatalf("after a quiet second the same host's new port should take over, remote %v", e.Remote())
	}
	e.Relearn()
	if send(stranger) != 1 || e.Remote().Addr().String() != "127.0.0.2" {
		t.Fatal("after Relearn the next source is learned")
	}
}
