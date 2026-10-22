package config

import (
	"net/netip"
	"time"
)

// SIP Guard: protections ("jails") that ban an
// address after repeated failures within a time window.

// Jail names.
const (
	JailSIPRequest      = "sip-request"      // calls to numbers that do not exist (dial plan probing)
	JailSIPRegistration = "sip-registration" // failed sign-ins of unknown or disabled users
	JailSIPPassword     = "sip-password"     // an existing extension with a wrong password
	JailSIPInvite       = "sip-invite"       // too many calls from one address (flooding)
	JailWebRequest      = "web-request"      // web pages that do not exist (scanners)
	JailWebTraffic      = "web-traffic"      // too many web requests (denial of service)
	JailWebLogin        = "web-login"        // failed sign-ins to the web interface
)

// JailOrder is the order the protections are listed in.
var JailOrder = []string{JailSIPRequest, JailSIPRegistration, JailWebRequest, JailWebTraffic, JailSIPPassword,
	JailWebLogin, JailSIPInvite}

// JailArea is what a ban of the jail closes: "sip" or "web".
func JailArea(name string) string {
	switch name {
	case JailWebRequest, JailWebTraffic, JailWebLogin:
		return "web"
	}
	return "sip"
}

// Jail is one protection.
type Jail struct {
	Enabled     bool
	MaxFailures int           // failures within Window that ban the address
	Window      time.Duration // the time failures are counted over
	BanTime     time.Duration // how long the ban lasts
}

// Guard configures SIP Guard (stored in rq_settings).
type Guard struct {
	Enabled bool // off: nothing is banned
	Jails   map[string]Jail
}

// DefaultGuard is SIP Guard before anything was changed: the protections
// of RingQ's SIP Guard; wrong passwords use auth-fail-* of ringqswitch.xml.
func DefaultGuard(s Settings) Guard {
	pw := Jail{Enabled: s.AuthFailLimit > 0, MaxFailures: s.AuthFailLimit, Window: s.AuthFailWindow, BanTime: s.AuthBlockTime}
	if pw.MaxFailures <= 0 {
		pw.MaxFailures = 10
	}
	if pw.Window <= 0 {
		pw.Window = time.Minute
	}
	if pw.BanTime <= 0 {
		pw.BanTime = 10 * time.Minute
	}
	return Guard{Enabled: true, Jails: map[string]Jail{
		JailSIPRequest:      {Enabled: true, MaxFailures: 20, Window: time.Minute, BanTime: time.Hour},
		JailSIPRegistration: {Enabled: true, MaxFailures: 30, Window: 5 * time.Minute, BanTime: 30 * time.Minute},
		JailSIPPassword:     pw,
		JailSIPInvite:       {Enabled: false, MaxFailures: 200, Window: 3 * time.Minute, BanTime: time.Hour},
		JailWebRequest:      {Enabled: true, MaxFailures: 300, Window: time.Minute, BanTime: time.Hour},
		JailWebTraffic:      {Enabled: true, MaxFailures: 3000, Window: time.Minute, BanTime: time.Hour},
		JailWebLogin:        {Enabled: true, MaxFailures: 10, Window: 10 * time.Minute, BanTime: time.Hour},
	}}
}

// StoredBan is a ban kept in the database (sip_security_blocks), restored
// when the switch starts.
type StoredBan struct {
	IP       netip.Addr
	Jail     string
	Since    time.Time
	Until    time.Time
	Failures int
	Reason   string
	Source   string
}
