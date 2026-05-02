package post_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
)

func TestCopySnaps_NoSnapdData(t *testing.T) {
	mock := setupMockExec(t)

	// du returns 0 — no snapd data present in live environment.
	mock.responses["du -sb"] = struct {
		out []byte
		err error
	}{out: []byte("0\t/var/lib/snapd\n")}

	target := t.TempDir()
	if err := post.CopySnaps(target, ""); err != nil {
		t.Fatalf("CopySnaps: %v", err)
	}

	// No tar calls should have been made.
	for _, call := range mock.calls {
		if call.name == "tar" {
			t.Errorf("unexpected tar call when no snapd data present: %v", call.args)
		}
	}
}

func TestCopySnaps_CopiesDataToDefaultPath(t *testing.T) {
	mock := setupMockExec(t)

	// du returns non-zero — snapd data present.
	mock.responses["du -sb"] = struct {
		out []byte
		err error
	}{out: []byte("102400\t/var/lib/snapd\n")}
	// tar succeeds (Start/Wait return nil via mock).
	mock.responses["tar"] = struct {
		out []byte
		err error
	}{}

	target := t.TempDir()
	if err := post.CopySnaps(target, ""); err != nil {
		t.Fatalf("CopySnaps: %v", err)
	}

	// For composefs-native (no /ostree dir), the dst should be:
	//   <target>/state/os/default/var/lib/snapd
	want := filepath.Join(target, "state", "os", "default", "var", "lib", "snapd")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected dst dir %s to exist: %v", want, err)
	}

	// Verify at least one tar call was made.
	hasTar := false
	for _, call := range mock.calls {
		if call.name == "tar" {
			hasTar = true
			break
		}
	}
	if !hasTar {
		t.Error("expected tar to be called when snapd data is present")
	}
}

func TestCopySnaps_SnapVarPathOverride(t *testing.T) {
	mock := setupMockExec(t)

	mock.responses["du -sb"] = struct {
		out []byte
		err error
	}{out: []byte("1024\t/var/lib/snapd\n")}
	mock.responses["tar"] = struct {
		out []byte
		err error
	}{}

	target := t.TempDir()
	snapVarPath := "state/os/default/var"
	if err := post.CopySnaps(target, snapVarPath); err != nil {
		t.Fatalf("CopySnaps: %v", err)
	}

	want := filepath.Join(target, snapVarPath, "lib", "snapd")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected dst dir at override path %s: %v", want, err)
	}

	// Ensure the auto-detected default path was NOT created.
	defaultPath := filepath.Join(target, "ostree", "deploy", "default", "var", "lib", "snapd")
	if _, err := os.Stat(defaultPath); err == nil {
		t.Errorf("default path %s was created — snapVarPath override was ignored", defaultPath)
	}
}

func TestCopySnaps_EmitsSnapNameSubsteps(t *testing.T) {
	mock := setupMockExec(t)

	// Point SnapDataPath at a temp dir with fake snap files.
	fakeSnapData := t.TempDir()
	snapsDir := filepath.Join(fakeSnapData, "snaps")
	if err := os.MkdirAll(snapsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"firefox_100.snap", "libreoffice_42.snap", "core24_1.snap"} {
		if err := os.WriteFile(filepath.Join(snapsDir, f), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	oldPath := post.SnapDataPath
	post.SnapDataPath = fakeSnapData
	t.Cleanup(func() { post.SnapDataPath = oldPath })

	mock.responses["du -sb"] = struct {
		out []byte
		err error
	}{out: []byte("204800\t" + fakeSnapData + "\n")}
	mock.responses["tar"] = struct {
		out []byte
		err error
	}{}

	var substeps []string
	origSubstep := progress.SubstepFn
	progress.SubstepFn = func(msg string) { substeps = append(substeps, msg) }
	defer func() { progress.SubstepFn = origSubstep }()

	target := t.TempDir()
	if err := post.CopySnaps(target, ""); err != nil {
		t.Fatalf("CopySnaps: %v", err)
	}

	// All three snap names must appear in substep messages.
	for _, name := range []string{"firefox", "libreoffice", "core24"} {
		found := false
		for _, s := range substeps {
			if strings.Contains(s, name) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no substep contained snap name %q; substeps: %v", name, substeps)
		}
	}
}

func TestSnapNames(t *testing.T) {
	dir := t.TempDir()

	files := []string{
		"firefox_100.snap",
		"firefox_99.snap",     // older revision — same name, should deduplicate
		"libreoffice_42.snap",
		"core24_1.snap",
		"notasnap.txt",        // non-.snap file — should be ignored
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	oldPath := post.SnapDataPath
	post.SnapDataPath = filepath.Dir(dir) // snapNames is called with SnapDataPath/snaps
	// Rename dir to "snaps" so the path resolves correctly.
	snapsDir := filepath.Join(filepath.Dir(dir), "snaps")
	if err := os.Rename(dir, snapsDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { post.SnapDataPath = oldPath })

	mock := setupMockExec(t)
	mock.responses["du -sb"] = struct {
		out []byte
		err error
	}{out: []byte("0\t" + post.SnapDataPath + "\n")} // 0 = skip copy, but we test names separately

	// Call snapNames indirectly via the substep output.
	var substeps []string
	origSubstep := progress.SubstepFn
	progress.SubstepFn = func(msg string) { substeps = append(substeps, msg) }
	defer func() { progress.SubstepFn = origSubstep }()

	// With du=0 CopySnaps exits early, so test snapNames directly by giving it data.
	mock.responses["du -sb"] = struct {
		out []byte
		err error
	}{out: []byte("1024\t" + post.SnapDataPath + "\n")}
	mock.responses["tar"] = struct {
		out []byte
		err error
	}{}

	target := t.TempDir()
	if err := post.CopySnaps(target, ""); err != nil {
		t.Fatalf("CopySnaps: %v", err)
	}

	// "firefox" should appear exactly once (deduplicated from two revisions).
	firefoxCount := 0
	for _, s := range substeps {
		if strings.Contains(s, "firefox") {
			firefoxCount++
		}
	}
	if firefoxCount != 1 {
		t.Errorf("firefox should appear once in substeps (deduplication); got %d — substeps: %v", firefoxCount, substeps)
	}

	// "notasnap" must not appear.
	for _, s := range substeps {
		if strings.Contains(s, "notasnap") {
			t.Errorf("non-.snap file appeared in substeps: %q", s)
		}
	}
}
