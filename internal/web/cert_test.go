package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCert(t *testing.T, dir, cn string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A renewed certificate is served without a restart.
func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	writeCert(t, dir, "first")
	r := &certReloader{certFile: filepath.Join(dir, "cert.pem"), keyFile: filepath.Join(dir, "key.pem"), log: slog.Default()}
	if err := r.load(); err != nil {
		t.Fatal(err)
	}
	cn := func() string {
		c, _ := r.get(nil)
		leaf, _ := x509.ParseCertificate(c.Certificate[0])
		return leaf.Subject.CommonName
	}
	if cn() != "first" {
		t.Fatal("first certificate not served")
	}
	time.Sleep(10 * time.Millisecond)
	writeCert(t, dir, "renewed")
	_ = os.Chtimes(filepath.Join(dir, "cert.pem"), time.Now().Add(time.Second), time.Now().Add(time.Second))
	r.checked = time.Time{} // the minute has passed
	if got := cn(); got != "renewed" {
		t.Fatalf("after renewal: %s", got)
	}
	// A broken renewal keeps the old certificate.
	_ = os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("garbage"), 0o644)
	_ = os.Chtimes(filepath.Join(dir, "cert.pem"), time.Now().Add(2*time.Second), time.Now().Add(2*time.Second))
	r.checked = time.Time{}
	if got := cn(); got != "renewed" {
		t.Fatalf("after a broken renewal: %s", got)
	}
}

func TestPasswordHash(t *testing.T) {
	h := hashPassword("correct horse")
	if !verifyPassword(h, "correct horse") || verifyPassword(h, "wrong") || verifyPassword("plain", "plain") {
		t.Fatal("password hashing")
	}
	if hashPassword("x1234567") == hashPassword("x1234567") {
		t.Fatal("no salt")
	}
}

func TestClientIPBehindProxy(t *testing.T) {
	for _, c := range []struct {
		remote, real, fwd, proto string
		ip                       string
		https                    bool
	}{
		{"203.0.113.9:5000", "", "", "", "203.0.113.9", false},
		{"203.0.113.9:5000", "198.51.100.1", "", "https", "203.0.113.9", false}, // not a local proxy: headers ignored
		{"127.0.0.1:5000", "198.51.100.1", "", "https", "198.51.100.1", true},
		{"127.0.0.1:5000", "", "10.0.0.1, 198.51.100.2", "http", "198.51.100.2", false},
		{"[::1]:5000", "", "198.51.100.3", "https", "198.51.100.3", true},
		{"127.0.0.1:5000", "", "", "https", "127.0.0.1", false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.real != "" {
			r.Header.Set("X-Real-IP", c.real)
		}
		if c.fwd != "" {
			r.Header.Set("X-Forwarded-For", c.fwd)
		}
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if got := clientIP(r); got != c.ip {
			t.Errorf("%+v: clientIP = %s", c, got)
		}
		if got := isHTTPS(r); got != c.https {
			t.Errorf("%+v: isHTTPS = %v", c, got)
		}
	}
}

func TestHostHealth(t *testing.T) {
	hh := &hostHealth{disk: t.TempDir()}
	hh.sample()
	time.Sleep(50 * time.Millisecond)
	hh.sample()
	s := hh.snapshot()
	if s.CPUs < 1 || len(s.CPUHistory) != 2 || len(s.MemHistory) != 2 {
		t.Fatalf("health: %+v", s)
	}
	if _, err := os.Stat("/proc/meminfo"); err == nil {
		if !s.Available || s.MemTotalMB <= 0 || s.MemFreeMB <= 0 || s.MemFreeMB > s.MemTotalMB || s.MemPercent < 0 || s.MemPercent > 100 {
			t.Fatalf("memory: %+v", s)
		}
		if s.DiskTotalGB <= 0 || s.DiskFreeGB > s.DiskTotalGB || s.CPUPercent < 0 || s.CPUPercent > 100 {
			t.Fatalf("disk/cpu: %+v", s)
		}
	}
	for i := 0; i < healthHistory+5; i++ {
		hh.sample()
	}
	if n := len(hh.snapshot().CPUHistory); n != healthHistory {
		t.Fatalf("history length %d", n)
	}
}
