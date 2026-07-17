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

// HostArgsWithEnv is like HostArgs but also forwards the provided env vars
// to the host process via --env=KEY=VALUE when running inside a Flatpak.
//
// Background: flatpak-spawn --host spawns the command in the host mount
// namespace but does NOT automatically forward the Flatpak sandbox's
// environment variables to the spawned process. Any env vars that the host
// command needs (e.g. TMPDIR, CONTAINERS_STORAGE_CONF) must be passed
// explicitly with --env=KEY=VALUE flags.
//
// For non-Flatpak invocations the result is identical to HostArgs; callers
// are expected to set cmd.Env on the returned command to propagate the vars.
func HostArgsWithEnv(name string, args []string, envVars []string) (string, []string) {
	if inFlatpak() {
		fpArgs := make([]string, 0, 1+len(envVars)+1+len(args))
		fpArgs = append(fpArgs, "--host")
		for _, e := range envVars {
			fpArgs = append(fpArgs, "--env="+e)
		}
		fpArgs = append(fpArgs, name)
		fpArgs = append(fpArgs, args...)
		return "flatpak-spawn", fpArgs
	}
	return name, args
}

// InFlatpak reports whether the current process is running inside a Flatpak
// sandbox. Exported for use in conditional code paths outside this package.
func InFlatpak() bool { return inFlatpak() }

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

// DefaultOutput is the real implementation of Output. Tests can replace
// OutputFn to intercept calls without executing them.
func DefaultOutput(name string, args ...string) ([]byte, error) {
	name, args = HostArgs(name, args)
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// OutputFn is the function invoked by Output. Tests replace it with a
// recording function to intercept calls without executing them.
var OutputFn = DefaultOutput

// Output runs a command and returns its combined stdout output as bytes.
// Unlike Run, this does not stream output to os.Stdout. Flatpak-spawn wrapping
// is applied when running inside a sandbox.
func Output(name string, args ...string) ([]byte, error) {
	return OutputFn(name, args...)
}
