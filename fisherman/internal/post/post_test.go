package post_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
)

// setupRecorder, recorder, execCall are defined in cleanup_test.go (same package)

// TestWriteHostname_ComposeFsNative verifies that when no /ostree/ directory
// exists under the target (composefs-native deployment), hostname is written
// directly to $TARGET/etc/hostname.
func TestWriteHostname_ComposeFsNative(t *testing.T) {
	// No runner interception needed — this path uses os.WriteFile, not exec.
	target := t.TempDir()
	// Deliberately do NOT create target/ostree/ — that's what makes it composefs-native.

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

// TestWriteHostname_ComposeFsNative_CreatesEtcDir verifies that /etc is created
// if it doesn't already exist (composefs-native path).
func TestWriteHostname_ComposeFsNative_CreatesEtcDir(t *testing.T) {
	target := t.TempDir()
	// No ostree dir, no etc dir — both should be created.
	if err := post.WriteHostname(target, "tunaos"); err != nil {
		t.Fatalf("WriteHostname: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "etc", "hostname")); err != nil {
		t.Errorf("hostname file not created: %v", err)
	}
}

// TestWriteHostname_OstreeBackend verifies that when /ostree/ exists under the
// target (ostree-based deployment), hostname is written to the path returned by
// DeploymentDirFn, not to $TARGET/etc/hostname directly.
func TestWriteHostname_OstreeBackend(t *testing.T) {
	target := t.TempDir()

	// Create the ostree directory to trigger the ostree code path.
	if err := os.MkdirAll(filepath.Join(target, "ostree"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a fake deploy dir that DeploymentDirFn will return.
	fakeDeployDir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(fakeDeployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Stub DeploymentDirFn to return our fake deploy dir.
	post.DeploymentDirFn = func(sysroot string) (string, error) {
		return fakeDeployDir, nil
	}
	t.Cleanup(func() { post.DeploymentDirFn = post.DefaultDeploymentDir })

	// Also need runner for the ostree exec — but DeploymentDirFn bypasses exec.
	rec := setupRecorder(t)
	_ = rec // no exec calls expected in this path

	if err := post.WriteHostname(target, "tunahost"); err != nil {
		t.Fatalf("WriteHostname: %v", err)
	}

	// Hostname must be in the deploy dir's etc/, NOT in target/etc/.
	hostnameInDeploy := filepath.Join(fakeDeployDir, "etc", "hostname")
	data, err := os.ReadFile(hostnameInDeploy)
	if err != nil {
		t.Fatalf("reading hostname from deploy dir: %v", err)
	}
	if string(data) != "tunahost\n" {
		t.Errorf("hostname = %q, want %q", string(data), "tunahost\n")
	}

	// The direct target/etc/hostname must NOT exist.
	if _, err := os.Stat(filepath.Join(target, "etc", "hostname")); err == nil {
		t.Error("hostname should NOT be written to target/etc/hostname for ostree deployments")
	}
}
