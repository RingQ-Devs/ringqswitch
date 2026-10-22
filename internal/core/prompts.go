package core

import (
	"embed"
	"os"
	"path/filepath"
	"sync"
)

// Built-in spoken prompts (8 kHz WAV, made with SVOX Pico), used when no
// recording of the same name is in the sounds or recordings folders
// (prompts/<name>.wav, uploaded in Sounds & music).
//
//	queue-no-agent      a caller leaves a queue nobody can answer
//	queue-timeout       a caller leaves a queue after its longest wait
//	voicemail-greeting  a voicemail box without a greeting of its own
//
//go:embed prompts/*.wav
var builtinPrompts embed.FS

var promptFiles sync.Map // name: path of the extracted file

// prompt returns the file to play for a built-in prompt: the recording
// uploaded under prompts/<name>.wav, or the built-in one ("" if neither).
func (c *Core) prompt(ch *Channel, name string) string {
	if p := c.resolveSound(ch, "prompts/"+name+".wav"); p != "" {
		return p
	}
	if p, ok := promptFiles.Load(name); ok {
		return p.(string)
	}
	data, err := builtinPrompts.ReadFile("prompts/" + name + ".wav")
	if err != nil {
		return ""
	}
	dir := filepath.Join(os.TempDir(), "ringqswitch-prompts-"+Version)
	path := filepath.Join(dir, name+".wav")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil || os.Rename(path+".tmp", path) != nil {
		return ""
	}
	promptFiles.Store(name, path)
	return path
}
