package sip

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ParseDigest parses a "Digest k=v, k2="v2"" credential or challenge into a
// map with lower-case keys and unquoted values.
func ParseDigest(h string) (map[string]string, bool) {
	h = strings.TrimSpace(h)
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Digest") {
		return nil, false
	}
	out := map[string]string{}
	for _, part := range splitQuoted(rest, ',') {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = unescapeQuoted(v[1 : len(v)-1])
		}
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out, true
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// HA1 computes MD5(username:realm:password).
func HA1(username, realm, password string) string {
	return md5hex(username + ":" + realm + ":" + password)
}

// DigestResponse computes the RFC 2617 digest response for MD5.
func DigestResponse(ha1, nonce, nc, cnonce, qop, method, uri string) string {
	ha2 := md5hex(method + ":" + uri)
	if qop == "auth" || qop == "auth-int" {
		return md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	}
	return md5hex(ha1 + ":" + nonce + ":" + ha2)
}

// BuildAuthorization answers a digest challenge (client side, used for
// gateway registration and outbound calls).
func BuildAuthorization(challenge map[string]string, method, uri, username, password string) string {
	realm := challenge["realm"]
	nonce := challenge["nonce"]
	qop := ""
	if q, ok := challenge["qop"]; ok {
		for _, opt := range strings.Split(q, ",") {
			if strings.TrimSpace(opt) == "auth" {
				qop = "auth"
			}
		}
	}
	ha1 := HA1(username, realm, password)
	var b strings.Builder
	fmt.Fprintf(&b, `Digest username="%s", realm="%s", nonce="%s", uri="%s"`, username, realm, nonce, uri)
	if qop != "" {
		nc := "00000001"
		cnonce := randHex(8)
		fmt.Fprintf(&b, `, response="%s", cnonce="%s", nc=%s, qop=%s`,
			DigestResponse(ha1, nonce, nc, cnonce, qop, method, uri), cnonce, nc, qop)
	} else {
		fmt.Fprintf(&b, `, response="%s"`, DigestResponse(ha1, nonce, "", "", "", method, uri))
	}
	b.WriteString(", algorithm=MD5")
	if op, ok := challenge["opaque"]; ok {
		fmt.Fprintf(&b, `, opaque="%s"`, op)
	}
	return b.String()
}

// NonceState is the result of validating a nonce.
type NonceState int

const (
	NonceValid   NonceState = iota
	NonceStale              // correctly signed but expired
	NonceInvalid            // not issued by us (or by a previous process)
)

// NonceManager issues stateless, HMAC signed nonces and tracks nonce counts
// to reject replayed credentials.
type NonceManager struct {
	secret []byte
	ttl    time.Duration

	mu     sync.Mutex
	counts map[string]uint64
}

// NewNonceManager creates a manager with a random secret.
func NewNonceManager(ttl time.Duration) *NonceManager {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return &NonceManager{secret: secret, ttl: ttl, counts: map[string]uint64{}}
}

// New issues a fresh nonce.
func (n *NonceManager) New() string {
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(time.Now().UnixNano()))
	return hex.EncodeToString(ts[:]) + hex.EncodeToString(n.sign(ts[:]))
}

func (n *NonceManager) sign(ts []byte) []byte {
	mac := hmac.New(sha256.New, n.secret)
	mac.Write(ts)
	return mac.Sum(nil)[:16]
}

// Check validates a nonce.
func (n *NonceManager) Check(nonce string) NonceState {
	raw, err := hex.DecodeString(nonce)
	if err != nil || len(raw) != 24 {
		return NonceInvalid
	}
	if !hmac.Equal(raw[8:], n.sign(raw[:8])) {
		return NonceInvalid
	}
	issued := time.Unix(0, int64(binary.BigEndian.Uint64(raw[:8])))
	if time.Since(issued) > n.ttl {
		return NonceStale
	}
	return NonceValid
}

// UseCount records a nonce count and reports whether it is fresh (strictly
// greater than any previously seen count for the nonce).
func (n *NonceManager) UseCount(nonce string, nc uint64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.counts) > 50000 {
		// Nonces expire after ttl; dropping the table only relaxes replay
		// protection for nonces that are about to go stale anyway.
		n.counts = map[string]uint64{}
	}
	if last, ok := n.counts[nonce]; ok && nc <= last {
		return false
	}
	n.counts[nonce] = nc
	return true
}

// Challenge builds a WWW-Authenticate / Proxy-Authenticate value.
func (n *NonceManager) Challenge(realm string, stale bool) string {
	s := fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5, qop="auth"`, realm, n.New())
	if stale {
		s += ", stale=true"
	}
	return s
}
