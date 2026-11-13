package disk

import (
	"fmt"
	"strings"
)

// Layout names one of the GPT layouts the auto-partition path writes to a
// whole disk. The install (cmd/fisherman) partitions with PartitionLayout
// and `fisherman plan` reports LayoutPartitions, so both read the same
// table and cannot disagree about what is created.
type Layout string

const (
	// LayoutGRUB2: EFI + ext4 /boot + root. GRUB cannot read modern XFS
	// features, so /boot is ext4 on its own partition, encrypted or not.
	LayoutGRUB2 Layout = "grub2"
	// LayoutSystemdBoot: EFI + root. The kernel lives on the ESP; the root
	// carries the architecture's DPS root type for GPT auto-discovery.
	LayoutSystemdBoot Layout = "systemd-boot"
	// LayoutZFS: EFI + ZFS pool, booted with systemd-boot.
	LayoutZFS Layout = "zfs"
)

// Partition roles in a Layout.
const (
	RoleEFI  = "efi"  // the EFI system partition, FAT32
	RoleBoot = "boot" // the ext4 /boot of LayoutGRUB2
	RoleRoot = "root" // the root filesystem, its LUKS container, or the ZFS pool
)

// PartitionSpec is one partition of a Layout, in the terms of the sfdisk
// script that creates it.
type PartitionSpec struct {
	Number int    // partition number: PartName(disk, Number) is its device
	Role   string // RoleEFI, RoleBoot or RoleRoot
	Name   string // GPT partition name
	Type   string // sfdisk type: an alias ("uefi", "linux") or a GPT type GUID
	Size   string // sfdisk size, e.g. "2GiB"; "" takes the rest of the disk
}

// LayoutPartitions returns the partitions layout creates, in order. It is a
// pure function: it reads no disk and runs nothing.
func LayoutPartitions(layout Layout) ([]PartitionSpec, error) {
	switch layout {
	case LayoutGRUB2:
		return []PartitionSpec{
			{1, RoleEFI, "EFI-SYSTEM", "uefi", "2GiB"},
			{2, RoleBoot, "boot", "linux", "2GiB"},
			{3, RoleRoot, "root", "linux", ""},
		}, nil
	case LayoutSystemdBoot:
		return []PartitionSpec{
			{1, RoleEFI, "EFI-SYSTEM", "uefi", "2GiB"},
			{2, RoleRoot, "root", LinuxRootPartType(), ""},
		}, nil
	case LayoutZFS:
		return []PartitionSpec{
			{1, RoleEFI, "EFI-SYSTEM", "uefi", "1GiB"},
			{2, RoleRoot, "zfs-pool", "linux", ""},
		}, nil
	}
	return nil, fmt.Errorf("unknown partition layout %q", layout)
}

// PartNumber returns the number of the partition with role in layout, or 0
// when the layout has none (RoleBoot outside LayoutGRUB2).
func (layout Layout) PartNumber(role string) int {
	parts, _ := LayoutPartitions(layout)
	for _, p := range parts {
		if p.Role == role {
			return p.Number
		}
	}
	return 0
}

// layoutScript is the sfdisk script for parts.
func layoutScript(parts []PartitionSpec) string {
	lines := []string{"label: gpt", ""}
	for _, p := range parts {
		line := fmt.Sprintf(`type=%s, name="%s"`, p.Type, p.Name)
		if p.Size != "" {
			line = "size=" + p.Size + ", " + line
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n"
}

// PartitionLayout wipes disk and writes layout to it (see partition).
func PartitionLayout(disk string, layout Layout) error {
	parts, err := LayoutPartitions(layout)
	if err != nil {
		return err
	}
	return partition(disk, layoutScript(parts))
}
