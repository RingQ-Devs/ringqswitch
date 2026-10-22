package web

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SIP profiles (Global settings → SIP Profile): the RingQ portal's v_sip_profiles
// with their main settings. RingQswitch opens each enabled profile's port
// (the main port of ringqswitch.xml is always open).
//
//	GET    /api/sipprofiles         the profiles and whether their ports are open
//	POST   /api/sipprofiles         {name, description, enabled, sip_ip, port, protocol, ext_sip_ip}
//	PUT    /api/sipprofiles/{uuid}
//	DELETE /api/sipprofiles/{uuid}

var reProfileName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type sipProfileBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
	SIPIP       string `json:"sip_ip"`
	Port        any    `json:"port"`
	Protocol    string `json:"protocol"`
	ExtSIPIP    string `json:"ext_sip_ip"`
}

// saveSIPProfile checks and stores a profile (id "" = a new one).
func (s *Server) saveSIPProfile(ctx context.Context, id string, b sipProfileBody) (string, error) {
	b.Name = strings.TrimSpace(b.Name)
	if !reProfileName.MatchString(b.Name) {
		return "", badRequest("profile name: letters, digits, - and _ (e.g. external)")
	}
	port := atoi(toStr(b.Port), 0)
	if port < 1024 || port > 65535 {
		return "", badRequest("port: 1024 to 65535 (5060, 5080...)")
	}
	if err := s.portFree(port); err != nil {
		return "", err
	}
	for _, ip := range []*string{&b.SIPIP, &b.ExtSIPIP} {
		*ip = strings.TrimSpace(*ip)
		if *ip != "" && !strings.EqualFold(*ip, "auto") && net.ParseIP(*ip) == nil {
			return "", badRequest("%q is not an IP address (leave it empty for this server's address)", *ip)
		}
	}
	if b.Protocol != "udp" {
		b.Protocol = "udp,tcp"
	}
	on := b.Enabled == nil || *b.Enabled
	if err := s.ensureProfileTables(ctx); err != nil {
		return "", err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var dup string
	_ = tx.QueryRow(ctx, `select sip_profile_uuid::text from v_sip_profiles where lower(sip_profile_name) = lower($1) and sip_profile_uuid::text <> $2`,
		b.Name, id).Scan(&dup)
	if dup != "" {
		return "", badRequest("a profile called %s exists already", b.Name)
	}
	if id == "" {
		id = newUUID()
		if _, err := tx.Exec(ctx, `insert into v_sip_profiles (sip_profile_uuid, sip_profile_name, sip_profile_hostname, sip_profile_enabled,
			sip_profile_description, insert_date) values ($1, $2, null, $3, $4, now())`, id, b.Name, strconv.FormatBool(on), b.Description); err != nil {
			return "", err
		}
	} else {
		tag, err := tx.Exec(ctx, `update v_sip_profiles set sip_profile_name = $2, sip_profile_enabled = $3, sip_profile_description = $4,
			update_date = now() where sip_profile_uuid = $1::uuid`, id, b.Name, strconv.FormatBool(on), b.Description)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() == 0 {
			return "", notFound("SIP profile")
		}
	}
	orAuto := func(v, auto string) string {
		if v == "" || strings.EqualFold(v, "auto") {
			return auto
		}
		return v
	}
	bind := ""
	if b.Protocol == "udp" {
		bind = "transport=udp"
	}
	settings := map[string]string{"sip-ip": orAuto(b.SIPIP, "$${local_ip_v4}"), "sip-port": strconv.Itoa(port),
		"ext-sip-ip": orAuto(b.ExtSIPIP, "$${external_sip_ip}"), "ext-rtp-ip": orAuto(b.ExtSIPIP, "$${external_rtp_ip}"), "bind-params": bind}
	for name, value := range settings {
		if err := setProfileSetting(ctx, tx, id, name, value); err != nil {
			return "", err
		}
	}
	return id, tx.Commit(ctx)
}

func (s *Server) sipProfilesAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) error {
	switch {
	case id == "" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, p := range s.sw.SIPProfiles() {
			out = append(out, map[string]any{"uuid": p.UUID, "name": p.Name, "description": p.Description, "enabled": p.Enabled,
				"sip_ip": p.IP, "port": p.Port, "protocol": map[bool]string{true: "udp,tcp", false: "udp"}[p.TCP && s.sw.Config().Settings.EnableTCP],
				"ext_sip_ip": p.ExtSIPIP, "tls": p.TLS, "main": p.Main, "listening": p.Listening, "bind_ip": p.BindIP,
				"advertise": p.Advertise, "error": p.Error, "from_config": p.FromConfig,
				"ip_here": p.IP == "" || localAddress(p.IP)})
		}
		inDB := 0
		if s.store != nil && s.store.HasTable("v_sip_profiles") {
			_ = s.store.Pool().QueryRow(ctx, `select count(*) from v_sip_profiles`).Scan(&inDB)
		}
		writeJSON(w, map[string]any{"profiles": out, "database": s.store != nil, "in_database": inDB})
		return nil
	case id == "defaults" && r.Method == http.MethodPost:
		// RingQ's standard profiles: internal on the main port, external on 5080.
		if err := s.needDB(); err != nil {
			return err
		}
		if err := s.ensureProfileTables(ctx); err != nil {
			return err
		}
		added := []string{}
		for _, p := range []sipProfileBody{
			{Name: "internal", Description: "Phones and the browser dialer", Port: s.sw.SIPPort()},
			{Name: "external", Description: "Carriers (SIP trunks)", Port: 5080},
		} {
			var n int
			if err := s.store.Pool().QueryRow(ctx, `select count(*) from v_sip_profiles where lower(sip_profile_name) = $1`, p.Name).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			if _, err := s.saveSIPProfile(ctx, "", p); err != nil {
				return err
			}
			added = append(added, p.Name)
		}
		s.reload()
		writeJSON(w, map[string]any{"added": added})
		return nil
	case (id == "" && r.Method == http.MethodPost) || (reUUID.MatchString(id) && r.Method == http.MethodPut):
		if err := s.needDB(); err != nil {
			return err
		}
		var b sipProfileBody
		if err := decodeBody(r, &b); err != nil {
			return err
		}
		uuid, err := s.saveSIPProfile(ctx, id, b)
		if err != nil {
			return err
		}
		s.reload()
		writeJSON(w, map[string]string{"uuid": uuid})
		return nil
	case reUUID.MatchString(id) && r.Method == http.MethodDelete:
		if err := s.needDB(); err != nil {
			return err
		}
		tx, err := s.store.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx, `delete from v_sip_profile_settings where sip_profile_uuid = $1::uuid`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from v_sip_profiles where sip_profile_uuid = $1::uuid`, id); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		s.reload()
		writeJSON(w, map[string]string{"ok": "deleted"})
		return nil
	}
	return errMethod
}

// setProfileSetting writes one v_sip_profile_settings row (enabled).
func setProfileSetting(ctx context.Context, tx pgx.Tx, profile, name, value string) error {
	tag, err := tx.Exec(ctx, `update v_sip_profile_settings set sip_profile_setting_value = $3, sip_profile_setting_enabled = 'true', update_date = now()
		where sip_profile_uuid = $1::uuid and sip_profile_setting_name = $2`, profile, name, value)
	if err != nil || tag.RowsAffected() > 0 {
		return err
	}
	if value == "" {
		return nil
	}
	_, err = tx.Exec(ctx, `insert into v_sip_profile_settings (sip_profile_setting_uuid, sip_profile_uuid, sip_profile_setting_name,
		sip_profile_setting_value, sip_profile_setting_enabled, insert_date) values ($1, $2, $3, $4, 'true', now())`, newUUID(), profile, name, value)
	return err
}

// ensureProfileTables creates the RingQ portal's SIP profile tables in a database
// that has none (a new RingQswitch database).
func (s *Server) ensureProfileTables(ctx context.Context) error {
	if s.store.HasTable("v_sip_profiles") && s.store.HasTable("v_sip_profile_settings") {
		return nil
	}
	if _, err := s.store.CreateMissingTables(ctx); err != nil {
		return err
	}
	if !s.store.HasTable("v_sip_profiles") {
		return badRequest("the database has no v_sip_profiles table")
	}
	return nil
}

// portFree refuses the ports the server already uses for something else.
func (s *Server) portFree(port int) error {
	st := s.sw.Config().Settings
	switch {
	case port >= st.RTPStartPort && port <= st.RTPEndPort:
		return badRequest("port %d is in the audio (RTP) range %d-%d", port, st.RTPStartPort, st.RTPEndPort)
	case s.cfg.WebRTCPort > 0 && port == s.cfg.WebRTCPort:
		return badRequest("port %d is the browser audio port (webrtc-port)", port)
	}
	if a, ok := s.addr.(*net.TCPAddr); ok && a.Port == port {
		return badRequest("port %d is the web interface's", port)
	}
	return nil
}

func localAddress(ip string) bool {
	want := net.ParseIP(ip)
	if want == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(want) {
			return true
		}
	}
	return false
}
