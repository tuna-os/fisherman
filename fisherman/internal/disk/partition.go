package disk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/tuna-os/fisherman/internal/runner"
)

// procMountsPath is the path to the mounts file. Overridden in tests.
var procMountsPath = "/proc/mounts"

// SetProcMountsPath overrides the mounts file path. For testing only.
func SetProcMountsPath(p string) { procMountsPath = p }
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

// Partition wipes disk and creates a three-partition GPT layout using sfdisk:
//
//	Partition 1 – EFI System (512 MiB)
//	Partition 2 – /boot     (1 GiB, ext4 — bootloader reads this)
//	Partition 3 – Linux root (remaining space)
//
// A separate /boot partition is required because GRUB's built-in XFS driver
// does not support the newer XFS features enabled by mkfs.xfs on el10
// (nrext64, exchange, rmapbt). By keeping /boot on ext4, GRUB never needs
// to parse XFS. This matches what bootc install to-disk always does.
//
// On real block devices sfdisk notifies the kernel via BLKRRPART, so
// partition devices appear after udevadm settle. Loop devices reject
// BLKRRPART; we work around this by detaching and re-attaching the loop
// device with --partscan so the kernel creates /dev/loopNpM nodes.
func Partition(disk string) error {
	script := strings.Join([]string{
		"label: gpt",
		"",
		`size=512MiB, type=uefi, name="EFI-SYSTEM"`,
		`size=1GiB,   type=linux, name="boot"`,
		`type=linux, name="root"`,
	}, "\n") + "\n"
	return partition(disk, script)
}

// PartitionEncrypted wipes disk and creates the same three-partition GPT
// layout as Partition. The separate unencrypted /boot is also required for
// encrypted installs so that bootupctl (which runs in a restricted bwrap
// sandbox) can find the boot filesystem UUID from the raw block device.
func PartitionEncrypted(disk string) error {
	return Partition(disk)
}

// PartitionSystemdBoot wipes disk and creates a two-partition GPT layout for
// systemd-boot installs:
//
//	Partition 1 – EFI System (1 GiB — holds bootloader binary, kernel and initrd)
//	Partition 2 – Linux root (remaining space)
//
// Unlike grub2 installs, systemd-boot can only read FAT32 via UEFI firmware
// protocols. A separate ext4 /boot would be unreadable, so everything lands
// directly on the FAT32 ESP. 1 GiB gives headroom for multiple kernel versions.
// This layout is used for both unencrypted and encrypted systemd-boot installs
// (encrypted: LUKS wraps partition 2).
func PartitionSystemdBoot(disk string) error {
	script := strings.Join([]string{
		"label: gpt",
		"",
		`size=1GiB, type=uefi, name="EFI-SYSTEM"`,
		`type=linux, name="root"`,
	}, "\n") + "\n"
	return partition(disk, script)
}

// partition is the shared implementation for Partition and PartitionEncrypted.
func partition(disk, script string) error {
	// Unmount any mounted partitions on this disk before partitioning.
	if err := unmountAll(disk); err != nil {
		return fmt.Errorf("unmounting partitions: %w", err)
	}

	// First attempt without --force.
	err := runner.RunWithStdin(bytes.NewBufferString(script), "sfdisk", "--wipe=always", disk)
	if err != nil {
		// The disk may still be considered "in use" by the kernel even after
		// aggressive unmounting (e.g. a race with udev re-probing). Retry once
		// with --force --no-reread so sfdisk writes the table unconditionally.
		fmt.Fprintf(os.Stdout, "+ sfdisk first attempt failed (%v), retrying with --force\n", err)
		time.Sleep(500 * time.Millisecond)
		_ = runner.Run("udevadm", "settle")
		if err2 := runner.RunWithStdin(bytes.NewBufferString(script), "sfdisk", "--wipe=always", "--force", "--no-reread", disk); err2 != nil {
			return fmt.Errorf("sfdisk: %w", err2)
		}
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

// FindSystemdBootPartitions returns the EFI and root partition paths after
// `bootc install to-disk` with systemd-boot. bootc creates a 3-partition GPT:
//
//	p1 = BIOS boot (1 MiB)
//	p2 = EFI System (512 MiB, FAT32)
//	p3 = Linux root (remainder, btrfs/xfs/ext4)
//
// It uses lsblk to confirm the layout rather than hardcoding partition numbers.
func FindSystemdBootPartitions(diskDev string) (efiPart, rootPart string, err error) {
	out, err := exec.Command("lsblk", "-J", "-p", "-o", "NAME,PARTTYPE", diskDev).Output()
	if err != nil {
		return "", "", fmt.Errorf("lsblk: %w", err)
	}
	type blockdev struct {
		Name     string      `json:"name"`
		Parttype string      `json:"parttype"`
		Children []blockdev  `json:"children"`
	}
	type lsblkOutput struct {
		Blockdevices []blockdev `json:"blockdevices"`
	}
	var parsed lsblkOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", "", fmt.Errorf("parsing lsblk output: %w", err)
	}
	const (
		efiGUID  = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
		biosGUID = "21686148-6449-6e6f-744e-656564454649"
	)
	for _, dev := range parsed.Blockdevices {
		for _, part := range dev.Children {
			switch strings.ToLower(part.Parttype) {
			case efiGUID:
				efiPart = part.Name
			case biosGUID:
				// skip
			default:
				if part.Name != "" {
					rootPart = part.Name
				}
			}
		}
	}
	if efiPart == "" || rootPart == "" {
		return "", "", fmt.Errorf("could not identify EFI and root partitions on %s", diskDev)
	}
	return efiPart, rootPart, nil
}

// RescanPartitions asks the kernel to re-read the partition table on disk.
// Used after `bootc install to-disk` which partitions the disk inside a
// container — the host kernel may not have seen the new partition layout yet.
// Returns nil even if the rescan is best-effort (e.g. non-loop block devices).
func RescanPartitions(d string) error {
	time.Sleep(200 * time.Millisecond)
	_ = runner.Run("udevadm", "settle")
	if strings.HasPrefix(filepath.Base(d), "loop") {
		return loopRescan(d)
	}
	// For real block devices, partprobe (or partx) triggers a kernel re-read.
	_ = runner.Run("partprobe", d)
	_ = runner.Run("udevadm", "settle")
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

// unmountAll aggressively releases all kernel and userspace references to disk
// so that sfdisk can safely repartition it.
//
// Flash drives and USB disks are managed by udisksd, which holds an open FD
// on the block device even after umount -l. sfdisk detects that FD and refuses
// with "disk is currently in use". The sequence below is ordered from least to
// most destructive:
//
//  1. udisksctl unmount — releases udisksd's lock for automounted partitions.
//  2. umount -l         — lazy fallback for manually-mounted or non-udisks mounts.
//  3. swapoff           — disable any active swap partitions on the disk.
//  4. fuser -km         — SIGKILL every process with an open FD on the disk.
//  5. blockdev --flushbufs — flush pending I/O so the kernel can drop references.
//  6. udevadm settle    — wait for udev/udisksd to release device references.
func unmountAll(disk string) error {
	data, err := os.ReadFile(procMountsPath)
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
		if !strings.HasPrefix(devBase, base) {
			continue
		}

		// swap entries have mount point "none" and fstype "swap".
		if mp == "none" || (len(fields) >= 3 && fields[2] == "swap") {
			fmt.Fprintf(os.Stdout, "+ swapoff %s\n", dev)
			_ = runner.Run("swapoff", dev)
			continue
		}

		fmt.Fprintf(os.Stdout, "+ unmount %s (%s)\n", mp, dev)

		// Try udisksctl first — properly releases udisksd's open FD.
		if err := runner.Run("udisksctl", "unmount", "--no-user-interaction", "--block-device", dev); err != nil {
			// Fall back to umount -l for mounts not managed by udisks.
			fmt.Fprintf(os.Stdout, "+ udisksctl failed (%v), falling back to umount -l\n", err)
			_ = runner.Run("umount", "-l", mp)
		}
	}

	// Kill any processes still holding FDs open on any partition of this disk.
	// fuser exits non-zero when no processes are found — that is fine.
	fmt.Fprintf(os.Stdout, "+ fuser -km %s (kill remaining holders)\n", disk)
	_ = runner.Run("fuser", "-km", disk)

	// Flush pending I/O so the kernel can drop its internal references.
	_ = runner.Run("blockdev", "--flushbufs", disk)

	// Give udev and udisksd time to release all device references.
	_ = runner.Run("udevadm", "settle")
	return nil
}

// inFlatpakEnv reports whether the current process is inside a Flatpak sandbox.
func inFlatpakEnv() bool {
	_, err := os.Stat("/.flatpak-info")
	return err == nil
}
