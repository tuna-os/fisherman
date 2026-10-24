package post

import (
	"github.com/tuna-os/fisherman/internal/recipe"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishIntentStateNoOverwrite(t *testing.T) {
	for _, kind := range []string{"file", "dangling-symlink", "existing-symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "state.json")
			other := filepath.Join(t.TempDir(), "other")
			switch kind {
			case "file":
				os.WriteFile(state, []byte("old"), 0644)
			case "dangling-symlink":
				os.Symlink(other, state)
			case "existing-symlink":
				os.WriteFile(other, []byte("old"), 0644)
				os.Symlink(other, state)
			}
			if err := publishIntentState(state, []byte("new")); err == nil {
				t.Fatal("overwrote concurrent state")
			}
			if kind == "dangling-symlink" {
				if _, err := os.Stat(other); !os.IsNotExist(err) {
					t.Fatal("followed dangling link")
				}
			}
			if kind == "existing-symlink" {
				data, _ := os.ReadFile(other)
				if string(data) != "old" {
					t.Fatal("followed existing link")
				}
			}
			if kind == "file" {
				data, _ := os.ReadFile(state)
				if string(data) != "old" {
					t.Fatal("overwrote file")
				}
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("staging state leaked")
			}
		})
	}
}
func TestPublishIntentStateComplete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := publishIntentState(path, []byte("complete\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete\n" {
		t.Fatalf("bad publication: %s %v", data, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary file leaked")
	}
}

func TestTargetIntentSandboxFailsBeforeFilesystem(t *testing.T) {
	old := intentInFlatpak
	intentInFlatpak = func() bool { return true }
	t.Cleanup(func() { intentInFlatpak = old })
	intent := &recipe.FlatpakIntent{Version: 1, Live: recipe.FlatpakSelection{Mode: "inherit"}, Target: recipe.FlatpakSelection{Mode: "replace", Apps: []recipe.FlatpakApp{}}}
	if err := CheckTargetFlatpakIntentContext(intent); err == nil {
		t.Fatal("sandbox accepted")
	}
	if err := ApplyTargetFlatpakIntent("/path/that/must/not/be/touched", "", intent); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("did not reject before filesystem lookup: %v", err)
	}
	if err := CheckTargetFlatpakIntentContext(nil); err != nil {
		t.Fatal("legacy changed")
	}
	intent.Target.Mode = "inherit"
	if err := CheckTargetFlatpakIntentContext(intent); err != nil {
		t.Fatal("inherit changed")
	}
}
