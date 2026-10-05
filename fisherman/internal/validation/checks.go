package validation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// checkToolAvailable verifies that a CLI tool exists in PATH.
func checkToolAvailable(tool string) error {
	_, err := exec.LookPath(tool)
	if err != nil {
		return fmt.Errorf("%s: not found in PATH", tool)
	}
	return nil
}

// checkImageCached verifies that the OS image file exists and is readable.
func checkImageCached(imagePath string) error {
	if imagePath == "" {
		return fmt.Errorf("image path not specified")
	}

	info, err := os.Stat(imagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("image not cached: %s", imagePath)
		}
		return fmt.Errorf("cannot access image: %v", err)
	}

	if info.IsDir() {
		return fmt.Errorf("image path is a directory, not a file: %s", imagePath)
	}

	// Verify we can read it
	f, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("image not readable: %v", err)
	}
	defer f.Close()

	return nil
}

// CheckFilesystemAvailable verifies that a mount point or device is accessible.
func CheckFilesystemAvailable(path string) error {
	if path == "" {
		return fmt.Errorf("path not specified")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %v", err)
	}

	// For mount points, check if the parent directory exists
	parent := filepath.Dir(abs)
	_, err = os.Stat(parent)
	if err != nil {
		return fmt.Errorf("parent directory not accessible: %v", err)
	}

	return nil
}

// CheckPermissions verifies that the current user can perform privileged operations.
// This is a best-effort check; the actual install will fail with proper privileges.
func CheckPermissions() error {
	// If we're root, we have permission
	if os.Geteuid() == 0 {
		return nil
	}
	// If not root, installation will fail — but we let it fail with a better message during install
	return fmt.Errorf("installation requires root privileges")
}
