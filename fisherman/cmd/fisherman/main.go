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

	// Compute total step count up front so the GUI can show accurate progress.
	totalSteps := 6
	if hasEncryption {
		totalSteps = 7
	}
	step := 1

	// ── Step 1: Partition disk ────────────────────────────────────────────────
	progress.Step(step, totalSteps, "Partitioning disk")
	step++

	if err := disk.Partition(r.Disk); err != nil {
		fatal("partitioning disk: %v", err)
	}

	efiPart := disk.PartName(r.Disk, 2)
	rootPart := disk.PartName(r.Disk, 3)
	rootDev := rootPart // may be replaced by /dev/mapper/fisherman-root if LUKS

	// ── Step 2: Format EFI ───────────────────────────────────────────────────
	progress.Step(step, totalSteps, "Formatting EFI partition")
	step++

	if err := disk.FormatEFI(efiPart); err != nil {
		fatal("formatting EFI: %v", err)
	}

	// ── Step 3: Disk encryption (optional) ───────────────────────────────────
	if hasEncryption {
		progress.Step(step, totalSteps, "Setting up disk encryption")
		step++

		var passphrase string
		switch r.Encryption.Type {
		case "luks-passphrase":
			passphrase = r.Encryption.Passphrase
		case "tpm2-luks":
			passphrase = luks.RandomPassphrase()
			progress.Info(
				"TPM2-LUKS: a temporary passphrase is used for installation. " +
					"After first boot enroll the TPM2 chip with: " +
					"systemd-cryptenroll --tpm2-device=auto /dev/disk/by-label/root",
			)
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

	// Mount a 4 GiB tmpfs at /var/tmp so bootc has room for layer blobs.
	// The live ISO's overlayfs root is too small for large image writes.
	if err := disk.MountTmpfs("/var/tmp", "4G"); err != nil {
		fatal("mounting tmpfs at /var/tmp: %v", err)
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
		Target:          targetMount,
	}); err != nil {
		fatal("bootc install: %v", err)
	}

	// ── Step 7: Post-install configuration ───────────────────────────────────
	progress.Step(step, totalSteps, "Configuring installed system")

	progress.Info(fmt.Sprintf("Writing hostname: %s", r.Hostname))
	if err := post.WriteHostname(targetMount, r.Hostname); err != nil {
		fatal("writing hostname: %v", err)
	}

	// Tear down mounts and LUKS before declaring success.
	cleanup.Run()

	progress.Complete("Installation complete!")
}
