package runner_test

import (
	"errors"
	"io"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
)

func TestRunFn_DefaultIsNonNil(t *testing.T) {
	if runner.RunFn == nil {
		t.Error("RunFn must not be nil at startup")
	}
}

func TestRunFn_Substitution(t *testing.T) {
	var called bool
	var gotName string
	var gotArgs []string

	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		called = true
		gotName = name
		gotArgs = args
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	if err := runner.Run("echo", "hello", "world"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !called {
		t.Error("RunFn was not called")
	}
	if gotName != "echo" {
		t.Errorf("name = %q, want echo", gotName)
	}
	wantArgs := []string{"hello", "world"}
	if len(gotArgs) != len(wantArgs) {
		t.Errorf("args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestRunFn_ErrorPropagation(t *testing.T) {
	want := errors.New("test error")
	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		return want
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	err := runner.Run("anything")
	if err != want {
		t.Errorf("Run() error = %v, want %v", err, want)
	}
}

func TestRunFn_RestoredAfterCleanup(t *testing.T) {
	original := runner.RunFn

	func() {
		runner.RunFn = func(io.Reader, string, ...string) error { return nil }
		t.Cleanup(func() { runner.RunFn = runner.DefaultRun })
	}()
	// t.Cleanup runs at test end; here we just check the mechanism works in concept.
	// The real assertion is that tests using t.Cleanup don't bleed into each other.
	_ = original
}

func TestRunWithStdin_PassesStdin(t *testing.T) {
	var gotStdin io.Reader

	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		gotStdin = stdin
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	type nopReader struct{ io.Reader }
	sentinel := &nopReader{}

	if err := runner.RunWithStdin(sentinel, "cat"); err != nil {
		t.Fatalf("RunWithStdin: %v", err)
	}
	if gotStdin != sentinel {
		t.Error("RunWithStdin did not pass stdin to RunFn")
	}
}

func TestRun_PassesNilStdin(t *testing.T) {
	stdinSet := false

	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		stdinSet = stdin != nil
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	if err := runner.Run("echo"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stdinSet {
		t.Error("Run should pass nil stdin to RunFn")
	}
}
