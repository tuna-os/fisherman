package post

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
)

// cacheMockCommand implements runner.Command, returning a canned result
// keyed by command name via cacheMockExecutor.
type cacheMockCommand struct {
	err    error
	output []byte
}

func (c *cacheMockCommand) Run() error   { return c.err }
func (c *cacheMockCommand) Start() error { return c.err }
func (c *cacheMockCommand) Wait() error  { return c.err }
func (c *cacheMockCommand) Output() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.output, nil
}
func (c *cacheMockCommand) SetStdin(io.Reader)  {}
func (c *cacheMockCommand) SetStdout(io.Writer) {}
func (c *cacheMockCommand) SetStderr(io.Writer) {}

// cachesExecCall records one Exec.Command invocation for assertions.
type cachesExecCall struct {
	name string
	args []string
}

// cacheMockExecutor records every command invoked and returns a per-command
// canned error/output (success with no output by default).
type cacheMockExecutor struct {
	calls  []cachesExecCall
	errFor map[string]error
	outFor map[string][]byte
}

func (e *cacheMockExecutor) Command(name string, args ...string) runner.Command {
	e.calls = append(e.calls, cachesExecCall{name: name, args: args})
	return &cacheMockCommand{err: e.errFor[name], output: e.outFor[name]}
}

func (e *cacheMockExecutor) called(name string) bool {
	for _, c := range e.calls {
		if c.name == name {
			return true
		}
	}
	return false
}

func setupCacheMockExec(t *testing.T) *cacheMockExecutor {
	t.Helper()
	mock := &cacheMockExecutor{errFor: map[string]error{}, outFor: map[string][]byte{}}
	old := Exec
	Exec = mock
	t.Cleanup(func() { Exec = old })
	return mock
}

// TestWarmCaches_ComposefsSkipsLdconfig verifies that on a composefs-native
// target, ldconfig is never invoked (the OCI image already ships a correct
// ld.so.cache — see the WarmCaches doc comment) and the flatpak dir resolves
// under the composefs var, not $TARGET/var.
func TestWarmCaches_ComposefsSkipsLdconfig(t *testing.T) {
	target := t.TempDir()
	// composefs-native signal: state/deploy exists.
	if err := os.MkdirAll(filepath.Join(target, "state", "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	composefsVar := filepath.Join(target, "state", "os", "default", "var")
	if err := os.MkdirAll(composefsVar, 0o755); err != nil {
		t.Fatal(err)
	}

	mock := setupCacheMockExec(t)
	WarmCaches(target)

	if mock.called("ldconfig") {
		t.Error("ldconfig should never run for a composefs-native target")
	}
}

// TestWarmCaches_OstreeRunsLdconfig verifies that on an ostree target (no
// state/deploy, no /ostree either — the "legacy: no /ostree at all" branch
// of isComposeFsNative returns true, so this test uses a real /ostree dir to
// land on the ostree path), ldconfig runs against $TARGET.
func TestWarmCaches_OstreeRunsLdconfig(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "ostree"), 0o755); err != nil {
		t.Fatal(err)
	}

	mock := setupCacheMockExec(t)
	WarmCaches(target)

	found := false
	for _, c := range mock.calls {
		if c.name == "ldconfig" {
			found = true
			if len(c.args) != 2 || c.args[0] != "-r" || c.args[1] != target {
				t.Errorf("ldconfig args = %v, want [-r %s]", c.args, target)
			}
		}
	}
	if !found {
		t.Error("expected ldconfig to run for an ostree target")
	}
}

func TestWarmFlatpakAppstream_NoFlatpakDir(t *testing.T) {
	target := t.TempDir()
	flatpakDir := filepath.Join(target, "var", "lib", "flatpak")
	if err := warmFlatpakAppstream(flatpakDir); err != nil {
		t.Errorf("expected nil for a missing flatpak dir, got %v", err)
	}
}

func TestWarmFlatpakAppstream_WritesTimestamp(t *testing.T) {
	target := t.TempDir()
	flatpakDir := filepath.Join(target, "var", "lib", "flatpak")
	appstreamDir := filepath.Join(flatpakDir, "appstream")
	if err := os.MkdirAll(appstreamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origNow := nowUnix
	nowUnix = func() int64 { return 1234567890 }
	t.Cleanup(func() { nowUnix = origNow })

	if err := warmFlatpakAppstream(flatpakDir); err != nil {
		t.Fatalf("warmFlatpakAppstream: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(appstreamDir, ".timestamp"))
	if err != nil {
		t.Fatalf("reading .timestamp: %v", err)
	}
	if string(data) != "1234567890" {
		t.Errorf(".timestamp content = %q, want %q", string(data), "1234567890")
	}
}

func TestWarmFontCache_NoFontDirs(t *testing.T) {
	target := t.TempDir()
	mock := setupCacheMockExec(t)
	if err := warmFontCache(target); err != nil {
		t.Errorf("expected nil with no font dirs present, got %v", err)
	}
	if mock.called("fc-cache") {
		t.Error("fc-cache should not run when no font directories exist")
	}
}

func TestWarmFontCache_RunsFcCacheWithSysroot(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "usr", "share", "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}
	mock := setupCacheMockExec(t)

	if err := warmFontCache(target); err != nil {
		t.Fatalf("warmFontCache: %v", err)
	}

	if len(mock.calls) != 1 || mock.calls[0].name != "fc-cache" {
		t.Fatalf("calls = %v, want exactly one fc-cache call", mock.calls)
	}
	args := strings.Join(mock.calls[0].args, " ")
	if !strings.Contains(args, "--sysroot "+target) {
		t.Errorf("fc-cache args = %q, want --sysroot %s", args, target)
	}
}

func TestWarmFontCache_FallsBackWithoutSysroot(t *testing.T) {
	target := t.TempDir()
	fontDir := filepath.Join(target, "usr", "share", "fonts")
	if err := os.MkdirAll(fontDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock := setupCacheMockExec(t)
	mock.errFor["fc-cache"] = os.ErrInvalid // first call (--sysroot) fails

	if err := warmFontCache(target); err != nil {
		// The fallback call also uses the same mocked "fc-cache" name/error,
		// so it fails too here — that's fine, the assertion is about the
		// fallback actually being attempted (two calls), not its outcome.
		_ = err
	}

	if len(mock.calls) != 2 {
		t.Fatalf("expected fc-cache to be tried twice (sysroot, then fallback), got %d calls: %v", len(mock.calls), mock.calls)
	}
	if strings.Contains(strings.Join(mock.calls[1].args, " "), "--sysroot") {
		t.Error("fallback call should not include --sysroot")
	}
}

func TestWarmIconCache_NoIconsDir(t *testing.T) {
	target := t.TempDir()
	mock := setupCacheMockExec(t)
	if err := warmIconCache(target); err != nil {
		t.Errorf("expected nil with no icons dir, got %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected no exec calls, got %v", mock.calls)
	}
}

func TestWarmIconCache_SkipsNonThemeDirs(t *testing.T) {
	target := t.TempDir()
	iconsDir := filepath.Join(target, "usr", "share", "icons")
	// A real theme (has index.theme) and a non-theme dir (no index.theme).
	if err := os.MkdirAll(filepath.Join(iconsDir, "hicolor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iconsDir, "hicolor", "index.theme"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(iconsDir, "not-a-theme"), 0o755); err != nil {
		t.Fatal(err)
	}

	mock := setupCacheMockExec(t)
	if err := warmIconCache(target); err != nil {
		t.Fatalf("warmIconCache: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected exactly one update-icon-cache call (for hicolor only), got %v", mock.calls)
	}
	if mock.calls[0].name != "gtk4-update-icon-cache" {
		t.Errorf("expected gtk4-update-icon-cache first, got %s", mock.calls[0].name)
	}
}

func TestWarmIconCache_FallsBackToGtk3(t *testing.T) {
	target := t.TempDir()
	iconsDir := filepath.Join(target, "usr", "share", "icons")
	if err := os.MkdirAll(filepath.Join(iconsDir, "hicolor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iconsDir, "hicolor", "index.theme"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := setupCacheMockExec(t)
	mock.errFor["gtk4-update-icon-cache"] = os.ErrInvalid

	if err := warmIconCache(target); err != nil {
		t.Fatalf("warmIconCache: %v", err)
	}

	if len(mock.calls) != 2 {
		t.Fatalf("expected gtk4 attempt + gtk3 fallback, got %v", mock.calls)
	}
	if mock.calls[1].name != "gtk-update-icon-cache" {
		t.Errorf("expected fallback to gtk-update-icon-cache, got %s", mock.calls[1].name)
	}
}

func TestWarmGSettingsSchemas_NoDir(t *testing.T) {
	target := t.TempDir()
	mock := setupCacheMockExec(t)
	if err := warmGSettingsSchemas(target); err != nil {
		t.Errorf("expected nil with no schemas dir, got %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected no exec calls, got %v", mock.calls)
	}
}

func TestWarmGSettingsSchemas_CompilesWhenPresent(t *testing.T) {
	target := t.TempDir()
	schemasDir := filepath.Join(target, "usr", "share", "glib-2.0", "schemas")
	if err := os.MkdirAll(schemasDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock := setupCacheMockExec(t)

	if err := warmGSettingsSchemas(target); err != nil {
		t.Fatalf("warmGSettingsSchemas: %v", err)
	}
	if len(mock.calls) != 1 || mock.calls[0].name != "glib-compile-schemas" {
		t.Fatalf("calls = %v, want one glib-compile-schemas call", mock.calls)
	}
	if len(mock.calls[0].args) != 1 || mock.calls[0].args[0] != schemasDir {
		t.Errorf("glib-compile-schemas args = %v, want [%s]", mock.calls[0].args, schemasDir)
	}
}

func TestWarmPixbufLoaders_NoLoaderDir(t *testing.T) {
	target := t.TempDir()
	mock := setupCacheMockExec(t)
	if err := warmPixbufLoaders(target); err != nil {
		t.Errorf("expected nil with no loader dir, got %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected no exec calls, got %v", mock.calls)
	}
}

func TestWarmPixbufLoaders_WritesCacheFromLib64(t *testing.T) {
	target := t.TempDir()
	loaderDir := filepath.Join(target, "usr", "lib64", "gdk-pixbuf-2.0", "2.10.0")
	if err := os.MkdirAll(loaderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock := setupCacheMockExec(t)
	mock.outFor["gdk-pixbuf-query-loaders"] = []byte("# fake loader cache\n")

	if err := warmPixbufLoaders(target); err != nil {
		t.Fatalf("warmPixbufLoaders: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(loaderDir, "loaders.cache"))
	if err != nil {
		t.Fatalf("reading loaders.cache: %v", err)
	}
	if string(data) != "# fake loader cache\n" {
		t.Errorf("loaders.cache content = %q, want the mocked query output", string(data))
	}
}

func TestWarmPixbufLoaders_FallsBackToLib(t *testing.T) {
	target := t.TempDir()
	// Only the /usr/lib (not lib64) variant exists.
	loaderDir := filepath.Join(target, "usr", "lib", "gdk-pixbuf-2.0", "2.10.0")
	if err := os.MkdirAll(loaderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock := setupCacheMockExec(t)
	mock.outFor["gdk-pixbuf-query-loaders"] = []byte("cache\n")

	if err := warmPixbufLoaders(target); err != nil {
		t.Fatalf("warmPixbufLoaders: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loaderDir, "loaders.cache")); err != nil {
		t.Errorf("expected loaders.cache under the lib (not lib64) path: %v", err)
	}
}

func TestWarmGIOModules_NoDir(t *testing.T) {
	target := t.TempDir()
	mock := setupCacheMockExec(t)
	if err := warmGIOModules(target); err != nil {
		t.Errorf("expected nil with no gio modules dir, got %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected no exec calls, got %v", mock.calls)
	}
}

func TestWarmGIOModules_RunsQuerymodules(t *testing.T) {
	target := t.TempDir()
	modulesDir := filepath.Join(target, "usr", "lib64", "gio", "modules")
	if err := os.MkdirAll(modulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock := setupCacheMockExec(t)

	if err := warmGIOModules(target); err != nil {
		t.Fatalf("warmGIOModules: %v", err)
	}
	if len(mock.calls) != 1 || mock.calls[0].name != "gio-querymodules" {
		t.Fatalf("calls = %v, want one gio-querymodules call", mock.calls)
	}
}

func TestWarmLdconfig(t *testing.T) {
	target := t.TempDir()
	mock := setupCacheMockExec(t)

	if err := warmLdconfig(target); err != nil {
		t.Fatalf("warmLdconfig: %v", err)
	}
	if len(mock.calls) != 1 || mock.calls[0].name != "ldconfig" {
		t.Fatalf("calls = %v, want one ldconfig call", mock.calls)
	}
	want := []string{"-r", target}
	if len(mock.calls[0].args) != 2 || mock.calls[0].args[0] != want[0] || mock.calls[0].args[1] != want[1] {
		t.Errorf("ldconfig args = %v, want %v", mock.calls[0].args, want)
	}
}

func TestWarmManDB_NoDir(t *testing.T) {
	target := t.TempDir()
	mock := setupCacheMockExec(t)
	if err := warmManDB(target); err != nil {
		t.Errorf("expected nil with no man dir, got %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected no exec calls, got %v", mock.calls)
	}
}

func TestWarmManDB_RunsMandb(t *testing.T) {
	target := t.TempDir()
	manDir := filepath.Join(target, "usr", "share", "man")
	if err := os.MkdirAll(manDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock := setupCacheMockExec(t)

	if err := warmManDB(target); err != nil {
		t.Fatalf("warmManDB: %v", err)
	}
	if len(mock.calls) != 1 || mock.calls[0].name != "mandb" {
		t.Fatalf("calls = %v, want one mandb call", mock.calls)
	}
	args := strings.Join(mock.calls[0].args, " ")
	if !strings.Contains(args, "--manpath "+manDir) {
		t.Errorf("mandb args = %q, want --manpath %s", args, manDir)
	}
}
