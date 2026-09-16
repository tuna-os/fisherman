package runner

// Tests for the flatpak-spawn wrapping logic (HostArgs, HostArgsWithEnv,
// useHost, defaultExecutor) plus the Output seam and DefaultRun/
// DefaultOutput error wrapping. These live in the internal package so they
// can swap inFlatpakFn (the sandbox detector) and reach useHost directly —
// the file's own RunFn/OutputFn pattern, applied to the detector.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeFlatpak runs t with the sandbox detector forced to want.
func fakeFlatpak(t *testing.T, want bool) {
	t.Helper()
	orig := inFlatpakFn
	inFlatpakFn = func() bool { return want }
	t.Cleanup(func() { inFlatpakFn = orig })
}

func TestUseHostDecisionTable(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Bundled inside the sandbox — run directly (block devices via --device=all).
		{"mkfs.fat", false},
		{"mkfs.vfat", false},
		{"mkfs.ext4", false},
		{"mkfs.ext3", false},
		{"mkfs.ext2", false},
		{"mkfs.xfs", false},
		{"mkfs.btrfs", false},
		{"btrfs", false},
		{"mkswap", false},
		// Privileged / host-only tools — forward via flatpak-spawn --host.
		{"sfdisk", true},
		{"cryptsetup", true},
		{"mount", true},
		{"podman", true},
		// Anything not in the local set defaults to host.
		{"echo", true},
		{"ls", true},
	}
	for _, tc := range cases {
		if got := useHost(tc.name); got != tc.want {
			t.Errorf("useHost(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHostArgsOutsideFlatpak(t *testing.T) {
	fakeFlatpak(t, false)
	name, args := HostArgs("sfdisk", []string{"-d", "/dev/sda"})
	if name != "sfdisk" || !reflect.DeepEqual(args, []string{"-d", "/dev/sda"}) {
		t.Errorf("HostArgs outside flatpak = (%q, %v), want passthrough", name, args)
	}
}

func TestHostArgsInsideFlatpak(t *testing.T) {
	fakeFlatpak(t, true)

	name, args := HostArgs("sfdisk", []string{"-d", "/dev/sda"})
	if name != "flatpak-spawn" {
		t.Errorf("host tool name = %q, want flatpak-spawn", name)
	}
	want := []string{"--host", "sfdisk", "-d", "/dev/sda"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("host tool args = %v, want %v", args, want)
	}

	// Local (bundled) tools are NOT forwarded.
	name, args = HostArgs("mkfs.fat", []string{"/dev/sda1"})
	if name != "mkfs.fat" || !reflect.DeepEqual(args, []string{"/dev/sda1"}) {
		t.Errorf("local tool = (%q, %v), want passthrough", name, args)
	}
}

func TestHostArgsWithEnvOutsideFlatpak(t *testing.T) {
	fakeFlatpak(t, false)
	// Outside a sandbox the env vars are the caller's problem (set on
	// cmd.Env); HostArgsWithEnv must not touch name/args.
	name, args := HostArgsWithEnv("podman", []string{"run", "x"}, []string{"TMPDIR=/t", "A=1"})
	if name != "podman" || !reflect.DeepEqual(args, []string{"run", "x"}) {
		t.Errorf("HostArgsWithEnv outside flatpak = (%q, %v), want passthrough", name, args)
	}
}

func TestHostArgsWithEnvInsideFlatpak(t *testing.T) {
	fakeFlatpak(t, true)

	name, args := HostArgsWithEnv(
		"podman", []string{"run", "--rm", "img"},
		[]string{"TMPDIR=/tmp/x", "CONTAINERS_STORAGE_CONF=/tmp/storage.conf"},
	)
	if name != "flatpak-spawn" {
		t.Errorf("name = %q, want flatpak-spawn", name)
	}
	want := []string{
		"--host",
		"--env=TMPDIR=/tmp/x",
		"--env=CONTAINERS_STORAGE_CONF=/tmp/storage.conf",
		"podman", "run", "--rm", "img",
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestHostArgsWithEnvInsideFlatpakNoEnvVars(t *testing.T) {
	fakeFlatpak(t, true)
	// Zero env vars: exactly one --host flag, no --env flags.
	name, args := HostArgsWithEnv("cryptsetup", []string{"open"}, nil)
	if name != "flatpak-spawn" {
		t.Errorf("name = %q, want flatpak-spawn", name)
	}
	want := []string{"--host", "cryptsetup", "open"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}

	// Local tools stay local even with env vars present.
	name, args = HostArgsWithEnv("btrfs", []string{"subvolume", "list", "/mnt"}, []string{"TMPDIR=/t"})
	if name != "btrfs" || !reflect.DeepEqual(args, []string{"subvolume", "list", "/mnt"}) {
		t.Errorf("local tool with env = (%q, %v), want passthrough", name, args)
	}
}

func TestInFlatpakReflectsDetector(t *testing.T) {
	fakeFlatpak(t, true)
	if !InFlatpak() {
		t.Error("InFlatpak() = false, want true with detector forced on")
	}
	fakeFlatpak(t, false)
	if InFlatpak() {
		t.Error("InFlatpak() = true, want false with detector forced off")
	}
}

func TestDefaultExecutorAppliesHostArgs(t *testing.T) {
	// defaultExecutor.Command must route through HostArgs: inside a flatpak
	// a host tool becomes flatpak-spawn; outside it stays as-is.
	cmd := DefaultExecutor.Command("sfdisk", "-d", "/dev/sda")
	rc, ok := cmd.(*realCommand)
	if !ok {
		t.Fatalf("Command() returned %T, want *realCommand", cmd)
	}
	// Outside flatpak (this test process): no wrapping.
	if rc.Args[0] != "sfdisk" {
		t.Errorf("executor args = %v, want sfdisk first", rc.Args)
	}

	fakeFlatpak(t, true)
	cmd = DefaultExecutor.Command("cryptsetup", "open")
	rc = cmd.(*realCommand)
	if rc.Args[0] != "flatpak-spawn" || len(rc.Args) < 2 || rc.Args[1] != "--host" {
		t.Errorf("executor inside flatpak args = %v, want flatpak-spawn --host …", rc.Args)
	}
}

func TestOutputFn_SubstitutionAndErrorPropagation(t *testing.T) {
	wantErr := errors.New("boom")
	runnerOutputCalls := 0

	orig := OutputFn
	OutputFn = func(name string, args ...string) ([]byte, error) {
		runnerOutputCalls++
		if name == "fail" {
			return nil, wantErr
		}
		return []byte("out"), nil
	}
	t.Cleanup(func() { OutputFn = orig })

	out, err := Output("echo", "hi")
	if err != nil || string(out) != "out" {
		t.Errorf("Output = (%q, %v), want (\"out\", nil)", out, err)
	}
	if _, err := Output("fail"); err != wantErr {
		t.Errorf("Output(fail) error = %v, want %v", err, wantErr)
	}
	if runnerOutputCalls != 2 {
		t.Errorf("OutputFn called %d times, want 2", runnerOutputCalls)
	}
}

// emptyPATH returns a PATH that contains no executables, so DefaultRun /
// DefaultOutput hit exec-not-found deterministically (no reliance on the
// host's binaries, and no risk of running a real command).
func emptyPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestDefaultRunErrorWrapsCommandName(t *testing.T) {
	emptyPATH(t)
	err := DefaultRun(nil, "tbox-definitely-not-a-real-binary")
	if err == nil {
		t.Fatal("DefaultRun with a missing binary must error")
	}
	if !strings.Contains(err.Error(), "tbox-definitely-not-a-real-binary") {
		t.Errorf("error = %v, want it to name the command", err)
	}
}

func TestDefaultOutputErrorWrapsCommandName(t *testing.T) {
	emptyPATH(t)
	_, err := DefaultOutput("tbox-definitely-not-a-real-binary", "--flag")
	if err == nil {
		t.Fatal("DefaultOutput with a missing binary must error")
	}
	if !strings.Contains(err.Error(), "tbox-definitely-not-a-real-binary") {
		t.Errorf("error = %v, want it to name the command", err)
	}
	if !strings.Contains(err.Error(), "--flag") {
		t.Errorf("error = %v, want it to include the args", err)
	}
}

func TestDefaultRunStreamsAndHonorsArgs(t *testing.T) {
	// DefaultRun with a real, harmless binary: /bin/sh -c 'exit 0' via a
	// PATH we control. We only assert success and that non-zero exits error.
	dir := t.TempDir()
	// Use sh from a known location so we don't depend on PATH order.
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	_ = os.Symlink(shPath, filepath.Join(dir, "sh"))
	t.Setenv("PATH", dir)

	if err := DefaultRun(nil, "sh", "-c", "exit 0"); err != nil {
		t.Errorf("DefaultRun exit-0: %v", err)
	}
	if err := DefaultRun(nil, "sh", "-c", "exit 3"); err == nil {
		t.Error("DefaultRun exit-3: expected an error")
	}
}
