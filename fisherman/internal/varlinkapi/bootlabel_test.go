package varlinkapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootLabelIsTheImagesName(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := bootLabel(nil); got != defaultBootLabel {
		t.Errorf("no deployment: %q", got)
	}
	if got := bootLabel([]string{dir}); got != defaultBootLabel {
		t.Errorf("no os-release: %q", got)
	}
	write("usr/lib/os-release", "ID=bonito\nNAME=\"Bonito\"\nPRETTY_NAME=\"Bonito 44\"\n")
	if got := bootLabel([]string{dir}); got != "Bonito" {
		t.Errorf("usr/lib: %q", got)
	}
	write("etc/os-release", "NAME='Yellowfin'\n")
	if got := bootLabel([]string{dir}); got != "Yellowfin" {
		t.Errorf("etc wins: %q", got)
	}
	write("etc/os-release", "NAME=\"Bad\\nName\"\n")
	if got := bootLabel([]string{dir}); got != "BadName" {
		t.Errorf("control characters: %q", got)
	}
	write("etc/os-release", "NAME=\n")
	if got := bootLabel([]string{dir}); got != "Bonito" {
		t.Errorf("empty NAME falls through: %q", got)
	}
}
