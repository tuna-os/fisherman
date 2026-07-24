package disk

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

// FormatEFI formats a partition as FAT32 for use as the EFI System Partition.
func FormatEFI(part string) error {
	return runner.Run("mkfs.fat", "-F32", "-n", "EFI-SYSTEM", part)
}

// FormatRoot formats a device as the root filesystem (xfs, ext4, or btrfs).
func FormatRoot(dev, filesystem string) error {
	switch filesystem {
	case "xfs":
		return runner.Run("mkfs.xfs", "-f", "-L", "root", dev)
	case "ext4":
		// -O verity is required for composefs (bootc's --composefs-backend uses
		// FS_IOC_ENABLE_VERITY on individual files; ext4 only supports this when
		// the verity feature is enabled at format time).
		return runner.Run("mkfs.ext4", "-F", "-L", "root", "-O", "verity", dev)
	case "btrfs":
		return runner.Run("mkfs.btrfs", "-f", "-L", "root", dev)
	default:
		return fmt.Errorf("unsupported filesystem: %q", filesystem)
	}
}

// Mount mounts dev at target. opts is an optional comma-separated option string
// passed to -o; pass "" for no options.
//
// On failure the error carries mount's own stderr plus a blkid probe of the
// device: in the deployer initramfs, runner.Run's streamed output never
// reaches the serial console, so "exit status 32" arrived with zero
// diagnosable context (GH run 20260723T2257 burned an hour of archaeology
// on a message that mount itself had already explained).
func Mount(dev, target, opts string) error {
	args := []string{}
	if opts != "" {
		args = append(args, "-o", opts)
	}
	args = append(args, dev, target)
	if err := runner.Run("mount", args...); err != nil {
		name, cargs := runner.HostArgs("mount", args)
		out, _ := exec.Command(name, cargs...).CombinedOutput()
		probe, _ := runner.Output("blkid", dev)
		// The kernel's ring buffer is the only place that says WHY a mount
		// was rejected (unsupported feature bits, SB validation, missing
		// module): mount's own stderr came back empty in the deployer
		// initramfs (GH bonito repro 20260724T0335).
		dmesg, _ := runner.Output("sh", "-c", "dmesg | tail -8")
		return fmt.Errorf("%w: %s (blkid: %s) (dmesg: %s)", err,
			strings.TrimSpace(string(out)), strings.TrimSpace(string(probe)),
			strings.TrimSpace(string(dmesg)))
	}
	return nil
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

// FormatBoot formats a partition as ext4 for use as /boot.
// ext4 is used for broad bootloader compatibility.
func FormatBoot(part string) error {
	return runner.Run("mkfs.ext4", "-L", "boot", "-F", part)
}

// MountBoot creates the /boot directory under rootMount and mounts bootPart
// there. Must be called before MountEFI so /boot/efi can be created inside it.
func MountBoot(rootMount, bootPart string) error {
	bootDir := rootMount + "/boot"
	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", bootDir, err)
	}
	return runner.Run("mount", bootPart, bootDir)
}

// FinalizeFilesystem replicates the three operations bootc performs when
// --skip-finalize is NOT passed (bootc's internal finalize_filesystem()):
//  1. fstrim  — discards unused blocks for SSD health
//  2. remount ro — flushes writeback and locks the deployment read-only
//  3. fsfreeze/thaw — flushes the journal so the first boot is clean
func FinalizeFilesystem(path string) error {
	progress.Info(fmt.Sprintf("fstrim %s", path))
	if err := runner.Run("fstrim", "--quiet-unsupported", "-v", path); err != nil {
		return fmt.Errorf("fstrim: %w", err)
	}
	progress.Info(fmt.Sprintf("remount ro %s", path))
	if err := runner.Run("mount", "-o", "remount,ro", path); err != nil {
		return fmt.Errorf("remount ro: %w", err)
	}
	progress.Info(fmt.Sprintf("fsfreeze %s", path))
	if err := runner.Run("fsfreeze", "-f", path); err != nil {
		return fmt.Errorf("fsfreeze: %w", err)
	}
	if err := runner.Run("fsfreeze", "-u", path); err != nil {
		return fmt.Errorf("fsfreeze -u: %w", err)
	}
	return nil
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

// FormatVar formats a device as XFS for use as a dedicated /var partition.
func FormatVar(dev string) error {
	return runner.Run("mkfs.xfs", "-f", "-L", "var", dev)
}

// UUID returns the filesystem UUID of dev using blkid.
// Returns an empty string on any error (non-fatal; fstab write will be skipped).
func UUID(dev string) string {
	out, err := runner.Output("blkid", "-s", "UUID", "-o", "value", dev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
