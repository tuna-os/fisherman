package disk

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/runner"
)

// These tests live in package disk so they can swap the clock, sleep, stat
// and mount seams in retag.go; nothing here sleeps for real.

type retagCall struct {
	name string
	args []string
}

// stubRunner records Run and Output calls. Output answers sfdisk reads with
// readOut/readErr.
type stubRunner struct {
	calls   []retagCall
	readOut string
	readErr error
}

func setupStubRunner(t *testing.T, readOut string, readErr error) *stubRunner {
	t.Helper()
	s := &stubRunner{readOut: readOut, readErr: readErr}
	origRun, origOut := runner.RunFn, runner.OutputFn
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		s.calls = append(s.calls, retagCall{name, args})
		return nil
	}
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		s.calls = append(s.calls, retagCall{name, args})
		if s.readErr != nil {
			return nil, s.readErr
		}
		return []byte(s.readOut), nil
	}
	t.Cleanup(func() { runner.RunFn, runner.OutputFn = origRun, origOut })
	return s
}

// writes returns the sfdisk calls that carry a type argument (i.e. writes).
func (s *stubRunner) writes() []retagCall {
	var w []retagCall
	for _, c := range s.calls {
		if c.name == "sfdisk" && len(c.args) == 4 {
			w = append(w, c)
		}
	}
	return w
}

// retagOnce mirrors main.go: read, and write only when the read says so.
func retagOnce(t *testing.T) bool {
	t.Helper()
	need, _ := PartitionTypeNeedsRewrite("/dev/loop0", 2, GPTPartTypeLinuxRootX86_64)
	if need {
		if err := SetPartitionType("/dev/loop0", 2, GPTPartTypeLinuxRootX86_64); err != nil {
			t.Fatalf("SetPartitionType: %v", err)
		}
	}
	return need
}

func TestRetagSkipsWriteWhenTypeMatches(t *testing.T) {
	// sfdisk prints the GUID upper case; the comparison must ignore case.
	s := setupStubRunner(t, strings.ToUpper(GPTPartTypeLinuxRootX86_64)+"\n", nil)
	if retagOnce(t) {
		t.Fatal("PartitionTypeNeedsRewrite = true for a matching type")
	}
	if w := s.writes(); len(w) != 0 {
		t.Fatalf("expected no sfdisk write, got %v", w)
	}
	if len(s.calls) != 1 || strings.Join(s.calls[0].args, " ") != "--part-type /dev/loop0 2" {
		t.Fatalf("expected a single read-only sfdisk call, got %v", s.calls)
	}
}

func TestRetagWritesWhenTypeDiffers(t *testing.T) {
	s := setupStubRunner(t, "0FC63DAF-8483-4772-8E79-3D69D8477DE4\n", nil)
	if !retagOnce(t) {
		t.Fatal("PartitionTypeNeedsRewrite = false for a differing type")
	}
	w := s.writes()
	if len(w) != 1 || w[0].args[3] != GPTPartTypeLinuxRootX86_64 {
		t.Fatalf("expected one sfdisk write of the root type, got %v", w)
	}
}

func TestRetagWritesWhenReadFails(t *testing.T) {
	s := setupStubRunner(t, "", errors.New("sfdisk: exit status 1"))
	if !retagOnce(t) {
		t.Fatal("PartitionTypeNeedsRewrite = false after a failed read")
	}
	if w := s.writes(); len(w) != 1 {
		t.Fatalf("expected the write to go ahead after a failed read, got %v", w)
	}
}

func TestRetagWritesWhenReadIsEmpty(t *testing.T) {
	s := setupStubRunner(t, "  \n", nil)
	if !retagOnce(t) {
		t.Fatal("PartitionTypeNeedsRewrite = false after an empty read")
	}
	if w := s.writes(); len(w) != 1 {
		t.Fatalf("expected the write to go ahead after an empty read, got %v", w)
	}
}

// fakeClock drives nowFn/sleepFn: sleeping advances time instantly.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func setupFakeClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	origNow, origSleep := nowFn, sleepFn
	nowFn = func() time.Time { return c.now }
	sleepFn = func(d time.Duration) { c.sleeps = append(c.sleeps, d); c.now = c.now.Add(d) }
	t.Cleanup(func() { nowFn, sleepFn = origNow, origSleep })
	return c
}

type fakeInfo struct{ mode fs.FileMode }

func (f fakeInfo) Name() string       { return "loop0p2" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// setupStat makes statFn report "missing" until the fake clock reaches
// appearAfter, and then mode.
func setupStat(t *testing.T, c *fakeClock, appearAfter time.Duration, mode fs.FileMode) {
	t.Helper()
	start := c.now
	orig := statFn
	statFn = func(string) (os.FileInfo, error) {
		if c.now.Sub(start) < appearAfter {
			return nil, fs.ErrNotExist
		}
		return fakeInfo{mode: mode}, nil
	}
	t.Cleanup(func() { statFn = orig })
}

func TestWaitForPartitionNodeSettlesAndSucceedsWhenNodeAppearsLate(t *testing.T) {
	s := setupStubRunner(t, "", nil)
	c := setupFakeClock(t)
	setupStat(t, c, 3*time.Second, os.ModeDevice)

	if err := WaitForPartitionNode("/dev/loop0p2", 15*time.Second); err != nil {
		t.Fatalf("WaitForPartitionNode: %v", err)
	}
	if len(s.calls) == 0 || s.calls[0].name != "udevadm" || s.calls[0].args[0] != "settle" {
		t.Fatalf("expected udevadm settle first, got %v", s.calls)
	}
	if len(c.sleeps) == 0 {
		t.Fatal("expected to poll while the node was missing")
	}
}

func TestWaitForPartitionNodeGivesUpAfterDeadline(t *testing.T) {
	setupStubRunner(t, "", nil)
	c := setupFakeClock(t)
	setupStat(t, c, time.Hour, os.ModeDevice)

	start := c.now
	err := WaitForPartitionNode("/dev/loop0p2", 10*time.Second)
	if err == nil {
		t.Fatal("expected an error after the deadline")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error should wrap the last stat error, got %v", err)
	}
	if waited := c.now.Sub(start); waited < 10*time.Second || waited > 11*time.Second {
		t.Fatalf("waited %s, want about the 10s deadline", waited)
	}
}

func TestWaitForPartitionNodeRejectsNonBlockDevice(t *testing.T) {
	setupStubRunner(t, "", nil)
	c := setupFakeClock(t)
	// A character device or a regular file at the path is not the partition.
	setupStat(t, c, 0, os.ModeDevice|os.ModeCharDevice)
	if err := WaitForPartitionNode("/dev/loop0p2", time.Second); err == nil {
		t.Fatal("expected an error for a character device")
	}
}

func setupMount(t *testing.T, failures int, err error) *int {
	t.Helper()
	calls := 0
	orig := mountFn
	mountFn = func(dev, target, opts string) error {
		calls++
		if calls <= failures {
			return err
		}
		return nil
	}
	t.Cleanup(func() { mountFn = orig })
	return &calls
}

func emptyProcMounts(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mounts")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	orig := procMountsPath
	procMountsPath = p
	t.Cleanup(func() { procMountsPath = orig })
}

func TestMountWithRetrySucceedsAfterTransientFailures(t *testing.T) {
	emptyProcMounts(t)
	c := setupFakeClock(t)
	calls := setupMount(t, 2, errors.New("exit status 32"))

	if err := MountWithRetry("/dev/loop0p2", "/mnt/t", "", 5, 500*time.Millisecond); err != nil {
		t.Fatalf("MountWithRetry: %v", err)
	}
	if *calls != 3 {
		t.Fatalf("mount called %d times, want 3", *calls)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if len(c.sleeps) != 2 || c.sleeps[0] != want[0] || c.sleeps[1] != want[1] {
		t.Fatalf("backoff = %v, want %v", c.sleeps, want)
	}
}

func TestMountWithRetryReturnsLastErrorWhenAllAttemptsFail(t *testing.T) {
	emptyProcMounts(t)
	c := setupFakeClock(t)
	last := errors.New("exit status 32")
	calls := setupMount(t, 100, last)

	err := MountWithRetry("/dev/loop0p2", "/mnt/t", "", 3, 500*time.Millisecond)
	if !errors.Is(err, last) {
		t.Fatalf("expected the last mount error, got %v", err)
	}
	if *calls != 3 {
		t.Fatalf("mount called %d times, want 3", *calls)
	}
	if len(c.sleeps) != 2 {
		t.Fatalf("expected no sleep after the final attempt, got %v", c.sleeps)
	}
}

func TestMountWithRetryAcceptsMountLeftByFailedAttempt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mounts")
	if err := os.WriteFile(p, []byte("/dev/loop0p2 /mnt/t xfs rw 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := procMountsPath
	procMountsPath = p
	t.Cleanup(func() { procMountsPath = orig })
	setupFakeClock(t)
	calls := setupMount(t, 100, errors.New("exit status 32"))

	if err := MountWithRetry("/dev/loop0p2", "/mnt/t", "", 5, time.Second); err != nil {
		t.Fatalf("MountWithRetry: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("mount called %d times, want 1", *calls)
	}
}
