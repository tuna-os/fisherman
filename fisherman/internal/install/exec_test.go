package install

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
)

// installMockCommand implements runner.Command, returning a canned
// error/output and (when stdout is set via SetStdout) writing that output to
// it on Run — mirroring exec.Cmd.CombinedOutput for callers that read
// combined output after a failed Run instead of via Output().
type installMockCommand struct {
	err    error
	output []byte
	stdout io.Writer
}

func (c *installMockCommand) Run() error {
	if c.output != nil && c.stdout != nil {
		_, _ = c.stdout.Write(c.output)
	}
	return c.err
}
func (c *installMockCommand) Start() error { return c.err }
func (c *installMockCommand) Wait() error  { return c.err }
func (c *installMockCommand) Output() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.output, nil
}
func (c *installMockCommand) SetStdin(io.Reader)    {}
func (c *installMockCommand) SetStdout(w io.Writer) { c.stdout = w }
func (c *installMockCommand) SetStderr(io.Writer)   {}

// installExecCall records one Exec.Command invocation for assertions.
type installExecCall struct {
	name string
	args []string
}

// installMockExecutor records every command invoked and returns a
// per-command canned error/output (success with no output by default).
type installMockExecutor struct {
	calls  []installExecCall
	errFor map[string]error
	outFor map[string][]byte
}

func (e *installMockExecutor) Command(name string, args ...string) runner.Command {
	e.calls = append(e.calls, installExecCall{name: name, args: args})
	return &installMockCommand{err: e.errFor[name], output: e.outFor[name]}
}

func (e *installMockExecutor) lastCall() installExecCall {
	return e.calls[len(e.calls)-1]
}

func setupInstallMockExec(t *testing.T) *installMockExecutor {
	t.Helper()
	mock := &installMockExecutor{errFor: map[string]error{}, outFor: map[string][]byte{}}
	old := Exec
	Exec = mock
	t.Cleanup(func() { Exec = old })
	return mock
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// TestBuildSelinuxBypassShim_InvokesExpectedCCArgs verifies the exact cc
// invocation (flags documented in the function's doc comment) and that the
// generated C source is always cleaned up. The mocked cc never actually
// writes the .so file, so chmod fails afterward — a real, if synthetic,
// error path (cc reporting success without producing its declared output).
func TestBuildSelinuxBypassShim_InvokesExpectedCCArgs(t *testing.T) {
	_ = os.Remove("/tmp/fisherman-selinux-bypass.so")
	mock := setupInstallMockExec(t)

	_, err := BuildSelinuxBypassShim()
	if err == nil || !strings.Contains(err.Error(), "chmod shim") {
		t.Fatalf("BuildSelinuxBypassShim() error = %v, want a chmod error (mock never creates the .so)", err)
	}

	if len(mock.calls) != 1 || mock.calls[0].name != "cc" {
		t.Fatalf("expected exactly one cc invocation, got calls=%v", mock.calls)
	}
	assertArgs(t, mock.calls[0].args, []string{
		"-shared", "-fPIC", "-O2", "-nostartfiles", "-ldl",
		"-o", "/tmp/fisherman-selinux-bypass.so", "/tmp/fisherman-selinux-bypass.c",
	})

	if _, statErr := os.Stat("/tmp/fisherman-selinux-bypass.c"); !os.IsNotExist(statErr) {
		t.Error("shim source file should be removed after BuildSelinuxBypassShim returns")
	}
}

// TestBuildSelinuxBypassShim_CCFailureIncludesCompilerOutput verifies that a
// cc failure surfaces the compiler's diagnostic output in the returned error,
// and that the source file is still cleaned up.
func TestBuildSelinuxBypassShim_CCFailureIncludesCompilerOutput(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.errFor["cc"] = fmt.Errorf("exit status 1")
	mock.outFor["cc"] = []byte("fisherman-selinux-bypass.c:5:1: error: unknown type name 'foo'\n")

	_, err := BuildSelinuxBypassShim()
	if err == nil {
		t.Fatal("expected error when cc fails")
	}
	if !strings.Contains(err.Error(), "unknown type name") {
		t.Errorf("expected compiler diagnostic in error, got: %v", err)
	}
	if _, statErr := os.Stat("/tmp/fisherman-selinux-bypass.c"); !os.IsNotExist(statErr) {
		t.Error("shim source file should be removed even when cc fails")
	}
}

func TestLoopBackingFile(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.outFor["losetup"] = []byte("/var/tmp/disk.img\n")

	got, err := loopBackingFile("/dev/loop3")
	if err != nil {
		t.Fatalf("loopBackingFile() error = %v", err)
	}
	if got != "/var/tmp/disk.img" {
		t.Errorf("loopBackingFile() = %q, want trimmed %q", got, "/var/tmp/disk.img")
	}
	assertArgs(t, mock.lastCall().args, []string{"--noheadings", "-O", "BACK-FILE", "/dev/loop3"})
}

func TestLoopBackingFile_Error(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.errFor["losetup"] = fmt.Errorf("no such device")
	if _, err := loopBackingFile("/dev/loop9"); err == nil {
		t.Error("expected error to propagate")
	}
}

func TestLoopDetach(t *testing.T) {
	mock := setupInstallMockExec(t)
	if err := loopDetach("/dev/loop3"); err != nil {
		t.Fatalf("loopDetach() error = %v", err)
	}
	assertArgs(t, mock.lastCall().args, []string{"-d", "/dev/loop3"})
}

func TestLoopDetach_Error(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.errFor["losetup"] = fmt.Errorf("device busy")
	if err := loopDetach("/dev/loop3"); err == nil {
		t.Error("expected error to propagate")
	}
}

func TestLoopReattach(t *testing.T) {
	mock := setupInstallMockExec(t)
	if err := loopReattach("/dev/loop3", "/var/tmp/disk.img"); err != nil {
		t.Fatalf("loopReattach() error = %v", err)
	}
	assertArgs(t, mock.lastCall().args, []string{"-P", "/dev/loop3", "/var/tmp/disk.img"})
}

func TestLoopReattach_Error(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.errFor["losetup"] = fmt.Errorf("no such device")
	if err := loopReattach("/dev/loop3", "/var/tmp/disk.img"); err == nil {
		t.Error("expected error to propagate")
	}
}

func TestLoopAttachFile(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.outFor["losetup"] = []byte("/dev/loop5\n")

	got, err := loopAttachFile("/var/tmp/disk.img")
	if err != nil {
		t.Fatalf("loopAttachFile() error = %v", err)
	}
	if got != "/dev/loop5" {
		t.Errorf("loopAttachFile() = %q, want %q", got, "/dev/loop5")
	}
	assertArgs(t, mock.lastCall().args, []string{"--find", "--partscan", "--show", "/var/tmp/disk.img"})
}

func TestLoopAttachFile_Error(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.errFor["losetup"] = fmt.Errorf("no free loop devices")
	if _, err := loopAttachFile("/var/tmp/disk.img"); err == nil {
		t.Error("expected error to propagate")
	}
}

func TestDefaultSkopeoInspect(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.outFor["skopeo"] = []byte(`{"Digest":"sha256:aaaa"}`)

	out, err := DefaultSkopeoInspect("docker://quay.io/tuna-os/yellowfin:gnome")
	if err != nil {
		t.Fatalf("DefaultSkopeoInspect() error = %v", err)
	}
	if string(out) != `{"Digest":"sha256:aaaa"}` {
		t.Errorf("DefaultSkopeoInspect() = %s, want digest json", out)
	}
	assertArgs(t, mock.lastCall().args, []string{"inspect", "docker://quay.io/tuna-os/yellowfin:gnome"})
}

func TestDefaultSkopeoInspect_Error(t *testing.T) {
	mock := setupInstallMockExec(t)
	mock.errFor["skopeo"] = fmt.Errorf("manifest unknown")
	if _, err := DefaultSkopeoInspect("docker://example.com/missing"); err == nil {
		t.Error("expected error to propagate")
	}
}
