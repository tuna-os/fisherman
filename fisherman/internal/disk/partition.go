package disk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tuna-os/fisherman/internal/runner"
)

// procMountsPath is the path to the mounts file. Overridden in tests.
var procMountsPath = "/proc/mounts"

// GPTPartTypeLinuxRootX86_64 and GPTPartTypeLinuxRootAArch64 are the
// Discoverable Partitions Specification root types. systemd-gpt-auto-generator
// only auto-discovers a root filesystem — or the LUKS container holding one —
// whose GPT type matches the running architecture.
const GPTPartTypeLinuxRootX86_64 = "4f68bce3-e8cd-4db1-96e7-fbcaf984b709"
const GPTPartTypeLinuxRootAArch64 = "b921b045-1df0-41c3-af44-4c6f280d3fae"

// LinuxRootPartType returns the DPS root partition type for the architecture
// fisherman was built for, which is the one the installed system boots on.
func LinuxRootPartType() string {
	if runtime.GOARCH == "arm64" {
		return GPTPartTypeLinuxRootAArch64
	}
	return GPTPartTypeLinuxRootX86_64
}

// GetProcMountsPath returns the current mounts file path. For testing only.
func GetProcMountsPath() string { return procMountsPath }

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

// UnmountPartition releases kernel and userspace references to a specific partition
// in preparation for modifying its GPT attributes (e.g., retagging). This is less
// destructive than unmountAll() — it only affects the specific partition, not the
// entire disk. It does NOT deactivate LVM or kill processes.
func UnmountPartition(disk string, partNum int) error {
	partPath := PartName(disk, partNum)
	data, err := os.ReadFile(procMountsPath)
	if err != nil {
		return fmt.Errorf("reading /proc/mounts: %w", err)
	}

	// Find and unmount any mounts for this specific partition.
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		dev := fields[0]
		mp := fields[1]

		// Only process mounts for this specific partition.
		if dev != partPath {
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
			_ = runner.Run("umount", "-l", mp)
		}
	}

	// Flush pending I/O so the kernel can drop its internal references.
	fmt.Fprintf(os.Stdout, "+ blockdev --flushbufs %s\n", partPath)
	_ = runner.Run("blockdev", "--flushbufs", partPath)

	// Give udev and udisksd time to release all device references.
	fmt.Fprintf(os.Stdout, "+ udevadm settle\n")
	_ = runner.Run("udevadm", "settle")

	return nil
}

// SetPartitionType rewrites the GPT type for a single partition on disk.
func SetPartitionType(disk string, partNum int, partType string) error {
	if err := runner.Run("sfdisk", "--part-type", disk, strconv.Itoa(partNum), partType); err != nil {
		return fmt.Errorf("setting partition type: %w", err)
	}
	return nil
}

// Partition wipes disk and creates a three-partition GPT layout using sfdisk:
//
//	Partition 1 – EFI System (2 GiB)
//	Partition 2 – /boot     (2 GiB, ext4 — GRUB reads this)
//	Partition 3 – Linux root (remaining space)
//
// 2 GiB ESP for fleet consistency with dakota (systemd-boot). Fedora/Anaconda
// defaults to 500-600 MiB but that's for interactive installs that don't need
// to hold multiple kernels on the ESP. Uniform 2 GiB simplifies fleet tooling.
//
// A separate /boot (ext4) is required because GRUB's built-in XFS driver
// does not support newer XFS features (nrext64, rmapbt) on el10.
//
// On real block devices sfdisk notifies the kernel via BLKRRPART, so
// partition devices appear after udevadm settle. Loop devices reject
// BLKRRPART; we work around this by detaching and re-attaching the loop
// device with --partscan so the kernel creates /dev/loopNpM nodes.
func Partition(disk string) error {
	script := strings.Join([]string{
		"label: gpt",
		"",
		`size=2GiB, type=uefi, name="EFI-SYSTEM"`,
		`size=2GiB,   type=linux, name="boot"`,
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
//	Partition 1 – EFI System (2 GiB — holds bootloader binary, kernel and initrd)
//	Partition 2 – Linux root (remaining space)
//
// Unlike grub2 installs, systemd-boot can only read FAT32 via UEFI firmware
// protocols. A separate ext4 /boot would be unreadable, so everything lands
// directly on the FAT32 ESP. Each kernel+initrd pair is ~200 MiB; 2 GiB
// comfortably accommodates the booted entry, rollback, and a staged upgrade
// without running out of space even if stale entries accumulate.
// This layout is used for both unencrypted and encrypted systemd-boot installs
// (encrypted: LUKS wraps partition 2).
//
// The root partition carries the architecture's DPS root type from the start.
// Sealed composefs images boot from a UKI whose command line carries no root=,
// so systemd-gpt-auto-generator has to find the root — plain or LUKS — by that
// GPT type; a generic "linux" type leaves the boot waiting on
// /dev/gpt-auto-root until it times out (#219). Setting it here rather than
// retagging after the install also covers the encrypted case, where the raw
// partition is hidden behind the LUKS mapping and cannot be unmounted and
// remounted around an sfdisk rewrite.
func PartitionSystemdBoot(disk string) error {
	script := strings.Join([]string{
		"label: gpt",
		"",
		`size=2GiB, type=uefi, name="EFI-SYSTEM"`,
		fmt.Sprintf(`type=%s, name="root"`, LinuxRootPartType()),
	}, "\n") + "\n"
	return partition(disk, script)
}

// PartitionZFS wipes disk and creates a two-partition GPT layout for
// ZFS installs:
//
//	Partition 1 – EFI System (1 GiB — holds bootloader, kernel, initrd)
//	Partition 2 – ZFS pool   (remaining space)
//
// ZFS installs use systemd-boot which reads directly from the FAT32 ESP.
// The ZFS pool on p2 holds the composefs-native bootc store.
func PartitionZFS(disk string) error {
	script := strings.Join([]string{
		"label: gpt",
		"",
		`size=1GiB, type=uefi, name="EFI-SYSTEM"`,
		`type=linux, name="zfs-pool"`,
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
		// --no-reread means the kernel still holds the OLD partition table.
		// Force a re-read with partprobe so subsequent mkfs calls see the
		// correct partition sizes. Without this, mkfs operates on stale
		// (possibly much smaller) partition boundaries → "no space left".
		fmt.Fprintf(os.Stdout, "+ partprobe %s (force kernel partition table re-read after --no-reread)\n", disk)
		time.Sleep(500 * time.Millisecond)
		_ = runner.Run("partprobe", disk)
		_ = runner.Run("udevadm", "settle")
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
//	p2 = EFI System (2 GiB, FAT32)
//	p3 = Linux root (remainder, btrfs/xfs/ext4)
//
// It uses lsblk to confirm the layout rather than hardcoding partition numbers.
func FindSystemdBootPartitions(diskDev string) (efiPart, rootPart string, err error) {
	name, args := runner.HostArgs("lsblk", []string{"-J", "-p", "-o", "NAME,PARTTYPE", diskDev})
	out, err := runner.WrappedCommand(name, args...).Output()
	if err != nil {
		return "", "", fmt.Errorf("lsblk: %w", err)
	}
	type blockdev struct {
		Name     string     `json:"name"`
		Parttype string     `json:"parttype"`
		Children []blockdev `json:"children"`
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
// After re-attach, partprobe forces a partition-table re-read to eliminate a
// race where the kernel hasn't finished populating the partition nodes before
// subsequent mkfs/mount calls access them (observed as "No such device or
// address" on /dev/loopNpM in CI, especially under load with large images).
func loopRescan(disk string) error {
	qname, qargs := runner.HostArgs("losetup", []string{"--noheadings", "-O", "BACK-FILE", disk})
	out, err := runner.WrappedCommand(qname, qargs...).Output()
	if err != nil {
		return fmt.Errorf("query backing file: %w", err)
	}
	backFile := strings.TrimSpace(string(out))

	dname, dargs := runner.HostArgs("losetup", []string{"-d", disk})
	if err := runner.WrappedCommand(dname, dargs...).Run(); err != nil {
		return fmt.Errorf("detach: %w", err)
	}
	rname, rargs := runner.HostArgs("losetup", []string{"-P", disk, backFile})
	if err := runner.WrappedCommand(rname, rargs...).Run(); err != nil {
		return fmt.Errorf("reattach with partscan: %w", err)
	}

	// partprobe forces the kernel to re-read the partition table even
	// when udev is still processing the loop-attach event. Without this,
	// a busy CI runner can hit a window where losetup -P has returned but
	// /dev/loopNpM nodes don't exist yet → mkfs fails with ENOENT.
	_ = runner.Run("partprobe", disk)
	_ = runner.Run("udevadm", "settle")
	return nil
}

// blockNode is one entry of `lsblk -J -o NAME,PKNAME,MAJ:MIN,TYPE`.
type blockNode struct {
	Name     string      `json:"name"`
	PKName   string      `json:"pkname"`
	MajMin   string      `json:"maj:min"`
	Type     string      `json:"type"`
	Children []blockNode `json:"children"`
}

// blockSet is the selected disk and every block device stacked on it:
// partitions, LUKS and other device-mapper mappings, LVM logical volumes.
// It is built from the kernel's own parent/child records, never from device
// names, so /dev/nvme0n1 does not take /dev/nvme0n10 with it (#357).
type blockSet struct {
	disk   string            // the path the caller selected
	root   blockNode         // the selected disk as lsblk reports it
	names  map[string]string // lsblk NAME → MAJ:MIN
	majmin map[string]string // MAJ:MIN → lsblk NAME
	// order lists every member's MAJ:MIN deepest-first, so a mapping is
	// removed before the device it sits on.
	order []string
}

func (b *blockSet) hasMajMin(mm string) bool { _, ok := b.majmin[mm]; return ok }

// hasType reports whether any member has the given lsblk TYPE.
func (b *blockSet) hasType(typ string) bool {
	var walk func(n blockNode) bool
	walk = func(n blockNode) bool {
		if n.Type == typ {
			return true
		}
		for _, c := range n.Children {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(b.root)
}

// ownsDevicePath reports whether a device path from /proc/mounts names a
// member of the set. The match is on the whole name: /dev/<name> for disks
// and partitions, /dev/mapper/<name> for device-mapper devices.
func (b *blockSet) ownsDevicePath(dev string) bool {
	for _, prefix := range []string{"/dev/mapper/", "/dev/"} {
		if name, ok := strings.CutPrefix(dev, prefix); ok && !strings.Contains(name, "/") {
			if _, member := b.names[name]; member {
				return true
			}
		}
	}
	return false
}

// resolveBlockSet asks the host's lsblk for the block-device tree and returns
// the selected disk with its descendants. It fails closed: when lsblk fails,
// its output cannot be read, the disk is not in it, or a device stacked on
// the disk also sits on a device outside it (an md array, a multipath map, a
// logical volume that spans disks), it returns an error and the caller must
// not tear anything down.
func resolveBlockSet(disk string) (*blockSet, error) {
	out, err := runner.Output("lsblk", "-J", "-o", "NAME,PKNAME,MAJ:MIN,TYPE")
	if err != nil {
		return nil, fmt.Errorf("cannot establish which block devices belong to %s: lsblk: %w", disk, err)
	}
	var parsed struct {
		Blockdevices []blockNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("cannot establish which block devices belong to %s: parsing lsblk output: %w", disk, err)
	}

	var normalize func(nodes []blockNode)
	normalize = func(nodes []blockNode) {
		for i := range nodes {
			nodes[i].MajMin = strings.TrimSpace(nodes[i].MajMin)
			normalize(nodes[i].Children)
		}
	}
	normalize(parsed.Blockdevices)

	// The caller may pass an alias (/dev/disk/by-id/…); lsblk reports kernel
	// names. Accept either spelling, but only as a whole-name match.
	want := map[string]bool{filepath.Base(disk): true}
	if real, err := filepath.EvalSymlinks(disk); err == nil {
		want[filepath.Base(real)] = true
	}

	var root *blockNode
	var find func(nodes []blockNode)
	find = func(nodes []blockNode) {
		for i := range nodes {
			n := &nodes[i]
			if want[n.Name] && n.MajMin != "" {
				if root == nil {
					root = n
				}
				continue
			}
			find(n.Children)
		}
	}
	find(parsed.Blockdevices)
	if root == nil {
		return nil, fmt.Errorf("cannot establish which block devices belong to %s: lsblk does not list it", disk)
	}

	set := &blockSet{disk: disk, root: *root, names: map[string]string{}, majmin: map[string]string{}}
	var bad error
	var collect func(n blockNode)
	collect = func(n blockNode) {
		for _, c := range n.Children {
			collect(c)
		}
		if n.Name == "" || n.MajMin == "" {
			bad = fmt.Errorf("cannot establish which block devices belong to %s: lsblk reported a device without a name or a major:minor number", disk)
			return
		}
		if _, seen := set.majmin[n.MajMin]; !seen {
			set.order = append(set.order, n.MajMin)
		}
		set.majmin[n.MajMin] = n.Name
		set.names[n.Name] = n.MajMin
	}
	collect(*root)
	if bad != nil {
		return nil, bad
	}

	// Anything stacked on the disk that lsblk also reaches without passing
	// through the disk is shared with other storage.
	var shared []string
	seenShared := map[string]bool{}
	var outside func(nodes []blockNode)
	outside = func(nodes []blockNode) {
		for _, n := range nodes {
			if n.MajMin == root.MajMin {
				continue // the selected disk; its subtree is the set
			}
			if set.hasMajMin(n.MajMin) && !seenShared[n.MajMin] {
				seenShared[n.MajMin] = true
				shared = append(shared, fmt.Sprintf("%s (%s)", n.Name, n.MajMin))
			}
			outside(n.Children)
		}
	}
	outside(parsed.Blockdevices)
	if len(shared) > 0 {
		return nil, fmt.Errorf("refusing to prepare %s: %s also uses a device that is not on this disk; "+
			"stop it or remove this disk from it first", disk, strings.Join(shared, ", "))
	}
	return set, nil
}

// volumeGroupsOn returns the LVM volume groups whose physical volumes are all
// members of set. A group that also has a physical volume elsewhere is an
// error: deactivating it would take storage on another disk offline.
func volumeGroupsOn(set *blockSet) ([]string, error) {
	out, err := runner.Output("pvs", "--noheadings", "--separator", "|", "-o", "pv_name,vg_name,pv_major,pv_minor")
	if err != nil {
		// No lvm2 on the host means no volume groups. But when lsblk already
		// shows a logical volume on this disk, LVM is in use and its layout
		// must be known before anything is torn down.
		if set.hasType("lvm") {
			return nil, fmt.Errorf("cannot establish which volume groups use %s: pvs: %w", set.disk, err)
		}
		fmt.Fprintf(os.Stdout, "+ pvs unavailable (%v); no LVM on %s to deactivate\n", err, set.disk)
		return nil, nil
	}
	inside := map[string][]string{}
	outside := map[string][]string{}
	var order []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimSpace(line), "|")
		if len(f) != 4 {
			continue
		}
		pv, vg := strings.TrimSpace(f[0]), strings.TrimSpace(f[1])
		if vg == "" {
			continue // an orphan physical volume belongs to no group
		}
		mm := strings.TrimSpace(f[2]) + ":" + strings.TrimSpace(f[3])
		if _, known := inside[vg]; !known {
			if _, known := outside[vg]; !known {
				order = append(order, vg)
			}
		}
		if set.hasMajMin(mm) {
			inside[vg] = append(inside[vg], pv)
		} else {
			outside[vg] = append(outside[vg], pv)
		}
	}
	var vgs []string
	for _, vg := range order {
		if len(inside[vg]) == 0 {
			continue
		}
		if len(outside[vg]) > 0 {
			return nil, fmt.Errorf("refusing to prepare %s: volume group %s also uses %s, which is not on this disk; "+
				"remove this disk from the group (vgreduce) or pick a different disk",
				set.disk, vg, strings.Join(outside[vg], ", "))
		}
		vgs = append(vgs, vg)
	}
	return vgs, nil
}

// unmountAll aggressively releases all kernel and userspace references to disk
// so that sfdisk can safely repartition it.
//
// It first resolves the exact set of block devices that belong to disk
// (resolveBlockSet) and the volume groups that live only on it
// (volumeGroupsOn). Every teardown command below acts on that set and on
// nothing else; if the set cannot be established, nothing is torn down.
//
// Flash drives and USB disks are managed by udisksd, which holds an open FD
// on the block device even after umount -l. sfdisk detects that FD and refuses
// with "disk is currently in use". The sequence below is ordered from least to
// most destructive:
//
//  1. udisksctl unmount — releases udisksd's lock for automounted partitions.
//  2. umount -l         — lazy fallback for manually-mounted or non-udisks mounts.
//  3. swapoff           — disable any active swap partitions on the disk.
//  4. deactivateLVM     — deactivate LVM VGs and DM devices (dm-crypt etc.) that
//     hold the disk open; without this, sfdisk's BLKRRPART ioctl fails, forcing
//     the --no-reread path and leaving the kernel with a stale partition table.
//  5. fuser -km         — SIGKILL every process with an open FD on the disk.
//  6. blockdev --flushbufs — flush pending I/O so the kernel can drop references.
//  7. udevadm settle    — wait for udev/udisksd to release device references.
func unmountAll(disk string) error {
	set, err := resolveBlockSet(disk)
	if err != nil {
		return err
	}
	vgs, err := volumeGroupsOn(set)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(procMountsPath)
	if err != nil {
		return fmt.Errorf("reading /proc/mounts: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		dev := fields[0] // e.g. /dev/sda1
		mp := fields[1]  // e.g. /run/media/james/Yellowfin_Live
		if !set.ownsDevicePath(dev) {
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

	// Deactivate LVM volume groups and device-mapper devices backed by this
	// disk. Without this, the LVM/dm layer holds the disk open and sfdisk's
	// BLKRRPART ioctl fails, forcing --no-reread and leaving the kernel with
	// a stale (potentially much smaller) partition table.
	deactivateLVM(set, vgs)

	// Kill any processes still holding FDs open on any partition of this disk.
	// fuser exits non-zero when no processes are found — that is fine.
	//
	// EXCEPTION: network block devices (/dev/nbd*) are served by a userspace
	// process (qemu-nbd --connect) that holds the device open by design.
	// fuser -km would SIGKILL that server, tearing down the connection and
	// leaving sfdisk with "cannot open /dev/nbd0: Invalid argument". A
	// freshly attached NBD device has no stale holders to evict anyway, so
	// skip the kill entirely for it.
	if isNBD(disk) {
		fmt.Fprintf(os.Stdout, "+ skipping fuser -km on %s (NBD server must survive)\n", disk)
	} else {
		fmt.Fprintf(os.Stdout, "+ fuser -km %s (kill remaining holders)\n", disk)
		_ = runner.Run("fuser", "-km", disk)
	}

	// Flush pending I/O so the kernel can drop its internal references.
	_ = runner.Run("blockdev", "--flushbufs", disk)

	// Give udev and udisksd time to release all device references.
	_ = runner.Run("udevadm", "settle")
	return nil
}

// isNBD reports whether disk is a network block device (/dev/nbd*),
// which is served by a userspace qemu-nbd process that must not be killed.
func isNBD(disk string) bool {
	return strings.HasPrefix(filepath.Base(disk), "nbd")
}

// deactivateLVM deactivates the volume groups in vgs and removes the
// device-mapper devices (dm-crypt, LUKS, leftover LVs) that are members of
// set. This is required before repartitioning a disk that previously held LVM
// or LUKS — the device-mapper layer keeps an open reference on the underlying
// block device, causing sfdisk to fail with "disk is currently in use" even
// after all filesystems are unmounted.
//
// Both lists come from unmountAll's verified block set. A mapper is removed
// only when the kernel's current `dmsetup ls` shows it under the same name
// and the same major:minor number that lsblk reported as a descendant of the
// disk; a name or a number that merely looks similar is never enough.
func deactivateLVM(set *blockSet, vgs []string) {
	for _, vg := range vgs {
		fmt.Fprintf(os.Stdout, "+ vgchange -an %s (LVM VG on %s)\n", vg, set.disk)
		_ = runner.Run("vgchange", "-an", vg)
	}

	dmList, err := runner.Output("dmsetup", "ls")
	if err != nil {
		return
	}
	// `dmsetup ls` prints "name (253:0)"; older releases print "(253, 0)".
	live := map[string]string{} // MAJ:MIN → mapper name
	for _, line := range strings.Split(string(dmList), "\n") {
		open := strings.LastIndex(line, "(")
		closeIdx := strings.LastIndex(line, ")")
		if open < 0 || closeIdx < open {
			continue
		}
		name := strings.TrimSpace(line[:open])
		nums := strings.FieldsFunc(line[open+1:closeIdx], func(r rune) bool { return r == ':' || r == ',' || r == ' ' })
		if name == "" || len(nums) != 2 {
			continue
		}
		live[nums[0]+":"+nums[1]] = name
	}
	for _, mm := range set.order {
		name, ok := live[mm]
		if !ok || set.majmin[mm] != name || mm == set.root.MajMin {
			continue
		}
		fmt.Fprintf(os.Stdout, "+ dmsetup remove --force %s (%s, backed by %s)\n", name, mm, set.disk)
		_ = runner.Run("dmsetup", "remove", "--force", name)
	}
}
