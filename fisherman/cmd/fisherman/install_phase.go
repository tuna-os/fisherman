package main

import (
	"fmt"
	"os"
	"time"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/reuse"
)

// The install phase of an install (#313): from the mounted target to an
// installed image that boots. The scratch directory bootc pulls into, the
// target image reference, bootc install itself (step 6), the check of a
// reused source, the systemd-boot and GRUB fallback loaders, and the GPT
// root retag of an unencrypted systemd-boot composefs install.
//
// Like diskPhase it takes what it needs as arguments, returns what the
// later steps need (installResult), and never ends the process: a failure
// comes back as a *phaseError with the code and message main() used to
// pass to fatalCode (or the problem it passed to fatalProblem).

// installInput is what the install phase reads from the pre-flight and
// the disk phase.
type installInput struct {
	// disk is the disk phase's result: the mounted target, the ESP, the
	// root partition and the LUKS UUID.
	disk diskResult
	// sourceImgref is the resolved install source (media input or reuse
	// copy applied).
	sourceImgref string
	mediaInput   *install.MediaInput
	reused       *reuse.Result
	imageStores  []string
	imageCheck   install.ImageCheck
	// selinuxShim and sourceRoot are what the pre-flight prepared.
	selinuxShim string
	sourceRoot  install.SourceRoot
}

// installResult is what the install phase hands to the rest of the install.
type installResult struct {
	// scratchDir is bootc's scratch space; liveISO says it is on the
	// target disk (live media) rather than /var/fisherman-tmp on the host.
	scratchDir string
	liveISO    bool
	// targetImgref is the --target-imgref the install passed ("" for
	// none); the receipt names it.
	targetImgref string
}

// installOps are the operations the install phase performs and the
// teardown registration it makes. liveInstallOps is the real set.
type installOps struct {
	spaceConstrained   func() bool
	prepareScratch     func(target string, liveISO bool) (string, error)
	addPostRemoval     func(path string)
	bootcInstall       func(opts install.Options) error
	checkReused        func(target string, res *reuse.Result) (string, *recipe.Problem)
	ensureSystemdBoot  func(target string) error
	ensureGrubFallback func(target, distro string) error
	partTypeRewrite    func(dev string, n int, want string) (bool, string)
	unmountPartition   func(dev string, n int) error
	setPartitionType   func(dev string, n int, typ string) error
	mkdirAll           func(path string, perm os.FileMode) error
	waitForNode        func(part string, timeout time.Duration) error
	mountWithRetry     func(dev, target, opts string, attempts int, backoff time.Duration) error
	mountEFI           func(target, part string) error

	info func(msg string)
	// stderr writes a line of the diagnostic log (not a progress event).
	stderr func(msg string)
}

// liveInstallOps binds the install phase to the real operations. bootc and
// the scratch directory are read through their package-level seams at the
// call, so a test that replaces them still sees the replacement.
func liveInstallOps() installOps {
	return installOps{
		spaceConstrained:  func() bool { return varIsSpaceConstrained() },
		prepareScratch:    prepareScratchDir,
		addPostRemoval:    cleanup.AddPostRemoval,
		bootcInstall:      func(opts install.Options) error { return bootcInstall(opts) },
		checkReused:       checkReusedInstall,
		ensureSystemdBoot: install.InstallSystemdBoot,
		ensureGrubFallback: func(target, distro string) error {
			return install.InstallGrubFallback(target, distro)
		},
		partTypeRewrite:  disk.PartitionTypeNeedsRewrite,
		unmountPartition: disk.UnmountPartition,
		setPartitionType: disk.SetPartitionType,
		mkdirAll:         os.MkdirAll,
		waitForNode:      disk.WaitForPartitionNode,
		mountWithRetry:   disk.MountWithRetry,
		mountEFI:         disk.MountEFI,
		info:             progress.Info,
		stderr:           func(msg string) { fmt.Fprint(os.Stderr, msg) },
	}
}

// installPhase installs the image onto the target the disk phase mounted:
// the scratch directory, then step 6 and what bootc leaves to do. The
// parameters are named r and seq for the step contract test
// (steps_test.go), which replays this body as part of main().
//
// The caller removes a host scratch directory (!liveISO) when the install
// returns, as main() always did with a defer; os.Exit skips that, and the
// cleanup registration made here covers it.
func installPhase(r *recipe.Recipe, seq *stepSequencer, in installInput, ops installOps) (installResult, error) {
	var res installResult
	activeTargetMount := in.disk.targetMount

	// Bind-mount a host-side scratch directory at /var/tmp so bootc has
	// disk-backed space for layer blobs. We deliberately use a path OUTSIDE
	// the target tree so bootc's "empty rootfs" check doesn't find stray
	// directories inside /mnt/fisherman-target.
	//
	// On installed (ostree/conventional) systems /var is always disk-backed,
	// so /var/fisherman-tmp gives us plenty of space. On live ISOs, however,
	// /var lives on a tmpfs that is far too small for multi-gigabyte image
	// blobs. Detect that case and place scratch on the already-formatted
	// target disk instead. A self-bind mount makes bootc's empty-rootdir
	// check see a mount point (which it tolerates) rather than a plain
	// directory (which it rejects).
	liveISO := ops.spaceConstrained() && activeTargetMount != ""
	scratchDir, err := ops.prepareScratch(activeTargetMount, liveISO)
	if err != nil {
		return res, phaseFail(progress.FailureScratchFailed, "preparing scratch dir: %v", err)
	}
	res.scratchDir, res.liveISO = scratchDir, liveISO
	// The bootc container receives scratch at /var/tmp. Its launcher also
	// redirects host-side OCI staging while podman creates the container:
	// that import runs before the container's scratch bind mount exists.
	//
	// On live media scratchDir is on the target disk: prepareScratchDir
	// registered it with cleanup.AddTargetScratch, which removes it while the
	// target is still mounted (before finalize on success, first thing in
	// teardown otherwise). Elsewhere it is /var/fisherman-tmp on the host,
	// removed by the caller when the install returns AND via
	// cleanup.AddPostRemoval so it also goes on fatal() paths (os.Exit
	// bypasses defers).
	if !liveISO {
		ops.addPostRemoval(scratchDir)
	}

	// ── Step 6: Install OS ────────────────────────────────────────────────────
	seq.begin(stepInstallOS)

	// Only pass --target-imgref when it is non-empty and differs from the source.
	targetImgref := r.TargetImgref
	if targetImgref == r.Image {
		targetImgref = ""
	}
	if in.mediaInput != nil {
		// A bound media input is the install source; the installed system
		// tracks the media's day-2 ref (or the canonical image ref when the
		// media names none), never the on-disk layout path.
		if targetImgref == "" {
			targetImgref = in.mediaInput.TargetImgref
		}
		if targetImgref == "" {
			targetImgref = r.Image
			if targetImgref == "" {
				targetImgref = in.mediaInput.Image
			}
		}
		if targetImgref == in.sourceImgref {
			targetImgref = ""
		}
	}
	res.targetImgref = targetImgref

	opts := buildBootcInstallOptions(r, in.sourceImgref, targetImgref, activeTargetMount, scratchDir, in.imageStores, in.imageCheck.NeedsPull, in.imageCheck.LayerCount)
	if in.reused != nil {
		reuseInstallOptions(&opts)
	}
	opts.SelinuxShimPath = in.selinuxShim
	opts.SourceRoot = in.sourceRoot
	if r.EncryptsRoot() {
		// Without the UUID no loader entry can name the LUKS container.
		// Stop before the image is deployed, not after "complete" (#410).
		if in.disk.luksUUID == "" {
			return res, phaseFail(progress.FailureLUKSFailed, "LUKS: could not read the UUID of the encrypted root partition %s", in.disk.rootPart)
		}
		// Where nothing else finds the root, bootc writes the argument
		// into the entries it creates, the way rootflags gets there.
		if luksNeedsKarg(r.UsesSystemdBoot(), r.ComposeFsBackend) {
			opts.KernelArguments = append(opts.KernelArguments, post.LuksKarg(in.disk.luksUUID))
		}
	}
	if err := ops.bootcInstall(opts); err != nil {
		if hint := layoutHint(r, err); hint != "" {
			return res, phaseFail(bootcFailureCode(err), "bootc install: %v\n  hint: %s", err, hint)
		}
		return res, phaseFail(bootcFailureCode(err), "bootc install: %v", err)
	}
	if in.reused != nil {
		msg, p := ops.checkReused(activeTargetMount, in.reused)
		if p != nil {
			return res, phaseProblem("source reuse", *p)
		}
		ops.info(msg)
	}

	// Ensure BOOTX64.EFI is on the ESP before anything touches the mount stack.
	// Newer bootctl (e.g. arch-bootc systemd ≥ v255) enables --graceful when
	// running in a container and silently skips writing to the ESP. Copying
	// directly from the ostree deployment is a reliable fallback and a no-op
	// when bootctl ran correctly (EFI/BOOT/BOOTX64.EFI already present).
	//
	// This used to be reached only on the unencrypted branch below, so an
	// encrypted systemd-boot install whose bootctl skipped the ESP got no
	// bootloader at all. Nothing about that bootctl behaviour depends on
	// encryption, and the call is idempotent and warning-only, so it runs for
	// every systemd-boot composefs install.
	if !r.IsManualLayout() && r.UsesSystemdBoot() && r.ComposeFsBackend {
		if err := ops.ensureSystemdBoot(activeTargetMount); err != nil {
			ops.info(fmt.Sprintf("Warning: could not ensure systemd-boot EFI binary: %v", err))
		}
	}

	// GRUB installs: bootupctl writes EFI/<vendor>/ and an NVRAM entry but
	// can leave no fallback loader at EFI/BOOT/BOOTX64.EFI, so a reset NVRAM
	// or a disk moved to another machine falls through to PXE (#233). Fill
	// that gap from the vendor directory. Never on a manual layout: that ESP
	// may be shared, and its EFI/BOOT belongs to whoever set it up.
	if !r.IsManualLayout() && !r.UsesSystemdBoot() {
		if err := ops.ensureGrubFallback(activeTargetMount, r.DistroID); err != nil {
			ops.info(fmt.Sprintf("Warning: could not ensure the UEFI fallback loader: %v", err))
		}
	}

	// systemd-boot composefs installs rely on GPT auto-discovery for the root
	// filesystem, so the root partition must carry the architecture's Linux
	// root GUID. disk.PartitionSystemdBoot now writes that type when it creates
	// the table, which is what makes the encrypted layout boot (#219) — behind
	// a LUKS mapping the raw partition is not what is mounted, so the
	// unmount/rewrite/remount dance below cannot run there at all.
	//
	// It stays here for the unencrypted layout as a belt-and-braces pass: a
	// disk reused from an earlier install can reach this point with a stale
	// type when bootc rewrites the table itself.
	//
	// The type is read first, while everything is still mounted (reading the
	// table is harmless), and the whole unmount/rewrite/remount sequence is
	// skipped when it already matches — the normal case, since
	// PartitionSystemdBoot wrote it. An sfdisk write makes the kernel and udev
	// drop and re-add the partition nodes, and remounting straight after it
	// raced that re-add ("mount /dev/loop0p2 ...: exit status 32", dakota CI).
	// A failed read falls through to the write, as before.
	retagRoot := false
	if !r.IsManualLayout() && r.UsesSystemdBoot() && !r.HasEncryption() && r.ComposeFsBackend {
		var curType string
		retagRoot, curType = ops.partTypeRewrite(r.Disk, 2, disk.LinuxRootPartType())
		if !retagRoot {
			ops.info(fmt.Sprintf("Root partition already tagged for systemd GPT auto-discovery (%s)", curType))
		}
	}
	if retagRoot {
		ops.info("Retagging root partition for systemd GPT auto-discovery")

		// Unmount the EFI partition explicitly so the FAT32 state is flushed to
		// the page cache before the root lazy-unmount below orphans the submount.
		if err := ops.unmountPartition(r.Disk, 1); err != nil {
			ops.stderr(fmt.Sprintf("warning: could not unmount EFI partition before retag: %v\n", err))
		}

		// Release kernel and userspace references to the root partition before
		// modifying its GPT type. bootc install may have left active references.
		if err := ops.unmountPartition(r.Disk, 2); err != nil {
			ops.stderr(fmt.Sprintf("warning: could not fully clean partition references: %v\n", err))
		}
		if err := ops.setPartitionType(r.Disk, 2, disk.LinuxRootPartType()); err != nil {
			return res, phaseFail(progress.FailureRetagFailed, "retagging root partition: %v", err)
		}
		// Remount root so finalization and post-install writes can proceed.
		// udisksctl unmount (used by UnmountPartition above) removes the
		// mountpoint directory it manages, and disk.Mount — unlike
		// MountTmpfs/BindMount — does not create its target, so recreate it
		// or the plain `mount` syscall below fails with ENOENT.
		if err := ops.mkdirAll(activeTargetMount, 0o755); err != nil {
			return res, phaseFail(progress.FailureRetagFailed, "recreating target mountpoint before remount: %v", err)
		}
		// The sfdisk write makes the kernel re-read the table, so the node
		// can be missing or mid-re-add here: settle, wait (bounded) for it to
		// be a block device again, and retry the mount briefly. Either still
		// failing is fatal.
		rootPart := disk.PartName(r.Disk, 2)
		if err := ops.waitForNode(rootPart, disk.DefaultPartitionNodeTimeout); err != nil {
			return res, phaseFail(progress.FailureRetagFailed, "remounting root partition after retagging: %v", err)
		}
		// With the same options as the first mount (and as the installed
		// system's rootflags): on btrfs a bare mount is the top-level
		// subvolume, not @, and has no compression. Every later step would
		// then look for the deployment in the wrong tree (#416).
		if err := ops.mountWithRetry(rootPart, activeTargetMount, r.RootMountOptions(), disk.DefaultMountAttempts, disk.DefaultMountBackoff); err != nil {
			return res, phaseFail(progress.FailureRetagFailed, "remounting root partition after retagging: %v", err)
		}
		// Remount EFI so that Plymouth/LUKS arg writes land on the real ESP
		// instead of the empty /boot/efi directory in the XFS root.
		if err := ops.mountEFI(activeTargetMount, in.disk.efiPart); err != nil {
			ops.stderr(fmt.Sprintf("warning: could not remount EFI partition after retag: %v\n", err))
		}
	}

	return res, nil
}
