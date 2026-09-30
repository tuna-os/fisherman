package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tuna-os/fisherman/tui/internal/config"
)

// TestFishermanArgs_PositionalRecipe pins the argv this module hands the
// backend: the recipe path, alone. fisherman/cmd/fisherman/main.go treats
// os.Args[1] as the recipe unless it is one of its few utility subcommands.
func TestFishermanArgs_PositionalRecipe(t *testing.T) {
	got := fishermanArgs("/run/user/1000/recipe.json")
	if len(got) != 1 || got[0] != "/run/user/1000/recipe.json" {
		t.Fatalf("fishermanArgs = %q, want the recipe path as the only argument", got)
	}
	for _, a := range got {
		if a == "install" || a == "--recipe" {
			t.Fatalf("fishermanArgs carries %q, which the backend does not accept", a)
		}
	}
}

// TestFishermanContract_FakeBackend is the cross-module contract test #178
// asked for. A fake `fisherman` on PATH asserts that it receives exactly one
// argument, that the argument is not one of the backend's subcommands or a
// flag, and that it names a readable recipe written by this module's own
// config.WriteRecipe. It exercises findFisherman, fishermanArgs and the
// recipe writer together, the way startInstall does, without a real install.
func TestFishermanContract_FakeBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake backend is a shell script")
	}
	bin := t.TempDir()
	fake := filepath.Join(bin, "fisherman")
	script := `#!/bin/sh
# Fake backend mirroring the dispatch in fisherman/cmd/fisherman/main.go.
[ "$#" -eq 1 ] || { echo "expected 1 argument, got $#: $*" >&2; exit 3; }
case "$1" in
  install|validate|images|scan|version|help|-*)
    echo "not a recipe path: $1" >&2; exit 4 ;;
esac
[ -f "$1" ] || { echo "recipe file missing: $1" >&2; exit 5; }
grep -q '"disk"' "$1" || { echo "recipe has no disk field" >&2; exit 6; }
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.InstallConfig{
		DiskDevice: "/dev/sda",
		Filesystem: "xfs",
		Image:      "ghcr.io/example/marlin:latest",
		Hostname:   "marlin",
		Username:   "alice",
		FullName:   "Alice",
		Password:   "hunter2",
	}
	recipePath := filepath.Join(t.TempDir(), "recipe.json")
	if err := cfg.WriteRecipe(recipePath); err != nil {
		t.Fatalf("WriteRecipe: %v", err)
	}

	backend := findFisherman()
	if backend != fake {
		t.Fatalf("findFisherman() = %q, want the fake on PATH %q", backend, fake)
	}
	out, err := exec.Command(backend, fishermanArgs(recipePath)...).CombinedOutput()
	if err != nil {
		t.Fatalf("the fake backend rejected this module's invocation: %v\n%s", err, out)
	}
}
