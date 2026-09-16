package install

// Integration tests for storage driver filesystem detection.
// These tests create real loopback devices and require root.
// They are skipped automatically when not running as root or when
// the required mkfs tools or kernel support are not available.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// requireRoot skips the test if not running as root.
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("requires root: run as root or with sudo go test")
	}
}

// requireTool skips the test if the named binary is not in PATH.
func requireTool(t *testing.T, tool string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("requires %s: not found in PATH", tool)
	}
}

// withLoopbackFS creates a loopback device formatted with the given filesystem
// type, mounts it, and returns the mount point. Cleanup is registered with
// t.Cleanup. The test is skipped if the kernel does not support the filesystem.
func withLoopbackFS(t *testing.T, fsType string) string {
	t.Helper()
	requireRoot(t)
	requireTool(t, "mkfs."+fsType)
	requireTool(t, "mount")

	f, err := os.CreateTemp("", "fisherman-loopback-*.img")
	if err != nil {
		t.Fatalf("creating loopback image: %v", err)
	}
	imgPath := f.Name()
	f.Close()

	// Size requirements vary: XFS needs ≥300 MiB, BTRFS ≥114 MiB, ext4 is fine smaller.
	var sizeBytes int64
	switch fsType {
	case "xfs":
		sizeBytes = 350 * 1024 * 1024
	default:
		sizeBytes = 150 * 1024 * 1024
	}
	if err := os.Truncate(imgPath, sizeBytes); err != nil {
		os.Remove(imgPath)
		t.Fatalf("truncating loopback image: %v", err)
	}

	var mkfsArgs []string
	switch fsType {
	case "ext4":
		mkfsArgs = []string{"-F", imgPath}
	case "xfs", "btrfs":
		mkfsArgs = []string{"-f", imgPath}
	default:
		os.Remove(imgPath)
		t.Fatalf("unsupported fsType %q", fsType)
	}

	if out, err := exec.Command("mkfs."+fsType, mkfsArgs...).CombinedOutput(); err != nil {
		os.Remove(imgPath)
		t.Fatalf("mkfs.%s: %v\n%s", fsType, err, out)
	}

	mountDir, err := os.MkdirTemp("", "fisherman-mount-*")
	if err != nil {
		os.Remove(imgPath)
		t.Fatalf("creating mount dir: %v", err)
	}

	if out, err := exec.Command("mount", "-o", "loop", imgPath, mountDir).CombinedOutput(); err != nil {
		os.RemoveAll(mountDir)
		os.Remove(imgPath)
		outStr := string(out)
		if strings.Contains(outStr, "unknown filesystem type") {
			t.Skipf("kernel does not support %s filesystem", fsType)
		}
		t.Fatalf("mount: %v\n%s", err, outStr)
	}

	t.Cleanup(func() {
		exec.Command("umount", mountDir).Run() //nolint:errcheck
		os.RemoveAll(mountDir)
		os.Remove(imgPath)
	})

	return mountDir
}

func TestFilesystemType_Loopback_Ext4(t *testing.T) {
	mountDir := withLoopbackFS(t, "ext4")
	fsType, err := filesystemType(mountDir)
	if err != nil {
		t.Fatalf("filesystemType: %v", err)
	}
	if fsType != "ext4" {
		t.Errorf("filesystemType = %q, want ext4", fsType)
	}
}

func TestFilesystemType_Loopback_XFS(t *testing.T) {
	mountDir := withLoopbackFS(t, "xfs")
	fsType, err := filesystemType(mountDir)
	if err != nil {
		t.Fatalf("filesystemType: %v", err)
	}
	if fsType != "xfs" {
		t.Errorf("filesystemType = %q, want xfs", fsType)
	}
}

func TestFilesystemType_Loopback_BTRFS(t *testing.T) {
	mountDir := withLoopbackFS(t, "btrfs")
	fsType, err := filesystemType(mountDir)
	if err != nil {
		t.Fatalf("filesystemType: %v", err)
	}
	if fsType != "btrfs" {
		t.Errorf("filesystemType = %q, want btrfs", fsType)
	}
}

func TestOverlayCandidate_Loopback_Ext4(t *testing.T) {
	mountDir := withLoopbackFS(t, "ext4")
	candidate := overlayCandidate(mountDir)
	if candidate.driver != "overlay" {
		t.Errorf("overlayCandidate on ext4 = %q (%s), want overlay", candidate.driver, candidate.reason)
	}
}

func TestOverlayCandidate_Loopback_XFS(t *testing.T) {
	mountDir := withLoopbackFS(t, "xfs")
	candidate := overlayCandidate(mountDir)
	if candidate.driver != "overlay" {
		t.Errorf("overlayCandidate on xfs = %q (%s), want overlay", candidate.driver, candidate.reason)
	}
}

func TestOverlayCandidate_Loopback_BTRFS(t *testing.T) {
	mountDir := withLoopbackFS(t, "btrfs")
	candidate := overlayCandidate(mountDir)
	if candidate.driver != "overlay" {
		t.Errorf("overlayCandidate on btrfs = %q (%s), want overlay", candidate.driver, candidate.reason)
	}
}

func TestSelectStorageDriver_Loopback_Ext4(t *testing.T) {
	mountDir := withLoopbackFS(t, "ext4")
	driver, reason := selectStorageDriver(mountDir)
	t.Logf("ext4 driver=%s reason=%s", driver, reason)
	// overlay is expected; vfs is acceptable if podman probe fails in this environment.
	if driver != "overlay" && driver != "vfs" {
		t.Errorf("unexpected driver %q", driver)
	}
	if driver == "overlay" {
		t.Log("overlay selected on ext4 ✓")
	} else {
		t.Logf("fell back to vfs: %s", reason)
	}
}

func TestSelectStorageDriver_Loopback_XFS(t *testing.T) {
	mountDir := withLoopbackFS(t, "xfs")
	driver, reason := selectStorageDriver(mountDir)
	t.Logf("xfs driver=%s reason=%s", driver, reason)
	if driver != "overlay" && driver != "vfs" {
		t.Errorf("unexpected driver %q", driver)
	}
	if driver == "overlay" {
		t.Log("overlay selected on xfs ✓")
	} else {
		t.Logf("fell back to vfs: %s", reason)
	}
}

func TestSelectStorageDriver_Loopback_BTRFS(t *testing.T) {
	mountDir := withLoopbackFS(t, "btrfs")
	driver, reason := selectStorageDriver(mountDir)
	t.Logf("btrfs driver=%s reason=%s", driver, reason)
	if driver != "overlay" && driver != "vfs" {
		t.Errorf("unexpected driver %q", driver)
	}
	if driver == "overlay" {
		t.Log("overlay selected on btrfs ✓")
	} else {
		t.Logf("fell back to vfs on btrfs (probe failed): %s", reason)
	}
}
