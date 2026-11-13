package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/runner"
)

// recordingRunner lets `ls` (the layout probe) really run and records
// everything else as a success.
func recordingRunner(t *testing.T, fail string) *[]string {
	t.Helper()
	var calls []string
	old := runner.RunFn
	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		if name == "ls" {
			return exec.Command(name, args...).Run()
		}
		calls = append(calls, name+" "+strings.Join(args, " "))
		if fail != "" && name == fail {
			return errors.New(fail + " failed")
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = old })
	return &calls
}

// The first-boot TPM2 enrolment, driven through the install's own call:
// everything lands in the deployment the installed system boots, on both
// layouts, and nothing in the physical sysroot (tuna-os/fisherman#360).
func TestStageTPM2Enrollment_StagesInTheBootedDeployment(t *testing.T) {
	for name, fx := range map[string]deploytest.Sysroot{
		"ostree":    deploytest.Ostree(t),
		"composefs": deploytest.ComposeFs(t),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "ostree" {
				if err := os.WriteFile(filepath.Join(fx.Root, "usr", "bin", "systemd-cryptenroll"), nil, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			calls := recordingRunner(t, "")
			msg := stageTPM2Enrollment(fx.Path, "/dev/vda3", "1111-2222", "users-passphrase")
			if msg != "TPM2 auto-unlock will be enrolled on first boot" {
				t.Fatalf("message = %q", msg)
			}
			keyPath := filepath.Join(fx.Etc, "fisherman", "tpm2-enroll.key")
			key, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatalf("key in the deployment etc: %v", err)
			}
			if string(key) == "users-passphrase" || len(key) != 64 {
				t.Errorf("staged key = %q, want a random single-use key, never the passphrase (#409)", key)
			}
			for _, rel := range []string{
				"systemd/system/" + luks.EnrollUnitName,
				"systemd/system/multi-user.target.wants/" + luks.EnrollUnitName,
			} {
				if _, err := os.Lstat(filepath.Join(fx.Etc, rel)); err != nil {
					t.Errorf("%s in the deployment etc: %v", rel, err)
				}
			}
			if s := deploytest.Strays(fx); len(s) != 0 {
				t.Errorf("wrote %v into the physical sysroot", s)
			}
			// The single-use slot: a cheap KDF (its key is random), then
			// "preferred", so the first-boot lookup by key never runs the
			// user slot's memory-hard KDF (#421).
			want := []string{
				"cryptsetup luksAddKey --batch-mode --key-file=- --key-slot 31 --pbkdf pbkdf2 --pbkdf-force-iterations 1000 /dev/vda3 " + keyPath,
				"cryptsetup config /dev/vda3 --key-slot 31 --priority prefer",
			}
			if len(*calls) != len(want) || (*calls)[0] != want[0] || (*calls)[1] != want[1] {
				t.Errorf("commands = %v\nwant %v", *calls, want)
			}
		})
	}
}

// The tool is looked for in the TARGET image. An image without it gets no
// keyslot and no key file, and the log says the enrolment was skipped.
func TestStageTPM2Enrollment_TargetHasNoCryptenroll(t *testing.T) {
	fx := deploytest.Ostree(t)
	calls := recordingRunner(t, "")
	msg := stageTPM2Enrollment(fx.Path, "/dev/vda3", "1111-2222", "users-passphrase")
	if !strings.HasPrefix(msg, "TPM2 auto-unlock skipped: the installed image has no systemd-cryptenroll") ||
		!strings.Contains(msg, "still unlocks the disk") {
		t.Errorf("message = %q", msg)
	}
	if len(*calls) != 0 {
		t.Errorf("ran %v", *calls)
	}
	if _, err := os.Stat(filepath.Join(fx.Etc, "fisherman")); err == nil {
		t.Error("key staged for an image that cannot enrol")
	}
}

func TestStageTPM2Enrollment_FailureIsAWarningAndLeavesNoKey(t *testing.T) {
	fx := deploytest.ComposeFs(t)
	recordingRunner(t, "cryptsetup")
	msg := stageTPM2Enrollment(fx.Path, "/dev/vda3", "1111-2222", "users-passphrase")
	if !strings.HasPrefix(msg, "Warning: could not stage first-boot TPM2 enrollment") {
		t.Errorf("message = %q", msg)
	}
	if _, err := os.Stat(filepath.Join(fx.Etc, "fisherman")); err == nil {
		t.Error("key left behind after the keyslot could not be added")
	}

	// No deployment at all: a warning, not a panic and not a stray write.
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "ostree", "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	msg = stageTPM2Enrollment(empty, "/dev/vda3", "1111-2222", "users-passphrase")
	if !strings.HasPrefix(msg, "Warning:") {
		t.Errorf("message for a target with no deployment = %q", msg)
	}
	if _, err := os.Stat(filepath.Join(empty, "etc")); err == nil {
		t.Error("wrote <target>/etc for a target with no deployment")
	}
}
