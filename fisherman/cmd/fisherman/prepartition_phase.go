package main

import (
	"fmt"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"

	"github.com/tuna-os/wootc/slurp"
)

// The pre-partition phase of an install (#313): what has to happen on the
// host after the pre-flight admitted the install and before the disk phase
// wipes the target. Friendly audio names for the live session, and the
// Windows data (or wallpapers) migrated off the target disk while they
// still exist. Everything here is non-fatal: a failure is an info line and
// the install goes on, as before. It announces no step.

// prepartitionResult is the Windows data held for the post-install phase
// (nil when nothing was found or asked for).
type prepartitionResult struct {
	dataResult      *slurp.DataSlurpResult
	wallpaperResult *slurp.WallpaperResult
}

// prepartitionOps are the host operations of the pre-partition phase.
// livePrepartitionOps is the real set.
type prepartitionOps struct {
	applyAudioLive    func() error
	extractData       func(cfg *slurp.SlurpConfig) (*slurp.DataSlurpResult, error)
	detectNTFS        func(disk string) ([]string, error)
	extractWallpapers func(part string) (*slurp.WallpaperResult, error)

	info func(msg string)
}

func livePrepartitionOps() prepartitionOps {
	return prepartitionOps{
		applyAudioLive:    post.ApplyAudioConfigLive,
		extractData:       slurper.ExtractData,
		detectNTFS:        slurper.DetectNTFS,
		extractWallpapers: slurper.ExtractWallpapers,
		info:              progress.Info,
	}
}

// prepartitionPhase runs the live-session audio configuration (extras
// permitting) and the Windows migration for r, before any disk step.
func prepartitionPhase(r *recipe.Recipe, extras recipe.Extras, hostIsLive func() bool, ops prepartitionOps) prepartitionResult {

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

	// ── Pre-partition: Wallpaper slurp (easter egg) ──────────────────────────
	// Extract Windows wallpapers from any NTFS partition on the target disk
	// before partitioning destroys them. Held in RAM (/run). Entirely non-fatal.
	var wallpaperResult *slurp.WallpaperResult
	var dataResult *slurp.DataSlurpResult

	if r.Slurp != nil && !r.IsManualLayout() {
		// Full data slurp: user selected specific categories in the GUI
		ops.info("Migrating Windows user data before partitioning...")
		cfg := &slurp.SlurpConfig{
			SourcePartition: r.Slurp.SourcePartition,
		}
		for _, u := range r.Slurp.Users {
			cfg.Users = append(cfg.Users, slurp.SlurpUserConfig{
				Name:       u.Name,
				Categories: u.Categories,
			})
		}
		result, err := ops.extractData(cfg)
		if err != nil {
			ops.info(fmt.Sprintf("Data migration skipped: %v", err))
		} else {
			dataResult = result
		}
	} else if r.SlurpWallpapers && !r.IsManualLayout() {
		// Wallpaper-only easter egg (no explicit slurp config)
		ops.info("Checking for Windows wallpapers to migrate...")
		ntfsPartitions, err := ops.detectNTFS(r.Disk)
		if err != nil {
			ops.info(fmt.Sprintf("NTFS detection skipped: %v", err))
		} else if len(ntfsPartitions) > 0 {
			ops.info(fmt.Sprintf("Found %d NTFS partition(s), extracting wallpapers", len(ntfsPartitions)))
			for _, part := range ntfsPartitions {
				result, err := ops.extractWallpapers(part)
				if err != nil {
					ops.info(fmt.Sprintf("Wallpaper extraction from %s skipped: %v", part, err))
					continue
				}
				if result.Found {
					wallpaperResult = result
					break // take first successful extraction
				}
			}
		}
		if wallpaperResult == nil {
			ops.info("No Windows wallpapers found — continuing normally")
		}
	}

	return prepartitionResult{dataResult: dataResult, wallpaperResult: wallpaperResult}
}
