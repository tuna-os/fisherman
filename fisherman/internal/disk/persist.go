package disk

import (
	"fmt"
	"strings"

	"github.com/tuna-os/fisherman/internal/runner"
)

// FSType returns the filesystem type blkid reads from dev ("xfs", "ext4",
// "vfat", "swap", "crypto_LUKS", ...), or "" when blkid finds none. It asks
// for a low-level probe first, which reads the device and not blkid's cache:
// the answer is used right after mkfs, and for an fstab line.
func FSType(dev string) string {
	for _, args := range [][]string{
		{"-p", "-s", "TYPE", "-o", "value", dev},
		{"-s", "TYPE", "-o", "value", dev},
	} {
		out, err := runner.Output("blkid", args...)
		if t := strings.TrimSpace(string(out)); err == nil && t != "" {
			return t
		}
	}
	return ""
}

// containerTypes are blkid types that are not a filesystem to mount: the
// data is inside another layer fisherman does not open.
var containerTypes = map[string]string{
	"crypto_LUKS":       "an encrypted (LUKS) volume",
	"LVM2_member":       "an LVM physical volume",
	"linux_raid_member": "a RAID member",
	"zfs_member":        "a ZFS pool member",
	"bcache":            "a bcache device",
	"swap":              "swap space",
}

// posixTypes are the filesystems that can hold /var: owners, modes and
// SELinux labels must survive.
var posixTypes = map[string]bool{"xfs": true, "ext4": true, "ext3": true, "ext2": true, "btrfs": true, "f2fs": true}

// CheckKeptFilesystem says whether a device that is mounted as it is (not
// formatted) holds a filesystem the install can mount and the installed
// system can mount again from fstab. fstype is FSType(dev). posix asks for a
// filesystem that can hold /var. A nil error means it is usable.
func CheckKeptFilesystem(dev, fstype string, posix bool) error {
	if fstype == "" {
		return fmt.Errorf("%s has no filesystem blkid can identify, so it cannot be mounted without formatting", dev)
	}
	if what, ok := containerTypes[fstype]; ok {
		return fmt.Errorf("%s is %s (%s), not a filesystem that can be mounted as it is", dev, what, fstype)
	}
	if posix && !posixTypes[fstype] {
		return fmt.Errorf("%s holds %s, which cannot hold /var (use xfs, ext4 or btrfs)", dev, fstype)
	}
	return nil
}

// PrepareVarDisk formats a whole disk as XFS (label "var") for use as /var.
//
// The disk is released first, the same way the system disk is before sfdisk
// (unmountAll): filesystems on it or on its partitions are unmounted, swap is
// turned off, and LVM and device-mapper holders are removed. Without that,
// mkfs.xfs fails on a disk a desktop session auto-mounted, and it fails
// after the system disk is already wiped (#406). Old signatures, a partition
// table included, are wiped so blkid reports one filesystem and no stale
// partition is left to mount by mistake.
func PrepareVarDisk(dev string) error {
	if err := unmountAll(dev); err != nil {
		return fmt.Errorf("releasing %s: %w", dev, err)
	}
	if err := runner.Run("wipefs", "-a", dev); err != nil {
		return fmt.Errorf("wipefs %s: %w", dev, err)
	}
	// Drop partition nodes of a table that is gone now. Loop devices and a
	// disk with no table refuse or ignore the ioctl; neither matters here.
	_ = runner.Run("blockdev", "--rereadpt", dev)
	if err := FormatVar(dev); err != nil {
		return err
	}
	_ = runner.Run("udevadm", "settle")
	return nil
}
