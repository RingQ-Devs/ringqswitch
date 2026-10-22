package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// The voice AI service: the voice AI service's Realtime API (GA interface) over a
// WebSocket. Speech goes in and comes out as audio, so the agent answers
// without a separate recognise-think-speak chain; the service hears when
// the caller starts and stops talking (server VAD) and answers at once.

type rtSession struct {
	Instructions string
	Voice        string
	Noise        string // "near" (handset) or "far" (speakerphone); "" = none
	Language     string // the caller's language for the transcripts ("" = any)
	Tools        []rtTool
	Format       string // "audio/pcmu" (calls, default) or "audio/pcm" (24 kHz)
	NoTurns      bool   // no turn detection (one-off speech: previews)
	Transcribe   bool   // transcripts of what the caller says
}

type rtTool struct {
	Name        string
	Description string
	Params      map[string]any
}

type rtError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *rtError) Error() string {
	if e.Code != "" {
		return e.Message + " (" + e.Code + ")"
	}
	return e.Message
}

type rtResponse struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	StatusDetails *struct {
		Type   string   `json:"type"`
		Reason string   `json:"reason"`
		Error  *rtError `json:"error"`
	} `json:"status_details"`
}

type rtEvent struct {
	Type       string      `json:"type"`
	ResponseID string      `json:"response_id"`
	ItemID     string      `json:"item_id"`
	Delta      string      `json:"delta"`
	Transcript string      `json:"transcript"`
	CallID     string      `json:"call_id"`
	Name       string      `json:"name"`
	Arguments  string      `json:"arguments"`
	Error      *rtError    `json:"error"`
	Response   *rtResponse `json:"response"`
}

type rtClient struct {
	conn *websocket.Conn
	wmu  sync.Mutex
	ev   chan rtEvent
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	err  error // why the events stopped
}

// rtDial opens a session with the service.
func rtDial(ctx context.Context, c aiConfig) (*rtClient, error) {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") {
		return nil, fmt.Errorf("the voice AI service address %q is not a wss:// address", c.URL)
	}
	q := u.Query()
	if q.Get("model") == "" {
		q.Set("model", c.Model)
	}
	u.RawQuery = q.Encode()
	d := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second}
	conn, resp, err := d.DialContext(ctx, u.String(), http.Header{"Authorization": {"Bearer " + c.Key}})
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			var e struct {
				Error rtError `json:"error"`
			}
			if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
				return nil, fmt.Errorf("the voice AI service refused: %s", e.Error.Error())
			}
			return nil, fmt.Errorf("the voice AI service refused: %s", resp.Status)
		}
		return nil, fmt.Errorf("cannot reach the voice AI service: %w", err)
	}
	conn.SetReadLimit(32 << 20)
	rt := &rtClient{conn: conn, ev: make(chan rtEvent, 1024), done: make(chan struct{})}
	go rt.read()
	return rt, nil
}

func (rt *rtClient) read() {
	defer close(rt.ev)
	for {
		_, data, err := rt.conn.ReadMessage()
		if err != nil {
			rt.mu.Lock()
			if rt.err == nil {
				rt.err = err
			}
			rt.mu.Unlock()
			return
		}
		var e rtEvent
		if json.Unmarshal(data, &e) != nil || e.Type == "" {
			continue
		}
		select {
		case rt.ev <- e:
		case <-rt.done:
			return
		}
	}
}

// failure is why the session ended.
func (rt *rtClient) failure() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.err == nil {
		return errors.New("the voice AI service closed the session")
	}
	var ce *websocket.CloseError
	if errors.As(rt.err, &ce) && ce.Text != "" {
		return fmt.Errorf("the voice AI service closed the session: %s", ce.Text)
	}
	return fmt.Errorf("the voice AI session broke: %w", rt.err)
}

func (rt *rtClient) close() {
	rt.once.Do(func() {
		close(rt.done)
		rt.wmu.Lock()
		_ = rt.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		rt.wmu.Unlock()
		rt.conn.Close()
	})
}

func (rt *rtClient) send(v any) error {
	rt.wmu.Lock()
	defer rt.wmu.Unlock()
	_ = rt.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return rt.conn.WriteJSON(v)
}

// appendAudio sends what the caller said (in the session's format).
func (rt *rtClient) appendAudio(b []byte) error {
	return rt.send(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(b)})
}

// respond asks for an answer now; instructions (optional) apply to it only.
func (rt *rtClient) respond(instructions string) error {
	r := map[string]any{}
	if instructions != "" {
		r["instructions"] = instructions
	}
	return rt.send(map[string]any{"type": "response.create", "response": r})
}

func (rt *rtClient) toolResult(callID string, v any) error {
	out, _ := json.Marshal(v)
	return rt.send(map[string]any{"type": "conversation.item.create",
		"item": map[string]any{"type": "function_call_output", "call_id": callID, "output": string(out)}})
}

// truncate tells the service how much of an answer the caller heard (they
// spoke over the rest), so the conversation holds what was really said.
func (rt *rtClient) truncate(itemID string, heard time.Duration) error {
	return rt.send(map[string]any{"type": "conversation.item.truncate", "item_id": itemID, "content_index": 0,
		"audio_end_ms": heard.Milliseconds()})
}

func (rt *rtClient) cancel() error { return rt.send(map[string]string{"type": "response.cancel"}) }

func (s rtSession) payload() map[string]any {
	format := map[string]any{"type": firstNonEmpty(s.Format, "audio/pcmu")}
	if format["type"] == "audio/pcm" {
		format["rate"] = 24000
	}
	in := map[string]any{"format": format}
	if s.Noise == "near" || s.Noise == "far" {
		in["noise_reduction"] = map[string]any{"type": s.Noise + "_field"}
	}
	if s.Transcribe {
		tr := map[string]any{"model": "gpt-4o-mini-transcribe"}
		if s.Language != "" {
			tr["language"] = s.Language
		}
		in["transcription"] = tr
	}
	if s.NoTurns {
		in["turn_detection"] = nil
	} else {
		// Quick turns: the answer starts half a second after the caller
		// stops; speaking over the agent stops it at once.
		in["turn_detection"] = map[string]any{"type": "server_vad", "threshold": 0.5, "prefix_padding_ms": 300,
			"silence_duration_ms": 500, "create_response": true, "interrupt_response": true}
	}
	sess := map[string]any{"type": "realtime", "instructions": s.Instructions, "output_modalities": []string{"audio"},
		"audio": map[string]any{"input": in, "output": map[string]any{"format": format, "voice": firstNonEmpty(s.Voice, "marin")}}}
	if len(s.Tools) > 0 {
		var tools []map[string]any
		for _, t := range s.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Params})
		}
		sess["tools"], sess["tool_choice"] = tools, "auto"
	}
	return map[string]any{"type": "session.update", "session": sess}
}

// configure sets the session up and waits until the service took it.
func (rt *rtClient) configure(ctx context.Context, s rtSession) error {
	if err := rt.send(s.payload()); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return errors.New("the voice AI service did not answer in time")
		case e, ok := <-rt.ev:
			if !ok {
				return rt.failure()
			}
			switch e.Type {
			case "session.updated":
				return nil
			case "error":
				if e.Error != nil {
					return fmt.Errorf("the voice AI service: %s", e.Error.Error())
				}
				return errors.New("the voice AI service reported an error")
			}
		}
	}
}

// speak has the service say a text once (the voice preview): PCM 24 kHz.
func (rt *rtClient) speak(ctx context.Context, text string) ([]byte, error) {
	if err := rt.respond("Say exactly the following, word for word, in a warm and natural voice, and nothing else: " + text); err != nil {
		return nil, err
	}
	var pcm []byte
	for {
		select {
		case <-ctx.Done():
			return nil, errors.New("the voice AI service did not answer in time")
		case e, ok := <-rt.ev:
			if !ok {
				return nil, rt.failure()
			}
			switch e.Type {
			case "response.output_audio.delta", "response.audio.delta":
				b, err := base64.StdEncoding.DecodeString(e.Delta)
				if err == nil {
					pcm = append(pcm, b...)
				}
			case "response.done":
				if e.Response != nil && e.Response.Status == "failed" && e.Response.StatusDetails != nil && e.Response.StatusDetails.Error != nil {
					return nil, fmt.Errorf("the voice AI service: %s", e.Response.StatusDetails.Error.Error())
				}
				return pcm, nil
			case "error":
				if e.Error != nil {
					return nil, fmt.Errorf("the voice AI service: %s", e.Error.Error())
				}
			}
		}
	}
}

// aiPreviewAPI lets owners hear a voice saying a text (a greeting).
func (s *Server) aiPreviewAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow) error {
	var b struct {
		Voice string `json:"voice"`
		Text  string `json:"text"`
	}
	if err := decodeBody(r, &b); err != nil {
		return err
	}
	b.Voice = strings.ToLower(firstNonEmpty(strings.TrimSpace(b.Voice), "marin"))
	if !contains(aiVoices, b.Voice) {
		return badRequest("unknown voice")
	}
	b.Text = strings.TrimSpace(firstNonEmpty(b.Text, aiDefaultGreeting))
	if len(b.Text) > 600 {
		return badRequest("the text is too long")
	}
	c := s.aiConfig(ctx, dom.uuid)
	if c.Key == "" {
		return badRequest("enter the API key in AI agent settings first")
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rt, err := rtDial(pctx, c)
	if err != nil {
		return badRequest("%s", err.Error())
	}
	defer rt.close()
	if err := rt.configure(pctx, rtSession{Instructions: "You read texts aloud.", Voice: b.Voice, Format: "audio/pcm", NoTurns: true}); err != nil {
		return badRequest("%s", err.Error())
	}
	pcm, err := rt.speak(pctx, b.Text)
	if err != nil {
		return badRequest("%s", err.Error())
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(wavPCM16(pcm, 24000))
	return nil
}

// wavPCM16 wraps 16-bit mono little-endian samples in a WAV header.
func wavPCM16(pcm []byte, rate int) []byte {
	n := len(pcm) &^ 1
	h := make([]byte, 44, 44+n)
	put32 := func(o, v int) { h[o], h[o+1], h[o+2], h[o+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24) }
	put16 := func(o, v int) { h[o], h[o+1] = byte(v), byte(v>>8) }
	copy(h[0:], "RIFF")
	put32(4, 36+n)
	copy(h[8:], "WAVEfmt ")
	put32(16, 16)
	put16(20, 1)
	put16(22, 1)
	put32(24, rate)
	put32(28, rate*2)
	put16(32, 2)
	put16(34, 16)
	copy(h[36:], "data")
	put32(40, n)
	return append(h, pcm[:n]...)
}
