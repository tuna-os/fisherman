package install

import (
	"os"
	"strings"
	"testing"
)

// TestAppendImageStoreArgs_NoStoresNoEnv is the trivial case: no caller env,
// no recipe-supplied stores → no podman flags appended, no temp file created.
func TestAppendImageStoreArgs_NoStoresNoEnv(t *testing.T) {
	t.Setenv("CONTAINERS_STORAGE_CONF", "")
	scratch := t.TempDir()
	out, cleanup := appendImageStoreArgs(nil, scratch, Options{})
	defer cleanup()
	if len(out) != 0 {
		t.Errorf("expected no args; got %v", out)
	}
	// No file should have been created under scratch/fisherman-conf.
	if entries, _ := os.ReadDir(scratch + "/fisherman-conf"); len(entries) != 0 {
		t.Errorf("expected no conf file; got %d entries", len(entries))
	}
}

// TestAppendImageStoreArgs_WritesGeneratedConf verifies that when the recipe
// declares additional stores (and no caller env override is set), each store
// is bind-mounted read-only and a generated storage.conf listing them all is
// passed via CONTAINERS_STORAGE_CONF.
func TestAppendImageStoreArgs_WritesGeneratedConf(t *testing.T) {
	t.Setenv("CONTAINERS_STORAGE_CONF", "")
	scratch := t.TempDir()
	opts := Options{AdditionalImageStores: []string{
		"/var/lib/store-a",
		"/var/lib/store-b",
	}}
	out, cleanup := appendImageStoreArgs(nil, scratch, opts)
	defer cleanup()

	joined := strings.Join(out, " ")
	for _, want := range []string{
		"/var/lib/store-a:/var/lib/store-a:ro",
		"/var/lib/store-b:/var/lib/store-b:ro",
		"CONTAINERS_STORAGE_CONF=/etc/containers/storage.conf",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("podman args missing %q; got: %v", want, out)
		}
	}

	// Locate the generated host-side conf file and assert it lists both stores.
	confDir := scratch + "/fisherman-conf"
	entries, err := os.ReadDir(confDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one generated conf in %s; got %v err=%v",
			confDir, entries, err)
	}
	body, err := os.ReadFile(confDir + "/" + entries[0].Name())
	if err != nil {
		t.Fatalf("reading generated conf: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"/var/lib/store-a"`) || !strings.Contains(s, `"/var/lib/store-b"`) {
		t.Errorf("generated conf missing stores; body=%q", s)
	}
	if !strings.Contains(s, "additionalimagestores = [") {
		t.Errorf("generated conf missing additionalimagestores key; body=%q", s)
	}
}

// TestAppendImageStoreArgs_CallerEnvWins verifies the documented escape hatch:
// when CONTAINERS_STORAGE_CONF is set, fisherman bind-mounts it into scratch
// and forwards the env var as-is. No fisherman-generated conf is written.
func TestAppendImageStoreArgs_CallerEnvWins(t *testing.T) {
	scratch := t.TempDir()
	callerConf := scratch + "/caller-storage.conf"
	if err := os.WriteFile(callerConf, []byte("# caller\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTAINERS_STORAGE_CONF", callerConf)

	opts := Options{AdditionalImageStores: []string{"/var/lib/store-x"}}
	out, cleanup := appendImageStoreArgs(nil, scratch, opts)
	defer cleanup()

	joined := strings.Join(out, " ")
	// Store still gets bind-mounted (so storage.conf entries resolve)…
	if !strings.Contains(joined, "/var/lib/store-x:/var/lib/store-x:ro") {
		t.Errorf("expected store bind-mount; got: %v", out)
	}
	// …but the caller's conf wins for the env var.
	if !strings.Contains(joined, "CONTAINERS_STORAGE_CONF=/etc/containers/storage.conf") {
		t.Errorf("expected caller env to win; got: %v", out)
	}
	// And no auto-generated file should have been written under fisherman-conf.
	if entries, _ := os.ReadDir(scratch + "/fisherman-conf"); len(entries) != 0 {
		t.Errorf("auto-generated conf should be skipped when caller env wins; got %d entries", len(entries))
	}
}
