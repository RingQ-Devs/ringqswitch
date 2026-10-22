package web

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Who may use the web interface:
//
//   - the administrator from ringqswitch.xml (admin-user / admin-password):
//     everything, every domain;
//   - extensions with web access (rq_web_users), signing in with their
//     extension number (1001, or 1001@domain when the number exists in
//     several domains) and their web password, which is separate from the
//     SIP password their phone uses:
//   - Owner: everything in their domain (and the server pages —
//     Security, System, Domains — when owner-server-access is on);
//   - Manager: a supervisor: all monitoring (listen, whisper, barge,
//     divert, hang up), every agent's status and all reports, but no
//     settings;
//   - User: Monitor, Reports (their own calls), Dialer, Chat, Meetings.
const (
	RoleAdmin   = "admin"
	RoleOwner   = "owner"
	RoleManager = "manager"
	RoleUser    = "user"
)

type principal struct {
	Kind          string `json:"kind"` // admin, extension
	User          string `json:"user"` // what was typed to sign in
	Name          string `json:"name"`
	Role          string `json:"role"`
	DomainUUID    string `json:"domain_uuid,omitempty"`
	Domain        string `json:"domain,omitempty"`
	Extension     string `json:"extension,omitempty"`
	ExtensionUUID string `json:"extension_uuid,omitempty"`
	Email         string `json:"email,omitempty"` // meeting guests, when given
}

func (p *principal) admin() bool { return p.Role == RoleAdmin }
func (p *principal) owner() bool { return p.Role == RoleAdmin || p.Role == RoleOwner }

// supervisor: monitoring, every agent and all reports (owners and managers).
func (p *principal) supervisor() bool { return p.owner() || p.Role == RoleManager }

// key identifies the principal for presence and chat ("1001@example.com").
func (p *principal) key() string {
	if p.Kind == "extension" {
		return p.Extension + "@" + p.Domain
	}
	return p.User
}

func (p *principal) canDomain(name string) bool {
	return p.admin() || strings.EqualFold(p.Domain, name)
}

// serverAccess: Security, System, Domains and the event log are about the
// whole server.
func (s *Server) serverAccess(p *principal) bool {
	return p.admin() || (p.Role == RoleOwner && s.cfg.OwnerServerAccess)
}

var errForbidden = &apiError{http.StatusForbidden, "your account cannot do this"}

// ---------------------------------------------------------------------------
// Passwords: PBKDF2-HMAC-SHA256, "pbkdf2-sha256$<iterations>$<salt>$<hash>".

const pbkdf2Iterations = 600000

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key, _ := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iterations, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

func verifyPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 || iter > 10000000 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func checkPasswordStrength(pw string) error {
	if len(pw) < 8 {
		return badRequest("web password: at least 8 characters")
	}
	if len(pw) > 200 {
		return badRequest("web password: too long")
	}
	return nil
}

// dummyHash is verified when the user does not exist, so a wrong user name
// takes as long as a wrong password.
var dummyHash = hashPassword("not-a-real-password")

// authenticate checks a sign-in. It returns nil for wrong credentials.
func (s *Server) authenticate(ctx context.Context, user, pw string) (*principal, error) {
	user = strings.TrimSpace(user)
	if user == "" || pw == "" {
		return nil, nil
	}
	okUser := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) == 1
	okPass := subtle.ConstantTimeCompare([]byte(pw), []byte(s.cfg.AdminPassword)) == 1
	if okUser {
		if !okPass {
			return nil, nil
		}
		return &principal{Kind: "admin", User: user, Name: "Administrator", Role: RoleAdmin}, nil
	}
	if s.store == nil || !s.store.HasTable("rq_web_users") {
		verifyPassword(dummyHash, pw)
		return nil, nil
	}
	ext, domain, _ := strings.Cut(user, "@")
	if !reNumber.MatchString(ext) {
		verifyPassword(dummyHash, pw)
		return nil, nil
	}
	rows, err := s.store.Pool().Query(ctx, `select w.extension_uuid::text, w.role, coalesce(w.web_password, ''), e.extension,
		coalesce(nullif(trim(coalesce(e.directory_first_name, '') || ' ' || coalesce(e.directory_last_name, '')), ''), e.effective_caller_id_name, ''),
		d.domain_uuid::text, d.domain_name
		from rq_web_users w
		join v_extensions e on e.extension_uuid = w.extension_uuid
		join v_domains d on d.domain_uuid = e.domain_uuid
		where w.enabled and e.extension = $1 and ($2 = '' or lower(d.domain_name) = lower($2))
		and coalesce(e.enabled::text, 'true') in ('true', 't') and coalesce(d.domain_enabled::text, 'true') in ('true', 't')`, ext, domain)
	if err != nil {
		return nil, err
	}
	type cand struct {
		p    principal
		hash string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		c.p.Kind = "extension"
		if err := rows.Scan(&c.p.ExtensionUUID, &c.p.Role, &c.hash, &c.p.Extension, &c.p.Name, &c.p.DomainUUID, &c.p.Domain); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if len(cands) != 1 {
		verifyPassword(dummyHash, pw)
		if len(cands) > 1 {
			return nil, badRequest("extension %s exists in several domains: sign in as %s@<domain>", ext, ext)
		}
		return nil, nil
	}
	c := cands[0]
	if !verifyPassword(c.hash, pw) {
		return nil, nil
	}
	if !s.passwordSignIn(ctx, c.p.DomainUUID) {
		return nil, badRequest("%s signs in with single sign-on: use the Microsoft, Google or LinkedIn button", c.p.Domain)
	}
	if c.p.Role != RoleOwner && c.p.Role != RoleManager {
		c.p.Role = RoleUser
	}
	c.p.User = user
	c.p.Name = firstNonEmpty(c.p.Name, c.p.Extension)
	_, _ = s.store.Pool().Exec(ctx, `update rq_web_users set last_login = now() where extension_uuid = $1::uuid`, c.p.ExtensionUUID)
	s.keepViewable(ctx, c.p.ExtensionUUID, pw)
	return &c.p, nil
}

// changePassword lets a signed-in extension change its own web password.
func (s *Server) changePassword(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal) error {
	if r.Method != http.MethodPost {
		return errMethod
	}
	if p.Kind != "extension" {
		return badRequest("the administrator password is set in ringqswitch.xml (admin-password)")
	}
	var body struct{ Current, New string }
	if err := decodeBody(r, &body); err != nil {
		return err
	}
	if err := checkPasswordStrength(body.New); err != nil {
		return err
	}
	var hash string
	err := s.store.Pool().QueryRow(ctx, `select coalesce(web_password, '') from rq_web_users where extension_uuid = $1::uuid`, p.ExtensionUUID).Scan(&hash)
	if err == pgx.ErrNoRows || (err == nil && !verifyPassword(hash, body.Current)) {
		time.Sleep(500 * time.Millisecond)
		return badRequest("the current password is wrong")
	}
	if err != nil {
		return err
	}
	if _, err := s.store.Pool().Exec(ctx, `update rq_web_users set web_password = $2, update_date = now() where extension_uuid = $1::uuid`,
		p.ExtensionUUID, hashPassword(body.New)); err != nil {
		return err
	}
	s.keepViewable(ctx, p.ExtensionUUID, body.New)
	s.log.Info("web: password changed", "user", p.key())
	writeJSON(w, map[string]string{"ok": "password changed"})
	return nil
}
