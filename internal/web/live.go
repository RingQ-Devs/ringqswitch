package web

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/transcribe"
)

// Live transcription: while an extension's call goes on, what both parties
// say is recognised as they say it and sent to that extension's open web
// pages (the dialer's live panel), split by speaker. Settings → Call
// transcription → "Live transcription during calls" turns it on for a
// domain. Nothing is stored. WebSocket messages, to the extension's tabs:
//
//	live.start {id, me: "a"|"b", sides: {a: {name}, b: {name}}, started}
//	live.speak   {id, side, seq, on}    someone started (or stopped) speaking
//	live.partial {id, side, seq, at, text}  what they said so far, while they speak
//	live.text    {id, side, seq, at, text}  what they said (at: ms from the start)
//	live.end     {id}
//
// Side "a" is the caller's leg, "b" the leg that answered; "me" says which
// one is the extension the message goes to.

type liveSession struct {
	id      string
	domain  string
	started time.Time
	keys    []string          // who sees it (principal keys)
	me      map[string]string // principal key → its side
	sides   map[string]map[string]string
	stops   []func()

	lang  *liveLang
	mu    sync.Mutex
	texts []map[string]any // what was said so far, for a page opened during the call
}

// liveLang is the language a call is recognised in: the domain's ("Language
// spoken"), or, set to Detect automatically, the one a confident detection
// on a real sentence finds, kept for the rest of the call (both parties).
// Detecting on every short phrase made whisper hear French in English
// speech and noise.
type liveLang struct {
	mu     sync.Mutex
	fixed  string
	locked string
	votes  map[string]float64
}

func newLiveLang(setting string) *liveLang {
	l := &liveLang{votes: map[string]float64{}}
	if setting != "" && setting != "auto" {
		l.fixed = setting
	}
	return l
}

// get is the language to recognise in ("auto" until it is known).
func (l *liveLang) get() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return firstNonEmpty(l.fixed, l.locked, "auto")
}

// heard notes a detection on secs seconds of speech; it reports whether
// the text recognised with it can be shown (the detection was not a guess).
func (l *liveLang) heard(lang string, prob, secs float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fixed != "" || l.locked != "" || lang == "" {
		return true
	}
	// One clear sentence decides; weaker detections add up.
	l.votes[lang] += prob * min(secs, 4)
	if prob >= 0.75 && secs >= 1.2 || l.votes[lang] >= 3 {
		l.locked = lang
	}
	return prob >= 0.5
}

// startMsg is the live.start message for one of the session's extensions.
func (ls *liveSession) startMsg(key string) map[string]any {
	return map[string]any{"t": "live.start", "id": ls.id, "me": ls.me[key], "sides": ls.sides, "started": ls.started.UnixMilli()}
}

// liveReplay sends a page that just opened the calls of its extension
// being written down now, with what was said so far.
func (s *Server) liveReplay(c *wsClient) {
	key := strings.ToLower(c.p.key())
	s.mu.Lock()
	var mine []*liveSession
	for _, ls := range s.lives {
		if _, ok := ls.me[key]; ok {
			mine = append(mine, ls)
		}
	}
	s.mu.Unlock()
	for _, ls := range mine {
		msgs := []map[string]any{ls.startMsg(key)}
		ls.mu.Lock()
		msgs = append(msgs, ls.texts...)
		ls.mu.Unlock()
		for _, m := range msgs {
			if data, err := json.Marshal(m); err == nil {
				c.write(data)
			}
		}
	}
}

// liveSubscribe subscribes to bridges and hang-ups (before the worker
// starts, so none is missed).
func (s *Server) liveSubscribe() *core.Subscription {
	if s.trReady() != nil {
		return nil
	}
	return s.sw.Events().Subscribe(256, func(e *core.Event) bool {
		return e.Name == core.EvChannelBridge || e.Name == core.EvChannelUnbridge || e.Name == core.EvChannelHangupComplete
	})
}

func (s *Server) liveLoop(stop <-chan struct{}, sub *core.Subscription) {
	defer sub.Close()
	idle := time.NewTicker(time.Minute)
	defer idle.Stop()
	defer s.liveStopServers()
	for {
		select {
		case <-stop:
			return
		case e := <-sub.C:
			switch e.Name {
			case core.EvChannelBridge:
				s.liveStart(e.Get("Bridge-A-Unique-ID"), e.Get("Bridge-B-Unique-ID"))
			case core.EvChannelUnbridge:
				s.liveEnd(e.Get("Bridge-A-Unique-ID") + "/" + e.Get("Bridge-B-Unique-ID"))
			case core.EvChannelHangupComplete:
				s.liveEndLeg(e.Get("Unique-ID"))
			}
		case <-idle.C:
			s.liveWarm()
		case <-s.liveWake:
			s.liveWarm()
		}
	}
}

// legExtension is the extension a call leg belongs to: the one it rings, or
// the one whose phone placed it.
func legExtension(v map[string]string) string {
	if u := v["dialed_user"]; u != "" {
		return u
	}
	if v["sip_authorized"] == "true" && v["user_name"] != "" {
		return v["user_name"]
	}
	return ""
}

func (s *Server) liveStart(aID, bID string) {
	a, b := s.sw.Channel(aID), s.sw.Channel(bID)
	if a == nil || b == nil {
		return
	}
	av, bv := a.Vars(), b.Vars()
	domain := strings.ToLower(firstNonEmpty(av["domain_name"], bv["domain_name"]))
	s.mu.Lock()
	st := s.trSettings[domain]
	eng := s.trEng
	_, already := s.lives[aID+"/"+bID]
	s.mu.Unlock()
	if !st.Live || eng.err != nil || already {
		return
	}
	type sub struct{ key, me string }
	var subs []sub
	for _, l := range []struct{ ext, side string }{{legExtension(av), "a"}, {legExtension(bv), "b"}} {
		if key := l.ext + "@" + domain; l.ext != "" && s.hub.online(key) {
			subs = append(subs, sub{key, l.side})
		}
	}
	if len(subs) == 0 {
		return // nobody is watching
	}
	rec := s.liveRecognizer(st, eng)
	ls := &liveSession{id: aID + "/" + bID, domain: domain, started: time.Now(), me: map[string]string{}, lang: newLiveLang(st.Language), sides: map[string]map[string]string{
		"a": {"name": partyName(av["caller_id_name"], av["caller_id_number"])},
		"b": {"name": partyName(bv["callee_id_name"], firstNonEmpty(bv["callee_id_number"], bv["dialed_user"], bv["destination_number"]))},
	}}
	for _, x := range subs {
		ls.keys = append(ls.keys, x.key)
		ls.me[strings.ToLower(x.key)] = x.me
	}
	for _, x := range subs {
		s.hub.sendTo([]string{x.key}, ls.startMsg(strings.ToLower(x.key)))
	}
	var wg sync.WaitGroup
	for side, id := range map[string]string{"a": aID, "b": bID} {
		frames, stop, err := s.sw.Hear(id)
		if err != nil {
			continue
		}
		ls.stops = append(ls.stops, stop)
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.liveSide(ls, side, frames, rec)
		}()
	}
	s.mu.Lock()
	if s.lives == nil {
		s.lives = map[string]*liveSession{}
	}
	s.lives[ls.id] = ls
	s.mu.Unlock()
	go func() {
		wg.Wait()
		s.hub.sendTo(ls.keys, map[string]any{"t": "live.end", "id": ls.id})
	}()
	s.log.Info("live transcription", "call", aID, "to", strings.Join(ls.keys, ", "))
}

// liveSide cuts one party's audio into utterances and recognises them in
// order; it ends when the party's audio does (and its last words are sent).
// While the party speaks, what they said so far is recognised about every
// second too (live.partial), when the recogniser has nothing else to do.
func (s *Server) liveSide(ls *liveSession, side string, frames <-chan []int16, rec transcribe.LiveRecognizer) {
	type utt struct {
		seq   int
		at    int64 // ms from the start of the session
		pcm   []int16
		final bool
	}
	finals := make(chan utt, 32)
	partials := make(chan utt, 1) // only the latest is worth recognising
	busy, canPartial := rec.(interface{ InFlight() int })
	var pmu sync.Mutex
	var pcancel context.CancelFunc // the partial being recognised, and its utterance
	pseq := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		finalSeq, lastPartial := 0, ""
		for {
			var u utt
			var ok bool
			select { // what was said first
			case u, ok = <-finals:
			default:
				select {
				case u, ok = <-finals:
				case u, ok = <-partials:
				}
			}
			if !ok {
				return
			}
			if !u.final {
				if u.seq <= finalSeq {
					continue // its utterance was finished meanwhile
				}
				lang := ls.lang.get()
				if lang == "auto" {
					continue // words so far only once the call's language is known
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				pmu.Lock()
				pcancel, pseq = cancel, u.seq
				pmu.Unlock()
				res, err := rec.RecognizeLive(ctx, u.pcm, 8000, lang)
				pmu.Lock()
				pcancel = nil
				pmu.Unlock()
				stale := ctx.Err() != nil
				cancel()
				if err == nil && !stale && res.Text != "" && res.Text != lastPartial {
					lastPartial = res.Text
					s.hub.sendTo(ls.keys, map[string]any{"t": "live.partial", "id": ls.id, "side": side, "seq": u.seq, "at": u.at, "text": res.Text})
				}
				continue
			}
			finalSeq, lastPartial = u.seq, ""
			lang := ls.lang.get()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			res, err := rec.RecognizeLive(ctx, u.pcm, 8000, lang)
			cancel()
			if err != nil {
				s.log.Warn("live transcription", "call", ls.id, "err", err)
			}
			show := true
			if lang == "auto" && err == nil {
				// The utterance's speech: its length less the pre-roll and tail.
				show = ls.lang.heard(res.Language, res.LangProb, max(0, float64(len(u.pcm))/8000-0.6))
			}
			if res.Text == "" || !show {
				s.hub.sendTo(ls.keys, map[string]any{"t": "live.speak", "id": ls.id, "side": side, "seq": u.seq, "on": false})
				continue
			}
			msg := map[string]any{"t": "live.text", "id": ls.id, "side": side, "seq": u.seq, "at": u.at, "text": res.Text}
			ls.mu.Lock()
			if len(ls.texts) < 2000 {
				ls.texts = append(ls.texts, msg)
			}
			ls.mu.Unlock()
			s.hub.sendTo(ls.keys, msg)
		}
	}()
	seg := transcribe.NewSegmenter()
	seq, asked := 0, 0 // asked: the utterance's length when its text was last asked for
	handle := func(evs []transcribe.LiveEvent) {
		for _, e := range evs {
			at := e.From * 1000 / 8000 // ms; the two parties' audio starts together
			if e.Start {
				seq++
				asked = 0
				s.hub.sendTo(ls.keys, map[string]any{"t": "live.speak", "id": ls.id, "side": side, "seq": seq, "on": true, "at": at})
				continue
			}
			pmu.Lock()
			if pcancel != nil && pseq == seq {
				pcancel() // the whole utterance is coming: its partial text is not needed
			}
			pmu.Unlock()
			select {
			case finals <- utt{seq: seq, at: at, pcm: e.Utterance, final: true}:
			default: // the recogniser is far behind: skip rather than lag
				s.hub.sendTo(ls.keys, map[string]any{"t": "live.speak", "id": ls.id, "side": side, "seq": seq, "on": false})
			}
		}
	}
	for f := range frames {
		handle(seg.Feed(f))
		// A second more of speech: its text so far, if the recogniser is free.
		if n := seg.Spoken(); canPartial && n >= 8000 && n-asked >= 8000 && busy.InFlight() == 0 && len(partials) == 0 && len(finals) == 0 {
			if pcm, from, ok := seg.Current(); ok {
				asked = n
				select {
				case partials <- utt{seq: seq, at: from * 1000 / 8000, pcm: pcm}:
				default:
				}
			}
		}
	}
	handle(seg.Flush())
	close(finals)
	<-done
}

// liveEnd ends a session (the call was unbridged); liveEndLeg ends those of
// a leg that hung up.
func (s *Server) liveEnd(id string) {
	s.mu.Lock()
	ls := s.lives[id]
	delete(s.lives, id)
	s.mu.Unlock()
	if ls != nil {
		for _, stop := range ls.stops {
			stop()
		}
	}
}

func (s *Server) liveEndLeg(uuid string) {
	if uuid == "" {
		return
	}
	s.mu.Lock()
	var ids []string
	for id := range s.lives {
		if strings.HasPrefix(id, uuid+"/") || strings.HasSuffix(id, "/"+uuid) {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.liveEnd(id)
	}
}

// liveRecognizer picks the recogniser for a domain's live transcripts: a
// the live recogniser with its live model (the fastest installed by default),
// or the recogniser when the live recogniser is not installed.
func (s *Server) liveRecognizer(st trSettings, eng trEngine) transcribe.LiveRecognizer {
	cfg := s.sw.Config().Settings
	if url := cfg.TranscribeLiveURL; url != "" {
		return &transcribe.LiveServer{URL: url}
	}
	model := eng.liveModel(st.LiveModel)
	server := transcribe.FindServerCommand(eng.command)
	if server == "" {
		return &transcribe.LiveCLI{CLI: &transcribe.SpeechCLI{Command: eng.command, Model: model.Path, Threads: cfg.TranscribeThreads}}
	}
	return s.liveServerFor(server, model.Path, cfg.TranscribeThreads)
}

// liveServerFor is the live recogniser of a model (one per model, shared by
// the calls and domains using it).
func (s *Server) liveServerFor(command, model string, threads int) *transcribe.LiveServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveServers == nil {
		s.liveServers = map[string]*transcribe.LiveServer{}
	}
	ls := s.liveServers[model]
	if ls == nil {
		ls = &transcribe.LiveServer{Command: command, Model: model, Threads: threads}
		s.liveServers[model] = ls
	}
	return ls
}

// liveWarm keeps a live recogniser running, its model loaded, for each model
// a domain with live transcription on uses (so the first words of a call
// are not kept waiting), and stops the others once they are idle.
func (s *Server) liveWarm() {
	cfg := s.sw.Config().Settings
	s.mu.Lock()
	eng := s.trEng
	wanted := map[string]bool{}
	if eng.err == nil {
		for _, st := range s.trSettings {
			if st.Live {
				wanted[eng.liveModel(st.LiveModel).Path] = true
			}
		}
	}
	s.mu.Unlock()
	server := ""
	if eng.err == nil && cfg.TranscribeLiveURL == "" {
		server = transcribe.FindServerCommand(eng.command)
	}
	if server == "" {
		wanted = nil
	}
	for model := range wanted {
		ls := s.liveServerFor(server, model, cfg.TranscribeThreads)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			err := ls.Warm(ctx)
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			s.mu.Lock()
			if s.liveErr == nil {
				s.liveErr = map[string]string{}
			}
			changed := s.liveErr[model] != msg
			s.liveErr[model] = msg
			s.mu.Unlock()
			if changed && err != nil {
				s.log.Warn("live transcription: live recogniser", "model", model, "err", err)
			} else if changed {
				s.log.Info("live transcription: live recogniser ready", "model", model)
			}
		}()
	}
	s.mu.Lock()
	for model, ls := range s.liveServers {
		if !wanted[model] && ls.Running() && ls.InFlight() == 0 && ls.IdleFor() > time.Minute {
			go ls.Stop()
		}
	}
	s.mu.Unlock()
}

func (s *Server) liveStopServers() {
	s.mu.Lock()
	servers := s.liveServers
	s.liveServers = nil
	s.mu.Unlock()
	for _, ls := range servers {
		ls.Stop()
	}
}

// liveKick asks the live loop to check its live recognisers now (settings
// changed).
func (s *Server) liveKick() {
	select {
	case s.liveWake <- struct{}{}:
	default:
	}
}

// liveModel is the model live transcripts use: the one chosen, else the
// fastest good one installed (base, then tiny, small...).
func (e trEngine) liveModel(name string) transcribe.Installed {
	if name != "" {
		if m := e.model(name); m.Name == name {
			return m
		}
	}
	for _, n := range []string{"base", "base.en", "small", "small.en", "tiny", "tiny.en", "large-v3-turbo-q5_0", "medium", "medium.en"} {
		if m := e.model(n); m.Name == n {
			return m
		}
	}
	return e.def
}
