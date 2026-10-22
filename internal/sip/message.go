package sip

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"net/textproto"
	"strconv"
	"strings"
)

// Addr identifies a transport-level peer.
type Addr struct {
	Transport string // "udp" or "tcp"
	AP        netip.AddrPort
	// Local is the extra UDP listening port (a second SIP profile, e.g.
	// 5080) a message arrived on, or should leave from; 0 = the main port.
	Local uint16
}

func (a Addr) String() string { return a.Transport + ":" + a.AP.String() }

// IsValid reports whether the address is usable.
func (a Addr) IsValid() bool { return a.Transport != "" && a.AP.IsValid() }

// Reliable reports whether the transport is stream based.
func (a Addr) Reliable() bool { return a.Transport != "udp" }

// Header is a single header line. Multi-valued headers (Via, Route, ...) are
// stored as one Header per value.
type Header struct {
	Name  string
	Value string
}

// Message is a SIP request or response.
type Message struct {
	Method     string // requests only
	RequestURI *URI   // requests only
	StatusCode int    // responses only
	Reason     string // responses only
	Headers    []Header
	Body       []byte

	// Src is the peer a received message came from.
	Src Addr
}

var compactForms = map[string]string{
	"i": "Call-ID", "m": "Contact", "e": "Content-Encoding", "l": "Content-Length",
	"c": "Content-Type", "f": "From", "s": "Subject", "k": "Supported", "t": "To",
	"v": "Via", "r": "Refer-To", "b": "Referred-By", "o": "Event", "u": "Allow-Events",
	"x": "Session-Expires", "a": "Accept-Contact", "j": "Reject-Contact",
	"d": "Request-Disposition", "y": "Identity",
}

var specialNames = map[string]string{
	"call-id": "Call-ID", "cseq": "CSeq", "www-authenticate": "WWW-Authenticate",
	"rseq": "RSeq", "rack": "RAck", "mime-version": "MIME-Version",
	"sip-etag": "SIP-ETag", "sip-if-match": "SIP-If-Match",
	"p-asserted-identity": "P-Asserted-Identity", "p-preferred-identity": "P-Preferred-Identity",
}

// multiValue lists headers whose comma separated values are split on parse.
var multiValue = map[string]bool{"Via": true, "Contact": true, "Route": true, "Record-Route": true}

// CanonicalName returns the canonical form of a header name, expanding
// compact forms.
func CanonicalName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if long, ok := compactForms[lower]; ok {
		return long
	}
	if n, ok := specialNames[lower]; ok {
		return n
	}
	return textproto.CanonicalMIMEHeaderKey(lower)
}

// ErrMalformed is returned for messages that cannot be parsed.
var ErrMalformed = errors.New("sip: malformed message")

// Parse parses a complete SIP message.
func Parse(data []byte) (*Message, error) {
	headEnd, sepLen := bytes.Index(data, []byte("\r\n\r\n")), 4
	if headEnd < 0 {
		headEnd, sepLen = bytes.Index(data, []byte("\n\n")), 2
	}
	var head string
	var body []byte
	if headEnd < 0 {
		head = string(bytes.TrimRight(data, "\r\n"))
	} else {
		head = string(data[:headEnd])
		body = data[headEnd+sepLen:]
	}
	lines := strings.Split(head, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if len(lines) == 0 || lines[0] == "" {
		return nil, ErrMalformed
	}
	m := &Message{}
	if err := m.parseStartLine(lines[0]); err != nil {
		return nil, err
	}
	// Unfold continuation lines.
	var unfolded []string
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		if (l[0] == ' ' || l[0] == '\t') && len(unfolded) > 0 {
			unfolded[len(unfolded)-1] += " " + strings.TrimSpace(l)
			continue
		}
		unfolded = append(unfolded, l)
	}
	for _, l := range unfolded {
		colon := strings.IndexByte(l, ':')
		if colon <= 0 {
			return nil, fmt.Errorf("%w: bad header line %q", ErrMalformed, l)
		}
		name := CanonicalName(l[:colon])
		value := strings.TrimSpace(l[colon+1:])
		if multiValue[name] && value != "*" {
			for _, v := range splitQuoted(value, ',') {
				if v = strings.TrimSpace(v); v != "" {
					m.Headers = append(m.Headers, Header{name, v})
				}
			}
			continue
		}
		m.Headers = append(m.Headers, Header{name, value})
	}
	if cl := m.Get("Content-Length"); cl != "" {
		n, err := strconv.Atoi(cl)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%w: bad Content-Length %q", ErrMalformed, cl)
		}
		if n > len(body) {
			return nil, fmt.Errorf("%w: truncated body (%d of %d bytes)", ErrMalformed, len(body), n)
		}
		body = body[:n]
	}
	if len(body) > 0 {
		m.Body = append([]byte(nil), body...)
	}
	return m, nil
}

func (m *Message) parseStartLine(line string) error {
	if strings.HasPrefix(strings.ToUpper(line), "SIP/2.0 ") {
		parts := strings.SplitN(line, " ", 3)
		code, err := strconv.Atoi(parts[1])
		if err != nil || code < 100 || code > 699 {
			return fmt.Errorf("%w: bad status line %q", ErrMalformed, line)
		}
		m.StatusCode = code
		if len(parts) == 3 {
			m.Reason = parts[2]
		}
		return nil
	}
	parts := strings.Fields(line)
	if len(parts) != 3 || !strings.EqualFold(parts[2], "SIP/2.0") {
		return fmt.Errorf("%w: bad request line %q", ErrMalformed, line)
	}
	u, err := ParseURI(parts[1])
	if err != nil {
		return err
	}
	m.Method = strings.ToUpper(parts[0])
	m.RequestURI = u
	return nil
}

// IsRequest reports whether m is a request.
func (m *Message) IsRequest() bool { return m.Method != "" }

// Bytes serialises the message. Content-Length is always recomputed.
func (m *Message) Bytes() []byte {
	var b bytes.Buffer
	if m.IsRequest() {
		fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", m.Method, m.RequestURI)
	} else {
		fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", m.StatusCode, m.Reason)
	}
	for _, h := range m.Headers {
		if h.Name == "Content-Length" {
			continue
		}
		b.WriteString(h.Name)
		b.WriteString(": ")
		b.WriteString(h.Value)
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(m.Body))
	b.Write(m.Body)
	return b.Bytes()
}

func (m *Message) String() string { return string(m.Bytes()) }

// Short returns a one-line description for logs.
func (m *Message) Short() string {
	if m.IsRequest() {
		return m.Method + " " + m.RequestURI.String()
	}
	_, method := m.CSeq()
	return fmt.Sprintf("%d %s (%s)", m.StatusCode, m.Reason, method)
}

// Get returns the first value of the named header.
func (m *Message) Get(name string) string {
	name = CanonicalName(name)
	for _, h := range m.Headers {
		if h.Name == name {
			return h.Value
		}
	}
	return ""
}

// GetAll returns all values of the named header.
func (m *Message) GetAll(name string) []string {
	name = CanonicalName(name)
	var out []string
	for _, h := range m.Headers {
		if h.Name == name {
			out = append(out, h.Value)
		}
	}
	return out
}

// Set replaces all values of the header with a single value, keeping the
// position of the first occurrence.
func (m *Message) Set(name, value string) {
	name = CanonicalName(name)
	for i, h := range m.Headers {
		if h.Name == name {
			m.Headers[i].Value = value
			m.delFrom(name, i+1)
			return
		}
	}
	m.Headers = append(m.Headers, Header{name, value})
}

// Add appends a header value.
func (m *Message) Add(name, value string) {
	m.Headers = append(m.Headers, Header{CanonicalName(name), value})
}

// Prepend inserts a header value before any existing value of that header
// (or at the top when there is none).
func (m *Message) Prepend(name, value string) {
	name = CanonicalName(name)
	idx := 0
	for i, h := range m.Headers {
		if h.Name == name {
			idx = i
			break
		}
	}
	m.Headers = append(m.Headers, Header{})
	copy(m.Headers[idx+1:], m.Headers[idx:])
	m.Headers[idx] = Header{name, value}
}

// Del removes all values of the header.
func (m *Message) Del(name string) { m.delFrom(CanonicalName(name), 0) }

func (m *Message) delFrom(name string, from int) {
	out := m.Headers[:from]
	for _, h := range m.Headers[from:] {
		if h.Name != name {
			out = append(out, h)
		}
	}
	m.Headers = out
}

// setFirst replaces the first value of a header.
func (m *Message) setFirst(name, value string) {
	for i, h := range m.Headers {
		if h.Name == name {
			m.Headers[i].Value = value
			return
		}
	}
}

// Clone returns a deep copy.
func (m *Message) Clone() *Message {
	c := *m
	c.RequestURI = m.RequestURI.Clone()
	c.Headers = append([]Header(nil), m.Headers...)
	c.Body = append([]byte(nil), m.Body...)
	return &c
}

// CallID returns the Call-ID header.
func (m *Message) CallID() string { return m.Get("Call-ID") }

// CSeq returns the sequence number and method.
func (m *Message) CSeq() (uint32, string) {
	num, method, _ := strings.Cut(strings.TrimSpace(m.Get("CSeq")), " ")
	n, _ := strconv.ParseUint(strings.TrimSpace(num), 10, 32)
	return uint32(n), strings.ToUpper(strings.TrimSpace(method))
}

// From parses the From header.
func (m *Message) From() (*Address, error) { return ParseAddress(m.Get("From")) }

// To parses the To header.
func (m *Message) To() (*Address, error) { return ParseAddress(m.Get("To")) }

// Contact parses the first Contact header.
func (m *Message) Contact() (*Address, error) {
	c := m.Get("Contact")
	if c == "" {
		return nil, errors.New("sip: no Contact header")
	}
	return ParseAddress(c)
}

// TopVia parses the first Via header.
func (m *Message) TopVia() (*Via, error) {
	v := m.Get("Via")
	if v == "" {
		return nil, errors.New("sip: no Via header")
	}
	return ParseVia(v)
}

// ContentType returns the lower-case media type without parameters.
func (m *Message) ContentType() string {
	ct, _, _ := strings.Cut(m.Get("Content-Type"), ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

// HasSDP reports whether the message carries an SDP body.
func (m *Message) HasSDP() bool {
	return len(m.Body) > 0 && m.ContentType() == "application/sdp"
}

// NewRequest creates a request with a start line only.
func NewRequest(method string, ruri *URI) *Message {
	return &Message{Method: method, RequestURI: ruri}
}

// NewResponse builds a response to req copying the mandatory headers.
func NewResponse(req *Message, code int, reason string) *Message {
	if reason == "" {
		reason = StatusText(code)
	}
	r := &Message{StatusCode: code, Reason: reason}
	dialogCreating := req.Method == "INVITE" || req.Method == "SUBSCRIBE" || req.Method == "REFER"
	for _, h := range req.Headers {
		switch h.Name {
		case "Via", "From", "To", "Call-ID", "CSeq":
			r.Headers = append(r.Headers, h)
		case "Record-Route":
			if dialogCreating && code > 100 && code < 300 {
				r.Headers = append(r.Headers, h)
			}
		}
	}
	return r
}

// SetToTag adds a tag to the To header if it has none.
func (m *Message) SetToTag(tag string) {
	to, err := m.To()
	if err != nil || to.Tag() != "" {
		return
	}
	to.Params.Set("tag", tag)
	m.Set("To", to.String())
}

// Via is a parsed Via header value.
type Via struct {
	Transport string // upper case: UDP, TCP, TLS, WS...
	Host      string
	Port      int
	Params    Params
}

// ParseVia parses a single Via value.
func ParseVia(s string) (*Via, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 3)
	if len(parts) != 3 || !strings.EqualFold(strings.TrimSpace(parts[0]), "SIP") ||
		strings.TrimSpace(parts[1]) != "2.0" {
		return nil, fmt.Errorf("sip: bad Via %q", s)
	}
	rest := strings.TrimLeft(parts[2], " \t")
	i := strings.IndexAny(rest, " \t")
	if i < 0 {
		return nil, fmt.Errorf("sip: bad Via %q", s)
	}
	v := &Via{Transport: strings.ToUpper(rest[:i])}
	rest = strings.TrimSpace(rest[i:])
	hostport := rest
	if sc := strings.IndexByte(rest, ';'); sc >= 0 {
		hostport = strings.TrimSpace(rest[:sc])
		v.Params = parseParams(rest[sc+1:], ';')
	}
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return nil, fmt.Errorf("sip: bad Via %q: %w", s, err)
	}
	v.Host, v.Port = host, port
	return v, nil
}

// String renders the Via value.
func (v *Via) String() string {
	var b strings.Builder
	b.WriteString("SIP/2.0/")
	b.WriteString(v.Transport)
	b.WriteByte(' ')
	b.WriteString(v.Host)
	if v.Port > 0 {
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(v.Port))
	}
	v.Params.write(&b, ';')
	return b.String()
}

// Branch returns the branch parameter.
func (v *Via) Branch() string {
	b, _ := v.Params.Get("branch")
	return b
}

var statusText = map[int]string{
	100: "Trying", 180: "Ringing", 181: "Call Is Being Forwarded", 182: "Queued",
	183: "Session Progress", 200: "OK", 202: "Accepted", 300: "Multiple Choices",
	301: "Moved Permanently", 302: "Moved Temporarily", 400: "Bad Request",
	401: "Unauthorized", 402: "Payment Required", 403: "Forbidden", 404: "Not Found",
	405: "Method Not Allowed", 406: "Not Acceptable", 407: "Proxy Authentication Required",
	408: "Request Timeout", 410: "Gone", 413: "Request Entity Too Large",
	414: "Request-URI Too Long", 415: "Unsupported Media Type", 416: "Unsupported URI Scheme",
	420: "Bad Extension", 421: "Extension Required", 423: "Interval Too Brief",
	480: "Temporarily Unavailable", 481: "Call/Transaction Does Not Exist",
	482: "Loop Detected", 483: "Too Many Hops", 484: "Address Incomplete", 485: "Ambiguous",
	486: "Busy Here", 487: "Request Terminated", 488: "Not Acceptable Here",
	489: "Bad Event", 491: "Request Pending", 493: "Undecipherable",
	500: "Server Internal Error", 501: "Not Implemented", 502: "Bad Gateway",
	503: "Service Unavailable", 504: "Server Time-out", 505: "Version Not Supported",
	513: "Message Too Large", 600: "Busy Everywhere", 603: "Decline",
	604: "Does Not Exist Anywhere", 606: "Not Acceptable",
}

// StatusText returns the default reason phrase for a status code.
func StatusText(code int) string {
	if t, ok := statusText[code]; ok {
		return t
	}
	switch code / 100 {
	case 1:
		return "Trying"
	case 2:
		return "OK"
	case 3:
		return "Redirection"
	case 4:
		return "Client Error"
	case 5:
		return "Server Error"
	}
	return "Global Failure"
}
