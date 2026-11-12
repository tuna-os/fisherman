package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/diagnostics"
	"github.com/tuna-os/fisherman/internal/probe"
)

// withDiagnosticsSeams points the export at an empty root, a fixed clock
// and a fake probe.
func withDiagnosticsSeams(t *testing.T, probeFn func() (*probe.Result, error), timeout time.Duration) {
	t.Helper()
	oldProbe, oldTimeout, oldRoot, oldNow := diagnosticsProbe, diagnosticsProbeTimeout, diagnosticsRoot, diagnosticsNow
	diagnosticsProbe, diagnosticsProbeTimeout, diagnosticsRoot = probeFn, timeout, t.TempDir()
	diagnosticsNow = func() time.Time { return time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		diagnosticsProbe, diagnosticsProbeTimeout, diagnosticsRoot, diagnosticsNow = oldProbe, oldTimeout, oldRoot, oldNow
	})
}

func noProbe() (*probe.Result, error) { return &probe.Result{ProtocolVersion: 1}, nil }

func runDiag(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runDiagnostics(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestDiagnosticsUsage(t *testing.T) {
	withDiagnosticsSeams(t, noProbe, time.Second)
	dir := t.TempDir()
	for name, args := range map[string][]string{
		"no output":       {},
		"both outputs":    {"--stdout", "--output-dir", dir},
		"unknown":         {"--stdout", "--upload"},
		"missing value":   {"--stdout", "--recipe"},
		"empty value":     {"--stdout", "--recipe="},
		"two stdins":      {"--stdout", "--recipe", "-", "--progress-log", "-"},
		"probe twice":     {"--stdout", "--probe", "--probe-report", "x.json"},
		"positional only": {"recipe.json"},
	} {
		if code, _, errOut := runDiag(t, "", args...); code != exitUsage || !strings.Contains(errOut, "usage: fisherman diagnostics") {
			t.Errorf("%s: exit %d, stderr %q; want %d and the usage", name, code, errOut, exitUsage)
		}
	}
	if code, out, _ := runDiag(t, "", "--help"); code != 0 || !strings.Contains(out, "usage:") {
		t.Errorf("--help: exit %d", code)
	}
}

func TestDiagnosticsStdoutReview(t *testing.T) {
	withDiagnosticsSeams(t, noProbe, time.Second)
	rec := `{"disk":"/dev/vda","filesystem":"xfs","hostname":"box","encryption":{"type":"luks-passphrase","passphrase":"pass-phrase-123"}}`
	logPath := filepath.Join(t.TempDir(), "progress.ndjson")
	if err := os.WriteFile(logPath, []byte(`{"type":"error","message":"luksOpen pass-phrase-123 failed","code":"luks_failed"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runDiag(t, rec, "--stdout", "--recipe", "-", "--progress-log", logPath, "--probe")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if strings.Contains(out, "pass-phrase-123") {
		t.Errorf("bundle holds the passphrase:\n%s", out)
	}
	var b diagnostics.Bundle
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatal(err)
	}
	if b.Format != diagnostics.Format || b.Recipe == nil || b.Progress == nil || b.Probe == nil {
		t.Fatalf("bundle sections missing: %+v", b.Manifest.Sections)
	}
	if b.Recipe.Hostname != "host-1" || b.Progress.Outcome.Code != "luks_failed" || b.Versions.Fisherman != version {
		t.Errorf("recipe %+v outcome %+v versions %+v", b.Recipe, b.Progress.Outcome, b.Versions)
	}
}

func TestDiagnosticsWritesFile(t *testing.T) {
	withDiagnosticsSeams(t, noProbe, time.Second)
	dir := t.TempDir()
	code, out, errOut := runDiag(t, "", "--output-dir", dir, "--include-identifiers")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	path := strings.TrimSpace(out)
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || fi.Mode().Perm() != 0o600 {
		t.Errorf("path %q mode %v", path, fi.Mode())
	}
	if !strings.Contains(errOut, "review the bundle") {
		t.Errorf("stderr %q has no review reminder", errOut)
	}
	b, _ := os.ReadFile(path)
	var bundle diagnostics.Bundle
	if err := json.Unmarshal(b, &bundle); err != nil || bundle.Manifest.IdentifiersMasked {
		t.Errorf("bundle: %v, masked %v", err, bundle.Manifest.IdentifiersMasked)
	}
}

// TestDiagnosticsUnreadableInputs: an input that cannot be read is a
// section status, not a failure of the export.
func TestDiagnosticsUnreadableInputs(t *testing.T) {
	withDiagnosticsSeams(t, noProbe, time.Second)
	bad := filepath.Join(t.TempDir(), "probe.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "absent")
	code, out, errOut := runDiag(t, "", "--stdout", "--recipe", missing, "--progress-log", missing, "--probe-report", bad)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var b diagnostics.Bundle
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"recipe", "progress", "probe"} {
		if b.Manifest.Sections[s] != diagnostics.StatusUnreadable {
			t.Errorf("%s: %q, want %q", s, b.Manifest.Sections[s], diagnostics.StatusUnreadable)
		}
	}
}

// TestDiagnosticsProbeTimeout: a probe that does not answer in time is a
// timed_out section; the export still finishes.
func TestDiagnosticsProbeTimeout(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	withDiagnosticsSeams(t, func() (*probe.Result, error) { <-block; return nil, nil }, 20*time.Millisecond)
	code, out, _ := runDiag(t, "", "--stdout", "--probe")
	if code != 0 || !strings.Contains(out, `"probe": "timed_out"`) {
		t.Errorf("exit %d, out %s", code, out)
	}
}

// TestDiagnosticsOutputDirFailure: an unusable output directory fails the
// export with exit 1 and writes nothing.
func TestDiagnosticsOutputDirFailure(t *testing.T) {
	withDiagnosticsSeams(t, noProbe, time.Second)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runDiag(t, "", "--output-dir", link); code != exitFailure || out != "" {
		t.Errorf("exit %d, out %q; want %d and nothing", code, out, exitFailure)
	}
	if ents, _ := os.ReadDir(real); len(ents) != 0 {
		t.Errorf("wrote through the symlink: %v", ents)
	}
}

func TestReadTailKeepsEnd(t *testing.T) {
	data := strings.Repeat("a", 100) + "END"
	got, truncated, err := readTail(strings.NewReader(data), 10)
	if err != nil || !truncated || string(got) != data[len(data)-10:] {
		t.Errorf("readTail = %q, %v, %v", got, truncated, err)
	}
	got, truncated, _ = readTail(strings.NewReader("short"), 10)
	if truncated || string(got) != "short" {
		t.Errorf("readTail short = %q, %v", got, truncated)
	}
}

func TestReadProgressLogLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.ndjson")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(diagnostics.MaxProgressBytes + 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("TAIL"), diagnostics.MaxProgressBytes+6); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, truncated, err := readProgressLog(path, nil)
	if err != nil || !truncated || len(got) != diagnostics.MaxProgressBytes || !bytes.HasSuffix(got, []byte("TAIL")) {
		t.Errorf("len %d truncated %v err %v", len(got), truncated, err)
	}
}
