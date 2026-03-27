package disk

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/tuna-os/fisherman/internal/runner"
)

// PartSuffix returns "p" for NVMe/MMC device names (which end in a digit),
// or "" for all others.
func PartSuffix(disk string) string {
	if len(disk) == 0 {
		return ""
	}
	if unicode.IsDigit(rune(disk[len(disk)-1])) {
		return "p"
	}
	return ""
}

// PartName returns the full device path for partition num on disk.
// e.g. PartName("/dev/sda", 2) → "/dev/sda2"
//
//	PartName("/dev/nvme0n1", 2) → "/dev/nvme0n1p2"
func PartName(disk string, num int) string {
	return fmt.Sprintf("%s%s%d", disk, PartSuffix(disk), num)
}

// Partition wipes disk and creates a two-partition GPT layout using sfdisk:
//
//	Partition 1 – EFI System (512 MiB, unformatted — caller runs FormatEFI)
//	Partition 2 – Linux root (remaining space, unformatted)
//
// On real block devices sfdisk notifies the kernel via BLKRRPART, so partition
// devices appear after udevadm settle.  Loop devices reject BLKRRPART; we work
// around this by detaching and re-attaching the loop device with --partscan so
// the kernel creates /dev/loopNpM nodes before returning.
func Partition(disk string) error {
	script := strings.Join([]string{
		"label: gpt",
		"",
		`size=512MiB, type=uefi, name="EFI-SYSTEM"`,
		`type=linux, name="root"`,
	}, "\n") + "\n"

	if err := runner.RunWithStdin(bytes.NewBufferString(script), "sfdisk", "--wipe=always", disk); err != nil {
		return fmt.Errorf("sfdisk: %w", err)
	}

	// Brief sleep then settle — mirrors bootc's approach.
	time.Sleep(200 * time.Millisecond)
	_ = runner.Run("udevadm", "settle")

	// Loop devices reject the BLKRRPART ioctl that sfdisk uses to notify the
	// kernel.  Re-attach with --partscan so /dev/loopNpM nodes are created.
	if strings.HasPrefix(filepath.Base(disk), "loop") {
		if err := loopRescan(disk); err != nil {
			return fmt.Errorf("loop device rescan: %w", err)
		}
	}

	return nil
}

// loopRescan detaches a loop device and re-attaches it with --partscan (-P)
// so the kernel creates partition block devices (/dev/loopNpM).
func loopRescan(disk string) error {
	out, err := exec.Command("losetup", "--noheadings", "-O", "BACK-FILE", disk).Output()
	if err != nil {
		return fmt.Errorf("query backing file: %w", err)
	}
	backFile := strings.TrimSpace(string(out))

	if err := exec.Command("losetup", "-d", disk).Run(); err != nil {
		return fmt.Errorf("detach: %w", err)
	}
	if err := exec.Command("losetup", "-P", disk, backFile).Run(); err != nil {
		return fmt.Errorf("reattach with partscan: %w", err)
	}

	_ = runner.Run("udevadm", "settle")
	return nil
}
