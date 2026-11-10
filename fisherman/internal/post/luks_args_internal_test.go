package post

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func luksFixture(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range entries {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRequireLuksArgs_EntriesPresent(t *testing.T) {
	arg := LuksKarg("u-u-i-d")
	dir := luksFixture(t, map[string]string{
		"boot/loader/entries/a.conf":     "title a\noptions root=UUID=x rw\n",
		"boot/loader/entries/b.conf":     "title b\noptions root=UUID=x " + arg + " rw\n",
		"boot/efi/loader/entries/c.conf": "title c\noptions quiet\n",
	})
	res, err := RequireLuksArgs(dir, "u-u-i-d")
	if err != nil {
		t.Fatalf("RequireLuksArgs: %v", err)
	}
	if res != (LuksArgsResult{Entries: 3, Patched: 2}) {
		t.Errorf("result = %+v, want 3 entries, 2 patched", res)
	}
	// Idempotent: nothing left to patch.
	res, err = RequireLuksArgs(dir, "u-u-i-d")
	if err != nil || res != (LuksArgsResult{Entries: 3}) {
		t.Errorf("second run = %+v, %v; want 3 entries, 0 patched", res, err)
	}
}

func TestRequireLuksArgs_ZeroEntries(t *testing.T) {
	for name, dir := range map[string]string{
		"no entries directory": t.TempDir(),
		"empty directory":      luksFixture(t, map[string]string{"boot/loader/entries/README": "not an entry"}),
	} {
		res, err := RequireLuksArgs(dir, "u-u-i-d")
		if !errors.Is(err, ErrNoLoaderEntries) || res.Entries != 0 {
			t.Errorf("%s: RequireLuksArgs = %+v, %v; want ErrNoLoaderEntries", name, res, err)
		}
		// The lenient form keeps its contract.
		if n, err := EnsureLuksArgs(dir, "u-u-i-d"); n != 0 || err != nil {
			t.Errorf("%s: EnsureLuksArgs = %d, %v; want 0, nil", name, n, err)
		}
	}
	if _, err := RequireLuksArgs(t.TempDir(), ""); err == nil {
		t.Error("an empty UUID must be an error")
	}
}

func TestRequireLuksArgs_WriteFailure(t *testing.T) {
	dir := luksFixture(t, map[string]string{"boot/loader/entries/a.conf": "options root=UUID=x\n"})
	orig := luksWriteFile
	t.Cleanup(func() { luksWriteFile = orig })

	// The write fails outright (a read-only or full /boot).
	luksWriteFile = func(string, []byte, fs.FileMode) error { return errors.New("read-only file system") }
	if _, err := RequireLuksArgs(dir, "u-u-i-d"); err == nil || !strings.Contains(err.Error(), "read-only file system") {
		t.Fatalf("RequireLuksArgs = %v, want the write error", err)
	}

	// The write reports success but the entry did not change: the
	// read-back catches it.
	luksWriteFile = func(string, []byte, fs.FileMode) error { return nil }
	if _, err := RequireLuksArgs(dir, "u-u-i-d"); err == nil || !strings.Contains(err.Error(), "a.conf") {
		t.Fatalf("RequireLuksArgs = %v, want the verification error for a.conf", err)
	}
}
