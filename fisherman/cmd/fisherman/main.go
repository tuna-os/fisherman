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

func fatal(format string, args ...any) {
	cleanup.Run()
	fmt.Fprintf(os.Stderr, "fisherman: fatal: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: fisherman <recipe.json>\n")
		os.Exit(1)
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

	// Compute total step count up front so the GUI can show accurate progress.
	totalSteps := 7
	if hasEncryption {
		totalSteps++
	}
	if hasTPM2 && r.Encryption.Type == "tpm2-luks-passphrase" {
		totalSteps++ // extra step for TPM2 enrolment
	}
	step := 1

	// ── Step 1: Partition disk ────────────────────────────────────────────────
	progress.Step(step, totalSteps, "Partitioning disk")
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

	efiPart := disk.PartName(r.Disk, 1)
	// With encryption: p2=/boot (unencrypted), p3=LUKS root
	// Without encryption: p2=root
	var bootPart, rootPart string
	if hasEncryption {
		bootPart = disk.PartName(r.Disk, 2)
		rootPart = disk.PartName(r.Disk, 3)
	} else {
		rootPart = disk.PartName(r.Disk, 2)
	}
	rootDev := rootPart // may be replaced by /dev/mapper/fisherman-root if LUKS

	// ── Step 2: Format EFI ───────────────────────────────────────────────────
	progress.Step(step, totalSteps, "Formatting EFI partition")
	step++

	if err := disk.FormatEFI(efiPart); err != nil {
		fatal("formatting EFI: %v", err)
	}
	if hasEncryption {
		if err := disk.FormatBoot(bootPart); err != nil {
			fatal("formatting /boot: %v", err)
		}
	}

	// ── Step 3: Disk encryption (optional) ───────────────────────────────────
	if hasEncryption {
		progress.Step(step, totalSteps, "Setting up disk encryption")
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

	// ── Step 4: Format root filesystem ───────────────────────────────────────
	progress.Step(step, totalSteps, "Formatting root filesystem")
	step++

	if err := disk.FormatRoot(rootDev, r.Filesystem); err != nil {
		fatal("formatting root filesystem: %v", err)
	}

	// ── Step 5: Mount filesystem ──────────────────────────────────────────────
	progress.Step(step, totalSteps, "Mounting filesystem")
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

	// For encrypted installs, mount the unencrypted /boot partition BEFORE the
	// EFI partition. bootupctl (run by bootc) reads /boot's block device UUID
	// from the raw partition — this only works with an unencrypted /boot.
	if hasEncryption {
		if err := disk.MountBoot(targetMount, bootPart); err != nil {
			fatal("mounting /boot: %v", err)
		}
		cleanup.AddMount(targetMount + "/boot")
	}

	// Mount the EFI partition at /boot/efi inside the target.
	// bootc install to-filesystem requires this to exist before it runs.
	if err := disk.MountEFI(targetMount, efiPart); err != nil {
		fatal("mounting EFI: %v", err)
	}
	cleanup.AddMount(targetMount + "/boot/efi")

	// Bind-mount a host-side scratch directory at /var/tmp so bootc has
	// disk-backed space for layer blobs. We deliberately use a path OUTSIDE
	// the target tree so bootc's "empty rootfs" check doesn't find stray
	// directories inside /mnt/fisherman-target.
	scratchDir := "/run/fisherman-tmp"
	if err := os.MkdirAll(scratchDir, 0o1777); err != nil {
		fatal("creating scratch dir: %v", err)
	}
	if err := disk.BindMount(scratchDir, "/var/tmp"); err != nil {
		fatal("bind-mounting scratch dir at /var/tmp: %v", err)
	}
	cleanup.AddMount("/var/tmp")

	// ── Step 6: Install OS ────────────────────────────────────────────────────
	progress.Step(step, totalSteps, "Installing OS")
	step++

	// Only pass --target-imgref when it is non-empty and differs from the source.
	targetImgref := r.TargetImgref
	if targetImgref == r.Image {
		targetImgref = ""
	}

	if err := install.BootcInstall(install.Options{
		SourceImgref:    r.Image,
		TargetImgref:    targetImgref,
		SelinuxDisabled: r.SelinuxDisabled,
		UnifiedStorage:  r.UnifiedStorage,
		Target:          targetMount,
	}); err != nil {
		fatal("bootc install: %v", err)
	}

	// ── TPM2 enrolment (tpm2-luks-passphrase only) ────────────────────────────
	// For plain tpm2-luks the random passphrase is ephemeral; no enrolment step.
	// For tpm2-luks-passphrase the user's passphrase unlocks LUKS; we add a
	// TPM2 token on top so the system auto-unlocks, with the password as fallback.
	if r.Encryption.Type == "tpm2-luks-passphrase" {
		progress.Step(step, totalSteps, "Enrolling TPM2 auto-unlock")
		step++

		if err := luks.EnrollTPM2(rootPart, r.Encryption.Passphrase); err != nil {
			// Non-fatal: TPM2 hardware may not be present (e.g. VMs).
			progress.Info(fmt.Sprintf("Warning: TPM2 enrolment failed (password unlock still works): %v", err))
		}
	}

	// ── Step 7: Copy system flatpaks ──────────────────────────────────────────
	progress.Step(step, totalSteps, "Copying system Flatpaks")
	step++

	if err := post.CopyFlatpaks(targetMount, r.Flatpaks); err != nil {
		// Non-fatal — the system will work without pre-installed flatpaks.
		progress.Info(fmt.Sprintf("Warning: could not copy flatpaks: %v", err))
	}

	// ── Step 8: Post-install configuration ───────────────────────────────────
	progress.Step(step, totalSteps, "Configuring installed system")

	progress.Info(fmt.Sprintf("Writing hostname: %s", r.Hostname))
	if err := post.WriteHostname(targetMount, r.Hostname); err != nil {
		fatal("writing hostname: %v", err)
	}

	// Tear down mounts and LUKS before declaring success.
	cleanup.Run()

	progress.Complete("Installation complete!")
}
