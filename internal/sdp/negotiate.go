package sdp

import (
	"strconv"
	"strings"
)

// Local describes our side of a media stream.
type Local struct {
	IP        string
	Port      int
	SessionID string
	Version   uint64
}

func (l Local) addrType() string {
	if strings.Contains(l.IP, ":") {
		return "IP6"
	}
	return "IP4"
}

func (l Local) session() *Session {
	return &Session{
		Origin:   Origin{Username: "RingQswitch", SessionID: l.SessionID, Version: l.Version, NetType: "IN", AddrType: l.addrType(), Address: l.IP},
		Name:     "RingQswitch",
		ConnAddr: l.IP,
		ConnType: l.addrType(),
	}
}

// DTMFCodec is the default RFC 4733 telephone-event format we offer.
var DTMFCodec = Codec{PT: 101, Name: "telephone-event", Rate: 8000, Channels: 1, Fmtp: "0-16"}

// NewOffer builds an audio offer with the given codecs.
func NewOffer(l Local, codecs []Codec, direction string) *Session {
	s := l.session()
	m := &Media{Type: "audio", Port: l.Port, Proto: "RTP/AVP"}
	m.SetCodecs(codecs)
	m.Attributes = append(m.Attributes, "ptime:20", orDefault(direction, "sendrecv"))
	s.Media = []*Media{m}
	return s
}

// OfferFromPeer builds an offer for the second leg of a bridged call that
// passes through the codecs offered by the first leg (no transcoding).
// Secure-only offers return nil because we cannot terminate SRTP.
func OfferFromPeer(peer *Session, l Local) *Session {
	pa, _ := peer.Audio()
	if pa == nil || strings.Contains(pa.Proto, "SAVP") {
		return nil
	}
	codecs := pa.Codecs()
	if len(codecs) == 0 {
		return nil
	}
	s := l.session()
	m := &Media{Type: "audio", Port: l.Port, Proto: "RTP/AVP"}
	m.SetCodecs(codecs)
	for _, a := range []string{"ptime", "maxptime"} {
		if v, ok := pa.Attr(a); ok {
			m.Attributes = append(m.Attributes, a+":"+v)
		}
	}
	m.Attributes = append(m.Attributes, peer.Direction(pa))
	s.Media = []*Media{m}
	return s
}

// Answer builds an answer to offer accepting only the first audio stream
// with the given codecs (which must use the offer's payload types). All other
// streams are rejected with port 0 as RFC 3264 requires.
func Answer(offer *Session, l Local, codecs []Codec, direction string) *Session {
	s := l.session()
	_, audioIdx := offer.Audio()
	for i, om := range offer.Media {
		m := &Media{Type: om.Type, Proto: om.Proto}
		if i != audioIdx || len(codecs) == 0 {
			m.Port = 0
			m.Formats = append([]string(nil), om.Formats...)
			if len(m.Formats) == 0 {
				m.Formats = []string{"0"}
			}
			s.Media = append(s.Media, m)
			continue
		}
		m.Port = l.Port
		m.Proto = "RTP/AVP"
		m.SetCodecs(codecs)
		ptime := "20"
		if v, ok := om.Attr("ptime"); ok {
			ptime = v
		}
		m.Attributes = append(m.Attributes, "ptime:"+ptime, orDefault(direction, "sendrecv"))
		s.Media = append(s.Media, m)
	}
	return s
}

// MapToOffer returns the entries of offered that match chosen (by encoding
// name and rate) in the order of chosen, i.e. it translates a peer's answer
// into the payload type numbering of our offerer.
func MapToOffer(offered []Codec, chosen []Codec) []Codec {
	var out []Codec
	for _, c := range chosen {
		for _, o := range offered {
			if o.Same(c) && !containsPT(out, o.PT) {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

func containsPT(list []Codec, pt int) bool {
	for _, c := range list {
		if c.PT == pt {
			return true
		}
	}
	return false
}

// Select picks the first offered codec whose name is in supported, plus
// telephone-event when offered. It returns nil when nothing matches.
func Select(offered []Codec, supported []string) []Codec {
	var out []Codec
	for _, o := range offered {
		if o.IsDTMF() {
			continue
		}
		for _, name := range supported {
			if strings.EqualFold(o.Name, name) && o.Rate == 8000 {
				out = append(out, o)
				break
			}
		}
		if len(out) > 0 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	for _, o := range offered {
		if o.IsDTMF() && o.Rate == 8000 {
			out = append(out, o)
			break
		}
	}
	return out
}

// RTCPPort returns the RTCP port for m (a=rtcp or RTP port + 1).
func (m *Media) RTCPPort() int {
	if v, ok := m.Attr("rtcp"); ok {
		f := strings.Fields(v)
		if len(f) > 0 {
			if p, err := strconv.Atoi(f[0]); err == nil {
				return p
			}
		}
	}
	return m.Port + 1
}
