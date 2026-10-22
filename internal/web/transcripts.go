package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/transcribe"
)

// Call transcription: when a domain turns it on, its calls are recorded in
// stereo (each party on its own channel) and, after the hang-up,
// transcribed on this server: who said what and when (Speaker A, the
// caller; Speaker B, who answered), with the sentiment of each utterance,
// each speaker and the call. The transcript shows from Call history and
// the Queue calls report.
//
//	GET    /api/d/{domain}/transcription              settings, engine, models, counts (owners)
//	PUT    /api/d/{domain}/transcription              {enabled, scope, language, model} (owners)
//	POST   /api/d/{domain}/transcription/models/{m}   download a speech model (server access)
//	DELETE /api/d/{domain}/transcription/models/{m}   delete a downloaded model (server access)
//	GET    /api/d/{domain}/transcripts/{call uuid}    the transcript (whoever may see the call)
//	POST   /api/d/{domain}/transcripts/{call uuid}    transcribe a recorded call (again) (managers)
//
// Statuses: queued, processing, then done, empty (no speech) or failed;
// each change is pushed to the domain's open pages ({t: "transcript"}).

type trSettings struct {
	Enabled  bool   `json:"enabled"`
	Scope    string `json:"scope"` // external (calls through a trunk) or all
	Language string `json:"language"`
	Model    string `json:"model"` // a speech model's name; "" = the server's default
	// Voicemail: messages left in the domain's voicemail boxes are
	// transcribed too (whether or not calls are).
	Voicemail bool `json:"voicemail"`
	// Live: calls are written down while they go on, for the extensions on
	// them (the dialer's live panel); LiveModel: "" = the fastest installed.
	Live      bool   `json:"live"`
	LiveModel string `json:"live_model"`
}

// trEngine is what was found of the speech recogniser: the program, the models, and
// the one used when a domain chooses none.
type trEngine struct {
	command string
	models  []transcribe.Installed
	def     transcribe.Installed
	err     error
}

// model returns the model a domain's calls are transcribed with.
func (e trEngine) model(name string) transcribe.Installed {
	for _, m := range e.models {
		if m.Name == name {
			return m
		}
	}
	return e.def
}

// trDownload is a speech model being downloaded.
type trDownload struct {
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
}

var reLanguage = regexp.MustCompile(`^(auto|[a-z]{2,3})$`)

func (s *Server) trReady() error {
	if s.store == nil || !s.store.HasTable("rq_transcripts") || !s.store.HasTable("rq_transcription_settings") {
		return badRequest("the transcription tables are missing: restart RingQswitch once to add them")
	}
	return nil
}

// transcribeWanted tells the switch which calls to record for a transcript.
func (s *Server) transcribeWanted(domain string, external bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.trSettings[strings.ToLower(domain)]
	return st.Enabled && s.trEng.err == nil && (external || st.Scope == "all")
}

// trModelDir is where speech models are downloaded to.
func (s *Server) trModelDir() string { return filepath.Join(s.cfg.DataDir, "speech") }

// trMigrateModelDir moves the models downloaded before 0.10.1 (to
// data-dir/whisper) to trModelDir, leaving a link for settings that name
// the old path.
func (s *Server) trMigrateModelDir() {
	old := filepath.Join(s.cfg.DataDir, "whisper")
	if st, err := os.Lstat(old); err != nil || !st.IsDir() {
		return
	}
	if _, err := os.Stat(s.trModelDir()); err == nil {
		return
	}
	if err := os.Rename(old, s.trModelDir()); err != nil {
		s.log.Warn("speech models: could not move the old folder", "from", old, "err", err)
		return
	}
	_ = os.Symlink("speech", old)
	s.log.Info("speech models moved", "to", s.trModelDir())
}

func (s *Server) trCheckEngine() {
	cfg := s.sw.Config().Settings
	cmd, err := transcribe.FindCommand(cfg.TranscribeCommand)
	models := transcribe.InstalledModels(append([]string{s.trModelDir()}, transcribe.SystemModelDirs...), s.trModelDir())
	var def transcribe.Installed
	if cfg.TranscribeModel != "" {
		// The model set in ringqswitch.xml is the default.
		if st, e := os.Stat(cfg.TranscribeModel); e == nil && st.Size() > 0 {
			name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(cfg.TranscribeModel), "ggml-"), ".bin")
			def = transcribe.Installed{Name: name, Path: cfg.TranscribeModel, Size: st.Size()}
			if m := (trEngine{models: models}).model(name); m.Path == "" {
				models = append([]transcribe.Installed{def}, models...)
			}
		} else if err == nil {
			err = errors.New("the speech model " + cfg.TranscribeModel + " is not there")
		}
	} else if m, ok := transcribe.AutomaticModel(models); ok {
		def = m
	}
	if err == nil && def.Path == "" {
		err = errors.New("no speech model is installed")
	}
	s.mu.Lock()
	changed := (err == nil) != (s.trEng.err == nil) || def.Path != s.trEng.def.Path
	s.trEng = trEngine{command: cmd, models: models, def: def, err: err}
	s.mu.Unlock()
	if changed || err != nil && s.trAnyEnabled() {
		if err != nil {
			s.log.Warn("transcription: the speech recogniser is not ready (see Settings → Call transcription)", "err", err)
		} else {
			s.log.Info("transcription: speech recogniser ready", "command", cmd, "model", def.Path, "models", len(models))
		}
	}
}

// trNotify tells a domain's open pages that a transcript changed status.
// No call id: users may not see the call.
func (s *Server) trNotify(domain, status string) {
	s.hub.broadcastDomain(domain, map[string]string{"t": "transcript", "status": status})
}

func (s *Server) trAnyEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.trSettings {
		if st.Enabled {
			return true
		}
	}
	return false
}

func (s *Server) trLoadSettings() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('domain', d.domain_name, 'enabled', t.enabled, 'scope', t.scope,
		'language', t.language, 'model', t.model, 'voicemail', t.voicemail, 'live', t.live, 'live_model', t.live_model)
		from rq_transcription_settings t join v_domains d on d.domain_uuid = t.domain_uuid`)
	if err != nil {
		s.log.Warn("transcription: settings", "err", err)
		return
	}
	m := map[string]trSettings{}
	for _, r := range rows {
		m[strings.ToLower(r["domain"])] = trSettings{Enabled: truthy(r["enabled"]), Scope: r["scope"], Language: r["language"], Model: r["model"],
			Voicemail: truthy(r["voicemail"]), Live: truthy(r["live"]), LiveModel: r["live_model"]}
	}
	s.mu.Lock()
	s.trSettings = m
	s.mu.Unlock()
}

// ---- the worker

// trSubscribe subscribes to the calls and voicemail messages to transcribe
// (before the worker starts, so none is missed); nil without the tables.
func (s *Server) trSubscribe() *core.Subscription {
	if s.trReady() != nil {
		return nil
	}
	return s.sw.Events().Subscribe(1024, func(e *core.Event) bool {
		return e.Name == core.EvChannelHangupComplete && e.Get("variable_transcribe") == "true" ||
			e.Subclass() == "vm::maintenance" && e.Get("VM-Action") == "leave-message"
	})
}

func (s *Server) transcribeLoop(stop <-chan struct{}, sub *core.Subscription) {
	defer sub.Close()
	s.trMigrateModelDir()
	s.trLoadSettings()
	s.trCheckEngine()
	s.liveKick()
	s.sw.SetTranscription(s.transcribeWanted)
	defer s.sw.SetTranscription(nil)
	s.trRecover()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	refresh := time.NewTicker(time.Minute)
	defer refresh.Stop()
	for {
		select {
		case <-stop:
			return
		case e := <-sub.C:
			if e.Subclass() == "vm::maintenance" {
				s.trQueueVoicemail(e)
			} else {
				s.trQueueCall(e)
			}
		case <-tick.C:
			s.trDispatch()
		case <-refresh.C:
			s.trLoadSettings()
			s.trCheckEngine()
		}
	}
}

// trRecover queues again what a restart interrupted.
func (s *Server) trRecover() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.store.Pool().Exec(ctx, `update rq_transcripts set status = 'queued', updated_at = now() where status = 'processing'`); err != nil {
		s.log.Warn("transcription: recovering jobs", "err", err)
	}
}

// trQueueCall queues a call that just ended.
func (s *Server) trQueueCall(e *core.Event) {
	call := firstNonEmpty(e.Get("variable_uuid"), e.Get("Unique-ID"))
	dir, name := e.Get("variable_record_path"), e.Get("variable_record_name")
	if !reUUID.MatchString(call) || name == "" {
		return
	}
	if secs := atoi(e.Get("variable_billsec"), 0); secs < s.sw.Config().Settings.TranscribeMinSeconds {
		return // too short to say anything
	}
	path := filepath.Join(dir, name)
	if !inside(s.sw.Config().Settings.RecordingsDir, path) {
		s.log.Warn("transcription: the recording is outside the recordings directory", "path", path)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	domain := e.Get("variable_domain_uuid")
	if !reUUID.MatchString(domain) {
		if err := s.store.Pool().QueryRow(ctx, `select domain_uuid::text from v_domains where domain_name = $1`, e.Get("variable_domain_name")).Scan(&domain); err != nil {
			return
		}
	}
	if _, err := s.store.Pool().Exec(ctx, `insert into rq_transcripts (call_uuid, domain_uuid, status, direction, speaker_a, speaker_b,
		customer, recording) values ($1::uuid, $2::uuid, 'queued', $3, $4, $5, $6, $7) on conflict (call_uuid) do nothing`,
		call, domain, e.Get("variable_call_direction"), e.Get("variable_transcribe_a"), e.Get("variable_transcribe_b"),
		e.Get("variable_transcribe_customer"), path); err != nil {
		s.log.Warn("transcription: queueing the call", "uuid", call, "err", err)
		return
	}
	s.trNotify(e.Get("variable_domain_name"), "queued")
}

// trQueueVoicemail queues a voicemail message that was just left.
func (s *Server) trQueueVoicemail(e *core.Event) {
	msg, domainName, path := e.Get("VM-Message-UUID"), e.Get("VM-Domain"), e.Get("VM-File-Path")
	if !reUUID.MatchString(msg) || atoi(e.Get("VM-Message-Len"), 0) < 1 {
		return
	}
	s.mu.Lock()
	st, ready := s.trSettings[strings.ToLower(domainName)], s.trEng.err == nil
	s.mu.Unlock()
	if !st.Voicemail || !ready {
		return
	}
	if !inside(s.sw.Config().Settings.VoicemailDir, path) {
		s.log.Warn("transcription: the voicemail message is outside the voicemail directory", "path", path)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var domain string
	if err := s.store.Pool().QueryRow(ctx, `select domain_uuid::text from v_domains where lower(domain_name) = lower($1)`, domainName).Scan(&domain); err != nil {
		return
	}
	if err := s.trQueue(ctx, msg, domain, "voicemail", partyName(e.Get("VM-Caller-ID-Name"), e.Get("VM-Caller-ID-Number")),
		"Voicemail "+e.Get("VM-User"), path, false); err != nil {
		s.log.Warn("transcription: queueing the voicemail message", "uuid", msg, "err", err)
		return
	}
	s.trNotify(domainName, "queued")
}

// trQueue queues a voicemail message (again, with again).
func (s *Server) trQueue(ctx context.Context, id, domain, kind, a, b, path string, again bool) error {
	conflict := "do nothing"
	if again {
		conflict = `do update set status = 'queued', error = null, recording = excluded.recording, updated_at = now()
			where rq_transcripts.status not in ('queued', 'processing')`
	}
	_, err := s.store.Pool().Exec(ctx, `insert into rq_transcripts (call_uuid, domain_uuid, status, kind, direction, speaker_a, speaker_b,
		customer, recording) values ($1::uuid, $2::uuid, 'queued', $3, $3, $4, $5, '', $6) on conflict (call_uuid) `+conflict,
		id, domain, kind, a, b, path)
	return err
}

type trJob struct {
	call, recording, customer, domain, language, model, kind string
}

func (s *Server) trDispatch() {
	s.mu.Lock()
	free := max(1, s.sw.Config().Settings.TranscribeWorkers) - s.trBusy
	ready := s.trEng.err == nil
	eng := s.trEng
	s.mu.Unlock()
	if free <= 0 || !ready {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var j trJob
	err := s.store.Pool().QueryRow(ctx, `update rq_transcripts t set status = 'processing', updated_at = now() from v_domains d
		where t.call_uuid = (select call_uuid from rq_transcripts where status = 'queued' order by created_at limit 1 for update skip locked)
		and d.domain_uuid = t.domain_uuid
		returning t.call_uuid::text, coalesce(t.recording, ''), coalesce(t.customer, ''), d.domain_name,
		coalesce((select language from rq_transcription_settings x where x.domain_uuid = t.domain_uuid), 'auto'),
		coalesce((select model from rq_transcription_settings x where x.domain_uuid = t.domain_uuid), ''), t.kind`).
		Scan(&j.call, &j.recording, &j.customer, &j.domain, &j.language, &j.model, &j.kind)
	if err != nil {
		return // nothing queued
	}
	s.mu.Lock()
	s.trBusy++
	s.mu.Unlock()
	s.trNotify(j.domain, "processing")
	go func() {
		defer func() {
			s.mu.Lock()
			s.trBusy--
			s.mu.Unlock()
		}()
		s.trRun(j, eng)
	}()
}

func (s *Server) trRun(j trJob, eng trEngine) {
	started := time.Now()
	limit := 10 * time.Minute
	st, statErr := os.Stat(j.recording)
	if statErr == nil {
		limit += 4 * time.Duration(st.Size()/32000) * time.Second // stereo, 8 kHz, 16 bit
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	model := eng.model(j.model)
	res, err := transcribe.Transcribe(ctx, j.recording, transcribe.Options{
		Recognizer: &transcribe.SpeechCLI{Command: eng.command, Model: model.Path, Threads: s.sw.Config().Settings.TranscribeThreads},
		Language:   j.language, Customer: j.customer,
	})
	status, reason := "done", ""
	switch {
	case statErr != nil:
		status, reason = "failed", "the call's recording is gone ("+firstNonEmpty(j.recording, "no file")+")"
		res = &transcribe.Result{}
	case errors.Is(err, transcribe.ErrNoSpeech):
		status = "empty"
	case err != nil:
		status, reason = "failed", err.Error()
		res = &transcribe.Result{}
	}
	segs, _ := json.Marshal(nonNil(res.Segments))
	spk, _ := json.Marshal(nonNil(res.Speakers))
	var custScore *float64
	custLabel := ""
	for _, sp := range res.Speakers {
		if res.Customer != "" && sp.Speaker == res.Customer {
			v := sp.Sentiment
			custScore, custLabel = &v, sp.Label
		}
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if _, err := s.store.Pool().Exec(ctx2, `update rq_transcripts set status = $2, language = $3, duration = $4, stereo = $5,
		segments = $6::jsonb, speakers = $7::jsonb, sentiment = $8, sentiment_label = $9, customer_sentiment = $10,
		customer_label = nullif($11, ''), engine = $12, error = nullif($13, ''), updated_at = now() where call_uuid = $1::uuid`,
		j.call, status, res.Language, res.Duration, res.Stereo, string(segs), string(spk), res.Sentiment, res.Label,
		custScore, custLabel, "rq-speech "+model.Name, reason); err != nil {
		s.log.Warn("transcription: saving", "uuid", j.call, "err", err)
		return
	}
	if j.kind == "voicemail" && status != "failed" && s.store.HasColumn("v_voicemail_messages", "message_transcription") {
		// Where RingQ keeps a message's text (the RingQ portal shows it).
		var text []string
		for _, sg := range res.Segments {
			text = append(text, sg.Text)
		}
		if _, err := s.store.Pool().Exec(ctx2, `update v_voicemail_messages set message_transcription = nullif($2, '')
			where voicemail_message_uuid = $1::uuid`, j.call, strings.Join(text, " ")); err != nil {
			s.log.Warn("transcription: saving the voicemail text", "uuid", j.call, "err", err)
		}
	}
	if reason != "" {
		s.log.Warn("transcription failed", "uuid", j.call, "err", reason)
	} else {
		s.log.Info("call transcribed", "uuid", j.call, "model", model.Name, "status", status, "segments", len(res.Segments), "sentiment", res.Label,
			"customer", custLabel, "audio", strconv.FormatFloat(res.Duration, 'f', 0, 64)+"s", "took", time.Since(started).Round(time.Second).String())
	}
	s.trNotify(j.domain, status)
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// ---- the API

func (s *Server) transcriptionAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	if err := s.trReady(); err != nil {
		return err
	}
	if len(parts) == 2 && parts[0] == "models" {
		return s.trModelAPI(w, r, p, parts[1])
	}
	if len(parts) > 0 {
		return notFound("page")
	}
	switch r.Method {
	case http.MethodGet:
		st := trSettings{Scope: "external", Language: "auto", Voicemail: true}
		_ = s.store.Pool().QueryRow(ctx, `select enabled, scope, language, model, voicemail, live, live_model from rq_transcription_settings
			where domain_uuid = $1::uuid`, dom.uuid).Scan(&st.Enabled, &st.Scope, &st.Language, &st.Model, &st.Voicemail, &st.Live, &st.LiveModel)
		s.trCheckEngine()
		s.mu.Lock()
		eng := s.trEng
		downloads := map[string]trDownload{}
		for k, v := range s.trDownloads {
			downloads[k] = *v
		}
		s.mu.Unlock()
		engine := map[string]any{"ok": eng.err == nil, "command": eng.command, "model": eng.def.Path, "error": ""}
		if eng.err != nil {
			engine["error"] = eng.err.Error()
		}
		counts := map[string]int{}
		rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('status', status, 'n', count(*)) from rq_transcripts
			where domain_uuid = $1::uuid and (status in ('queued', 'processing') or updated_at > now() - interval '1 day') group by status`, dom.uuid)
		if err != nil {
			return err
		}
		for _, r := range rows {
			counts[r["status"]] = atoi(r["n"], 0)
		}
		cfg := s.sw.Config().Settings
		liveServer := transcribe.FindServerCommand(eng.command) != "" || cfg.TranscribeLiveURL != ""
		// Whether the domain's live model is loaded in a running the live recogniser.
		liveRunning, liveErr := cfg.TranscribeLiveURL != "", ""
		if lm := eng.liveModel(st.LiveModel).Path; !liveRunning && lm != "" {
			s.mu.Lock()
			ls := s.liveServers[lm]
			liveErr = s.liveErr[lm]
			s.mu.Unlock()
			liveRunning = ls != nil && ls.Running()
		}
		writeJSON(w, map[string]any{"settings": st, "engine": engine, "counts": counts, "models": s.trModelList(eng, downloads),
			"default_model": eng.def.Name, "model_in_use": eng.model(st.Model).Name, "can_manage_models": s.serverAccess(p),
			"live_model_in_use": eng.liveModel(st.LiveModel).Name, "live_default_model": eng.liveModel("").Name, "live_server": liveServer,
			"live_running": liveRunning, "live_error": liveErr,
			"model_dir": s.trModelDir(), "threads": cfg.TranscribeThreads, "workers": max(1, cfg.TranscribeWorkers),
			"min_seconds": cfg.TranscribeMinSeconds})
		return nil
	case http.MethodPut:
		var b trSettings
		if err := decodeBody(r, &b); err != nil {
			return err
		}
		if b.Scope != "all" {
			b.Scope = "external"
		}
		b.Language = strings.ToLower(strings.TrimSpace(firstNonEmpty(b.Language, "auto")))
		if !reLanguage.MatchString(b.Language) {
			return badRequest("language: auto or a language code (en, ms, ta, zh...)")
		}
		b.Model, b.LiveModel = strings.TrimSpace(b.Model), strings.TrimSpace(b.LiveModel)
		for _, m := range []string{b.Model, b.LiveModel} {
			if m == "" {
				continue
			}
			s.mu.Lock()
			found := s.trEng.model(m).Name == m
			s.mu.Unlock()
			if !found {
				return badRequest("the speech model %s is not installed: download it first", m)
			}
		}
		if _, err := s.store.Pool().Exec(ctx, `insert into rq_transcription_settings (domain_uuid, enabled, scope, language, model, voicemail,
			live, live_model, updated_at) values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, now()) on conflict (domain_uuid) do update
			set enabled = excluded.enabled, scope = excluded.scope, language = excluded.language, model = excluded.model,
			voicemail = excluded.voicemail, live = excluded.live, live_model = excluded.live_model, updated_at = now()`,
			dom.uuid, b.Enabled, b.Scope, b.Language, b.Model, b.Voicemail, b.Live, b.LiveModel); err != nil {
			return err
		}
		s.trLoadSettings()
		s.liveKick()
		s.sw.RecordEvent("settings", dom.name+": call transcription "+map[bool]string{true: "on", false: "off"}[b.Enabled]+
			" ("+b.Scope+", "+b.Language+", model "+firstNonEmpty(b.Model, "default")+"), voicemail "+map[bool]string{true: "on", false: "off"}[b.Voicemail]+
			", live "+map[bool]string{true: "on", false: "off"}[b.Live])
		writeJSON(w, b)
		return nil
	}
	return errMethod
}

// trModelList is the catalog of speech models, with what is installed or
// downloading, then installed models outside the catalog.
func (s *Server) trModelList(eng trEngine, downloads map[string]trDownload) []map[string]any {
	installed := map[string]transcribe.Installed{}
	for _, m := range eng.models {
		installed[m.Name] = m
	}
	var out []map[string]any
	add := func(name, title, note string, size int64) {
		row := map[string]any{"name": name, "title": title, "note": note, "size": size, "installed": false}
		if m, ok := installed[name]; ok {
			row["installed"], row["path"], row["size"], row["removable"] = true, m.Path, m.Size, m.Removable
			delete(installed, name)
		}
		if d, ok := downloads[name]; ok {
			row["download"] = d
		}
		out = append(out, row)
	}
	for _, m := range transcribe.Models {
		add(m.Name, m.Title, m.Note, m.Size)
	}
	for _, m := range eng.models {
		if _, ok := installed[m.Name]; ok {
			add(m.Name, m.Name, "", m.Size)
		}
	}
	return out
}

// trModelAPI downloads (POST) or deletes (DELETE) a speech model; the
// models serve the whole server.
func (s *Server) trModelAPI(w http.ResponseWriter, r *http.Request, p *principal, name string) error {
	if !s.serverAccess(p) {
		return errForbidden
	}
	switch r.Method {
	case http.MethodPost:
		if _, ok := transcribe.LookupModel(name); !ok {
			return notFound("model")
		}
		s.mu.Lock()
		if d := s.trDownloads[name]; d != nil && d.Error == "" {
			s.mu.Unlock()
			return &apiError{http.StatusConflict, "the model is already downloading"}
		}
		if m := s.trEng.model(name); m.Name == name {
			s.mu.Unlock()
			return badRequest("the model is already installed")
		}
		d := &trDownload{}
		s.trDownloads[name] = d
		s.mu.Unlock()
		go s.trDownload(name, d, p.key())
		writeJSON(w, map[string]string{"status": "downloading"})
		return nil
	case http.MethodDelete:
		s.mu.Lock()
		m := s.trEng.model(name)
		delete(s.trDownloads, name) // a failed download's message
		s.mu.Unlock()
		if m.Name != name {
			writeJSON(w, map[string]string{"status": "gone"})
			return nil
		}
		if !m.Removable {
			return badRequest("this model was installed with install-transcription.sh; remove it on the server: sudo rm %s", m.Path)
		}
		if err := os.Remove(m.Path); err != nil {
			return err
		}
		s.trCheckEngine()
		s.sw.RecordEvent("settings", "speech model "+name+" deleted by "+p.key())
		writeJSON(w, map[string]string{"status": "deleted"})
		return nil
	}
	return errMethod
}

func (s *Server) trDownload(name string, d *trDownload, who string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	base := firstNonEmpty(s.sw.Config().Settings.TranscribeModelURL, transcribe.ModelURL)
	s.log.Info("transcription: downloading a speech model", "model", name, "from", base, "by", who)
	started := time.Now()
	err := transcribe.DownloadModel(ctx, base, name, s.trModelDir(), func(done, total int64) {
		s.mu.Lock()
		d.Done, d.Total = done, total
		s.mu.Unlock()
	})
	s.mu.Lock()
	if err != nil {
		d.Error = err.Error()
	} else {
		delete(s.trDownloads, name)
	}
	s.mu.Unlock()
	if err != nil {
		s.log.Warn("transcription: downloading a speech model failed", "model", name, "err", err)
		return
	}
	s.trCheckEngine()
	s.log.Info("transcription: speech model downloaded", "model", name, "took", time.Since(started).Round(time.Second).String())
	s.sw.RecordEvent("settings", "speech model "+name+" downloaded by "+who)
}

// trCall is the call record a transcript belongs to, if the asker may see it.
func (s *Server) trCall(ctx context.Context, dom domainRow, id string, own ownScope) (map[string]string, error) {
	if !s.store.HasTable("v_xml_cdr") {
		if own.on {
			return nil, notFound("call")
		}
		return map[string]string{}, nil
	}
	args := []any{id, dom.uuid}
	cond := "true"
	if own.on {
		cond = s.cdrOwnCondition(own, &args)
	}
	var fields []string
	for _, c := range []string{"direction", "caller_id_name", "caller_id_number", "caller_destination", "destination_number",
		"start_stamp", "billsec", "record_path", "record_name", "cc_queue", "cc_agent"} {
		if s.store.HasColumn("v_xml_cdr", c) {
			fields = append(fields, "'"+c+"', "+c)
		}
	}
	rows, err := s.store.JSONRows(ctx, `select jsonb_build_object(`+strings.Join(fields, ", ")+`) from v_xml_cdr
		where xml_cdr_uuid = $1::uuid and domain_uuid = $2::uuid and `+cond, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		if own.on {
			return nil, notFound("call")
		}
		return map[string]string{}, nil
	}
	return rows[0], nil
}

func (s *Server) transcriptsAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, id string, own ownScope) error {
	if err := s.trReady(); err != nil {
		return err
	}
	if !reUUID.MatchString(id) {
		return notFound("transcript")
	}
	call, err := s.trCall(ctx, dom, id, own)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		var raw []byte
		err := s.store.Pool().QueryRow(ctx, `select to_jsonb(t) - 'recording' from rq_transcripts t where call_uuid = $1::uuid and domain_uuid = $2::uuid`,
			id, dom.uuid).Scan(&raw)
		if err != nil {
			return notFound("transcript")
		}
		delete(call, "record_path")
		writeJSON(w, map[string]any{"call": call, "transcript": json.RawMessage(raw)})
		return nil
	case http.MethodPost:
		// Transcribe a recorded call now (or again).
		if !p.supervisor() {
			return errForbidden
		}
		if call["record_name"] == "" {
			return badRequest("this call has no recording")
		}
		path := filepath.Join(call["record_path"], call["record_name"])
		if !inside(s.sw.Config().Settings.RecordingsDir, path) {
			return badRequest("the recording is outside the recordings directory")
		}
		if _, err := os.Stat(path); err != nil {
			return badRequest("the recording file is gone")
		}
		customer := map[string]string{"inbound": "A", "outbound": "B"}[call["direction"]]
		a := call["caller_id_number"]
		if n := call["caller_id_name"]; n != "" && n != a && !strings.Contains(n, a) {
			a = n + " (" + a + ")"
		} else if n != "" && a == "" {
			a = n
		}
		b := firstNonEmpty(call["cc_agent"], call["caller_destination"], call["destination_number"])
		if _, err := s.store.Pool().Exec(ctx, `insert into rq_transcripts (call_uuid, domain_uuid, status, direction, speaker_a, speaker_b,
			customer, recording) values ($1::uuid, $2::uuid, 'queued', $3, $4, $5, $6, $7) on conflict (call_uuid) do update
			set status = 'queued', error = null, recording = excluded.recording, updated_at = now()
			where rq_transcripts.status not in ('queued', 'processing')`, id, dom.uuid, call["direction"], a, b, customer, path); err != nil {
			return err
		}
		s.trNotify(dom.name, "queued")
		writeJSON(w, map[string]string{"status": "queued"})
		return nil
	}
	return errMethod
}

// trAnnotate adds each call's transcript status and sentiment (the
// customer's when there is one) to report rows; key is the call's uuid.
func (s *Server) trAnnotate(ctx context.Context, rows []map[string]string, key string) {
	if s.trReady() != nil || len(rows) == 0 {
		return
	}
	var ids []string
	for _, r := range rows {
		if reUUID.MatchString(r[key]) {
			ids = append(ids, strings.ToLower(r[key]))
		}
	}
	if len(ids) == 0 {
		return
	}
	found, err := s.store.JSONRows(ctx, `select jsonb_build_object('id', call_uuid, 'status', status,
		'label', coalesce(customer_label, sentiment_label, ''), 'score', coalesce(customer_sentiment, sentiment))
		from rq_transcripts where call_uuid::text = any($1)`, ids)
	if err != nil {
		s.log.Warn("transcription: report rows", "err", err)
		return
	}
	by := map[string]map[string]string{}
	for _, f := range found {
		by[strings.ToLower(f["id"])] = f
	}
	for _, r := range rows {
		if f := by[strings.ToLower(r[key])]; f != nil {
			r["transcript"], r["sentiment"], r["sentiment_score"] = f["status"], f["label"], f["score"]
		}
	}
}

// ---- voicemail messages

// trAnnotateVoicemail adds each message's transcript status, text and
// sentiment to the voicemail list (or the text RingQ keeps, if any).
func (s *Server) trAnnotateVoicemail(ctx context.Context, rows []map[string]string) {
	for _, r := range rows {
		if t := r["message_transcription"]; t != "" {
			r["transcript"], r["transcript_text"] = "done", t
		}
		delete(r, "message_transcription")
	}
	if s.trReady() != nil || len(rows) == 0 {
		return
	}
	var ids []string
	for _, r := range rows {
		if reUUID.MatchString(r["uuid"]) {
			ids = append(ids, strings.ToLower(r["uuid"]))
		}
	}
	found, err := s.store.JSONRows(ctx, `select jsonb_build_object('id', call_uuid, 'status', status, 'label', coalesce(sentiment_label, ''),
		'score', sentiment, 'text', coalesce((select string_agg(x->>'text', ' ') from jsonb_array_elements(segments) x), ''))
		from rq_transcripts where call_uuid::text = any($1)`, ids)
	if err != nil {
		s.log.Warn("transcription: voicemail rows", "err", err)
		return
	}
	by := map[string]map[string]string{}
	for _, f := range found {
		by[strings.ToLower(f["id"])] = f
	}
	for _, r := range rows {
		if f := by[strings.ToLower(r["uuid"])]; f != nil {
			r["transcript"], r["sentiment"], r["sentiment_score"] = f["status"], f["label"], f["score"]
			if f["text"] != "" {
				r["transcript_text"] = f["text"]
			}
		}
	}
}

// trVoicemail serves a message's transcript (GET) or transcribes it (again)
// (POST).
func (s *Server) trVoicemail(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow, id, box, path string) error {
	if err := s.trReady(); err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		var raw []byte
		if err := s.store.Pool().QueryRow(ctx, `select to_jsonb(t) - 'recording' from rq_transcripts t where call_uuid = $1::uuid and domain_uuid = $2::uuid`,
			id, dom.uuid).Scan(&raw); err != nil {
			return notFound("transcript")
		}
		writeJSON(w, map[string]any{"transcript": json.RawMessage(raw)})
		return nil
	case http.MethodPost:
		s.mu.Lock()
		engErr := s.trEng.err
		s.mu.Unlock()
		if engErr != nil {
			return badRequest("the speech recogniser is not ready: %s", engErr.Error())
		}
		if _, err := os.Stat(path); err != nil {
			return badRequest("the message's recording is gone")
		}
		var name, number string
		_ = s.store.Pool().QueryRow(ctx, `select coalesce(caller_id_name, ''), coalesce(caller_id_number, '') from v_voicemail_messages
			where voicemail_message_uuid = $1::uuid`, id).Scan(&name, &number)
		if err := s.trQueue(ctx, id, dom.uuid, "voicemail", partyName(name, number), "Voicemail "+box, path, true); err != nil {
			return err
		}
		s.trNotify(dom.name, "queued")
		writeJSON(w, map[string]string{"status": "queued"})
		return nil
	}
	return errMethod
}
