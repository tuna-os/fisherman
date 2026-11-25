package varlinkapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Shutdown implements io.systemd.Shutdown with systemctl. PowerOff, Reboot
// and Halt are real. KExec and SoftReboot are refused: the installed system
// has not been prepared for a restart into it without firmware
// (tunaOS#4909 owns the soft-reboot mechanism, and kexec is blocked by
// policy), so asking for them fails with ErrDeferred rather than running a
// command that restarts into the wrong root.
//
// The inhibitor rules follow systemd's: without SkipInhibitors a blocking
// inhibitor stops the call (BlockedByInhibitor); with it, systemctl runs
// with --check-inhibitors=no. polkit has already approved the call before it
// gets here.
func (r Real) Shutdown(ctx context.Context, kind string, p ShutdownParams) error {
	var verb string
	switch kind {
	case "PowerOff":
		verb = "poweroff"
	case "Reboot":
		verb = "reboot"
	case "Halt":
		verb = "halt"
	case "KExec", "SoftReboot":
		return fmt.Errorf("%s: %w", kind, errShutdownDeferred)
	default:
		return fmt.Errorf("unknown shutdown kind %q", kind)
	}
	inhibit := "yes"
	if p.SkipInhibitors != nil && *p.SkipInhibitors {
		inhibit = "no"
	}
	args := []string{verb, "--check-inhibitors=" + inhibit, "--no-block"}
	if _, err := r.cmd().Run(ctx, "systemctl", args, CmdOpts{}); err != nil {
		var ce *CmdError
		if errors.As(err, &ce) {
			low := strings.ToLower(ce.Stderr)
			switch {
			case strings.Contains(low, "inhibit"):
				return &BlockedByInhibitor{Who: "another program", Why: ce.Stderr}
			case strings.Contains(low, "already in progress") || strings.Contains(low, "transaction is destructive"):
				return ErrAlreadyInProgress
			}
		}
		return err
	}
	return nil
}

// errShutdownDeferred marks kexec and soft-reboot, which wait on
// tunaOS#4909 and policy.
var errShutdownDeferred = errors.New("not supported yet (tunaOS#4909)")
