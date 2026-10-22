package sdp

import (
	"strconv"
	"strings"
)

// T.38 fax (RFC 3362, T.38 Annex D): an m=image section over UDPTL.

// T38 holds the T.38 attributes of an m=image section.
type T38 struct {
	Version        int
	MaxBitRate     int
	RateManagement string // transferredTCF or localTCF
	MaxBuffer      int
	MaxDatagram    int
	UDPEC          string // t38UDPRedundancy, t38UDPFEC or t38UDPNoEC
	FillBitRemoval bool
	MMR, JBIG      bool
}

// Image returns the first active T.38 (udptl) section and its index, or nil.
func (s *Session) Image() (*Media, int) {
	for i, m := range s.Media {
		if m.Type == "image" && m.Port != 0 && strings.EqualFold(m.Proto, "udptl") {
			return m, i
		}
	}
	return nil, -1
}

// T38 reads the T.38 attributes of m.
func (m *Media) T38() T38 {
	var t T38
	num := func(name string) int {
		v, _ := m.Attr(name)
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	flag := func(name string) bool {
		v, ok := m.Attr(name)
		return ok && strings.TrimSpace(v) != "0"
	}
	t.Version = num("T38FaxVersion")
	t.MaxBitRate = num("T38MaxBitRate")
	t.RateManagement, _ = m.Attr("T38FaxRateManagement")
	t.MaxBuffer = num("T38FaxMaxBuffer")
	t.MaxDatagram = num("T38FaxMaxDatagram")
	t.UDPEC, _ = m.Attr("T38FaxUdpEC")
	t.FillBitRemoval = flag("T38FaxFillBitRemoval")
	t.MMR = flag("T38FaxTranscodingMMR")
	t.JBIG = flag("T38FaxTranscodingJBIG")
	return t
}

// Attributes renders t as a=... values (without "a=").
func (t T38) Attributes() []string {
	a := []string{"T38FaxVersion:" + strconv.Itoa(t.Version)}
	if t.MaxBitRate > 0 {
		a = append(a, "T38MaxBitRate:"+strconv.Itoa(t.MaxBitRate))
	}
	if t.FillBitRemoval {
		a = append(a, "T38FaxFillBitRemoval")
	}
	if t.MMR {
		a = append(a, "T38FaxTranscodingMMR")
	}
	if t.JBIG {
		a = append(a, "T38FaxTranscodingJBIG")
	}
	a = append(a, "T38FaxRateManagement:"+orDefault(t.RateManagement, "transferredTCF"))
	if t.MaxBuffer > 0 {
		a = append(a, "T38FaxMaxBuffer:"+strconv.Itoa(t.MaxBuffer))
	}
	if t.MaxDatagram > 0 {
		a = append(a, "T38FaxMaxDatagram:"+strconv.Itoa(t.MaxDatagram))
	}
	if t.UDPEC != "" {
		a = append(a, "T38FaxUdpEC:"+t.UDPEC)
	}
	return a
}

// AnswerT38 answers an offer's T.38 section with ours (every other
// section declined).
func AnswerT38(offer *Session, l Local, t T38) *Session {
	s := l.session()
	_, idx := offer.Image()
	for i, om := range offer.Media {
		m := &Media{Type: om.Type, Proto: om.Proto, Formats: append([]string(nil), om.Formats...)}
		if i == idx {
			m.Port = l.Port
			m.Formats = []string{"t38"}
			m.Attributes = t.Attributes()
		} else if len(m.Formats) == 0 {
			m.Formats = []string{"0"}
		}
		s.Media = append(s.Media, m)
	}
	return s
}
