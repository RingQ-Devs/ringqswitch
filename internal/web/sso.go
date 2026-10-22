package web

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Single sign-on: people sign in with their Microsoft (Entra ID), Google
// or LinkedIn account. OpenID Connect, the authorization code flow with
// state, nonce and (Microsoft, Google) PKCE; the ID token's signature is
// checked against the provider's published keys. The account's email
// chooses the extension: its sign-in email (extension page), else the
// email of its RingQ user, else its voicemail email. The extension needs
// web access (a role), as for a password sign-in. Each domain sets up its
// providers on Settings → Integrations; client secrets are kept encrypted
// with the data-dir key.
//
//	GET  /api/sso/providers                  the sign-in page's buttons
//	GET  /api/sso/start/{provider}?domain=   to the provider's sign-in page
//	GET  /api/sso/callback/{provider}        back from it: signs in
//	GET  /api/d/{domain}/sso                 owners: the settings
//	PUT  /api/d/{domain}/sso/{provider}      {client_id, client_secret, tenant_id, enabled}
//	DELETE /api/d/{domain}/sso/{provider}
//	PUT  /api/d/{domain}/sso/options         {web_password}

type ssoProvider struct {
	ID, Name  string
	discovery string // {tenant}: the Microsoft tenant
	tenant    bool
	pkce      bool
	scope     string
	extra     url.Values
}

var ssoProviders = []ssoProvider{
	{ID: "microsoft", Name: "Microsoft", discovery: "https://login.microsoftonline.com/{tenant}/v2.0/.well-known/openid-configuration",
		tenant: true, pkce: true, scope: "openid email profile", extra: url.Values{"prompt": {"select_account"}}},
	{ID: "google", Name: "Google", discovery: "https://accounts.google.com/.well-known/openid-configuration",
		pkce: true, scope: "openid email profile", extra: url.Values{"prompt": {"select_account"}}},
	{ID: "linkedin", Name: "LinkedIn", discovery: "https://www.linkedin.com/oauth/.well-known/openid-configuration",
		scope: "openid profile email"},
}

// ssoDiscoveryOverride points a provider at another identity provider
// (tests).
var ssoDiscoveryOverride = map[string]string{}

var ssoHTTP = &http.Client{Timeout: 15 * time.Second}

const ssoCookie = "rqsso"

var (
	reTenant   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,99}$`)
	reClientID = regexp.MustCompile(`^[A-Za-z0-9._~:/@+=-]{3,200}$`)
)

func ssoProviderByID(id string) *ssoProvider {
	for i := range ssoProviders {
		if ssoProviders[i].ID == id {
			return &ssoProviders[i]
		}
	}
	return nil
}

func (sp *ssoProvider) discoveryURL(tenant string) string {
	if u := ssoDiscoveryOverride[sp.ID]; u != "" {
		return strings.ReplaceAll(u, "{tenant}", url.PathEscape(tenant))
	}
	return strings.ReplaceAll(sp.discovery, "{tenant}", url.PathEscape(tenant))
}

// ---- provider metadata and keys (cached for an hour)

type oidcMeta struct {
	Issuer   string `json:"issuer"`
	AuthURL  string `json:"authorization_endpoint"`
	TokenURL string `json:"token_endpoint"`
	JWKSURL  string `json:"jwks_uri"`
}

type ssoCache struct {
	mu   sync.Mutex
	meta map[string]cachedMeta
	keys map[string]cachedKeys
}

type cachedMeta struct {
	m  oidcMeta
	at time.Time
}

type cachedKeys struct {
	keys map[string]*rsa.PublicKey
	at   time.Time
}

var ssoCached = &ssoCache{meta: map[string]cachedMeta{}, keys: map[string]cachedKeys{}}

func ssoGetJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := ssoHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", hostOf(u), resp.Status)
	}
	return json.Unmarshal(body, out)
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

func (c *ssoCache) metadata(ctx context.Context, u string) (oidcMeta, error) {
	c.mu.Lock()
	if m, ok := c.meta[u]; ok && time.Since(m.at) < time.Hour {
		c.mu.Unlock()
		return m.m, nil
	}
	c.mu.Unlock()
	var m oidcMeta
	if err := ssoGetJSON(ctx, u, &m); err != nil {
		return m, err
	}
	if m.Issuer == "" || m.AuthURL == "" || m.TokenURL == "" || m.JWKSURL == "" {
		return m, errors.New("the provider's OpenID configuration is incomplete")
	}
	c.mu.Lock()
	c.meta[u] = cachedMeta{m, time.Now()}
	c.mu.Unlock()
	return m, nil
}

// key returns the provider's signing key kid (the key set is fetched again
// for a key it does not know yet).
func (c *ssoCache) key(ctx context.Context, jwksURL, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	ks, ok := c.keys[jwksURL]
	c.mu.Unlock()
	if ok && time.Since(ks.at) < time.Hour {
		if k := ks.keys[kid]; k != nil {
			return k, nil
		}
	}
	var set struct {
		Keys []struct {
			Kty, Kid, N, E, Use string
		} `json:"keys"`
	}
	if err := ssoGetJSON(ctx, jwksURL, &set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		ev := 0
		for _, b := range e {
			ev = ev<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: ev}
	}
	c.mu.Lock()
	c.keys[jwksURL] = cachedKeys{keys, time.Now()}
	c.mu.Unlock()
	if k := keys[kid]; k != nil {
		return k, nil
	}
	return nil, errors.New("the ID token is signed with an unknown key")
}

// verifyIDToken checks an RS256 ID token and returns its claims.
func verifyIDToken(ctx context.Context, raw string, meta oidcMeta, clientID, nonce string, now time.Time) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("the provider sent no valid ID token")
	}
	var head struct{ Alg, Kid string }
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &head) != nil {
		return nil, errors.New("the ID token cannot be read")
	}
	if head.Alg != "RS256" {
		return nil, fmt.Errorf("the ID token is signed with %q (RS256 expected)", head.Alg)
	}
	key, err := ssoCached.key(ctx, meta.JWKSURL, head.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("the ID token cannot be read")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return nil, errors.New("the ID token's signature is wrong")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("the ID token cannot be read")
	}
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, errors.New("the ID token cannot be read")
	}
	iss := meta.Issuer
	if strings.Contains(iss, "{tenantid}") { // Microsoft "common" / "organizations"
		iss = strings.ReplaceAll(iss, "{tenantid}", claimStr(claims, "tid"))
	}
	if claimStr(claims, "iss") != iss {
		return nil, errors.New("the ID token comes from another issuer")
	}
	audOK := false
	switch a := claims["aud"].(type) {
	case string:
		audOK = a == clientID
	case []any:
		for _, x := range a {
			audOK = audOK || x == clientID
		}
	}
	if !audOK {
		return nil, errors.New("the ID token is for another application")
	}
	const skew = 2 * time.Minute
	exp, _ := claims["exp"].(float64)
	if exp == 0 || now.After(time.Unix(int64(exp), 0).Add(skew)) {
		return nil, errors.New("the ID token has expired")
	}
	if nbf, ok := claims["nbf"].(float64); ok && now.Add(skew).Before(time.Unix(int64(nbf), 0)) {
		return nil, errors.New("the ID token is not valid yet")
	}
	if subtle.ConstantTimeCompare([]byte(claimStr(claims, "nonce")), []byte(nonce)) != 1 {
		return nil, errors.New("the sign-in answer does not belong to this sign-in (nonce)")
	}
	return claims, nil
}

func claimStr(c map[string]any, k string) string {
	s, _ := c[k].(string)
	return s
}

// ssoEmail is the account's email: email, else (Microsoft) the user
// principal name. An email the provider says is not verified is refused.
func ssoEmail(c map[string]any) (string, error) {
	if v, ok := c["email_verified"]; ok {
		if b, isBool := v.(bool); (isBool && !b) || v == "false" {
			return "", errors.New("the account's email address is not verified")
		}
	}
	for _, k := range []string{"email", "preferred_username", "upn"} {
		if e := strings.ToLower(strings.TrimSpace(claimStr(c, k))); len(e) <= 254 && reEmail.MatchString(e) {
			return e, nil
		}
	}
	return "", errors.New("the provider sent no email address (allow the email scope)")
}

// ---- settings

type ssoSetting struct {
	Provider, ClientID, SecretEnc, Tenant string
	Enabled                               bool
}

func (s *Server) ssoSettings(ctx context.Context, domainUUID string) (map[string]ssoSetting, error) {
	out := map[string]ssoSetting{}
	if s.store == nil || !s.store.HasTable("rq_sso_providers") {
		return out, nil
	}
	rows, err := s.store.Pool().Query(ctx, `select provider, coalesce(client_id, ''), coalesce(client_secret_enc, ''), coalesce(tenant_id, ''), enabled
		from rq_sso_providers where domain_uuid = $1::uuid`, domainUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x ssoSetting
		if err := rows.Scan(&x.Provider, &x.ClientID, &x.SecretEnc, &x.Tenant, &x.Enabled); err != nil {
			return nil, err
		}
		out[x.Provider] = x
	}
	return out, rows.Err()
}

// ssoSecret opens a stored client secret (kept as it is when no key was
// available to encrypt it).
func (s *Server) ssoSecret(enc string) string {
	if strings.HasPrefix(enc, "v1:") {
		return s.openPass(enc)
	}
	return enc
}

// passwordSignIn reports whether the domain allows web-password sign-in.
func (s *Server) passwordSignIn(ctx context.Context, domainUUID string) bool {
	if s.store == nil || !s.store.HasTable("rq_signin_options") {
		return true
	}
	var on bool
	if err := s.store.Pool().QueryRow(ctx, `select web_password from rq_signin_options where domain_uuid = $1::uuid`, domainUUID).Scan(&on); err != nil {
		return true
	}
	return on
}

func (s *Server) publicBase(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) ssoRedirect(r *http.Request, provider string) string {
	return s.publicBase(r) + "/api/sso/callback/" + provider
}

// ssoAdminAPI: the domain's providers and sign-in options (owners).
func (s *Server) ssoAdminAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	if !s.store.HasTable("rq_sso_providers") {
		return badRequest("the sign-in tables are missing: restart RingQswitch once to add them")
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		set, err := s.ssoSettings(ctx, dom.uuid)
		if err != nil {
			return err
		}
		list := []map[string]any{}
		for _, sp := range ssoProviders {
			x := set[sp.ID]
			list = append(list, map[string]any{"id": sp.ID, "name": sp.Name, "configured": x.ClientID != "", "enabled": x.Enabled,
				"client_id": x.ClientID, "tenant_id": x.Tenant, "secret_set": x.SecretEnc != "", "needs_tenant": sp.tenant,
				"redirect_uri": s.ssoRedirect(r, sp.ID)})
		}
		writeJSON(w, map[string]any{"providers": list, "web_password": s.passwordSignIn(ctx, dom.uuid)})
		return nil
	case len(parts) == 1 && parts[0] == "options" && r.Method == http.MethodPut:
		var b struct {
			WebPassword *bool `json:"web_password"`
		}
		if err := decodeBody(r, &b); err != nil {
			return err
		}
		if b.WebPassword == nil {
			return badRequest("web_password: true or false")
		}
		if !*b.WebPassword {
			set, err := s.ssoSettings(ctx, dom.uuid)
			if err != nil {
				return err
			}
			on := false
			for _, x := range set {
				on = on || x.Enabled
			}
			if !on {
				return badRequest("turn on a single sign-on provider first: without web passwords nobody of this domain could sign in")
			}
		}
		if _, err := s.store.Pool().Exec(ctx, `insert into rq_signin_options (domain_uuid, web_password, update_date) values ($1::uuid, $2, now())
			on conflict (domain_uuid) do update set web_password = excluded.web_password, update_date = now()`, dom.uuid, *b.WebPassword); err != nil {
			return err
		}
		s.sw.RecordEvent("security", fmt.Sprintf("%s: web password sign-in turned %s by %s", dom.name, map[bool]string{true: "on", false: "off"}[*b.WebPassword], p.key()))
		writeJSON(w, map[string]bool{"web_password": *b.WebPassword})
		return nil
	case len(parts) == 1 && ssoProviderByID(parts[0]) != nil && r.Method == http.MethodPut:
		sp := ssoProviderByID(parts[0])
		var b struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			TenantID     string `json:"tenant_id"`
			Enabled      *bool  `json:"enabled"`
		}
		if err := decodeBody(r, &b); err != nil {
			return err
		}
		set, err := s.ssoSettings(ctx, dom.uuid)
		if err != nil {
			return err
		}
		cur := set[sp.ID]
		id, secret, tenant := strings.TrimSpace(b.ClientID), strings.TrimSpace(b.ClientSecret), strings.TrimSpace(b.TenantID)
		enabled := cur.Enabled
		if b.Enabled != nil {
			enabled = *b.Enabled
		}
		if !reClientID.MatchString(id) {
			return badRequest("client ID: copy it from the provider's app registration")
		}
		if secret == "" && cur.SecretEnc == "" {
			return badRequest("client secret: copy it from the provider's app registration")
		}
		if len(secret) > 500 {
			return badRequest("client secret: too long")
		}
		if sp.tenant && !reTenant.MatchString(tenant) {
			return badRequest("tenant ID: the Directory (tenant) ID of your Microsoft Entra ID, e.g. 72f988bf-86f1-41af-91ab-2d7cd011db47")
		}
		// Only your own organization's accounts: a shared tenant would let
		// any Microsoft account sign in with an email it merely claims.
		switch strings.ToLower(tenant) {
		case "common", "organizations", "consumers":
			return badRequest("tenant ID: your organization's own tenant ID, not %q (that would let accounts of other organizations sign in)", tenant)
		}
		if !sp.tenant {
			tenant = ""
		}
		if enabled {
			// A wrong tenant shows now, not at someone's sign-in.
			if _, err := ssoCached.metadata(ctx, sp.discoveryURL(tenant)); err != nil {
				return badRequest("%s's sign-in configuration could not be read (%v): check the tenant ID", sp.Name, err)
			}
		}
		enc := cur.SecretEnc
		if secret != "" {
			if enc = s.sealPass(secret); enc == "" {
				enc = secret
			}
		}
		if _, err := s.store.Pool().Exec(ctx, `insert into rq_sso_providers (domain_uuid, provider, client_id, client_secret_enc, tenant_id, enabled, update_date)
			values ($1::uuid, $2, $3, $4, $5, $6, now()) on conflict (domain_uuid, provider) do update set client_id = excluded.client_id,
			client_secret_enc = excluded.client_secret_enc, tenant_id = excluded.tenant_id, enabled = excluded.enabled, update_date = now()`,
			dom.uuid, sp.ID, id, enc, tenant, enabled); err != nil {
			return err
		}
		s.sw.RecordEvent("security", fmt.Sprintf("%s: %s sign-in %s by %s", dom.name, sp.Name, map[bool]string{true: "turned on", false: "saved (off)"}[enabled], p.key()))
		writeJSON(w, map[string]any{"id": sp.ID, "enabled": enabled})
		return nil
	case len(parts) == 1 && ssoProviderByID(parts[0]) != nil && r.Method == http.MethodDelete:
		if _, err := s.store.Pool().Exec(ctx, `delete from rq_sso_providers where domain_uuid = $1::uuid and provider = $2`, dom.uuid, parts[0]); err != nil {
			return err
		}
		// No provider left: passwords come back on, so nobody is locked out.
		_, _ = s.store.Pool().Exec(ctx, `update rq_signin_options set web_password = true, update_date = now() where domain_uuid = $1::uuid
			and not exists (select 1 from rq_sso_providers where domain_uuid = $1::uuid and enabled)`, dom.uuid)
		writeJSON(w, map[string]string{"ok": "removed"})
		return nil
	}
	return errMethod
}

// ---- signing in

type ssoState struct {
	Provider string `json:"p"`
	Domain   string `json:"d"`
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v,omitempty"`
	Exp      int64  `json:"e"`
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) sealState(st ssoState) string {
	data, _ := json.Marshal(st)
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("sso:" + payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) openState(v string) (ssoState, bool) {
	var st ssoState
	payload, sig, ok := strings.Cut(v, ".")
	if !ok {
		return st, false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("sso:" + payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return st, false
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(data, &st) != nil || time.Now().Unix() > st.Exp {
		return st, false
	}
	return st, true
}

// ssoDomains lists the domains with the provider turned on.
func (s *Server) ssoDomains(ctx context.Context, provider string) ([]domainRow, error) {
	if s.store == nil || !s.store.HasTable("rq_sso_providers") {
		return nil, nil
	}
	rows, err := s.store.Pool().Query(ctx, `select d.domain_uuid::text, d.domain_name from rq_sso_providers p
		join v_domains d on d.domain_uuid = p.domain_uuid
		where p.provider = $1 and p.enabled and coalesce(d.domain_enabled::text, 'true') in ('true', 't') order by d.domain_name`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domainRow
	for rows.Next() {
		var d domainRow
		if err := rows.Scan(&d.uuid, &d.name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ssoDomainFor picks the domain a sign-in is for: the one asked for, else
// the one named like the address in the browser, else the only one.
func ssoDomainFor(list []domainRow, asked, host string) (domainRow, bool) {
	host, _, _ = strings.Cut(host, ":")
	for _, d := range list {
		if asked != "" && strings.EqualFold(d.name, asked) {
			return d, true
		}
	}
	if asked != "" {
		return domainRow{}, false
	}
	for _, d := range list {
		if strings.EqualFold(d.name, host) {
			return d, true
		}
	}
	if len(list) == 1 {
		return list[0], true
	}
	return domainRow{}, false
}

// ssoPublic serves /api/sso/... (no session needed).
func (s *Server) ssoPublic(w http.ResponseWriter, r *http.Request, parts []string) {
	ctx := r.Context()
	switch {
	case len(parts) == 1 && parts[0] == "providers":
		out := []map[string]any{}
		for _, sp := range ssoProviders {
			list, err := s.ssoDomains(ctx, sp.ID)
			if err != nil || len(list) == 0 {
				continue
			}
			_, one := ssoDomainFor(list, "", r.Host)
			out = append(out, map[string]any{"id": sp.ID, "name": sp.Name, "need_domain": !one})
		}
		writeJSON(w, out)
	case len(parts) == 2 && parts[0] == "start" && ssoProviderByID(parts[1]) != nil:
		s.ssoStart(w, r, ssoProviderByID(parts[1]))
	case len(parts) == 2 && parts[0] == "callback" && ssoProviderByID(parts[1]) != nil:
		s.ssoCallback(w, r, ssoProviderByID(parts[1]))
	default:
		httpError(w, http.StatusNotFound, "not found")
	}
}

// ssoFail sends the browser back to the sign-in page with the reason.
func (s *Server) ssoFail(w http.ResponseWriter, r *http.Request, sp *ssoProvider, msg string) {
	s.log.Warn("web: single sign-on failed", "provider", sp.ID, "ip", clientIP(r), "reason", msg)
	http.SetCookie(w, &http.Cookie{Name: ssoCookie, Value: "", Path: "/api/sso/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/#sso-error="+url.QueryEscape(sp.Name+": "+msg), http.StatusFound)
}

func (s *Server) ssoStart(w http.ResponseWriter, r *http.Request, sp *ssoProvider) {
	ctx := r.Context()
	if s.loginBlocked(clientIP(r)) {
		s.ssoFail(w, r, sp, "too many failed sign-ins; wait 10 minutes")
		return
	}
	list, err := s.ssoDomains(ctx, sp.ID)
	if err != nil {
		s.ssoFail(w, r, sp, "the settings cannot be read")
		return
	}
	dom, ok := ssoDomainFor(list, strings.TrimSpace(r.URL.Query().Get("domain")), r.Host)
	if !ok {
		s.ssoFail(w, r, sp, "single sign-on is not turned on for this company (check the domain)")
		return
	}
	set, err := s.ssoSettings(ctx, dom.uuid)
	cfg := set[sp.ID]
	if err != nil || !cfg.Enabled || cfg.ClientID == "" {
		s.ssoFail(w, r, sp, "single sign-on is not set up")
		return
	}
	meta, err := ssoCached.metadata(ctx, sp.discoveryURL(cfg.Tenant))
	if err != nil {
		s.ssoFail(w, r, sp, "the provider cannot be reached ("+err.Error()+")")
		return
	}
	st := ssoState{Provider: sp.ID, Domain: dom.name, State: randToken(24), Nonce: randToken(24), Exp: time.Now().Add(10 * time.Minute).Unix()}
	q := url.Values{"response_type": {"code"}, "client_id": {cfg.ClientID}, "redirect_uri": {s.ssoRedirect(r, sp.ID)},
		"scope": {sp.scope}, "state": {st.State}, "nonce": {st.Nonce}}
	if sp.pkce {
		st.Verifier = randToken(48)
		sum := sha256.Sum256([]byte(st.Verifier))
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		q.Set("code_challenge_method", "S256")
	}
	for k, v := range sp.extra {
		q[k] = v
	}
	// The cookie comes back on the provider's redirect (a top-level GET), so Lax.
	http.SetCookie(w, &http.Cookie{Name: ssoCookie, Value: s.sealState(st), Path: "/api/sso/", MaxAge: 600, HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	sep := "?"
	if strings.Contains(meta.AuthURL, "?") {
		sep = "&"
	}
	http.Redirect(w, r, meta.AuthURL+sep+q.Encode(), http.StatusFound)
}

func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request, sp *ssoProvider) {
	ctx := r.Context()
	ip := clientIP(r)
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.ssoFail(w, r, sp, firstNonEmpty(q.Get("error_description"), e))
		return
	}
	c, err := r.Cookie(ssoCookie)
	if err != nil {
		s.ssoFail(w, r, sp, "the sign-in took too long or started in another browser; try again")
		return
	}
	st, ok := s.openState(c.Value)
	if !ok || st.Provider != sp.ID || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		s.loginFailed(ip)
		s.ssoFail(w, r, sp, "the sign-in answer does not belong to this sign-in; try again")
		return
	}
	list, err := s.ssoDomains(ctx, sp.ID)
	dom, found := ssoDomainFor(list, st.Domain, "")
	if err != nil || !found {
		s.ssoFail(w, r, sp, "single sign-on is not turned on for this company")
		return
	}
	set, err := s.ssoSettings(ctx, dom.uuid)
	cfg := set[sp.ID]
	if err != nil || !cfg.Enabled {
		s.ssoFail(w, r, sp, "single sign-on is not set up")
		return
	}
	meta, err := ssoCached.metadata(ctx, sp.discoveryURL(cfg.Tenant))
	if err != nil {
		s.ssoFail(w, r, sp, "the provider cannot be reached ("+err.Error()+")")
		return
	}
	idToken, err := s.ssoExchange(ctx, meta, cfg, q.Get("code"), s.ssoRedirect(r, sp.ID), st.Verifier)
	if err != nil {
		s.loginFailed(ip)
		s.ssoFail(w, r, sp, err.Error())
		return
	}
	claims, err := verifyIDToken(ctx, idToken, meta, cfg.ClientID, st.Nonce, time.Now())
	if err != nil {
		s.loginFailed(ip)
		s.ssoFail(w, r, sp, err.Error())
		return
	}
	email, err := ssoEmail(claims)
	if err != nil {
		s.ssoFail(w, r, sp, err.Error())
		return
	}
	p, err := s.ssoPrincipal(ctx, dom, email)
	if err != nil {
		s.loginFailed(ip)
		s.sw.RecordEvent("security", fmt.Sprintf("web interface: %s sign-in of %s refused from %s: %v", sp.Name, email, ip, err))
		s.ssoFail(w, r, sp, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ssoCookie, Value: "", Path: "/api/sso/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "rqsession", Value: s.newSession(p), Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	s.log.Info("web: login", "user", p.key(), "role", p.Role, "ip", ip, "sso", sp.ID, "email", email)
	s.agentSignedIn(p)
	s.sw.RecordEvent("security", fmt.Sprintf("web interface: %s (%s) signed in with %s (%s) from %s", p.key(), p.Role, sp.Name, email, ip))
	// The Strict session cookie goes with requests the page makes itself.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=/"><title>Signing in…</title><p>Signing in… <a href="/">Continue</a></p>`)
}

// ssoExchange trades the code for the ID token.
func (s *Server) ssoExchange(ctx context.Context, meta oidcMeta, cfg ssoSetting, code, redirect, verifier string) (string, error) {
	if code == "" {
		return "", errors.New("the provider sent no sign-in code")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {cfg.ClientID}, "client_secret": {s.ssoSecret(cfg.SecretEnc)}}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := ssoHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("the provider cannot be reached (%v)", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tok)
	if resp.StatusCode != http.StatusOK || tok.IDToken == "" {
		msg := firstNonEmpty(tok.Desc, tok.Error, resp.Status)
		if strings.Contains(strings.ToLower(msg), "secret") || tok.Error == "invalid_client" {
			msg += " (check the client secret)"
		}
		return "", errors.New("the provider refused the sign-in: " + msg)
	}
	return tok.IDToken, nil
}

// ssoPrincipal finds the extension of an email in the domain: its sign-in
// email, else its RingQ user's email, else its voicemail email.
func (s *Server) ssoPrincipal(ctx context.Context, dom domainRow, email string) (*principal, error) {
	base := `select w.extension_uuid::text, w.role, e.extension,
		coalesce(nullif(trim(coalesce(e.directory_first_name, '') || ' ' || coalesce(e.directory_last_name, '')), ''), e.effective_caller_id_name, '')
		from rq_web_users w join v_extensions e on e.extension_uuid = w.extension_uuid
		where w.enabled and e.domain_uuid = $1::uuid and coalesce(e.enabled::text, 'true') in ('true', 't') and `
	queries := []string{}
	if s.store.HasColumn("rq_web_users", "sso_email") {
		queries = append(queries, base+`lower(trim(w.sso_email)) = $2`)
	}
	if s.store.HasTable("v_users") && s.store.HasTable("v_extension_users") {
		queries = append(queries, base+`e.extension_uuid in (select eu.extension_uuid from v_extension_users eu join v_users u on u.user_uuid = eu.user_uuid
			where lower(trim(u.user_email)) = $2 and coalesce(u.user_enabled::text, 'true') in ('true', 't'))`)
	}
	if s.store.HasTable("v_voicemails") {
		queries = append(queries, base+`e.extension in (select v.voicemail_id from v_voicemails v where v.domain_uuid = $1::uuid
			and $2 = any(string_to_array(regexp_replace(lower(coalesce(v.voicemail_mail_to, '')), '\s', '', 'g'), ',')))`)
	}
	for _, q := range queries {
		rows, err := s.store.Pool().Query(ctx, q, dom.uuid, email)
		if err != nil {
			return nil, err
		}
		var found []principal
		for rows.Next() {
			var p principal
			if err := rows.Scan(&p.ExtensionUUID, &p.Role, &p.Extension, &p.Name); err != nil {
				rows.Close()
				return nil, err
			}
			found = append(found, p)
		}
		rows.Close()
		if len(found) > 1 {
			return nil, fmt.Errorf("%s is the email of several extensions: set a sign-in email on one of them", email)
		}
		if len(found) == 1 {
			p := found[0]
			p.Kind, p.Domain, p.DomainUUID, p.User = "extension", dom.name, dom.uuid, email
			if p.Role != RoleOwner && p.Role != RoleManager {
				p.Role = RoleUser
			}
			p.Name = firstNonEmpty(p.Name, p.Extension)
			_, _ = s.store.Pool().Exec(ctx, `update rq_web_users set last_login = now() where extension_uuid = $1::uuid`, p.ExtensionUUID)
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no extension with web access uses %s: set it as the sign-in email on the extension page", email)
}

// checkSSOEmail validates an extension's sign-in email (unique in the domain).
func checkSSOEmail(ctx context.Context, tx pgx.Tx, dom domainRow, extUUID, email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", nil
	}
	if len(email) > 254 || !reEmail.MatchString(email) {
		return "", badRequest("sign-in email: an email address like name@company.com")
	}
	var other string
	err := tx.QueryRow(ctx, `select e.extension from rq_web_users w join v_extensions e on e.extension_uuid = w.extension_uuid
		where w.domain_uuid = $1::uuid and lower(trim(w.sso_email)) = $2 and w.extension_uuid <> $3::uuid limit 1`, dom.uuid, email, extUUID).Scan(&other)
	if err == nil {
		return "", badRequest("sign-in email %s is already used by extension %s", email, other)
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	return email, nil
}
