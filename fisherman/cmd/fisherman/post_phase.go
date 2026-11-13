package main

import (
	"fmt"

	"github.com/tuna-os/fisherman/internal/deploy"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"

	"github.com/tuna-os/wootc/slurp"
)

// The post-install phase of an install (#313): everything written into the
// installed system once bootc has deployed it. The persistent mounts (a
// /var disk, a manual layout's data partitions and swap), the first-boot
// TPM2 enrolment (its own step) and the recovery key, the system flatpaks
// (step 7), the configuration of step 8 (hostname, user, locale, boot
// splash, root flags, LUKS boot arguments, host secrets, audio, migrated
// Windows data, OEM packages, print services) and the installation
// receipt.
//
// Like the other phases it takes what it needs as arguments and never ends
// the process: a failure comes back as a *phaseError with the failure code
// and message main() used to pass to fatalCode. Everything non-fatal stays
// a warning line, as before.

// postInput is what the post-install phase reads from the earlier phases.
type postInput struct {
	// disk is the disk phase's result: the target, the LUKS partition, its
	// UUID and the tpm2-luks recovery key.
	disk diskResult
	// extras are the desktop extras the recipe or the catalog turned on;
	// hostIsLive asks, at most once, whether the host is live media.
	extras     recipe.Extras
	hostIsLive func() bool
	// dataResult and wallpaperResult are the Windows data slurped before
	// partitioning (nil when there was none).
	dataResult      *slurp.DataSlurpResult
	wallpaperResult *slurp.WallpaperResult
	// The receipt's inputs: the install source, the --target-imgref the
	// install passed, the live image and the media input.
	sourceImgref string
	targetImgref string
	liveImage    string
	mediaInput   *install.MediaInput
}

// postOps are the operations the post-install phase performs, and the
// teardown registration of the mounts it makes. livePostOps is the real
// set.
type postOps struct {
	persistMounts     func(r *recipe.Recipe, target string, mounted func(path string)) error
	addMount          func(path string)
	stageTPM2         func(target, rootPart, luksUUID, passphrase string) string
	recoveryKey       func(key string)
	copyFlatpaks      func(target string, refs []string, flatpakVarPath string) error
	writeHostname     func(target, hostname string) error
	createUser        func(target string, u post.UserConfig) error
	applyLocale       func(target string, r *recipe.Recipe, encrypted bool)
	plymouthArgs      func(target string) (int, error)
	rootFlags         func(target, options string) (string, error)
	luksBoot          func(target, luksUUID string, required bool) (string, error)
	copyHostSecrets   func(target string, r *recipe.Recipe, isLive func() bool)
	audioConfig       func(target string) error
	resolveDeployment func(target string) (*deploy.Deployment, error)
	injectData        func(varDir string, res *slurp.DataSlurpResult) error
	injectWallpapers  func(varDir string, res *slurp.WallpaperResult) error
	cleanupSlurp      func()
	oemPackages       func(target, distroID, brewTap string) error
	printServices     func(target string) (enabled, missing []string)
	stageReceipt      func(target, sourceImgref, updateRef string, media *install.MediaInput) (progress.ReceiptStatus, string)
	receipt           func(st progress.ReceiptStatus)

	info func(msg string)
}

// livePostOps binds the post-install phase to the real operations and to
// the install's cleanup.
func livePostOps() postOps {
	return postOps{
		persistMounts:     persistMounts,
		addMount:          cleanup.AddMount,
		stageTPM2:         stageTPM2Enrollment,
		recoveryKey:       progress.RecoveryKey,
		copyFlatpaks:      post.CopyFlatpaks,
		writeHostname:     post.WriteHostname,
		createUser:        post.CreateUser,
		applyLocale:       applyLocale,
		plymouthArgs:      post.EnsurePlymouthArgs,
		rootFlags:         ensureRootFlags,
		luksBoot:          ensureLuksBoot,
		copyHostSecrets:   copyHostSecrets,
		audioConfig:       post.GenerateAudioConfig,
		resolveDeployment: post.ResolveDeployment,
		injectData:        slurper.InjectData,
		injectWallpapers:  slurper.InjectWallpapers,
		cleanupSlurp:      slurper.CleanupScratch,
		oemPackages:       post.InstallOEMPackages,
		printServices:     post.EnablePrintServices,
		stageReceipt:      stageReceipt,
		receipt:           progress.Receipt,
		info:              progress.Info,
	}
}

// postPhase writes the installed system's configuration on the target the
// install phase deployed: the TPM2 step (when enrolled), steps 7 and 8, and
// the receipt. The parameters are named r and seq for the step contract
// test (steps_test.go), which replays this body as part of main().
func postPhase(r *recipe.Recipe, seq *stepSequencer, in postInput, ops postOps) error {
	activeTargetMount := in.disk.targetMount
	activeRootPart := in.disk.rootPart // only used for TPM2 enrolment, empty in manual mode
	activeLuksUUID := in.disk.luksUUID // empty if no encryption
	luksRecoveryKey := in.disk.recoveryKey
	extras, hostIsLive := in.extras, in.hostIsLive
	dataResult, wallpaperResult := in.dataResult, in.wallpaperResult

	// ── Persistent mounts (varDisk, customMounts data and swap) ──────────────
	// From here on every step writes into the installed system through the
	// deployment resolver (post.ResolveDeployment). persistMounts tells it
	// that a separate /var (a varDisk, or a manual /var partition) is what
	// the booted system mounts at /var, mounts the other /var devices where
	// those steps write, and writes the fstab lines. See mounts.go (#406,
	// #372).
	if err := ops.persistMounts(r, activeTargetMount, ops.addMount); err != nil {
		return phaseFail(progress.FailurePersistentMountsFailed, "persistent mounts: %v", err)
	}

	// ── TPM2 enrolment ────────────────────────────────────────────────────────
	// Both tpm2-luks and tpm2-luks-passphrase add a TPM2 auto-unlock token so
	// the system boots without a passphrase prompt. The difference:
	//   tpm2-luks:            random passphrase (recovery key) + TPM2
	//   tpm2-luks-passphrase: user passphrase (fallback) + TPM2
	if r.EnrollsTPM2() {
		seq.begin(stepEnrollTPM2)

		unlockPassphrase := r.Encryption.Passphrase
		if r.Encryption.Type == "tpm2-luks" {
			unlockPassphrase = luksRecoveryKey
		}
		ops.info(ops.stageTPM2(activeTargetMount, activeRootPart, activeLuksUUID, unlockPassphrase))

		// For tpm2-luks the user never chose a passphrase, so we emit the
		// random one as a recovery key they must save before rebooting.
		if luksRecoveryKey != "" {
			ops.recoveryKey(luksRecoveryKey)
		}
	}

	// ── Step 7: Copy system flatpaks ──────────────────────────────────────────
	seq.begin(stepFlatpaks)

	if err := ops.copyFlatpaks(activeTargetMount, r.Flatpaks, r.FlatpakVarPath); err != nil {
		// Non-fatal — the system will work without pre-installed flatpaks.
		ops.info(fmt.Sprintf("Warning: could not copy flatpaks: %v", err))
	}

	// ── Step 8: Post-install configuration ───────────────────────────────────
	seq.begin(stepConfigure)

	ops.info(fmt.Sprintf("Writing hostname: %s", r.Hostname))
	if err := ops.writeHostname(activeTargetMount, r.Hostname); err != nil {
		return phaseFail(progress.FailureHostnameFailed, "writing hostname: %v", err)
	}

	// Create a user account if the recipe requests one (e.g. Bazzite has no OOBE).
	if r.User.Username != "" {
		ops.info(fmt.Sprintf("Creating user: %s", r.User.Username))
		if err := ops.createUser(activeTargetMount, post.UserConfig{
			Username: r.User.Username,
			Fullname: r.User.Fullname,
			Password: r.User.Password,
			Groups:   r.User.Groups,
		}); err != nil {
			return phaseFail(progress.FailureUserFailed, "creating user: %v", err)
		}
	}

	// Language, keyboard layout and time zone, when the recipe has them. For
	// an encrypted install the keymap also goes to the initrd. Non-fatal.
	ops.applyLocale(activeTargetMount, r, r.EncryptsRoot())

	// rhgb and quiet in every BLS loader entry, for the graphical boot
	// splash: only for an image that asks (extras.bootSplash). Another
	// image keeps the kernel arguments it ships. Non-fatal.
	if extras.BootSplash {
		n, err := ops.plymouthArgs(activeTargetMount)
		if err != nil {
			ops.info(fmt.Sprintf("Warning: could not set Plymouth kernel args: %v", err))
		} else if n > 0 {
			ops.info(fmt.Sprintf("Added Plymouth boot args to %d loader entr%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]))
		} else {
			ops.info("Plymouth boot args: already in every loader entry")
		}
	}

	// Every BLS entry must mount the root with the recipe's options
	// (rootflags=subvol=@,compress=zstd:1 for btrfsSubvolumes): bootc 1.15.x
	// drops --karg on the composefs path and writes only its own
	// rootflags=subvol=<mount's subvol>. Before the LUKS check, which wants
	// rd.luks.name last.
	if msg, err := ops.rootFlags(activeTargetMount, r.RootMountOptions()); err != nil {
		return phaseFail(progress.FailureRootFlagsFailed, "root mount options: %v", err)
	} else if msg != "" {
		ops.info(msg)
	}

	// Every BLS entry must carry rd.luks.name=<UUID>=root so the initrd
	// unlocks the LUKS container and maps it to /dev/mapper/root before
	// mounting the root filesystem. bootc install to-filesystem only sees the
	// open mapper device and never writes LUKS parameters itself; the --karg
	// above asks it to, and this checks every entry and patches what is
	// missing. Fatal unless GPT auto-discovery finds the root (#410).
	if activeLuksUUID != "" {
		msg, err := ops.luksBoot(activeTargetMount, activeLuksUUID, luksNeedsKarg(r.UsesSystemdBoot(), r.ComposeFsBackend))
		if err != nil {
			return phaseFail(progress.FailureLUKSBootArgsFailed, "LUKS boot arguments: %v", err)
		}
		ops.info(msg)
	}

	// Wi-Fi profiles and Bluetooth pairings hold secrets (passwords, link
	// keys). They follow the user from live media to the disk; from any
	// other host they are copied only when the recipe asks (#411).
	ops.copyHostSecrets(activeTargetMount, r, hostIsLive)

	// Generate friendly audio device names and hide useless outputs (S/PDIF,
	// Pro Audio, monitor loopbacks). Writes WirePlumber rules to the
	// deployment's /etc. Only for an image that asks (extras.audioNames).
	// Non-fatal.
	if extras.AudioNames {
		if err := ops.audioConfig(activeTargetMount); err != nil {
			ops.info(fmt.Sprintf("Warning: could not configure audio devices: %v", err))
		}
	}

	// Inject slurped Windows data into the installed system's /var (the one
	// the booted system mounts, from the deployment resolver). Non-fatal.
	if (dataResult != nil && dataResult.Found) || (wallpaperResult != nil && wallpaperResult.Found) {
		if d, err := ops.resolveDeployment(activeTargetMount); err != nil {
			ops.info(fmt.Sprintf("Warning: could not inject migrated data: %v", err))
		} else {
			if dataResult != nil && dataResult.Found {
				if err := ops.injectData(d.Var, dataResult); err != nil {
					ops.info(fmt.Sprintf("Warning: could not inject user data: %v", err))
				}
			}
			if wallpaperResult != nil && wallpaperResult.Found {
				if err := ops.injectWallpapers(d.Var, wallpaperResult); err != nil {
					ops.info(fmt.Sprintf("Warning: could not inject wallpapers: %v", err))
				}
			}
		}
	}
	// Cleanup scratch space (both data and wallpaper slurps use /run/fisherman-slurp)
	if dataResult != nil || wallpaperResult != nil {
		ops.cleanupSlurp()
	}

	// Detect OEM hardware (ASUS, Framework) and queue vendor-specific packages
	// for first-login install via brew; enable the vendor's service when the
	// image has it. Only for an image that asks (extras.oemSetup). Non-fatal.
	if extras.OEMSetup {
		if err := ops.oemPackages(activeTargetMount, r.DistroID, r.BrewTap); err != nil {
			ops.info(fmt.Sprintf("Warning: OEM package setup: %v", err))
		}
	}

	// Print auto-discovery (cups-browsed, avahi-daemon, ipp-usb): only for an
	// image that asks (extras.printServices), and each unit only when the
	// installed image has it.
	if extras.PrintServices {
		ops.printServices(activeTargetMount)
	}

	// The installation receipt and its first-boot collector (#385). The
	// install says "installed, reboot required"; only the installed system,
	// after it boots, can say which image it booted.
	// The log line, then the `receipt` event a frontend keys on.
	receiptStatus, receiptMsg := ops.stageReceipt(activeTargetMount, in.sourceImgref,
		receiptUpdateRef(in.sourceImgref, in.targetImgref, in.liveImage), in.mediaInput)
	ops.info(receiptMsg)
	ops.receipt(receiptStatus)

	// No cache warming and no thumbnail generation here: the image's /usr is
	// read-only (and, on ostree, hard-linked into the repository), its caches
	// are built with the image, and the host's tools would write the host's
	// answers (#407).

	return nil
}
