package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCheckHalted_AllowsEverythingBeforeHalt(t *testing.T) {
	t.Cleanup(resetHaltForTest)
	resetHaltForTest()
	if err := checkHalted("mkfs.xfs", []string{"/dev/sda3"}); err != nil {
		t.Fatalf("before Halt: %v", err)
	}
}

func TestCheckHalted_RefusesPipelineAllowsTeardown(t *testing.T) {
	t.Cleanup(resetHaltForTest)
	Halt()
	if !Halted() {
		t.Fatal("Halted() = false after Halt()")
	}

	refused := [][]string{
		{"mkfs.xfs", "/dev/sda3"},
		{"mount", "/dev/sda3", "/mnt"},
		{"sfdisk", "/dev/sda"},
		{"bootc", "install"},
		{"cryptsetup", "luksFormat", "/dev/sda3"},
		{"cryptsetup", "open", "/dev/sda3", "root"},
	}
	for _, c := range refused {
		if err := checkHalted(c[0], c[1:]); !errors.Is(err, ErrHalted) {
			t.Errorf("%v after Halt: got %v, want ErrHalted", c, err)
		}
	}

	allowed := [][]string{
		{"umount", "-Rl", "/mnt"},
		{"fuser", "-km", "/dev/mapper/x"},
		{"blockdev", "--flushbufs", "/dev/mapper/x"},
		{"udevadm", "settle"},
		{"cryptsetup", "luksClose", "x"},
		{"cryptsetup", "close", "x"},
	}
	for _, c := range allowed {
		if err := checkHalted(c[0], c[1:]); err != nil {
			t.Errorf("%v after Halt: teardown command refused: %v", c, err)
		}
	}
}

func TestDefaultExecutor_HaltedCommandNeverStarts(t *testing.T) {
	t.Cleanup(resetHaltForTest)
	Halt()
	cmd := DefaultExecutor.Command("mkfs.xfs", "/dev/null")
	if err := cmd.Run(); !errors.Is(err, ErrHalted) {
		t.Fatalf("Run: got %v, want ErrHalted", err)
	}
	if _, err := DefaultOutput("lsblk"); !errors.Is(err, ErrHalted) {
		t.Fatalf("DefaultOutput: got %v, want ErrHalted", err)
	}
	if err := DefaultRun(nil, "sfdisk", "/dev/null"); !errors.Is(err, ErrHalted) {
		t.Fatalf("DefaultRun: got %v, want ErrHalted", err)
	}
}

// TestReadStat_CommandNameWithParens checks the parser splits after the last
// ')' so a process named "a) (b" does not shift the fields.
func TestReadStat_CommandNameWithParens(t *testing.T) {
	dir := t.TempDir()
	old := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = old })

	if err := os.MkdirAll(filepath.Join(dir, "42"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Fields 3..22: state ppid pgrp session tty tpgid flags minflt cminflt
	// majflt cmajflt utime stime cutime cstime priority nice threads
	// itrealvalue starttime.
	stat := "42 (a) (b) S 7 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 98765 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "42", "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	ppid, state, start, ok := readStat(42)
	if !ok || ppid != 7 || state != "S" || start != "98765" {
		t.Fatalf("readStat = %d %q %q %v, want 7 S 98765 true", ppid, state, start, ok)
	}
}

// TestStopDescendants_StopsGrandchildren starts a shell that starts a sleep,
// and checks both are gone after StopDescendants, including the grandchild,
// which fisherman never spawned directly.
func TestStopDescendants_StopsGrandchildren(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60 & wait")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sh: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	grandchild := waitForDescendants(t, 2)

	if killed := StopDescendants(5 * time.Second); killed != 0 {
		t.Errorf("StopDescendants killed %d with SIGKILL; both obey SIGTERM", killed)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shell still running after StopDescendants")
	}
	waitGone(t, grandchild)
}

// TestStopDescendants_EscalatesToSIGKILL covers a child that ignores SIGTERM.
func TestStopDescendants_EscalatesToSIGKILL(t *testing.T) {
	cmd := exec.Command("sh", "-c", `trap "" TERM; while :; do sleep 1; done`)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sh: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	waitForDescendants(t, 1)

	start := time.Now()
	if killed := StopDescendants(300 * time.Millisecond); killed < 1 {
		t.Errorf("StopDescendants killed %d with SIGKILL, want >= 1", killed)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Error("SIGKILL sent before the grace period elapsed")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM-ignoring child survived StopDescendants")
	}
}

// waitForDescendants waits until at least n descendants exist and returns
// the deepest one found.
func waitForDescendants(t *testing.T, n int) procEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d := descendants(os.Getpid()); len(d) >= n {
			return d[len(d)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fewer than %d descendants appeared", n)
	return procEntry{}
}

func waitGone(t *testing.T, e procEntry) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(e) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(e.pid, syscall.SIGKILL)
	t.Fatalf("process %d still alive", e.pid)
}
