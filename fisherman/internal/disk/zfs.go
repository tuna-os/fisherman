package disk

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

// PoolName returns the ZFS pool name from a recipe, defaulting to "rpool".
func PoolName(name string) string {
	if name == "" {
		return "rpool"
	}
	return name
}

// CreateZFSPool creates a new ZFS pool on rawPart, mounting its root dataset
// at altroot. The pool is created with sane defaults for a bootc system:
//   - ashift=12 (4K sectors, recommended for most SSDs/NVMe)
//   - compression=zstd
//   - acltype=posixacl (required for systemd)
//   - xattr=sa (store xattrs in SA for performance)
//   - dnodesize=auto (for large xattrs)
//   - relatime=on (reduce atime write overhead)
//
// The pool root dataset gets mountpoint=/ and is mounted at altroot.
func CreateZFSPool(poolName, rawPart, altroot string) error {
	progress.Info(fmt.Sprintf("Creating ZFS pool %s on %s", poolName, rawPart))
	return runner.Run("zpool", "create",
		"-R", altroot,
		"-O", "compression=zstd",
		"-O", "mountpoint=/",
		"-O", "acltype=posixacl",
		"-O", "xattr=sa",
		"-O", "dnodesize=auto",
		"-O", "relatime=on",
		"-o", "ashift=12",
		"-f",
		poolName, rawPart,
	)
}

// CreateZFSRootDataset creates the root dataset for a bootc composefs install.
// poolName/root is created with mountpoint=/ so it is mounted at altroot by
// the pool's -R flag set during CreateZFSPool.
func CreateZFSRootDataset(poolName string) error {
	rootDs := poolName + "/root"
	progress.Info(fmt.Sprintf("Creating ZFS dataset %s", rootDs))
	if err := runner.Run("zfs", "create",
		"-o", "mountpoint=/",
		rootDs,
	); err != nil {
		return fmt.Errorf("zfs create %s: %w", rootDs, err)
	}
	return nil
}

// MountZFSRoot mounts the pool root dataset at altroot. The pool must already
// be imported (CreateZFSPool imports it via -R altroot).
// This is a no-op if the dataset is already mounted.
func MountZFSRoot(poolName, altroot string) error {
	rootDs := poolName + "/root"
	progress.Info(fmt.Sprintf("Mounting ZFS dataset %s at %s", rootDs, altroot))
	if err := os.MkdirAll(altroot, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", altroot, err)
	}
	if err := runner.Run("zfs", "mount", rootDs); err != nil {
		if !strings.Contains(err.Error(), "already mounted") {
			return fmt.Errorf("zfs mount %s: %w", rootDs, err)
		}
	}
	return nil
}

// ExportZFSPool exports a pool so it can be cleanly imported at next boot.
func ExportZFSPool(poolName string) error {
	progress.Info(fmt.Sprintf("Exporting ZFS pool %s", poolName))
	return runner.Run("zpool", "export", poolName)
}

// SetZFSCachefile writes the pool's zpool.cache to the installed system so
// the initramfs can import the pool by cache instead of scanning.
func SetZFSCachefile(poolName, targetRoot string) error {
	cacheDir := filepath.Join(targetRoot, "etc", "zfs")
	cacheFile := filepath.Join(cacheDir, "zpool.cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", cacheDir, err)
	}
	progress.Info(fmt.Sprintf("Writing zpool.cache to %s", cacheFile))
	return runner.Run("zpool", "set", "cachefile="+cacheFile, poolName)
}

// WriteHostID writes a machine host ID to <targetRoot>/etc/hostid.
// ZFS requires a stable host ID (zgenhostid) so pools can be correctly
// associated with the host. We generate 4 random bytes and write them in
// the native byte order format that zgenhostid uses.
func WriteHostID(targetRoot string) error {
	etcDir := filepath.Join(targetRoot, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", etcDir, err)
	}
	hostidFile := filepath.Join(etcDir, "hostid")
	progress.Info(fmt.Sprintf("Generating ZFS host ID at %s", hostidFile))

	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("generating host ID: %w", err)
	}
	// Write as native-endian 32-bit integer (same format as zgenhostid).
	var buf [4]byte
	binary.NativeEndian.PutUint32(buf[:], binary.NativeEndian.Uint32(id[:]))
	if err := os.WriteFile(hostidFile, buf[:], 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", hostidFile, err)
	}
	return nil
}
