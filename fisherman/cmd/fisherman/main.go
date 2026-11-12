package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
	"github.com/tuna-os/fisherman/internal/slurp"
	"github.com/tuna-os/fisherman/internal/validation"
)

const (
	defaultTargetMount = "/mnt/fisherman-target"
	defaultLuksMapper  = "fisherman-root"
)

// These are resolved from the recipe in main(); package-level vars rather than
// constants so the rest of this file (which references them by short name)
// stays readable. Tests don't touch these directly — they exercise the helper
// functions in disk/, luks/, post/ which take the paths as arguments.
var (
	targetMount = defaultTargetMount
	luksMapper  = defaultLuksMapper
)

// cleanup is global so fatal() can tear everything down on any error path.
var cleanup = &post.Cleanup{}

// bindMount is disk.BindMount by default; tests replace it to avoid real mounts.
var bindMount = disk.BindMount

// bootcInstall is install.BootcInstall by default; tests replace it.
var bootcInstall = install.BootcInstall

// prepareSelinuxWorkaround is install.PrepareSelinuxWorkaround by default;
// tests replace it.
var prepareSelinuxWorkaround = install.PrepareSelinuxWorkaround

// fatal ends the install: it stops child processes, tears down every mount
// and LUKS mapping registered on cleanup, emits one {"type":"error"} event
// (noting any teardown failure) and exits with exitFailure. If a cancel
// signal is already being handled it defers to that path instead. See
// terminator in terminate.go.
func fatal(format string, args ...any) {
	term.fail(fmt.Sprintf(format, args...))
}

// fatalProblem is fatal for a failure with a stable problem code (a recipe
// rule or a preflight check): the error event carries the code and field
// that `fisherman validate --json` reports for the same problem.
func fatalProblem(prefix string, p recipe.Problem) {
	term.failProblem(prefix+": "+p.Message, p)
}

// fatalLoad reports a recipe that could not be loaded. A strict-parse
// problem (an unknown field) keeps its code. Any other failure gets the
// code `fisherman validate --json` gives it: recipe_malformed for empty
// or invalid JSON, recipe_unreadable for a recipe that cannot be read.
func fatalLoad(err error) {
	var p recipe.Problem
	if errors.As(err, &p) {
		fatalProblem("loading recipe", p)
		return
	}
	code := recipe.CodeRecipeUnreadable
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.Is(err, recipe.ErrEmptyRecipe) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &syntax) || errors.As(err, &typeErr) {
		code = recipe.CodeRecipeMalformed
	}
	msg := fmt.Sprintf("loading recipe: %v", err)
	term.failProblem(msg, recipe.Problem{Code: code, Message: msg})
}

// bootcFailureCode is the failure code of a bootcInstall error: pull_failed
// for the image pull (install.PullError, through any wrapping), else
// bootc_install_failed.
func bootcFailureCode(err error) string {
	var pull *install.PullError
	if errors.As(err, &pull) {
		return progress.FailurePullFailed
	}
	return progress.FailureBootcInstallFailed
}

// fatalCode is fatal for a runtime failure: the error event carries code,
// one of the progress.Failure* codes, so a frontend need not read the
// message to tell which operation failed.
func fatalCode(code, format string, args ...any) {
	term.failCode(fmt.Sprintf(format, args...), code)
}

// teardown releases everything the install registered: a filesystem the
// finalize step froze, the swap a manual layout activated, then the mounts,
// the LUKS mapping and the scratch directories on cleanup. Every exit path
// runs it.
func teardown() error {
	// The thaw comes first: a frozen filesystem cannot be unmounted cleanly,
	// and its LUKS mapping cannot be closed.
	thawErr := freezes.ThawAll()
	swapErr := swaps.Off()
	return errors.Join(thawErr, swapErr, cleanup.Run())
}

// geteuid is os.Geteuid by default; replaced in tests.
var geteuid = os.Geteuid

// lookPath is exec.LookPath by default; replaced in tests.
var lookPath = exec.LookPath

// isSpaceConstrained reports whether the given path is on a filesystem that is
// too small for multi-gigabyte scratch I/O. This covers:
//   - tmpfs: RAM-backed, used by some live ISOs for /var
//   - overlayfs: used by dmsquash-live (dracut) live ISOs where / and /var
//     are an overlay on top of a squashfs; the writable upper layer has only
//     a few GiB available
func isSpaceConstrained(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	const (
		tmpfsMagic   = 0x01021994
		overlayMagic = 0x794c7630
	)
	return st.Type == tmpfsMagic || st.Type == overlayMagic
}

// varIsSpaceConstrained reports whether the host's /var is too small for
// scratch I/O, so scratch goes on the target disk. The install and `plan`
// ask it; tests replace it.
var varIsSpaceConstrained = func() bool { return isSpaceConstrained("/var") }

func prepareScratchDir(activeTargetMount string, liveISO bool) (string, error) {
	scratchDir := "/var/fisherman-tmp"
	if liveISO {
		scratchDir = filepath.Join(activeTargetMount, ".fisherman-scratch")
		progress.Info("Live environment detected (/var is space-constrained) — using target disk for scratch I/O")
	}
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return "", err
	}
	if liveISO {
		// Self-bind so bootc sees a mount point, not a plain directory.
		if err := bindMount(scratchDir, scratchDir); err != nil {
			// Nothing is mounted and nothing is registered: leave no
			// directory behind on the target either (#291).
			_ = os.Remove(scratchDir)
			return "", err
		}
		// One registration for the bind mount AND the directory under it:
		// teardown releases the bind and deletes the directory while the
		// target filesystem is still mounted, before any other unmount and
		// before the LUKS close. Deleting it after them (as a post-removal)
		// only deleted an empty mountpoint on the host and left the OCI cache
		// on the installed disk (#307).
		cleanup.AddTargetScratch(scratchDir)
	}
	return scratchDir, nil
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

// checkRequiredTools verifies that every host program this recipe's install
// runs (validation.RequiredTools) is on the host before any disk is touched.
// It returns the first missing one, which names the tool and its package.
func checkRequiredTools(r *recipe.Recipe) error {
	look := validation.HostLookPath
	if !runner.InFlatpak() {
		// Natively the host's PATH is this process's (after expandPath).
		look = func(tool string) error { _, err := lookPath(tool); return err }
	}
	if missing := validation.MissingTools(r, look); len(missing) > 0 {
		return missing[0]
	}
	return nil
}

var version = "dev"

func printHelp() {
	fmt.Printf(`fisherman — bootc disk installer backend

Usage:
  fisherman <recipe.json>          run an installation from a recipe file
  fisherman -                      run an installation, reading the recipe JSON from stdin
  fisherman validate <recipe.json> validate a recipe without installing ("-" reads stdin)
  fisherman validate --json [--partial] <recipe.json|->
                                   every problem as JSON (docs/VALIDATE.md)
  fisherman plan --json -          what an install of the recipe on stdin would do, as
                                   JSON; writes nothing, pulls nothing (docs/PLAN.md)
  fisherman images [<query>]       list or search the image catalog
  fisherman scan <disk>            scan disk for Windows data available to migrate
  fisherman probe --json           print disks, TPM, RAM/CPU/UEFI, live and offline facts
  fisherman verify --json          on the installed system: compare the installation
                                   receipt with the booted image (docs/RECEIPT.md)
  fisherman diagnostics --output-dir <dir> [--recipe <f>] [--progress-log <f>] [--probe]
                                   write a local support bundle of allowlisted facts,
                                   no secrets (docs/DIAGNOSTICS.md)
  fisherman version                print version information
  fisherman help                   show this help

Options for 'images':
  --file <path>   use a specific images.json instead of auto-detecting
  --plain         plain text output (no ANSI color or tree characters)

Examples:
  fisherman /tmp/recipe.json
  fisherman - < /tmp/recipe.json
  fisherman validate /tmp/recipe.json
  echo '{"hostname":"my-pc"}' | fisherman validate --json --partial -
  fisherman plan --json - < /tmp/recipe.json
  fisherman images
  fisherman images Bluefin
  fisherman images "GNOME 50"
  fisherman images --plain yellowfin
  fisherman scan /dev/nvme0n1
  fisherman probe --json
`)
}

// looksLikeSubcommand reports whether arg is a mistyped command rather than a
// recipe path. The bare positional form (`fisherman recipe.json`) is the
// install entry point, so the fall-through to recipe.Load must stay; but a
// flag, or a bare word with no path separator and no file behind it, is a
// command this backend does not have (`fisherman install --recipe …`, #178).
// Naming it beats "loading recipe: open install: no such file or directory".
func looksLikeSubcommand(arg string) bool {
	if arg == recipe.StdinArg {
		return false // `fisherman -`: the recipe is on stdin
	}
	if strings.HasPrefix(arg, "-") {
		return true
	}
	if strings.ContainsAny(arg, `/\`) || strings.HasSuffix(arg, ".json") {
		return false
	}
	_, err := os.Stat(arg)
	return err != nil
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(exitFailure)
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
	case "probe":
		os.Exit(runProbe(os.Args[2:], os.Stdout, os.Stderr))
	case "verify":
		os.Exit(runVerify(os.Args[2:], os.Stdout, os.Stderr))
	case "plan":
		os.Exit(runPlan(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "diagnostics":
		os.Exit(runDiagnostics(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "scan":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: fisherman scan <disk>\n")
			os.Exit(exitFailure)
		}
		output, err := slurp.ScanJSON(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			os.Exit(exitFailure)
		}
		fmt.Println(output)
		return
	}

	if looksLikeSubcommand(os.Args[1]) {
		fmt.Fprintf(os.Stderr, "fisherman: unknown command %q, and no such recipe file\n\n", os.Args[1])
		printHelp()
		os.Exit(exitUsage)
	}

	// From here on this is an install. SIGTERM, SIGINT and SIGHUP cancel it
	// through the same teardown as fatal() and exit with exitCancelled. So
	// does the death of fisherman's parent (the frontend's wrapper), which is
	// how an unprivileged frontend cancels a root fisherman; see
	// parentdeath_linux.go.
	term.watchSignals()
	term.armParentDeathCancel()

	// An install partitions, formats and mounts: it needs root for its first
	// disk step, and before that step it already changes the host (the live
	// audio configuration, mount points, scratch space). Refuse here, before
	// the recipe is even read, rather than fail half-way.
	if geteuid() != 0 {
		fatalProblem("cannot install", recipe.Problem{Code: recipe.CodeNotRoot,
			Message: "fisherman must run as root to install (run it through pkexec or sudo); " +
				"`fisherman validate` and `fisherman probe` need no root"})
	}

	// One install per host at a time; see acquireInstallLock.
	if err := acquireInstallLock(); err != nil {
		if errors.Is(err, errInstallRunning) {
			fatalProblem("cannot install", recipe.Problem{Code: recipe.CodeInstallAlreadyRunning,
				Message: err.Error() + "; wait for it to finish, or cancel it, then start this install again"})
		}
		fatalCode(progress.FailureLockFailed, "cannot take the install lock %s: %v", installLockPath, err)
	}

	r, err := loadRecipe(os.Args[1])
	if err != nil {
		fatalLoad(err)
	}
	// No error event or failure line may carry these.
	progress.RegisterSecret(r.Encryption.Passphrase)
	progress.RegisterSecret(r.User.Password)

	// Resolve and verify host-owned media before live probing (bootc status),
	// store discovery or CheckImage can inspect a stale cache or the registry.
	// A sourceReuse recipe names its source itself, and is refused on live
	// media (reuseProblems), so no media input is bound for it.
	var mediaInput *install.MediaInput
	if r.SourceReuse == nil {
		mediaInput, err = resolveMediaInput(r)
		if err != nil {
			fatalCode(progress.FailureLocalSourceUnavailable, "required local source is absent or unreadable; refusing installation: %v", err)
		}
	}
	sourceImgref := r.Image
	if mediaInput != nil {
		if mediaInput.ContainersStoragePath != "" {
			sourceImgref = "containers-storage:" + mediaInput.Image
		} else {
			sourceImgref = "oci:" + mediaInput.OCILayoutPath
		}
	}

	// Pre-flight: every rule `fisherman validate` enforces runs here, through
	// the same function (installProblems), before any disk is touched. The
	// first problem ends the install, and the error event carries its code.
	var live probe.Live
	if r.Image == "" && mediaInput == nil && r.SourceReuse == nil {
		live = hostLive()
	}
	if mediaInput != nil {
		live = probe.Live{IsLive: true, LiveImage: mediaInput.Image, DetectedBy: "verified media input"}
	}
	var imageErr error
	if r.Image == "" {
		imageErr = r.ValidateImage(live.IsLive)
	}
	if ps := installProblemsWith(r, imageErr); len(ps) > 0 {
		fatalProblem("invalid recipe", ps[0])
	}
	resolveTargets(r)

	// Recipe-level overrides for the otherwise-shared global mount paths.
	// Keeps two parallel installs on the same host from colliding.
	if r.TargetMount != "" {
		targetMount = r.TargetMount
	}
	if r.LuksMapperName != "" {
		luksMapper = r.LuksMapperName
	}

	// Log fisherman version for CI diagnostics
	fmt.Fprintf(os.Stderr, "[fisherman] version: %s\n", version)

	// Expand PATH to cover all standard sbin locations and any tools staged
	// alongside this binary (e.g. by the tuna-installer Flatpak).  pkexec
	// strips the calling user's PATH to a minimal safe set which often omits
	// /usr/sbin and /sbin on some immutable distros.
	expandPath()

	// Pre-flight: verify that every host tool required for this recipe is
	// present before we touch any disks.
	if err := checkRequiredTools(r); err != nil {
		fatalProblem("missing required host tool", recipe.Problem{Code: recipe.CodeHostToolMissing, Message: err.Error()})
	}

	// A device the recipe keeps as it is must hold a mountable filesystem.
	// blkid says so now; the mount would say so after the disk is wiped.
	if ps := keptFilesystemProblems(r); len(ps) > 0 {
		fatalProblem("cannot keep the existing filesystem", ps[0])
	}

	// ── Source reuse (explicit opt-in, #384) ─────────────────────────────
	// Copy the image the recipe names by digest out of this host's bootc
	// storage, verify it, and install from the copy. Before any disk step:
	// a refusal or a failed copy leaves the target untouched. Absent
	// sourceReuse, nothing here runs.
	reused, reuseErr := acquireSourceReuse(r, cleanup.AddRelease)
	if reuseErr != nil {
		fatalProblem("source reuse", *reuseErr)
	}
	if reused != nil {
		sourceImgref = reused.SourceImgref
	}

	if r.Image == "" && reused == nil {
		// Only reachable on live media (checkImageSource).
		msg := fmt.Sprintf("Live media (%s): installing the running image", live.DetectedBy)
		if live.LiveImage != "" {
			msg += " " + live.LiveImage
		}
		progress.Info(msg)
	}

	// An OCI media source needs no store discovery or podman image listing.
	// Preserve explicitly supplied stores without probing unrelated caches.
	imageStores := r.AdditionalImageStores
	var discoveredStores bool
	if mediaInput == nil && reused == nil {
		imageStores, discoveredStores = resolveImageStores(r)
	}
	if discoveredStores && len(imageStores) > 0 {
		progress.Info("Offline image stores found on the host: " + strings.Join(imageStores, ", "))
	}

	// ── Pre-flight: check image cache ─────────────────────────────────────────
	var imageCheck install.ImageCheck
	if mediaInput != nil {
		imageCheck.Offline = true
		progress.Info("Offline: using embedded media image " + mediaInput.OCILayoutPath)
	} else if reused != nil {
		// The verified copy, read locally; never a registry. Its identity
		// is checked by digest against the booted deployment
		// (acquireSourceReuse), so only its presence matters here.
		imageCheck = install.CheckImage(sourceImgref)
		if imageCheck.NeedsPull {
			fatalProblem("source reuse", reuseProblem(recipe.CodeSourceReuseVerifyFailed,
				"the verified copy "+reused.Destination+" is no longer readable in containers-storage"))
		}
	} else if r.Image != "" {
		progress.Info("Checking image cache...")
		imageCheck = install.CheckImage(r.Image)
		if install.IsOCISource(r.Image) && imageCheck.NeedsPull {
			fatalCode(progress.FailureLocalSourceUnavailable, "required local source is absent or unreadable; refusing installation")
		}
		if strings.HasPrefix(r.Image, "containers-storage:") && imageCheck.NeedsPull {
			fatalCode(progress.FailureLocalSourceUnavailable, "required local source is absent or unreadable; refusing installation")
		}
		// A source that is known to fail, or is not a bootc image for this
		// host, is refused here: the pull and bootc would refuse it only
		// after the disk is wiped (#364).
		if e := imageCheck.Err; e != nil {
			fatalProblem("image source", recipe.Problem{Code: e.Code, Field: "image", Message: e.Message})
		}
		progress.Info(sourceIdentityLine(imageCheck.Source))
		if imageCheck.NeedsPull {
			progress.Info(fmt.Sprintf("Image pull required (%d layers)", imageCheck.LayerCount))
		} else if imageCheck.Offline {
			progress.Info("Offline: registry unreachable, using locally cached image")
		} else {
			progress.Info("Image found in local cache, no pull needed")
		}
	}

	// ── Pre-flight: SELinux label workaround ───────────────────────────────
	// A target with SELinux disabled, installed from the image's container
	// on a host that enforces SELinux, needs a preload library that drops the
	// image's security.selinux labels (install.SelinuxWorkaroundRequired).
	// It is built here, before any disk step, so a host that cannot build it
	// is refused with the disk untouched. Every other install needs no
	// compiler.
	shimOpts := buildBootcInstallOptions(r, sourceImgref, "", "", "", imageStores, imageCheck.NeedsPull, imageCheck.LayerCount)
	if reused != nil {
		reuseInstallOptions(&shimOpts)
	}
	// ── Pre-flight: source execution root (#363) ──────────────────────────
	// The host's bootc may install a local source directly only when the
	// host booted that source; establish it once, before any disk step.
	shimOpts.SourceRoot = establishSourceRoot(shimOpts, imageStores)
	if line := install.LocalSourceContextLine(shimOpts); line != "" {
		progress.Info(line)
	}
	selinuxShim, shimDir, shimErr := prepareSelinuxWorkaround(shimOpts)
	if shimErr != nil {
		fatalProblem("cannot prepare the install", recipe.Problem{Code: recipe.CodeSelinuxWorkaroundUnavailable, Message: shimErr.Error()})
	}
	if shimDir != "" {
		cleanup.AddPostRemoval(shimDir)
		progress.Info("SELinux: the target disables SELinux and this host enforces a policy; the label workaround is ready")
	}

	// The step plan for this recipe (steps.go). The sequencer announces
	// each step with its number, the plan's total and its bar position.
	seq := newStepSequencer(planSteps(r, imageCheck.NeedsPull))

	// ── Desktop extras ───────────────────────────────────────────────────
	// Print services, the OEM brew unit, friendly audio names and the boot
	// splash suit a desktop product image. They are off unless the recipe
	// (`extras`) or the image catalog turns them on (#412).
	extraRefs := []string{r.TargetImgref, r.Image, live.LiveImage}
	if mediaInput != nil {
		extraRefs = append(extraRefs, mediaInput.TargetImgref, mediaInput.Image)
	}
	extras, extrasLine := resolveExtras(r, extraRefs...)
	progress.Info(extrasLine)

	// hostIsLive: the host booted from live media. Asked at most once.
	hostIsLive := liveOnce(live)

	// ── Immediate: Apply friendly audio names to live session ─────────────
	// Detect hardware, rename ugly ALSA names, hide S/PDIF etc. This changes
	// the RUNNING session (the user's WirePlumber configuration and the
	// host's /etc/wireplumber), so it runs only on live media, where that
	// session is the installer's own. Non-fatal.
	if extras.AudioNames {
		if !hostIsLive() {
			progress.Info("Live audio config: skipped, this host is not live media and its session is not the installer's to change")
		} else if err := post.ApplyAudioConfigLive(); err != nil {
			progress.Info(fmt.Sprintf("Live audio config skipped: %v", err))
		}
	}

	// ── Pre-partition: Wallpaper slurp (easter egg) ──────────────────────────
	// Extract Windows wallpapers from any NTFS partition on the target disk
	// before partitioning destroys them. Held in RAM (/run). Entirely non-fatal.
	var wallpaperResult *slurp.WallpaperResult
	var dataResult *slurp.DataSlurpResult

	if r.Slurp != nil && !r.IsManualLayout() {
		// Full data slurp: user selected specific categories in the GUI
		progress.Info("Migrating Windows user data before partitioning...")
		cfg := &slurp.SlurpConfig{
			SourcePartition: r.Slurp.SourcePartition,
		}
		for _, u := range r.Slurp.Users {
			cfg.Users = append(cfg.Users, slurp.SlurpUserConfig{
				Name:       u.Name,
				Categories: u.Categories,
			})
		}
		result, err := slurp.ExtractData(cfg)
		if err != nil {
			progress.Info(fmt.Sprintf("Data migration skipped: %v", err))
		} else {
			dataResult = result
		}
	} else if r.SlurpWallpapers && !r.IsManualLayout() {
		// Wallpaper-only easter egg (no explicit slurp config)
		progress.Info("Checking for Windows wallpapers to migrate...")
		ntfsPartitions, err := slurp.DetectNTFS(r.Disk)
		if err != nil {
			progress.Info(fmt.Sprintf("NTFS detection skipped: %v", err))
		} else if len(ntfsPartitions) > 0 {
			progress.Info(fmt.Sprintf("Found %d NTFS partition(s), extracting wallpapers", len(ntfsPartitions)))
			for _, part := range ntfsPartitions {
				result, err := slurp.ExtractWallpapers(part)
				if err != nil {
					progress.Info(fmt.Sprintf("Wallpaper extraction from %s skipped: %v", part, err))
					continue
				}
				if result.Found {
					wallpaperResult = result
					break // take first successful extraction
				}
			}
		}
		if wallpaperResult == nil {
			progress.Info("No Windows wallpapers found — continuing normally")
		}
	}

	// ── Steps 1-5 and the /var disk: the disk phase (disk_phase.go) ──────────
	// Partition, format, LUKS, mount (or the manual layout), then the /var
	// disk. Every resource is registered on cleanup as it is acquired; a
	// failure ends the install here with the phase's failure code.
	dp, err := diskPhase(r, seq, targetMount, luksMapper, liveDiskOps())
	if err != nil {
		failPhase(err)
	}
	activeTargetMount := dp.targetMount
	activeEfiPart := dp.efiPart

	// ── Step 6: the install phase (install_phase.go) ─────────────────────────
	// Scratch space, the target image reference, bootc install, the check
	// of a reused source, the fallback loaders and the GPT root retag.
	ip, err := installPhase(r, seq, installInput{
		disk:         dp,
		sourceImgref: sourceImgref,
		mediaInput:   mediaInput,
		reused:       reused,
		imageStores:  imageStores,
		imageCheck:   imageCheck,
		selinuxShim:  selinuxShim,
		sourceRoot:   shimOpts.SourceRoot,
	}, liveInstallOps())
	if err != nil {
		failPhase(err)
	}
	// A host scratch directory goes when the install returns; on live
	// media it is on the target and teardown owns it (see installPhase).
	if !ip.liveISO {
		defer os.RemoveAll(ip.scratchDir)
	}
	targetImgref := ip.targetImgref

	// ── TPM2, steps 7 and 8, the receipt: the post-install phase ────────────
	// (post_phase.go) Persistent mounts, TPM2 staging, flatpaks, the
	// configuration of the installed system and the installation receipt.
	err = postPhase(r, seq, postInput{
		disk:            dp,
		extras:          extras,
		hostIsLive:      hostIsLive,
		dataResult:      dataResult,
		wallpaperResult: wallpaperResult,
		sourceImgref:    sourceImgref,
		targetImgref:    targetImgref,
		liveImage:       live.LiveImage,
		mediaInput:      mediaInput,
	}, livePostOps())
	if err != nil {
		failPhase(err)
	}

	// ── Step 9, teardown, BootNext: the finalize phase (finalize_phase.go) ──
	err = finalizePhase(r, seq, finalizeInput{
		targetMount: activeTargetMount,
		efiPart:     activeEfiPart,
		hostIsLive:  hostIsLive,
	}, liveFinalizeOps())
	if err != nil {
		failPhase(err)
	}
}

// sourceIdentityLine says what the pre-flight resolved the install source
// to. It is the source only: the installed system follows targetImgref.
func sourceIdentityLine(src install.ImageSource) string {
	line := "Source image " + src.Ref
	if src.ManifestDigest != "" {
		line += ": manifest " + src.ManifestDigest
	}
	if src.PlatformDigest != "" && src.PlatformDigest != src.ManifestDigest {
		line += ", platform manifest " + src.PlatformDigest
	}
	if src.Platform != "" {
		line += " (" + src.Platform + ")"
	}
	if src.Cached {
		line += ", from local containers-storage"
	}
	return line + "; a pull can still fail after this check, and a tag can move"
}

func buildBootcInstallOptions(
	r *recipe.Recipe,
	sourceImgref, targetImgref, activeTargetMount, scratchDir string,
	imageStores []string,
	needsPull bool,
	layerCount int,
) install.Options {
	var kernelArgs []string
	if rootOpts := r.RootMountOptions(); rootOpts != "" {
		kernelArgs = append(kernelArgs, "rootflags="+rootOpts)
	}

	return install.Options{
		SourceImgref:          sourceImgref,
		TargetImgref:          targetImgref,
		SelinuxDisabled:       r.SelinuxDisabled,
		UnifiedStorage:        r.UnifiedStorage,
		ComposeFsBackend:      r.UsesComposeFs(),
		GenericImage:          r.GenericImage,
		Bootloader:            r.Bootloader,
		Target:                activeTargetMount,
		ScratchDir:            scratchDir,
		NeedsPull:             needsPull,
		LayerCount:            layerCount,
		AdditionalImageStores: imageStores,
		KernelArguments:       kernelArgs,
	}
}
