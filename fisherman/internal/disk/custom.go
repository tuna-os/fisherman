package disk

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

// MountSpec describes a single partition → mountpoint mapping for manual layouts.
// Mirrors recipe.CustomMount but lives in the disk package to avoid import cycles.
type MountSpec struct {
	Partition string // block device, e.g. "/dev/sda1"
	Target    string // absolute mountpoint, e.g. "/" or "/boot/efi"; "swap" for swap
	Fstype    string // "xfs", "btrfs", "ext4", "ext3", "fat32", "unformatted", "swap"
}

// ApplyCustomLayout formats and mounts a user-specified set of partitions under
// targetBase (typically /mnt/fisherman-target).
//
// Returns:
//   - targetMount: path where "/" was mounted (targetBase itself)
//   - efiPart:     block device mounted at /boot/efi (needed for EFI BootNext lookup)
//   - mounted:     all paths that were successfully mounted (register with Cleanup)
func ApplyCustomLayout(specs []MountSpec, targetBase string) (targetMount, efiPart string, mounted []string, err error) {
	if len(specs) == 0 {
		return "", "", nil, fmt.Errorf("no mount specs provided")
	}

	// Sort mounts by target depth so "/" comes first, then "/boot", then "/boot/efi" etc.
	sorted := make([]MountSpec, len(specs))
	copy(sorted, specs)
	sort.Slice(sorted, func(i, j int) bool {
		ti, tj := sorted[i].Target, sorted[j].Target
		// "/" sorts before everything; swap sorts last (handled separately)
		if ti == "/" {
			return true
		}
		if tj == "/" {
			return false
		}
		return strings.Count(ti, "/") < strings.Count(tj, "/")
	})

	targetMount = targetBase

	for _, s := range sorted {
		if s.Target == "swap" || s.Target == "" {
			if s.Target == "swap" && s.Fstype == "swap" {
				progress.Info(fmt.Sprintf("Activating swap: %s", s.Partition))
				if err2 := formatAndActivateSwap(s.Partition); err2 != nil {
					progress.Info(fmt.Sprintf("Warning: swap setup failed: %v", err2))
				}
			}
			continue
		}

		// Format the partition (unless user asked to keep existing filesystem).
		if s.Fstype != "unformatted" && s.Fstype != "" {
			progress.Info(fmt.Sprintf("Formatting %s as %s", s.Partition, s.Fstype))
			if err2 := formatPartition(s.Partition, s.Fstype); err2 != nil {
				return "", "", mounted, fmt.Errorf("format %s (%s): %w", s.Partition, s.Fstype, err2)
			}
		}

		// Determine the host-side mount path.
		var hostTarget string
		if s.Target == "/" {
			hostTarget = targetBase
		} else {
			hostTarget = targetBase + s.Target
		}

		if err2 := os.MkdirAll(hostTarget, 0o755); err2 != nil {
			return "", "", mounted, fmt.Errorf("mkdir %s: %w", hostTarget, err2)
		}

		progress.Info(fmt.Sprintf("Mounting %s → %s", s.Partition, hostTarget))
		if err2 := runner.Run("mount", s.Partition, hostTarget); err2 != nil {
			return "", "", mounted, fmt.Errorf("mount %s at %s: %w", s.Partition, hostTarget, err2)
		}
		mounted = append(mounted, hostTarget)

		if s.Target == "/boot/efi" {
			efiPart = s.Partition
		}
	}

	if targetMount == "" {
		return "", "", mounted, fmt.Errorf("no root partition (/) found in mount specs")
	}
	return targetMount, efiPart, mounted, nil
}

// formatPartition formats dev with the given filesystem type.
func formatPartition(dev, fstype string) error {
	switch fstype {
	case "fat32":
		return runner.Run("mkfs.fat", "-F32", dev)
	case "ext4":
		// -O verity for the same reason FormatRoot uses it: bootc's
		// --composefs-backend calls FS_IOC_ENABLE_VERITY on deployed files, and
		// ext4 only permits that when the feature was set at format time.
		// Omitting it here made composefs installs onto a manual layout fail
		// with "Filesystem does not support fs-verity" *after* the target had
		// been formatted and the image deployed — the manual-layout path is the
		// one used to install alongside an existing OS, so that late failure
		// lands on a partition the user cannot afford to lose.
		return runner.Run("mkfs.ext4", "-F", "-O", "verity", dev)
	case "ext3":
		return runner.Run("mkfs.ext3", "-F", dev)
	case "xfs":
		return runner.Run("mkfs.xfs", "-f", dev)
	case "btrfs":
		return runner.Run("mkfs.btrfs", "-f", dev)
	default:
		return fmt.Errorf("unsupported filesystem: %q", fstype)
	}
}

// formatAndActivateSwap formats dev as swap and activates it.
func formatAndActivateSwap(dev string) error {
	if err := runner.Run("mkswap", dev); err != nil {
		return fmt.Errorf("mkswap: %w", err)
	}
	return runner.Run("swapon", dev)
}
