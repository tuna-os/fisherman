package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Run executes a command, streaming its stdout and stderr to os.Stdout in real time.
func Run(name string, args ...string) error {
	return RunWithStdin(nil, name, args...)
}

// RunWithStdin executes a command with the given stdin reader, streaming output in real time.
func RunWithStdin(stdin io.Reader, name string, args ...string) error {
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
