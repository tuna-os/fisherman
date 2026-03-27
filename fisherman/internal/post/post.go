package post

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Cleanup tracks mounted filesystems and an open LUKS device so they can be
// torn down in the correct order on both success and error paths.
type Cleanup struct {
	// mounts holds mount points in the order they were added; cleanup unmounts
	// them in reverse order.
	mounts []string
	// luksMapper is the dm-crypt mapper name to close after unmounting, or ""
	// if no LUKS device was opened.
	luksMapper string
	// done ensures Run is idempotent — safe to call from both a fatal handler
	// and the normal exit path.
	done bool
}

// AddMount registers a mount point to be unmounted during cleanup.
func (c *Cleanup) AddMount(path string) {
	c.mounts = append(c.mounts, path)
}

// SetLUKS registers a dm-crypt mapper name to be closed during cleanup.
func (c *Cleanup) SetLUKS(mapperName string) {
	c.luksMapper = mapperName
}

// Run unmounts all registered mount points in reverse order, then closes any
// open LUKS device. It is idempotent — subsequent calls are no-ops.
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

// WriteHostname writes hostname to <target>/etc/hostname, creating /etc if needed.
func WriteHostname(target, hostname string) error {
	etcDir := filepath.Join(target, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", etcDir, err)
	}
	hostnameFile := filepath.Join(etcDir, "hostname")
	if err := os.WriteFile(hostnameFile, []byte(hostname+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", hostnameFile, err)
	}
	return nil
}
