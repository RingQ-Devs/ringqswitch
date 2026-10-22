// Package sdp implements the subset of RFC 4566 / RFC 3264 needed by a
// media-relaying B2BUA.
package sdp

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Origin is the o= line.
type Origin struct {
	Username  string
	SessionID string
	Version   uint64
	NetType   string
	AddrType  string
	Address   string
}

// Session is a parsed SDP description.
type Session struct {
	Origin     Origin
	Name       string
	ConnAddr   string // session level c= address
	ConnType   string // IP4 / IP6
	Bandwidth  []string
	Times      []string
	Attributes []string // session level a= values (without "a=")
	Media      []*Media
}

// Media is an m= section.
type Media struct {
	Type       string // audio, video, ...
	Port       int
	NumPorts   int
	Proto      string // RTP/AVP, RTP/SAVP, ...
	Formats    []string
	ConnAddr   string
	ConnType   string
	Bandwidth  []string
	Attributes []string
}

// Codec describes one payload format of a media section.
type Codec struct {
	PT       int
	Name     string
	Rate     int
	Channels int
	Fmtp     string
}

// Parse parses an SDP body.
func Parse(b []byte) (*Session, error) {
	s := &Session{}
	var cur *Media
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	sawVersion := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) < 2 || line[1] != '=' {
			return nil, fmt.Errorf("sdp: bad line %q", line)
		}
		typ, val := line[0], line[2:]
		switch typ {
		case 'v':
			sawVersion = true
		case 'o':
			f := strings.Fields(val)
			if len(f) != 6 {
				return nil, fmt.Errorf("sdp: bad origin %q", val)
			}
			ver, _ := strconv.ParseUint(f[2], 10, 64)
			s.Origin = Origin{f[0], f[1], ver, f[3], f[4], f[5]}
		case 's':
			s.Name = val
		case 'c':
			f := strings.Fields(val)
			if len(f) != 3 {
				return nil, fmt.Errorf("sdp: bad connection %q", val)
			}
			addr, _, _ := strings.Cut(f[2], "/") // strip multicast TTL
			if cur != nil {
				cur.ConnAddr, cur.ConnType = addr, f[1]
			} else {
				s.ConnAddr, s.ConnType = addr, f[1]
			}
		case 'b':
			if cur != nil {
				cur.Bandwidth = append(cur.Bandwidth, val)
			} else {
				s.Bandwidth = append(s.Bandwidth, val)
			}
		case 't':
			s.Times = append(s.Times, val)
		case 'm':
			f := strings.Fields(val)
			if len(f) < 3 {
				return nil, fmt.Errorf("sdp: bad media %q", val)
			}
			m := &Media{Type: f[0], Proto: f[2], Formats: f[3:]}
			portStr, num, hasNum := strings.Cut(f[1], "/")
			p, err := strconv.Atoi(portStr)
			if err != nil || p < 0 || p > 65535 {
				return nil, fmt.Errorf("sdp: bad media port %q", f[1])
			}
			m.Port = p
			if hasNum {
				m.NumPorts, _ = strconv.Atoi(num)
			}
			s.Media = append(s.Media, m)
			cur = m
		case 'a':
			if cur != nil {
				cur.Attributes = append(cur.Attributes, val)
			} else {
				s.Attributes = append(s.Attributes, val)
			}
		}
	}
	if !sawVersion {
		return nil, fmt.Errorf("sdp: missing v= line")
	}
	return s, nil
}

// Marshal renders the description.
func (s *Session) Marshal() []byte {
	var b bytes.Buffer
	b.WriteString("v=0\r\n")
	o := s.Origin
	fmt.Fprintf(&b, "o=%s %s %d %s %s %s\r\n", orDash(o.Username), o.SessionID, o.Version, orDefault(o.NetType, "IN"), orDefault(o.AddrType, "IP4"), o.Address)
	fmt.Fprintf(&b, "s=%s\r\n", orDash(s.Name))
	if s.ConnAddr != "" {
		fmt.Fprintf(&b, "c=IN %s %s\r\n", orDefault(s.ConnType, "IP4"), s.ConnAddr)
	}
	for _, bw := range s.Bandwidth {
		fmt.Fprintf(&b, "b=%s\r\n", bw)
	}
	if len(s.Times) == 0 {
		b.WriteString("t=0 0\r\n")
	}
	for _, t := range s.Times {
		fmt.Fprintf(&b, "t=%s\r\n", t)
	}
	for _, a := range s.Attributes {
		fmt.Fprintf(&b, "a=%s\r\n", a)
	}
	for _, m := range s.Media {
		port := strconv.Itoa(m.Port)
		if m.NumPorts > 1 {
			port += "/" + strconv.Itoa(m.NumPorts)
		}
		fmt.Fprintf(&b, "m=%s %s %s %s\r\n", m.Type, port, m.Proto, strings.Join(m.Formats, " "))
		if m.ConnAddr != "" {
			fmt.Fprintf(&b, "c=IN %s %s\r\n", orDefault(m.ConnType, "IP4"), m.ConnAddr)
		}
		for _, bw := range m.Bandwidth {
			fmt.Fprintf(&b, "b=%s\r\n", bw)
		}
		for _, a := range m.Attributes {
			fmt.Fprintf(&b, "a=%s\r\n", a)
		}
	}
	return b.Bytes()
}

func orDash(s string) string { return orDefault(s, "-") }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Audio returns the first active audio section and its index, or nil.
func (s *Session) Audio() (*Media, int) {
	for i, m := range s.Media {
		if m.Type == "audio" && m.Port != 0 {
			return m, i
		}
	}
	return nil, -1
}

// Conn returns the connection address that applies to m.
func (s *Session) Conn(m *Media) string {
	if m.ConnAddr != "" {
		return m.ConnAddr
	}
	return s.ConnAddr
}

// Attr returns the value of the first attribute named name ("" if the
// attribute is a flag) and whether it exists.
func (m *Media) Attr(name string) (string, bool) {
	for _, a := range m.Attributes {
		k, v, _ := strings.Cut(a, ":")
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// Direction returns sendrecv, sendonly, recvonly or inactive for m,
// considering session level attributes as a fallback.
func (s *Session) Direction(m *Media) string {
	for _, list := range [][]string{m.Attributes, s.Attributes} {
		for _, a := range list {
			switch a {
			case "sendrecv", "sendonly", "recvonly", "inactive":
				return a
			}
		}
	}
	return "sendrecv"
}

// ReverseDirection returns the direction an answerer uses for an offered one.
func ReverseDirection(d string) string {
	switch d {
	case "sendonly":
		return "recvonly"
	case "recvonly":
		return "sendonly"
	case "inactive":
		return "inactive"
	}
	return "sendrecv"
}

var staticPT = map[int]Codec{
	0:  {PT: 0, Name: "PCMU", Rate: 8000, Channels: 1},
	3:  {PT: 3, Name: "GSM", Rate: 8000, Channels: 1},
	4:  {PT: 4, Name: "G723", Rate: 8000, Channels: 1},
	8:  {PT: 8, Name: "PCMA", Rate: 8000, Channels: 1},
	9:  {PT: 9, Name: "G722", Rate: 8000, Channels: 1},
	13: {PT: 13, Name: "CN", Rate: 8000, Channels: 1},
	18: {PT: 18, Name: "G729", Rate: 8000, Channels: 1},
}

// Codecs returns the payload formats of m in preference order.
func (m *Media) Codecs() []Codec {
	byPT := map[int]*Codec{}
	var out []*Codec
	for _, f := range m.Formats {
		pt, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		c := &Codec{PT: pt, Channels: 1}
		if st, ok := staticPT[pt]; ok {
			*c = st
		}
		byPT[pt] = c
		out = append(out, c)
	}
	for _, a := range m.Attributes {
		k, v, _ := strings.Cut(a, ":")
		switch strings.ToLower(k) {
		case "rtpmap":
			ptStr, enc, _ := strings.Cut(v, " ")
			pt, err := strconv.Atoi(ptStr)
			if c := byPT[pt]; err == nil && c != nil {
				parts := strings.Split(strings.TrimSpace(enc), "/")
				c.Name = parts[0]
				if len(parts) > 1 {
					c.Rate, _ = strconv.Atoi(parts[1])
				}
				if len(parts) > 2 {
					c.Channels, _ = strconv.Atoi(parts[2])
				}
			}
		case "fmtp":
			ptStr, params, _ := strings.Cut(v, " ")
			if pt, err := strconv.Atoi(ptStr); err == nil && byPT[pt] != nil {
				byPT[pt].Fmtp = strings.TrimSpace(params)
			}
		}
	}
	res := make([]Codec, 0, len(out))
	for _, c := range out {
		if c.Name != "" {
			res = append(res, *c)
		}
	}
	return res
}

// Same reports whether two codecs describe the same encoding.
func (c Codec) Same(o Codec) bool {
	return strings.EqualFold(c.Name, o.Name) && c.Rate == o.Rate
}

// IsDTMF reports whether c is RFC 4733 telephone-event.
func (c Codec) IsDTMF() bool { return strings.EqualFold(c.Name, "telephone-event") }

// String renders "NAME/rate".
func (c Codec) String() string { return fmt.Sprintf("%s/%d", c.Name, c.Rate) }

// SetCodecs replaces the formats and codec attributes of m, keeping other
// attributes except those listed in drop.
func (m *Media) SetCodecs(codecs []Codec) {
	var keep []string
	for _, a := range m.Attributes {
		k, _, _ := strings.Cut(a, ":")
		switch strings.ToLower(k) {
		case "rtpmap", "fmtp":
			continue
		}
		keep = append(keep, a)
	}
	m.Formats = m.Formats[:0]
	var attrs []string
	for _, c := range codecs {
		m.Formats = append(m.Formats, strconv.Itoa(c.PT))
		enc := fmt.Sprintf("rtpmap:%d %s/%d", c.PT, c.Name, c.Rate)
		if c.Channels > 1 {
			enc += "/" + strconv.Itoa(c.Channels)
		}
		attrs = append(attrs, enc)
		if c.Fmtp != "" {
			attrs = append(attrs, fmt.Sprintf("fmtp:%d %s", c.PT, c.Fmtp))
		}
	}
	m.Attributes = append(attrs, keep...)
}
