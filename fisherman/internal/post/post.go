package post

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Cleanup tracks mounted filesystems and an open LUKS device so they can be
// torn down in the correct order on both success and error paths.
type Cleanup struct {
	mounts     []string
	luksMapper string
	done       bool
}

func (c *Cleanup) AddMount(path string) { c.mounts = append(c.mounts, path) }
func (c *Cleanup) SetLUKS(name string)  { c.luksMapper = name }

// Run unmounts all registered mount points in reverse order, then closes any
// open LUKS device. It is idempotent.
func (c *Cleanup) Run() {
	if c.done {
		return
	}
	c.done = true
	for i := len(c.mounts) - 1; i >= 0; i-- {
		mp := c.mounts[i]
		if err := runner.Run("umount", "-R", mp); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unmounting %s: %v\n", mp, err)
		}
	}
	if c.luksMapper != "" {
		if err := luks.Close(c.luksMapper); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing LUKS device %s: %v\n", c.luksMapper, err)
		}
	}
}

// deploymentDir returns the ostree deployment directory inside sysroot using
// `ostree admin --sysroot=<sysroot> --print-current-dir`.
func deploymentDir(sysroot string) (string, error) {
	out, err := exec.Command("ostree", "admin", "--sysroot="+sysroot, "--print-current-dir").Output()
	if err != nil {
		return "", fmt.Errorf("ostree admin --print-current-dir: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("ostree admin --print-current-dir returned empty path")
	}
	return path, nil
}

// WriteHostname writes /etc/hostname into the ostree deployment inside target.
// It uses `ostree admin --print-current-dir` to locate the deployment directory,
// which is necessary because bootc installs into an ostree deployment subtree,
// not directly into the sysroot root.
func WriteHostname(target, hostname string) error {
	deployDir, err := deploymentDir(target)
	if err != nil {
		return fmt.Errorf("finding deployment dir: %w", err)
	}
	etcDir := filepath.Join(deployDir, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", etcDir, err)
	}
	hostnameFile := filepath.Join(etcDir, "hostname")
	if err := os.WriteFile(hostnameFile, []byte(hostname+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", hostnameFile, err)
	}
	fmt.Fprintf(os.Stdout, "  wrote hostname %q to %s\n", hostname, hostnameFile)
	return nil
}
