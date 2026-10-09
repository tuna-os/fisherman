package probe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRoot builds a filesystem root from path -> content. A path ending in
// "/" is a directory.
func fakeRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fakeHost answers the host commands the probe runs: `test -e|-d <path>`
// and `cat <path>` against a second fake root (the host as the sandbox
// cannot see it), plus canned output for anything else.
type fakeHost struct {
	root   string
	canned map[string]string
	calls  []string
}

func (h *fakeHost) run(name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	h.calls = append(h.calls, call)
	switch name {
	case "test":
		if h.root == "" {
			return nil, errors.New("exit status 1")
		}
		fi, err := os.Stat(filepath.Join(h.root, args[1]))
		if err != nil || (args[0] == "-d" && !fi.IsDir()) {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	case "cat":
		if h.root == "" {
			return nil, errors.New("exit status 1")
		}
		return os.ReadFile(filepath.Join(h.root, args[0]))
	}
	if out, ok := h.canned[call]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("unexpected command: " + call)
}

func noEnv(string) string { return "" }

// flatpakMarker is a fake-root key meaning "this root is a Flatpak sandbox".
// It is not a file the probe reads: sandbox detection belongs to
// internal/runner, so newProber turns the key into Prober.Sandboxed.
const flatpakMarker = "@flatpak"

// newProber builds a Prober over a fake sandbox root.
func newProber(t *testing.T, files map[string]string, host *fakeHost, env func(string) string) *Prober {
	t.Helper()
	_, sandboxed := files[flatpakMarker]
	clean := map[string]string{}
	for k, v := range files {
		if k != flatpakMarker {
			clean[k] = v
		}
	}
	if env == nil {
		env = noEnv
	}
	return &Prober{Root: fakeRoot(t, clean), Run: host.run, Getenv: env, Sandboxed: func() bool { return sandboxed }}
}
