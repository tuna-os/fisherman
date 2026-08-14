package post

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
)

// Tests for internal/post/caches.go (was 0%): the first-boot cache warmers.

// cachesMockExecutor records commands and returns configurable results.
type cachesMockExecutor struct {
	calls    []string // "name arg1 arg2"
	runErr   error
	output   []byte
	outputOK map[string]bool // command keys that should return output
}

func (m *cachesMockExecutor) Command(name string, args ...string) runner.Command {
	full := name
	if len(args) > 0 {
		full += " " + strings.Join(args, " ")
	}
	m.calls = append(m.calls, full)
	return &cachesMockCommand{exec: m}
}

type cachesMockCommand struct {
	exec *cachesMockExecutor
}

func (c *cachesMockCommand) Run() error { return c.exec.runErr }
func (c *cachesMockCommand) Start() error {
	return c.exec.runErr
}
func (c *cachesMockCommand) Wait() error           { return c.exec.runErr }
func (c *cachesMockCommand) SetStdin(r io.Reader)  {}
func (c *cachesMockCommand) SetStdout(w io.Writer) {}
func (c *cachesMockCommand) SetStderr(w io.Writer) {}
func (c *cachesMockCommand) Output() ([]byte, error) {
	if c.exec.runErr != nil {
		return nil, c.exec.runErr
	}
	return c.exec.output, nil
}

func setupCachesMock(t *testing.T) *cachesMockExecutor {
	t.Helper()
	m := &cachesMockExecutor{}
	orig := Exec
	Exec = m
	t.Cleanup(func() { Exec = orig })
	return m
}

func TestWarmFlatpakAppstream(t *testing.T) {
	target := t.TempDir()
	flatpakDir := filepath.Join(target, "var", "lib", "flatpak")
	appstreamDir := filepath.Join(flatpakDir, "appstream")
	if err := os.MkdirAll(appstreamDir, 0o755); err != nil {
		t.Fatal(err)
	}

	orig := nowUnix
	nowUnix = func() int64 { return 12345 }
	t.Cleanup(func() { nowUnix = orig })

	if err := warmFlatpakAppstream(flatpakDir); err != nil {
		t.Fatalf("warmFlatpakAppstream: %v", err)
	}
	ts, err := os.ReadFile(filepath.Join(appstreamDir, ".timestamp"))
	if err != nil {
		t.Fatalf("reading timestamp: %v", err)
	}
	if string(ts) != "12345" {
		t.Errorf("timestamp = %q, want 12345", string(ts))
	}
}

func TestWarmFlatpakAppstreamMissingDirs(t *testing.T) {
	target := t.TempDir()
	// No flatpak dir at all → nil, no error.
	if err := warmFlatpakAppstream(filepath.Join(target, "nope")); err != nil {
		t.Errorf("missing flatpak dir: %v", err)
	}
	// Flatpak dir exists but no appstream subdir → nil.
	flatpakDir := filepath.Join(target, "var", "lib", "flatpak")
	if err := os.MkdirAll(flatpakDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := warmFlatpakAppstream(flatpakDir); err != nil {
		t.Errorf("flatpak dir without appstream: %v", err)
	}
	if _, err := os.Stat(filepath.Join(flatpakDir, "appstream", ".timestamp")); err == nil {
		t.Error(".timestamp written although appstream dir missing")
	}
}

func TestWarmFontCacheNoFonts(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()
	if err := warmFontCache(target); err != nil {
		t.Fatalf("warmFontCache: %v", err)
	}
	// No font dirs → no fc-cache invocation.
	for _, call := range mock.calls {
		if strings.HasPrefix(call, "fc-cache") {
			t.Errorf("fc-cache called (%q) although no font dirs exist", call)
		}
	}
	// But the cache dir must be created.
	if _, err := os.Stat(filepath.Join(target, "var", "cache", "fontconfig")); err != nil {
		t.Errorf("fontconfig cache dir not created: %v", err)
	}
}

func TestWarmFontCacheSysrootAndFallback(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()
	// Create one font dir so fc-cache runs with --sysroot.
	if err := os.MkdirAll(filepath.Join(target, "usr", "share", "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}

	mock.runErr = nil
	if err := warmFontCache(target); err != nil {
		t.Fatalf("warmFontCache: %v", err)
	}
	found := false
	for _, call := range mock.calls {
		if strings.HasPrefix(call, "fc-cache") && strings.Contains(call, "--sysroot") {
			found = true
		}
	}
	if !found {
		t.Errorf("fc-cache --sysroot not invoked; calls=%v", mock.calls)
	}

	// Now make the sysroot form fail → fallback without --sysroot.
	mock.runErr = os.ErrNotExist
	if err := warmFontCache(target); err == nil {
		t.Error("expected error when both fc-cache forms fail")
	}
	fallback := false
	for _, call := range mock.calls {
		if strings.HasPrefix(call, "fc-cache") && !strings.Contains(call, "--sysroot") {
			fallback = true
		}
	}
	if !fallback {
		t.Errorf("fc-cache fallback (no --sysroot) not invoked; calls=%v", mock.calls)
	}
}

func TestWarmIconCache(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()

	// No icons dir → nil, no calls.
	if err := warmIconCache(target); err != nil {
		t.Fatalf("warmIconCache(empty): %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("unexpected calls for empty target: %v", mock.calls)
	}

	// A real theme dir with index.theme triggers gtk4-update-icon-cache.
	themeDir := filepath.Join(target, "usr", "share", "icons", "Adwaita")
	if err := os.MkdirAll(themeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "index.theme"), []byte("[Icon Theme]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock.runErr = nil
	if err := warmIconCache(target); err != nil {
		t.Fatalf("warmIconCache: %v", err)
	}
	if !strings.Contains(strings.Join(mock.calls, "\n"), "gtk4-update-icon-cache") {
		t.Errorf("gtk4-update-icon-cache not invoked; calls=%v", mock.calls)
	}

	// gtk4 failing falls back to gtk-update-icon-cache.
	mock.runErr = os.ErrNotExist
	_ = warmIconCache(target)
	if !strings.Contains(strings.Join(mock.calls, "\n"), "gtk-update-icon-cache") {
		t.Errorf("gtk-update-icon-cache fallback not invoked; calls=%v", mock.calls)
	}
}

func TestWarmGSettingsSchemas(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()

	if err := warmGSettingsSchemas(target); err != nil {
		t.Fatalf("no schemas dir: %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("unexpected calls without schemas dir: %v", mock.calls)
	}

	schemasDir := filepath.Join(target, "usr", "share", "glib-2.0", "schemas")
	if err := os.MkdirAll(schemasDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock.runErr = nil
	if err := warmGSettingsSchemas(target); err != nil {
		t.Fatalf("warmGSettingsSchemas: %v", err)
	}
	if !strings.Contains(strings.Join(mock.calls, "\n"), "glib-compile-schemas "+schemasDir) {
		t.Errorf("glib-compile-schemas not invoked with schemas dir; calls=%v", mock.calls)
	}
}

func TestWarmPixbufLoaders(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()

	if err := warmPixbufLoaders(target); err != nil {
		t.Fatalf("no loader dir: %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("unexpected calls without loader dir: %v", mock.calls)
	}

	// Standard lib64 location.
	loaderDir := filepath.Join(target, "usr", "lib64", "gdk-pixbuf-2.0", "2.10.0")
	if err := os.MkdirAll(loaderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock.output = []byte("loaders output")
	mock.runErr = nil
	if err := warmPixbufLoaders(target); err != nil {
		t.Fatalf("warmPixbufLoaders: %v", err)
	}
	if !strings.Contains(strings.Join(mock.calls, "\n"), "gdk-pixbuf-query-loaders") {
		t.Errorf("gdk-pixbuf-query-loaders not invoked; calls=%v", mock.calls)
	}
	cache, err := os.ReadFile(filepath.Join(loaderDir, "loaders.cache"))
	if err != nil {
		t.Fatalf("reading loaders.cache: %v", err)
	}
	if string(cache) != "loaders output" {
		t.Errorf("loaders.cache = %q", string(cache))
	}

	// lib (not lib64) fallback.
	target2 := t.TempDir()
	loaderDir2 := filepath.Join(target2, "usr", "lib", "gdk-pixbuf-2.0", "2.10.0")
	if err := os.MkdirAll(loaderDir2, 0o755); err != nil {
		t.Fatal(err)
	}
	mock.calls = nil
	if err := warmPixbufLoaders(target2); err != nil {
		t.Fatalf("warmPixbufLoaders(lib): %v", err)
	}
	if _, err := os.Stat(filepath.Join(loaderDir2, "loaders.cache")); err != nil {
		t.Errorf("loaders.cache not written for lib path: %v", err)
	}
}

func TestWarmGIOModules(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()

	if err := warmGIOModules(target); err != nil {
		t.Fatalf("no modules dir: %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("unexpected calls without modules dir: %v", mock.calls)
	}

	modulesDir := filepath.Join(target, "usr", "lib64", "gio", "modules")
	if err := os.MkdirAll(modulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock.runErr = nil
	if err := warmGIOModules(target); err != nil {
		t.Fatalf("warmGIOModules: %v", err)
	}
	if !strings.Contains(strings.Join(mock.calls, "\n"), "gio-querymodules "+modulesDir) {
		t.Errorf("gio-querymodules not invoked; calls=%v", mock.calls)
	}
}

func TestWarmLdconfigAndManDB(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()

	if err := warmLdconfig(target); err != nil {
		t.Fatalf("warmLdconfig: %v", err)
	}
	if !strings.Contains(strings.Join(mock.calls, "\n"), "ldconfig -r "+target) {
		t.Errorf("ldconfig -r not invoked; calls=%v", mock.calls)
	}

	// No man dir → nil, no mandb call.
	mock.calls = nil
	if err := warmManDB(target); err != nil {
		t.Fatalf("warmManDB(no dir): %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("unexpected calls without man dir: %v", mock.calls)
	}

	manDir := filepath.Join(target, "usr", "share", "man")
	if err := os.MkdirAll(manDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mock.runErr = nil
	if err := warmManDB(target); err != nil {
		t.Fatalf("warmManDB: %v", err)
	}
	if !strings.Contains(strings.Join(mock.calls, "\n"), "mandb --no-purge") {
		t.Errorf("mandb not invoked; calls=%v", mock.calls)
	}
}

// TestWarmCachesOstree verifies the orchestrator runs ldconfig for ostree
// targets (skipLdconfig=false) and warms all caches.
func TestWarmCachesOstree(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()
	// Presence of /ostree makes isComposeFsNative return false.
	if err := os.MkdirAll(filepath.Join(target, "ostree"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := nowUnix
	nowUnix = func() int64 { return 42 }
	t.Cleanup(func() { nowUnix = orig })

	WarmCaches(target)

	joined := strings.Join(mock.calls, "\n")
	if !strings.Contains(joined, "ldconfig -r "+target) {
		t.Errorf("ldconfig missing for ostree target; calls=%v", mock.calls)
	}
}

// TestWarmCachesComposeFs verifies ldconfig is skipped for composefs-native
// targets and the flatpak appstream path resolves under state/os/default/var.
func TestWarmCachesComposeFs(t *testing.T) {
	mock := setupCachesMock(t)
	target := t.TempDir()
	// Presence of state/deploy makes isComposeFsNative return true.
	if err := os.MkdirAll(filepath.Join(target, "state", "deploy", "abc"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate the writable /var with an appstream dir.
	flatpakDir := filepath.Join(target, "state", "os", "default", "var", "lib", "flatpak")
	if err := os.MkdirAll(filepath.Join(flatpakDir, "appstream"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := nowUnix
	nowUnix = func() int64 { return 42 }
	t.Cleanup(func() { nowUnix = orig })

	WarmCaches(target)

	joined := strings.Join(mock.calls, "\n")
	if strings.Contains(joined, "ldconfig") {
		t.Errorf("ldconfig should be skipped for composefs target; calls=%v", mock.calls)
	}
	if _, err := os.Stat(filepath.Join(flatpakDir, "appstream", ".timestamp")); err != nil {
		t.Errorf("composefs flatpak appstream timestamp not written: %v", err)
	}
}
