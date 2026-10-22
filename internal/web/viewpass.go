package web

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Web passwords are checked against their hash; owners can also see them
// on the extension page, like the SIP password. For that an encrypted copy
// is kept (rq_web_users.web_password_enc, AES-256-GCM) with a key that
// stays on the server (data-dir/web-passwords.key), not in the database.

const passKeyFile = "web-passwords.key"

type passBox struct {
	once sync.Once
	aead cipher.AEAD
	err  error
}

func (s *Server) passAEAD() (cipher.AEAD, error) {
	s.pass.once.Do(func() {
		dir := s.cfg.DataDir
		if dir == "" {
			s.pass.err = errors.New("no data-dir")
			return
		}
		path := filepath.Join(dir, passKeyFile)
		key, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			key = make([]byte, 32)
			if _, err = rand.Read(key); err == nil {
				_ = os.MkdirAll(dir, 0o750)
				err = os.WriteFile(path, key, 0o600)
			}
		}
		if err != nil {
			s.pass.err = err
			s.log.Warn("web: web passwords cannot be shown (no key)", "file", path, "err", err)
			return
		}
		if len(key) != 32 {
			s.pass.err = errors.New("bad key length in " + path)
			return
		}
		block, err := aes.NewCipher(key)
		if err == nil {
			s.pass.aead, err = cipher.NewGCM(block)
		}
		s.pass.err = err
	})
	return s.pass.aead, s.pass.err
}

// sealPass encrypts a web password for viewing ("" if it cannot).
func (s *Server) sealPass(pw string) string {
	a, err := s.passAEAD()
	if err != nil || pw == "" {
		return ""
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	return "v1:" + base64.StdEncoding.EncodeToString(a.Seal(nonce, nonce, []byte(pw), nil))
}

// openPass decrypts sealPass's output ("" if it cannot).
func (s *Server) openPass(enc string) string {
	data, ok := strings.CutPrefix(enc, "v1:")
	if !ok {
		return ""
	}
	a, err := s.passAEAD()
	if err != nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(raw) < a.NonceSize() {
		return ""
	}
	pt, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], nil)
	if err != nil {
		return ""
	}
	return string(pt)
}

// keepViewable stores the viewable copy of a password that was just set
// or proved at sign-in (older accounts become viewable that way).
func (s *Server) keepViewable(ctx context.Context, extUUID, pw string) {
	if s.store == nil || !s.store.HasColumn("rq_web_users", "web_password_enc") {
		return
	}
	if enc := s.sealPass(pw); enc != "" {
		_, _ = s.store.Pool().Exec(ctx, `update rq_web_users set web_password_enc = $2 where extension_uuid = $1::uuid`, extUUID, enc)
	}
}
