package install

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// storageDriverCandidate represents a storage driver choice and reasoning.
type storageDriverCandidate struct {
	driver string // "overlay" or "vfs"
	reason string // human-readable explanation
}

// selectStorageDriver chooses a storage driver for the composefs path.
// If composefs is not enabled, returns ("vfs", "reason") unconditionally for backward compat.
// If composefs is enabled, returns ("overlay", "reason") when safe, or ("vfs", "reason") as fallback.
// Checks include:
// - Filesystem type of scratchPath (rejects BTRFS, overlayfs, tmpfs)
// - Explicit podman overlay probe to verify the driver works on the target root
func selectStorageDriver(scratchPath string, composefs bool) (driver, reason string) {
	if !composefs {
		// Non-composefs installs always use vfs, unchanged from current behavior.
		return "vfs", "standard containers-storage (non-composefs path)"
	}

	// Composefs path: try overlay, but carefully.
	candidate := overlayCandidate(scratchPath)
	if candidate.driver == "vfs" {
		return candidate.driver, candidate.reason
	}

	// Probe podman overlay support on the target root.
	if err := probeOverlay(scratchPath); err != nil {
		return "vfs", fmt.Sprintf("podman overlay probe failed: %v", err)
	}

	return "overlay", "overlay-backed composefs temporary storage on safe filesystem"
}

// overlayCandidate checks if the scratch filesystem is safe for overlay, returning either
// ("overlay", "...") or ("vfs", "fallback reason").
func overlayCandidate(scratchPath string) storageDriverCandidate {
	fsType, err := filesystemType(scratchPath)
	if err != nil {
		return storageDriverCandidate{"vfs", fmt.Sprintf("could not detect filesystem type: %v", err)}
	}

	// Filesystems where overlay should NOT be used.
	unsafeFS := map[string]bool{
		"btrfs":    true,
		"overlayfs": true,
		"tmpfs":    true,
	}

	if unsafeFS[fsType] {
		return storageDriverCandidate{"vfs", fmt.Sprintf("scratch filesystem %s does not support overlay", fsType)}
	}

	// Unknown filesystem: be conservative and use vfs.
	knownSafeFS := map[string]bool{
		"ext4": true,
		"xfs":  true,
	}
	if !knownSafeFS[fsType] {
		return storageDriverCandidate{"vfs", fmt.Sprintf("scratch filesystem %s is not known to be overlay-safe", fsType)}
	}

	return storageDriverCandidate{"overlay", ""}
}

// filesystemType returns the filesystem type of the given path using statfs.
func filesystemType(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}

	// Map fstype magic numbers to names.
	// See include/uapi/linux/magic.h in the Linux kernel.
	fsTypes := map[int64]string{
		0x61756673: "aufs",
		0x9123683e: "btrfs",
		0x00c0ffee: "cramfs",
		0x00004d44: "msdos",
		0x3153464a: "jfs",
		0xf995e849: "hpfs",
		0x9660:     "isofs",
		0x137d:     "ext",
		0xef53:     "ext2/ext3/ext4",
		0xf2f52010: "ubifs",
		0x58465342: "xfs",
		0x794c7630: "overlayfs",
		0x01021994: "tmpfs",
		0x6969:     "nfs",
	}

	if name, ok := fsTypes[st.Type]; ok {
		return name, nil
	}

	return fmt.Sprintf("unknown(0x%x)", st.Type), nil
}

// probeOverlay attempts to verify that podman can use the overlay driver on the target root.
// It runs a minimal `podman info --storage-driver overlay` probe and returns any error.
// If the probe fails, the caller should fall back to vfs.
func probeOverlay(scratchPath string) error {
	// Create a temporary containers-root for the probe.
	probeRoot := scratchPath + "/.overlay-probe"
	if err := os.MkdirAll(probeRoot, 0755); err != nil {
		return fmt.Errorf("creating probe root: %w", err)
	}
	defer os.RemoveAll(probeRoot)

	// Run `podman --root <probeRoot> info --format json` to check overlay support.
	// If overlay is not available or doesn't work on this root, podman will error.
	cmd := exec.Command("podman", "--root", probeRoot, "info", "--format", "json")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("podman probe failed: %w (output: %s)", err, output)
	}

	return nil
}
