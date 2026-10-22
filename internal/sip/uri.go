package sip

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Param is a single ";key[=value]" parameter.
type Param struct {
	Key   string
	Value string
	Flag  bool // parameter has no value, e.g. ";lr"
}

// Params is an ordered parameter list with case-insensitive keys.
type Params []Param

// Get returns the value of key and whether it is present.
func (p Params) Get(key string) (string, bool) {
	for _, kv := range p {
		if strings.EqualFold(kv.Key, key) {
			return kv.Value, true
		}
	}
	return "", false
}

// Has reports whether key is present.
func (p Params) Has(key string) bool {
	_, ok := p.Get(key)
	return ok
}

// Set sets key=value, replacing an existing value.
func (p *Params) Set(key, value string) {
	for i := range *p {
		if strings.EqualFold((*p)[i].Key, key) {
			(*p)[i].Value = value
			(*p)[i].Flag = false
			return
		}
	}
	*p = append(*p, Param{Key: key, Value: value})
}

// SetFlag sets a value-less parameter.
func (p *Params) SetFlag(key string) {
	for i := range *p {
		if strings.EqualFold((*p)[i].Key, key) {
			(*p)[i].Value = ""
			(*p)[i].Flag = true
			return
		}
	}
	*p = append(*p, Param{Key: key, Flag: true})
}

// Del removes key.
func (p *Params) Del(key string) {
	out := (*p)[:0]
	for _, kv := range *p {
		if !strings.EqualFold(kv.Key, key) {
			out = append(out, kv)
		}
	}
	*p = out
}

// Clone returns a copy.
func (p Params) Clone() Params {
	if p == nil {
		return nil
	}
	return append(Params(nil), p...)
}

func (p Params) write(b *strings.Builder, sep byte) {
	for _, kv := range p {
		b.WriteByte(sep)
		b.WriteString(kv.Key)
		if !kv.Flag {
			b.WriteByte('=')
			b.WriteString(kv.Value)
		}
	}
}

// parseParams parses "k=v;k2;k3=\"quoted;value\"" (without the leading separator).
func parseParams(s string, sep byte) Params {
	var out Params
	for _, part := range splitQuoted(s, sep) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.IndexByte(part, '='); i >= 0 {
			out = append(out, Param{Key: strings.TrimSpace(part[:i]), Value: strings.TrimSpace(part[i+1:])})
		} else {
			out = append(out, Param{Key: part, Flag: true})
		}
	}
	return out
}

// splitQuoted splits s on sep, ignoring separators inside double quotes or
// angle brackets.
func splitQuoted(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote:
			if c == '\\' {
				i++
			} else if c == '"' {
				inQuote = false
			}
		case c == '"':
			inQuote = true
		case c == '<':
			depth++
		case c == '>':
			if depth > 0 {
				depth--
			}
		case c == sep && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// URI is a SIP, SIPS or TEL URI.
type URI struct {
	Scheme   string
	User     string
	Password string
	Host     string // IPv6 hosts keep their brackets
	Port     int    // 0 when absent
	Params   Params
	Headers  string // raw header part after '?'
}

// ParseURI parses a sip:, sips: or tel: URI.
func ParseURI(s string) (*URI, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return nil, fmt.Errorf("sip: invalid uri %q", s)
	}
	u := &URI{Scheme: strings.ToLower(s[:i])}
	rest := s[i+1:]
	if u.Scheme == "tel" {
		number, params, _ := strings.Cut(rest, ";")
		u.User = number
		if params != "" {
			u.Params = parseParams(params, ';')
		}
		if u.User == "" {
			return nil, fmt.Errorf("sip: invalid tel uri %q", s)
		}
		return u, nil
	}
	if u.Scheme != "sip" && u.Scheme != "sips" {
		return nil, fmt.Errorf("sip: unsupported uri scheme %q", u.Scheme)
	}
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		u.Headers = rest[q+1:]
		rest = rest[:q]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		userinfo := rest[:at]
		rest = rest[at+1:]
		if c := strings.IndexByte(userinfo, ':'); c >= 0 {
			u.User, u.Password = userinfo[:c], userinfo[c+1:]
		} else {
			u.User = userinfo
		}
	}
	hostport := rest
	if sc := strings.IndexByte(rest, ';'); sc >= 0 {
		hostport = rest[:sc]
		u.Params = parseParams(rest[sc+1:], ';')
	}
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return nil, fmt.Errorf("sip: invalid uri %q: %w", s, err)
	}
	u.Host, u.Port = host, port
	return u, nil
}

func splitHostPort(hp string) (string, int, error) {
	hp = strings.TrimSpace(hp)
	if hp == "" {
		return "", 0, errors.New("empty host")
	}
	if hp[0] == '[' {
		end := strings.IndexByte(hp, ']')
		if end < 0 {
			return "", 0, errors.New("unterminated IPv6 host")
		}
		host, rest := hp[:end+1], hp[end+1:]
		if rest == "" {
			return host, 0, nil
		}
		if rest[0] != ':' {
			return "", 0, errors.New("garbage after IPv6 host")
		}
		p, err := parsePort(rest[1:])
		return host, p, err
	}
	if c := strings.LastIndexByte(hp, ':'); c >= 0 {
		p, err := parsePort(hp[c+1:])
		return hp[:c], p, err
	}
	return hp, 0, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return p, nil
}

// String renders the URI.
func (u *URI) String() string {
	var b strings.Builder
	b.WriteString(u.Scheme)
	b.WriteByte(':')
	if u.Scheme == "tel" {
		b.WriteString(u.User)
		u.Params.write(&b, ';')
		return b.String()
	}
	if u.User != "" {
		b.WriteString(u.User)
		if u.Password != "" {
			b.WriteByte(':')
			b.WriteString(u.Password)
		}
		b.WriteByte('@')
	}
	b.WriteString(u.Host)
	if u.Port > 0 {
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(u.Port))
	}
	u.Params.write(&b, ';')
	if u.Headers != "" {
		b.WriteByte('?')
		b.WriteString(u.Headers)
	}
	return b.String()
}

// Clone returns a deep copy.
func (u *URI) Clone() *URI {
	if u == nil {
		return nil
	}
	c := *u
	c.Params = u.Params.Clone()
	return &c
}

// Transport returns the lower-case transport parameter ("udp" by default).
func (u *URI) Transport() string {
	if t, ok := u.Params.Get("transport"); ok && t != "" {
		return strings.ToLower(t)
	}
	if u.Scheme == "sips" {
		return "tls"
	}
	return "udp"
}

// HostAddr returns the host as an IP address when it is a literal IP.
func (u *URI) HostAddr() (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.Trim(u.Host, "[]"))
	return a, err == nil
}

// Address is a name-addr or addr-spec as found in From, To, Contact, Route...
type Address struct {
	Display  string // unquoted display name
	URI      *URI
	Params   Params // header parameters (tag, expires, q, ...)
	Wildcard bool   // "Contact: *"
}

// ParseAddress parses a name-addr / addr-spec.
func ParseAddress(s string) (*Address, error) {
	s = strings.TrimSpace(s)
	if s == "*" {
		return &Address{Wildcard: true}, nil
	}
	a := &Address{}
	if strings.HasPrefix(s, "\"") {
		end := -1
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("sip: unterminated display name in %q", s)
		}
		a.Display = unescapeQuoted(s[1:end])
		s = strings.TrimSpace(s[end+1:])
		if !strings.HasPrefix(s, "<") {
			return nil, fmt.Errorf("sip: expected '<' after display name in %q", s)
		}
	}
	var uriPart, rest string
	if lt := strings.IndexByte(s, '<'); lt >= 0 {
		if a.Display == "" {
			a.Display = strings.TrimSpace(s[:lt])
		}
		gt := strings.IndexByte(s[lt:], '>')
		if gt < 0 {
			return nil, fmt.Errorf("sip: unterminated '<' in %q", s)
		}
		uriPart = s[lt+1 : lt+gt]
		rest = s[lt+gt+1:]
	} else {
		// addr-spec form: trailing parameters belong to the header.
		uriPart = s
		if sc := strings.IndexByte(s, ';'); sc >= 0 {
			uriPart, rest = s[:sc], s[sc:]
		}
	}
	u, err := ParseURI(uriPart)
	if err != nil {
		return nil, err
	}
	a.URI = u
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, ";") {
		a.Params = parseParams(rest[1:], ';')
	}
	return a, nil
}

// String renders the address in name-addr form.
func (a *Address) String() string {
	if a.Wildcard {
		return "*"
	}
	var b strings.Builder
	if a.Display != "" {
		b.WriteByte('"')
		b.WriteString(escapeQuoted(a.Display))
		b.WriteString("\" ")
	}
	b.WriteByte('<')
	b.WriteString(a.URI.String())
	b.WriteByte('>')
	a.Params.write(&b, ';')
	return b.String()
}

// Tag returns the tag parameter.
func (a *Address) Tag() string {
	t, _ := a.Params.Get("tag")
	return t
}

// Clone returns a deep copy.
func (a *Address) Clone() *Address {
	if a == nil {
		return nil
	}
	c := *a
	c.URI = a.URI.Clone()
	c.Params = a.Params.Clone()
	return &c
}

func unescapeQuoted(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func escapeQuoted(s string) string {
	if !strings.ContainsAny(s, "\"\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
