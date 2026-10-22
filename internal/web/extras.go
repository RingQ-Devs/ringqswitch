package web

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Cal4Care-Developers/RingQswitch/internal/db"
)

// Settings that belong to an extension but live in other tables: its
// call-centre agent (v_call_center_agents) and its web portal access
// (rq_web_users). The extension form shows and saves them together.

var agentFields = map[string]string{ // extension form field -> v_call_center_agents column
	"agent_call_timeout":         "20",
	"agent_wrap_up_time":         "10",
	"agent_max_no_answer":        "3",
	"agent_no_answer_delay_time": "30",
}

func agentContact(ext string, dom domainRow) string { return "user/" + ext + "@" + dom.name }

// ensureAgent returns the agent of an extension, creating it (with the
// default settings, logged out) when the extension is not an agent yet.
// settings, when given, are applied to the agent.
func (s *Server) ensureAgent(ctx context.Context, tx pgx.Tx, dom domainRow, ext, agentUUID string, settings map[string]string) (string, string, error) {
	if !s.store.HasTable("v_call_center_agents") {
		return "", "", badRequest("the database has no v_call_center_agents table")
	}
	var id, name string
	if ext == "" && reUUID.MatchString(agentUUID) {
		err := tx.QueryRow(ctx, `select call_center_agent_uuid::text, coalesce(agent_name, '') from v_call_center_agents
			where call_center_agent_uuid = $1::uuid and domain_uuid = $2::uuid`, agentUUID, dom.uuid).Scan(&id, &name)
		if err != nil {
			return "", "", badRequest("queue member: unknown agent")
		}
		return id, name, nil
	}
	if !reNumber.MatchString(ext) {
		return "", "", badRequest("queue member: choose an extension")
	}
	err := tx.QueryRow(ctx, `select call_center_agent_uuid::text, coalesce(agent_name, '') from v_call_center_agents
		where domain_uuid = $1::uuid and agent_contact = $2 order by insert_date limit 1`, dom.uuid, agentContact(ext, dom)).Scan(&id, &name)
	switch {
	case err == nil:
		if len(settings) > 0 {
			if err := updateRow(ctx, tx, s.store, "v_call_center_agents", "call_center_agent_uuid", id, settings); err != nil {
				return "", "", err
			}
		}
		return id, name, nil
	case err != pgx.ErrNoRows:
		return "", "", err
	}
	var cid string
	if err := tx.QueryRow(ctx, `select coalesce(nullif(effective_caller_id_name, ''), extension) from v_extensions
		where domain_uuid = $1::uuid and extension = $2`, dom.uuid, ext).Scan(&cid); err != nil {
		return "", "", badRequest("extension %s does not exist", ext)
	}
	id, name = newUUID(), cid
	if name == ext {
		name = "Agent " + ext
	}
	row := map[string]string{"call_center_agent_uuid": id, "domain_uuid": dom.uuid, "agent_name": name, "agent_type": "callback",
		"agent_id": ext, "agent_contact": agentContact(ext, dom), "agent_status": "Logged Out", "agent_reject_delay_time": "10",
		"agent_busy_delay_time": "60", "agent_record": "false", "insert_date": time.Now().Format(time.RFC3339)}
	for k, def := range agentFields {
		row[k] = def
	}
	for k, v := range settings {
		row[k] = v
	}
	return id, name, insertRow(ctx, tx, s.store, "v_call_center_agents", row)
}

// decorateExtensions adds the agent and web access fields to extension rows.
func (s *Server) decorateExtensions(ctx context.Context, dom domainRow, rows []map[string]string) {
	agents := map[string]map[string]string{}
	if s.store.HasTable("v_call_center_agents") {
		if list, err := s.store.JSONRows(ctx, `select to_jsonb(a) from v_call_center_agents a where domain_uuid = $1::uuid`, dom.uuid); err == nil {
			for _, a := range list {
				if ext := extFromContact(a["agent_contact"]); ext != "" {
					agents[ext] = a
				}
			}
		}
	}
	web := map[string]map[string]string{}
	if s.store.HasTable("rq_web_users") {
		enc, sso := "''", "''"
		if s.store.HasColumn("rq_web_users", "web_password_enc") {
			enc = "coalesce(web_password_enc, '')"
		}
		if s.store.HasColumn("rq_web_users", "sso_email") {
			sso = "coalesce(sso_email, '')"
		}
		if list, err := s.store.JSONRows(ctx, `select jsonb_build_object('id', extension_uuid, 'role', role, 'enabled', enabled,
			'set', coalesce(web_password, '') <> '', 'enc', `+enc+`, 'sso', `+sso+`, 'last_login', last_login) from rq_web_users where domain_uuid = $1::uuid`, dom.uuid); err == nil {
			for _, w := range list {
				web[w["id"]] = w
			}
		}
	}
	for _, r := range rows {
		a := agents[r["extension"]]
		r["agent_enabled"] = strconv.FormatBool(a != nil)
		for k, def := range agentFields {
			if a != nil && a[k] != "" {
				r[k] = a[k]
			} else {
				r[k] = def
			}
		}
		if a != nil {
			r["agent_uuid"] = a["call_center_agent_uuid"]
		}
		r["web_role"], r["web_password_set"], r["web_last_login"], r["web_password"], r["sso_email"] = "", "false", "", "", ""
		if w := web[r["extension_uuid"]]; w != nil && truthy(firstNonEmpty(w["enabled"], "true")) {
			r["web_role"], r["web_password_set"], r["web_last_login"], r["sso_email"] = w["role"], w["set"], w["last_login"], w["sso"]
			r["web_password"] = s.openPass(w["enc"]) // shown masked, with the eye
		}
	}
}

// saveExtensionExtras saves the agent and web access fields given in raw.
func (s *Server) saveExtensionExtras(ctx context.Context, tx pgx.Tx, dom domainRow, row, old map[string]string, raw map[string]any) error {
	if old != nil && old["extension"] != row["extension"] && s.store.HasTable("v_call_center_agents") {
		if _, err := tx.Exec(ctx, `update v_call_center_agents set agent_contact = $3, agent_id = $4
			where domain_uuid = $1::uuid and agent_contact = $2`, dom.uuid, agentContact(old["extension"], dom), agentContact(row["extension"], dom), row["extension"]); err != nil {
			return err
		}
	}
	if v, given := raw["agent_enabled"]; given && s.store.HasTable("v_call_center_agents") {
		if truthy(toStr(v)) {
			settings := map[string]string{}
			for k := range agentFields {
				if x, ok := raw[k]; ok {
					n := atoi(toStr(x), -1)
					if n < 0 {
						return badRequest("%s: a number", k)
					}
					settings[k] = strconv.Itoa(n)
				}
			}
			if _, _, err := s.ensureAgent(ctx, tx, dom, row["extension"], "", settings); err != nil {
				return err
			}
		} else if err := s.deleteAgent(ctx, tx, dom, row["extension"]); err != nil {
			return err
		}
	}
	return s.saveWebAccess(ctx, tx, dom, row, raw)
}

func (s *Server) deleteAgent(ctx context.Context, tx pgx.Tx, dom domainRow, ext string) error {
	if !s.store.HasTable("v_call_center_agents") {
		return nil
	}
	if s.store.HasTable("v_call_center_tiers") {
		if _, err := tx.Exec(ctx, `delete from v_call_center_tiers where call_center_agent_uuid in
			(select call_center_agent_uuid from v_call_center_agents where domain_uuid = $1::uuid and agent_contact = $2)`, dom.uuid, agentContact(ext, dom)); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `delete from v_call_center_agents where domain_uuid = $1::uuid and agent_contact = $2`, dom.uuid, agentContact(ext, dom))
	return err
}

// deleteExtensionExtras removes the agent and web access of a deleted extension.
func (s *Server) deleteExtensionExtras(ctx context.Context, tx pgx.Tx, dom domainRow, row map[string]string) error {
	if err := s.deleteAgent(ctx, tx, dom, row["extension"]); err != nil {
		return err
	}
	if s.store.HasTable("rq_web_users") {
		if _, err := tx.Exec(ctx, `delete from rq_web_users where extension_uuid = $1::uuid`, row["extension_uuid"]); err != nil {
			return err
		}
	}
	return nil
}

// saveWebAccess stores the web portal role and password of an extension.
// web_role "" removes the access; an empty web_password keeps the current one.
func (s *Server) saveWebAccess(ctx context.Context, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any) error {
	roleV, roleGiven := raw["web_role"]
	pwV, pwGiven := raw["web_password"]
	ssoV, ssoGiven := raw["sso_email"]
	ssoGiven = ssoGiven && s.store.HasColumn("rq_web_users", "sso_email")
	if (!roleGiven && !pwGiven && !ssoGiven) || !s.store.HasTable("rq_web_users") {
		return nil
	}
	role, pw := toStr(roleV), toStr(pwV)
	id := row["extension_uuid"]
	var cur string
	var hasPassword bool
	err := tx.QueryRow(ctx, `select role, coalesce(web_password, '') <> '' from rq_web_users where extension_uuid = $1::uuid`, id).Scan(&cur, &hasPassword)
	exists := err == nil
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if !roleGiven {
		role = cur
	}
	ssoEmail := ""
	if ssoGiven {
		var err error
		if ssoEmail, err = checkSSOEmail(ctx, tx, dom, id, toStr(ssoV)); err != nil {
			return err
		}
	}
	switch role {
	case "":
		if exists {
			_, err := tx.Exec(ctx, `delete from rq_web_users where extension_uuid = $1::uuid`, id)
			return err
		}
		return nil
	case RoleUser, RoleManager, RoleOwner:
	default:
		return badRequest("web access: choose User, Manager or Owner")
	}
	if pw != "" {
		if err := checkPasswordStrength(pw); err != nil {
			return err
		}
	} else if !hasPassword {
		return badRequest("web access: set a web password (at least 8 characters)")
	}
	now := time.Now().Format(time.RFC3339)
	if !exists {
		return insertRow(ctx, tx, s.store, "rq_web_users", map[string]string{"extension_uuid": id, "domain_uuid": dom.uuid, "role": role,
			"web_password": hashPassword(pw), "web_password_enc": s.sealPass(pw), "sso_email": ssoEmail, "enabled": "true", "insert_date": now, "update_date": now})
	}
	upd := map[string]string{"role": role, "enabled": "true", "update_date": now}
	if ssoGiven {
		upd["sso_email"] = ssoEmail
	}
	if pw != "" {
		upd["web_password"] = hashPassword(pw)
		upd["web_password_enc"] = s.sealPass(pw)
	}
	return updateRow(ctx, tx, s.store, "rq_web_users", "extension_uuid", id, upd)
}

// SetWebAccess gives an extension web access with a role and password
// (ringqswitch -web-access 1000@domain, for scripts).
func SetWebAccess(ctx context.Context, st *db.Store, ext, domain, role, password string) error {
	if role != RoleOwner && role != RoleManager && role != RoleUser {
		return fmt.Errorf("role: owner, manager or user")
	}
	if err := checkPasswordStrength(password); err != nil {
		return err
	}
	if err := st.EnsureSchema(ctx); err != nil {
		return err
	}
	var extUUID, domUUID string
	if err := st.Pool().QueryRow(ctx, `select e.extension_uuid::text, d.domain_uuid::text from v_extensions e join v_domains d on d.domain_uuid = e.domain_uuid
		where e.extension = $1 and lower(d.domain_name) = lower($2)`, ext, domain).Scan(&extUUID, &domUUID); err != nil {
		return fmt.Errorf("extension %s@%s not found", ext, domain)
	}
	_, err := st.Pool().Exec(ctx, `insert into rq_web_users (extension_uuid, domain_uuid, role, web_password, enabled, insert_date, update_date)
		values ($1, $2, $3, $4, true, now(), now())
		on conflict (extension_uuid) do update set role = excluded.role, web_password = excluded.web_password, enabled = true, update_date = now()`,
		extUUID, domUUID, role, hashPassword(password))
	if err == nil && st.HasColumn("rq_web_users", "web_password_enc") {
		// The viewable copy is made again at the next sign-in.
		_, err = st.Pool().Exec(ctx, `update rq_web_users set web_password_enc = null where extension_uuid = $1::uuid`, extUUID)
	}
	return err
}
