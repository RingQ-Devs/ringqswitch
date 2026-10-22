package sip

import (
	"bufio"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

const inviteRaw = "INVITE sip:1001@192.168.1.10 SIP/2.0\r\n" +
	"v: SIP/2.0/UDP 10.0.0.5:5062;branch=z9hG4bK776asdhds;rport, SIP/2.0/TCP proxy.example.com;branch=z9hG4bKabc\r\n" +
	"Max-Forwards: 70\r\n" +
	"To: <sip:1001@192.168.1.10>\r\n" +
	"f: \"Alice \\\"A\\\" Smith\" <sip:1000@192.168.1.10>;tag=1928301774\r\n" +
	"i: a84b4c76e66710@pc33.example.com\r\n" +
	"CSeq: 314159 INVITE\r\n" +
	"m: <sip:1000@10.0.0.5:5062;transport=udp>;expires=60, <sip:1000@203.0.113.9>\r\n" +
	"Subject: long\r\n  folded line\r\n" +
	"c: application/sdp\r\n" +
	"l: 4\r\n" +
	"\r\n" +
	"v=0\r\nEXTRA"

func TestParseRequest(t *testing.T) {
	m, err := Parse([]byte(inviteRaw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Method != "INVITE" || m.RequestURI.User != "1001" || m.RequestURI.Host != "192.168.1.10" {
		t.Fatalf("bad request line: %s %v", m.Method, m.RequestURI)
	}
	if got := len(m.GetAll("Via")); got != 2 {
		t.Fatalf("want 2 Via values, got %d", got)
	}
	via, err := m.TopVia()
	if err != nil || via.Transport != "UDP" || via.Host != "10.0.0.5" || via.Port != 5062 || via.Branch() != "z9hG4bK776asdhds" || !via.Params.Has("rport") {
		t.Fatalf("bad via %+v %v", via, err)
	}
	from, err := m.From()
	if err != nil || from.Display != `Alice "A" Smith` || from.Tag() != "1928301774" || from.URI.User != "1000" {
		t.Fatalf("bad from %+v %v", from, err)
	}
	if m.CallID() != "a84b4c76e66710@pc33.example.com" {
		t.Fatalf("bad call-id %q", m.CallID())
	}
	seq, method := m.CSeq()
	if seq != 314159 || method != "INVITE" {
		t.Fatalf("bad cseq %d %s", seq, method)
	}
	contacts := m.GetAll("Contact")
	if len(contacts) != 2 {
		t.Fatalf("want 2 contacts got %v", contacts)
	}
	c, _ := ParseAddress(contacts[0])
	if e, _ := c.Params.Get("expires"); e != "60" || c.URI.Transport() != "udp" || c.URI.Port != 5062 {
		t.Fatalf("bad contact %+v", c)
	}
	if m.Get("Subject") != "long folded line" {
		t.Fatalf("folding failed: %q", m.Get("Subject"))
	}
	if string(m.Body) != "v=0\r" {
		t.Fatalf("body not limited by content-length: %q", m.Body)
	}
	if !m.HasSDP() {
		t.Fatal("expected sdp content type")
	}
	// Round trip.
	m2, err := Parse(m.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if m2.Get("From") != m.Get("From") || len(m2.GetAll("Via")) != 2 || string(m2.Body) != string(m.Body) {
		t.Fatal("round trip mismatch")
	}
}

func TestParseResponseAndErrors(t *testing.T) {
	m, err := Parse([]byte("SIP/2.0 486 Busy Here\r\nVia: SIP/2.0/UDP h;branch=z9hG4bK1\r\nCSeq: 1 INVITE\r\n\r\n"))
	if err != nil || m.StatusCode != 486 || m.Reason != "Busy Here" || m.IsRequest() {
		t.Fatalf("bad response parse %+v %v", m, err)
	}
	for _, bad := range []string{
		"", "HELLO\r\n\r\n", "INVITE sip:x SIP/3.0\r\n\r\n", "SIP/2.0 99 X\r\n\r\n",
		"INVITE sip:a@b SIP/2.0\r\nContent-Length: 10\r\n\r\nabc",
		"INVITE sip:a@b SIP/2.0\r\nNoColonHere\r\n\r\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestURIAndAddress(t *testing.T) {
	cases := []struct{ in, out string }{
		{"sip:alice:secret@example.com:5070;transport=tcp;lr?subject=x", "sip:alice:secret@example.com:5070;transport=tcp;lr?subject=x"},
		{"sip:[2001:db8::1]:5060", "sip:[2001:db8::1]:5060"},
		{"sips:bob@example.com", "sips:bob@example.com"},
		{"tel:+15551234;phone-context=example.com", "tel:+15551234;phone-context=example.com"},
		{"sip:1000;npdi@10.0.0.1", "sip:1000;npdi@10.0.0.1"},
	}
	for _, c := range cases {
		u, err := ParseURI(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if u.String() != c.out {
			t.Errorf("round trip %q -> %q", c.in, u.String())
		}
	}
	if _, err := ParseURI("http://x"); err == nil {
		t.Error("expected error for http uri")
	}
	if _, err := ParseURI("sip:host:99999"); err == nil {
		t.Error("expected error for bad port")
	}
	a, err := ParseAddress("sip:bob@example.com;tag=abc")
	if err != nil || a.Tag() != "abc" || a.URI.Params.Has("tag") {
		t.Fatalf("addr-spec params should be header params: %+v %v", a, err)
	}
	a, err = ParseAddress("Bob <sip:bob@example.com;transport=tcp>;tag=x;expires=30")
	if err != nil || a.Display != "Bob" || a.URI.Transport() != "tcp" || a.Tag() != "x" {
		t.Fatalf("bad name-addr %+v %v", a, err)
	}
	if a.String() != `"Bob" <sip:bob@example.com;transport=tcp>;tag=x;expires=30` {
		t.Errorf("unexpected string %q", a.String())
	}
	w, _ := ParseAddress("*")
	if !w.Wildcard || w.String() != "*" {
		t.Error("wildcard contact")
	}
}

func TestHeaderManipulation(t *testing.T) {
	m := NewRequest("OPTIONS", &URI{Scheme: "sip", Host: "h"})
	m.Add("Via", "SIP/2.0/UDP a;branch=z9hG4bK1")
	m.Add("To", "<sip:x@h>")
	m.Prepend("Via", "SIP/2.0/UDP b;branch=z9hG4bK2")
	if v := m.GetAll("via"); len(v) != 2 || !strings.Contains(v[0], " b;") {
		t.Fatalf("prepend failed: %v", v)
	}
	m.Set("Via", "SIP/2.0/UDP c;branch=z9hG4bK3")
	if v := m.GetAll("Via"); len(v) != 1 || !strings.Contains(v[0], " c;") {
		t.Fatalf("set failed: %v", v)
	}
	m.SetToTag("t1")
	m.SetToTag("t2")
	if to, _ := m.To(); to.Tag() != "t1" {
		t.Fatalf("to tag %q", to.Tag())
	}
	m.Del("Via")
	if m.Get("Via") != "" {
		t.Fatal("del failed")
	}
}

// RFC 2617 section 3.5 example.
func TestDigestRFC2617(t *testing.T) {
	ha1 := HA1("Mufasa", "testrealm@host.com", "Circle Of Life")
	got := DigestResponse(ha1, "dcd98b7102dd2f0e8b11d0f600bfb0c093", "00000001", "0a4f113b", "auth", "GET", "/dir/index.html")
	if got != "6629fae49393a05397450978507c4ef1" {
		t.Fatalf("digest mismatch: %s", got)
	}
}

func TestDigestRoundTrip(t *testing.T) {
	nm := NewNonceManager(time.Minute)
	chal, ok := ParseDigest(nm.Challenge("pbx.local", false))
	if !ok || chal["realm"] != "pbx.local" || chal["qop"] != "auth" {
		t.Fatalf("bad challenge %v", chal)
	}
	if nm.Check(chal["nonce"]) != NonceValid {
		t.Fatal("fresh nonce should be valid")
	}
	if nm.Check("deadbeef") != NonceInvalid {
		t.Fatal("garbage nonce should be invalid")
	}
	cred, ok := ParseDigest(BuildAuthorization(chal, "REGISTER", "sip:pbx.local", "1000", "pw"))
	if !ok {
		t.Fatal("unparsable authorization")
	}
	want := DigestResponse(HA1("1000", "pbx.local", "pw"), cred["nonce"], cred["nc"], cred["cnonce"], cred["qop"], "REGISTER", cred["uri"])
	if cred["response"] != want {
		t.Fatal("client digest does not verify")
	}
	if !nm.UseCount(cred["nonce"], 1) || nm.UseCount(cred["nonce"], 1) {
		t.Fatal("nonce count replay protection failed")
	}
	old := NewNonceManager(-time.Second)
	if old.Check(old.New()) != NonceStale {
		t.Fatal("expired nonce should be stale")
	}
}

func TestReadStreamMessage(t *testing.T) {
	raw := "\r\n\r\nOPTIONS sip:a@b SIP/2.0\r\nl: 3\r\n\r\nabcREGISTER sip:b SIP/2.0\r\n\r\n"
	r := bufio.NewReader(strings.NewReader(raw))
	pongs := 0
	b, err := readStreamMessage(r, func() { pongs++ })
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(b)
	if err != nil || m.Method != "OPTIONS" || string(m.Body) != "abc" || pongs != 1 {
		t.Fatalf("bad first message %v %v pongs=%d", m, err, pongs)
	}
	b, err = readStreamMessage(r, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := Parse(b); m.Method != "REGISTER" {
		t.Fatal("bad second message")
	}
}

type recorder struct {
	reqs chan *ServerTx
	acks chan *Message
}

func (r *recorder) OnRequest(tx *ServerTx)        { r.reqs <- tx }
func (r *recorder) OnAck(m *Message)              { r.acks <- m }
func (r *recorder) OnStrayResponse(resp *Message) {}

func newTestStack(t *testing.T, h Handler, accept func(Addr) bool) *Stack {
	t.Helper()
	var st *Stack
	cfg := Config{
		Transport: TransportConfig{ListenIP: "127.0.0.1", EnableTCP: true},
		Advertise: func(Addr) (string, int) { return "127.0.0.1", st.Port() },
		Accept:    accept,
		T1:        50 * time.Millisecond,
	}
	st, err := NewStack(cfg, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func buildReq(method string, target *Stack, cseq int) *Message {
	m := NewRequest(method, &URI{Scheme: "sip", User: "x", Host: "127.0.0.1", Port: target.Port()})
	m.Add("From", "<sip:a@127.0.0.1>;tag="+NewTag())
	m.Add("To", "<sip:x@127.0.0.1>")
	m.Add("Call-ID", NewCallID())
	m.Add("CSeq", strconv.Itoa(cseq)+" "+method)
	return m
}

func mustAP(t *testing.T, ip string, port int) netip.AddrPort {
	t.Helper()
	return netip.AddrPortFrom(netip.MustParseAddr(ip), uint16(port))
}

func TestTransactionsOverUDPAndTCP(t *testing.T) {
	for _, transport := range []string{"udp", "tcp"} {
		t.Run(transport, func(t *testing.T) {
			srvRec := &recorder{reqs: make(chan *ServerTx, 10), acks: make(chan *Message, 10)}
			server := newTestStack(t, srvRec, nil)
			client := newTestStack(t, &recorder{reqs: make(chan *ServerTx, 10), acks: make(chan *Message, 10)}, nil)
			dst := Addr{Transport: transport, AP: mustAP(t, "127.0.0.1", server.Port())}

			// Non-INVITE.
			tx, err := client.Request(buildReq("OPTIONS", server, 1), dst)
			if err != nil {
				t.Fatal(err)
			}
			stx := <-srvRec.reqs
			if stx.Request.Method != "OPTIONS" || stx.Request.Src.Transport != transport {
				t.Fatalf("unexpected server request %s via %s", stx.Request.Method, stx.Request.Src.Transport)
			}
			if err := stx.Respond(NewResponse(stx.Request, 200, "")); err != nil {
				t.Fatal(err)
			}
			resp := <-tx.Responses()
			if resp == nil || resp.StatusCode != 200 {
				t.Fatalf("expected 200, got %v", resp)
			}

			// INVITE rejected: the client transaction must ACK automatically
			// and the server transaction must absorb it.
			tx, err = client.Request(buildReq("INVITE", server, 2), dst)
			if err != nil {
				t.Fatal(err)
			}
			stx = <-srvRec.reqs
			_ = stx.Respond(NewResponse(stx.Request, 100, ""))
			r486 := NewResponse(stx.Request, 486, "")
			r486.SetToTag("abc")
			_ = stx.Respond(r486)
			var codes []int
			for r := range tx.Responses() {
				codes = append(codes, r.StatusCode)
			}
			if len(codes) != 2 || codes[0] != 100 || codes[1] != 486 {
				t.Fatalf("unexpected responses %v", codes)
			}
			select {
			case a := <-srvRec.acks:
				t.Fatalf("ACK for non-2xx leaked to TU: %s", a.Short())
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

func TestClientTimeout(t *testing.T) {
	client := newTestStack(t, &recorder{}, nil)
	// The sink drops everything it receives.
	sink := newTestStack(t, &recorder{}, func(Addr) bool { return false })
	tx, err := client.Request(buildReq("OPTIONS", sink, 1), Addr{Transport: "udp", AP: mustAP(t, "127.0.0.1", sink.Port())})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-tx.Responses():
		if r.StatusCode != 408 {
			t.Fatalf("expected synthetic 408, got %d", r.StatusCode)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no timeout")
	}
}

func TestStatsCountDrops(t *testing.T) {
	srvRec := &recorder{reqs: make(chan *ServerTx, 10), acks: make(chan *Message, 10)}
	server := newTestStack(t, srvRec, nil)
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: server.Port()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))                  // not SIP
	_, _ = c.Write([]byte("OPTIONS sip:x@h SIP/2.0\r\nCSeq: 1 OPTIONS\r\n\r\n")) // missing headers
	_, _ = c.Write(buildReq("OPTIONS", server, 1).Bytes())                       // missing Via
	tx, err := server.Request(buildReq("OPTIONS", server, 2), Addr{Transport: "udp", AP: mustAP(t, "127.0.0.1", server.Port())})
	if err != nil {
		t.Fatal(err)
	}
	stx := <-srvRec.reqs
	_ = stx.Respond(NewResponse(stx.Request, 200, ""))
	<-tx.Responses()
	st := server.Stats()
	if st.Received < 5 || st.Unparsable != 1 || st.Invalid != 2 || st.Sent < 2 || st.SendErrors != 0 {
		t.Fatalf("unexpected stats %+v", st)
	}
}

func TestReplyAddr(t *testing.T) {
	src := Addr{Transport: "udp", AP: netip.MustParseAddrPort("203.0.113.7:41234")}
	cases := []struct {
		via, want string
	}{
		{"SIP/2.0/UDP 203.0.113.7:5060;branch=z9hG4bK1", "203.0.113.7:5060"},              // a carrier: its Via port
		{"SIP/2.0/UDP 203.0.113.7;branch=z9hG4bK1", "203.0.113.7:5060"},                   // default port
		{"SIP/2.0/UDP sip.example.net:5070;branch=z9hG4bK1", "203.0.113.7:5070"},          // a name: source IP, Via port
		{"SIP/2.0/UDP 203.0.113.7:5060;rport=41234;branch=z9hG4bK1", "203.0.113.7:41234"}, // rport
		{"SIP/2.0/UDP 192.168.1.20:5060;branch=z9hG4bK1", "203.0.113.7:41234"},            // behind NAT
		{"SIP/2.0/UDP 203.0.113.7:41234;branch=z9hG4bK1", "203.0.113.7:41234"},
	}
	for _, c := range cases {
		v, err := ParseVia(c.via)
		if err != nil {
			t.Fatal(err)
		}
		if got := replyAddr(src, v); got.AP.String() != c.want {
			t.Errorf("%s: reply to %s, want %s", c.via, got.AP, c.want)
		}
	}
	tcp := Addr{Transport: "tcp", AP: src.AP}
	v, _ := ParseVia("SIP/2.0/TCP 203.0.113.7:5060;branch=z9hG4bK1")
	if got := replyAddr(tcp, v); got != tcp {
		t.Errorf("tcp: reply to %s", got)
	}
}
