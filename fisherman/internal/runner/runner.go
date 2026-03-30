package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// inFlatpak returns true when the process is running inside a Flatpak sandbox.
// The presence of /.flatpak-info is the canonical indicator.
var inFlatpak = sync.OnceValue(func() bool {
	_, err := os.Stat("/.flatpak-info")
	return err == nil
})

// HostArgs prepends "flatpak-spawn --host" when running inside a Flatpak so
// that privileged host tools (sfdisk, cryptsetup, podman, …) execute in the
// host mount namespace rather than the sandbox.
func HostArgs(name string, args []string) (string, []string) {
	if inFlatpak() {
		return "flatpak-spawn", append([]string{"--host", name}, args...)
	}
	return name, args
}

// DefaultRun is the real subprocess implementation. It applies flatpak-spawn
// wrapping when running inside a Flatpak sandbox, then streams the command's
// stdout and stderr to os.Stdout in real time.
//
// Replace RunFn in tests to intercept subprocess calls; restore it afterwards
// with runner.RunFn = runner.DefaultRun.
func DefaultRun(stdin io.Reader, name string, args ...string) error {
	name, args = HostArgs(name, args)
	cmd := exec.Command(name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	fmt.Fprintf(os.Stdout, "+ %s %s\n", name, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// RunFn is the function invoked by Run and RunWithStdin. Tests replace it with
// a recording function to intercept subprocess calls without executing them.
var RunFn = DefaultRun

// Run executes a command, streaming its stdout and stderr to os.Stdout in real time.
func Run(name string, args ...string) error {
	return RunFn(nil, name, args...)
}

// RunWithStdin executes a command with the given stdin reader, streaming output in real time.
func RunWithStdin(stdin io.Reader, name string, args ...string) error {
	return RunFn(stdin, name, args...)
}
