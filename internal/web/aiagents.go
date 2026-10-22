package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AI agents: a number (like an extension) that answers calls with a voice
// AI. Callers hear the agent's greeting and then talk with it: it answers
// from its prompt, context, FAQ text and web pages, and connects callers
// to people (connect keywords) or ends the call. DIDs, IVR options and
// Call Flow send calls to it like to any other number.
//
// The voice is the voice AI service's Realtime API (speech to speech, μ-law 8 kHz both
// ways, so the call's audio goes as it is), with the domain's API key.

// The voices of the Realtime API; marin and cedar sound the most natural.
var aiVoices = []string{"marin", "cedar", "alloy", "ash", "ballad", "coral", "echo", "sage", "shimmer", "verse"}

// The languages an agent can speak (and their names for the model).
var aiLanguages = []struct{ Code, Name string }{
	{"en", "English"}, {"es", "Spanish"}, {"fr", "French"}, {"de", "German"}, {"ja", "Japanese"}, {"ko", "Korean"},
	{"zh", "Chinese"}, {"fil", "Filipino"}, {"ta", "Tamil"}, {"ms", "Malay"}, {"hi", "Hindi"}, {"id", "Indonesian"},
	{"th", "Thai"}, {"vi", "Vietnamese"}, {"ar", "Arabic"}, {"pt", "Portuguese"}, {"it", "Italian"}, {"nl", "Dutch"},
}

var reModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,80}$`)

func aiLanguageName(code string) string {
	for _, l := range aiLanguages {
		if l.Code == code {
			return l.Name
		}
	}
	return ""
}

const (
	aiDefaultModel    = "gpt-realtime"
	aiDefaultURL      = "wss://api.openai.com/v1/realtime"
	aiDefaultGreeting = "Hi there! Welcome. How can I help you today?"
	aiDefaultPrompt   = "You are the friendly voice assistant of our company. Answer the caller's questions about our products " +
		"and services clearly and briefly. When you do not know the answer, say so and offer to connect the caller to a person."
	aiMaxURLs = 10
)

func aiAgentResource() *resource {
	return &resource{
		name: "aiagents", table: "rq_ai_agents", key: "ai_agent_uuid", order: "agent_extension", nameCol: "agent_name",
		numberCol: "agent_extension", enabledCol: "agent_enabled",
		columns: []string{"agent_name", "agent_extension", "agent_enabled", "agent_voice", "agent_noise", "agent_languages",
			"agent_greetings", "agent_prompt", "agent_context", "knowledge_urls", "knowledge_text", "max_minutes", "allow_end_call",
			"fallback_action", "agent_description"},
		required: []string{"agent_name", "agent_extension"},
		defaults: func(dom domainRow) map[string]string {
			g, _ := json.Marshal([]string{aiDefaultGreeting})
			return map[string]string{"agent_enabled": "true", "agent_voice": "marin", "agent_noise": "near", "agent_languages": "en",
				"agent_greetings": string(g), "agent_prompt": aiDefaultPrompt, "max_minutes": "10", "allow_end_call": "true"}
		},
		prepare: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string, raw map[string]any, isNew bool) error {
			if !reNumber.MatchString(row["agent_extension"]) {
				return badRequest("AI agent number: digits only (up to 15)")
			}
			row["agent_voice"] = strings.ToLower(firstNonEmpty(row["agent_voice"], "marin"))
			if !contains(aiVoices, row["agent_voice"]) {
				return badRequest("voice: one of %s", strings.Join(aiVoices, ", "))
			}
			if row["agent_noise"] != "far" {
				row["agent_noise"] = "near"
			}
			var langs []string
			for _, l := range strings.Split(row["agent_languages"], ",") {
				l = strings.ToLower(strings.TrimSpace(l))
				if l == "" || contains(langs, l) {
					continue
				}
				if aiLanguageName(l) == "" {
					return badRequest("language %q is not offered", l)
				}
				langs = append(langs, l)
			}
			if len(langs) == 0 {
				return badRequest("choose at least one language")
			}
			row["agent_languages"] = strings.Join(langs, ",")
			greets := aiGreetings(row["agent_greetings"])
			if len(greets) > 10 {
				return badRequest("greetings: up to 10")
			}
			for _, g := range greets {
				if len(g) > 600 {
					return badRequest("a greeting is too long (600 characters at most)")
				}
			}
			b, _ := json.Marshal(greets)
			row["agent_greetings"] = string(b)
			for k, limit := range map[string]int{"agent_prompt": 20000, "agent_context": 20000, "knowledge_text": 100000} {
				if len(row[k]) > limit {
					return badRequest("%s: %d characters at most", strings.ReplaceAll(strings.TrimPrefix(k, "agent_"), "_", " "), limit)
				}
			}
			urls, err := aiParseURLs(row["knowledge_urls"])
			if err != nil {
				return err
			}
			row["knowledge_urls"] = strings.Join(urls, "\n")
			row["max_minutes"] = strconv.Itoa(min(120, max(1, atoi(row["max_minutes"], 10))))
			row["allow_end_call"] = strconv.FormatBool(row["allow_end_call"] == "" || truthy(row["allow_end_call"]))
			return nil
		},
		children: &childSpec{table: "rq_ai_agent_keywords", key: "ai_agent_keyword_uuid", parent: "ai_agent_uuid", field: "keywords",
			order: "keyword_order, keyword", columns: []string{"keyword", "destination", "keyword_order"},
			prepare: func(dom domainRow, parent, c map[string]string, raw map[string]any) error {
				if c["keyword"] == "" {
					return badRequest("connect keyword: enter the word the caller says")
				}
				if len(c["keyword"]) > 80 {
					return badRequest("connect keyword %q is too long", c["keyword"])
				}
				if app, _ := splitAction(c["destination"]); app == "" {
					return badRequest("connect keyword %q: choose where the call goes", c["keyword"])
				}
				c["keyword_order"] = strconv.Itoa(atoi(c["keyword_order"], 10))
				return nil
			}},
		afterSave: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row, old map[string]string, raw map[string]any) error {
			if row["knowledge_urls"] != old["knowledge_urls"] {
				s.aiKnowledgeKick(dom.uuid, row["ai_agent_uuid"], strings.Fields(row["knowledge_urls"]))
			}
			return nil
		},
		afterDel: func(ctx context.Context, s *Server, tx pgx.Tx, dom domainRow, row map[string]string) error {
			if s.store.HasTable("rq_ai_knowledge") {
				_, err := tx.Exec(ctx, `delete from rq_ai_knowledge where ai_agent_uuid = $1::uuid`, row["ai_agent_uuid"])
				return err
			}
			return nil
		},
		dialplan: aiAgentDialplan,
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// aiGreetings reads the greetings: a JSON list, or one per line.
func aiGreetings(v string) []string {
	var list []string
	if err := json.Unmarshal([]byte(v), &list); err != nil {
		list = strings.Split(v, "\n")
	}
	out := []string{}
	for _, g := range list {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// aiParseURLs checks the agent's web addresses (one per line or space).
func aiParseURLs(v string) ([]string, error) {
	var out []string
	for _, f := range strings.Fields(v) {
		if !strings.Contains(f, "://") {
			f = "https://" + f
		}
		u, err := url.Parse(f)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, badRequest("web address %q is not valid", f)
		}
		u.Fragment = ""
		if !contains(out, u.String()) {
			out = append(out, u.String())
		}
	}
	if len(out) > aiMaxURLs {
		return nil, badRequest("web addresses: up to %d", aiMaxURLs)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The voice AI service of a domain

type aiConfig struct {
	Key, Model, URL string
	FromEnv         bool // the key is the server's RQ_AI_API_KEY
}

// aiEnvKey is the key set in the service's environment: RQ_AI_API_KEY, or
// the variable the voice AI service's own tools use.
func aiEnvKey() string {
	if k := strings.TrimSpace(os.Getenv("RQ_AI_API_KEY")); k != "" {
		return k
	}
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

func (s *Server) aiConfig(ctx context.Context, domainUUID string) aiConfig {
	var enc, model, base string
	if s.store != nil && s.store.HasTable("rq_ai_settings") {
		_ = s.store.Pool().QueryRow(ctx, `select coalesce(api_key_enc, ''), model, base_url from rq_ai_settings where domain_uuid = $1::uuid`,
			domainUUID).Scan(&enc, &model, &base)
	}
	c := aiConfig{Key: s.openPass(enc), Model: firstNonEmpty(model, aiDefaultModel), URL: firstNonEmpty(base, aiDefaultURL)}
	if c.Key == "" {
		c.Key, c.FromEnv = aiEnvKey(), true
	}
	return c
}

// aiKeyHint shows the end of a key ("sk-…abcd").
func aiKeyHint(k string) string {
	if len(k) < 12 {
		return strings.Repeat("•", len(k))
	}
	return k[:3] + "…" + k[len(k)-4:]
}

func (s *Server) aiAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	if !s.store.HasTable("rq_ai_agents") || !s.store.HasTable("rq_ai_calls") {
		return badRequest("AI agents need RingQswitch's rq_ai_ tables (the database user cannot create them)")
	}
	switch part(parts, 0) {
	case "calls":
		if !p.supervisor() {
			return errForbidden
		}
		return s.aiCallsAPI(ctx, w, r, dom, part(parts, 1))
	case "status":
		if !p.supervisor() {
			return errForbidden
		}
		return s.aiStatusAPI(ctx, w, dom)
	}
	if !p.owner() {
		return errForbidden
	}
	switch part(parts, 0) {
	case "settings":
		if part(parts, 1) == "test" {
			if r.Method != http.MethodPost {
				return errMethod
			}
			return s.aiTestAPI(ctx, w, r, dom)
		}
		return s.aiSettingsAPI(ctx, w, r, p, dom)
	case "knowledge":
		return s.aiKnowledgeAPI(ctx, w, r, dom, part(parts, 1))
	case "preview":
		if r.Method != http.MethodPost {
			return errMethod
		}
		return s.aiPreviewAPI(ctx, w, r, dom)
	case "options":
		langs := []map[string]string{}
		for _, l := range aiLanguages {
			langs = append(langs, map[string]string{"code": l.Code, "name": l.Name})
		}
		writeJSON(w, map[string]any{"voices": aiVoices, "languages": langs, "default_prompt": aiDefaultPrompt,
			"default_greeting": aiDefaultGreeting})
		return nil
	}
	return notFound("page")
}

func (s *Server) aiSettingsAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow) error {
	switch r.Method {
	case http.MethodGet:
		var enc, model, base string
		_ = s.store.Pool().QueryRow(ctx, `select coalesce(api_key_enc, ''), model, base_url from rq_ai_settings where domain_uuid = $1::uuid`,
			dom.uuid).Scan(&enc, &model, &base)
		key := s.openPass(enc)
		env := aiEnvKey()
		writeJSON(w, map[string]any{"has_key": key != "", "key_hint": aiKeyHint(key), "env_key": env != "", "model": model,
			"base_url": base, "default_model": aiDefaultModel, "default_base_url": aiDefaultURL, "key_lost": enc != "" && key == ""})
		return nil
	case http.MethodPut:
		var b struct {
			APIKey   *string `json:"api_key"`
			ClearKey bool    `json:"clear_key"`
			Model    string  `json:"model"`
			BaseURL  string  `json:"base_url"`
		}
		if err := decodeBody(r, &b); err != nil {
			return err
		}
		b.Model, b.BaseURL = strings.TrimSpace(b.Model), strings.TrimSpace(b.BaseURL)
		if b.Model != "" && !reModelName.MatchString(b.Model) {
			return badRequest("model: letters, digits, dots and dashes")
		}
		if b.BaseURL != "" {
			u, err := url.Parse(b.BaseURL)
			if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
				return badRequest("service address: a wss:// address (default %s)", aiDefaultURL)
			}
		}
		var enc string
		_ = s.store.Pool().QueryRow(ctx, `select coalesce(api_key_enc, '') from rq_ai_settings where domain_uuid = $1::uuid`, dom.uuid).Scan(&enc)
		if b.ClearKey {
			enc = ""
		}
		if b.APIKey != nil && strings.TrimSpace(*b.APIKey) != "" {
			k := strings.TrimSpace(*b.APIKey)
			if len(k) < 20 || strings.ContainsAny(k, " \t\r\n") {
				return badRequest("the API key does not look right")
			}
			if enc = s.sealPass(k); enc == "" {
				return badRequest("the server cannot store keys: its data-dir is not writable")
			}
		}
		if _, err := s.store.Pool().Exec(ctx, `insert into rq_ai_settings (domain_uuid, api_key_enc, model, base_url, updated_at)
			values ($1::uuid, nullif($2, ''), $3, $4, now()) on conflict (domain_uuid) do update set api_key_enc = excluded.api_key_enc,
			model = excluded.model, base_url = excluded.base_url, updated_at = now()`, dom.uuid, enc, b.Model, b.BaseURL); err != nil {
			return err
		}
		s.log.Info("web: AI agent settings saved", "domain", dom.name, "user", p.key(), "key", enc != "")
		writeJSON(w, map[string]string{"ok": "saved"})
		return nil
	}
	return errMethod
}

// aiTestAPI opens a session with the voice AI service (the domain's key, or
// one given to try before saving) and reports what it said.
func (s *Server) aiTestAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow) error {
	var b struct {
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
		BaseURL string `json:"base_url"`
	}
	_ = decodeBody(r, &b)
	c := s.aiConfig(ctx, dom.uuid)
	if k := strings.TrimSpace(b.APIKey); k != "" {
		c.Key = k
	}
	if m := strings.TrimSpace(b.Model); m != "" {
		c.Model = m
	}
	if u := strings.TrimSpace(b.BaseURL); u != "" {
		c.URL = u
	}
	if c.Key == "" {
		return badRequest("enter the API key first")
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	rt, err := rtDial(tctx, c)
	if err == nil {
		defer rt.close()
		err = rt.configure(tctx, rtSession{Instructions: "Test.", Voice: "marin"})
	}
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return nil
	}
	writeJSON(w, map[string]any{"ok": true, "model": c.Model, "ms": time.Since(start).Milliseconds()})
	return nil
}

// ---- calls taken

func (s *Server) aiCallsAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow, id string) error {
	if r.Method != http.MethodGet {
		return errMethod
	}
	if id != "" {
		if !reUUID.MatchString(id) {
			return notFound("AI call")
		}
		rows, err := s.store.JSONRows(ctx, `select to_jsonb(c) || jsonb_build_object('agent_name', coalesce(a.agent_name, ''))
			from rq_ai_calls c left join rq_ai_agents a using (ai_agent_uuid) where c.domain_uuid = $1::uuid and c.ai_call_uuid = $2::uuid`,
			dom.uuid, id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return notFound("AI call")
		}
		out := map[string]any{}
		for k, v := range rows[0] {
			out[k] = v
		}
		var turns []map[string]any
		_ = json.Unmarshal([]byte(rows[0]["transcript"]), &turns)
		if turns == nil {
			turns = []map[string]any{}
		}
		out["transcript"] = turns
		writeJSON(w, out)
		return nil
	}
	q := r.URL.Query()
	limit := min(200, max(1, atoi(q.Get("limit"), 50)))
	offset := max(0, atoi(q.Get("offset"), 0))
	where, args := `c.domain_uuid = $1::uuid`, []any{dom.uuid}
	if a := q.Get("agent"); a != "" {
		if !reUUID.MatchString(a) {
			return badRequest("invalid agent")
		}
		args = append(args, a)
		where += fmt.Sprintf(` and c.ai_agent_uuid = $%d::uuid`, len(args))
	}
	var total int
	if err := s.store.Pool().QueryRow(ctx, `select count(*) from rq_ai_calls c where `+where, args...).Scan(&total); err != nil {
		return err
	}
	args = append(args, limit, offset)
	rows, err := s.store.JSONRows(ctx, fmt.Sprintf(`select to_jsonb(c) - 'transcript' || jsonb_build_object('agent_name', coalesce(a.agent_name, ''),
		'agent_extension', coalesce(a.agent_extension, '')) from rq_ai_calls c left join rq_ai_agents a using (ai_agent_uuid)
		where %s order by c.started_at desc limit $%d offset $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return err
	}
	if rows == nil {
		rows = []map[string]string{}
	}
	writeJSON(w, map[string]any{"rows": rows, "total": total})
	return nil
}

// aiStatusAPI sums up each agent's calls: today, now and in the last 30 days.
func (s *Server) aiStatusAPI(ctx context.Context, w http.ResponseWriter, dom domainRow) error {
	rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('ai_agent_uuid', a.ai_agent_uuid,
		'active', count(c.*) filter (where c.ended_at is null and c.started_at > now() - interval '4 hours'),
		'today', count(c.*) filter (where c.started_at >= date_trunc('day', now())),
		'calls', count(c.*),
		'transferred', count(c.*) filter (where c.outcome = 'transferred'),
		'failed', count(c.*) filter (where c.outcome = 'failed'),
		'avg_duration', coalesce(round(avg(c.duration) filter (where c.ended_at is not null)), 0),
		'last_call', max(c.started_at))
		from rq_ai_agents a left join rq_ai_calls c on c.ai_agent_uuid = a.ai_agent_uuid and c.started_at > now() - interval '30 days'
		where a.domain_uuid = $1::uuid group by a.ai_agent_uuid`, dom.uuid)
	if err != nil {
		return err
	}
	c := s.aiConfig(ctx, dom.uuid)
	out := map[string]any{"agents": rows, "configured": c.Key != "", "model": c.Model}
	if rows == nil {
		out["agents"] = []map[string]string{}
	}
	writeJSON(w, out)
	return nil
}
