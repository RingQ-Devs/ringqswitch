package sdp

import (
	"strings"
	"testing"
)

const phoneOffer = "v=0\r\n" +
	"o=- 3868 3868 IN IP4 192.168.1.50\r\n" +
	"s=Softphone\r\n" +
	"c=IN IP4 192.168.1.50\r\n" +
	"t=0 0\r\n" +
	"m=audio 40000 RTP/AVP 9 0 8 96 101\r\n" +
	"a=rtpmap:96 opus/48000/2\r\n" +
	"a=fmtp:96 useinbandfec=1\r\n" +
	"a=rtpmap:101 telephone-event/8000\r\n" +
	"a=fmtp:101 0-15\r\n" +
	"a=ptime:20\r\n" +
	"a=sendonly\r\n" +
	"m=video 40002 RTP/AVP 97\r\n" +
	"a=rtpmap:97 H264/90000\r\n"

func TestParseAndCodecs(t *testing.T) {
	s, err := Parse([]byte(phoneOffer))
	if err != nil {
		t.Fatal(err)
	}
	a, idx := s.Audio()
	if a == nil || idx != 0 || a.Port != 40000 || s.Conn(a) != "192.168.1.50" {
		t.Fatalf("bad audio section %+v", a)
	}
	codecs := a.Codecs()
	names := []string{}
	for _, c := range codecs {
		names = append(names, c.String())
	}
	if strings.Join(names, ",") != "G722/8000,PCMU/8000,PCMA/8000,opus/48000,telephone-event/8000" {
		t.Fatalf("unexpected codecs %v", names)
	}
	if codecs[3].Channels != 2 || codecs[3].Fmtp != "useinbandfec=1" {
		t.Fatalf("opus details lost: %+v", codecs[3])
	}
	if s.Direction(a) != "sendonly" || ReverseDirection("sendonly") != "recvonly" {
		t.Fatal("direction handling")
	}
	re, err := Parse(s.Marshal())
	if err != nil || len(re.Media) != 2 || len(re.Media[0].Codecs()) != 5 {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := Parse([]byte("o=bad\r\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestSelectAndAnswer(t *testing.T) {
	offer, _ := Parse([]byte(phoneOffer))
	a, _ := offer.Audio()
	chosen := Select(a.Codecs(), []string{"PCMU", "PCMA"})
	if len(chosen) != 2 || chosen[0].Name != "PCMU" || !chosen[1].IsDTMF() || chosen[1].PT != 101 {
		t.Fatalf("unexpected selection %+v", chosen)
	}
	ans := Answer(offer, Local{IP: "10.0.0.1", Port: 20000, SessionID: "1", Version: 1}, chosen, ReverseDirection(offer.Direction(a)))
	if len(ans.Media) != 2 {
		t.Fatalf("answer must mirror m-lines, got %d", len(ans.Media))
	}
	if ans.Media[1].Port != 0 {
		t.Fatal("video must be rejected")
	}
	aa, _ := ans.Audio()
	if aa.Port != 20000 || ans.ConnAddr != "10.0.0.1" || ans.Direction(aa) != "recvonly" {
		t.Fatalf("bad answer audio %+v", aa)
	}
	if got := strings.Join(aa.Formats, " "); got != "0 101" {
		t.Fatalf("answer formats %q", got)
	}
	if Select(a.Codecs(), []string{"G729"}) != nil {
		t.Fatal("expected no match")
	}
}

func TestOfferFromPeerAndMap(t *testing.T) {
	offer, _ := Parse([]byte(phoneOffer))
	fwd := OfferFromPeer(offer, Local{IP: "10.0.0.1", Port: 30000, SessionID: "9", Version: 1})
	if fwd == nil || len(fwd.Media) != 1 {
		t.Fatal("expected audio-only forwarded offer")
	}
	fa, _ := fwd.Audio()
	if fa.Port != 30000 || len(fa.Codecs()) != 5 {
		t.Fatalf("forwarded offer lost codecs: %+v", fa)
	}
	// Peer answers G722 + telephone-event with different PT numbering.
	peerAnswer := []Codec{{PT: 9, Name: "G722", Rate: 8000}, {PT: 100, Name: "telephone-event", Rate: 8000}}
	mapped := MapToOffer(offer.Media[0].Codecs(), peerAnswer)
	if len(mapped) != 2 || mapped[0].PT != 9 || mapped[1].PT != 101 {
		t.Fatalf("bad mapping %+v", mapped)
	}
	secure, _ := Parse([]byte(strings.Replace(phoneOffer, "RTP/AVP 9", "RTP/SAVP 9", 1)))
	if OfferFromPeer(secure, Local{IP: "1.1.1.1", Port: 1}) != nil {
		t.Fatal("SAVP offers cannot be relayed")
	}
}

func TestT38(t *testing.T) {
	offer, err := Parse([]byte("v=0\r\no=- 1 2 IN IP4 198.51.100.9\r\ns=-\r\nc=IN IP4 198.51.100.9\r\nt=0 0\r\n" +
		"m=audio 0 RTP/AVP 8\r\nm=image 30000 udptl t38\r\na=T38FaxVersion:0\r\na=T38MaxBitRate:14400\r\n" +
		"a=T38FaxFillBitRemoval:0\r\na=t38faxratemanagement:transferredTCF\r\na=T38FaxMaxBuffer:262\r\n" +
		"a=T38FaxMaxDatagram:176\r\na=T38FaxUdpEC:t38UDPRedundancy\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := offer.Audio(); a != nil {
		t.Fatal("audio is declined")
	}
	img, idx := offer.Image()
	if img == nil || idx != 1 || img.Port != 30000 {
		t.Fatalf("image %+v %d", img, idx)
	}
	got := img.T38()
	want := T38{Version: 0, MaxBitRate: 14400, RateManagement: "transferredTCF", MaxBuffer: 262, MaxDatagram: 176, UDPEC: "t38UDPRedundancy"}
	if got != want {
		t.Fatalf("t38 %+v", got)
	}
	ans := AnswerT38(offer, Local{IP: "203.0.113.1", Port: 20000, SessionID: "1", Version: 3},
		T38{MaxBitRate: 9600, MaxBuffer: 2000, MaxDatagram: 400, UDPEC: "t38UDPRedundancy"})
	body := string(ans.Marshal())
	for _, s := range []string{"m=audio 0 RTP/AVP 8\r\n", "m=image 20000 udptl t38\r\n", "a=T38FaxVersion:0\r\n",
		"a=T38MaxBitRate:9600\r\n", "a=T38FaxRateManagement:transferredTCF\r\n", "a=T38FaxUdpEC:t38UDPRedundancy\r\n"} {
		if !strings.Contains(body, s) {
			t.Fatalf("answer lacks %q:\n%s", s, body)
		}
	}
}
