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

// MountAction is what ApplyCustomLayout does with one MountSpec.
type MountAction string

const (
	// MountFormat: the partition is formatted with Fstype, then mounted.
	MountFormat MountAction = "format"
	// MountPreserve: the partition is mounted as it is (fstype "" or
	// "unformatted"); its data is kept.
	MountPreserve MountAction = "preserve"
	// MountSwap: target "swap" with fstype "swap": mkswap, then swapon.
	MountSwap MountAction = "swap"
	// MountIgnore: target "" or "swap" with another fstype: left alone.
	MountIgnore MountAction = "ignore"
)

// Action returns what ApplyCustomLayout does with s. It reads nothing.
func (s MountSpec) Action() MountAction {
	switch {
	case s.Target == "swap" && s.Fstype == "swap":
		return MountSwap
	case s.Target == "swap" || s.Target == "":
		return MountIgnore
	case s.Fstype == "unformatted" || s.Fstype == "":
		return MountPreserve
	}
	return MountFormat
}

// OrderMounts returns specs in the order ApplyCustomLayout acts on them:
// "/" first, then by the number of slashes in the target ("/boot" before
// "/boot/efi"), the recipe's order among equals. "swap" and "" have no
// slash, so they come right after "/". It does not change specs.
func OrderMounts(specs []MountSpec) []MountSpec {
	sorted := make([]MountSpec, len(specs))
	copy(sorted, specs)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, tj := sorted[i].Target, sorted[j].Target
		if ti == "/" {
			return true
		}
		if tj == "/" {
			return false
		}
		return strings.Count(ti, "/") < strings.Count(tj, "/")
	})
	return sorted
}

// ApplyCustomLayout formats and mounts a user-specified set of partitions under
// targetBase (typically /mnt/fisherman-target).
//
// Returns:
//   - targetMount: path where "/" was mounted (targetBase itself)
//   - efiPart:     block device mounted at /boot/efi (needed for EFI BootNext lookup)
//   - mounted:     all paths that were successfully mounted (register with Cleanup)
func ApplyCustomLayout(specs []MountSpec, targetBase string) (targetMount, efiPart string, mounted []string, err error) {
	return ApplyCustomLayoutWith(specs, targetBase, LayoutHooks{})
}

// LayoutHooks lets the caller take ownership of each resource
// ApplyCustomLayoutWith acquires at the moment it is acquired, so a failure
// on a later partition, or a cancel signal that arrives half-way, still
// finds everything registered for teardown. Both are optional.
type LayoutHooks struct {
	// Mounted is called with the host path right after each successful
	// mount, in mount order (root first).
	Mounted func(path string)
	// SwapOn is called with the device before mkswap/swapon run on it.
	// Swap setup is non-fatal, so the device may never become active.
	SwapOn func(dev string)
}

// ApplyCustomLayoutWith is ApplyCustomLayout with ownership hooks.
func ApplyCustomLayoutWith(specs []MountSpec, targetBase string, hooks LayoutHooks) (targetMount, efiPart string, mounted []string, err error) {
	if len(specs) == 0 {
		return "", "", nil, fmt.Errorf("no mount specs provided")
	}

	hasRoot := false
	for _, s := range specs {
		if s.Target == "/" {
			hasRoot = true
			break
		}
	}
	if !hasRoot {
		return "", "", nil, fmt.Errorf("no root partition (/) found in mount specs")
	}

	sorted := OrderMounts(specs)

	targetMount = targetBase

	for _, s := range sorted {
		switch s.Action() {
		case MountIgnore:
			continue
		case MountSwap:
			progress.Info(fmt.Sprintf("Activating swap: %s", s.Partition))
			if hooks.SwapOn != nil {
				hooks.SwapOn(s.Partition)
			}
			if err2 := formatAndActivateSwap(s.Partition); err2 != nil {
				progress.Info(fmt.Sprintf("Warning: swap setup failed: %v", err2))
			}
			continue
		case MountFormat:
			progress.Info(fmt.Sprintf("Formatting %s as %s", s.Partition, s.Fstype))
			if err2 := formatPartition(s.Partition, s.Fstype); err2 != nil {
				return "", "", mounted, fmt.Errorf("format %s (%s): %w", s.Partition, s.Fstype, err2)
			}
		case MountPreserve:
			// Mounted as it is: the user keeps the existing filesystem.
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
		var mountErr error
		if s.Fstype == "btrfs" {
			mountErr = MountType(s.Partition, hostTarget, "btrfs", "compress=zstd:1")
		} else {
			mountErr = runner.Run("mount", s.Partition, hostTarget)
		}
		if mountErr != nil {
			return "", "", mounted, fmt.Errorf("mount %s at %s: %w", s.Partition, hostTarget, mountErr)
		}
		mounted = append(mounted, hostTarget)
		if hooks.Mounted != nil {
			hooks.Mounted(hostTarget)
		}

		if s.Target == "/boot/efi" {
			efiPart = s.Partition
		}
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
