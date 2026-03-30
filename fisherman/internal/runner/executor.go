package runner

import (
	"io"
	"os/exec"
)

// Command represents an external command to be executed.
type Command interface {
	Run() error
	Output() ([]byte, error)
	Start() error
	Wait() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
}

// realCommand wraps exec.Cmd to implement the Command interface.
type realCommand struct {
	*exec.Cmd
}

func (c *realCommand) SetStdin(r io.Reader)  { c.Stdin = r }
func (c *realCommand) SetStdout(w io.Writer) { c.Stdout = w }
func (c *realCommand) SetStderr(w io.Writer) { c.Stderr = w }

// Executor creates Command instances.
type Executor interface {
	Command(name string, args ...string) Command
}

// defaultExecutor implements Executor by calling exec.Command.
type defaultExecutor struct{}

func (e defaultExecutor) Command(name string, args ...string) Command {
	name, args = HostArgs(name, args)
	return &realCommand{exec.Command(name, args...)}
}

// DefaultExecutor is the standard implementation of Executor.
var DefaultExecutor Executor = defaultExecutor{}
