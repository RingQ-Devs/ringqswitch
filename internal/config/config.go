// Package config loads the RingQswitch XML configuration tree:
//
//	conf/ringqswitch.xml   core settings, global vars, gateways, ACLs
//	conf/directory/*.xml   users (extensions)
//	conf/dialplan/*.xml    dialplan contexts
//
// Every file is pre-processed by replacing $${name} with global variables
// before it is parsed.
package config

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
)

// Settings are the core parameters from <settings>.
type Settings struct {
	LogLevel string

	SIPBindIP     string // "" = all interfaces
	SIPIP         string // advertised on private networks
	ExtSIPIP      string // advertised to public peers ("" = same as SIPIP)
	SIPPort       int
	EnableTCP     bool
	RTPBindIP     string
	RTPIP         string
	ExtRTPIP      string
	RTPStartPort  int
	RTPEndPort    int
	Domain        string
	AuthCalls     bool
	CodecPrefs    []string
	MinRegExpires int
	MaxRegExpires int
	DefRegExpires int
	ESLListen     string
	ESLPassword   string
	CDRPath       string
	SoundsDir     string
	RecordingsDir string
	VoicemailDir  string
	FaxDir        string // faxes sent and received (eFax)
	// Call transcription (the speech recogniser): the program and model ("" = found
	// where install-transcription.sh puts them), CPU threads per job, jobs
	// at once, and the shortest call transcribed.
	TranscribeCommand    string
	TranscribeModel      string
	TranscribeModelURL   string // where speech models are downloaded from ("" = the recogniser's)
	TranscribeLiveURL    string // a live recogniser for live transcripts ("" = one run here)
	TranscribeThreads    int
	TranscribeWorkers    int
	TranscribeMinSeconds int
	VoicemailMax         time.Duration // longest voicemail message
	ParkTimeout          time.Duration // parked calls return or hang up after this
	AuthFailLimit        int
	AuthFailWindow       time.Duration
	AuthBlockTime        time.Duration
	RTPTimeout           time.Duration
	UserAgent            string
	SIPTrace             bool
	MaxSessions          int
	CallTimeout          time.Duration
}

// Gateway is an upstream SIP trunk.
type Gateway struct {
	Name           string
	Label          string // display name ("" = Name)
	UUID           string // RingQ gateway_uuid; dial strings may use it instead of Name
	Proxy          string // host[:port]
	OutboundProxy  string // host[:port] requests are sent to ("" = Proxy)
	RegisterProxy  string // host[:port] REGISTERs are sent to ("" = OutboundProxy or Proxy)
	AuthUsername   string // digest username ("" = Username)
	Transport      string
	Username       string
	Password       string
	Realm          string
	FromUser       string
	FromDomain     string
	Register       bool
	Expire         int
	RetrySeconds   int
	Context        string // context for calls received from this gateway
	Extension      string // fixed destination for inbound calls ("" = request URI user)
	CallerIDInFrom bool
	Profile        string // SIP profile the gateway uses (its port is the source port)
}

// SIPProfile is a SIP listening port with its own addresses, like a
// SIP profile (RingQ v_sip_profiles: internal 5060,
// external 5080). The main port of ringqswitch.xml is always open; other
// enabled profiles open their port too.
type SIPProfile struct {
	UUID        string
	Name        string
	Description string
	Enabled     bool
	IP          string // bind address ("" = all)
	Port        int
	TCP         bool
	ExtSIPIP    string // advertised to public addresses ("" = the global ext-sip-ip)
	ExtRTPIP    string
	TLS         bool // configured in the database; not served by RingQswitch
}

// ACLEntry trusts a network: calls from it skip authentication and are routed
// to Context.
type ACLEntry struct {
	Name    string
	Prefix  netip.Prefix
	Context string
}

// User is a directory entry.
type User struct {
	ID               string
	UUID             string // RingQ extension_uuid ("" for XML users)
	Domain           string // domain name the user belongs to
	Password         string
	A1Hash           string // optional precomputed MD5(user:realm:password)
	Disabled         bool
	MaxRegistrations int // 0 = default limit
	Vars             map[string]string
	Params           map[string]string
}

// Domain is a tenant: a SIP realm with its own users and dialplan context.
type Domain struct {
	Name     string
	UUID     string
	Users    map[string]*User // keyed by lower-case id
	aliases  map[string]*User // number aliases
	TimeZone string           // IANA name (v_domain_settings domain / time_zone)
	Loc      *time.Location   // the time zone (nil: the server's)
	Language string           // e.g. en-us (v_domain_settings domain / language)
	Hours    *OfficeHours     // global office hours, breaks, holidays (nil: none)
	Outbound *OutboundRules   // allowed countries and PIN dialing (nil: no limits)
}

// Now is the current time in the domain's time zone.
func (dom *Domain) Now() time.Time {
	if dom != nil && dom.Loc != nil {
		return time.Now().In(dom.Loc)
	}
	return time.Now()
}

// Add registers a user in the domain.
func (dom *Domain) Add(u *User) error {
	key := strings.ToLower(u.ID)
	if _, dup := dom.Users[key]; dup {
		return fmt.Errorf("duplicate user %s@%s", u.ID, dom.Name)
	}
	u.Domain = dom.Name
	dom.Users[key] = u
	return nil
}

// Put adds or replaces a user.
func (dom *Domain) Put(u *User) {
	u.Domain = dom.Name
	dom.Users[strings.ToLower(u.ID)] = u
}

// AddAlias makes alias resolve to u (number-alias).
func (dom *Domain) AddAlias(alias string, u *User) {
	if alias != "" {
		dom.aliases[strings.ToLower(alias)] = u
	}
}

// Lookup finds a user or alias in the domain.
func (dom *Domain) Lookup(id string) *User {
	key := strings.ToLower(id)
	if u := dom.Users[key]; u != nil {
		return u
	}
	return dom.aliases[key]
}

// Directory is the user database, organised by domain.
type Directory struct {
	Domain  string             // default domain name
	Domains map[string]*Domain // keyed by lower-case name
}

// NewDirectory creates a directory whose default domain is defaultDomain.
func NewDirectory(defaultDomain string) *Directory {
	d := &Directory{Domain: defaultDomain, Domains: map[string]*Domain{}}
	d.AddDomain(defaultDomain, "")
	return d
}

// AddDomain returns the named domain, creating it if needed.
func (d *Directory) AddDomain(name, uuid string) *Domain {
	key := strings.ToLower(name)
	if dom := d.Domains[key]; dom != nil {
		if uuid != "" {
			dom.UUID = uuid
		}
		return dom
	}
	dom := &Domain{Name: name, UUID: uuid, Users: map[string]*User{}, aliases: map[string]*User{}}
	d.Domains[key] = dom
	return dom
}

// Default returns the default domain.
func (d *Directory) Default() *Domain { return d.AddDomain(d.Domain, "") }

// Resolve maps a SIP host to a domain. IP addresses (phones configured with
// the server's address) map to the default domain, and so does any host
// while there is a single domain, so single-tenant setups work whatever
// address phones use. With several tenants an unknown name returns nil:
// a typo must not land a phone in another tenant.
func (d *Directory) Resolve(host string) *Domain {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if dom := d.Domains[host]; dom != nil {
		return dom
	}
	if len(d.Domains) <= 1 || host == "" || net.ParseIP(host) != nil {
		return d.Default()
	}
	return nil
}

// Find returns the named domain or nil.
func (d *Directory) Find(name string) *Domain {
	return d.Domains[strings.ToLower(strings.Trim(name, "[]"))]
}

// SetDefault makes an existing domain the default one.
func (d *Directory) SetDefault(name string) {
	if dom := d.Find(name); dom != nil {
		d.Domain = dom.Name
	}
}

// Remove deletes a domain (the default domain cannot be removed).
func (d *Directory) Remove(name string) {
	if !strings.EqualFold(name, d.Domain) {
		delete(d.Domains, strings.ToLower(name))
	}
}

// List returns the domains sorted by name.
func (d *Directory) List() []*Domain {
	out := make([]*Domain, 0, len(d.Domains))
	for _, dom := range d.Domains {
		out = append(out, dom)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ByUUID finds a domain by its RingQ domain_uuid.
func (d *Directory) ByUUID(uuid string) *Domain {
	for _, dom := range d.Domains {
		if uuid != "" && strings.EqualFold(dom.UUID, uuid) {
			return dom
		}
	}
	return nil
}

// Lookup finds a user in the default domain.
func (d *Directory) Lookup(id string) *User { return d.Default().Lookup(id) }

// LookupIn finds a user in a domain ("" = default domain).
func (d *Directory) LookupIn(domain, id string) *User {
	if domain == "" {
		return d.Lookup(id)
	}
	dom := d.Resolve(domain)
	if dom == nil {
		return nil
	}
	return dom.Lookup(id)
}

// UserCount returns the number of users in all domains.
func (d *Directory) UserCount() int {
	n := 0
	for _, dom := range d.Domains {
		n += len(dom.Users)
	}
	return n
}

// Database configures the PostgreSQL (RingQ schema) integration.
type Database struct {
	DSN                string
	WriteCDR           bool
	CDRTables          []string
	WriteRegistrations bool
	WriteChannels      bool
	PollInterval       time.Duration
	Hostname           string
	DefaultDomain      string // domain for phones that register with an IP address
	CDRBLeg            bool   // also write B-leg CDRs (RingQ logs A legs only)
	ManageSchema       bool   // create RingQswitch's own rq_* tables when missing
	XMLDirectory       bool   // also load conf/directory/*.xml (default off with a database)
	XMLDialplan        bool   // also load conf/dialplan/*.xml (default off with a database)
	// GrantTo are database roles that may read and change RingQswitch's
	// tables (besides the database owner and the owner of v_domains, who
	// always may), e.g. the user you sign in to a database tool with.
	GrantTo []string
}

// Web configures the built-in administration web interface.
type Web struct {
	Listen        string // host:port, "" = disabled
	TLS           string // self-signed (default), off, or "cert.pem,key.pem"
	AdminUser     string
	AdminPassword string
	DataDir       string // where the self-signed certificate is kept
	Brand         string // product name shown in the UI
	// OwnerServerAccess lets extension owners use the server-wide pages
	// (Security, System, Domains, event log). Turn it off when several
	// companies (domains) share the server.
	OwnerServerAccess bool
	WebRTCPort        int    // UDP port for the browser dialer and meetings (0 = off)
	PublicIP          string // address browsers reach for WebRTC media ("" = ext-rtp-ip, else the interface)
}

// Security holds the address and user-agent rules (rq_security_rules) and
// SIP Guard.
type Security struct {
	Deny     []netip.Prefix // SIP from these addresses is dropped
	Allow    []netip.Prefix // never blocked for failed authentications
	WebAllow []netip.Prefix // only these may open the web interface (empty = anyone)
	WebDeny  []netip.Prefix // these may not open the web interface
	UADeny   []string       // lower-case User-Agent fragments of scanners
	// RestrictSIP (global IP restriction): only the Allow addresses, the
	// gateways and trusted carriers may send SIP.
	RestrictSIP bool
	Guard       *Guard      // nil: DefaultGuard
	Bans        []StoredBan // bans in force when the database was read
}

// Config is a fully loaded configuration.
type Config struct {
	Dir       string
	Vars      map[string]string
	Settings  Settings
	Database  Database
	Web       Web
	Security  Security
	Gateways  []Gateway
	Profiles  []SIPProfile
	ACL       []ACLEntry
	Directory *Directory
	Dialplan  *dialplan.Dialplan
	Features  *Features
	Warnings  []string
}

type xmlParam struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type xmlMain struct {
	Vars struct {
		Vars []xmlParam `xml:"var"`
	} `xml:"vars"`
	Settings struct {
		Params []xmlParam `xml:"param"`
	} `xml:"settings"`
	Gateways struct {
		Gateways []struct {
			Name   string     `xml:"name,attr"`
			Params []xmlParam `xml:"param"`
		} `xml:"gateway"`
	} `xml:"gateways"`
	Database struct {
		Params []xmlParam `xml:"param"`
	} `xml:"database"`
	Web struct {
		Params []xmlParam `xml:"param"`
	} `xml:"web"`
	ACL struct {
		Nodes []struct {
			CIDR    string `xml:"cidr,attr"`
			Context string `xml:"context,attr"`
			Name    string `xml:"name,attr"`
		} `xml:"node"`
	} `xml:"acl"`
}

// ExpandGlobals replaces $${name} references (outside XML comments) with
// global variables, returning the undefined names it met.
func ExpandGlobals(data []byte, vars map[string]string) ([]byte, []string) {
	var missing []string
	out := expandGlobals(data, vars, func(s string) { missing = append(missing, s) })
	return out, missing
}

var varRef = regexp.MustCompile(`\$\$\{([A-Za-z0-9_.-]+)\}`)

var sqlIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// expandGlobals replaces $${name} outside XML comments.
func expandGlobals(data []byte, vars map[string]string, warn func(string)) []byte {
	var out []byte
	for len(data) > 0 {
		start := bytes.Index(data, []byte("<!--"))
		if start < 0 {
			return append(out, expandText(data, vars, warn)...)
		}
		out = append(out, expandText(data[:start], vars, warn)...)
		end := bytes.Index(data[start:], []byte("-->"))
		if end < 0 {
			return append(out, data[start:]...)
		}
		end += start + 3
		out = append(out, data[start:end]...)
		data = data[end:]
	}
	return out
}

func expandText(data []byte, vars map[string]string, warn func(string)) []byte {
	return varRef.ReplaceAllFunc(data, func(m []byte) []byte {
		name := string(varRef.FindSubmatch(m)[1])
		v, ok := vars[name]
		if !ok {
			warn(fmt.Sprintf("undefined global variable $${%s}", name))
		}
		return []byte(xmlEscape(v))
	})
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// DetectLocalIP returns the IPv4 address of the interface holding the
// default route (no packets are sent).
func DetectLocalIP() string {
	c, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return "127.0.0.1"
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

// Load reads the configuration tree rooted at dir.
func Load(dir string) (*Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	cfg := &Config{Dir: abs, Vars: map[string]string{}, Features: NewFeatures()}
	warn := func(s string) { cfg.Warnings = append(cfg.Warnings, s) }
	host, _ := os.Hostname()
	cfg.Vars["local_ip_v4"] = DetectLocalIP()
	cfg.Vars["hostname"] = host
	cfg.Vars["conf_dir"] = abs
	cfg.Vars["base_dir"] = filepath.Dir(abs)

	mainPath := filepath.Join(abs, "ringqswitch.xml")
	raw, err := os.ReadFile(mainPath)
	if err != nil {
		return nil, err
	}
	// First pass: collect <vars> in order, expanding references to earlier ones.
	var pre xmlMain
	if err := xml.Unmarshal(raw, &pre); err != nil {
		return nil, fmt.Errorf("%s: %w", mainPath, err)
	}
	for _, v := range pre.Vars.Vars {
		cfg.Vars[v.Name] = string(varRef.ReplaceAllFunc([]byte(v.Value), func(m []byte) []byte {
			return []byte(cfg.Vars[string(varRef.FindSubmatch(m)[1])])
		}))
	}
	var main xmlMain
	if err := xml.Unmarshal(expandGlobals(raw, cfg.Vars, warn), &main); err != nil {
		return nil, fmt.Errorf("%s: %w", mainPath, err)
	}
	if err := cfg.applySettings(main); err != nil {
		return nil, fmt.Errorf("%s: %w", mainPath, err)
	}
	// With a database the XML sample users and dialplan would shadow the
	// real ones, so they are only loaded when asked for.
	useDB := cfg.Database.DSN != ""
	if useDB && !cfg.Database.XMLDirectory {
		cfg.Directory = NewDirectory(cfg.Settings.Domain)
	} else if err := cfg.loadDirectory(); err != nil {
		return nil, err
	}
	if useDB && !cfg.Database.XMLDialplan {
		cfg.Dialplan = dialplan.New()
	} else if err := cfg.loadDialplan(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func params(list []xmlParam) map[string]string {
	m := map[string]string{}
	for _, p := range list {
		m[strings.ToLower(strings.TrimSpace(p.Name))] = strings.TrimSpace(p.Value)
	}
	return m
}

type paramReader struct {
	m   map[string]string
	err error
}

func (r *paramReader) str(name, def string) string {
	if v, ok := r.m[name]; ok && v != "" {
		return v
	}
	return def
}

// optional is like str but an explicitly empty value disables the feature.
func (r *paramReader) optional(name, def string) string {
	if v, ok := r.m[name]; ok {
		return v
	}
	return def
}

func (r *paramReader) int(name string, def int) int {
	v, ok := r.m[name]
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil && r.err == nil {
		r.err = fmt.Errorf("param %s: %q is not a number", name, v)
	}
	return n
}

func (r *paramReader) bool(name string, def bool) bool {
	v, ok := r.m[name]
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1", "enabled":
		return true
	case "false", "no", "off", "0", "disabled":
		return false
	}
	if r.err == nil {
		r.err = fmt.Errorf("param %s: %q is not a boolean", name, v)
	}
	return def
}

func (r *paramReader) secs(name string, def int) time.Duration {
	return time.Duration(r.int(name, def)) * time.Second
}

// resolveIP maps "auto" / "" to (bind all, advertise detected).
func resolveIP(v, detected string) (bind, advertise string, err error) {
	switch v {
	case "", "auto", "0.0.0.0":
		return "", detected, nil
	}
	if net.ParseIP(v) == nil {
		return "", "", fmt.Errorf("%q is not an IP address", v)
	}
	return v, v, nil
}

func (cfg *Config) applySettings(main xmlMain) error {
	r := &paramReader{m: params(main.Settings.Params)}
	s := &cfg.Settings
	detected := cfg.Vars["local_ip_v4"]
	var err error
	s.LogLevel = r.str("log-level", "info")
	if s.SIPBindIP, s.SIPIP, err = resolveIP(r.str("sip-ip", "auto"), detected); err != nil {
		return fmt.Errorf("sip-ip: %w", err)
	}
	s.ExtSIPIP = r.str("ext-sip-ip", "")
	s.SIPPort = r.int("sip-port", 5060)
	s.EnableTCP = r.bool("enable-tcp", true)
	rtpDefault := r.str("sip-ip", "auto")
	if s.RTPBindIP, s.RTPIP, err = resolveIP(r.str("rtp-ip", rtpDefault), detected); err != nil {
		return fmt.Errorf("rtp-ip: %w", err)
	}
	s.ExtRTPIP = r.str("ext-rtp-ip", s.ExtSIPIP)
	for _, ext := range []string{s.ExtSIPIP, s.ExtRTPIP} {
		if ext != "" && net.ParseIP(ext) == nil {
			return fmt.Errorf("external ip %q is not an IP address", ext)
		}
	}
	s.RTPStartPort = r.int("rtp-start-port", 16384)
	s.RTPEndPort = r.int("rtp-end-port", 32768)
	s.Domain = r.str("domain", cfg.Vars["domain"])
	if s.Domain == "" {
		s.Domain = s.SIPIP
	}
	s.AuthCalls = r.bool("auth-calls", true)
	for _, c := range strings.Split(r.str("codec-prefs", "PCMU,PCMA"), ",") {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "PCMU" && c != "PCMA" {
			return fmt.Errorf("codec-prefs: %q cannot be terminated locally (supported: PCMU, PCMA)", c)
		}
		s.CodecPrefs = append(s.CodecPrefs, c)
	}
	s.MinRegExpires = r.int("min-register-expires", 60)
	s.MaxRegExpires = r.int("max-register-expires", 3600)
	s.DefRegExpires = r.int("default-register-expires", 3600)
	s.ESLListen = r.optional("event-socket-listen", "127.0.0.1:8021")
	s.ESLPassword = r.str("event-socket-password", "")
	s.CDRPath = cfg.path(r.optional("cdr-csv", "../log/cdr.csv"))
	s.SoundsDir = cfg.path(r.str("sounds-dir", "../sounds"))
	s.RecordingsDir = cfg.path(r.str("recordings-dir", "../recordings"))
	s.VoicemailDir = cfg.path(r.str("voicemail-dir", "../voicemail"))
	// Faxes live next to the recordings unless placed elsewhere.
	s.FaxDir = cfg.path(r.str("fax-dir", filepath.Join(filepath.Dir(s.RecordingsDir), "fax")))
	s.TranscribeCommand = r.str("transcribe-command", "")
	s.TranscribeModel = r.str("transcribe-model", "")
	if s.TranscribeModel != "" {
		s.TranscribeModel = cfg.path(s.TranscribeModel)
	}
	s.TranscribeModelURL = r.str("transcribe-model-url", "")
	s.TranscribeLiveURL = r.str("transcribe-live-url", "")
	s.TranscribeThreads = r.int("transcribe-threads", max(1, min(4, runtime.NumCPU()/2)))
	s.TranscribeWorkers = r.int("transcribe-workers", 1)
	s.TranscribeMinSeconds = r.int("transcribe-min-seconds", 5)
	s.VoicemailMax = r.secs("voicemail-max-seconds", 180)
	s.ParkTimeout = r.secs("park-timeout", 120)
	s.AuthFailLimit = r.int("auth-fail-threshold", 10)
	s.AuthFailWindow = r.secs("auth-fail-window", 60)
	s.AuthBlockTime = r.secs("auth-block-seconds", 600)
	s.RTPTimeout = r.secs("rtp-timeout", 120)
	s.UserAgent = r.str("user-agent", "RingQswitch")
	s.SIPTrace = r.bool("sip-trace", false)
	s.MaxSessions = r.int("max-sessions", 1000)
	s.CallTimeout = r.secs("call-timeout", 60)
	if r.err != nil {
		return r.err
	}
	if s.RTPStartPort <= 1024 || s.RTPEndPort > 65535 || s.RTPEndPort-s.RTPStartPort < 2 {
		return fmt.Errorf("invalid rtp port range %d-%d", s.RTPStartPort, s.RTPEndPort)
	}
	if s.ESLListen != "" && s.ESLPassword == "" {
		return errors.New("event-socket-password must be set when the event socket is enabled")
	}

	for _, xg := range main.Gateways.Gateways {
		gr := &paramReader{m: params(xg.Params)}
		g := Gateway{
			Name:           xg.Name,
			UUID:           gr.str("uuid", ""),
			Proxy:          gr.str("proxy", gr.str("realm", "")),
			OutboundProxy:  gr.str("outbound-proxy", ""),
			RegisterProxy:  gr.str("register-proxy", ""),
			AuthUsername:   gr.str("auth-username", ""),
			Transport:      strings.ToLower(gr.str("transport", "udp")),
			Username:       gr.str("username", ""),
			Password:       gr.str("password", ""),
			Register:       gr.bool("register", false),
			Expire:         gr.int("expire-seconds", 600),
			RetrySeconds:   gr.int("retry-seconds", 30),
			Context:        gr.str("context", "public"),
			Extension:      gr.str("extension", ""),
			CallerIDInFrom: gr.bool("caller-id-in-from", false),
		}
		g.Realm = gr.str("realm", hostOnly(g.Proxy))
		g.FromUser = gr.str("from-user", g.Username)
		g.FromDomain = gr.str("from-domain", g.Realm)
		if gr.err != nil {
			return fmt.Errorf("gateway %s: %w", g.Name, gr.err)
		}
		if g.Name == "" || g.Proxy == "" {
			return fmt.Errorf("gateway %q: name and proxy are required", g.Name)
		}
		if g.Transport != "udp" && g.Transport != "tcp" {
			return fmt.Errorf("gateway %s: unsupported transport %q", g.Name, g.Transport)
		}
		if g.Register && g.Username == "" {
			return fmt.Errorf("gateway %s: register=true needs a username", g.Name)
		}
		cfg.Gateways = append(cfg.Gateways, g)
	}
	wr := &paramReader{m: params(main.Web.Params)}
	cfg.Web = Web{
		Listen: wr.optional("listen", ""), TLS: wr.str("tls", "self-signed"),
		AdminUser: wr.str("admin-user", "admin"), AdminPassword: wr.str("admin-password", ""),
		DataDir: wr.str("data-dir", "/var/lib/ringqswitch"), Brand: wr.str("brand", "RingQswitch"),
		OwnerServerAccess: wr.bool("owner-server-access", true), WebRTCPort: wr.int("webrtc-port", 8443),
		PublicIP: wr.str("public-ip", ""),
	}
	if cfg.Web.WebRTCPort < 0 || cfg.Web.WebRTCPort > 65535 {
		return fmt.Errorf("web: webrtc-port %d is not a port", cfg.Web.WebRTCPort)
	}
	if cfg.Web.Listen != "" && len(cfg.Web.AdminPassword) < 8 {
		return errors.New("web: admin-password must be set (8 characters or more) when the web interface is enabled")
	}
	dr := &paramReader{m: params(main.Database.Params)}
	db := &cfg.Database
	db.DSN = dr.str("dsn", "")
	db.WriteCDR = dr.bool("write-cdr", true)
	for _, t := range strings.Split(dr.str("cdr-tables", "v_xml_cdr"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			if !sqlIdent.MatchString(t) {
				return fmt.Errorf("database cdr-tables: %q is not a table name", t)
			}
			db.CDRTables = append(db.CDRTables, t)
		}
	}
	db.WriteRegistrations = dr.bool("write-registrations", true)
	db.WriteChannels = dr.bool("write-channels", true)
	db.PollInterval = dr.secs("poll-seconds", 15)
	db.Hostname = dr.str("hostname", cfg.Vars["hostname"])
	db.DefaultDomain = dr.str("default-domain", "")
	db.CDRBLeg = dr.bool("cdr-b-leg", false)
	db.ManageSchema = dr.bool("manage-schema", true)
	db.XMLDirectory = dr.bool("xml-directory", false)
	db.XMLDialplan = dr.bool("xml-dialplan", false)
	for _, r := range strings.Split(dr.str("grant-to", ""), ",") {
		if r = strings.TrimSpace(r); r != "" {
			if !sqlIdent.MatchString(r) {
				return fmt.Errorf("database grant-to: %q is not a role name", r)
			}
			db.GrantTo = append(db.GrantTo, r)
		}
	}
	if dr.err != nil {
		return fmt.Errorf("database: %w", dr.err)
	}
	if db.PollInterval < time.Second && db.PollInterval != 0 {
		db.PollInterval = time.Second
	}

	for _, n := range main.ACL.Nodes {
		cidr := n.CIDR
		if !strings.Contains(cidr, "/") {
			if a, err := netip.ParseAddr(cidr); err == nil {
				cidr = fmt.Sprintf("%s/%d", cidr, a.BitLen())
			}
		}
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fmt.Errorf("acl node %q: %w", n.CIDR, err)
		}
		ctx := n.Context
		if ctx == "" {
			ctx = "public"
		}
		cfg.ACL = append(cfg.ACL, ACLEntry{Name: n.Name, Prefix: p.Masked(), Context: ctx})
	}
	return nil
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func (cfg *Config) path(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Clean(filepath.Join(cfg.Dir, p))
}

func (cfg *Config) xmlFiles(sub string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(cfg.Dir, sub, "*.xml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (cfg *Config) readExpanded(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return expandGlobals(raw, cfg.Vars, func(s string) { cfg.Warnings = append(cfg.Warnings, path+": "+s) }), nil
}

type xmlUser struct {
	ID     string `xml:"id,attr"`
	Params struct {
		Params []xmlParam `xml:"param"`
	} `xml:"params"`
	Variables struct {
		Vars []xmlParam `xml:"variable"`
	} `xml:"variables"`
}

func (cfg *Config) loadDirectory() error {
	dir := NewDirectory(cfg.Settings.Domain)
	files, err := cfg.xmlFiles("directory")
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := cfg.readExpanded(f)
		if err != nil {
			return err
		}
		dec := xml.NewDecoder(bytes.NewReader(data))
		// Users outside a <domain name="..."> element belong to the default
		// domain; <domain> wrappers select another one.
		var domains []*Domain
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "domain" && len(domains) > 0 {
				domains = domains[:len(domains)-1]
				continue
			}
			se, ok := tok.(xml.StartElement)
			if !ok {
				continue
			}
			if se.Name.Local == "domain" {
				name := ""
				for _, a := range se.Attr {
					if a.Name.Local == "name" {
						name = strings.TrimSpace(a.Value)
					}
				}
				if name == "" {
					name = dir.Domain
				}
				domains = append(domains, dir.AddDomain(name, ""))
				continue
			}
			if se.Name.Local != "user" {
				continue
			}
			dom := dir.Default()
			if len(domains) > 0 {
				dom = domains[len(domains)-1]
			}
			var xu xmlUser
			if err := dec.DecodeElement(&xu, &se); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			if xu.ID == "" {
				return fmt.Errorf("%s: user without id", f)
			}
			p := params(xu.Params.Params)
			u := &User{ID: xu.ID, Password: p["password"], A1Hash: p["a1-hash"], Params: p, Vars: map[string]string{}}
			for _, v := range xu.Variables.Vars {
				u.Vars[strings.TrimSpace(v.Name)] = v.Value
			}
			if u.Password == "" && u.A1Hash == "" {
				return fmt.Errorf("%s: user %s has no password", f, u.ID)
			}
			if err := dom.Add(u); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			if alias := p["number-alias"]; alias != "" {
				dom.AddAlias(alias, u)
			}
		}
	}
	cfg.Directory = dir
	return nil
}

func (cfg *Config) loadDialplan() error {
	dp := dialplan.New()
	files, err := cfg.xmlFiles("dialplan")
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := cfg.readExpanded(f)
		if err != nil {
			return err
		}
		if err := dp.Load(bytes.NewReader(data), f); err != nil {
			return err
		}
	}
	cfg.Dialplan = dp
	return nil
}

// MatchACL returns the ACL entry containing addr.
func (cfg *Config) MatchACL(addr netip.Addr) *ACLEntry {
	for i := range cfg.ACL {
		if cfg.ACL[i].Prefix.Contains(addr.Unmap()) {
			return &cfg.ACL[i]
		}
	}
	return nil
}
