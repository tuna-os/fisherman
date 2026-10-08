package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// recorder is a terminator with every side effect replaced by a recording.
type recorder struct {
	mu       sync.Mutex
	calls    []string
	errors   []string
	exits    []int
	parks    int
	stderr   bytes.Buffer
	exited   chan int
	cleanupF func() error
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func newRecorded(t *testing.T) (*terminator, *recorder) {
	t.Helper()
	r := &recorder{exited: make(chan int, 4)}
	tm := newTerminator()
	tm.halt = func() { r.add("halt") }
	tm.stopChildren = func() int { r.add("stopChildren"); return 0 }
	tm.cleanup = func() error {
		r.add("cleanup")
		if r.cleanupF != nil {
			return r.cleanupF()
		}
		return nil
	}
	tm.emitError = func(msg string) {
		r.mu.Lock()
		r.errors = append(r.errors, msg)
		r.mu.Unlock()
		r.add("error")
	}
	tm.stderr = &syncWriter{w: &r.stderr, mu: &r.mu}
	tm.exit = func(code int) {
		r.mu.Lock()
		r.exits = append(r.exits, code)
		r.mu.Unlock()
		r.add("exit")
		r.exited <- code
	}
	tm.park = func() {
		r.mu.Lock()
		r.parks++
		r.mu.Unlock()
	}
	tm.settle = 20 * time.Millisecond
	return tm, r
}

type syncWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (r *recorder) snapshot() (calls, errs []string, exits []int, parks int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...), append([]string(nil), r.errors...), append([]int(nil), r.exits...), r.parks
}

func waitExit(t *testing.T, r *recorder) int {
	t.Helper()
	select {
	case code := <-r.exited:
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("terminator never exited")
		return -1
	}
}

func TestExitCodesAreDistinct(t *testing.T) {
	codes := map[int]string{0: "success"}
	for name, c := range map[string]int{"exitFailure": exitFailure, "exitUsage": exitUsage, "exitCancelled": exitCancelled} {
		if prev, dup := codes[c]; dup {
			t.Errorf("%s = %d collides with %s", name, c, prev)
		}
		codes[c] = name
	}
	if exitCancelled != 130 {
		t.Errorf("exitCancelled = %d; frontends and docs rely on 130", exitCancelled)
	}
}

func TestFail_TeardownThenErrorThenExit1(t *testing.T) {
	tm, r := newRecorded(t)
	tm.fail("partitioning disk: device busy")

	calls, errs, exits, _ := r.snapshot()
	want := []string{"halt", "stopChildren", "cleanup", "error", "exit"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", calls, want)
	}
	if len(errs) != 1 || errs[0] != "partitioning disk: device busy" {
		t.Errorf("error events = %q", errs)
	}
	if len(exits) != 1 || exits[0] != exitFailure {
		t.Errorf("exits = %v, want [%d]", exits, exitFailure)
	}
}

func TestSignal_CancelRunsTeardownAndExits130(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(signalName(sig), func(t *testing.T) {
			tm, r := newRecorded(t)
			tm.onSignal(sig)

			calls, errs, exits, _ := r.snapshot()
			want := []string{"halt", "stopChildren", "cleanup", "error", "exit"}
			if strings.Join(calls, ",") != strings.Join(want, ",") {
				t.Errorf("order = %v, want %v", calls, want)
			}
			wantMsg := "installation cancelled (" + signalName(sig) + ")"
			if len(errs) != 1 || errs[0] != wantMsg {
				t.Errorf("error events = %q, want [%q]", errs, wantMsg)
			}
			if len(exits) != 1 || exits[0] != exitCancelled {
				t.Errorf("exits = %v, want [%d]", exits, exitCancelled)
			}
		})
	}
}

// A second signal while teardown is running is logged and ignored: teardown
// finishes, and there is still one error event and one exit.
func TestSignal_SecondSignalDoesNotInterruptTeardown(t *testing.T) {
	tm, r := newRecorded(t)
	started := make(chan struct{})
	release := make(chan struct{})
	r.cleanupF = func() error { close(started); <-release; return nil }

	go tm.onSignal(syscall.SIGTERM)
	<-started
	tm.onSignal(syscall.SIGTERM) // returns at once
	tm.onSignal(syscall.SIGINT)
	close(release)

	if code := waitExit(t, r); code != exitCancelled {
		t.Fatalf("exit = %d, want %d", code, exitCancelled)
	}
	calls, errs, exits, _ := r.snapshot()
	if n := count(calls, "cleanup"); n != 1 {
		t.Errorf("cleanup ran %d times", n)
	}
	if len(errs) != 1 || len(exits) != 1 {
		t.Errorf("errors = %q, exits = %v; want one of each", errs, exits)
	}
	if !strings.Contains(r.stderr.String(), "will not be interrupted") {
		t.Errorf("second signal not logged; stderr = %q", r.stderr.String())
	}
}

// A signal during fatal() does not start a second teardown or change the
// exit code.
func TestSignal_DuringFatalIsIgnored(t *testing.T) {
	tm, r := newRecorded(t)
	started := make(chan struct{})
	release := make(chan struct{})
	r.cleanupF = func() error { close(started); <-release; return nil }

	go tm.fail("bootc install: exit status 1")
	<-started
	tm.onSignal(syscall.SIGTERM)
	close(release)

	if code := waitExit(t, r); code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	calls, errs, _, _ := r.snapshot()
	if n := count(calls, "cleanup"); n != 1 {
		t.Errorf("cleanup ran %d times", n)
	}
	if len(errs) != 1 || errs[0] != "bootc install: exit status 1" {
		t.Errorf("error events = %q", errs)
	}
}

// fatal() reached because the cancel killed a child parks and leaves the
// exit to the cancel path, so the frontend sees "cancelled", not the child's
// "signal: terminated".
func TestFail_AfterCancelDefersToCancel(t *testing.T) {
	tm, r := newRecorded(t)
	started := make(chan struct{})
	release := make(chan struct{})
	r.cleanupF = func() error { close(started); <-release; return nil }

	go tm.onSignal(syscall.SIGTERM)
	<-started
	tm.fail("bootc install: signal: terminated")
	close(release)

	if code := waitExit(t, r); code != exitCancelled {
		t.Fatalf("exit = %d, want %d", code, exitCancelled)
	}
	_, errs, _, parks := r.snapshot()
	if parks != 1 {
		t.Errorf("fatal parked %d times, want 1", parks)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "cancelled") {
		t.Errorf("error events = %q", errs)
	}
}

// The child of a process-group SIGTERM can fail a moment before fisherman's
// handler runs. A fatal() inside the settle window still reports a cancel.
func TestFail_SignalWithinSettleWindowWins(t *testing.T) {
	tm, r := newRecorded(t)
	tm.settle = 2 * time.Second

	failed := make(chan struct{})
	go func() { tm.fail("mkfs.xfs: signal: terminated"); close(failed) }()
	time.Sleep(50 * time.Millisecond)
	go tm.onSignal(syscall.SIGTERM)

	if code := waitExit(t, r); code != exitCancelled {
		t.Fatalf("exit = %d, want %d", code, exitCancelled)
	}
	<-failed
	_, errs, exits, parks := r.snapshot()
	if len(errs) != 1 || len(exits) != 1 || parks != 1 {
		t.Errorf("errors = %q, exits = %v, parks = %d", errs, exits, parks)
	}
}

func TestFinish_CleanupFailureIsReported(t *testing.T) {
	tm, r := newRecorded(t)
	r.cleanupF = func() error { return errors.New("closing LUKS device fisherman-root: busy") }
	tm.onSignal(syscall.SIGTERM)

	_, errs, exits, _ := r.snapshot()
	if len(errs) != 1 ||
		!strings.HasPrefix(errs[0], "installation cancelled (SIGTERM)") ||
		!strings.Contains(errs[0], "cleanup failed") ||
		!strings.Contains(errs[0], "fisherman-root: busy") {
		t.Errorf("error events = %q", errs)
	}
	if len(exits) != 1 || exits[0] != exitCancelled {
		t.Errorf("exits = %v", exits)
	}
}

func TestFinish_CleanupTimeoutStillExits(t *testing.T) {
	tm, r := newRecorded(t)
	tm.cleanupTimeout = 50 * time.Millisecond
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r.cleanupF = func() error { <-block; return nil }
	tm.fail("formatting root filesystem: exit status 1")

	_, errs, exits, _ := r.snapshot()
	if len(errs) != 1 || !strings.Contains(errs[0], "did not finish within") {
		t.Errorf("error events = %q", errs)
	}
	if len(exits) != 1 || exits[0] != exitFailure {
		t.Errorf("exits = %v", exits)
	}
}

func TestClaimSuccess(t *testing.T) {
	t.Run("signal after success is ignored", func(t *testing.T) {
		tm, r := newRecorded(t)
		tm.claimSuccess()
		tm.onSignal(syscall.SIGTERM)
		calls, _, _, parks := r.snapshot()
		if len(calls) != 0 || parks != 0 {
			t.Errorf("calls = %v, parks = %d; want none", calls, parks)
		}
	})
	t.Run("success after cancel parks", func(t *testing.T) {
		tm, r := newRecorded(t)
		tm.onSignal(syscall.SIGTERM)
		tm.claimSuccess()
		_, _, _, parks := r.snapshot()
		if parks != 1 {
			t.Errorf("parks = %d, want 1", parks)
		}
	})
}

func count(xs []string, s string) int {
	n := 0
	for _, x := range xs {
		if x == s {
			n++
		}
	}
	return n
}

// ── Real-signal tests ──────────────────────────────────────────────────────
//
// These re-exec the test binary as a stand-in fisherman: it installs the real
// signal handler, the real child-process stopper and the real progress.Error
// and os.Exit, with only the disk teardown replaced by one that writes a
// marker file. The parent sends real signals to that process only (not its
// group), so the child must be stopped by fisherman itself.

const helperEnv = "FISHERMAN_TERMINATE_HELPER"

func TestTerminateHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		t.Skip("helper process for the real-signal tests")
	}
	marker := os.Getenv("FISHERMAN_TERMINATE_MARKER")
	cleanup.AddMount("/mnt/fisherman-target") // what the teardown would release
	term = newTerminator()
	term.cleanup = func() error {
		fmt.Fprintln(os.Stderr, "CLEANUP_START")
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		fmt.Fprintln(f, "cleanup")
		f.Close()
		time.Sleep(700 * time.Millisecond) // window for a second signal
		return nil
	}
	term.watchSignals()

	switch mode {
	case "cancel":
		// Mimic a pipeline step: run a child and call fatal() if it fails.
		child := exec.Command("sleep", "60")
		if err := child.Start(); err != nil {
			fatal("starting child: %v", err)
		}
		fmt.Fprintf(os.Stderr, "READY %d\n", child.Process.Pid)
		if err := child.Wait(); err != nil {
			fatal("bootc install: %v", err)
		}
		fatal("child exited cleanly; it should have been stopped")
	case "fatal":
		fmt.Fprintln(os.Stderr, "READY 0")
		fatal("partitioning disk: device busy")
	}
	parkForever()
}

type helperResult struct {
	exitCode int
	events   []map[string]any
	stderr   string
	cleanups int
}

// runHelper starts the helper, waits for READY, calls act with the helper's
// pid, the child's pid and a channel of stderr lines, then collects results.
func runHelper(t *testing.T, mode string, act func(pid, childPID int, lines <-chan string)) helperResult {
	t.Helper()
	if testing.Short() {
		t.Skip("re-exec signal test")
	}
	marker := filepath.Join(t.TempDir(), "cleanup.marker")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminateHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode, "FISHERMAN_TERMINATE_MARKER="+marker)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string, 64)
	var stderrAll strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(lines)
		sc := bufio.NewScanner(stderrPipe)
		for sc.Scan() {
			stderrAll.WriteString(sc.Text() + "\n")
			lines <- sc.Text()
		}
	}()

	childPID := -1
	deadline := time.After(10 * time.Second)
	for childPID < 0 {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("helper exited before READY")
			}
			if strings.HasPrefix(l, "READY ") {
				childPID, _ = strconv.Atoi(strings.TrimPrefix(l, "READY "))
			}
		case <-deadline:
			_ = cmd.Process.Kill()
			t.Fatal("helper never became ready")
		}
	}

	act(cmd.Process.Pid, childPID, lines)

	go func() {
		for range lines { // drain so the stderr reader never blocks
		}
	}()
	waitErr := cmd.Wait()
	wg.Wait()

	res := helperResult{stderr: stderrAll.String()}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		res.exitCode = 0
	case errors.As(waitErr, &exitErr):
		res.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("waiting for helper: %v", waitErr)
	}
	for _, l := range strings.Split(stdout.String(), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(l), &ev) == nil {
			res.events = append(res.events, ev)
		}
	}
	if data, err := os.ReadFile(marker); err == nil {
		res.cleanups = strings.Count(string(data), "cleanup")
	}

	if childPID > 0 {
		assertGone(t, childPID)
	}
	return res
}

func waitLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("helper exited before %q", want)
			}
			if strings.Contains(l, want) {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func assertGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return
		}
		if i := strings.LastIndexByte(string(data), ')'); i > 0 {
			if f := strings.Fields(string(data)[i+1:]); len(f) > 0 && f[0] == "Z" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("child %d outlived fisherman", pid)
}

func errorEvents(evs []map[string]any) []string {
	var out []string
	for _, ev := range evs {
		if ev["type"] == "error" {
			msg, _ := ev["message"].(string)
			out = append(out, msg)
		}
	}
	return out
}

func TestRealSignal_SIGTERMCancelsCleanly(t *testing.T) {
	res := runHelper(t, "cancel", func(pid, _ int, _ <-chan string) {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	})
	if res.exitCode != exitCancelled {
		t.Errorf("exit code = %d, want %d\nstderr:\n%s", res.exitCode, exitCancelled, res.stderr)
	}
	errs := errorEvents(res.events)
	if len(errs) != 1 || errs[0] != "installation cancelled (SIGTERM)" {
		t.Errorf("error events = %q, want exactly one cancel event", errs)
	}
	if res.cleanups != 1 {
		t.Errorf("cleanup ran %d times, want 1", res.cleanups)
	}
}

func TestRealSignal_SecondSignalDuringCleanup(t *testing.T) {
	res := runHelper(t, "cancel", func(pid, _ int, lines <-chan string) {
		_ = syscall.Kill(pid, syscall.SIGINT)
		waitLine(t, lines, "CLEANUP_START")
		_ = syscall.Kill(pid, syscall.SIGTERM)
		_ = syscall.Kill(pid, syscall.SIGHUP)
	})
	if res.exitCode != exitCancelled {
		t.Errorf("exit code = %d, want %d\nstderr:\n%s", res.exitCode, exitCancelled, res.stderr)
	}
	errs := errorEvents(res.events)
	if len(errs) != 1 || errs[0] != "installation cancelled (SIGINT)" {
		t.Errorf("error events = %q", errs)
	}
	if res.cleanups != 1 {
		t.Errorf("cleanup ran %d times, want 1", res.cleanups)
	}
	if !strings.Contains(res.stderr, "will not be interrupted") {
		t.Errorf("second signal was not reported on stderr:\n%s", res.stderr)
	}
}

func TestRealSignal_DuringFatal(t *testing.T) {
	res := runHelper(t, "fatal", func(pid, _ int, lines <-chan string) {
		waitLine(t, lines, "CLEANUP_START")
		_ = syscall.Kill(pid, syscall.SIGTERM)
	})
	if res.exitCode != exitFailure {
		t.Errorf("exit code = %d, want %d\nstderr:\n%s", res.exitCode, exitFailure, res.stderr)
	}
	errs := errorEvents(res.events)
	if len(errs) != 1 || errs[0] != "partitioning disk: device busy" {
		t.Errorf("error events = %q", errs)
	}
	if res.cleanups != 1 {
		t.Errorf("cleanup ran %d times, want 1", res.cleanups)
	}
}
