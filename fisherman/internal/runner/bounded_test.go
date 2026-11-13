package runner

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBoundedOutputReturnsStdout(t *testing.T) {
	out, err := BoundedOutput(5*time.Second, 64, "sh", "-c", "printf hello; echo noise >&2")
	if err != nil || string(out) != "hello" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestBoundedOutputCapsOutput(t *testing.T) {
	out, err := BoundedOutput(5*time.Second, 1000, "sh", "-c", "head -c 100000 /dev/zero")
	if !errors.Is(err, ErrOutputTooLarge) || out != nil {
		t.Fatalf("got %d bytes, %v; want ErrOutputTooLarge", len(out), err)
	}
}

func TestBoundedOutputTimesOut(t *testing.T) {
	start := time.Now()
	_, err := BoundedOutput(100*time.Millisecond, 64, "sleep", "30")
	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("err = %v, want ErrTimedOut", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("timeout took %v", time.Since(start))
	}
}

// The exit status survives; what the command printed does not reach the
// error text.
func TestBoundedOutputKeepsExitStatusDropsText(t *testing.T) {
	_, err := BoundedOutput(5*time.Second, 64, "sh", "-c", "echo SECRET-SENTINEL; echo SECRET-SENTINEL >&2; exit 2")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("err = %v, want exit status 2", err)
	}
	if strings.Contains(err.Error(), "SECRET-SENTINEL") || len(exit.Stderr) != 0 {
		t.Errorf("command output leaked into the error: %v", err)
	}
}

func TestBoundedOutputHonoursHalt(t *testing.T) {
	Halt()
	t.Cleanup(resetHaltForTest)
	if _, err := BoundedOutput(time.Second, 64, "sh", "-c", "exit 0"); err == nil {
		t.Error("a command started after Halt")
	}
}
