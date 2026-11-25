package varlinkapi

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/tuna-os/fisherman/internal/handoff"
	"github.com/tuna-os/fisherman/internal/runner"
)

// CmdOpts are the optional inputs of one command.
type CmdOpts struct {
	// Stdin feeds the command's standard input. Secrets go here, never in
	// the argument list.
	Stdin []byte
	// Quiet keeps the command's stderr out of the returned error. Set it on
	// every command that takes a secret.
	Quiet bool
}

// Commander is the one seam the real executor runs host programs through:
// context aware, with stdin, returning stdout. The system implementation
// applies the halt gate and the Flatpak wrapping of internal/runner. Unit
// tests substitute a fake that records the command lines, so the goldens pin
// exactly what an install runs.
//
// Implementations must not log arguments or stdin.
type Commander interface {
	Run(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error)
}

// CmdError is a failed command. It names the program and the exit status,
// plus the tail of stderr unless the command was Quiet. It never carries
// the arguments: they name devices, slots and paths, and the failed event
// shows the message to the user.
type CmdError struct {
	Name   string
	Err    error
	Stderr string
}

func (e *CmdError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %v: %s", e.Name, e.Err, e.Stderr)
	}
	return fmt.Sprintf("%s: %v", e.Name, e.Err)
}

func (e *CmdError) Unwrap() error { return e.Err }

// cancelGrace is how long a cancelled command gets to tear itself down after
// SIGTERM before it is killed: systemd-repart removes its own dm mappings
// when it can.
const cancelGrace = 30 * time.Second

type systemCommander struct{}

func (systemCommander) Run(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error) {
	// HostCommand applies the halt gate and the Flatpak wrapping. Its
	// *exec.Cmd has no context, so rebuild it with one: SIGTERM first, then
	// the kill after cancelGrace.
	hc := runner.HostCommand(name, args...)
	cmd := exec.CommandContext(ctx, hc.Args[0], hc.Args[1:]...)
	if hc.Err != nil {
		cmd.Err = hc.Err
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = cancelGrace
	if o.Stdin != nil {
		cmd.Stdin = bytes.NewReader(o.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		ce := &CmdError{Name: name, Err: err}
		if ctx.Err() != nil {
			ce.Err = ctx.Err()
		}
		if !o.Quiet {
			ce.Stderr = tail(stderr.String())
		}
		return nil, ce
	}
	return stdout.Bytes(), nil
}

// tail keeps the last line of s that says something, at most 300 bytes.
func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 300 {
		last = last[len(last)-300:]
	}
	return last
}

// SystemCommander runs the real programs.
func SystemCommander() Commander { return systemCommander{} }

// handoffRunner adapts a Commander to handoff.Runner.
func handoffRunner(c Commander) handoff.Runner {
	return handoff.RunnerFunc(func(ctx context.Context, name string, args ...string) error {
		_, err := c.Run(ctx, name, args, CmdOpts{})
		return err
	})
}

// fileExists reports whether path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
