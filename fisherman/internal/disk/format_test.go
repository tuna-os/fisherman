package disk_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
)

// ── FormatEFI ─────────────────────────────────────────────────────────────

func TestFormatEFI(t *testing.T) {
	rec := setupRecorder(t)
	if err := disk.FormatEFI("/dev/sda1"); err != nil {
		t.Fatalf("FormatEFI: %v", err)
	}
	assertSingleCall(t, rec, "mkfs.fat", []string{"-F32", "-n", "EFI-SYSTEM", "/dev/sda1"})
}

// ── FormatBoot ────────────────────────────────────────────────────────────

func TestFormatBoot(t *testing.T) {
	rec := setupRecorder(t)
	if err := disk.FormatBoot("/dev/sda2"); err != nil {
		t.Fatalf("FormatBoot: %v", err)
	}
	assertSingleCall(t, rec, "mkfs.ext4", []string{"-L", "boot", "-F", "/dev/sda2"})
}

// ── FormatRoot ────────────────────────────────────────────────────────────

func TestFormatRoot(t *testing.T) {
	tests := []struct {
		name       string
		filesystem string
		wantName   string
		wantArgs   []string
		wantErr    bool
	}{
		{
			name:       "xfs",
			filesystem: "xfs",
			wantName:   "mkfs.xfs",
			wantArgs:   []string{"-f", "-L", "root", "/dev/sda3"},
		},
		{
			name:       "ext4",
			filesystem: "ext4",
			wantName:   "mkfs.ext4",
			wantArgs:   []string{"-F", "-L", "root", "-O", "verity", "/dev/sda3"},
		},
		{
			name:       "btrfs",
			filesystem: "btrfs",
			wantName:   "mkfs.btrfs",
			wantArgs:   []string{"-f", "-L", "root", "/dev/sda3"},
		},
		{
			name:       "unsupported empty",
			filesystem: "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := setupRecorder(t)
			err := disk.FormatRoot("/dev/sda3", tt.filesystem)
			if tt.wantErr {
				if err == nil {
					t.Errorf("FormatRoot(%q): expected error, got nil", tt.filesystem)
				}
				return
			}
			if err != nil {
				t.Fatalf("FormatRoot: %v", err)
			}
			assertSingleCall(t, rec, tt.wantName, tt.wantArgs)
		})
	}
}

// ── SetupBtrfsSubvolumes ──────────────────────────────────────────────────

func TestSetupBtrfsSubvolumes(t *testing.T) {
	rec := setupRecorder(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "target")

	if err := disk.SetupBtrfsSubvolumes("/dev/sda3", target); err != nil {
		t.Fatalf("SetupBtrfsSubvolumes: %v", err)
	}

	// Collect btrfs subvolume create calls.
	var subvolCalls []execCall
	for _, c := range rec.calls {
		if c.name == "btrfs" {
			subvolCalls = append(subvolCalls, c)
		}
	}

	if len(subvolCalls) != 3 {
		t.Fatalf("expected 3 btrfs subvolume create calls, got %d (all calls: %+v)", len(subvolCalls), rec.calls)
	}

	// Must be created in order: @, @home, @snapshots.
	wantSubvols := []string{"@", "@home", "@snapshots"}
	for i, sv := range wantSubvols {
		c := subvolCalls[i]
		wantArgs := []string{"subvolume", "create", target + "/" + sv}
		if !equalSlice(c.args, wantArgs) {
			t.Errorf("subvol call %d args = %v, want %v", i, c.args, wantArgs)
		}
	}

	// Final mount must use subvol=@ and zstd compression.
	var finalMount *execCall
	for i := len(rec.calls) - 1; i >= 0; i-- {
		c := rec.calls[i]
		if c.name == "mount" && !contains(c.args, "--bind") && !contains(c.args, "-R") {
			finalMount = &rec.calls[i]
			break
		}
	}
	if finalMount == nil {
		t.Fatal("no final mount call found")
	}
	opts := ""
	for i, arg := range finalMount.args {
		if arg == "-o" && i+1 < len(finalMount.args) {
			opts = finalMount.args[i+1]
			break
		}
	}
	if !strings.Contains(opts, "subvol=@") {
		t.Errorf("final mount opts %q missing subvol=@", opts)
	}
	if !strings.Contains(opts, "compress=zstd:1") {
		t.Errorf("final mount opts %q missing compress=zstd:1", opts)
	}
}

// ── BindMount / scratch space ─────────────────────────────────────────────

// TestBindMount verifies that BindMount calls mount --bind with the correct args.
func TestBindMount(t *testing.T) {
	rec := setupRecorder(t)

	dir := t.TempDir()
	src := "/var/fisherman-tmp"
	dst := filepath.Join(dir, "var", "tmp")

	if err := disk.BindMount(src, dst); err != nil {
		t.Fatalf("BindMount: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %+v", len(rec.calls), rec.calls)
	}
	c := rec.calls[0]
	if c.name != "mount" {
		t.Errorf("name = %q, want mount", c.name)
	}
	wantArgs := []string{"--bind", src, dst}
	if !equalSlice(c.args, wantArgs) {
		t.Errorf("args = %v, want %v", c.args, wantArgs)
	}
}

// TestBindMount_ScratchSpacePath is a regression test for the scratch-space
// location. The bind mount source must be under /var/ (disk-backed), not /run/
// (tmpfs, ~50% RAM, too small for large bootc image blobs like 3.7 GB images).
func TestBindMount_ScratchSpacePath(t *testing.T) {
	rec := setupRecorder(t)

	dir := t.TempDir()
	// These are the exact values used in cmd/fisherman/main.go.
	scratchDir := "/var/fisherman-tmp"
	bindDst := filepath.Join(dir, "var", "tmp") // substitute temp dir for /var/tmp

	if err := disk.BindMount(scratchDir, bindDst); err != nil {
		t.Fatalf("BindMount: %v", err)
	}

	c := rec.calls[0]

	// src must be /var/fisherman-tmp, not /run/fisherman-tmp or similar.
	if c.args[1] != scratchDir {
		t.Errorf("bind src = %q, want %q", c.args[1], scratchDir)
	}
	if strings.HasPrefix(c.args[1], "/run/") {
		t.Errorf("scratch dir %q must not be under /run/ (tmpfs, too small); must be under /var/", c.args[1])
	}
	if !strings.HasPrefix(c.args[1], "/var/") {
		t.Errorf("scratch dir %q should be under /var/ (disk-backed)", c.args[1])
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func assertSingleCall(t *testing.T, rec *recorder, wantName string, wantArgs []string) {
	t.Helper()
	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %+v", len(rec.calls), rec.calls)
	}
	c := rec.calls[0]
	if c.name != wantName {
		t.Errorf("name = %q, want %q", c.name, wantName)
	}
	if !equalSlice(c.args, wantArgs) {
		t.Errorf("args = %v, want %v", c.args, wantArgs)
	}
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

