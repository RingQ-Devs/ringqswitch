// Package web is RingQswitch's administration interface: an HTTPS server
// with a single-page application (embedded) and a JSON API. Everything it
// configures is stored in the PostgreSQL database (RingQ tables
// plus RingQswitch's rq_* tables), so the RingQ portal and this interface
// can be used side by side. After each change the switch reloads.
package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	golog "log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/db"
	"github.com/Cal4Care-Developers/RingQswitch/internal/transcribe"
)

//go:embed static
var staticFiles embed.FS

// Server is the web interface.
type Server struct {
	cfg    config.Web
	sw     *core.Core
	store  *db.Store
	log    *slog.Logger
	srv    *http.Server
	addr   net.Addr
	hub    *hub
	rtc    *rtcEngine
	meets  *meetings
	health *hostHealth
	stop   chan struct{}

	mu       sync.Mutex
	sessions map[string]session
	// agents logged out by signing out of the portal (and their status then)
	signedOutAgents map[string]string
	fails           map[string][]time.Time // login failures per client IP
	secret          []byte
	pass            passBox         // the key of the viewable web passwords
	aiCrawl         aiCrawls        // AI agents' web pages being fetched
	aiClient        *http.Client    // fetches them (nil: the default; tests)
	faxActive       map[string]bool // eFax jobs being sent (guarded by mu)
	idleLogout      sync.Map        // domain name: idleEntry (web auto logout)
	// call transcription (guarded by mu): each domain's settings (by
	// name), the speech recogniser found, and jobs running
	trSettings  map[string]trSettings
	trEng       trEngine
	trBusy      int
	trDownloads map[string]*trDownload            // speech models being downloaded
	lives       map[string]*liveSession           // live transcripts of calls in progress
	liveServers map[string]*transcribe.LiveServer // the live recogniser per model
	liveErr     map[string]string                 // why a model's live recogniser is not running
	liveWake    chan struct{}                     // check the live recognisers now
}

type session struct {
	p       *principal
	expires time.Time
	seen    time.Time // the last request
}

type idleEntry struct {
	minutes int
	at      time.Time
}

// idleMinutes is the web auto logout of a principal's domain (Security
// settings; the default domain's for the administrator), 0 = off.
func (s *Server) idleMinutes(p *principal) int {
	if s.store == nil || p == nil {
		return 0
	}
	name := firstNonEmpty(p.Domain, s.sw.Config().Directory.Domain)
	if v, ok := s.idleLogout.Load(name); ok && time.Since(v.(idleEntry).at) < 30*time.Second {
		return v.(idleEntry).minutes
	}
	m := 0
	if d := s.sw.Config().Directory.Find(name); d != nil && d.UUID != "" && s.store.HasTable("rq_settings") {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		m = atoi(s.store.Setting(ctx, d.UUID, "web_idle_logout"), 0)
		cancel()
	}
	s.idleLogout.Store(name, idleEntry{m, time.Now()})
	return m
}

const sessionTTL = 12 * time.Hour

// Start launches the web interface. store may be nil (live pages only).
func Start(cfg config.Web, sw *core.Core, store *db.Store, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, sw: sw, store: store, log: log, sessions: map[string]session{}, signedOutAgents: map[string]string{}, fails: map[string][]time.Time{}, faxActive: map[string]bool{},
		hub: newHub(), meets: &meetings{rooms: map[string]*meetRoom{}, tokens: map[string]*meetPeer{}}, stop: make(chan struct{}),
		trDownloads: map[string]*trDownload{}}
	s.secret = make([]byte, 32)
	_, _ = rand.Read(s.secret)
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", s.count404(s.securityHeaders(http.FileServer(http.FS(static)))))
	// Meeting links (/meet/m/46ak-c6kg-hvcz) open the meeting page.
	meetPage, _ := fs.ReadFile(static, "meet.html")
	mux.Handle("/meet/m/", s.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(meetPage)
	})))
	mux.HandleFunc("/api/", s.route)
	s.srv = &http.Server{Addr: cfg.Listen, Handler: s.webGuard(mux), ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 120 * time.Second, ErrorLog: golog.New(httpErrorLog{log}, "", 0)}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	s.addr = ln.Addr()
	if cfg.WebRTCPort > 0 {
		publicIP := firstNonEmpty(cfg.PublicIP, sw.Config().Settings.ExtRTPIP)
		// Audio over TCP on the web port, for networks that block UDP. Not
		// when the web interface only listens on this server (behind a proxy).
		var split *splitListener
		var iceTCP net.Listener
		if a, ok := ln.Addr().(*net.TCPAddr); ok && !a.IP.IsLoopback() {
			split = newSplitListener(ln)
			ln, iceTCP = split.Web(), split.ICE()
		}
		if s.rtc, err = newRTCEngine(cfg.WebRTCPort, publicIP, iceTCP); err != nil {
			// The rest of the interface works without the dialer and meetings.
			log.Warn("web: browser dialer and meetings are off", "err", err)
			s.rtc = nil
			if iceTCP != nil {
				iceTCP.Close()
			}
		} else {
			log.Info("web: dialer and meetings audio", "udp", cfg.WebRTCPort, "tcp", s.rtc.tcpPort, "public_ip", publicIP)
		}
	}
	s.health = &hostHealth{disk: sw.Config().Settings.RecordingsDir}
	go s.health.run(s.stop)
	go s.livePump(s.stop)
	if store != nil {
		go s.wakeupLoop(s.stop) // hotel wake-up calls
		go s.faxLoop(s.stop)    // eFax: sending, and filing results
		s.aiStart()             // AI agents answer their calls
		go s.blfPump(s.stop)    // lamps of phone keys to the dialers
		if n, err := InstallDeskPhoneCodes(context.Background(), store); err != nil {
			log.Warn("web: adding the desk phone dial codes", "err", err)
		} else if n > 0 {
			log.Info("web: desk phone dial codes added", "count", n)
			sw.Reload() //nolint:errcheck
		}
		s.liveWake = make(chan struct{}, 1)
		if sub := s.trSubscribe(); sub != nil {
			go s.transcribeLoop(s.stop, sub) // call and voicemail transcripts, sentiment
		}
		if sub := s.liveSubscribe(); sub != nil {
			go s.liveLoop(s.stop, sub) // live transcripts during calls
		}
	}
	scheme := "https"
	switch strings.ToLower(cfg.TLS) {
	case "off", "none", "false":
		scheme = "http"
		go func() { _ = s.srv.Serve(ln) }()
	default:
		s.srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		if certFile, keyFile, ok := strings.Cut(cfg.TLS, ","); ok {
			r := &certReloader{certFile: strings.TrimSpace(certFile), keyFile: strings.TrimSpace(keyFile), log: log}
			if err := r.load(); err != nil {
				ln.Close()
				return nil, fmt.Errorf("web: certificate %s: %w", r.certFile, err)
			}
			r.checked = time.Now()
			s.srv.TLSConfig.GetCertificate = r.get
		} else {
			cert, err := s.certificate()
			if err != nil {
				ln.Close()
				return nil, fmt.Errorf("web: %w", err)
			}
			s.srv.TLSConfig.Certificates = []tls.Certificate{cert}
		}
		go func() { _ = s.srv.Serve(tls.NewListener(ln, s.srv.TLSConfig)) }()
	}
	log.Info("web interface listening", "url", fmt.Sprintf("%s://%s/", scheme, cfg.Listen), "user", cfg.AdminUser)
	return s, nil
}

// Addr returns the address the web interface listens on.
func (s *Server) Addr() net.Addr { return s.addr }

// Close stops the web interface.
func (s *Server) Close() {
	close(s.stop)
	for _, c := range s.hub.list() {
		_ = c.conn.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	if s.rtc != nil {
		s.rtc.Close()
	}
}

// certReloader serves a certificate from files and reloads it when they
// change (certificate renewals need no restart).
type certReloader struct {
	certFile, keyFile string
	log               *slog.Logger
	mu                sync.Mutex
	cert              *tls.Certificate
	mod               time.Time
	checked           time.Time
}

func (r *certReloader) load() error {
	st, err := os.Stat(r.certFile)
	if err != nil {
		return err
	}
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	r.cert, r.mod = &c, st.ModTime()
	return nil
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.checked) > time.Minute {
		r.checked = time.Now()
		if st, err := os.Stat(r.certFile); err == nil && !st.ModTime().Equal(r.mod) {
			if err := r.load(); err != nil {
				r.log.Warn("web: new certificate could not be loaded; keeping the old one", "err", err)
			} else {
				r.log.Info("web: certificate reloaded", "cert", r.certFile)
			}
		}
	}
	return r.cert, nil
}

// certificate loads "cert,key" files, or a self-signed certificate kept in
// the data directory (created on first start).
func (s *Server) certificate() (tls.Certificate, error) {
	if certFile, keyFile, ok := strings.Cut(s.cfg.TLS, ","); ok {
		return tls.LoadX509KeyPair(strings.TrimSpace(certFile), strings.TrimSpace(keyFile))
	}
	certPath := filepath.Join(s.cfg.DataDir, "web-cert.pem")
	keyPath := filepath.Join(s.cfg.DataDir, "web-key.pem")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: s.cfg.Brand + " " + host, Organization: []string{s.cfg.Brand}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host, "localhost"},
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ipn.IP)
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(s.cfg.DataDir, 0o750); err == nil {
		if os.WriteFile(keyPath, keyPEM, 0o600) == nil && os.WriteFile(certPath, certPEM, 0o644) == nil {
			s.log.Info("web: created a self-signed certificate (browsers will ask you to accept it once)", "cert", certPath)
		}
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'")
		h.Set("Permissions-Policy", "camera=(self), microphone=(self), display-capture=(self)")
		// Always revalidate, so an upgrade is picked up at once.
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Sessions

// clientIP is the browser's address. Behind a reverse proxy on the same
// server (a reverse proxy in front of the web interface), it is the address the proxy
// passes in X-Real-IP or X-Forwarded-For; those headers are believed only
// from a loopback peer, which a remote browser cannot be.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if fromLocalProxy(r) {
		fwd := r.Header.Get("X-Real-IP")
		if fwd == "" {
			list := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
			fwd = list[len(list)-1]
		}
		if ip, err := netip.ParseAddr(strings.TrimSpace(fwd)); err == nil {
			return ip.Unmap().String()
		}
	}
	return host
}

func fromLocalProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback() && (r.Header.Get("X-Real-IP") != "" || r.Header.Get("X-Forwarded-For") != "")
}

// isHTTPS reports whether the browser uses https (directly, or through a
// local proxy that says so).
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (fromLocalProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

func (s *Server) newSession(p *principal) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(tok))
	signed := tok + "." + hex.EncodeToString(mac.Sum(nil))
	s.mu.Lock()
	s.sessions[tok] = session{p: p, expires: time.Now().Add(sessionTTL), seen: time.Now()}
	for k, v := range s.sessions {
		if time.Now().After(v.expires) {
			delete(s.sessions, k)
		}
	}
	s.mu.Unlock()
	return signed
}

// sessionPrincipal returns who is signed in (nil = nobody).
func (s *Server) sessionPrincipal(r *http.Request) *principal {
	c, err := r.Cookie("rqsession")
	if err != nil {
		return nil
	}
	tok, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return nil
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(tok))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return nil
	}
	s.mu.Lock()
	sess, ok := s.sessions[tok]
	if !ok || time.Now().After(sess.expires) {
		delete(s.sessions, tok)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	// Web auto logout: no request at all for that long (the page itself
	// signs out sooner when nobody uses it).
	if m := s.idleMinutes(sess.p); m > 0 && time.Since(sess.seen) > time.Duration(m)*time.Minute+time.Minute {
		s.mu.Lock()
		delete(s.sessions, tok)
		s.mu.Unlock()
		s.log.Info("web: signed out after no activity", "user", sess.p.key(), "minutes", m)
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.sessions[tok]; ok {
		cur.expires, cur.seen = time.Now().Add(sessionTTL), time.Now()
		s.sessions[tok] = cur
	}
	return sess.p
}

// dropSessions signs out an extension everywhere (web access removed or
// password changed by an owner).
func (s *Server) dropSessions(match func(*principal) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.sessions {
		if match(v.p) {
			delete(s.sessions, k)
		}
	}
}

func (s *Server) loginBlocked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.fails[ip][:0]
	for _, t := range s.fails[ip] {
		if time.Since(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	s.fails[ip] = recent
	return len(recent) >= 10
}

func (s *Server) loginFailed(ip string) {
	s.mu.Lock()
	s.fails[ip] = append(s.fails[ip], time.Now())
	s.mu.Unlock()
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.loginBlocked(ip) {
		httpError(w, http.StatusTooManyRequests, "too many failed logins; wait 10 minutes")
		return
	}
	var body struct{ User, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	p, err := s.authenticate(r.Context(), body.User, body.Password)
	if err != nil {
		s.loginFailed(ip)
		s.fail(w, err)
		return
	}
	if p == nil {
		s.loginFailed(ip)
		s.sw.GuardFail(config.JailWebLogin, ip, "failed sign-in as "+truncate(body.User, 40))
		s.log.Warn("web: failed login", "user", body.User, "ip", ip)
		s.sw.RecordEvent("security", fmt.Sprintf("web interface: failed sign-in as %q from %s", body.User, ip))
		e := core.NewEvent(core.EvCustom, "ringq::web_login_failed")
		e.Add("User", body.User)
		e.Add("Network-IP", ip)
		s.sw.Events().Publish(e)
		time.Sleep(500 * time.Millisecond)
		httpError(w, http.StatusUnauthorized, "wrong user name or password")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "rqsession", Value: s.newSession(p), Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	s.log.Info("web: login", "user", p.key(), "role", p.Role, "ip", ip)
	s.agentSignedIn(p)
	s.sw.RecordEvent("security", fmt.Sprintf("web interface: %s (%s) signed in from %s", p.key(), p.Role, ip))
	writeJSON(w, p)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.agentSignedOut(s.sessionPrincipal(r))
	if c, err := r.Cookie("rqsession"); err == nil {
		tok, _, _ := strings.Cut(c.Value, ".")
		s.mu.Lock()
		delete(s.sessions, tok)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "rqsession", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, map[string]string{"ok": "bye"})
}

// ---------------------------------------------------------------------------
// Helpers

type apiError struct {
	code int
	msg  string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &apiError{http.StatusBadRequest, fmt.Sprintf(format, args...)}
}

func notFound(what string) error { return &apiError{http.StatusNotFound, what + " not found"} }

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		httpError(w, ae.code, ae.msg)
		return
	}
	s.log.Warn("web: request failed", "err", err)
	httpError(w, http.StatusInternalServerError, err.Error())
}

func decodeBody(r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(v); err != nil {
		return badRequest("invalid JSON: %v", err)
	}
	return nil
}

// httpErrorLog drops the TLS handshake errors of scanners and of browsers
// that do not trust the certificate; other server errors go to the log
// (debug).
type httpErrorLog struct{ log *slog.Logger }

func (w httpErrorLog) Write(p []byte) (int, error) {
	if msg := strings.TrimSpace(string(p)); !strings.Contains(msg, "TLS handshake error") {
		w.log.Debug("web: " + msg)
	}
	return len(p), nil
}
