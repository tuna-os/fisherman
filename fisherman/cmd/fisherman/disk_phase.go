package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The disk phase of an install (#313): everything from the first disk step
// to the point the target is mounted and ready for the OS install. For an
// automatic layout that is partition, format the ESP and the ext4 /boot,
// LUKS, format the root, mount it all; for a manual layout, the recipe's
// partitions formatted and mounted; for either, the /var disk.
//
// diskPhase takes what it needs as arguments (the recipe, the step
// sequencer, the mount point and LUKS mapper names, the disk operations)
// and returns what the later steps need (diskResult). It never ends the
// process: a failure comes back as a *phaseError with the failure code and
// the message main() passes to fatalCode, unchanged. Each resource is
// registered for teardown through ops the moment it is acquired, exactly
// as before, so a failure part-way leaves the same cleanup behind.

// diskResult is what the disk phase hands to the rest of the install.
type diskResult struct {
	// targetMount is where the target's root is mounted.
	targetMount string
	// efiPart is the ESP partition device.
	efiPart string
	// bootPart is the ext4 /boot partition of the GRUB2 layout; empty for
	// systemd-boot, ZFS and a manual layout.
	bootPart string
	// rootPart is the raw root partition; empty for a manual layout. TPM2
	// enrolment reads it.
	rootPart string
	// rootDev is the device the root filesystem was made on: rootPart, or
	// the LUKS mapper path. Empty for a manual layout.
	rootDev string
	// luksUUID is the UUID of the LUKS container on rootPart; empty without
	// encryption (or if it could not be read: main() refuses that).
	luksUUID string
	// recoveryKey is the random passphrase of a tpm2-luks install, emitted
	// as the recovery key after TPM2 enrolment; empty otherwise.
	recoveryKey string
}

// phaseError is a disk-phase failure: the progress.Failure* code and the
// message, both exactly what the inline fatalCode call used to get.
type phaseError struct {
	code string
	msg  string
}

func (e *phaseError) Error() string { return e.msg }

func phaseFail(code, format string, args ...any) *phaseError {
	return &phaseError{code: code, msg: fmt.Sprintf(format, args...)}
}

// failPhase ends the install for an error a phase returned, with the same
// error event and teardown as the fatalCode call it replaces.
func failPhase(err error) {
	if pe, ok := err.(*phaseError); ok {
		fatalCode(pe.code, "%s", pe.msg)
		return
	}
	fatal("%v", err)
}

// diskOps are the operations the disk phase performs and the teardown
// registrations it makes. liveDiskOps is the real set; the tests record
// and fail them.
type diskOps struct {
	applyCustomLayout func(specs []disk.MountSpec, target string, hooks disk.LayoutHooks) (string, string, []string, error)
	partitionLayout   func(dev string, layout disk.Layout) error
	formatEFI         func(part string) error
	formatBoot        func(part string) error
	mapperExists      func(path string) bool
	randomPassphrase  func() string
	luksClose         func(mapper string) error
	luksFormat        func(part, passphrase string) error
	luksOpen          func(part, passphrase, mapper string) error
	luksUUID          func(part string) string
	createZFSPool     func(pool, part, altroot string) error
	createZFSRoot     func(pool string) error
	formatRoot        func(dev, filesystem string) error
	mkdirAll          func(path string, perm os.FileMode) error
	mountZFSRoot      func(pool, altroot string) error
	setupBtrfsSubvols func(dev, target string) error
	mountType         func(dev, target, fstype, opts string) error
	mountBoot         func(target, part string) error
	mountEFI          func(target, part string) error
	prepareVarDisk    func(dev string) error
	mount             func(dev, target, opts string) error

	info           func(msg string)
	registerSecret func(s string)

	// Teardown registrations (cleanup and the swap registry).
	addMount   func(path string)
	setLUKS    func(mapper string)
	setZFSPool func(pool string)
	swapOn     func(dev string)
}

// liveDiskOps binds the disk phase to the real disk, LUKS and ZFS
// operations and to the install's cleanup and swap registry.
func liveDiskOps() diskOps {
	return diskOps{
		applyCustomLayout: disk.ApplyCustomLayoutWith,
		partitionLayout:   disk.PartitionLayout,
		formatEFI:         disk.FormatEFI,
		formatBoot:        disk.FormatBoot,
		mapperExists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		randomPassphrase:  luks.RandomPassphrase,
		luksClose:         luks.Close,
		luksFormat:        luks.Format,
		luksOpen:          luks.Open,
		luksUUID:          luks.UUID,
		createZFSPool:     disk.CreateZFSPool,
		createZFSRoot:     disk.CreateZFSRootDataset,
		formatRoot:        disk.FormatRoot,
		mkdirAll:          os.MkdirAll,
		mountZFSRoot:      disk.MountZFSRoot,
		setupBtrfsSubvols: disk.SetupBtrfsSubvolumes,
		mountType:         disk.MountType,
		mountBoot:         disk.MountBoot,
		mountEFI:          disk.MountEFI,
		prepareVarDisk:    disk.PrepareVarDisk,
		mount:             disk.Mount,
		info:              progress.Info,
		registerSecret:    progress.RegisterSecret,
		addMount:          cleanup.AddMount,
		setLUKS:           cleanup.SetLUKS,
		setZFSPool:        cleanup.SetZFSPool,
		swapOn:            swaps.Add,
	}
}

// diskPhase prepares the target for r: steps 1 to 5 (or "Preparing disk"
// for a manual layout) and the /var disk. targetMount and luksMapper are
// the resolved mount point and mapper name. The parameters are named r and
// seq on purpose: the step contract test (steps_test.go) replays this body
// as part of main() and decides each branch with r's methods.
func diskPhase(r *recipe.Recipe, seq *stepSequencer, targetMount, luksMapper string, ops diskOps) (diskResult, error) {
	var res diskResult

	if r.IsManualLayout() {
		// ── Step 1 (manual): Format and mount user-specified partitions ────────
		seq.begin(stepPrepareDisk)

		specs := make([]disk.MountSpec, 0, len(r.CustomMounts))
		for _, cm := range r.CustomMounts {
			specs = append(specs, disk.MountSpec{
				Partition: cm.Partition,
				Target:    cm.Target,
				Fstype:    cm.Fstype,
			})
		}

		// Each mount and the swap are registered for teardown the moment
		// they are acquired, not when ApplyCustomLayout returns: it can fail
		// on the second or third partition, or be cancelled half-way, with
		// the root already mounted (#362).
		var applyErr error
		res.targetMount, res.efiPart, _, applyErr = ops.applyCustomLayout(specs, targetMount,
			disk.LayoutHooks{Mounted: ops.addMount, SwapOn: ops.swapOn})
		if applyErr != nil {
			return res, phaseFail(progress.FailureManualLayoutFailed, "manual disk layout: %v", applyErr)
		}
	} else {
		// ── Step 1: Partition disk ────────────────────────────────────────────
		seq.begin(stepPartition)

		// The layout table (disk.LayoutPartitions) is the one `fisherman
		// plan` reports: ZFS and systemd-boot write two partitions (ESP +
		// root; LUKS, if any, wraps the root), GRUB2 writes three (ESP +
		// ext4 /boot + root), encrypted or not.
		layout := autoLayout(r)
		if err := ops.partitionLayout(r.Disk, layout); err != nil {
			return res, phaseFail(progress.FailurePartitionFailed, "partitioning disk: %v", err)
		}

		efiPart := disk.PartName(r.Disk, layout.PartNumber(disk.RoleEFI))
		rootPart := disk.PartName(r.Disk, layout.PartNumber(disk.RoleRoot))
		var bootPart string // only the GRUB2 layout has an ext4 /boot
		if n := layout.PartNumber(disk.RoleBoot); n > 0 {
			bootPart = disk.PartName(r.Disk, n)
		}
		rootDev := rootPart // may be replaced by /dev/mapper/fisherman-root if LUKS
		res.rootPart = rootPart
		res.bootPart = bootPart
		poolName := disk.PoolName(r.ZFSPoolName) // only used when r.IsZFS()

		// ── Step 2: Format EFI ───────────────────────────────────────────────
		seq.begin(stepFormatEFI)

		if err := ops.formatEFI(efiPart); err != nil {
			return res, phaseFail(progress.FailureFormatFailed, "formatting EFI: %v", err)
		}
		// grub2 installs need a separate ext4 /boot so GRUB never has to parse
		// XFS (GRUB's built-in XFS driver lacks support for modern XFS features).
		// systemd-boot reads the FAT32 ESP directly, so no separate /boot needed.
		if !r.UsesSystemdBoot() {
			if err := ops.formatBoot(bootPart); err != nil {
				return res, phaseFail(progress.FailureFormatFailed, "formatting /boot: %v", err)
			}
		}

		// ── Step 3: Disk encryption (optional) ──────────────────────────────
		if r.HasEncryption() {
			seq.begin(stepEncryption)

			var passphrase string
			switch r.Encryption.Type {
			case "luks-passphrase", "tpm2-luks-passphrase":
				passphrase = r.Encryption.Passphrase
			case "tpm2-luks":
				passphrase = ops.randomPassphrase()
				res.recoveryKey = passphrase // emitted later so user can write it down
				ops.registerSecret(passphrase)
				ops.info("TPM2-LUKS: generated random recovery passphrase; TPM2 will be enrolled after install")
			}

			// A previous interrupted run may have left the mapper open. Close it
			// before formatting so luksFormat and luksOpen succeed cleanly.
			if ops.mapperExists(luks.MapperPath(luksMapper)) {
				ops.info(fmt.Sprintf("Closing stale mapper %s from previous run", luksMapper))
				_ = ops.luksClose(luksMapper)
			}

			if err := ops.luksFormat(rootPart, passphrase); err != nil {
				return res, phaseFail(progress.FailureLUKSFailed, "LUKS format: %v", err)
			}
			if err := ops.luksOpen(rootPart, passphrase, luksMapper); err != nil {
				return res, phaseFail(progress.FailureLUKSFailed, "LUKS open: %v", err)
			}
			ops.setLUKS(luksMapper)
			rootDev = luks.MapperPath(luksMapper)
			res.luksUUID = ops.luksUUID(rootPart)
		}
		res.rootDev = rootDev

		// ── Step 4: Format root filesystem ──────────────────────────────────
		seq.begin(stepFormatRoot)

		if r.IsZFS() {
			if err := ops.createZFSPool(poolName, rootPart, targetMount); err != nil {
				return res, phaseFail(progress.FailureZFSFailed, "creating ZFS pool: %v", err)
			}
			// From here the pool is imported on this host. Teardown exports
			// it, on success and on every failure (#413).
			ops.setZFSPool(poolName)
			if err := ops.createZFSRoot(poolName); err != nil {
				return res, phaseFail(progress.FailureZFSFailed, "creating ZFS root dataset: %v", err)
			}
		} else {
			if err := ops.formatRoot(rootDev, r.Filesystem); err != nil {
				return res, phaseFail(progress.FailureFormatFailed, "formatting root filesystem: %v", err)
			}
		}

		// ── Step 5: Mount filesystem ─────────────────────────────────────────
		seq.begin(stepMount)

		if err := ops.mkdirAll(targetMount, 0o755); err != nil {
			return res, phaseFail(progress.FailureMountFailed, "creating mount point %s: %v", targetMount, err)
		}

		if r.IsZFS() {
			// Pool is already imported with -R altroot; root dataset is already
			// mounted. Ensure the mount point exists and call MountZFSRoot in
			// case the automount didn't fire (e.g. in a container environment).
			if err := ops.mountZFSRoot(poolName, targetMount); err != nil {
				return res, phaseFail(progress.FailureZFSFailed, "mounting ZFS root: %v", err)
			}
		} else if r.BtrfsSubvolumes {
			if err := ops.setupBtrfsSubvols(rootDev, targetMount); err != nil {
				return res, phaseFail(progress.FailureMountFailed, "setting up btrfs subvolumes: %v", err)
			}
		} else {
			mountOpts := ""
			if r.Filesystem == "btrfs" {
				mountOpts = "compress=zstd:1"
			}
			if err := ops.mountType(rootDev, targetMount, r.Filesystem, mountOpts); err != nil {
				return res, phaseFail(progress.FailureMountFailed, "mounting root: %v", err)
			}
		}
		ops.addMount(targetMount)

		// Mount the unencrypted /boot partition before the EFI partition.
		// bootupctl reads /boot's block device UUID from the raw partition.
		// Not needed for systemd-boot installs (no separate /boot partition).
		if !r.UsesSystemdBoot() {
			if err := ops.mountBoot(targetMount, bootPart); err != nil {
				return res, phaseFail(progress.FailureMountFailed, "mounting /boot: %v", err)
			}
			ops.addMount(targetMount + "/boot")
		}

		// Mount the EFI partition at /boot/efi inside the target.
		if err := ops.mountEFI(targetMount, efiPart); err != nil {
			return res, phaseFail(progress.FailureMountFailed, "mounting EFI: %v", err)
		}
		ops.addMount(targetMount + "/boot/efi")

		res.targetMount = targetMount
		res.efiPart = efiPart
	}

	// ── Step 5.5: Mount /var disk (optional) ─────────────────────────────────
	// Must happen before bootc install so bootc populates /var on the right disk.
	if r.HasVarDisk() {
		varDir := filepath.Join(res.targetMount, "var")
		if err := ops.mkdirAll(varDir, 0o755); err != nil {
			return res, phaseFail(progress.FailureMountFailed, "creating /var mount point: %v", err)
		}
		if r.FormatsVarDisk() {
			seq.begin(stepFormatVarDisk)
			// Release the disk (mounts, swap, LVM, old signatures), then
			// format it: see disk.PrepareVarDisk (#406).
			if err := ops.prepareVarDisk(r.VarDisk.Disk); err != nil {
				return res, phaseFail(progress.FailureFormatFailed, "formatting /var disk: %v", err)
			}
		} else {
			ops.info(fmt.Sprintf("Keeping existing data on /var disk %s", r.VarDisk.Disk))
		}
		if err := ops.mount(r.VarDisk.Disk, varDir, ""); err != nil {
			return res, phaseFail(progress.FailureMountFailed, "mounting /var disk: %v", err)
		}
		ops.addMount(varDir)
		ops.info(fmt.Sprintf("Mounted /var disk %s at /var", r.VarDisk.Disk))
	}

	return res, nil
}
