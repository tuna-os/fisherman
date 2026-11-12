package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

func TestEnsureRootFlags(t *testing.T) {
	if msg, err := ensureRootFlags(t.TempDir(), ""); msg != "" || err != nil {
		t.Fatalf("no options: %q, %v; want nothing to do", msg, err)
	}

	// A unified kernel image has no loader entry: a warning, not a failure.
	msg, err := ensureRootFlags(t.TempDir(), "compress=zstd:1")
	if err != nil || !strings.HasPrefix(msg, "Warning:") {
		t.Fatalf("no entries: %q, %v; want a warning", msg, err)
	}

	// The btrfsSubvolumes recipe's options reach bootc's composefs entry.
	dir := t.TempDir()
	entry := filepath.Join(dir, "boot", "efi", "loader", "entries", "bootc.conf")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("options root=UUID=x rootflags=subvol=/@ rw composefs=h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := recipe.Recipe{Filesystem: "btrfs", BtrfsSubvolumes: true}
	msg, err = ensureRootFlags(dir, r.RootMountOptions())
	if err != nil || !strings.Contains(msg, "1 patched here") {
		t.Fatalf("%q, %v", msg, err)
	}
	b, _ := os.ReadFile(entry)
	if string(b) != "options root=UUID=x rootflags=subvol=@,compress=zstd:1 rw composefs=h\n" {
		t.Fatalf("entry:\n%s", b)
	}

	// An entry with no options line cannot carry the options: fatal.
	if err := os.WriteFile(entry, []byte("title x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureRootFlags(dir, "compress=zstd:1"); err == nil {
		t.Fatal("an entry without an options line must fail the install")
	}
}
