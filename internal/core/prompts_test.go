package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// Every built-in prompt loads (16-bit or μ-law WAV) and is not silent.
func TestBuiltinPrompts(t *testing.T) {
	entries, err := builtinPrompts.ReadDir("prompts")
	if err != nil || len(entries) < 40 {
		t.Fatalf("prompts: %d %v", len(entries), err)
	}
	dir := t.TempDir()
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".wav")
		data, err := builtinPrompts.ReadFile("prompts/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, e.Name())
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		pcm, err := media.LoadWAV(path)
		if err != nil || len(pcm) < 800 {
			t.Fatalf("%s: %d samples %v", name, len(pcm), err)
		}
		peak := 0
		for _, v := range pcm {
			peak = max(peak, int(v), -int(v))
		}
		if peak < 2000 {
			t.Fatalf("%s: too quiet (peak %d)", name, peak)
		}
	}
}
