package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// A call with an AI agent: the caller's audio streams to the voice AI
// service and its answers play back as they arrive. When the caller speaks
// over an answer it stops at once, and the service is told how much was
// heard. The agent connects callers (connect keywords), ends calls, and
// searches its knowledge through tools; every call is logged with what
// was said (rq_ai_calls).

type aiKeyword struct {
	Word, Dest string
}

type aiAgent struct {
	UUID, Domain, DomainName, Name, Extension string
	Voice, Noise                              string
	Languages, Greetings                      []string
	Prompt, Context, FAQ                      string
	MaxMinutes                                int
	AllowEnd                                  bool
	Fallback                                  string
	Keywords                                  []aiKeyword
}

func (s *Server) aiLoadAgent(ctx context.Context, id string) (*aiAgent, error) {
	if !reUUID.MatchString(id) {
		return nil, fmt.Errorf("ai_agent: %q is not an AI agent id", id)
	}
	rows, err := s.store.JSONRows(ctx, `select to_jsonb(a) || jsonb_build_object('domain_name', coalesce(d.domain_name, ''))
		from rq_ai_agents a left join v_domains d using (domain_uuid) where a.ai_agent_uuid = $1::uuid`, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("ai_agent: no AI agent %s", id)
	}
	r := rows[0]
	a := &aiAgent{UUID: id, Domain: r["domain_uuid"], DomainName: r["domain_name"], Name: r["agent_name"], Extension: r["agent_extension"],
		Voice: firstNonEmpty(r["agent_voice"], "marin"), Noise: firstNonEmpty(r["agent_noise"], "near"), Prompt: r["agent_prompt"],
		Context: r["agent_context"], FAQ: r["knowledge_text"], MaxMinutes: min(120, max(1, atoi(r["max_minutes"], 10))),
		AllowEnd: r["allow_end_call"] == "" || truthy(r["allow_end_call"]), Fallback: r["fallback_action"],
		Greetings: aiGreetings(r["agent_greetings"])}
	if r["agent_enabled"] != "" && !truthy(r["agent_enabled"]) {
		return nil, fmt.Errorf("ai_agent: %s is disabled", a.Name)
	}
	for _, l := range strings.Split(r["agent_languages"], ",") {
		if l = strings.TrimSpace(l); aiLanguageName(l) != "" {
			a.Languages = append(a.Languages, l)
		}
	}
	if len(a.Languages) == 0 {
		a.Languages = []string{"en"}
	}
	kws, err := s.store.JSONRows(ctx, `select jsonb_build_object('k', keyword, 'd', destination) from rq_ai_agent_keywords
		where ai_agent_uuid = $1::uuid order by keyword_order, keyword`, id)
	if err != nil {
		return nil, err
	}
	for _, k := range kws {
		if w := strings.TrimSpace(k["k"]); w != "" && k["d"] != "" {
			a.Keywords = append(a.Keywords, aiKeyword{Word: w, Dest: k["d"]})
		}
	}
	return a, nil
}

// aiKnowledge loads what the agent knows: the text for its instructions and
// the index of the rest.
func (s *Server) aiKnowledge(ctx context.Context, a *aiAgent) (inline string, idx *aiIndex) {
	var docs []aiChunk
	var b strings.Builder
	if faq := strings.TrimSpace(a.FAQ); faq != "" {
		if len(faq) <= aiInlineFAQ {
			b.WriteString(faq + "\n")
		}
		for _, c := range aiFAQChunks(faq) {
			docs = append(docs, aiChunk{source: "faq", title: "FAQ", text: c})
		}
	}
	web, err := s.store.JSONRows(ctx, `select jsonb_build_object('s', source, 't', coalesce(title, ''), 'c', content) from rq_ai_knowledge
		where ai_agent_uuid = $1::uuid order by chunk_no`, a.UUID)
	if err != nil {
		s.log.Warn("ai agent: loading knowledge", "agent", a.Name, "err", err)
	}
	size := 0
	for _, r := range web {
		docs = append(docs, aiChunk{source: r["s"], title: r["t"], text: r["c"]})
		size += len(r["c"])
	}
	if size > 0 && size <= aiInlineWeb {
		last := ""
		for _, r := range web {
			if r["t"] != last {
				b.WriteString("\n[" + r["t"] + "]\n")
				last = r["t"]
			}
			b.WriteString(r["c"] + "\n")
		}
	}
	if len(a.FAQ) > aiInlineFAQ || size > aiInlineWeb {
		idx = newAIIndex(docs)
	}
	return strings.TrimSpace(b.String()), idx
}

// aiInstructions are what the agent is told before the call.
func aiInstructions(a *aiAgent, inline string, search bool) string {
	var langs []string
	for _, l := range a.Languages {
		langs = append(langs, aiLanguageName(l))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, a voice assistant answering phone calls.\n\n", firstNonEmpty(a.Name, "the assistant"))
	b.WriteString("# How you speak\n")
	b.WriteString("- This is a live phone call. Answer right away, warmly and naturally, like a friendly person; keep answers short " +
		"(one to three sentences) and let the caller talk.\n")
	b.WriteString("- Never use lists, markdown, emojis or symbols. Say numbers, prices, dates and web addresses the way people say them.\n")
	if len(langs) == 1 {
		fmt.Fprintf(&b, "- Speak %s.\n", langs[0])
	} else {
		fmt.Fprintf(&b, "- You speak %s. Answer in the language the caller speaks when it is one of these; otherwise use %s.\n",
			strings.Join(langs, ", "), langs[0])
	}
	b.WriteString("- If you did not hear or understand the caller, ask them to repeat. Do not guess.\n")
	b.WriteString("- Answer questions about the company and its products from your instructions and knowledge below. " +
		"You may also briefly answer general questions the caller asks.\n")
	b.WriteString("- Never invent prices, offers, dates or facts about the company. If you do not know, say so")
	if len(a.Keywords) > 0 {
		b.WriteString(" and offer to connect the caller to a person")
	}
	b.WriteString(".\n")
	if p := strings.TrimSpace(a.Prompt); p != "" {
		b.WriteString("\n# Your role\n" + p + "\n")
	}
	if c := strings.TrimSpace(a.Context); c != "" {
		b.WriteString("\n# Context\n" + c + "\n")
	}
	if inline != "" {
		b.WriteString("\n# Knowledge\n" + inline + "\n")
	}
	if search {
		b.WriteString("\n# Looking things up\nWhen the caller asks about the company, its products, services, prices or policies and the " +
			"answer is not above, call search_knowledge with a short query first, then answer from what it returns.\n")
	}
	if len(a.Keywords) > 0 {
		b.WriteString("\n# Connecting callers\nWhen the caller asks to speak to a person or for one of these, call transfer_call with it: ")
		var ws []string
		for _, k := range a.Keywords {
			ws = append(ws, k.Word)
		}
		b.WriteString(strings.Join(ws, ", ") + ". Before calling it, tell the caller in a few words that you are connecting them.\n")
	}
	if a.AllowEnd {
		b.WriteString("\n# Ending the call\nWhen the caller says goodbye or has nothing more to ask, say a short goodbye and call end_call.\n")
	}
	return b.String()
}

func aiTools(a *aiAgent, search bool) []rtTool {
	var tools []rtTool
	if search {
		tools = append(tools, rtTool{Name: "search_knowledge", Description: "Search the company's web pages and FAQ for the answer to the caller's question.",
			Params: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "What to look up, in a few words"}}, "required": []string{"query"}}})
	}
	if len(a.Keywords) > 0 {
		var ws []string
		for _, k := range a.Keywords {
			ws = append(ws, k.Word)
		}
		tools = append(tools, rtTool{Name: "transfer_call", Description: "Connect the caller to a person or department.",
			Params: map[string]any{"type": "object", "properties": map[string]any{
				"destination": map[string]any{"type": "string", "enum": ws, "description": "Who the caller wants"},
				"reason":      map[string]any{"type": "string", "description": "Why, in a few words"}}, "required": []string{"destination"}}})
	}
	if a.AllowEnd {
		tools = append(tools, rtTool{Name: "end_call", Description: "Hang up after saying goodbye, when the conversation is over.",
			Params: map[string]any{"type": "object", "properties": map[string]any{
				"reason": map[string]any{"type": "string", "description": "Why the call ends"}}}})
	}
	return tools
}

type aiTurn struct {
	Role string  `json:"role"` // caller | agent | event
	Text string  `json:"text"`
	At   float64 `json:"t"` // seconds into the call
}

// aiAction is a transfer or hang-up waiting for its last words to be said.
type aiAction struct {
	kind     string // transfer | end
	kw       aiKeyword
	respID   string // the answer saying the last words
	await    bool   // that answer is still to be created
	spoken   bool
	deadline time.Time
}

type aiCall struct {
	s     *Server
	t     *core.Talk
	a     *aiAgent
	rt    *rtClient
	log   *slog.Logger
	idx   *aiIndex
	start time.Time
	id    string // rq_ai_calls row

	turns    []aiTurn
	active   string          // the answer being made ("" = none)
	idle     []func()        // to do when it is done
	spoke    map[string]bool // answers that said something
	dropped  map[string]bool // answers the caller spoke over
	item     string          // the answer item playing
	offset   time.Duration   // where it starts in what Talk said
	pending  *aiAction       // transfer or hang-up to do
	kwAt     time.Time
	kwHeard  *aiKeyword
	outcome  string
	transfer string
	failure  string
}

// aiRun is the ai_agent application's work (core.AIAgentFunc).
func (s *Server) aiRun(ctx context.Context, t *core.Talk, agentID string) error {
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	a, err := s.aiLoadAgent(lctx, agentID)
	cancel()
	if err != nil {
		s.log.Warn("ai agent", "call", t.UUID(), "err", err)
		t.Note("ai", "AI agent unavailable")
		return err
	}
	c := &aiCall{s: s, t: t, a: a, start: time.Now(), log: s.log.With("ai_agent", a.Name, "call", t.UUID()),
		spoke: map[string]bool{}, dropped: map[string]bool{}}
	t.SetVar("ai_agent_name", a.Name)
	t.Note("ai", "AI agent "+a.Name+" answered")
	c.logStart()
	err = c.run(ctx)
	if err != nil && c.failure == "" {
		c.failure = err.Error()
	}
	c.logEnd()
	if c.failure != "" && c.outcome == "failed" {
		c.log.Warn("ai agent: call failed", "err", c.failure)
		t.Note("ai", "AI agent failed: "+c.failure)
		if !t.HungUp() && ctx.Err() == nil {
			c.fallback()
		}
	}
	return nil
}

// fallback sends the call where the agent says when it cannot talk.
func (c *aiCall) fallback() {
	app, data := splitAction(c.a.Fallback)
	switch app {
	case "transfer":
		f := strings.Fields(data)
		if len(f) > 0 {
			ctxName := ""
			if len(f) >= 3 {
				ctxName = f[2]
			}
			c.t.Transfer(f[0], firstNonEmpty(ctxName, c.a.DomainName))
		}
	}
}

func (c *aiCall) run(ctx context.Context) error {
	cfg := c.s.aiConfig(ctx, c.a.Domain)
	if cfg.Key == "" {
		c.outcome = "failed"
		return errors.New("no API key for the voice AI service (AI agent settings)")
	}
	inline, idx := c.s.aiKnowledge(ctx, c.a)
	c.idx = idx
	sess := rtSession{Instructions: aiInstructions(c.a, inline, idx != nil), Voice: c.a.Voice, Noise: c.a.Noise,
		Tools: aiTools(c.a, idx != nil), Transcribe: true}
	if len(c.a.Languages) == 1 {
		sess.Language = map[string]string{"fil": "tl"}[c.a.Languages[0]]
		if sess.Language == "" && len(c.a.Languages[0]) == 2 {
			sess.Language = c.a.Languages[0]
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	rt, err := rtDial(dctx, cfg)
	if err == nil {
		c.rt = rt
		defer rt.close()
		err = rt.configure(dctx, sess)
	}
	cancel()
	if err != nil {
		c.outcome = "failed"
		return err
	}
	if len(c.a.Greetings) > 0 {
		g := c.a.Greetings[rand.IntN(len(c.a.Greetings))]
		if err := rt.respond("Greet the caller by saying exactly this, word for word, and nothing else: " + g); err != nil {
			c.outcome = "failed"
			return err
		}
	} else if err := rt.respond("Greet the caller briefly and ask how you can help."); err != nil {
		c.outcome = "failed"
		return err
	}
	heard := make(chan error, 1)
	go func() { heard <- c.listen(ctx) }()
	limit := time.NewTimer(time.Duration(c.a.MaxMinutes) * time.Minute)
	defer limit.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	events := rt.ev
	for {
		select {
		case <-ctx.Done():
			// Transferred (the dialplan of the destination runs now).
			if c.outcome == "" {
				c.outcome = "completed"
			}
			return nil
		case <-c.t.Done():
			if c.outcome == "" {
				c.outcome = "completed"
				c.note("event", "The caller hung up")
			}
			return nil
		case err := <-heard:
			if c.t.HungUp() || ctx.Err() != nil {
				continue
			}
			c.outcome = "failed"
			return err
		case e, ok := <-events:
			if !ok {
				events = nil
				if c.t.HungUp() || ctx.Err() != nil {
					continue
				}
				c.outcome = "failed"
				return rt.failure()
			}
			if err := c.handle(e); err != nil {
				c.outcome = "failed"
				return err
			}
		case d := <-c.t.Digits():
			if d == '0' && len(c.a.Keywords) > 0 {
				c.note("event", "The caller pressed 0")
				c.connect(c.a.Keywords[0], false, true)
			}
		case <-limit.C:
			c.note("event", "Time limit reached")
			c.finish("Politely tell the caller that the time for this call is up, thank them, and say goodbye.")
		case <-tick.C:
			c.step()
		}
	}
}

// listen streams what the caller says, 40 ms at a time (silence when the
// phone sends nothing, so the service still hears the pauses).
func (c *aiCall) listen(ctx context.Context) error {
	buf := make([]byte, 0, 2*media.FrameSamples)
	silence := make([]int16, media.FrameSamples)
	for {
		f, err := c.t.ListenWait(60 * time.Millisecond)
		if err != nil {
			return err
		}
		if f == nil {
			f = silence
		}
		for _, v := range f {
			buf = append(buf, media.LinearToUlaw(v))
		}
		if len(buf) >= 2*media.FrameSamples {
			if err := c.rt.appendAudio(buf); err != nil {
				return fmt.Errorf("sending audio to the voice AI service: %w", err)
			}
			buf = buf[:0]
		}
	}
}

func (c *aiCall) note(role, text string) {
	if text = strings.TrimSpace(text); text != "" {
		c.turns = append(c.turns, aiTurn{Role: role, Text: text, At: time.Since(c.start).Round(100 * time.Millisecond).Seconds()})
	}
}

// whenIdle runs fn now when no answer is being made, else after it.
func (c *aiCall) whenIdle(fn func()) {
	if c.active == "" {
		fn()
		return
	}
	c.idle = append(c.idle, fn)
}

func (c *aiCall) handle(e rtEvent) error {
	switch e.Type {
	case "response.created":
		if e.Response != nil {
			c.active = e.Response.ID
			if c.pending != nil && c.pending.await {
				c.pending.respID, c.pending.await = e.Response.ID, false
			}
		}
	case "response.output_audio.delta", "response.audio.delta":
		if c.dropped[e.ResponseID] {
			return nil
		}
		b, err := base64.StdEncoding.DecodeString(e.Delta)
		if err != nil || len(b) == 0 {
			return nil
		}
		if e.ItemID != c.item {
			c.item, c.offset = e.ItemID, c.t.Said()+c.t.Pending()
		}
		pcm := make([]int16, len(b))
		for i, u := range b {
			pcm[i] = media.UlawToLinear(u)
		}
		c.t.Say(pcm)
		c.spoke[e.ResponseID] = true
	case "response.output_audio_transcript.done", "response.audio_transcript.done":
		c.note("agent", e.Transcript)
	case "input_audio_buffer.speech_started":
		// The caller speaks over the agent: stop at once.
		if c.t.Pending() > 0 && (c.pending == nil || c.pending.kind != "end") {
			said := c.t.Hush()
			if c.item != "" {
				_ = c.rt.truncate(c.item, max(0, said-c.offset))
			}
			if c.active != "" {
				c.dropped[c.active] = true
			}
			c.item, c.offset = "", 0
		}
	case "conversation.item.input_audio_transcription.completed":
		c.note("caller", e.Transcript)
		c.keywordHeard(e.Transcript)
	case "response.function_call_arguments.done":
		c.tool(e)
	case "response.done":
		id := ""
		if e.Response != nil {
			id = e.Response.ID
			if e.Response.Status == "failed" && e.Response.StatusDetails != nil && e.Response.StatusDetails.Error != nil {
				c.log.Warn("ai agent: answer failed", "err", e.Response.StatusDetails.Error.Error())
				c.failure = e.Response.StatusDetails.Error.Error()
			}
		}
		if c.pending != nil && c.pending.respID == id {
			c.pending.spoken = true
		}
		if c.active == id {
			c.active = ""
		}
		todo := c.idle
		c.idle = nil
		for _, fn := range todo {
			fn()
		}
	case "error":
		if e.Error != nil {
			switch e.Error.Code {
			case "response_cancel_not_active", "conversation_already_has_active_response", "item_truncate_invalid_item_id":
				c.log.Debug("ai agent: service", "err", e.Error.Error())
			case "invalid_api_key", "insufficient_quota", "session_expired":
				return fmt.Errorf("the voice AI service: %s", e.Error.Error())
			default:
				c.log.Warn("ai agent: service error", "err", e.Error.Error())
			}
		}
	}
	return nil
}

// tool runs what the agent asked for.
func (c *aiCall) tool(e rtEvent) {
	var args map[string]any
	_ = json.Unmarshal([]byte(e.Arguments), &args)
	said := c.spoke[e.ResponseID]
	switch e.Name {
	case "search_knowledge":
		q := strings.TrimSpace(toStr(args["query"]))
		var found []map[string]string
		for _, ch := range c.idx.search(q, aiSearchResults) {
			found = append(found, map[string]string{"page": ch.title, "text": ch.text})
		}
		res := map[string]any{"results": found}
		if len(found) == 0 {
			res["note"] = "Nothing found. Say you do not know this and offer to connect the caller to a person if possible."
		}
		c.whenIdle(func() {
			_ = c.rt.toolResult(e.CallID, res)
			_ = c.rt.respond("")
		})
	case "transfer_call":
		dest := strings.TrimSpace(toStr(args["destination"]))
		for _, k := range c.a.Keywords {
			if strings.EqualFold(k.Word, dest) {
				if r := strings.TrimSpace(toStr(args["reason"])); r != "" {
					c.note("event", "Connecting to "+k.Word+": "+r)
				}
				c.whenIdle(func() { _ = c.rt.toolResult(e.CallID, map[string]string{"status": "connecting"}) })
				c.connect(k, said, false)
				return
			}
		}
		c.whenIdle(func() {
			_ = c.rt.toolResult(e.CallID, map[string]string{"error": "unknown destination " + dest})
			_ = c.rt.respond("")
		})
	case "end_call":
		if !c.a.AllowEnd {
			return
		}
		c.whenIdle(func() { _ = c.rt.toolResult(e.CallID, map[string]string{"status": "ending"}) })
		if said {
			c.pending = &aiAction{kind: "end", respID: e.ResponseID, deadline: time.Now().Add(15 * time.Second)}
		} else {
			c.finish("Say a short, friendly goodbye to the caller.")
		}
	default:
		c.whenIdle(func() { _ = c.rt.toolResult(e.CallID, map[string]string{"error": "unknown tool"}) })
	}
}

// connect transfers the call to a connect keyword's destination after the
// agent said so (said: the current answer did; else it is asked to, after
// stopping what it was saying when the caller interrupts: 0, a keyword).
func (c *aiCall) connect(k aiKeyword, said, interrupt bool) {
	if c.pending != nil && c.pending.kind == "transfer" {
		return
	}
	c.pending = &aiAction{kind: "transfer", kw: k, deadline: time.Now().Add(12 * time.Second)}
	if said && c.active != "" {
		c.pending.respID = c.active
		return
	}
	if said {
		c.pending.spoken = true
		return
	}
	c.pending.await = true
	if interrupt {
		c.t.Hush()
		c.item, c.offset = "", 0
		if c.active != "" {
			c.dropped[c.active] = true
			_ = c.rt.cancel()
		}
	}
	c.whenIdle(func() {
		_ = c.rt.respond("In one short sentence, in the caller's language, tell the caller you are connecting them to " + k.Word +
			" now. Say nothing else.")
	})
}

// finish has the agent say its last words, then hangs up.
func (c *aiCall) finish(say string) {
	if c.pending != nil {
		return
	}
	c.pending = &aiAction{kind: "end", await: true, deadline: time.Now().Add(15 * time.Second)}
	c.whenIdle(func() { _ = c.rt.respond(say) })
}

var reWordEdge = regexp.MustCompile(`[\p{L}\p{N}]`)

// keywordHeard connects the caller who said a connect keyword when the
// agent did not do it by itself within moments.
func (c *aiCall) keywordHeard(text string) {
	if c.pending != nil || text == "" {
		return
	}
	low := " " + strings.ToLower(text) + " "
	for _, k := range c.a.Keywords {
		w := strings.ToLower(k.Word)
		i := strings.Index(low, w)
		if i < 0 {
			continue
		}
		before, after := low[i-1:i], low[i+len(w):i+len(w)+1]
		if reWordEdge.MatchString(before) || reWordEdge.MatchString(after) {
			continue
		}
		kw := k
		c.kwHeard, c.kwAt = &kw, time.Now().Add(1500*time.Millisecond)
		return
	}
}

// step does what is due: a pending transfer or hang-up once its last words
// were said, a keyword the agent did not act on.
func (c *aiCall) step() {
	if c.kwHeard != nil && time.Now().After(c.kwAt) {
		k := *c.kwHeard
		c.kwHeard = nil
		if c.pending == nil {
			c.note("event", "Connect keyword heard: "+k.Word)
			c.connect(k, false, true)
		}
	}
	p := c.pending
	if p == nil {
		return
	}
	if !(p.spoken && c.t.Pending() == 0) && time.Now().Before(p.deadline) {
		return
	}
	c.pending = nil
	c.t.WaitSaid(time.Second)
	switch p.kind {
	case "transfer":
		app, data := splitAction(p.kw.Dest)
		f := strings.Fields(data)
		switch {
		case app == "transfer" && len(f) > 0:
			ctxName := c.a.DomainName
			if len(f) >= 3 {
				ctxName = f[2]
			}
			c.outcome, c.transfer = "transferred", p.kw.Word+" ("+f[0]+")"
			c.note("event", "Transferred to "+p.kw.Word+" ("+f[0]+")")
			c.t.Note("ai", "AI agent connected the caller to "+p.kw.Word+" ("+f[0]+")")
			c.t.Transfer(f[0], ctxName)
		case app == "hangup":
			c.outcome = "completed"
			c.t.Hangup()
		default:
			c.log.Warn("ai agent: connect keyword destination cannot be used", "keyword", p.kw.Word, "destination", p.kw.Dest)
		}
	case "end":
		c.outcome = "completed"
		c.note("event", "The agent ended the call")
		c.t.Note("ai", "AI agent ended the call")
		c.t.Hangup()
	}
}

// ---- the call log

func (c *aiCall) logStart() {
	if !c.s.store.HasTable("rq_ai_calls") {
		return
	}
	c.id = newUUID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.s.store.Pool().Exec(ctx, `insert into rq_ai_calls (ai_call_uuid, domain_uuid, ai_agent_uuid, call_uuid, caller_number,
		caller_name, destination_number, started_at, outcome) values ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, 'active')`,
		c.id, c.a.Domain, c.a.UUID, c.t.UUID(), c.t.Var("caller_id_number"), c.t.Var("caller_id_name"),
		firstNonEmpty(c.t.Var("caller_destination"), c.t.Var("destination_number")), c.start); err != nil {
		c.log.Warn("ai agent: call log", "err", err)
		c.id = ""
	}
}

func (c *aiCall) logEnd() {
	if c.id == "" {
		return
	}
	turns := 0
	for _, t := range c.turns {
		if t.Role == "caller" {
			turns++
		}
	}
	tr, _ := json.Marshal(c.turns)
	if c.turns == nil {
		tr = []byte("[]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failure := ""
	if c.outcome == "failed" {
		failure = c.failure
	}
	if _, err := c.s.store.Pool().Exec(ctx, `update rq_ai_calls set ended_at = now(), duration = $2, outcome = $3, transferred_to = $4,
		turns = $5, transcript = $6, error = nullif($7, '') where ai_call_uuid = $1::uuid`, c.id, int(time.Since(c.start).Seconds()),
		firstNonEmpty(c.outcome, "completed"), c.transfer, turns, string(tr), failure); err != nil {
		c.log.Warn("ai agent: call log", "err", err)
	}
}

// aiStart makes AI agents answer calls and closes the log rows of calls a
// restart cut short.
func (s *Server) aiStart() {
	if s.store == nil || !s.store.HasTable("rq_ai_agents") {
		return
	}
	if s.store.HasTable("rq_ai_calls") {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = s.store.Pool().Exec(ctx, `update rq_ai_calls set ended_at = started_at, outcome = 'interrupted' where ended_at is null`)
		cancel()
	}
	s.sw.SetAIAgent(s.aiRun)
}
