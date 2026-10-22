package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Recognizer turns speech in WAV files into timed text: one result per
// file, in order.
type Recognizer interface {
	Recognize(ctx context.Context, wavs []string, language string) ([]*Recognition, error)
}

// Recognition is what the recogniser heard in a file.
type Recognition struct {
	Language string
	Segments []RecSegment
}

// RecSegment is a stretch of recognised text; times are in seconds from
// the start of the file.
type RecSegment struct {
	From, To float64
	Text     string
}

// SpeechCLI runs the speech recogniser's command line program (rq-speech) with a
// ggml model: speech recognition on this server, in 99 languages, no data
// leaving it.
type SpeechCLI struct {
	Command string // rq-speech
	Model   string // ggml-base.bin, ggml-small.bin...
	Threads int
	Extra   []string // more recogniser options
}

// Recognize runs the recogniser once for all the files (the model is loaded
// once); it writes each file's result next to it (<file>.json).
func (w *SpeechCLI) Recognize(ctx context.Context, wavs []string, language string) ([]*Recognition, error) {
	if len(wavs) == 0 {
		return nil, nil
	}
	if language == "" {
		language = "auto"
	}
	args := []string{"-m", w.Model, "-l", language, "-oj", "-np", "-nt"}
	if w.Threads > 0 {
		args = append(args, "-t", fmt.Sprint(w.Threads))
	}
	args = append(args, w.Extra...)
	for _, f := range wavs {
		os.Remove(f + ".json")
		args = append(args, "-f", f)
	}
	cmd := exec.CommandContext(ctx, w.Command, args...)
	cmd.WaitDelay = 5 * time.Second
	var msg bytes.Buffer
	cmd.Stdout, cmd.Stderr = &msg, &msg
	err := cmd.Start()
	if err == nil {
		// Calls come first: the recogniser gets the CPU time they leave.
		lowerPriority(cmd.Process.Pid)
		err = cmd.Wait()
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("speech recogniser: %v: %s", err, lastLine(msg.String()))
	}
	out := make([]*Recognition, len(wavs))
	for i, f := range wavs {
		data, err := os.ReadFile(f + ".json")
		if err != nil {
			return nil, fmt.Errorf("the speech recogniser wrote no result for %s: %v (%s)", filepath.Base(f), err, lastLine(msg.String()))
		}
		os.Remove(f + ".json")
		if out[i], err = parseSpeechJSON(data); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

type whisperJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Offsets *struct{ From, To int64 } `json:"offsets"`
		Text    string                    `json:"text"`
	} `json:"transcription"`
}

// parseSpeechJSON reads the recogniser's -oj output (times in ms).
func parseSpeechJSON(data []byte) (*Recognition, error) {
	var j whisperJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("speech recogniser result: %w", err)
	}
	r := &Recognition{Language: j.Result.Language}
	for _, s := range j.Transcription {
		seg := RecSegment{Text: s.Text}
		if s.Offsets != nil {
			seg.From, seg.To = float64(s.Offsets.From)/1000, float64(s.Offsets.To)/1000
		}
		r.Segments = append(r.Segments, seg)
	}
	return r, nil
}
