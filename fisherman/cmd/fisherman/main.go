package main

import (
	"fmt"
	"os"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

const (
	targetMount = "/mnt/fisherman-target"
	luksMapper  = "fisherman-root"
)

// cleanup is global so fatal() can tear everything down on any error path.
var cleanup = &post.Cleanup{}

type stepProfile struct {
	cumulativePct int
	weightPct     int
}

// buildProfile returns per-step weight profiles based on timing data from a
// yellowfin gnome-hwe loop-device install (264s uncached, ~111s cached).
// Weights sum to 100. cumulativePct is the bar position at step start.
func buildProfile(needsPull, hasLUKS, hasTPM2enrolment bool) []stepProfile {
	osWeight := 87
	flatpakWeight := 11
	if !needsPull {
		osWeight = 68
		flatpakWeight = 29
	}
	if hasLUKS {
		osWeight--
	}
	if hasTPM2enrolment {
		osWeight--
	}

	weights := []int{0, 1} // partition, format EFI
	if hasLUKS {
		weights = append(weights, 1) // LUKS setup
	}
	weights = append(weights, 0, 0)     // format root, mount
	weights = append(weights, osWeight) // install OS
	if hasTPM2enrolment {
		weights = append(weights, 1) // TPM2 enrolment
	}
	weights = append(weights, flatpakWeight, 0) // flatpaks, configure
	sum := 0
	for _, w := range weights {
		sum += w
	}
	weights = append(weights, 100-sum) // finalize

	profile := make([]stepProfile, len(weights))
	cumulative := 0
	for i, w := range weights {
		profile[i] = stepProfile{cumulative, w}
		cumulative += w
	}
	return profile
}

func fatal(format string, args ...any) {
	cleanup.Run()
	fmt.Fprintf(os.Stderr, "fisherman: fatal: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: fisherman <recipe.json>\n")
		fmt.Fprintf(os.Stderr, "       fisherman images [--file <path>] [--plain]\n")
		os.Exit(1)
	}

	if os.Args[1] == "images" {
		runImages(os.Args[2:])
		return
	}

	r, err := recipe.Load(os.Args[1])
	if err != nil {
		fatal("loading recipe: %v", err)
	}
	if err := r.Validate(); err != nil {
		fatal("invalid recipe: %v", err)
	}

	hasEncryption := r.Encryption.Type != "" && r.Encryption.Type != "none"
	hasTPM2 := r.Encryption.Type == "tpm2-luks" || r.Encryption.Type == "tpm2-luks-passphrase"
	hasTPM2enrolment := r.Encryption.Type == "tpm2-luks-passphrase"
	isManual := len(r.CustomMounts) > 0
	isSystemdBoot := r.Bootloader == "systemd"

	// ── Pre-flight: check image cache ─────────────────────────────────────────
	var imageCheck install.ImageCheck
	if r.Image != "" {
		progress.Info("Checking image cache...")
		imageCheck = install.CheckImage(r.Image)
		if imageCheck.NeedsPull {
			progress.Info(fmt.Sprintf("Image pull required (%d layers)", imageCheck.LayerCount))
		} else if imageCheck.Offline {
			progress.Info("Offline: registry unreachable, using locally cached image")
		} else {
			progress.Info("Image already up to date in local cache")
		}
	}

	// ── systemd-boot path (to-disk) ───────────────────────────────────────────
	// For systemd-boot images (e.g. Project Bluefin/Dakota), we use
	// `bootc install to-disk --via-loopback` which handles partitioning,
	// formatting, OS deployment, and bootloader installation in one shot.
	// --via-loopback causes bootc to auto-enable --generic-image, which
	// bypasses the bootupd presence check (required because images like Dakota
	// don't ship bootupd). It also ensures partition nodes are visible inside
	// the container via --partscan regardless of device type.
	if isSystemdBoot {
		scratchDir := "/var/fisherman-tmp"
		if err := os.MkdirAll(scratchDir, 0o1777); err != nil {
			fatal("creating scratch dir: %v", err)
		}
		if err := disk.BindMount(scratchDir, "/var/tmp"); err != nil {
			fatal("bind-mounting scratch dir at /var/tmp: %v", err)
		}
		cleanup.AddMount("/var/tmp")
		defer os.RemoveAll(scratchDir)

		osWeight := 87
		flatpakWeight := 11
		if !imageCheck.NeedsPull {
			osWeight = 68
			flatpakWeight = 29
		}
		const sdTotalSteps = 4

		// Step 1: Install OS
		progress.Step(1, sdTotalSteps, "Installing OS", 0, osWeight)

		targetImgref := r.TargetImgref
		if targetImgref == r.Image {
			targetImgref = ""
		}
		effectiveDisk, err := install.BootcToDisk(install.Options{
			SourceImgref:     r.Image,
			TargetImgref:     targetImgref,
			ComposeFsBackend: r.ComposeFsBackend,
			NeedsPull:        imageCheck.NeedsPull,
			LayerCount:       imageCheck.LayerCount,
		}, r.Disk, r.Filesystem)
		if err != nil {
			fatal("bootc install to-disk: %v", err)
		}

		// bootc creates a 3-partition layout: p1=BIOS boot, p2=EFI, p3=root.
		// effectiveDisk may differ from r.Disk when a raw file was auto-loop-attached.
		efiPart, rootPart, err := disk.FindSystemdBootPartitions(effectiveDisk)
		if err != nil {
			fatal("finding partitions after to-disk: %v", err)
		}

		if err := os.MkdirAll(targetMount, 0o755); err != nil {
			fatal("creating mount point %s: %v", targetMount, err)
		}
		if err := disk.Mount(rootPart, targetMount, ""); err != nil {
			fatal("mounting root for post-install: %v", err)
		}
		cleanup.AddMount(targetMount)

		// Step 2: Copy system Flatpaks
		progress.Step(2, sdTotalSteps, "Copying system Flatpaks", osWeight, flatpakWeight)
		if err := post.CopyFlatpaks(targetMount, r.Flatpaks); err != nil {
			progress.Info(fmt.Sprintf("Warning: could not copy flatpaks: %v", err))
		}

		// Step 3: Post-install configuration
		progress.Step(3, sdTotalSteps, "Configuring installed system", osWeight+flatpakWeight, 0)
		progress.Info(fmt.Sprintf("Writing hostname: %s", r.Hostname))
		if err := post.WriteHostname(targetMount, r.Hostname); err != nil {
			fatal("writing hostname: %v", err)
		}
		if r.User.Username != "" {
			progress.Info(fmt.Sprintf("Creating user: %s", r.User.Username))
			if err := post.CreateUser(targetMount, post.UserConfig{
				Username: r.User.Username,
				Fullname: r.User.Fullname,
				Password: r.User.Password,
				Groups:   r.User.Groups,
			}); err != nil {
				fatal("creating user: %v", err)
			}
		}
		n, err := post.EnsurePlymouthArgs(targetMount)
		if err != nil {
			progress.Info(fmt.Sprintf("Warning: could not set Plymouth kernel args: %v", err))
		} else if n > 0 {
			progress.Info(fmt.Sprintf("Added Plymouth boot args to %d loader entr%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]))
		}

		// Step 4: Finalize
		progress.Step(4, sdTotalSteps, "Finalizing installation", 99, 1)
		if err := disk.FinalizeFilesystem(targetMount); err != nil {
			fatal("finalizing target filesystem: %v", err)
		}

		cleanup.Run()

		bootID, err := post.FindBootNextID(efiPart)
		if err != nil {
			progress.Info(fmt.Sprintf("Warning: could not determine EFI boot entry: %v", err))
			bootID = ""
		} else if bootID != "" {
			progress.Info(fmt.Sprintf("EFI boot entry for installed system: Boot%s", bootID))
		}
		progress.Complete("Installation complete!", bootID)
		return
	}

	profile := buildProfile(imageCheck.NeedsPull, hasEncryption, hasTPM2enrolment)
	pi := 0 // profile index, incremented at each progress.Step call

	// Compute total step count up front so the GUI can show accurate progress.
	// Manual layouts collapse the 4 auto disk-setup steps into a single step.
	totalSteps := 8
	if isManual {
		totalSteps -= 3 // partition + format EFI + format root collapse into one step
	}
	if hasEncryption && !isManual {
		totalSteps++ // extra step for LUKS setup (auto mode only)
	}
	if hasTPM2 && r.Encryption.Type == "tpm2-luks-passphrase" {
		totalSteps++ // extra step for TPM2 enrolment
	}
	step := 1

	var activeTargetMount string
	var activeEfiPart string
	var activeRootPart string // only used for TPM2 enrolment, empty in manual mode

	if isManual {
		// ── Step 1 (manual): Format and mount user-specified partitions ────────
		progress.Step(step, totalSteps, "Preparing disk", profile[pi].cumulativePct, profile[pi].weightPct)
		pi++
		step++

		specs := make([]disk.MountSpec, 0, len(r.CustomMounts))
		for _, cm := range r.CustomMounts {
			specs = append(specs, disk.MountSpec{
				Partition: cm.Partition,
				Target:    cm.Target,
				Fstype:    cm.Fstype,
			})
		}

		var mountedPaths []string
		var applyErr error
		activeTargetMount, activeEfiPart, mountedPaths, applyErr = disk.ApplyCustomLayout(specs, targetMount)
		if applyErr != nil {
			fatal("manual disk layout: %v", applyErr)
		}
		for _, p := range mountedPaths {
			cleanup.AddMount(p)
		}
	} else {
		// ── Step 1: Partition disk ────────────────────────────────────────────
		progress.Step(step, totalSteps, "Partitioning disk", profile[pi].cumulativePct, profile[pi].weightPct)
		pi++
		step++

		if hasEncryption {
			if err := disk.PartitionEncrypted(r.Disk); err != nil {
				fatal("partitioning disk: %v", err)
			}
		} else {
			if err := disk.Partition(r.Disk); err != nil {
				fatal("partitioning disk: %v", err)
			}
		}

		var efiPart, bootPart, rootPart string
		// 3-partition: p1=EFI, p2=/boot, p3=root.
		efiPart = disk.PartName(r.Disk, 1)
		bootPart = disk.PartName(r.Disk, 2)
		rootPart = disk.PartName(r.Disk, 3)
		rootDev := rootPart // may be replaced by /dev/mapper/fisherman-root if LUKS
		activeRootPart = rootPart

		// ── Step 2: Format EFI ───────────────────────────────────────────────
		progress.Step(step, totalSteps, "Formatting EFI partition", profile[pi].cumulativePct, profile[pi].weightPct)
		pi++
		step++

		if err := disk.FormatEFI(efiPart); err != nil {
			fatal("formatting EFI: %v", err)
		}
		// grub2 installs need a separate ext4 /boot so GRUB never has to parse
		// XFS (GRUB's built-in XFS driver lacks support for modern XFS features).
		if err := disk.FormatBoot(bootPart); err != nil {
			fatal("formatting /boot: %v", err)
		}

		// ── Step 3: Disk encryption (optional) ──────────────────────────────
		if hasEncryption {
			progress.Step(step, totalSteps, "Setting up disk encryption", profile[pi].cumulativePct, profile[pi].weightPct)
			pi++
			step++

			var passphrase string
			switch r.Encryption.Type {
			case "luks-passphrase", "tpm2-luks-passphrase":
				passphrase = r.Encryption.Passphrase
			case "tpm2-luks":
				passphrase = luks.RandomPassphrase()
				progress.Info("TPM2-LUKS: using a temporary passphrase; TPM2 will be enrolled after install")
			}

			// A previous interrupted run may have left the mapper open. Close it
			// before formatting so luksFormat and luksOpen succeed cleanly.
			if _, err := os.Stat(luks.MapperPath(luksMapper)); err == nil {
				progress.Info(fmt.Sprintf("Closing stale mapper %s from previous run", luksMapper))
				_ = luks.Close(luksMapper)
			}

			if err := luks.Format(rootPart, passphrase); err != nil {
				fatal("LUKS format: %v", err)
			}
			if err := luks.Open(rootPart, passphrase, luksMapper); err != nil {
				fatal("LUKS open: %v", err)
			}
			cleanup.SetLUKS(luksMapper)
			rootDev = luks.MapperPath(luksMapper)
		}

		// ── Step 4: Format root filesystem ──────────────────────────────────
		progress.Step(step, totalSteps, "Formatting root filesystem", profile[pi].cumulativePct, profile[pi].weightPct)
		pi++
		step++

		if err := disk.FormatRoot(rootDev, r.Filesystem); err != nil {
			fatal("formatting root filesystem: %v", err)
		}

		// ── Step 5: Mount filesystem ─────────────────────────────────────────
		progress.Step(step, totalSteps, "Mounting filesystem", profile[pi].cumulativePct, profile[pi].weightPct)
		pi++
		step++

		if err := os.MkdirAll(targetMount, 0o755); err != nil {
			fatal("creating mount point %s: %v", targetMount, err)
		}

		if r.BtrfsSubvolumes {
			if err := disk.SetupBtrfsSubvolumes(rootDev, targetMount); err != nil {
				fatal("setting up btrfs subvolumes: %v", err)
			}
		} else {
			if err := disk.Mount(rootDev, targetMount, ""); err != nil {
				fatal("mounting root: %v", err)
			}
		}
		cleanup.AddMount(targetMount)

		// Mount the unencrypted /boot partition before the EFI partition.
		// bootupctl reads /boot's block device UUID from the raw partition.
		if err := disk.MountBoot(targetMount, bootPart); err != nil {
			fatal("mounting /boot: %v", err)
		}
		cleanup.AddMount(targetMount + "/boot")

		// Mount the EFI partition at /boot/efi inside the target.
		if err := disk.MountEFI(targetMount, efiPart); err != nil {
			fatal("mounting EFI: %v", err)
		}
		cleanup.AddMount(targetMount + "/boot/efi")

		activeTargetMount = targetMount
		activeEfiPart = efiPart
	}

	// Bind-mount a host-side scratch directory at /var/tmp so bootc has
	// disk-backed space for layer blobs. We deliberately use a path OUTSIDE
	// the target tree so bootc's "empty rootfs" check doesn't find stray
	// directories inside /mnt/fisherman-target.
	// Use /var/fisherman-tmp (not /run/fisherman-tmp): /var is always
	// disk-backed on ostree and conventional systems, whereas /run is a
	// tmpfs sized at ~50% of RAM — too small for large images (e.g. 3.7 GB).
	scratchDir := "/var/fisherman-tmp"
	if err := os.MkdirAll(scratchDir, 0o1777); err != nil {
		fatal("creating scratch dir: %v", err)
	}
	if err := disk.BindMount(scratchDir, "/var/tmp"); err != nil {
		fatal("bind-mounting scratch dir at /var/tmp: %v", err)
	}
	cleanup.AddMount("/var/tmp")
	defer os.RemoveAll(scratchDir)

	// ── Step 6: Install OS ────────────────────────────────────────────────────
	progress.Step(step, totalSteps, "Installing OS", profile[pi].cumulativePct, profile[pi].weightPct)
	pi++
	step++

	// Only pass --target-imgref when it is non-empty and differs from the source.
	targetImgref := r.TargetImgref
	if targetImgref == r.Image {
		targetImgref = ""
	}

	if err := install.BootcInstall(install.Options{
		SourceImgref:     r.Image,
		TargetImgref:     targetImgref,
		SelinuxDisabled:  r.SelinuxDisabled,
		UnifiedStorage:   r.UnifiedStorage,
		ComposeFsBackend: r.ComposeFsBackend,
		Bootloader:       r.Bootloader,
		Target:           activeTargetMount,
		NeedsPull:        imageCheck.NeedsPull,
		LayerCount:       imageCheck.LayerCount,
	}); err != nil {
		fatal("bootc install: %v", err)
	}

	// For systemd-boot installs, if --generic-image skipped the EFI binary
	// installation, install it manually from the ostree deployment.
	if isSystemdBoot {
		progress.Info("Installing systemd-boot EFI binary")
		if err := install.InstallSystemdBoot(activeTargetMount); err != nil {
			// Non-fatal: --generic-image may have already installed it.
			progress.Info(fmt.Sprintf("Warning: systemd-boot EFI install: %v", err))
		}
	}

	// ── TPM2 enrolment (tpm2-luks-passphrase only) ────────────────────────────
	// For plain tpm2-luks the random passphrase is ephemeral; no enrolment step.
	// For tpm2-luks-passphrase the user's passphrase unlocks LUKS; we add a
	// TPM2 token on top so the system auto-unlocks, with the password as fallback.
	if r.Encryption.Type == "tpm2-luks-passphrase" {
		progress.Step(step, totalSteps, "Enrolling TPM2 auto-unlock", profile[pi].cumulativePct, profile[pi].weightPct)
		pi++
		step++

		if err := luks.EnrollTPM2(activeRootPart, r.Encryption.Passphrase); err != nil {
			// Non-fatal: TPM2 hardware may not be present (e.g. VMs).
			progress.Info(fmt.Sprintf("Warning: TPM2 enrolment failed (password unlock still works): %v", err))
		}
	}

	// ── Step 7: Copy system flatpaks ──────────────────────────────────────────
	progress.Step(step, totalSteps, "Copying system Flatpaks", profile[pi].cumulativePct, profile[pi].weightPct)
	pi++
	step++

	if err := post.CopyFlatpaks(activeTargetMount, r.Flatpaks); err != nil {
		// Non-fatal — the system will work without pre-installed flatpaks.
		progress.Info(fmt.Sprintf("Warning: could not copy flatpaks: %v", err))
	}

	// ── Step 8: Post-install configuration ───────────────────────────────────
	progress.Step(step, totalSteps, "Configuring installed system", profile[pi].cumulativePct, profile[pi].weightPct)
	pi++
	step++

	progress.Info(fmt.Sprintf("Writing hostname: %s", r.Hostname))
	if err := post.WriteHostname(activeTargetMount, r.Hostname); err != nil {
		fatal("writing hostname: %v", err)
	}

	// Create a user account if the recipe requests one (e.g. Bazzite has no OOBE).
	if r.User.Username != "" {
		progress.Info(fmt.Sprintf("Creating user: %s", r.User.Username))
		if err := post.CreateUser(activeTargetMount, post.UserConfig{
			Username: r.User.Username,
			Fullname: r.User.Fullname,
			Password: r.User.Password,
			Groups:   r.User.Groups,
		}); err != nil {
			fatal("creating user: %v", err)
		}
	}

	// Ensure rhgb and quiet are in every BLS loader entry so Plymouth shows
	// the graphical boot splash. Non-fatal: the system boots fine without it.
	n, err := post.EnsurePlymouthArgs(activeTargetMount)
	if err != nil {
		progress.Info(fmt.Sprintf("Warning: could not set Plymouth kernel args: %v", err))
	} else if n > 0 {
		progress.Info(fmt.Sprintf("Added Plymouth boot args to %d loader entr%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]))
	}

	// ── Step 9: Finalize ─────────────────────────────────────────────────────
	// bootc's --skip-finalize kept the target writable for post-install writes.
	// Now replicate what bootc's finalize_filesystem() does internally:
	//   1. fstrim  — discard unused blocks (SSD optimization)
	//   2. remount ro — flush writeback, lock the deployment read-only
	//   3. fsfreeze/thaw — flush the journal for a clean first boot
	progress.Step(step, totalSteps, "Finalizing installation", profile[pi].cumulativePct, profile[pi].weightPct)
	if err := disk.FinalizeFilesystem(activeTargetMount); err != nil {
		fatal("finalizing target filesystem: %v", err)
	}

	// Tear down mounts and LUKS before declaring success.
	cleanup.Run()

	// Find the EFI boot entry so the frontend can set BootNext before rebooting.
	// Non-fatal: on VMs or systems without efibootmgr this may return empty.
	bootID, err := post.FindBootNextID(activeEfiPart)
	if err != nil {
		progress.Info(fmt.Sprintf("Warning: could not determine EFI boot entry: %v", err))
		bootID = ""
	} else if bootID != "" {
		progress.Info(fmt.Sprintf("EFI boot entry for installed system: Boot%s", bootID))
	}

	progress.Complete("Installation complete!", bootID)
}
