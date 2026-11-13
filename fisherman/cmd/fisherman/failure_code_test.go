package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The error event names the failed operation with a stable code, the step
// that was running, whether a disk step had started, and the teardown
// outcome on its own (#369). Before, all of it was prose in the message.

func lastFailure(t *testing.T, r *recorder) progress.Failure {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.failures) != 1 {
		t.Fatalf("error events = %d, want exactly 1", len(r.failures))
	}
	return r.failures[0]
}

func TestFailure_RuntimeCodeStepAndCleanup(t *testing.T) {
	tm, r := newRecorded(t)
	tm.stepState = func() (string, bool) { return "partition", true }
	tm.failCode("partitioning disk: exit status 1", progress.FailurePartitionFailed)

	f := lastFailure(t, r)
	if f.Code != progress.FailurePartitionFailed || f.StepID != "partition" || !f.DiskChanged {
		t.Errorf("failure = %+v", f)
	}
	if f.Cleanup == nil || f.Cleanup.Status != progress.CleanupOK || f.Cleanup.Message != "" {
		t.Errorf("cleanup = %+v, want ok", f.Cleanup)
	}
	if f.Message != "partitioning disk: exit status 1" {
		t.Errorf("message = %q", f.Message)
	}
}

func TestFailure_BeforeAnyStepNoDiskChanged(t *testing.T) {
	tm, r := newRecorded(t)
	tm.stepState = func() (string, bool) { return "", false }
	tm.failProblem("invalid recipe: disk is too small", recipe.Problem{Code: recipe.CodeDiskTooSmall, Field: "disk"})

	f := lastFailure(t, r)
	if f.Code != recipe.CodeDiskTooSmall || f.Field != "disk" || f.DiskChanged || f.StepID != "" {
		t.Errorf("failure = %+v", f)
	}
}

// A cleanup that fails too does not replace the primary cause.
func TestFailure_CleanupFailureKeepsPrimaryCode(t *testing.T) {
	tm, r := newRecorded(t)
	tm.stepState = func() (string, bool) { return "install_os", true }
	r.cleanupF = func() error { return errors.New("closing LUKS device fisherman-root: busy") }
	tm.failCode("bootc install: exit status 1", progress.FailureBootcInstallFailed)

	f := lastFailure(t, r)
	if f.Code != progress.FailureBootcInstallFailed {
		t.Errorf("code = %q, want the primary failure's", f.Code)
	}
	if f.Cleanup == nil || f.Cleanup.Status != progress.CleanupFailed || !strings.Contains(f.Cleanup.Message, "fisherman-root: busy") {
		t.Errorf("cleanup = %+v", f.Cleanup)
	}
	if !strings.Contains(f.Message, "; cleanup failed, the target may still be mounted or unlocked: closing LUKS") {
		t.Errorf("message %q lost the cleanup failure old consumers read", f.Message)
	}
}

func TestFailure_CleanupTimeout(t *testing.T) {
	tm, r := newRecorded(t)
	tm.cleanupTimeout = 50 * time.Millisecond
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r.cleanupF = func() error { <-block; return nil }
	tm.failCode("formatting root filesystem: exit status 1", progress.FailureFormatFailed)

	f := lastFailure(t, r)
	if f.Code != progress.FailureFormatFailed || f.Cleanup == nil || f.Cleanup.Status != progress.CleanupTimedOut {
		t.Errorf("failure = %+v cleanup = %+v", f, f.Cleanup)
	}
}

func TestFailure_UnknownFallback(t *testing.T) {
	tm, r := newRecorded(t)
	tm.fail("something without a code")
	if f := lastFailure(t, r); f.Code != progress.FailureUnknown {
		t.Errorf("code = %q, want %q", f.Code, progress.FailureUnknown)
	}
}

// A failure that loses the race to a cancel is reported as the cancel, with
// its code: the exit is 130, and the cause the frontend shows must agree.
func TestFailure_CancelRaceReportsCancelled(t *testing.T) {
	tm, r := newRecorded(t)
	tm.settle = 2 * time.Second
	failed := make(chan struct{})
	go func() {
		tm.failCode("mkfs.xfs: signal: terminated", progress.FailureFormatFailed)
		close(failed)
	}()
	time.Sleep(50 * time.Millisecond)
	go tm.onSignal(syscall.SIGTERM)
	if code := waitExit(t, r); code != exitCancelled {
		t.Fatalf("exit = %d", code)
	}
	<-failed
	if f := lastFailure(t, r); f.Code != progress.FailureCancelled {
		t.Errorf("code = %q, want %q", f.Code, progress.FailureCancelled)
	}
}

func TestFailure_StderrLineIsSanitized(t *testing.T) {
	const secret = "stderr-sentinel-passphrase"
	progress.RegisterSecret(secret)
	tm, r := newRecorded(t)
	tm.failCode("LUKS format: "+secret, progress.FailureLUKSFailed)
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(r.stderr.String(), secret) || !strings.Contains(r.stderr.String(), "[redacted]") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
}

// A pull failure stays a typed error up to main, through the wrapping of
// bootc's container path, so main tells it from any other bootc failure.
func TestBootcFailureCode(t *testing.T) {
	pull := error(&install.PullError{Image: "ghcr.io/example/os:stable", Err: errors.New("podman pull: exit status 125")})
	for _, tt := range []struct {
		err  error
		want string
	}{
		{pull, progress.FailurePullFailed},
		{fmt.Errorf("pulling image: %w", pull), progress.FailurePullFailed},
		{fmt.Errorf("bootc: %w", fmt.Errorf("pulling image: %w", pull)), progress.FailurePullFailed},
		{errors.New("bootc install to-filesystem (via container): exit status 1"), progress.FailureBootcInstallFailed},
		{errors.New("pulling image: timeout"), progress.FailureBootcInstallFailed},
	} {
		if got := bootcFailureCode(tt.err); got != tt.want {
			t.Errorf("bootcFailureCode(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
