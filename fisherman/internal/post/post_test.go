package post_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/runner"
)

// mockCommand implements runner.Command for testing.
type mockCommand struct {
	name   string
	args   []string
	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader
	output []byte
	err    error
}

func (c *mockCommand) Run() error   { return c.err }
func (c *mockCommand) Start() error { return c.err }
func (c *mockCommand) Wait() error  { return c.err }
func (c *mockCommand) Output() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.output, nil
}
func (c *mockCommand) SetStdin(r io.Reader)  { c.stdin = r }
func (c *mockCommand) SetStdout(w io.Writer) { c.stdout = w }
func (c *mockCommand) SetStderr(w io.Writer) { c.stderr = w }

// mockExecutor implements runner.Executor for testing.
type mockExecutor struct {
	calls []execCall
	// responses maps a command (joined by space) to a response output/error.
	responses map[string]struct {
		out []byte
		err error
	}
}

func (e *mockExecutor) Command(name string, args ...string) runner.Command {
	e.calls = append(e.calls, execCall{name: name, args: args})
	full := name
	if len(args) > 0 {
		full += " " + strings.Join(args, " ")
	}

	// Try exact match first, then prefix match for things like 'du -sb /var/lib/flatpak'
	res, ok := e.responses[full]
	if !ok {
		// Fallback for du/flatpak list calls which might have dynamic paths in tests
		for k, v := range e.responses {
			if strings.HasPrefix(full, k) {
				res = v
				ok = true
				break
			}
		}
	}

	return &mockCommand{
		name:   name,
		args:   args,
		output: res.out,
		err:    res.err,
	}
}

func setupMockExec(t *testing.T) *mockExecutor {
	t.Helper()
	mock := &mockExecutor{
		responses: make(map[string]struct {
			out []byte
			err error
		}),
	}
	old := post.Exec
	post.Exec = mock
	t.Cleanup(func() { post.Exec = old })
	return mock
}

// TestWriteHostname_ComposeFsNative verifies that when no /ostree/ directory
// exists under the target (composefs-native deployment), hostname is written
// directly to $TARGET/etc/hostname.
func TestWriteHostname_ComposeFsNative(t *testing.T) {
	target := t.TempDir()
	if err := post.WriteHostname(target, "myhost"); err != nil {
		t.Fatalf("WriteHostname: %v", err)
	}

	hostnameFile := filepath.Join(target, "etc", "hostname")
	data, err := os.ReadFile(hostnameFile)
	if err != nil {
		t.Fatalf("reading hostname file: %v", err)
	}
	if string(data) != "myhost\n" {
		t.Errorf("hostname file content = %q, want %q", string(data), "myhost\n")
	}
}

// TestWriteHostname_OstreeBackend verifies that when /ostree/ exists under the
// target (ostree-based deployment), hostname is written to the path returned by
// DeploymentDirFn, not to $TARGET/etc/hostname directly.
func TestWriteHostname_OstreeBackend(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "ostree"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeDeployDir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(fakeDeployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	post.DeploymentDirFn = func(sysroot string) (string, error) {
		return fakeDeployDir, nil
	}
	t.Cleanup(func() { post.DeploymentDirFn = post.DefaultDeploymentDir })

	if err := post.WriteHostname(target, "tunahost"); err != nil {
		t.Fatalf("WriteHostname: %v", err)
	}

	hostnameInDeploy := filepath.Join(fakeDeployDir, "etc", "hostname")
	data, err := os.ReadFile(hostnameInDeploy)
	if err != nil {
		t.Fatalf("reading hostname from deploy dir: %v", err)
	}
	if string(data) != "tunahost\n" {
		t.Errorf("hostname = %q, want %q", string(data), "tunahost\n")
	}
}

// TestCopyFlatpaks_NoLocalData verifies that when no local flatpak data is found,
// the function returns successfully without attempting a copy.
func TestCopyFlatpaks_NoLocalData(t *testing.T) {
	mock := setupMockExec(t)
	target := t.TempDir()

	// Mock 'du -sb /var/lib/flatpak' returning 0
	mock.responses["du -sb /var/lib/flatpak"] = struct {
		out []byte
		err error
	}{out: []byte("0\t/var/lib/flatpak\n")}

	if err := post.CopyFlatpaks(target, nil); err != nil {
		t.Fatalf("CopyFlatpaks: %v", err)
	}

	// Verify no tar or flatpak install calls were made
	for _, call := range mock.calls {
		if call.name == "tar" || (call.name == "flatpak" && len(call.args) > 0 && call.args[0] == "install") {
			t.Errorf("unexpected command call: %s %v", call.name, call.args)
		}
	}
}

// TestCopyFlatpaks_PromotesUserApps verifies that wanted apps missing from the
// system installation are promoted from the user installation.
func TestCopyFlatpaks_PromotesUserApps(t *testing.T) {
	mock := setupMockExec(t)
	target := t.TempDir()

	// 1. Mock 'flatpak list --system --app' (empty)
	mock.responses["flatpak list --system --columns=ref --app"] = struct {
		out []byte
		err error
	}{out: []byte("")}

	// 2. Mock 'flatpak list --system' (empty)
	mock.responses["flatpak list --system --columns=ref"] = struct {
		out []byte
		err error
	}{out: []byte("")}

	// 3. Mock 'flatpak list --user --columns=ref --app' (contains wanted app)
	wanted := "org.mozilla.firefox"
	mock.responses["flatpak list --user --columns=ref --app"] = struct {
		out []byte
		err error
	}{out: []byte(wanted + "/x86_64/stable\n")}

	// 4. Mock 'du -sb /var/lib/flatpak' (has data)
	mock.responses["du -sb /var/lib/flatpak"] = struct {
		out []byte
		err error
	}{out: []byte("1024\t/var/lib/flatpak\n")}

	// 5. Mock 'flatpak install --system' (success)
	mock.responses[fmt.Sprintf("flatpak install --system -y --noninteractive %s/x86_64/stable", wanted)] = struct {
		out []byte
		err error
	}{out: []byte("OK")}

	if err := post.CopyFlatpaks(target, []string{wanted}); err != nil {
		t.Fatalf("CopyFlatpaks: %v", err)
	}

	// Verify promotion call
	promoted := false
	for _, call := range mock.calls {
		if call.name == "flatpak" && len(call.args) > 0 && call.args[0] == "install" {
			for _, arg := range call.args {
				if strings.Contains(arg, wanted) {
					promoted = true
				}
			}
		}
	}
	if !promoted {
		t.Errorf("expected %s to be promoted to system, but no flatpak install call found", wanted)
	}
}
