package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// rolesAPI: the User roles page (owners).
//
//	GET /api/d/{domain}/roles               every extension with its web role ("" = no portal access)
//	PUT /api/d/{domain}/roles/{extension_uuid} {"role": "owner|manager|user|", "password": "..."}
//	    a password is needed when the extension has none yet
func (s *Server) rolesAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	if !s.store.HasTable("rq_web_users") {
		return badRequest("the database has no rq_web_users table (restart RingQswitch to create it)")
	}
	id := part(parts, 0)
	switch {
	case id == "" && r.Method == http.MethodGet:
		names := s.extensionNames(ctx, dom.uuid)
		online := map[string]bool{}
		for _, b := range s.sw.Registrar().All() {
			if strings.EqualFold(b.Domain, dom.name) {
				online[b.User] = true
			}
		}
		rows, err := s.store.Pool().Query(ctx, `select e.extension_uuid::text, e.extension,
			case when w.extension_uuid is null or not coalesce(w.enabled, true) then '' else w.role end,
			coalesce(w.web_password, '') <> '', coalesce(to_char(w.last_login, 'YYYY-MM-DD"T"HH24:MI:SSOF'), '')
			from v_extensions e left join rq_web_users w on w.extension_uuid = e.extension_uuid
			where e.domain_uuid = $1::uuid`, dom.uuid)
		if err != nil {
			return err
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var uuid, ext, role, last string
			var set bool
			if err := rows.Scan(&uuid, &ext, &role, &set, &last); err != nil {
				return err
			}
			if role != "" && role != RoleOwner && role != RoleManager {
				role = RoleUser
			}
			out = append(out, map[string]any{"extension_uuid": uuid, "extension": ext, "name": firstNonEmpty(names[ext], "Ext "+ext),
				"role": role, "password_set": set, "last_login": last, "registered": online[ext], "you": uuid == p.ExtensionUUID})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		sort.Slice(out, func(i, j int) bool { return extLess(out[i]["extension"].(string), out[j]["extension"].(string)) })
		writeJSON(w, out)
		return nil
	case reUUID.MatchString(id) && r.Method == http.MethodPut:
		var body struct{ Role, Password string }
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		if id == p.ExtensionUUID {
			return badRequest("you cannot change your own role (ask another owner)")
		}
		var ext string
		if err := s.store.Pool().QueryRow(ctx, `select extension from v_extensions where extension_uuid = $1::uuid and domain_uuid = $2::uuid`,
			id, dom.uuid).Scan(&ext); err != nil {
			return notFound("extension")
		}
		tx, err := s.store.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		row := map[string]string{"extension_uuid": id, "extension": ext}
		if err := s.saveWebAccess(ctx, tx, dom, row, map[string]any{"web_role": body.Role, "web_password": body.Password}); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		// Signed-in sessions keep the role they started with: sign them out.
		s.dropSessions(func(x *principal) bool { return x.ExtensionUUID == id })
		label := map[string]string{"": "no portal access", RoleUser: "User", RoleManager: "Manager", RoleOwner: "Owner"}[body.Role]
		s.sw.RecordEvent("security", fmt.Sprintf("%s set the web role of %s@%s to %s", p.key(), ext, dom.name, label))
		writeJSON(w, map[string]string{"ok": "saved", "role": body.Role})
		return nil
	}
	return errMethod
}

// extLess sorts extension numbers numerically when they are numbers.
func extLess(a, b string) bool {
	if len(a) != len(b) && reNumber.MatchString(a) && reNumber.MatchString(b) {
		return len(a) < len(b)
	}
	return a < b
}
