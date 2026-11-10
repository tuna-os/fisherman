package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testLuksUUID = "1520bba9-010e-443d-b082-2fe56abdfee1"

func writeEntry(t *testing.T, target, rel, body string) string {
	t.Helper()
	path := filepath.Join(target, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLuksNeedsKarg(t *testing.T) {
	for _, c := range []struct{ systemdBoot, composefs, want bool }{
		{false, false, true}, // GRUB, ostree
		{false, true, true},  // GRUB, composefs
		{true, false, true},  // systemd-boot, ostree
		{true, true, false},  // GPT auto-discovery
	} {
		if got := luksNeedsKarg(c.systemdBoot, c.composefs); got != c.want {
			t.Errorf("luksNeedsKarg(systemdBoot=%v, composefs=%v) = %v, want %v", c.systemdBoot, c.composefs, got, c.want)
		}
	}
}

// Entries present: every one ends up with the argument, whether bootc wrote
// it from --karg or the patch had to.
func TestEnsureLuksBoot_EntriesPresent(t *testing.T) {
	target := t.TempDir()
	arg := "rd.luks.name=" + testLuksUUID + "=root"
	fromKarg := writeEntry(t, target, "boot/loader/entries/ostree-1.conf", "title one\noptions root=UUID=abc rw "+arg+"\n")
	patched := writeEntry(t, target, "boot/loader/entries/ostree-2.conf", "title two\noptions root=UUID=abc rw\n")

	msg, err := ensureLuksBoot(target, testLuksUUID, true)
	if err != nil {
		t.Fatalf("ensureLuksBoot: %v", err)
	}
	if !strings.Contains(msg, "2 boot entries") || !strings.Contains(msg, "1 patched") {
		t.Errorf("message = %q, want 2 entries and 1 patched", msg)
	}
	for _, p := range []string{fromKarg, patched} {
		data, _ := os.ReadFile(p)
		if strings.Count(string(data), arg) != 1 {
			t.Errorf("%s: want the argument exactly once, got %q", p, data)
		}
	}
}

// Zero entries: fatal where the argument is the only way to find the root,
// a warning where GPT auto-discovery applies. Before #410 both were silent.
func TestEnsureLuksBoot_ZeroEntries(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "boot/loader/entries"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureLuksBoot(target, testLuksUUID, true); err == nil || !strings.Contains(err.Error(), "no BLS loader entry") {
		t.Fatalf("required, zero entries: err = %v, want the no-entry error", err)
	}
	msg, err := ensureLuksBoot(target, testLuksUUID, false)
	if err != nil {
		t.Fatalf("GPT auto-discovery, zero entries: must not be fatal, got %v", err)
	}
	if !strings.HasPrefix(msg, "Warning:") {
		t.Errorf("message = %q, want a warning", msg)
	}
}

// An entry that cannot be patched (here: a directory named like an entry,
// so the failure does not depend on permissions as root).
func TestEnsureLuksBoot_PatchFailure(t *testing.T) {
	target := t.TempDir()
	writeEntry(t, target, "boot/loader/entries/ok.conf", "options root=UUID=abc\n")
	if err := os.Mkdir(filepath.Join(target, "boot/loader/entries/broken.conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureLuksBoot(target, testLuksUUID, true); err == nil || !strings.Contains(err.Error(), "broken.conf") {
		t.Fatalf("err = %v, want an error that names the entry", err)
	}
	if _, err := ensureLuksBoot(target, testLuksUUID, false); err != nil {
		t.Fatalf("GPT auto-discovery: must not be fatal, got %v", err)
	}
}

// An entry with no options line (a UKI with an embedded command line) cannot
// carry the argument: fatal where it is required.
func TestEnsureLuksBoot_EntryWithoutOptions(t *testing.T) {
	target := t.TempDir()
	uki := writeEntry(t, target, "boot/efi/loader/entries/uki.conf", "title signed UKI\nuki /EFI/Linux/bootc/test.efi\n")
	if _, err := ensureLuksBoot(target, testLuksUUID, true); err == nil {
		t.Fatal("an entry with no options line must be fatal when the argument is required")
	}
	if data, _ := os.ReadFile(uki); string(data) != "title signed UKI\nuki /EFI/Linux/bootc/test.efi\n" {
		t.Errorf("the entry was rewritten: %q", data)
	}
}
