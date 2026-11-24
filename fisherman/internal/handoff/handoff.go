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

// Modes of io.tunaos.Fisherman.Handoff and Probe's handoff decision. The
// wire spelling keeps the Handoff enum's underscore (soft_reboot).
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

// Facts is what the takeover decision reads.
type Facts struct {
	// RootFSType is the installed root filesystem: btrfs, xfs, ext4.
	RootFSType string
	// EL is true on RHEL-derivative (EL10) bases.
	EL bool
}

// Reasons name why Decide picked the mode. Frontends match on the mode;
// the reason is for logs and debugging.
const (
	ReasonBtrfsSeed = "btrfs-seed"
	ReasonEL10      = "el10"
	ReasonDefault   = "default"
)

// Decide picks the takeover mode: btrfs roots migrate live, EL10 restarts
// softly into the copied system, everything else reboots.
func Decide(f Facts) (mode, reason string) {
	switch {
	case f.RootFSType == "btrfs":
		return ModeMigrate, ReasonBtrfsSeed
	case f.EL:
		return ModeSoftReboot, ReasonEL10
	default:
		return ModeReboot, ReasonDefault
	}
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
