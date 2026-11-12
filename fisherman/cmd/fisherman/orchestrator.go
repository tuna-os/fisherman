package main

import "os"

// The install orchestrator (#313). main() parses the command line; an
// install is installer.run, which owns the pipeline: the pre-flight, the
// pre-partition host work, then the step phases in order (disk, install,
// post-install, finalize). Each phase owns its domain and its operations
// (the *Ops structs, bound to the host by the live* constructors), takes
// what it needs as arguments, and returns what the next phase reads. None
// of them ends the process: a failure comes back as a *phaseError, and run
// ends the install with failPhase (installer.fail), which emits the same
// error event and runs the same teardown the inline fatal call did.
//
// The step contract (steps_test.go) replays run's body with the step
// phases spliced in, so run calls each one exactly once, as a statement of
// its own, with its own r and seq.

// installer is one install's pipeline: the operations of each phase, how
// the install arms its cancel paths, and how it ends on a failure.
type installer struct {
	// arm makes SIGTERM, SIGINT, SIGHUP and the parent's death cancel
	// the install.
	arm func()
	// fail ends the install for a phase's error (failPhase); it does not
	// return.
	fail func(err error)

	preflight    preflightOps
	prepartition prepartitionOps
	disk         diskOps
	install      installOps
	post         postOps
	finalize     finalizeOps
}

// liveInstaller is the installer bound to the host, the disk and the
// install's cleanup, swap, freeze and terminator registries.
func liveInstaller() installer {
	return installer{
		arm: func() {
			term.watchSignals()
			term.armParentDeathCancel()
		},
		fail:         failPhase,
		preflight:    livePreflightOps(),
		prepartition: livePrepartitionOps(),
		disk:         liveDiskOps(),
		install:      liveInstallOps(),
		post:         livePostOps(),
		finalize:     liveFinalizeOps(),
	}
}

// run installs the recipe at recipeArg (a path, or "-" for stdin). It
// returns only after the complete event; every failure and every cancel
// ends the process inside.
func (in installer) run(recipeArg string) {
	// From here on this is an install. SIGTERM, SIGINT and SIGHUP cancel it
	// through the same teardown as fatal() and exit with exitCancelled. So
	// does the death of fisherman's parent (the frontend's wrapper), which is
	// how an unprivileged frontend cancels a root fisherman; see
	// parentdeath_linux.go.
	in.arm()

	// ── Pre-flight (preflight_phase.go) ──────────────────────────────────────
	// Root, the lock, the recipe and every check before any disk step. The
	// step plan is built here; the sequencer announces each planned step.
	pre, err := preflightPhase(recipeArg, in.preflight)
	if err != nil {
		in.fail(err)
	}
	r, seq := pre.r, pre.seq

	// ── Pre-partition (prepartition_phase.go) ────────────────────────────────
	// The live session's audio names and the Windows data migration, before
	// partitioning destroys the data. Non-fatal.
	pp := prepartitionPhase(r, pre.extras, pre.hostIsLive, in.prepartition)

	// ── Steps 1-5 and the /var disk: the disk phase (disk_phase.go) ──────────
	// Partition, format, LUKS, mount (or the manual layout), then the /var
	// disk. Every resource is registered on cleanup as it is acquired; a
	// failure ends the install here with the phase's failure code.
	dp, err := diskPhase(r, seq, pre.targetMount, pre.luksMapper, in.disk)
	if err != nil {
		in.fail(err)
	}

	// ── Step 6: the install phase (install_phase.go) ─────────────────────────
	// Scratch space, the target image reference, bootc install, the check
	// of a reused source, the fallback loaders and the GPT root retag.
	ip, err := installPhase(r, seq, installInput{
		disk:         dp,
		sourceImgref: pre.sourceImgref,
		mediaInput:   pre.mediaInput,
		reused:       pre.reused,
		imageStores:  pre.imageStores,
		imageCheck:   pre.imageCheck,
		selinuxShim:  pre.selinuxShim,
		sourceRoot:   pre.sourceRoot,
	}, in.install)
	if err != nil {
		in.fail(err)
	}
	// A host scratch directory goes when the install returns; on live
	// media it is on the target and teardown owns it (see installPhase).
	if !ip.liveISO {
		defer os.RemoveAll(ip.scratchDir)
	}

	// ── TPM2, steps 7 and 8, the receipt: the post-install phase ────────────
	// (post_phase.go) Persistent mounts, TPM2 staging, flatpaks, the
	// configuration of the installed system and the installation receipt.
	err = postPhase(r, seq, postInput{
		disk:            dp,
		extras:          pre.extras,
		hostIsLive:      pre.hostIsLive,
		dataResult:      pp.dataResult,
		wallpaperResult: pp.wallpaperResult,
		sourceImgref:    pre.sourceImgref,
		targetImgref:    ip.targetImgref,
		liveImage:       pre.live.LiveImage,
		mediaInput:      pre.mediaInput,
	}, in.post)
	if err != nil {
		in.fail(err)
	}

	// ── Step 9, teardown, BootNext: the finalize phase (finalize_phase.go) ──
	err = finalizePhase(r, seq, finalizeInput{
		targetMount: dp.targetMount,
		efiPart:     dp.efiPart,
		hostIsLive:  pre.hostIsLive,
	}, in.finalize)
	if err != nil {
		in.fail(err)
	}
}
