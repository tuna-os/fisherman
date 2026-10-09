package runner

import (
	"os/exec"
)

// HostCommand builds the *exec.Cmd for a host subprocess: it applies the
// Flatpak wrapping HostArgs decides on, and it refuses to start the process
// at all once Halt has been called.
//
// It exists because a caller that needs exec.Cmd itself — raw Output or
// CombinedOutput, a detached Start, a custom cmd.Env — cannot go through Run
// or Output, and every such caller previously built its own exec.Command.
// That hand-built command answered to neither of this package's two
// invariants: it skipped the halt gate outright, and it was one forgotten
// HostArgs call away from running inside the sandbox instead of on the host.
//
// Pass the ORIGINAL program name, not one HostArgs has already rewritten:
// the halt gate's teardown allowlist is keyed on the real program, and in a
// sandbox the program being executed is "flatpak-spawn". Use WrappedCommand
// for an argv that is already wrapped.
//
// On halt the returned command is inert: cmd.Err is set, so Run, Start,
// Output and CombinedOutput all fail with ErrHalted without forking. The
// error is reported on the command rather than returned here so that callers
// keep their existing single error path.
func HostCommand(name string, args ...string) *exec.Cmd {
	haltErr := checkHalted(name, args)
	hostName, hostArgs := HostArgs(name, args)
	cmd := exec.Command(hostName, hostArgs...)
	if haltErr != nil {
		cmd.Err = haltErr
	}
	return cmd
}

// WrappedCommand is HostCommand for an argv that HostArgs or HostArgsWithEnv
// has already rewritten — the shape used by callers that need the effective
// argv first, to log it or to derive cmd.Env from it.
//
// It applies the halt gate only; the wrapping has already happened. The
// original program name is recovered from the flatpak-spawn argv so the gate
// sees the same name it would have seen before wrapping, and teardown
// commands stay permitted inside a sandbox.
func WrappedCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	if err := checkHalted(unwrapHostArgs(name, args)); err != nil {
		cmd.Err = err
	}
	return cmd
}
