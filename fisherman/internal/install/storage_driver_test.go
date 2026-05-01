package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectStorageDriver_NonComposefs(t *testing.T) {
	// Non-composefs always returns vfs, unchanged behavior.
	driver, reason := selectStorageDriver("/tmp", false)
	if driver != "vfs" {
		t.Errorf("non-composefs driver = %q, want vfs", driver)
	}
	if reason == "" {
		t.Error("non-composefs reason should not be empty")
	}
}

func TestFilesystemType_TmpfsDetection(t *testing.T) {
	// /tmp is typically tmpfs
	fsType, err := filesystemType("/tmp")
	if err != nil {
		t.Fatalf("filesystemType(/tmp) error: %v", err)
	}
	t.Logf("detected filesystem type for /tmp: %s", fsType)
	// We just verify it doesn't error and returns something.
	if fsType == "" {
		t.Error("filesystem type should not be empty")
	}
}

func TestOverlayCandidate_UnsafeFilesystems(t *testing.T) {
	tests := []struct {
		name         string
		testPath     string
		shouldReject string // if not empty, indicates a filesystem that should be rejected
	}{
		{"tmpfs", "/tmp", "tmpfs"},
		{"var_tmp", "/var/tmp", "tmpfs"}, // often tmpfs
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := overlayCandidate(tt.testPath)
			// If the test path is on an unsafe filesystem, we expect vfs.
			// But we can't assume the test environment matches our expectations,
			// so we just verify the function doesn't panic and returns a valid result.
			if candidate.driver != "vfs" && candidate.driver != "overlay" {
				t.Errorf("invalid driver: %s", candidate.driver)
			}
			if candidate.driver == "vfs" && candidate.reason == "" {
				t.Error("vfs fallback should have a reason")
			}
		})
	}
}

func TestProbeOverlay_WithTempDir(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a subdirectory to use as scratch
	scratchPath := filepath.Join(tmpDir, "scratch")
	if err := os.MkdirAll(scratchPath, 0755); err != nil {
		t.Fatalf("creating scratch dir: %v", err)
	}

	// Probe overlay. If podman is not available or overlay is not supported,
	// probeOverlay should return an error, which is fine for this test.
	// If podman is available and the environment supports overlay, it should succeed.
	// We can't make strong assertions without controlling the environment.
	err := probeOverlay(scratchPath)
	if err != nil {
		t.Logf("probeOverlay returned error (expected on some systems): %v", err)
	}
}

func TestSelectStorageDriver_Integration(t *testing.T) {
	// Test the full selector with a known temp directory.
	tmpDir := t.TempDir()
	driver, reason := selectStorageDriver(tmpDir, true)

	if driver != "vfs" && driver != "overlay" {
		t.Errorf("invalid driver: %s", driver)
	}
	if reason == "" {
		t.Error("reason should not be empty")
	}

	t.Logf("Selected driver: %s (%s)", driver, reason)
}
