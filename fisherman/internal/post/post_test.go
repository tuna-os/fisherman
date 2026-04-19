package post_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
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

	if err := post.CopyFlatpaks(target, nil, ""); err != nil {
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

	if err := post.CopyFlatpaks(target, []string{wanted}, ""); err != nil {
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

// TestCopyFlatpaks_EmitsPerAppSubsteps verifies that a substep is emitted for
// each wanted app that is found in the system install, so the UI can show
// individual app names as they are copied.
func TestCopyFlatpaks_EmitsPerAppSubsteps(t *testing.T) {
	mock := setupMockExec(t)
	target := t.TempDir()

	apps := []string{"org.mozilla.firefox", "org.gnome.Console"}
	refs := apps[0] + "/x86_64/stable\n" + apps[1] + "/x86_64/stable\n"

	mock.responses["flatpak list --system --columns=ref --app"] = struct {
		out []byte
		err error
	}{out: []byte(refs)}
	mock.responses["flatpak list --system --columns=ref"] = struct {
		out []byte
		err error
	}{out: []byte(refs)}
	mock.responses["flatpak list --user --columns=ref --app"] = struct {
		out []byte
		err error
	}{out: []byte("")}
	mock.responses["du -sb /var/lib/flatpak"] = struct {
		out []byte
		err error
	}{out: []byte("2048\t/var/lib/flatpak\n")}

	var substeps []string
	origSubstep := progress.SubstepFn
	progress.SubstepFn = func(msg string) { substeps = append(substeps, msg) }
	defer func() { progress.SubstepFn = origSubstep }()

	if err := post.CopyFlatpaks(target, apps, ""); err != nil {
		t.Fatalf("CopyFlatpaks: %v", err)
	}

	// Both wanted app names must appear in substep messages.
	for _, app := range apps {
		found := false
		for _, s := range substeps {
			if strings.Contains(s, app) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no substep message contained app name %q; substeps: %v", app, substeps)
		}
	}
}

// TestCopyFlatpaks_FlatpakVarPathOverride verifies that when flatpakVarPath is
// set (e.g. for GnomeOS/Dakota "state/os/default/var"), CopyFlatpaks writes
// to target/<flatpakVarPath>/lib/flatpak rather than the composefs default.
func TestCopyFlatpaks_FlatpakVarPathOverride(t *testing.T) {
	mock := setupMockExec(t)
	target := t.TempDir()

	// Simulate no local flatpak data (nothing to copy, just verify the mkdir target).
	mock.responses["du -sb /var/lib/flatpak"] = struct {
		out []byte
		err error
	}{out: []byte("0\t/var/lib/flatpak\n")}

	flatpakVarPath := "state/os/default/var"
	if err := post.CopyFlatpaks(target, nil, flatpakVarPath); err != nil {
		t.Fatalf("CopyFlatpaks: %v", err)
	}

	want := filepath.Join(target, flatpakVarPath, "lib", "flatpak")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected flatpak dir at %s to exist, got: %v", want, err)
	}

	// Ensure the legacy top-level var/lib/flatpak was NOT created.
	legacy := filepath.Join(target, "var", "lib", "flatpak")
	if _, err := os.Stat(legacy); err == nil {
		t.Errorf("legacy path %s was created — flatpakVarPath override was ignored", legacy)
	}
}

func TestEnsurePlymouthArgs(t *testing.T) {
tests := []struct {
name     string
input    string
wantOut  string
wantMod  bool
}{
{
name:    "adds rhgb and quiet when absent",
input:   "title TunaOS\noptions root=UUID=abc rw\n",
wantOut: "title TunaOS\noptions root=UUID=abc rw rhgb quiet\n",
wantMod: true,
},
{
name:    "adds only missing arg",
input:   "options root=UUID=abc rw rhgb\n",
wantOut: "options root=UUID=abc rw rhgb quiet\n",
wantMod: true,
},
{
name:    "no change when args already present",
input:   "options root=UUID=abc rw rhgb quiet\n",
wantOut: "options root=UUID=abc rw rhgb quiet\n",
wantMod: false,
},
}

for _, tc := range tests {
t.Run(tc.name, func(t *testing.T) {
dir := t.TempDir()
entriesDir := dir + "/boot/loader/entries"
if err := os.MkdirAll(entriesDir, 0o755); err != nil {
t.Fatal(err)
}
entryPath := entriesDir + "/test.conf"
if err := os.WriteFile(entryPath, []byte(tc.input), 0o644); err != nil {
t.Fatal(err)
}
n, err := post.EnsurePlymouthArgs(dir)
if err != nil {
t.Fatalf("EnsurePlymouthArgs: %v", err)
}
if tc.wantMod && n == 0 {
t.Error("expected entry to be modified, but it was not")
}
if !tc.wantMod && n != 0 {
t.Error("expected no modification, but entry was changed")
}
got, _ := os.ReadFile(entryPath)
if string(got) != tc.wantOut {
t.Errorf("entry content:\ngot:  %q\nwant: %q", string(got), tc.wantOut)
}
})
}
}

func TestEnsureLuksArgs(t *testing.T) {
const testUUID = "1520bba9-010e-443d-b082-2fe56abdfee1"
const wantArg = "rd.luks.name=" + testUUID + "=root"

t.Run("injects rd.luks.name into grub path", func(t *testing.T) {
dir := t.TempDir()
entriesDir := dir + "/boot/loader/entries"
if err := os.MkdirAll(entriesDir, 0o755); err != nil {
t.Fatal(err)
}
entryPath := entriesDir + "/test.conf"
input := "title TunaOS\noptions root=UUID=abc rw\n"
if err := os.WriteFile(entryPath, []byte(input), 0o644); err != nil {
t.Fatal(err)
}
n, err := post.EnsureLuksArgs(dir, testUUID)
if err != nil {
t.Fatalf("EnsureLuksArgs: %v", err)
}
if n != 1 {
t.Errorf("expected 1 entry modified, got %d", n)
}
got, _ := os.ReadFile(entryPath)
if !strings.Contains(string(got), wantArg) {
t.Errorf("entry missing %q:\n%s", wantArg, got)
}
})

t.Run("injects rd.luks.name into systemd-boot path", func(t *testing.T) {
dir := t.TempDir()
entriesDir := dir + "/boot/efi/loader/entries"
if err := os.MkdirAll(entriesDir, 0o755); err != nil {
t.Fatal(err)
}
entryPath := entriesDir + "/test.conf"
input := "title TunaOS\noptions root=UUID=abc rw\n"
if err := os.WriteFile(entryPath, []byte(input), 0o644); err != nil {
t.Fatal(err)
}
n, err := post.EnsureLuksArgs(dir, testUUID)
if err != nil {
t.Fatalf("EnsureLuksArgs: %v", err)
}
if n != 1 {
t.Errorf("expected 1 entry modified, got %d", n)
}
got, _ := os.ReadFile(entryPath)
if !strings.Contains(string(got), wantArg) {
t.Errorf("entry missing %q:\n%s", wantArg, got)
}
})

t.Run("idempotent — does not duplicate rd.luks.name", func(t *testing.T) {
dir := t.TempDir()
entriesDir := dir + "/boot/loader/entries"
if err := os.MkdirAll(entriesDir, 0o755); err != nil {
t.Fatal(err)
}
entryPath := entriesDir + "/test.conf"
input := "title TunaOS\noptions root=UUID=abc rw " + wantArg + "\n"
if err := os.WriteFile(entryPath, []byte(input), 0o644); err != nil {
t.Fatal(err)
}
n, err := post.EnsureLuksArgs(dir, testUUID)
if err != nil {
t.Fatalf("EnsureLuksArgs: %v", err)
}
if n != 0 {
t.Errorf("expected 0 entries modified (idempotent), got %d", n)
}
got, _ := os.ReadFile(entryPath)
count := strings.Count(string(got), wantArg)
if count != 1 {
t.Errorf("expected arg to appear once, got %d times:\n%s", count, got)
}
})

t.Run("empty UUID is a no-op", func(t *testing.T) {
dir := t.TempDir()
n, err := post.EnsureLuksArgs(dir, "")
if err != nil {
t.Fatalf("EnsureLuksArgs with empty UUID: %v", err)
}
if n != 0 {
t.Errorf("expected 0, got %d", n)
}
})
}
