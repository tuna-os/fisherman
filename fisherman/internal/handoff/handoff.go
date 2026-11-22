// Package handoff hands the installed system over: reboot into it, or stay
// in the live session while the installed system takes over.
//
// Only reboot shells out today (systemctl reboot). migrate and soft_reboot
// are declared but deferred: their mechanism (btrfs seed/migrate, EL10
// soft-reboot) belongs to tunaOS#4908 and tunaOS#4909, which own what the
// installed system must look like before anything restarts into it.
// Calling systemctl soft-reboot now could restart into the wrong root, so
// both fail with ErrDeferred until those issues land.
package handoff

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/tuna-os/fisherman/internal/runner"
)

// Modes of io.tunaos.Fisherman.Handoff.
const (
	ModeMigrate    = "migrate"
	ModeSoftReboot = "soft_reboot"
	ModeReboot     = "reboot"
)

// ErrDeferred marks migrate and soft_reboot: the interface is frozen, the
// mechanism lives in tunaOS#4908/#4909.
var ErrDeferred = errors.New("handoff: migrate and soft_reboot need the btrfs/soft-reboot mechanism (tunaOS#4908, tunaOS#4909)")

// Runner executes a helper. It must not log its arguments.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// RunnerFunc adapts a function to a Runner.
type RunnerFunc func(ctx context.Context, name string, args ...string) error

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, name string, args ...string) error {
	return f(ctx, name, args...)
}

// Handoff runs mode: reboot restarts into the installed system (the live
// session ends, so it reports false), migrate keeps the live session while
// the installed system takes over (true). Unknown modes fail; deferred
// modes fail with ErrDeferred.
func Handoff(ctx context.Context, run Runner, mode string) (sessionKeepsRunning bool, err error) {
	switch mode {
	case ModeReboot:
		return false, run.Run(ctx, "systemctl", "reboot")
	case ModeMigrate:
		return true, ErrDeferred
	case ModeSoftReboot:
		return false, ErrDeferred
	default:
		return false, fmt.Errorf("handoff: unknown mode %q", mode)
	}
}

// SystemRunner shells out to the helper, applying the Flatpak wrapping so
// host subprocess calls work from the Flatpak frontends too. Helper output
// is discarded: chatter must not reach the caller, and it may sit next to
// a secret on a hostile volume.
func SystemRunner(ctx context.Context, name string, args ...string) error {
	name, args = runner.HostArgs(name, args)
	cmd := exec.CommandContext(ctx, name, args...)
	if _, err := cmd.Output(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
