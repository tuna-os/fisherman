package disk

import (
	"fmt"
	"unicode"

	"github.com/tuna-os/fisherman/internal/runner"
)

// PartSuffix returns the separator between a disk name and a partition number.
// NVMe and MMC devices (whose names end in a digit) use "p"; all others use "".
func PartSuffix(disk string) string {
	if len(disk) == 0 {
		return ""
	}
	if unicode.IsDigit(rune(disk[len(disk)-1])) {
		return "p"
	}
	return ""
}

// PartName returns the full device path for a partition on a disk.
// e.g. PartName("/dev/sda", 3) → "/dev/sda3"
//
//	PartName("/dev/nvme0n1", 3) → "/dev/nvme0n1p3"
func PartName(disk string, num int) string {
	return fmt.Sprintf("%s%s%d", disk, PartSuffix(disk), num)
}

// Partition wipes the disk and creates the standard three-partition layout:
//
//	Partition 1 – BIOS boot  (1 MiB,   type ef02)
//	Partition 2 – EFI System (512 MiB, type ef00)
//	Partition 3 – Linux root (rest,    type 8304)
func Partition(disk string) error {
	if err := runner.Run("sgdisk", "--zap-all", disk); err != nil {
		return fmt.Errorf("zap partition table: %w", err)
	}

	if err := runner.Run("sgdisk",
		"--new=1:0:+1M", "--typecode=1:ef02",
		"--new=2:0:+512M", "--typecode=2:ef00",
		"--new=3:0:0", "--typecode=3:8304",
		disk,
	); err != nil {
		return fmt.Errorf("create partitions: %w", err)
	}

	// Notify the kernel and udev of the new partition table.
	// partprobe failure is non-fatal — udevadm settle covers the same ground.
	_ = runner.Run("partprobe", disk)
	_ = runner.Run("udevadm", "settle")

	return nil
}
