package transcribe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ModelInfo describes a speech model (ggml-<Name>.bin).
type ModelInfo struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Size  int64  `json:"size"` // bytes, about
	Note  string `json:"note"`
}

// Models are the speech models offered, from the fastest to the most
// accurate. The ".en" ones understand English only, a little better.
var Models = []ModelInfo{
	{"tiny", "Tiny", 78e6, "Fastest, least accurate: for a quick test"},
	{"tiny.en", "Tiny, English only", 78e6, "Fastest, least accurate: for a quick test"},
	{"base", "Base", 148e6, "Fast; good with clear speech"},
	{"base.en", "Base, English only", 148e6, "Fast; good with clear English"},
	{"small", "Small", 488e6, "Recommended: accurate, and quick enough on 4 CPUs"},
	{"small.en", "Small, English only", 488e6, "Recommended for English-only calls"},
	{"medium", "Medium", 1533e6, "More accurate; about three times slower than Small"},
	{"medium.en", "Medium, English only", 1533e6, "More accurate English; about three times slower than Small"},
	{"large-v3-turbo-q5_0", "Large v3 Turbo, compact", 574e6, "Close to the most accurate, in a third of the space; slower than Medium"},
	{"large-v3-turbo", "Large v3 Turbo", 1624e6, "Very accurate; slower than Medium"},
	{"large-v3", "Large v3", 3095e6, "The most accurate; very slow without a graphics card"},
}

// automatic is the order in which an installed model is chosen when none
// is set: the balanced ones first, not the slowest.
var automatic = []string{"small", "small.en", "base", "base.en", "large-v3-turbo-q5_0", "medium", "medium.en", "large-v3-turbo", "large-v3", "tiny", "tiny.en"}

// ModelURL is where models are downloaded from (the recogniser's model
// library).
const ModelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main"

// Where install-transcription.sh puts the program and the models (rq-speech
// since 0.10.1; the names of earlier installs and of the recogniser's own
// build still work).
var (
	speechCommands = []string{"/usr/local/bin/rq-speech", "/usr/local/bin/whisper-cli",
		"/opt/whisper.cpp/build-ringq/bin/whisper-cli", "/opt/whisper.cpp/build/bin/whisper-cli", "rq-speech", "whisper-cli"}
	// SystemModelDirs hold models installed by install-transcription.sh
	// or the recogniser's own build (read-only for RingQswitch).
	SystemModelDirs = []string{"/usr/local/share/ringqswitch/speech", "/usr/local/share/ringqswitch/whisper", "/opt/whisper.cpp/models"}
)

// FindCommand finds the speech recogniser: the configured one, or where it
// is usually installed.
func FindCommand(command string) (string, error) {
	if command != "" {
		p, err := exec.LookPath(command)
		if err != nil {
			return command, fmt.Errorf("the speech recogniser %s is not there", command)
		}
		return p, nil
	}
	for _, c := range speechCommands {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", errors.New("the speech recogniser is not installed (sudo bash packaging/install-transcription.sh)")
}

// Installed is a model file found on the server.
type Installed struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Removable: in the folder RingQswitch downloads to (the others were
	// installed with root's rights).
	Removable bool `json:"removable"`
}

func knownRank(name string) int {
	for i, m := range Models {
		if m.Name == name {
			return i
		}
	}
	return len(Models)
}

// InstalledModels lists the ggml-*.bin models in dirs (the first folder
// holding a name wins); writable is the folder downloads go to.
func InstalledModels(dirs []string, writable string) []Installed {
	seen := map[string]bool{}
	var out []Installed
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "ggml-*.bin"))
		for _, f := range files {
			name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "ggml-"), ".bin")
			st, err := os.Stat(f)
			if err != nil || st.Size() < 1<<20 || seen[name] || strings.HasPrefix(name, "silero") {
				continue
			}
			seen[name] = true
			out = append(out, Installed{Name: name, Path: f, Size: st.Size(), Removable: writable != "" && filepath.Dir(f) == filepath.Clean(writable)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := knownRank(out[i].Name), knownRank(out[j].Name); a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// AutomaticModel picks the model to use when none is chosen.
func AutomaticModel(models []Installed) (Installed, bool) {
	for _, name := range automatic {
		for _, m := range models {
			if m.Name == name {
				return m, true
			}
		}
	}
	if len(models) > 0 {
		return models[0], true
	}
	return Installed{}, false
}

// LookupModel returns the catalog entry of a model name.
func LookupModel(name string) (ModelInfo, bool) {
	for _, m := range Models {
		if m.Name == name {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// DownloadModel downloads a model of the catalog from base (ModelURL) into
// dir as ggml-<name>.bin, reporting its progress (bytes so far, of total).
func DownloadModel(ctx context.Context, base, name, dir string, progress func(done, total int64)) error {
	info, ok := LookupModel(name)
	if !ok {
		return fmt.Errorf("unknown model %q", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if free := freeSpace(dir); free >= 0 && free < info.Size+200e6 {
		return fmt.Errorf("not enough disk space in %s: %d MB free, %d MB needed", dir, free/1e6, (info.Size+200e6)/1e6)
	}
	url := strings.TrimSuffix(base, "/") + "/ggml-" + name + ".bin"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	dest := filepath.Join(dir, "ggml-"+name+".bin")
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer os.Remove(part) // after the rename, nothing
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 1<<20)
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if done == 0 && (n < 4 || string(buf[:4]) != "lmgg") {
				f.Close()
				return fmt.Errorf("%s is not a speech model", url)
			}
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return err
			}
			done += int64(n)
			if progress != nil && time.Since(last) > 500*time.Millisecond {
				progress(done, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("downloading %s: %v", url, rerr)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if total > 0 && done != total {
		return fmt.Errorf("downloading %s: %d of %d bytes", url, done, total)
	}
	if done < 10e6 {
		return fmt.Errorf("%s is too small for a speech model", url)
	}
	if progress != nil {
		progress(done, total)
	}
	return os.Rename(part, dest)
}
