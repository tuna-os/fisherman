package main

import (
	"fmt"

	"github.com/tuna-os/fisherman/internal/deploy"
	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The finalize phase of an install (#313): the last step and the end of
// the install. The target-resident scratch goes while the target is still
// writable, step 9 replicates bootc's finalize (fstrim, remount
// read-only, fsfreeze/thaw; bootc ran with --skip-finalize so the
// post-install writes could land), a ZFS install gets its host ID and
// zpool.cache, then the install claims success, tears down, sets BootNext
// and emits the complete event.
//
// Like the other phases it never ends the process: the one fatal failure,
// the filesystem finalize, comes back as a *phaseError with the code and
// message main() used to pass to fatalCode. Everything after claimSuccess
// only warns.

// finalizeInput is what the finalize phase reads from the earlier phases.
type finalizeInput struct {
	// targetMount and efiPart are the disk phase's target and ESP.
	targetMount string
	efiPart     string
	// hostIsLive asks, at most once, whether the host is live media.
	hostIsLive func() bool
}

// finalizeOps are the operations the finalize phase performs.
// liveFinalizeOps is the real set.
type finalizeOps struct {
	removeTargetScratch func() error
	finalizeFS          func(target string) error
	resolveDeployment   func(target string) (*deploy.Deployment, error)
	writeHostID         func(etcDir string) error
	setZFSCachefile     func(pool, etcDir string) error
	claimSuccess        func()
	teardown            func() error
	findBootNext        func(efiPart string) (string, error)
	applyBootNext       func(r *recipe.Recipe, bootID string, isLive func() bool) string
	complete            func(message, bootID string)

	info func(msg string)
}

// liveFinalizeOps binds the finalize phase to the real operations, the
// install's cleanup, freeze registry and terminator.
func liveFinalizeOps() finalizeOps {
	return finalizeOps{
		removeTargetScratch: cleanup.RemoveTargetScratch,
		finalizeFS: func(target string) error {
			return disk.FinalizeFilesystemWith(target, freezes.Hooks())
		},
		resolveDeployment: post.ResolveDeployment,
		writeHostID:       disk.WriteHostID,
		setZFSCachefile:   disk.SetZFSCachefile,
		claimSuccess:      func() { term.claimSuccess() },
		teardown:          teardown,
		findBootNext:      post.FindBootNextID,
		applyBootNext:     applyBootNext,
		complete:          progress.Complete,
		info:              progress.Info,
	}
}

// finalizePhase finishes the install on the target the earlier phases
// wrote: step 9, the ZFS host files, teardown, BootNext and the complete
// event. The parameters are named r and seq for the step contract test
// (steps_test.go), which replays this body as part of main().
func finalizePhase(r *recipe.Recipe, seq *stepSequencer, in finalizeInput, ops finalizeOps) error {
	activeTargetMount := in.targetMount
	activeEfiPart := in.efiPart

	// Target-resident scratch (live media) goes now, while the target is
	// still mounted read-write: finalize makes it read-only, and after the
	// unmount the path no longer names the target's data (#307).
	if err := ops.removeTargetScratch(); err != nil {
		ops.info(fmt.Sprintf("Warning: could not remove the install scratch from the target: %v", err))
	}

	// ── Step 9: Finalize ─────────────────────────────────────────────────────
	// bootc's --skip-finalize kept the target writable for post-install writes.
	// Now replicate what bootc's finalize_filesystem() does internally:
	//   1. fstrim  — discard unused blocks (SSD optimization)
	//   2. remount ro — flush writeback, lock the deployment read-only
	//   3. fsfreeze/thaw — flush the journal for a clean first boot
	// ZFS handles this internally; fstrim/remount-ro/fsfreeze do not apply.
	seq.begin(stepFinalize)
	// Every planned step is announced by now; a gap is a bug the step
	// tests exist to catch, so say so rather than end the install.
	if err := seq.check(); err != nil {
		ops.info("Warning: " + err.Error())
	}
	if !r.IsZFS() {
		if err := ops.finalizeFS(activeTargetMount); err != nil {
			return phaseFail(progress.FailureFinalizeFailed, "finalizing target filesystem: %v", err)
		}
	}

	// ZFS post-install: write host ID and zpool.cache into the deployment's
	// /etc (the resolver's: ZFS installs are composefs, and <target>/etc is
	// not what boots), so the installed system can import the pool. Must be
	// done before unmounting. Teardown then exports the pool.
	if r.IsZFS() {
		poolName := disk.PoolName(r.ZFSPoolName)
		if d, err := ops.resolveDeployment(activeTargetMount); err != nil {
			ops.info(fmt.Sprintf("Warning: ZFS host ID and zpool.cache not written: %v", err))
		} else {
			ops.info("ZFS post-install: writing hostid and zpool.cache")
			if err := ops.writeHostID(d.Etc); err != nil {
				ops.info(fmt.Sprintf("Warning: ZFS host ID: %v", err))
			}
			if err := ops.setZFSCachefile(poolName, d.Etc); err != nil {
				ops.info(fmt.Sprintf("Warning: ZFS cachefile: %v", err))
			}
		}
	}

	// Tear down mounts and LUKS before declaring success. Claiming the exit
	// first means a cancel signal from here on is ignored (the install has
	// finished), and a cancel that already started keeps the exit to itself.
	ops.claimSuccess()
	if err := ops.teardown(); err != nil {
		ops.info(fmt.Sprintf("Warning: teardown after install was incomplete: %v", err))
	}

	// Find the installed system's EFI boot entry, and make it the next boot
	// while fisherman is root: a frontend runs as the desktop user and cannot
	// write an EFI variable (tuna-os/bootc-installer#318). The id still goes
	// out in the complete event.
	// Non-fatal: on VMs or systems without efibootmgr this may return empty.
	bootID, err := ops.findBootNext(activeEfiPart)
	if err != nil {
		ops.info(fmt.Sprintf("Warning: could not determine EFI boot entry: %v", err))
		bootID = ""
	} else if bootID != "" {
		ops.info(fmt.Sprintf("EFI boot entry for installed system: Boot%s", bootID))
	}
	ops.info(ops.applyBootNext(r, bootID, in.hostIsLive))

	ops.complete("Installation complete!", bootID)

	return nil
}
