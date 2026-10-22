package web_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/web"
)

// fakeIdP is an OpenID Connect provider for the tests: discovery, an
// authorize endpoint that signs the user in at once, a token endpoint that
// checks the client secret and PKCE, and its signing keys.
type fakeIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]map[string]string // code -> nonce, challenge, redirect
	email string
	// mischief for the negative tests
	badNonce, otherAud, expired bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, codes: map[string]map[string]string{}, email: "alice@example.com"}
	mux := http.NewServeMux()
	mux.HandleFunc("/t/", func(w http.ResponseWriter, r *http.Request) { // /t/{tenant}/.well-known/openid-configuration
		tenant := strings.Split(strings.TrimPrefix(r.URL.Path, "/t/"), "/")[0]
		if tenant == "unknown-tenant" {
			http.Error(w, "no such tenant", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL + "/t/" + tenant + "/v2.0",
			"authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/keys"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "client-123" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad authorize request: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		code := "code-" + q.Get("state")[:8]
		f.mu.Lock()
		f.codes[code] = map[string]string{"nonce": q.Get("nonce"), "challenge": q.Get("code_challenge"), "redirect": q.Get("redirect_uri")}
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		c := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		switch {
		case c == nil:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		case r.Form.Get("client_secret") != "secret-xyz":
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": "Invalid client secret provided."})
			return
		case base64.RawURLEncoding.EncodeToString(sum[:]) != c["challenge"] || r.Form.Get("redirect_uri") != c["redirect"]:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "PKCE or redirect mismatch"})
			return
		}
		claims := map[string]any{"iss": f.srv.URL + "/t/tenant-1/v2.0", "aud": "client-123", "sub": "u1", "email": f.email,
			"nonce": c["nonce"], "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "name": "Alice"}
		if f.badNonce {
			claims["nonce"] = "something-else"
		}
		if f.otherAud {
			claims["aud"] = "another-app"
		}
		if f.expired {
			claims["exp"] = time.Now().Add(-time.Hour).Unix()
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(claims), "access_token": "at"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// ssoSignIn runs the browser's part: start, the provider, the callback. It
// returns the browser and where the callback sent it ("" = signed in).
func ssoSignIn(t *testing.T, r *rig, tamper func(state string) string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(u string) *http.Response {
		t.Helper()
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	resp := get(r.base + "sso/start/microsoft")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start: %s", resp.Status)
	}
	authorize := resp.Header.Get("Location")
	if strings.Contains(authorize, "#sso-error") {
		return c, authorize
	}
	resp = get(authorize)
	cb := resp.Header.Get("Location")
	if !strings.Contains(cb, "/api/sso/callback/microsoft?code=") {
		t.Fatalf("the provider did not send the browser back: %s %s", resp.Status, cb)
	}
	if tamper != nil {
		u, _ := url.Parse(cb)
		q := u.Query()
		q.Set("state", tamper(q.Get("state")))
		u.RawQuery = q.Encode()
		cb = u.String()
	}
	resp = get(cb)
	if resp.StatusCode == http.StatusFound {
		return c, resp.Header.Get("Location")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback: %s", resp.Status)
	}
	return c, ""
}

func whoAmI(t *testing.T, r *rig, c *http.Client) map[string]any {
	t.Helper()
	resp, err := c.Get(r.base + "me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var me map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&me)
	if resp.StatusCode != 200 {
		return nil
	}
	return me
}

// TestSingleSignOn: a domain turns on Microsoft sign-in; the account's
// email signs in the extension that has it as its sign-in email; forged,
// replayed or wrong answers are refused; web passwords can be turned off.
func TestSingleSignOn(t *testing.T) {
	idp := newFakeIdP(t)
	web.SetSSODiscovery("microsoft", idp.srv.URL+"/t/{tenant}/.well-known/openid-configuration")
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"

	var out map[string]any
	if c := r.call("PUT", d+"sso/microsoft", map[string]any{"client_id": "client-123", "client_secret": "secret-xyz", "tenant_id": "unknown-tenant", "enabled": true}, &out); c != 400 {
		t.Fatalf("an unknown tenant was accepted: %d %v", c, out)
	}
	if c := r.call("PUT", d+"sso/microsoft", map[string]any{"client_id": "client-123", "client_secret": "secret-xyz", "tenant_id": "common", "enabled": true}, &out); c != 400 {
		t.Fatalf("the shared tenant was accepted: %d %v", c, out)
	}
	r.mustCall("PUT", d+"sso/microsoft", map[string]any{"client_id": "client-123", "client_secret": "secret-xyz", "tenant_id": "tenant-1", "enabled": true})
	set := r.mustCall("GET", d+"sso", nil)
	ms := set["providers"].([]any)[0].(map[string]any)
	if ms["id"] != "microsoft" || ms["enabled"] != true || ms["secret_set"] != true || ms["client_secret"] != nil ||
		!strings.HasSuffix(ms["redirect_uri"].(string), "/api/sso/callback/microsoft") {
		t.Fatalf("settings: %v", ms)
	}
	var stored string
	_ = r.conn.QueryRow(t.Context(), `select client_secret_enc from rq_sso_providers where provider = 'microsoft'`).Scan(&stored)
	if strings.Contains(stored, "secret-xyz") {
		t.Fatal("the client secret is stored in clear")
	}

	// The sign-in page's buttons (no session).
	resp, err := http.Get(r.base + "sso/providers")
	if err != nil {
		t.Fatal(err)
	}
	var buttons []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&buttons)
	resp.Body.Close()
	if len(buttons) != 1 || buttons[0]["id"] != "microsoft" || buttons[0]["need_domain"] != false {
		t.Fatalf("sign-in buttons: %v", buttons)
	}

	// Nobody has this email yet.
	if _, where := ssoSignIn(t, r, nil); !strings.Contains(where, "#sso-error=") || !strings.Contains(where, "alice%40example.com") {
		t.Fatalf("an unknown email signed in: %q", where)
	}
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1001"), map[string]any{"web_role": "manager", "web_password": "Pass-1001-x", "sso_email": " Alice@Example.com "})
	if c := r.call("PUT", d+"extensions/"+r.extensionUUID("1002"), map[string]any{"web_role": "user", "web_password": "Pass-1002-x", "sso_email": "alice@example.com"}, &out); c != 400 {
		t.Fatalf("the same sign-in email on two extensions: %d %v", c, out)
	}
	c, where := ssoSignIn(t, r, nil)
	if where != "" {
		t.Fatalf("sign-in failed: %s", where)
	}
	me := whoAmI(t, r, c)
	if me == nil || me["extension"] != "1001" || me["role"] != "manager" || me["kind"] != "extension" {
		t.Fatalf("signed in as %v", me)
	}

	// Forged state, a wrong nonce, another application's token, an expired one.
	if _, where := ssoSignIn(t, r, func(s string) string { return s + "x" }); !strings.Contains(where, "#sso-error=") {
		t.Fatalf("a forged state signed in: %q", where)
	}
	for name, set := range map[string]*bool{"nonce": &idp.badNonce, "audience": &idp.otherAud, "expiry": &idp.expired} {
		*set = true
		if c, where := ssoSignIn(t, r, nil); !strings.Contains(where, "#sso-error=") || whoAmI(t, r, c) != nil {
			t.Fatalf("a token with a wrong %s signed in: %q", name, where)
		}
		*set = false
	}

	// Web passwords off: 1001 must use the button; the administrator still signs in.
	r.mustCall("PUT", d+"sso/options", map[string]any{"web_password": false})
	var e map[string]any
	if code := r.call("POST", "login", map[string]string{"user": "1001", "password": "Pass-1001-x"}, &e); code != 400 || !strings.Contains(e["error"].(string), "single sign-on") {
		t.Fatalf("password sign-in with web passwords off: %d %v", code, e)
	}
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	// Removing the only provider turns passwords back on (nobody is locked out).
	r.mustCall("DELETE", d+"sso/microsoft", nil)
	r.session("1001", "Pass-1001-x")
	if _, where := ssoSignIn(t, r, nil); !strings.Contains(where, "#sso-error=") {
		t.Fatalf("sign-in with a removed provider: %q", where)
	}
}
