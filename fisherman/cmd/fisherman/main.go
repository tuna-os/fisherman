package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

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

// lookPath is exec.LookPath by default; replaced in tests.
var lookPath = exec.LookPath

// isTmpfs reports whether the given path is on a tmpfs filesystem.
// Used to detect live-ISO environments where /var is RAM-backed and
// too small for multi-gigabyte scratch I/O.
func isTmpfs(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	const tmpfsMagic = 0x01021994
	return st.Type == tmpfsMagic
}

// expandPath prepends standard sbin directories and any tools staged alongside
// this binary to PATH.  pkexec strips the user's PATH to a minimal safe set
// that omits /usr/sbin and /sbin on many immutable distros (e.g. GnomeOS).
func expandPath() {
	current := os.Getenv("PATH")
	// Build candidate prefix: staged tools dir (sibling of this binary) first,
	// then the standard sbin locations that pkexec commonly strips.
	prefix := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if exe, err := os.Executable(); err == nil {
		toolsDir := filepath.Join(filepath.Dir(exe), "tools")
		if info, err := os.Stat(toolsDir); err == nil && info.IsDir() {
			prefix = toolsDir + ":" + prefix
		}
	}
	if current != "" {
		prefix = prefix + ":" + current
	}
	os.Setenv("PATH", prefix)
}

// checkRequiredTools verifies that every host binary needed by this recipe
// is reachable on PATH before we touch any disks, and returns a clear error
// naming the missing tool and the package that provides it.
func checkRequiredTools(r *recipe.Recipe) error {
	type requirement struct {
		tool string
		pkg  string
		when bool
	}
	reqs := []requirement{
		{"sfdisk", "util-linux", true},
		{"mkfs.fat", "dosfstools", true},
		{"mkfs.ext4", "e2fsprogs", true},
		{"mkfs.xfs", "xfsprogs", r.Filesystem == "xfs"},
		{"mkfs.btrfs", "btrfs-progs", r.Filesystem == "btrfs"},
		{"cryptsetup", "cryptsetup", r.Encryption.Type != "" && r.Encryption.Type != "none"},
		{"skopeo", "skopeo", true},
		{"podman", "podman", true},
	}
	for _, req := range reqs {
		if !req.when {
			continue
		}
		if _, err := lookPath(req.tool); err != nil {
			return fmt.Errorf("%q not found in PATH — install the %q package on the host", req.tool, req.pkg)
		}
	}
	return nil
}

var version = "dev"

func printHelp() {
	fmt.Printf(`fisherman — bootc disk installer backend

Usage:
  fisherman <recipe.json>          run an installation from a recipe file
  fisherman validate <recipe.json> validate a recipe without installing
  fisherman images [<query>]       list or search the image catalog
  fisherman version                print version information
  fisherman help                   show this help

Options for 'images':
  --file <path>   use a specific images.json instead of auto-detecting
  --plain         plain text output (no ANSI color or tree characters)

Examples:
  fisherman /tmp/recipe.json
  fisherman validate /tmp/recipe.json
  fisherman images
  fisherman images TunaOS
  fisherman images "GNOME 50"
  fisherman images --plain yellowfin
`)
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "help", "--help", "-h":
		printHelp()
		return
	case "version", "--version":
		fmt.Printf("fisherman %s\n", version)
		return
	case "images":
		runImages(os.Args[2:])
		return
	case "validate":
		runValidate(os.Args[2:])
		return
	}

	r, err := recipe.Load(os.Args[1])
	if err != nil {
		fatal("loading recipe: %v", err)
	}
	if err := r.Validate(); err != nil {
		fatal("invalid recipe: %v", err)
	}

	// Expand PATH to cover all standard sbin locations and any tools staged
	// alongside this binary (e.g. by the tuna-installer Flatpak).  pkexec
	// strips the calling user's PATH to a minimal safe set which often omits
	// /usr/sbin and /sbin on some immutable distros.
	expandPath()

	// Pre-flight: verify that every host tool required for this recipe is
	// present before we touch any disks.
	if err := checkRequiredTools(r); err != nil {
		fatal("missing required host tool: %v", err)
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
	var activeLuksUUID string // LUKS partition UUID for boot entry injection; empty if no encryption

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

		if isSystemdBoot {
			// systemd-boot always uses a 2-partition layout regardless of encryption.
			// LUKS (if requested) wraps p2 (root); the 1 GiB FAT32 ESP stays unencrypted.
			if err := disk.PartitionSystemdBoot(r.Disk); err != nil {
				fatal("partitioning disk: %v", err)
			}
		} else if hasEncryption {
			if err := disk.PartitionEncrypted(r.Disk); err != nil {
				fatal("partitioning disk: %v", err)
			}
		} else {
			if err := disk.Partition(r.Disk); err != nil {
				fatal("partitioning disk: %v", err)
			}
		}

		var efiPart, bootPart, rootPart string
		if isSystemdBoot {
			// 2-partition layout: p1=EFI (1 GiB FAT32), p2=root.
			// No separate ext4 /boot needed — systemd-boot reads directly from
			// the FAT32 ESP. LUKS (if any) wraps p2.
			efiPart = disk.PartName(r.Disk, 1)
			rootPart = disk.PartName(r.Disk, 2)
		} else {
			// 3-partition layout: p1=EFI, p2=/boot (ext4), p3=root.
			// The separate ext4 /boot keeps GRUB away from modern XFS features.
			efiPart = disk.PartName(r.Disk, 1)
			bootPart = disk.PartName(r.Disk, 2)
			rootPart = disk.PartName(r.Disk, 3)
		}
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
		// systemd-boot reads the FAT32 ESP directly, so no separate /boot needed.
		if !isSystemdBoot {
			if err := disk.FormatBoot(bootPart); err != nil {
				fatal("formatting /boot: %v", err)
			}
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
			activeLuksUUID = luks.UUID(rootPart)
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
		// Not needed for systemd-boot installs (no separate /boot partition).
		if !isSystemdBoot {
			if err := disk.MountBoot(targetMount, bootPart); err != nil {
				fatal("mounting /boot: %v", err)
			}
			cleanup.AddMount(targetMount + "/boot")
		}

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
	//
	// On installed (ostree/conventional) systems /var is always disk-backed,
	// so /var/fisherman-tmp gives us plenty of space. On live ISOs, however,
	// /var lives on a tmpfs that is far too small for multi-gigabyte image
	// blobs. Detect that case and place scratch on the already-formatted
	// target disk instead. A self-bind mount makes bootc's empty-rootdir
	// check see a mount point (which it tolerates) rather than a plain
	// directory (which it rejects).
	scratchDir := "/var/fisherman-tmp"
	liveISO := isTmpfs("/var") && activeTargetMount != ""
	if liveISO {
		scratchDir = filepath.Join(activeTargetMount, ".fisherman-scratch")
		progress.Info("Live environment detected (/var is tmpfs) — using target disk for scratch I/O")
	}
	if err := os.MkdirAll(scratchDir, 0o1777); err != nil {
		fatal("creating scratch dir: %v", err)
	}
	if liveISO {
		// Self-bind so bootc sees a mount point, not a plain directory.
		if err := disk.BindMount(scratchDir, scratchDir); err != nil {
			fatal("self-bind scratch dir: %v", err)
		}
		cleanup.AddMount(scratchDir)
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
		ScratchDir:       scratchDir,
		NeedsPull:        imageCheck.NeedsPull,
		LayerCount:       imageCheck.LayerCount,
	}); err != nil {
		fatal("bootc install: %v", err)
	}

	// systemd-boot composefs installs rely on GPT auto-discovery for the root
	// filesystem. Keep the auto-partitioned root on the architecture-specific
	// Linux root GUID so the installed system can find /sysroot on first boot.
	if !isManual && isSystemdBoot && !hasEncryption && r.ComposeFsBackend {
		progress.Info("Retagging root partition for systemd GPT auto-discovery")
		if err := disk.SetPartitionType(r.Disk, 2, disk.GPTPartTypeLinuxRootX86_64); err != nil {
			fatal("retagging root partition: %v", err)
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

	if err := post.CopyFlatpaks(activeTargetMount, r.Flatpaks, r.FlatpakVarPath); err != nil {
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

	// Inject rd.luks.uuid into every BLS entry so the initrd knows to unlock
	// the LUKS container before mounting root. bootc install to-filesystem only
	// sees the open mapper device and never writes LUKS parameters itself.
	if activeLuksUUID != "" {
		n, err := post.EnsureLuksArgs(activeTargetMount, activeLuksUUID)
		if err != nil {
			progress.Info(fmt.Sprintf("Warning: could not inject LUKS boot args: %v", err))
		} else if n > 0 {
			progress.Info(fmt.Sprintf("Injected rd.luks.uuid into %d boot entr%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]))
		}
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
