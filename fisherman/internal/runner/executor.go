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
	if err := checkHalted(name, args); err != nil {
		return &haltedCommand{err: err}
	}
	name, args = HostArgs(name, args)
	return &realCommand{exec.Command(name, args...)}
}

// DefaultExecutor is the standard implementation of Executor.
var DefaultExecutor Executor = defaultExecutor{}

// haltedCommand is returned by the default executor after runner.Halt: every
// attempt to run it fails with ErrHalted instead of starting a process.
type haltedCommand struct{ err error }

func (c *haltedCommand) Run() error              { return c.err }
func (c *haltedCommand) Output() ([]byte, error) { return nil, c.err }
func (c *haltedCommand) Start() error            { return c.err }
func (c *haltedCommand) Wait() error             { return c.err }
func (c *haltedCommand) SetStdin(io.Reader)      {}
func (c *haltedCommand) SetStdout(io.Writer)     {}
func (c *haltedCommand) SetStderr(io.Writer)     {}
