package main

import (
	"fmt"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The pre-partition phase of an install (#313): what has to happen on the
// host after the pre-flight admitted the install and before the disk phase
// wipes the target. Today that is the friendly audio names for the live
// session. Everything here is non-fatal: a failure is an info line and the
// install goes on, as before. It announces no step.

// prepartitionOps are the host operations of the pre-partition phase.
// livePrepartitionOps is the real set.
type prepartitionOps struct {
	applyAudioLive func() error

	info func(msg string)
}

func livePrepartitionOps() prepartitionOps {
	return prepartitionOps{
		applyAudioLive: post.ApplyAudioConfigLive,
		info:           progress.Info,
	}
}

// prepartitionPhase runs the live-session audio configuration (extras
// permitting), before any disk step.
func prepartitionPhase(extras recipe.Extras, hostIsLive func() bool, ops prepartitionOps) {

	// ── Immediate: Apply friendly audio names to live session ─────────────
	// Detect hardware, rename ugly ALSA names, hide S/PDIF etc. This changes
	// the RUNNING session (the user's WirePlumber configuration and the
	// host's /etc/wireplumber), so it runs only on live media, where that
	// session is the installer's own. Non-fatal.
	if extras.AudioNames {
		if !hostIsLive() {
			ops.info("Live audio config: skipped, this host is not live media and its session is not the installer's to change")
		} else if err := ops.applyAudioLive(); err != nil {
			ops.info(fmt.Sprintf("Live audio config skipped: %v", err))
		}
	}
}
