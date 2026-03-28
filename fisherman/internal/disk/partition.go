package disk

import (
	"bytes"
	"fmt"
	"os"
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
	// Unmount any mounted partitions on this disk before partitioning.
	if err := unmountAll(disk); err != nil {
		return fmt.Errorf("unmounting partitions: %w", err)
	}

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
	spawnArgs := func(name string, args ...string) (string, []string) {
		if inFlatpakEnv() {
			return "flatpak-spawn", append([]string{"--host", name}, args...)
		}
		return name, args
	}

	qname, qargs := spawnArgs("losetup", "--noheadings", "-O", "BACK-FILE", disk)
	out, err := exec.Command(qname, qargs...).Output()
	if err != nil {
		return fmt.Errorf("query backing file: %w", err)
	}
	backFile := strings.TrimSpace(string(out))

	dname, dargs := spawnArgs("losetup", "-d", disk)
	if err := exec.Command(dname, dargs...).Run(); err != nil {
		return fmt.Errorf("detach: %w", err)
	}
	rname, rargs := spawnArgs("losetup", "-P", disk, backFile)
	if err := exec.Command(rname, rargs...).Run(); err != nil {
		return fmt.Errorf("reattach with partscan: %w", err)
	}

	_ = runner.Run("udevadm", "settle")
	return nil
}

// unmountAll unmounts every mounted partition on disk by reading /proc/mounts.
func unmountAll(disk string) error {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return fmt.Errorf("reading /proc/mounts: %w", err)
	}
	base := filepath.Base(disk) // e.g. "sda"
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		dev := fields[0] // e.g. /dev/sda1
		mp := fields[1]  // e.g. /run/media/james/Yellowfin_Live
		devBase := filepath.Base(dev)
		if strings.HasPrefix(devBase, base) {
			fmt.Fprintf(os.Stdout, "+ umount %s (%s)\n", mp, dev)
			if err := runner.Run("umount", "-l", mp); err != nil {
				return fmt.Errorf("unmounting %s: %w", mp, err)
			}
		}
	}
	return nil
}

// inFlatpakEnv reports whether the current process is inside a Flatpak sandbox.
func inFlatpakEnv() bool {
	_, err := os.Stat("/.flatpak-info")
	return err == nil
}
