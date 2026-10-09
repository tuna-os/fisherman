package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// The halt gate is the point of these helpers: a command built here after
// runner.Halt must fail without forking. Each of exec.Cmd's four entry points
// is checked, because they are reached independently by real callers —
// CombinedOutput by the mount diagnostics, Output by the losetup/dmsetup
// probes, Start by the podman/bootc pipelines, Run by the loop re-attach.
func assertNeverStarts(t *testing.T, label string, build func() *exec.Cmd) {
	t.Helper()

	if err := build().Run(); !errors.Is(err, ErrHalted) {
		t.Errorf("%s: Run = %v, want ErrHalted", label, err)
	}
	if _, err := build().Output(); !errors.Is(err, ErrHalted) {
		t.Errorf("%s: Output = %v, want ErrHalted", label, err)
	}
	if _, err := build().CombinedOutput(); !errors.Is(err, ErrHalted) {
		t.Errorf("%s: CombinedOutput = %v, want ErrHalted", label, err)
	}
	if err := build().Start(); !errors.Is(err, ErrHalted) {
		t.Errorf("%s: Start = %v, want ErrHalted", label, err)
	}
}

// assertAllowed checks that the halt gate did not refuse this command.
//
// It asserts "not ErrHalted" rather than "cmd.Err == nil": exec.Command sets
// cmd.Err itself when the program is not on PATH, and the hosts these tests
// run on carry neither flatpak-spawn nor, necessarily, umount. A lookup error
// is an environment fact; only ErrHalted is a decision by the gate.
func assertAllowed(t *testing.T, label string, cmd *exec.Cmd) {
	t.Helper()
	if errors.Is(cmd.Err, ErrHalted) {
		t.Errorf("%s: cmd.Err = %v, want the gate to allow it (teardown is allowed)", label, cmd.Err)
	}
}

func TestHostCommand_PassesThroughBeforeHalt(t *testing.T) {
	resetHaltForTest()

	// A PATH we control, as in TestDefaultRunStreamsAndHonorsArgs, so the
	// test does not depend on PATH order.
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	dir := t.TempDir()
	if err := os.Symlink(shPath, filepath.Join(dir, "sh")); err != nil {
		t.Fatalf("symlinking sh: %v", err)
	}
	t.Setenv("PATH", dir)

	cmd := HostCommand("sh", "-c", "exit 0")
	if cmd.Err != nil {
		t.Fatalf("before Halt: cmd.Err = %v, want nil", cmd.Err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("before Halt: Run = %v, want success", err)
	}
}

// After Halt a pipeline command must not reach the host. This is the case the
// raw exec.Command callers used to miss: runner.Run already refused, but the
// hand-built command beside it ran anyway.
func TestHostCommand_NeverStartsAfterHalt(t *testing.T) {
	resetHaltForTest()
	Halt()
	t.Cleanup(resetHaltForTest)

	assertNeverStarts(t, "mount after Halt", func() *exec.Cmd {
		return HostCommand("mount", "/dev/sda3", "/mnt/root")
	})
}

// Teardown is what Halt exists to protect, so the allowlist must survive the
// trip through these helpers: refusing umount would leave the disk mounted.
func TestHostCommand_AllowsTeardownAfterHalt(t *testing.T) {
	resetHaltForTest()
	Halt()
	t.Cleanup(resetHaltForTest)

	assertAllowed(t, "umount after Halt", HostCommand("umount", "-Rl", "/mnt/root"))
}

func TestWrappedCommand_NeverStartsAfterHalt(t *testing.T) {
	resetHaltForTest()
	Halt()
	t.Cleanup(resetHaltForTest)

	// The shape used by callers that need the effective argv first.
	name, args := HostArgs("losetup", []string{"-d", "/dev/loop9"})
	assertNeverStarts(t, "losetup after Halt", func() *exec.Cmd {
		return WrappedCommand(name, args...)
	})
}

// Inside a sandbox the program being executed is flatpak-spawn, so a gate
// keyed on the executed name would read every command as "flatpak-spawn":
// pipeline commands would slip past the halt, and teardown commands would be
// refused. Both directions are checked.
func TestWrappedCommand_GateSeesOriginalNameInSandbox(t *testing.T) {
	resetHaltForTest()
	inFlatpakFn = func() bool { return true }
	Halt()
	t.Cleanup(func() {
		resetHaltForTest()
		inFlatpakFn = inFlatpak
	})

	name, args := HostArgs("mount", []string{"/dev/sda3", "/mnt/root"})
	if name != "flatpak-spawn" {
		t.Fatalf("HostArgs in sandbox = %q, want flatpak-spawn", name)
	}
	if err := WrappedCommand(name, args...).Err; !errors.Is(err, ErrHalted) {
		t.Errorf("wrapped mount after Halt: cmd.Err = %v, want ErrHalted", err)
	}

	tname, targs := HostArgs("umount", []string{"-Rl", "/mnt/root"})
	assertAllowed(t, "wrapped umount after Halt", WrappedCommand(tname, targs...))
}

// HostArgsWithEnv inserts --env= flags between --host and the program name,
// so the unwrap has to skip them to find the program.
func TestWrappedCommand_GateSkipsEnvFlags(t *testing.T) {
	resetHaltForTest()
	inFlatpakFn = func() bool { return true }
	Halt()
	t.Cleanup(func() {
		resetHaltForTest()
		inFlatpakFn = inFlatpak
	})

	name, args := HostArgsWithEnv("bootc", []string{"install"}, []string{"TMPDIR=/var/fisherman-tmp"})
	if err := WrappedCommand(name, args...).Err; !errors.Is(err, ErrHalted) {
		t.Errorf("wrapped bootc after Halt: cmd.Err = %v, want ErrHalted", err)
	}
}

func TestUnwrapHostArgs(t *testing.T) {
	tests := []struct {
		label    string
		name     string
		args     []string
		wantName string
		wantArgs []string
	}{
		{
			label:    "unwrapped argv is returned unchanged",
			name:     "mount",
			args:     []string{"/dev/sda3", "/mnt/root"},
			wantName: "mount",
			wantArgs: []string{"/dev/sda3", "/mnt/root"},
		},
		{
			label:    "flatpak-spawn --host",
			name:     "flatpak-spawn",
			args:     []string{"--host", "mount", "/dev/sda3", "/mnt/root"},
			wantName: "mount",
			wantArgs: []string{"/dev/sda3", "/mnt/root"},
		},
		{
			label:    "flatpak-spawn --host with env flags",
			name:     "flatpak-spawn",
			args:     []string{"--host", "--env=TMPDIR=/var/tmp", "--env=A=b", "bootc", "install"},
			wantName: "bootc",
			wantArgs: []string{"install"},
		},
		{
			label:    "program with no arguments",
			name:     "flatpak-spawn",
			args:     []string{"--host", "sync"},
			wantName: "sync",
			wantArgs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			gotName, gotArgs := unwrapHostArgs(tt.name, tt.args)
			if gotName != tt.wantName {
				t.Errorf("name = %q, want %q", gotName, tt.wantName)
			}
			if len(gotArgs) != 0 || len(tt.wantArgs) != 0 {
				if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
					t.Errorf("args = %v, want %v", gotArgs, tt.wantArgs)
				}
			}
		})
	}
}
