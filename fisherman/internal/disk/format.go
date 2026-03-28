package disk

import (
	"fmt"
	"os"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

// FormatEFI formats a partition as FAT32 for use as the EFI System Partition.
func FormatEFI(part string) error {
	return runner.Run("mkfs.fat", "-F32", "-n", "EFI-SYSTEM", part)
}

// FormatRoot formats a device as the root filesystem (xfs or btrfs).
func FormatRoot(dev, filesystem string) error {
	switch filesystem {
	case "xfs":
		return runner.Run("mkfs.xfs", "-f", "-L", "root", dev)
	case "btrfs":
		return runner.Run("mkfs.btrfs", "-f", "-L", "root", dev)
	default:
		return fmt.Errorf("unsupported filesystem: %q", filesystem)
	}
}

// Mount mounts dev at target. opts is an optional comma-separated option string
// passed to -o; pass "" for no options.
func Mount(dev, target, opts string) error {
	args := []string{}
	if opts != "" {
		args = append(args, "-o", opts)
	}
	args = append(args, dev, target)
	return runner.Run("mount", args...)
}

// MountTmpfs mounts a tmpfs of the given size (e.g. "4G") at path, creating
// the directory if needed.
func MountTmpfs(path, size string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	return runner.Run("mount", "-t", "tmpfs", "-o", "size="+size, "tmpfs", path)
}

// BindMount bind-mounts src onto dst, creating dst if needed.
func BindMount(src, dst string) error {
	if err := os.MkdirAll(dst, 0o1777); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	return runner.Run("mount", "--bind", src, dst)
}

// Umount unmounts path (simple, non-recursive).
func Umount(path string) error {
	return runner.Run("umount", path)
}

// UmountRecursive unmounts path and all mounts beneath it.
func UmountRecursive(path string) error {
	return runner.Run("umount", "-R", path)
}

// SetupBtrfsSubvolumes creates @, @home, and @snapshots subvolumes on a
// freshly-formatted btrfs device, then remounts the target using subvol=@
// with zstd compression.
func SetupBtrfsSubvolumes(dev, target string) error {
	// Bare mount to create subvolumes.
	if err := Mount(dev, target, ""); err != nil {
		return fmt.Errorf("initial btrfs mount: %w", err)
	}

	for _, sv := range []string{"@", "@home", "@snapshots"} {
		progress.Info(fmt.Sprintf("Creating btrfs subvolume %s", sv))
		if err := runner.Run("btrfs", "subvolume", "create", target+"/"+sv); err != nil {
			_ = Umount(target)
			return fmt.Errorf("create subvolume %s: %w", sv, err)
		}
	}

	if err := Umount(target); err != nil {
		return fmt.Errorf("umount after subvolume creation: %w", err)
	}

	// Remount with the @ subvolume and transparent compression.
	return Mount(dev, target, "subvol=@,compress=zstd:1")
}

// MountEFI creates the /boot/efi directory tree under rootMount and mounts
// efiPart there. This is required before running `bootc install to-filesystem`
// so bootc can find and install the bootloader.
func MountEFI(rootMount, efiPart string) error {
efiDir := rootMount + "/boot/efi"
if err := os.MkdirAll(efiDir, 0o755); err != nil {
return fmt.Errorf("mkdir %s: %w", efiDir, err)
}
return runner.Run("mount", efiPart, efiDir)
}
