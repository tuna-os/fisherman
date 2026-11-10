package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Exit codes. Frontends read these together with the final JSON event.
const (
	// exitFailure: the install failed. The last event is {"type":"error"}.
	exitFailure = 1
	// exitUsage: the command line named no recipe file and no subcommand.
	exitUsage = 2
	// exitCancelled: fisherman received SIGTERM, SIGINT or SIGHUP, or the
	// reader of its stdout went away, stopped its child processes, ran the
	// same teardown as a failure, and emitted one {"type":"error"} event
	// saying the installation was cancelled. 130 is the shell's code for a
	// process ended by Ctrl-C (128 + SIGINT); it is used for every cancel so
	// a frontend needs to check one value.
	exitCancelled = 130
)

const (
	// childStopGrace is how long children get to exit after SIGTERM before
	// they are sent SIGKILL. bootc and podman unwind their own mounts on
	// SIGTERM, so they are given a few seconds rather than killed outright.
	childStopGrace = 10 * time.Second
	// cleanupTimeout bounds the teardown. Unmounts are lazy and normally
	// finish in well under a second, but `udevadm settle` or a wedged
	// cryptsetup can block; after this fisherman reports what it could not
	// confirm and exits rather than leaving the frontend waiting forever.
	cleanupTimeout = 3 * time.Minute
	// cancelSettle is how long a failure waits for a cancel signal before
	// committing to exit code 1. A frontend signals the whole process group,
	// so the child (bootc, mkfs, …) can die and surface as a pipeline error
	// a moment before fisherman's own handler sees the same SIGTERM. Without
	// this window that cancel would be reported as an ordinary failure.
	cancelSettle = 300 * time.Millisecond
)

// terminator owns the one way fisherman leaves an install that did not
// succeed: stop child processes, tear down mounts and LUKS, emit exactly one
// error event, exit with a documented code.
//
// Exactly one caller — fatal(), the first cancel signal, or the success path —
// claims it. Everyone else defers to that caller:
//
//   - fatal() after a cancel signal parks; the cancel path exits the process.
//   - A signal during fatal() is logged and ignored; fatal exits with 1.
//   - A second signal during a cancel is logged and ignored. Teardown is not
//     interrupted: an interrupted teardown leaves the disk mounted or the
//     LUKS mapping open, which is exactly what the cancel exists to prevent.
//     Teardown is bounded by cleanupTimeout instead. SIGKILL cannot be
//     handled and still ends the process immediately.
//   - A signal after the success path has claimed (final teardown and the
//     "complete" event) is ignored; the install has already finished.
//
// The function fields are seams for tests.
type terminator struct {
	mu      sync.Mutex
	claimed bool
	// problem is the coded problem behind a failure (failProblem), so the
	// error event can carry its code and field.
	problem recipe.Problem

	cancelOnce sync.Once
	cancelled  chan struct{}

	halt           func()
	stopChildren   func() int
	cleanup        func() error
	emitError      func(string)
	stderr         io.Writer
	exit           func(int)
	silenceStdout  func()
	park           func()
	settle         time.Duration
	cleanupTimeout time.Duration
}

func newTerminator() *terminator {
	t := &terminator{
		cancelled:      make(chan struct{}),
		halt:           runner.Halt,
		stopChildren:   func() int { return runner.StopDescendants(childStopGrace) },
		cleanup:        teardown,
		stderr:         os.Stderr,
		exit:           os.Exit,
		silenceStdout:  stdoutToDevNull,
		park:           parkForever,
		settle:         cancelSettle,
		cleanupTimeout: cleanupTimeout,
	}
	t.emitError = func(msg string) {
		p := t.failedProblem()
		progress.ErrorWithCode(msg, p.Code, p.Field)
	}
	return t
}

// failedProblem is the coded problem a failProblem call recorded, or the
// zero Problem.
func (t *terminator) failedProblem() recipe.Problem {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.problem
}

// failProblem is fail for a failure with a stable code: the error event
// carries p's code and field next to the message. A cancel that wins the
// exit reports itself, without the code.
func (t *terminator) failProblem(msg string, p recipe.Problem) {
	t.mu.Lock()
	if !t.claimed {
		t.problem = p
	}
	t.mu.Unlock()
	t.fail(msg)
}

// term is the process-wide terminator used by fatal() and main().
var term = newTerminator()

// parkForever blocks the calling goroutine until another goroutine calls
// os.Exit. A sleep loop rather than select{} so the runtime never reports a
// deadlock while the exiting goroutine is blocked in a syscall.
func parkForever() {
	for {
		time.Sleep(time.Hour)
	}
}

// claim makes the caller the single path that ends the process. It returns
// false if another path got there first.
func (t *terminator) claim() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.claimed {
		return false
	}
	t.claimed = true
	return true
}

// watchSignals routes SIGTERM, SIGINT and SIGHUP to onSignal for the rest of
// the process's life. Every signal is handled — the first starts the cancel,
// later ones are logged — so none of them falls back to Go's default of
// killing the process mid-teardown.
//
// It also takes SIGPIPE away from Go's default, which ends the process on a
// broken-pipe write to stdout or stderr: no error event, no teardown, the
// target left mounted and the LUKS mapping open. That happens when the
// reader goes away while fisherman's parent lives on (the frontend crashes
// behind its wrapper shell, or `sudo fisherman r.json | tee log` loses its
// tee), so the parent-death signal does not fire either. With SIGPIPE
// handled, the write fails with EPIPE instead. SIGPIPE itself is not the
// cancel: the kernel also sends it for a write to any child's closed stdin.
// The cancel is an event that cannot be written (progress.OnBrokenPipe).
//
// The signal is handled, not ignored: an ignored signal stays ignored in
// every child, and bootc or podman writing into a closed pipe must still
// stop.
func (t *terminator) watchSignals() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for sig := range ch {
			go t.onSignal(sig)
		}
	}()

	pipe := make(chan os.Signal, 1)
	signal.Notify(pipe, syscall.SIGPIPE)
	go func() {
		for range pipe {
		}
	}()
	progress.OnBrokenPipe(t.onOutputClosed)
}

// onOutputClosed cancels the install because the reader of stdout is gone:
// nobody can see its progress or its result any more. It is the same cancel
// as a signal, with one step first: stdout is pointed at /dev/null, so the
// teardown commands (which inherit it) and the final event do not write
// into the closed pipe again.
func (t *terminator) onOutputClosed() {
	t.cancel("the reader of stdout went away (broken pipe)", "stdout closed", t.silenceStdout)
}

// stdoutToDevNull replaces file descriptor 1 with /dev/null.
func stdoutToDevNull() {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer null.Close()
	_ = syscall.Dup3(int(null.Fd()), int(os.Stdout.Fd()), 0)
}

// onSignal handles one cancel signal.
func (t *terminator) onSignal(sig os.Signal) {
	t.cancel("received "+signalName(sig), signalName(sig), nil)
}

// cancel runs the cancel path. what says what happened, for stderr
// ("received SIGTERM"); cause goes into the error event ("SIGTERM", "stdout
// closed"). before, if set, runs once the cancel has claimed the exit and
// before the teardown.
func (t *terminator) cancel(what, cause string, before func()) {
	first := false
	t.cancelOnce.Do(func() {
		first = true
		close(t.cancelled)
	})
	if !first {
		fmt.Fprintf(t.stderr, "fisherman: %s again; teardown is already running and will not be interrupted\n", what)
		return
	}
	if !t.claim() {
		fmt.Fprintf(t.stderr, "fisherman: %s while already exiting; letting that exit finish\n", what)
		return
	}
	fmt.Fprintf(t.stderr, "fisherman: %s, cancelling installation\n", what)
	if before != nil {
		before()
	}
	t.finish(fmt.Sprintf("installation cancelled (%s)", cause), exitCancelled, "cancelled")
}

// fail ends the install with exitFailure, unless a cancel signal arrives
// within the settle window, in which case the cancel path reports it.
func (t *terminator) fail(msg string) {
	select {
	case <-t.cancelled:
		t.park()
		return
	case <-time.After(t.settle):
	}
	if !t.claim() {
		t.park()
		return
	}
	t.finish(msg, exitFailure, "fatal")
}

// claimSuccess is called by the success path before its final teardown. If a
// cancel or failure has already claimed the exit, it parks until that path
// exits the process.
func (t *terminator) claimSuccess() {
	if !t.claim() {
		t.park()
	}
}

// finish is the single teardown path: refuse new subprocesses, stop running
// ones, tear down mounts and LUKS, then emit the error event and exit.
// Teardown runs before the event so the message can say whether it worked.
func (t *terminator) finish(msg string, code int, label string) {
	t.halt()
	if n := t.stopChildren(); n > 0 {
		fmt.Fprintf(t.stderr, "fisherman: %d child process(es) ignored SIGTERM and were killed\n", n)
	}
	if err := t.runCleanup(); err != nil {
		msg += "; cleanup failed, the target may still be mounted or unlocked: " + err.Error()
	}
	t.emitError(msg)
	fmt.Fprintf(t.stderr, "fisherman: %s: %s\n", label, msg)
	t.exit(code)
}

// runCleanup runs the registered teardown, bounded by cleanupTimeout.
func (t *terminator) runCleanup() error {
	done := make(chan error, 1)
	go func() { done <- t.cleanup() }()
	select {
	case err := <-done:
		return err
	case <-time.After(t.cleanupTimeout):
		return fmt.Errorf("teardown did not finish within %s", t.cleanupTimeout)
	}
}

func signalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return sig.String()
}
