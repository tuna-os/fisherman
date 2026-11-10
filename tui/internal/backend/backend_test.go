package backend_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/tui/internal/backend"
	"github.com/tuna-os/fisherman/tui/internal/testbackend"
)

const probeFixture = `{
  "protocol_version": 1,
  "disks": [
    {"path": "/dev/nvme0n1", "size_bytes": 512110190592, "size_label": "512 GB", "model": "Samsung SSD 980", "vendor": "",
     "transport": "nvme", "transport_label": "NVMe", "removable": false, "read_only": false, "eligible": true},
    {"path": "/dev/sda", "size_bytes": 62008590336, "size_label": "62 GB", "model": "Ultra Fit", "vendor": "SanDisk",
     "transport": "usb", "transport_label": "USB", "removable": true, "read_only": false, "eligible": false, "excluded_reason": "boot_disk"},
    {"path": "/dev/sdb", "size_bytes": 8000000000, "size_label": "8 GB", "model": "Tiny", "vendor": "",
     "transport": "usb", "transport_label": "USB", "removable": true, "read_only": false, "eligible": false, "excluded_reason": "too_small"}
  ],
  "tpm": {"usable": false}
}`

// The disk list is fisherman's: only the disks it calls eligible are
// offered, so the live USB stick (boot_disk) is not (#417).
func TestProbe_EligibleDisksOnly(t *testing.T) {
	dir := testbackend.Install(t)
	testbackend.Write(t, dir, "probe.json", probeFixture)

	all, err := backend.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("Probe returned %d disks, want 3", len(all))
	}
	eligible := backend.Eligible(all)
	if len(eligible) != 1 || eligible[0].Path != "/dev/nvme0n1" {
		t.Fatalf("Eligible = %+v, want only /dev/nvme0n1", eligible)
	}
	if got := eligible[0].Label(); got != "/dev/nvme0n1  512 GB  Samsung SSD 980  NVMe" {
		t.Errorf("Label = %q", got)
	}
	if all[1].ExcludedReason != "boot_disk" {
		t.Errorf("the live medium must carry its reason, got %+v", all[1])
	}
	if got := strings.TrimSpace(testbackend.Read(t, dir, "argv")); got != "probe --json" {
		t.Errorf("argv = %q, want probe --json", got)
	}
}

// A failed probe is an error and no disk. Before, a failed lsblk produced an
// invented "sda QEMU HARDDISK".
func TestProbe_FailureInventsNoDisk(t *testing.T) {
	testbackend.Install(t) // no probe.json: the fake's probe exits 1
	disks, err := backend.Probe()
	if err == nil || len(disks) != 0 {
		t.Fatalf("Probe = %+v, %v; want no disk and an error", disks, err)
	}
	if !strings.Contains(err.Error(), "lsblk failed") {
		t.Errorf("the error should carry fisherman's stderr, got %v", err)
	}
}

func TestParseProbe_Malformed(t *testing.T) {
	if disks, err := backend.ParseProbe([]byte("not json")); err == nil || disks != nil {
		t.Fatalf("ParseProbe = %+v, %v; want an error", disks, err)
	}
}

// Field checks go to `fisherman validate --json --partial -`, on stdin.
func TestValidatePartial(t *testing.T) {
	dir := testbackend.Install(t)
	fields := `{"hostname":"-router","encryption":{"type":"luks-passphrase","passphrase":"correct horse"}}`

	problems, err := backend.ValidatePartial([]byte(fields))
	if err != nil || len(problems) != 0 {
		t.Fatalf("valid fields: %+v, %v", problems, err)
	}
	if got := strings.TrimSpace(testbackend.Read(t, dir, "argv")); got != "validate --json --partial -" {
		t.Errorf("argv = %q: the fields (and the passphrase) must not be in it", got)
	}
	if got := testbackend.Read(t, dir, "validate-stdin.json"); got != fields {
		t.Errorf("stdin = %q, want the fields", got)
	}

	testbackend.Write(t, dir, "validate.json", `{"protocol_version":1,"valid":false,"errors":[
	  {"field":"hostname","code":"hostname_label_hyphen","message":"hostname label \"-router\" must not start or end with a hyphen"}]}`)
	problems, err = backend.ValidatePartial([]byte(fields))
	if err != nil {
		t.Fatalf("invalid fields are a verdict, not an error: %v", err)
	}
	if len(problems) != 1 || problems[0].Field != "hostname" || problems[0].Code != "hostname_label_hyphen" {
		t.Fatalf("problems = %+v", problems)
	}

	// Output that is no verdict is an error, never "valid".
	testbackend.Write(t, dir, "validate.json", `fisherman: internal error`)
	if _, err := backend.ValidatePartial([]byte(fields)); err == nil {
		t.Fatal("unreadable output must be an error")
	}
}

// The recipe reaches the backend on stdin and nowhere else: argv is "-",
// and no file holds it (#417).
func TestInstallCommand_RecipeOnStdin(t *testing.T) {
	dir := testbackend.Install(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	recipe := `{"disk":"/dev/sda","encryption":{"type":"luks-passphrase","passphrase":"correct horse"},"user":{"password":"hunter2"}}`

	cmd, err := backend.InstallCommand([]byte(recipe))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range cmd.Args[1:] {
		if a != "-" {
			t.Errorf("argv carries %q; the only argument is -", a)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the fake backend rejected the invocation: %v\n%s", err, out)
	}
	if got := testbackend.Read(t, dir, "recipe-stdin.json"); got != recipe {
		t.Errorf("stdin = %q, want the recipe", got)
	}
	if got := strings.TrimSpace(testbackend.Read(t, dir, "argv")); got != "-" {
		t.Errorf("argv = %q, want -", got)
	}
	left, _ := os.ReadDir(tmp)
	if len(left) != 0 {
		t.Errorf("a file was left in the temp directory: %v", left)
	}
}

// findFisherman's old "./fisherman" candidate ran whatever the current
// directory held, as root (#417).
func TestFind_IgnoresCurrentDirectory(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "fisherman"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Setenv("PATH", t.TempDir()) // an empty directory: fisherman is not on PATH

	path, err := backend.Find()
	if err == nil && filepath.Dir(path) == cwd {
		t.Fatalf("Find returned %q from the current directory", path)
	}
	if err == nil {
		// A real fisherman is installed on this machine: fine, as long as
		// it is one of the absolute candidates.
		if !filepath.IsAbs(path) {
			t.Fatalf("Find returned a relative path %q", path)
		}
		return
	}
	if err != backend.ErrNotFound {
		t.Fatalf("Find = %v, want ErrNotFound", err)
	}

	// "." on PATH does not bring it back.
	t.Setenv("PATH", ".")
	if path, err := backend.Find(); err == nil && filepath.Dir(path) == cwd {
		t.Fatalf("Find returned %q through a relative PATH entry", path)
	}
}

func TestFind_OnPath(t *testing.T) {
	testbackend.Install(t)
	path, err := backend.Find()
	if err != nil || !filepath.IsAbs(path) || filepath.Base(path) != "fisherman" {
		t.Fatalf("Find = %q, %v", path, err)
	}
	if cmd, err := backend.InstallCommand(nil); err != nil || cmd.Path != path {
		t.Fatalf("InstallCommand uses %v, %v; want %s", cmd, err, path)
	}
}
