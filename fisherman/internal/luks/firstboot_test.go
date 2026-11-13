package luks_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy"
	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/runner"
)

const (
	fbUUID       = "0a1b2c3d-1111-2222-3333-444455556666"
	fbPart       = "/dev/vda3"
	fbPassphrase = "the-users-own-passphrase"
)

func resolve(t *testing.T, fx deploytest.Sysroot) *deploy.Deployment {
	t.Helper()
	d, err := deploy.Resolve(fx.Path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func withCryptenroll(t *testing.T, fx deploytest.Sysroot) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fx.Root, "usr", "bin", "systemd-cryptenroll"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

// filesUnder lists every regular file and symlink below dir.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertStagedIn checks that the key, the unit and its enablement are all in
// the deployment's /etc, and that nothing was written to the physical
// sysroot (tuna-os/fisherman#360).
func assertStagedIn(t *testing.T, fx deploytest.Sysroot, rec *recorder) {
	t.Helper()
	if s := deploytest.Strays(fx); len(s) != 0 {
		t.Errorf("wrote %v into the physical sysroot, which no booted system reads", s)
	}

	keyPath := filepath.Join(fx.Etc, "fisherman", "tpm2-enroll.key")
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key perm %o, want 600", fi.Mode().Perm())
	}
	if di, err := os.Stat(filepath.Dir(keyPath)); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("key dir: %v, perm %v, want 700", err, di.Mode().Perm())
	}
	key, _ := os.ReadFile(keyPath)
	if len(key) != 64 {
		t.Errorf("single-use key is %d bytes, want 64 hex characters", len(key))
	}

	// The user's passphrase is nowhere on the target (#409).
	for _, f := range filesUnder(t, fx.Path) {
		if fi, err := os.Lstat(f); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, _ := os.ReadFile(f)
		if strings.Contains(string(data), fbPassphrase) {
			t.Errorf("%s holds the user's passphrase", f)
		}
	}

	unitPath := filepath.Join(fx.Etc, "systemd", "system", luks.EnrollUnitName)
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("unit: %v", err)
	}
	if string(unit) != luks.EnrollUnit(fbUUID) {
		t.Error("staged unit differs from EnrollUnit")
	}
	link := filepath.Join(fx.Etc, "systemd", "system", "multi-user.target.wants", luks.EnrollUnitName)
	dest, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("wants symlink: %v", err)
	}
	if dest != "/etc/systemd/system/"+luks.EnrollUnitName {
		t.Errorf("wants symlink -> %q, want the unit in /etc/systemd/system (not the sealed /usr)", dest)
	}

	// Two commands: cryptsetup luksAddKey (existing passphrase on stdin, the
	// new key from the staged file, in the pinned slot, with a cheap KDF),
	// then the slot is made "preferred" so a lookup by its key matches it
	// before the user slot's memory-hard KDF runs (#421).
	if len(rec.calls) != 2 {
		t.Fatalf("calls = %+v, want luksAddKey then config --priority prefer", rec.calls)
	}
	c := rec.calls[0]
	want := []string{"luksAddKey", "--batch-mode", "--key-file=-", "--key-slot", "31",
		"--pbkdf", "pbkdf2", "--pbkdf-force-iterations", "1000", fbPart, keyPath}
	if c.name != "cryptsetup" || !equalSlice(c.args, want) {
		t.Errorf("command = %s %v\nwant cryptsetup %v", c.name, c.args, want)
	}
	prefer := []string{"config", fbPart, "--key-slot", "31", "--priority", "prefer"}
	if rec.calls[1].name != "cryptsetup" || !equalSlice(rec.calls[1].args, prefer) || rec.calls[1].stdin != "" {
		t.Errorf("second command = %+v, want cryptsetup %v", rec.calls[1], prefer)
	}
	if c.stdin != fbPassphrase {
		t.Errorf("stdin = %q, want the unlocking passphrase", c.stdin)
	}
	for _, a := range c.args {
		if strings.Contains(a, fbPassphrase) || strings.Contains(a, string(key)) {
			t.Errorf("a secret is in argv: %q", a)
		}
	}
}

func TestStageFirstBootEnrollment_Ostree(t *testing.T) {
	fx := deploytest.Ostree(t)
	withCryptenroll(t, fx)
	rec := setup(t)
	if err := luks.StageFirstBootEnrollment(resolve(t, fx), fbPart, fbUUID, fbPassphrase); err != nil {
		t.Fatal(err)
	}
	assertStagedIn(t, fx, rec)
	// Not in the deployment's /usr either: that tree is hard-linked into
	// the ostree repository.
	if _, err := os.Stat(filepath.Join(fx.Root, "usr", "lib", "systemd", "system", luks.EnrollUnitName)); err == nil {
		t.Error("unit written under the deployment's /usr")
	}
}

func TestStageFirstBootEnrollment_ComposeFs(t *testing.T) {
	fx := deploytest.ComposeFs(t)
	rec := setup(t)
	// /usr cannot be inspected on composefs: the unit is staged and cleans
	// up after itself when the tool is missing.
	if err := luks.StageFirstBootEnrollment(resolve(t, fx), fbPart, fbUUID, fbPassphrase); err != nil {
		t.Fatal(err)
	}
	assertStagedIn(t, fx, rec)
}

// The check is for the tool in the TARGET image, not on the installer host.
func TestStageFirstBootEnrollment_TargetWithoutCryptenroll(t *testing.T) {
	fx := deploytest.Ostree(t)
	rec := setup(t)
	before := filesUnder(t, fx.Path)
	err := luks.StageFirstBootEnrollment(resolve(t, fx), fbPart, fbUUID, fbPassphrase)
	if !errors.Is(err, luks.ErrNoCryptenroll) {
		t.Fatalf("err = %v, want ErrNoCryptenroll", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("ran %+v for an image that cannot enrol", rec.calls)
	}
	if after := filesUnder(t, fx.Path); !equalSlice(before, after) {
		t.Errorf("target changed:\nbefore %v\nafter  %v", before, after)
	}
}

func TestStageFirstBootEnrollment_TransientEtc(t *testing.T) {
	fx := deploytest.Ostree(t)
	withCryptenroll(t, fx)
	conf := filepath.Join(fx.Root, "usr", "lib", "ostree", "prepare-root.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("[etc]\ntransient = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := setup(t)
	err := luks.StageFirstBootEnrollment(resolve(t, fx), fbPart, fbUUID, fbPassphrase)
	if !errors.Is(err, luks.ErrTransientEtc) {
		t.Fatalf("err = %v, want ErrTransientEtc", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("ran %+v", rec.calls)
	}
	if _, err := os.Stat(filepath.Join(fx.Etc, "fisherman")); err == nil {
		t.Error("key directory created for a transient /etc")
	}
}

// A keyslot that could not be added leaves no key file behind.
func TestStageFirstBootEnrollment_AddKeyFails(t *testing.T) {
	fx := deploytest.Ostree(t)
	withCryptenroll(t, fx)
	rec := setup(t)
	rec.err = errors.New("No key available with this passphrase")
	err := luks.StageFirstBootEnrollment(resolve(t, fx), fbPart, fbUUID, fbPassphrase)
	if err == nil {
		t.Fatal("no error although luksAddKey failed")
	}
	if _, serr := os.Stat(filepath.Join(fx.Etc, "fisherman")); serr == nil {
		t.Error("key file or directory left behind after a failed luksAddKey")
	}
	if _, serr := os.Lstat(filepath.Join(fx.Etc, "systemd", "system", luks.EnrollUnitName)); serr == nil {
		t.Error("unit left behind after a failed luksAddKey")
	}
	for _, c := range rec.calls {
		if len(c.args) > 0 && c.args[0] == "luksRemoveKey" {
			t.Errorf("tried to remove a slot that was never added: %v", c.args)
		}
	}
}

// A unit that cannot be written rolls the keyslot back.
func TestStageFirstBootEnrollment_UnitWriteFailsRemovesSlot(t *testing.T) {
	fx := deploytest.Ostree(t)
	withCryptenroll(t, fx)
	// etc/systemd/system/multi-user.target.wants is a file: MkdirAll fails.
	if err := os.WriteFile(filepath.Join(fx.Etc, "systemd", "system", "multi-user.target.wants"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := setup(t)
	if err := luks.StageFirstBootEnrollment(resolve(t, fx), fbPart, fbUUID, fbPassphrase); err == nil {
		t.Fatal("no error although the unit could not be enabled")
	}
	keyPath := filepath.Join(fx.Etc, "fisherman", "tpm2-enroll.key")
	if len(rec.calls) != 3 || rec.calls[2].name != "cryptsetup" ||
		!equalSlice(rec.calls[2].args, []string{"luksRemoveKey", fbPart, keyPath}) {
		t.Errorf("calls = %+v, want luksAddKey, config, then luksRemoveKey of the single-use key", rec.calls)
	}
	if _, err := os.Stat(filepath.Join(fx.Etc, "fisherman")); err == nil {
		t.Error("key left behind")
	}
}

func TestStageFirstBootEnrollment_Arguments(t *testing.T) {
	fx := deploytest.Ostree(t)
	withCryptenroll(t, fx)
	setup(t)
	if err := luks.StageFirstBootEnrollment(resolve(t, fx), fbPart, "", fbPassphrase); err == nil {
		t.Error("expected error for empty UUID")
	}
	if err := luks.StageFirstBootEnrollment(nil, fbPart, fbUUID, fbPassphrase); err == nil {
		t.Error("expected error for a nil deployment")
	}
}

// The unit's contract (#409): bounded retries, and the slot and the key file
// are removed in ExecStopPost, which systemd runs on failure too.
// ExecStartPost (the old removal) runs only after a successful ExecStart.
func TestEnrollUnit(t *testing.T) {
	u := luks.EnrollUnit(fbUUID)
	dev := "/dev/disk/by-uuid/" + fbUUID

	var start, stopPost []string
	for _, line := range strings.Split(u, "\n") {
		switch {
		case strings.HasPrefix(line, "ExecStartPost="):
			t.Errorf("ExecStartPost does not run when the enrolment fails: %s", line)
		case strings.HasPrefix(line, "ExecStart="):
			start = append(start, line)
		case strings.HasPrefix(line, "ExecStopPost="):
			stopPost = append(stopPost, line)
		}
	}
	if len(start) != 1 {
		t.Fatalf("ExecStart lines = %v", start)
	}
	for _, want := range []string{
		"for attempt in 1 2 3; do",
		"systemd-cryptenroll --tpm2-device=auto --tpm2-pcrs=7 --unlock-key-file=/etc/fisherman/tpm2-enroll.key " + dev,
		"exit 1",
	} {
		if !strings.Contains(start[0], want) {
			t.Errorf("ExecStart missing %q: %s", want, start[0])
		}
	}
	// systemd expands $VAR in Exec lines before the shell sees it.
	if strings.Contains(u, "$") {
		t.Error("unit uses $, which systemd would expand")
	}
	if strings.Contains(u, "shred") {
		t.Error("shred does nothing useful on btrfs or an SSD; the keyslot is removed instead")
	}

	if len(stopPost) != 2 {
		t.Fatalf("ExecStopPost lines = %v", stopPost)
	}
	// Slot first: by its key (cheap and preferred, AddKeyFile), then by
	// its number, then with systemd-cryptenroll. stdin is /dev/null, so no
	// step waits on it. Each removal is bounded, so the file removal after
	// it always runs (#421).
	for _, want := range []string{
		"timeout 60 cryptsetup luksRemoveKey " + dev + " /etc/fisherman/tpm2-enroll.key </dev/null || ",
		"timeout 60 cryptsetup -q luksKillSlot " + dev + " 31 </dev/null || ",
		"timeout 60 systemd-cryptenroll --unlock-key-file=/etc/fisherman/tpm2-enroll.key --wipe-slot=31 " + dev,
	} {
		if !strings.Contains(stopPost[0], want) {
			t.Errorf("first ExecStopPost missing %q: %s", want, stopPost[0])
		}
	}
	if !strings.Contains(u, "\nTimeoutStopSec=180\n") {
		t.Error("TimeoutStopSec must leave room for both bounded removals (2 × 60 s)")
	}
	if !strings.Contains(stopPost[1], "rm -f /etc/fisherman/tpm2-enroll.key") ||
		!strings.Contains(stopPost[1], "multi-user.target.wants/"+luks.EnrollUnitName) {
		t.Errorf("second ExecStopPost must remove the key file and the enablement: %s", stopPost[1])
	}
	for _, l := range stopPost {
		// "-": a failed cleanup command must not skip the next one.
		if !strings.HasPrefix(l, "ExecStopPost=-") {
			t.Errorf("ExecStopPost without the ignore-failure prefix: %s", l)
		}
	}
	for _, want := range []string{
		"ConditionPathExists=/etc/fisherman/tpm2-enroll.key",
		"Type=oneshot",
		"TimeoutStartSec=",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q", want)
		}
	}
}

func TestAddAndRemoveKeyFile(t *testing.T) {
	rec := setup(t)
	if err := luks.AddKeyFile("/dev/sda3", "pw", "/t/key", 31); err != nil {
		t.Fatal(err)
	}
	if err := luks.RemoveKeyFile("/dev/sda3", "/t/key"); err != nil {
		t.Fatal(err)
	}
	if !equalSlice(rec.calls[1].args, []string{"config", "/dev/sda3", "--key-slot", "31", "--priority", "prefer"}) {
		t.Errorf("AddKeyFile does not prefer the slot: %+v", rec.calls[1])
	}
	if !equalSlice(rec.calls[2].args, []string{"luksRemoveKey", "/dev/sda3", "/t/key"}) || rec.calls[2].stdin != "" {
		t.Errorf("RemoveKeyFile = %+v", rec.calls[2])
	}
}

// A slot that cannot be made preferred is removed again: the caller asked
// for one it can remove without the user slot's memory-hard KDF (#421).
func TestAddKeyFile_PreferFailsRemovesSlot(t *testing.T) {
	calls := 0
	var seen [][]string
	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		calls++
		seen = append(seen, append([]string{name}, args...))
		if calls == 2 {
			return errors.New("config failed")
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })
	if err := luks.AddKeyFile("/dev/sda3", "pw", "/t/key", 31); err == nil {
		t.Fatal("no error although the slot could not be made preferred")
	}
	if len(seen) != 3 || !equalSlice(seen[2], []string{"cryptsetup", "luksRemoveKey", "/dev/sda3", "/t/key"}) {
		t.Errorf("calls = %v, want luksAddKey, config, luksRemoveKey", seen)
	}
}
