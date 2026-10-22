package core

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

const maxContactsPerUser = 10

// Binding is one registered contact.
type Binding struct {
	User         string
	Domain       string // domain (realm) the user belongs to
	Contact      string // contact URI as registered
	Src          sip.Addr
	Expires      time.Time
	RegisteredAt time.Time
	UserAgent    string
	CallID       string
}

// Registrar is the location service.
type Registrar struct {
	mu       sync.Mutex
	bindings map[string][]*Binding // lower-case user@domain
}

func newRegistrar() *Registrar { return &Registrar{bindings: map[string][]*Binding{}} }

func regKey(user, domain string) string { return strings.ToLower(user + "@" + domain) }

// AOR returns user@domain.
func (b *Binding) AOR() string { return b.User + "@" + b.Domain }

// update stores a binding and returns the bindings it displaced: a
// registration of the same user from the same address (the same device
// re-registering with a new contact or Call-ID, as some apps do after a
// network change or restart), and the oldest ones beyond the limit.
func (r *Registrar) update(b *Binding, limit int) (dropped []*Binding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := regKey(b.User, b.Domain)
	list := r.bindings[key]
	for i, old := range list {
		if old.Contact == b.Contact {
			b.RegisteredAt = old.RegisteredAt
			list[i] = b
			return nil
		}
	}
	keep := list[:0]
	for _, old := range list {
		if old.Src == b.Src && b.Src.AP.IsValid() {
			b.RegisteredAt = old.RegisteredAt
			dropped = append(dropped, old)
			continue
		}
		keep = append(keep, old)
	}
	list = append(keep, b)
	if limit <= 0 {
		limit = maxContactsPerUser
	}
	if len(list) > limit {
		// Oldest registrations make way for new ones
		// with max-registrations-per-extension.
		dropped = append(dropped, list[:len(list)-limit]...)
		list = list[len(list)-limit:]
	}
	r.bindings[key] = list
	return dropped
}

func (r *Registrar) remove(user, domain, contact string) *Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := regKey(user, domain)
	list := r.bindings[key]
	for i, b := range list {
		if b.Contact == contact {
			r.bindings[key] = append(list[:i:i], list[i+1:]...)
			if len(r.bindings[key]) == 0 {
				delete(r.bindings, key)
			}
			return b
		}
	}
	return nil
}

func (r *Registrar) removeAll(user, domain string) []*Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := regKey(user, domain)
	list := r.bindings[key]
	delete(r.bindings, key)
	return list
}

func (r *Registrar) removeBySource(src sip.Addr) []*Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	var removed []*Binding
	for key, list := range r.bindings {
		keep := list[:0]
		for _, b := range list {
			if b.Src == src {
				removed = append(removed, b)
			} else {
				keep = append(keep, b)
			}
		}
		if len(keep) == 0 {
			delete(r.bindings, key)
		} else {
			r.bindings[key] = keep
		}
	}
	return removed
}

// Lookup returns the live bindings of user@domain, most recent first.
func (r *Registrar) Lookup(user, domain string) []*Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var out []*Binding
	for _, b := range r.bindings[regKey(user, domain)] {
		if b.Expires.After(now) {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RegisteredAt.After(out[j].RegisteredAt) })
	return out
}

// byContact finds the live binding registered with the given contact URI.
func (r *Registrar) byContact(contact string) *Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, list := range r.bindings {
		for _, b := range list {
			if b.Contact == contact && b.Expires.After(now) {
				return b
			}
		}
	}
	return nil
}

// All returns every live binding sorted by user.
func (r *Registrar) All() []*Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var out []*Binding
	for _, list := range r.bindings {
		for _, b := range list {
			if b.Expires.After(now) {
				out = append(out, b)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		if out[i].User != out[j].User {
			return out[i].User < out[j].User
		}
		return out[i].Contact < out[j].Contact
	})
	return out
}

func (r *Registrar) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.bindings {
		n += len(l)
	}
	return n
}

func (r *Registrar) expire(now time.Time) []*Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	var gone []*Binding
	for key, list := range r.bindings {
		keep := list[:0]
		for _, b := range list {
			if b.Expires.After(now) {
				keep = append(keep, b)
			} else {
				gone = append(gone, b)
			}
		}
		if len(keep) == 0 {
			delete(r.bindings, key)
		} else {
			r.bindings[key] = keep
		}
	}
	return gone
}

// ---------------------------------------------------------------------------
// Digest authentication

type authResult int

const (
	authMissing authResult = iota // no credentials for our realm
	authOK
	authStale   // nonce expired, unknown or replayed: challenge again
	authBad     // wrong password
	authUnknown // unknown or disabled user, unsupported algorithm
)

// realmFor picks the tenant (and therefore the digest realm) of a request
// from the host part of its identity URI: To for REGISTER, From otherwise.
// Hosts that are not a configured domain (typically the server's IP address)
// select the default domain.
func (c *Core) realmFor(req *sip.Message) *config.Domain {
	dir := c.config().Directory
	var a *sip.Address
	var err error
	if req.Method == "REGISTER" {
		a, err = req.To()
	} else {
		a, err = req.From()
	}
	if err != nil || a.URI == nil {
		return dir.Default()
	}
	if dom := dir.Resolve(a.URI.Host); dom != nil {
		return dom
	}
	// Unknown tenant: challenge with the host as realm; no user matches it,
	// so authentication fails as it should.
	return &config.Domain{Name: a.URI.Host}
}

func (c *Core) checkAuth(req *sip.Message, header string) (*config.User, authResult) {
	dom := c.realmFor(req)
	realm := dom.Name
	var cred map[string]string
	for _, h := range req.GetAll(header) {
		d, ok := sip.ParseDigest(h)
		if ok && strings.EqualFold(d["realm"], realm) {
			cred = d
			break
		}
	}
	if cred == nil {
		return nil, authMissing
	}
	if alg := cred["algorithm"]; alg != "" && !strings.EqualFold(alg, "MD5") {
		return nil, authUnknown
	}
	nonce := cred["nonce"]
	switch c.nonces.Check(nonce) {
	case sip.NonceInvalid, sip.NonceStale:
		return nil, authStale
	}
	user := dom.Lookup(cred["username"])
	if user == nil || user.Disabled {
		return nil, authUnknown
	}
	ha1 := user.A1Hash
	if ha1 == "" {
		ha1 = sip.HA1(user.ID, realm, user.Password)
	}
	qop := cred["qop"]
	expected := sip.DigestResponse(ha1, nonce, cred["nc"], cred["cnonce"], qop, req.Method, cred["uri"])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToLower(cred["response"]))) != 1 {
		return nil, authBad
	}
	if qop != "" {
		nc, err := strconv.ParseUint(cred["nc"], 16, 64)
		if err != nil || !c.nonces.UseCount(nonce, nc) {
			return nil, authStale
		}
	}
	return user, authOK
}

func (c *Core) challenge(tx *sip.ServerTx, proxy, stale bool) {
	if req := tx.Request; req.Method == "INVITE" {
		to, _ := req.To()
		user := ""
		if to != nil {
			user = to.URI.User + "@" + to.URI.Host
		}
		logAt(c.log, slog.LevelWarn, fmt.Sprintf("SIP auth challenge (INVITE) for [%s] from ip %s", user, req.Src.AP.Addr()))
	}
	code, hdr := 401, "WWW-Authenticate"
	if proxy {
		code, hdr = 407, "Proxy-Authenticate"
	}
	resp := sip.NewResponse(tx.Request, code, "")
	resp.SetToTag(sip.NewTag())
	resp.Add(hdr, c.nonces.Challenge(c.realmFor(tx.Request).Name, stale))
	_ = tx.Respond(resp)
}

func (c *Core) authFailed(tx *sip.ServerTx, who string, res authResult) {
	src := tx.Request.Src.AP.Addr()
	// A wrong password for an extension that exists, or any other failed
	// sign-in (unknown or disabled user).
	jail, why := config.JailSIPPassword, "wrong password for "+who
	if res == authUnknown {
		jail, why = config.JailSIPRegistration, "failed sign-in as "+who
	}
	blocked, n := false, 0
	if !c.neverBlock(tx.Request.Src) {
		blocked, n = c.guard.fail(jail, src, why)
	}
	j := c.guard.jail(jail)
	c.log.Warn("authentication failed (wrong user or password)", "method", tx.Request.Method, "user", who,
		"src", tx.Request.Src.String(), "failures", fmt.Sprintf("%d/%d per %s", n, j.MaxFailures, j.Window))
	if blocked {
		c.log.Warn("blocking address after repeated authentication failures", "ip", src, "for", j.BanTime)
		e := NewEvent(EvCustom, "ringq::auth_block")
		e.Add("Network-IP", src.String())
		c.events.Publish(e)
	}
	resp := sip.NewResponse(tx.Request, 403, "")
	resp.SetToTag(sip.NewTag())
	_ = tx.Respond(resp)
}

// ---------------------------------------------------------------------------
// REGISTER

func (c *Core) handleRegister(tx *sip.ServerTx) {
	req := tx.Request
	cfg := c.config()
	s := cfg.Settings
	to, err := req.To()
	if err != nil || to.URI.User == "" {
		_ = tx.Respond(sip.NewResponse(req, 400, "Bad To Header"))
		return
	}
	aor := to.URI.User
	user, res := c.checkAuth(req, "Authorization")
	switch res {
	case authMissing, authStale:
		c.challenge(tx, false, res == authStale)
		return
	case authBad, authUnknown:
		c.authFailed(tx, aor, res)
		return
	}
	if !strings.EqualFold(user.ID, aor) && user != c.config().Directory.LookupIn(user.Domain, aor) {
		c.authFailed(tx, aor, authUnknown)
		return
	}
	dom := user.Domain
	limit := maxContactsPerUser
	if user.MaxRegistrations > 0 {
		limit = user.MaxRegistrations
	}

	defExp := s.DefRegExpires
	if e := req.Get("Expires"); e != "" {
		if n, err := strconv.Atoi(e); err == nil && n >= 0 {
			defExp = n
		}
	}
	contacts := req.GetAll("Contact")
	ua := req.Get("User-Agent")
	now := time.Now()
	if len(contacts) == 1 && strings.TrimSpace(contacts[0]) == "*" {
		if defExp != 0 {
			_ = tx.Respond(sip.NewResponse(req, 400, "Wildcard Requires Expires 0"))
			return
		}
		for _, b := range c.reg.removeAll(user.ID, dom) {
			c.fireRegEvent("ringq::unregister", b, "wildcard")
		}
		contacts = nil
	}
	type pending struct {
		uri     string
		expires int
	}
	var todo []pending
	for _, raw := range contacts {
		a, err := sip.ParseAddress(raw)
		if err != nil || a.Wildcard {
			_ = tx.Respond(sip.NewResponse(req, 400, "Bad Contact"))
			return
		}
		exp := defExp
		if v, ok := a.Params.Get("expires"); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				exp = n
			}
		}
		if exp > 0 && exp < s.MinRegExpires {
			resp := sip.NewResponse(req, 423, "")
			resp.Add("Min-Expires", strconv.Itoa(s.MinRegExpires))
			_ = tx.Respond(resp)
			return
		}
		if exp > s.MaxRegExpires {
			exp = s.MaxRegExpires
		}
		todo = append(todo, pending{a.URI.String(), exp})
	}
	for _, p := range todo {
		if p.expires == 0 {
			if b := c.reg.remove(user.ID, dom, p.uri); b != nil {
				c.log.Info("unregistered", "user", b.AOR(), "contact", p.uri)
				c.recordEvent("registration", b.AOR()+" unregistered")
				c.fireRegEvent("ringq::unregister", b, "expires=0")
			}
			continue
		}
		b := &Binding{
			User: user.ID, Domain: dom, Contact: p.uri, Src: req.Src, UserAgent: ua, CallID: req.CallID(),
			Expires: now.Add(time.Duration(p.expires) * time.Second), RegisteredAt: now,
		}
		isNew := len(c.reg.Lookup(user.ID, dom)) == 0
		for _, d := range c.reg.update(b, limit) {
			c.fireRegEvent("ringq::unregister", d, "replaced")
		}
		if isNew {
			c.log.Info("registered", "user", b.AOR(), "contact", p.uri, "src", req.Src.String(), "ua", ua)
			c.recordEvent("registration", fmt.Sprintf("%s registered from %s (%s)", b.AOR(), req.Src.AP.Addr(), ua))
			go func(u, d string) {
				time.Sleep(time.Second) // let the phone process the 200 OK first
				c.mwi.mu.Lock()
				n := c.mwi.counts[strings.ToLower(u+"@"+d)]
				c.mwi.mu.Unlock()
				if n[0] > 0 {
					c.sendMWI(u, d)
				}
			}(user.ID, dom)
		}
		c.fireRegEvent("ringq::register", b, "")
	}

	resp := sip.NewResponse(req, 200, "")
	resp.SetToTag(sip.NewTag())
	for _, b := range c.reg.Lookup(user.ID, dom) {
		resp.Add("Contact", fmt.Sprintf("<%s>;expires=%d", b.Contact, int(time.Until(b.Expires).Seconds()+0.5)))
	}
	resp.Add("Date", now.UTC().Format(time.RFC1123))
	_ = tx.Respond(resp)
}

func (c *Core) fireRegEvent(subclass string, b *Binding, reason string) {
	expires := strconv.Itoa(max(0, int(time.Until(b.Expires).Seconds())))
	ip, port := b.Src.AP.Addr().String(), strconv.Itoa(int(b.Src.AP.Port()))
	e := NewEvent(EvCustom, subclass)
	e.Add("User", b.User)
	e.Add("Realm", b.Domain)
	e.Add("Contact", b.Contact)
	e.Add("Call-ID", b.CallID)
	e.Add("Network-Addr", b.Src.String())
	e.Add("Network-IP", ip)
	e.Add("Network-Port", port)
	e.Add("Network-Proto", b.Src.Transport)
	e.Add("User-Agent", b.UserAgent)
	e.Add("Expires", expires)
	if reason != "" {
		e.Add("Reason", reason)
	}
	c.events.Publish(e)

	// The same event under the older name and headers, for existing
	// event socket clients (sofia::register, sofia::unregister, sofia::expire).
	fs := NewEvent(EvCustom, "sofia::"+strings.TrimPrefix(subclass, "ringq::"))
	fs.Add("profile-name", "internal")
	fs.Add("from-user", b.User)
	fs.Add("from-host", b.Domain)
	fs.Add("presence-hosts", b.Domain)
	fs.Add("contact", "<"+b.Contact+">")
	fs.Add("call-id", b.CallID)
	fs.Add("rpid", "unknown")
	fs.Add("status", "Registered("+strings.ToUpper(b.Src.Transport)+")")
	fs.Add("expires", expires)
	fs.Add("to-user", b.User)
	fs.Add("to-host", b.Domain)
	fs.Add("network-ip", ip)
	fs.Add("network-port", port)
	fs.Add("username", b.User)
	fs.Add("realm", b.Domain)
	fs.Add("user-agent", b.UserAgent)
	if reason != "" {
		fs.Add("reason", reason)
	}
	c.events.Publish(fs)
}
