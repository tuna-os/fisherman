package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/reuse"
)

// The pre-flight of an install (#313): everything decided before the first
// disk step, so that every refusal leaves the disk untouched. Root, the
// host-wide install lock, the recipe, the media input, every rule
// `fisherman validate` enforces, the host tools, the kept filesystems,
// source reuse, the image source and cache, the SELinux label workaround,
// the step plan and the desktop extras.
//
// preflightPhase returns the recipe, the step sequencer and what the later
// phases read (preflightResult), and never ends the process: a refusal
// comes back as a *phaseError carrying the problem (or failure code, or
// load error) main() used to pass to fatalProblem (fatalCode, fatalLoad).

// preflightResult is what the pre-flight hands to the phases.
type preflightResult struct {
	r   *recipe.Recipe
	seq *stepSequencer
	// targetMount and luksMapper are the recipe's mount point and LUKS
	// mapper name, or the defaults.
	targetMount string
	luksMapper  string
	// mediaInput is the verified media input (nil without one), and
	// sourceImgref the resolved install source.
	mediaInput   *install.MediaInput
	sourceImgref string
	live         probe.Live
	reused       *reuse.Result
	imageStores  []string
	imageCheck   install.ImageCheck
	selinuxShim  string
	sourceRoot   install.SourceRoot
	extras       recipe.Extras
	// hostIsLive asks, at most once, whether the host booted live media.
	hostIsLive func() bool
}

// preflightOps are the host queries and registrations of the pre-flight.
// livePreflightOps is the real set.
type preflightOps struct {
	geteuid                  func() int
	acquireLock              func() error
	loadRecipe               func(arg string) (*recipe.Recipe, error)
	registerSecret           func(s string)
	resolveMediaInput        func(r *recipe.Recipe) (*install.MediaInput, error)
	hostLive                 func() probe.Live
	installProblems          func(r *recipe.Recipe, imageErr error) []recipe.Problem
	resolveTargets           func(r *recipe.Recipe)
	expandPath               func()
	checkTools               func(r *recipe.Recipe) error
	keptFilesystemProblems   func(r *recipe.Recipe) []recipe.Problem
	acquireSourceReuse       func(r *recipe.Recipe, register func(func() error)) (*reuse.Result, *recipe.Problem)
	addRelease               func(release func() error)
	resolveImageStores       func(r *recipe.Recipe) ([]string, bool)
	checkImage               func(ref string) install.ImageCheck
	establishSourceRoot      func(opts install.Options, stores []string) install.SourceRoot
	localSourceContextLine   func(opts install.Options) string
	prepareSelinuxWorkaround func(opts install.Options) (string, string, error)
	addPostRemoval           func(path string)
	resolveExtras            func(r *recipe.Recipe, refs ...string) (recipe.Extras, string)
	liveOnce                 func(known probe.Live) func() bool
	newSequencer             func(plan []plannedStep) *stepSequencer

	info func(msg string)
	// stderr writes a line of the diagnostic log (not a progress event).
	stderr func(msg string)
}

// livePreflightOps binds the pre-flight to the host and the install's
// cleanup. The seams tests replace (geteuid, acquireInstallLock,
// hostLive, prepareSelinuxWorkaround) are read at the call.
func livePreflightOps() preflightOps {
	return preflightOps{
		geteuid:                  func() int { return geteuid() },
		acquireLock:              func() error { return acquireInstallLock() },
		loadRecipe:               loadRecipe,
		registerSecret:           progress.RegisterSecret,
		resolveMediaInput:        resolveMediaInput,
		hostLive:                 func() probe.Live { return hostLive() },
		installProblems:          installProblemsWith,
		resolveTargets:           resolveTargets,
		expandPath:               expandPath,
		checkTools:               checkRequiredTools,
		keptFilesystemProblems:   keptFilesystemProblems,
		acquireSourceReuse:       acquireSourceReuse,
		addRelease:               cleanup.AddRelease,
		resolveImageStores:       resolveImageStores,
		checkImage:               install.CheckImage,
		establishSourceRoot:      establishSourceRoot,
		localSourceContextLine:   install.LocalSourceContextLine,
		prepareSelinuxWorkaround: func(o install.Options) (string, string, error) { return prepareSelinuxWorkaround(o) },
		addPostRemoval:           cleanup.AddPostRemoval,
		resolveExtras:            resolveExtras,
		liveOnce:                 liveOnce,
		newSequencer:             newStepSequencer,
		info:                     progress.Info,
		stderr:                   func(msg string) { fmt.Fprint(os.Stderr, msg) },
	}
}

// preflightPhase admits the install of the recipe at recipeArg (a path,
// or "-" for stdin) and prepares everything the phases need.
func preflightPhase(recipeArg string, ops preflightOps) (preflightResult, error) {
	var res preflightResult

	// An install partitions, formats and mounts: it needs root for its first
	// disk step, and before that step it already changes the host (the live
	// audio configuration, mount points, scratch space). Refuse here, before
	// the recipe is even read, rather than fail half-way.
	if ops.geteuid() != 0 {
		return res, phaseProblem("cannot install", recipe.Problem{Code: recipe.CodeNotRoot,
			Message: "fisherman must run as root to install (run it through pkexec or sudo); " +
				"`fisherman validate` and `fisherman probe` need no root"})
	}

	// One install per host at a time; see acquireInstallLock.
	if err := ops.acquireLock(); err != nil {
		if errors.Is(err, errInstallRunning) {
			return res, phaseProblem("cannot install", recipe.Problem{Code: recipe.CodeInstallAlreadyRunning,
				Message: err.Error() + "; wait for it to finish, or cancel it, then start this install again"})
		}
		return res, phaseFail(progress.FailureLockFailed, "cannot take the install lock %s: %v", installLockPath, err)
	}

	r, err := ops.loadRecipe(recipeArg)
	if err != nil {
		return res, phaseLoad(err)
	}
	res.r = r
	// No error event or failure line may carry these.
	ops.registerSecret(r.Encryption.Passphrase)
	ops.registerSecret(r.User.Password)

	// Resolve and verify host-owned media before live probing (bootc status),
	// store discovery or CheckImage can inspect a stale cache or the registry.
	// A sourceReuse recipe names its source itself, and is refused on live
	// media (reuseProblems), so no media input is bound for it.
	var mediaInput *install.MediaInput
	if r.SourceReuse == nil {
		mediaInput, err = ops.resolveMediaInput(r)
		if err != nil {
			return res, phaseFail(progress.FailureLocalSourceUnavailable, "required local source is absent or unreadable; refusing installation: %v", err)
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
		live = ops.hostLive()
	}
	if mediaInput != nil {
		live = probe.Live{IsLive: true, LiveImage: mediaInput.Image, DetectedBy: "verified media input"}
	}
	var imageErr error
	if r.Image == "" {
		imageErr = r.ValidateImage(live.IsLive)
	}
	if ps := ops.installProblems(r, imageErr); len(ps) > 0 {
		return res, phaseProblem("invalid recipe", ps[0])
	}
	ops.resolveTargets(r)

	// Recipe-level overrides for the otherwise-shared global mount paths.
	// Keeps two parallel installs on the same host from colliding.
	res.targetMount, res.luksMapper = defaultTargetMount, defaultLuksMapper
	if r.TargetMount != "" {
		res.targetMount = r.TargetMount
	}
	if r.LuksMapperName != "" {
		res.luksMapper = r.LuksMapperName
	}

	// Log fisherman version for CI diagnostics
	ops.stderr(fmt.Sprintf("[fisherman] version: %s\n", version))

	// Expand PATH to cover all standard sbin locations and any tools staged
	// alongside this binary (e.g. by the tuna-installer Flatpak).  pkexec
	// strips the calling user's PATH to a minimal safe set which often omits
	// /usr/sbin and /sbin on some immutable distros.
	ops.expandPath()

	// Pre-flight: verify that every host tool required for this recipe is
	// present before we touch any disks.
	if err := ops.checkTools(r); err != nil {
		return res, phaseProblem("missing required host tool", recipe.Problem{Code: recipe.CodeHostToolMissing, Message: err.Error()})
	}

	// A device the recipe keeps as it is must hold a mountable filesystem.
	// blkid says so now; the mount would say so after the disk is wiped.
	if ps := ops.keptFilesystemProblems(r); len(ps) > 0 {
		return res, phaseProblem("cannot keep the existing filesystem", ps[0])
	}

	// ── Source reuse (explicit opt-in, #384) ─────────────────────────────
	// Copy the image the recipe names by digest out of this host's bootc
	// storage, verify it, and install from the copy. Before any disk step:
	// a refusal or a failed copy leaves the target untouched. Absent
	// sourceReuse, nothing here runs.
	reused, reuseErr := ops.acquireSourceReuse(r, ops.addRelease)
	if reuseErr != nil {
		return res, phaseProblem("source reuse", *reuseErr)
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
		ops.info(msg)
	}

	// An OCI media source needs no store discovery or podman image listing.
	// Preserve explicitly supplied stores without probing unrelated caches.
	imageStores := r.AdditionalImageStores
	var discoveredStores bool
	if mediaInput == nil && reused == nil {
		imageStores, discoveredStores = ops.resolveImageStores(r)
	}
	if discoveredStores && len(imageStores) > 0 {
		ops.info("Offline image stores found on the host: " + strings.Join(imageStores, ", "))
	}

	// ── Pre-flight: check image cache ─────────────────────────────────────────
	var imageCheck install.ImageCheck
	if mediaInput != nil {
		imageCheck.Offline = true
		ops.info("Offline: using embedded media image " + mediaInput.OCILayoutPath)
	} else if reused != nil {
		// The verified copy, read locally; never a registry. Its identity
		// is checked by digest against the booted deployment
		// (acquireSourceReuse), so only its presence matters here.
		imageCheck = ops.checkImage(sourceImgref)
		if imageCheck.NeedsPull {
			return res, phaseProblem("source reuse", reuseProblem(recipe.CodeSourceReuseVerifyFailed,
				"the verified copy "+reused.Destination+" is no longer readable in containers-storage"))
		}
	} else if r.Image != "" {
		ops.info("Checking image cache...")
		imageCheck = ops.checkImage(r.Image)
		if install.IsOCISource(r.Image) && imageCheck.NeedsPull {
			return res, phaseFail(progress.FailureLocalSourceUnavailable, "required local source is absent or unreadable; refusing installation")
		}
		if strings.HasPrefix(r.Image, "containers-storage:") && imageCheck.NeedsPull {
			return res, phaseFail(progress.FailureLocalSourceUnavailable, "required local source is absent or unreadable; refusing installation")
		}
		// A source that is known to fail, or is not a bootc image for this
		// host, is refused here: the pull and bootc would refuse it only
		// after the disk is wiped (#364).
		if e := imageCheck.Err; e != nil {
			return res, phaseProblem("image source", recipe.Problem{Code: e.Code, Field: "image", Message: e.Message})
		}
		ops.info(sourceIdentityLine(imageCheck.Source))
		if imageCheck.NeedsPull {
			ops.info(fmt.Sprintf("Image pull required (%d layers)", imageCheck.LayerCount))
		} else if imageCheck.Offline {
			ops.info("Offline: registry unreachable, using locally cached image")
		} else {
			ops.info("Image found in local cache, no pull needed")
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
	shimOpts.SourceRoot = ops.establishSourceRoot(shimOpts, imageStores)
	if line := ops.localSourceContextLine(shimOpts); line != "" {
		ops.info(line)
	}
	selinuxShim, shimDir, shimErr := ops.prepareSelinuxWorkaround(shimOpts)
	if shimErr != nil {
		return res, phaseProblem("cannot prepare the install", recipe.Problem{Code: recipe.CodeSelinuxWorkaroundUnavailable, Message: shimErr.Error()})
	}
	if shimDir != "" {
		ops.addPostRemoval(shimDir)
		ops.info("SELinux: the target disables SELinux and this host enforces a policy; the label workaround is ready")
	}

	// The step plan for this recipe (steps.go). The sequencer announces
	// each step with its number, the plan's total and its bar position.
	seq := ops.newSequencer(planSteps(r, imageCheck.NeedsPull))

	// ── Desktop extras ───────────────────────────────────────────────────
	// Print services, the OEM brew unit, friendly audio names and the boot
	// splash suit a desktop product image. They are off unless the recipe
	// (`extras`) or the image catalog turns them on (#412).
	extraRefs := []string{r.TargetImgref, r.Image, live.LiveImage}
	if mediaInput != nil {
		extraRefs = append(extraRefs, mediaInput.TargetImgref, mediaInput.Image)
	}
	extras, extrasLine := ops.resolveExtras(r, extraRefs...)
	ops.info(extrasLine)

	// hostIsLive: the host booted from live media. Asked at most once.
	hostIsLive := ops.liveOnce(live)

	res.seq = seq
	res.mediaInput, res.sourceImgref, res.live = mediaInput, sourceImgref, live
	res.reused, res.imageStores, res.imageCheck = reused, imageStores, imageCheck
	res.selinuxShim, res.sourceRoot = selinuxShim, shimOpts.SourceRoot
	res.extras, res.hostIsLive = extras, hostIsLive
	return res, nil
}
