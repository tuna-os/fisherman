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
// The install does not call it directly: the pool is registered with
// post.Cleanup (SetZFSPool), which exports it after the unmounts on every
// exit path.
func ExportZFSPool(poolName string) error {
	progress.Info(fmt.Sprintf("Exporting ZFS pool %s", poolName))
	return runner.Run("zpool", "export", poolName)
}

// SetZFSCachefile writes the pool's zpool.cache into etcDir/zfs, so the
// installed system's initramfs can import the pool from the cache instead
// of scanning.
//
// etcDir is the /etc the BOOTED system sees (deploy.Deployment.Etc). ZFS
// installs use the composefs backend, whose /etc is
// state/deploy/<verity>/etc: <target>/etc is not it (#413).
//
// The cache is written to a temporary file on the host, copied into the
// target, and the pool's cachefile property is then set to "none". Pointing
// the property at a path inside the pool itself does not survive the
// export: `zpool export` removes the pool from its cache file, and by then
// that path names the unmounted mountpoint on the host.
func SetZFSCachefile(poolName, etcDir string) error {
	cacheDir := filepath.Join(etcDir, "zfs")
	cacheFile := filepath.Join(cacheDir, "zpool.cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", cacheDir, err)
	}
	tmp, err := os.CreateTemp("", "fisherman-zpool-*.cache")
	if err != nil {
		return fmt.Errorf("temporary cache file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	progress.Info(fmt.Sprintf("Writing zpool.cache to %s", cacheFile))
	if err := runner.Run("zpool", "set", "cachefile="+tmpPath, poolName); err != nil {
		return fmt.Errorf("zpool set cachefile: %w", err)
	}
	// Whatever happens next, the pool must not keep pointing at a file
	// that is about to be deleted.
	defer func() { _ = runner.Run("zpool", "set", "cachefile=none", poolName) }()

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("reading the pool cache: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("zpool wrote an empty cache file for %s", poolName)
	}
	if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", cacheFile, err)
	}
	return nil
}

// WriteHostID writes a machine host ID to etcDir/hostid, where etcDir is
// the /etc the booted system sees (deploy.Deployment.Etc; see
// SetZFSCachefile).
//
// ZFS wants a stable host ID (zgenhostid). The pool itself records the ID
// of the host that last had it imported, which is the installer host; that
// does not block the installed system, because the install exports the pool
// and an exported pool imports on any host without force.
//
// We generate 4 random bytes and write them in the native byte order format
// that zgenhostid uses.
func WriteHostID(etcDir string) error {
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
