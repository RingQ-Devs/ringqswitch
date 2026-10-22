package web

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
)

// Phone keys: the busy lamp field (BLF) rows of an extension, as RingQ keeps
// them (q_blfs: one row set per extension, types and values as lists), and
// the company's keys shown in every dialer (q_company_blfs). Desk phones get
// them from RingQ's provisioning: row 1 is key 2, row 2 key 3... The
// browser dialer shows them with their lamps.

// The key types (RingQ's names are read too).
const (
	keyBLF          = "blf"
	keySpeedDial    = "speed_dial"
	keyPark         = "park"
	keyAgentStatus  = "agent_status"
	keyChangeStatus = "change_status"
	keyBlank        = "blank"
	parkSlots       = 5
	maxPhoneKeys    = 60
)

var reParkSlot = regexp.MustCompile(`([1-5])\s*$`)

// keyType reads a key type as RingQ or RingQswitch writes it.
func keyType(v string) string {
	switch k := strings.ToLower(strings.TrimSpace(v)); {
	case k == "" || k == keyBlank || k == "none" || k == "empty":
		return keyBlank
	case strings.Contains(k, "speed"):
		return keySpeedDial
	case strings.Contains(k, "park"):
		return keyPark
	case strings.Contains(k, "agent"):
		return keyAgentStatus
	case strings.Contains(k, "change"):
		return keyChangeStatus
	case k == keyBLF || strings.Contains(k, "blf") || strings.Contains(k, "extension") || k == "ext":
		return keyBLF
	}
	return keyBlank
}

type phoneKey struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// parseList reads a JSON list of strings, or a comma separated list.
func parseList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(v), &out) == nil {
		return out
	}
	var any []any
	if json.Unmarshal([]byte(v), &any) == nil {
		for _, x := range any {
			out = append(out, toStr(x))
		}
		return out
	}
	for _, p := range strings.Split(v, ",") {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func parseKeys(types, values string) []phoneKey {
	ts, vs := parseList(types), parseList(values)
	out := []phoneKey{}
	for i, t := range ts {
		k := phoneKey{Type: keyType(t)}
		if i < len(vs) {
			k.Value = strings.TrimSpace(vs[i])
		}
		if k.Type == keyPark {
			if m := reParkSlot.FindStringSubmatch(k.Value); m != nil {
				k.Value = m[1]
			}
		}
		out = append(out, k)
	}
	return out
}

// cleanKeys checks the keys from the extension page.
func cleanKeys(in []phoneKey) ([]phoneKey, error) {
	if len(in) > maxPhoneKeys {
		return nil, badRequest("phone keys: %d at most", maxPhoneKeys)
	}
	out := make([]phoneKey, 0, len(in))
	for i, k := range in {
		k.Type, k.Value = keyType(k.Type), strings.TrimSpace(k.Value)
		switch k.Type {
		case keyBLF, keySpeedDial:
			if !reDID.MatchString(strings.TrimPrefix(k.Value, "*")) && !reNumber.MatchString(k.Value) {
				return nil, badRequest("key %d: enter a number", i+1)
			}
		case keyPark:
			n, err := strconv.Atoi(strings.TrimSpace(reParkSlot.FindString(k.Value)))
			if err != nil || n < 1 || n > parkSlots {
				return nil, badRequest("key %d: parking slot 1 to %d", i+1, parkSlots)
			}
			k.Value = strconv.Itoa(n)
		case keyAgentStatus:
			if k.Value == "" {
				return nil, badRequest("key %d: choose the agent status", i+1)
			}
		case keyChangeStatus:
			k.Value = strings.ToUpper(k.Value)
			if k.Value != "IN" && k.Value != "OUT" {
				return nil, badRequest("key %d: IN or OUT", i+1)
			}
		default:
			k.Value = ""
		}
		out = append(out, k)
	}
	return out, nil
}

func keysJSON(keys []phoneKey) (string, string) {
	ts, vs := make([]string, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		ts[i], vs[i] = k.Type, k.Value
	}
	a, _ := json.Marshal(ts)
	b, _ := json.Marshal(vs)
	return string(a), string(b)
}

// extensionKeys reads an extension's keys.
func (s *Server) extensionKeys(ctx context.Context, dom domainRow, ext string) []phoneKey {
	if !s.store.HasTable("q_blfs") {
		return []phoneKey{}
	}
	var types, values string
	if err := s.store.Pool().QueryRow(ctx, `select coalesce(types, ''), coalesce("values", '') from q_blfs where domain_uuid = $1::uuid
		and extension = $2 order by update_date desc nulls last limit 1`, dom.uuid, ext).Scan(&types, &values); err != nil {
		return []phoneKey{}
	}
	return parseKeys(types, values)
}

// companyKeys reads the domain's keys shown in every dialer.
func (s *Server) companyKeys(ctx context.Context, dom domainRow) ([]phoneKey, bool) {
	if !s.store.HasTable("q_company_blfs") {
		return []phoneKey{}, true
	}
	var types, values string
	var show int
	if err := s.store.Pool().QueryRow(ctx, `select coalesce(types, ''), coalesce("values", ''), show_dialer from q_company_blfs
		where domain_uuid = $1::uuid order by id limit 1`, dom.uuid).Scan(&types, &values, &show); err != nil {
		return []phoneKey{}, true
	}
	return parseKeys(types, values), show != 0
}

// keysUser is the user row q_blfs is keyed on: RingQ's user of the
// extension (v_extension_users), else the extension itself.
func (s *Server) keysUser(ctx context.Context, dom domainRow, extUUID string) string {
	if s.store.HasTable("v_extension_users") {
		var u string
		if err := s.store.Pool().QueryRow(ctx, `select user_uuid::text from v_extension_users where extension_uuid = $1::uuid
			and user_uuid is not null limit 1`, extUUID).Scan(&u); err == nil && u != "" {
			return u
		}
	}
	return extUUID
}

// phoneKeysAPI: GET/PUT /api/d/{domain}/phonekeys/{extension} (owners),
// GET/PUT /api/d/{domain}/phonekeys (company keys), POST
// .../phonekeys/{extension}/reboot.
func (s *Server) phoneKeysAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	if !s.store.HasTable("q_blfs") {
		return badRequest("the database has no q_blfs table")
	}
	ext := part(parts, 0)
	if ext == "" {
		return s.companyKeysAPI(ctx, w, r, p, dom)
	}
	var extUUID string
	if err := s.store.Pool().QueryRow(ctx, `select extension_uuid::text from v_extensions where domain_uuid = $1::uuid and extension = $2`,
		dom.uuid, ext).Scan(&extUUID); err != nil {
		return notFound("extension")
	}
	if part(parts, 1) == "reboot" {
		if r.Method != http.MethodPost {
			return errMethod
		}
		n := s.sw.CheckSync(ext, dom.name, true)
		s.log.Info("web: phone reboot", "extension", ext, "domain", dom.name, "phones", n, "user", p.key())
		writeJSON(w, map[string]int{"phones": n})
		return nil
	}
	switch r.Method {
	case http.MethodGet:
		company, show := s.companyKeys(ctx, dom)
		writeJSON(w, map[string]any{"keys": s.extensionKeys(ctx, dom, ext), "company": company, "company_in_dialer": show,
			"aux": core.AuxNumbers(s.sw.Config().Features, dom.name)})
		return nil
	case http.MethodPut:
		var body struct {
			Keys []phoneKey `json:"keys"`
		}
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		keys, err := cleanKeys(body.Keys)
		if err != nil {
			return err
		}
		types, values := keysJSON(keys)
		user := s.keysUser(ctx, dom, extUUID)
		tx, err := s.store.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		// One row set per extension: rows of an earlier user of the
		// extension go.
		if _, err := tx.Exec(ctx, `delete from q_blfs where (domain_uuid = $1::uuid and extension = $2) or user_uuid = $3::uuid`,
			dom.uuid, ext, user); err != nil {
			return err
		}
		if len(keys) > 0 {
			if _, err := tx.Exec(ctx, `insert into q_blfs (domain_uuid, user_uuid, types, "values", extension, insert_date, update_date)
				values ($1::uuid, $2::uuid, $3, $4, $5, now(), now())`, dom.uuid, user, types, values, ext); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		s.log.Info("web: phone keys saved", "extension", ext, "domain", dom.name, "keys", len(keys), "user", p.key())
		s.hub.broadcastDomain(dom.name, map[string]any{"t": "keys"})
		writeJSON(w, map[string]any{"keys": keys})
		return nil
	}
	return errMethod
}

func (s *Server) companyKeysAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow) error {
	if !s.store.HasTable("q_company_blfs") {
		return badRequest("the database has no q_company_blfs table")
	}
	switch r.Method {
	case http.MethodGet:
		keys, show := s.companyKeys(ctx, dom)
		writeJSON(w, map[string]any{"keys": keys, "show_dialer": show, "aux": core.AuxNumbers(s.sw.Config().Features, dom.name)})
		return nil
	case http.MethodPut:
		var body struct {
			Keys       []phoneKey `json:"keys"`
			ShowDialer *bool      `json:"show_dialer"`
		}
		if err := decodeBody(r, &body); err != nil {
			return err
		}
		keys, err := cleanKeys(body.Keys)
		if err != nil {
			return err
		}
		types, values := keysJSON(keys)
		show := 1
		if body.ShowDialer != nil && !*body.ShowDialer {
			show = 0
		}
		tag, err := s.store.Pool().Exec(ctx, `update q_company_blfs set types = $2, "values" = $3, show_dialer = $4, update_date = now()
			where id = (select min(id) from q_company_blfs where domain_uuid = $1::uuid)`, dom.uuid, types, values, show)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.store.Pool().Exec(ctx, `insert into q_company_blfs (domain_uuid, types, "values", show_dialer, insert_date, update_date)
				values ($1::uuid, $2, $3, $4, now(), now())`, dom.uuid, types, values, show); err != nil {
				return err
			}
		}
		s.log.Info("web: company keys saved", "domain", dom.name, "keys", len(keys), "user", p.key())
		s.hub.broadcastDomain(dom.name, map[string]any{"t": "keys"})
		writeJSON(w, map[string]any{"keys": keys})
		return nil
	}
	return errMethod
}

// ---- the dialer's keys

type dialerKey struct {
	Type   string `json:"type"`
	Value  string `json:"value"`
	Label  string `json:"label"`
	State  string `json:"state,omitempty"` // BLF and park: idle, ringing, busy; status keys: on
	Number string `json:"number,omitempty"`
	Name   string `json:"name,omitempty"`
	Since  int64  `json:"since,omitempty"`
	Dial   string `json:"dial,omitempty"` // what pressing the key dials (park, status keys)
	Group  string `json:"group"`          // mine or company
}

// myKeys answers GET /api/keys for the signed-in extension: its keys and
// the company's with their lamps, the parking slots, DND and the agent
// statuses (*26N).
func (s *Server) myKeys(ctx context.Context, w http.ResponseWriter, p *principal) error {
	if p.Kind != "extension" || s.store == nil {
		writeJSON(w, map[string]any{"keys": []dialerKey{}, "parks": []dialerKey{}})
		return nil
	}
	dom, err := s.domain(ctx, p.Domain)
	if err != nil {
		return err
	}
	names := s.extensionNames(ctx, dom.uuid)
	states := map[string]core.BLF{}
	for _, b := range s.sw.BLFStates(dom.name) {
		states[b.Resource] = b
	}
	aux := core.AuxNumbers(s.sw.Config().Features, dom.name)
	status, statusAux := "", ""
	if a := s.sw.Config().Features.AgentByExtension(p.Extension, dom.name); a != nil {
		for _, x := range s.sw.Agents(dom.name) {
			if x.UUID == a.UUID {
				status, statusAux = x.Status, x.Aux
			}
		}
	}
	auxNumber := func(name string) int {
		for i, a := range aux {
			if strings.EqualFold(a, name) {
				return i + 1
			}
		}
		return 0
	}
	build := func(k phoneKey, group string) dialerKey {
		d := dialerKey{Type: k.Type, Value: k.Value, Group: group}
		switch k.Type {
		case keyBLF:
			d.Label = strings.TrimSpace(k.Value + " " + names[k.Value])
			d.State = "idle"
			if b, ok := states[k.Value]; ok {
				d.State, d.Number, d.Name, d.Since = b.State, b.Number, b.Name, b.Since
			}
		case keySpeedDial:
			d.Label = k.Value
			if n := names[k.Value]; n != "" {
				d.Label += " " + n
			}
		case keyPark:
			slot := "*59" + k.Value
			d.Label, d.Dial, d.State = "Park "+k.Value, slot, "idle"
			if b, ok := states[slot]; ok {
				d.State, d.Number, d.Name, d.Since = b.State, b.Number, b.Name, b.Since
			}
		case keyAgentStatus:
			d.Label = k.Value
			if n := auxNumber(k.Value); n > 0 {
				d.Dial = "*26" + strconv.Itoa(n)
			}
			if strings.EqualFold(k.Value, "available") && status == "Available" || status == "On Break" && strings.EqualFold(statusAux, k.Value) {
				d.State = "on"
			}
		case keyChangeStatus:
			d.Label = map[string]string{"IN": "Status IN", "OUT": "Status OUT"}[k.Value]
			n := map[string]int{"IN": 1, "OUT": 2}[k.Value]
			if n <= len(aux) {
				d.Dial = "*26" + strconv.Itoa(n)
				if n == 1 && status == "Available" || n == 2 && status == "On Break" && strings.EqualFold(statusAux, aux[1]) {
					d.State = "on"
				}
			}
		}
		return d
	}
	keys := []dialerKey{}
	for _, k := range s.extensionKeys(ctx, dom, p.Extension) {
		keys = append(keys, build(k, "mine"))
	}
	if company, show := s.companyKeys(ctx, dom); show {
		for _, k := range company {
			keys = append(keys, build(k, "company"))
		}
	}
	parks := []dialerKey{}
	for i := 1; i <= parkSlots; i++ {
		parks = append(parks, build(phoneKey{Type: keyPark, Value: strconv.Itoa(i)}, "parking"))
	}
	writeJSON(w, map[string]any{"keys": keys, "parks": parks, "aux": aux, "agent_status": status, "agent_aux": statusAux,
		"dnd": truthy(s.sw.UserVar(p.Extension, dom.name, "do_not_disturb")), "now": time.Now().Unix()})
	return nil
}

// blfPump tells the tabs of a domain when lamps change (ringq::blf) and when
// an extension's DND or forwarding changed from a phone.
func (s *Server) blfPump(stop <-chan struct{}) {
	sub := s.sw.Events().Subscribe(1024, func(e *core.Event) bool {
		return e.Name == core.EvCustom && (e.Subclass() == "ringq::blf" || e.Subclass() == "ringq::user_vars")
	})
	defer sub.Close()
	for {
		select {
		case <-stop:
			return
		case e := <-sub.C:
			dom := strings.ToLower(e.Get("Domain"))
			if e.Subclass() == "ringq::user_vars" {
				s.presenceChanged(dom)
				continue
			}
			s.hub.broadcastDomain(dom, map[string]any{"t": "blf", "resources": strings.Split(e.Get("Resources"), ",")})
		}
	}
}
