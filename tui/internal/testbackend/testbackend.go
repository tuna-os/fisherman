// Package testbackend puts a fake `fisherman` on PATH for tests. The fake
// mirrors the dispatch of fisherman/cmd/fisherman/main.go, records what it
// was given, and never touches a disk.
package testbackend

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const script = `#!/bin/sh
# Fake fisherman. $FAKE_FISHERMAN_DIR holds its inputs and its records.
dir="$FAKE_FISHERMAN_DIR"
echo "$*" >> "$dir/argv"
case "$1" in
  probe)
    [ "$#" -eq 2 ] && [ "$2" = "--json" ] || { echo "probe: bad arguments: $*" >&2; exit 2; }
    [ -f "$dir/probe.json" ] || { echo "probe: lsblk failed" >&2; exit 1; }
    cat "$dir/probe.json"
    exit 0 ;;
  validate)
    [ "$*" = "validate --json --partial -" ] || { echo "validate: bad arguments: $*" >&2; exit 2; }
    cat > "$dir/validate-stdin.json"
    if [ -f "$dir/validate.json" ]; then
      cat "$dir/validate.json"
      grep -q '"valid": *true' "$dir/validate.json" && exit 0
      exit 1
    fi
    echo '{"protocol_version":1,"valid":true,"errors":[]}'
    exit 0 ;;
  -)
    [ "$#" -eq 1 ] || { echo "expected the single argument -, got: $*" >&2; exit 3; }
    cat > "$dir/recipe-stdin.json"
    grep -q '"disk"' "$dir/recipe-stdin.json" || { echo "recipe has no disk field" >&2; exit 6; }
    if [ -f "$dir/events" ]; then cat "$dir/events"; fi
    if [ -f "$dir/exit" ]; then exit "$(cat "$dir/exit")"; fi
    exit 0 ;;
  *)
    echo "fake fisherman: a recipe path or an unknown command: $*" >&2
    exit 4 ;;
esac
`

// Install writes the fake to a new directory, puts that directory first on
// PATH, and returns the record directory. Files the fake reads from it:
// probe.json (probe output; absent means probe fails), validate.json (the
// validate verdict; absent means valid), events (stdout of an install) and
// exit (its exit status). Files it writes: argv (one line per call),
// validate-stdin.json and recipe-stdin.json.
func Install(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake backend is a shell script")
	}
	bin, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "fisherman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_FISHERMAN_DIR", dir)
	return dir
}

// Write puts content in a file of the record directory.
func Write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Read returns a file of the record directory, or "" when it is absent.
func Read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}
