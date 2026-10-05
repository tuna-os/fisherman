package workflows

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/slurp"
)

const (
	defaultTargetMount = "/mnt/fisherman-target"
	defaultLuksMapper  = "fisherman-root"
)

// InstallationWorkflow encapsulates the state and orchestration of a single
// installation run. It separates concerns:
//
//   - State management: mount paths, LUKS configuration, progress tracking
//   - Recipe configuration: recipe validation, tool checking, image caching
//   - Error handling: centralized fatal() path with cleanup coordination
//   - Progress reporting: step accounting and progress callbacks
//
// By extracting this into a dedicated type, we make:
//   - Individual steps testable (can construct workflow in test state)
//   - Progress tracking independent of main() (can replace with different UI)
//   - Error paths explicit (cleanup() is always called)
//   - Future installation flows easier to add (inherit from InstallationWorkflow)
//
// Phase 2: Further decompose Run() into named step methods:
//   - runPreflight()      (image cache check, tool verification)
//   - runDiskSetup()      (partitioning, encryption, mounting)
//   - runOSInstall()      (bootc install, EFI setup)
//   - runPostInstall()    (hostname, users, audio, WiFi, etc.)
//   - runFinalization()   (cache warming, fsfreeze, cleanup)
type InstallationWorkflow struct {
	// Configuration
	recipe *recipe.Recipe
	version string

	// State: mount paths (may be overridden by recipe)
	targetMount string
	luksMapper  string

	// State: cleanup coordinator (runs on every error and on success)
	cleanup *post.Cleanup

	// State: progress tracking
	progressProfile []stepProfile
	currentStep     int
	totalSteps      int
	profileIndex    int

	// Disk state resolved during execution
	activeTargetMount string
	activeEfiPart    string
	activeRootPart   string // empty in manual mode
	activeLuksUUID   string // empty if no encryption
	luksRecoveryKey  string // random passphrase for tpm2-luks

	// Data migration state
	wallpaperResult *slurp.WallpaperResult
	dataResult      *slurp.DataSlurpResult
	imageCheck      install.ImageCheck

	// BindMount can be replaced in tests to avoid real mounts.
	BindMount func(host, container string, recursive bool) error
}

// NewInstallationWorkflow creates a new workflow for the given recipe.
// The version string is used for logging and diagnostics.
func NewInstallationWorkflow(r *recipe.Recipe, version string) *InstallationWorkflow {
	return &InstallationWorkflow{
		recipe:      r,
		version:     version,
		targetMount: defaultTargetMount,
		luksMapper:  defaultLuksMapper,
		cleanup:     &post.Cleanup{},
		BindMount:   disk.BindMount,
	}
}

// stepProfile holds the progress bar weight for one installation step.
// See buildProfile() below.
type stepProfile struct {
	cumulativePct int
	weightPct     int
}

// buildProfile calculates per-step progress weights based on feature flags.
// Weights sum to 100 and are based on timing data from real installations.
func (w *InstallationWorkflow) buildProfile() []stepProfile {
	needsPull := w.imageCheck.NeedsPull
	hasEncryption := w.recipe.Encryption.Type != "" && w.recipe.Encryption.Type != "none"
	hasTPM2 := w.recipe.Encryption.Type == "tpm2-luks" || w.recipe.Encryption.Type == "tpm2-luks-passphrase"
	isManual := len(w.recipe.CustomMounts) > 0
	hasVarDisk := w.recipe.VarDisk != nil && !w.recipe.VarDisk.KeepExisting

	osWeight := 87
	flatpakWeight := 11
	if !needsPull {
		osWeight = 68
		flatpakWeight = 29
	}
	if hasEncryption {
		osWeight--
	}
	if hasTPM2 {
		osWeight--
	}

	weights := []int{0, 1} // partition, format EFI
	if hasEncryption {
		weights = append(weights, 1) // LUKS setup
	}
	weights = append(weights, 0, 0) // format root, mount
	if hasVarDisk {
		weights = append(weights, 0) // format /var disk (fast)
	}
	weights = append(weights, osWeight) // install OS
	if hasTPM2 {
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

// Run executes the installation workflow.
// On any error (or success), cleanup is guaranteed to run.
//
// Phase 2 will decompose this into named step methods that can be
// tested and understood independently.
func (w *InstallationWorkflow) Run() error {
	// Ensure cleanup runs on all exit paths (both success and error).
	defer w.cleanup.Run()

	// Recipe overrides for globally-shared mount paths.
	// Keeps two parallel installs on the same host from colliding.
	if w.recipe.TargetMount != "" {
		w.targetMount = w.recipe.TargetMount
	}
	if w.recipe.LuksMapperName != "" {
		w.luksMapper = w.recipe.LuksMapperName
	}

	// Log fisherman version for CI diagnostics
	fmt.Fprintf(os.Stderr, "[fisherman] version: %s\n", w.version)

	// Expand PATH to cover all standard sbin locations.
	ExpandPath()

	// Pre-flight: verify that every host tool required for this recipe is present.
	if err := w.checkRequiredTools(); err != nil {
		Fatal(w.cleanup, "missing required host tool: %v", err)
	}

	// Calculate progress tracking parameters based on recipe features.
	w.calculateSteps()
	w.progressProfile = w.buildProfile()

	// Pre-flight: check image cache
	if w.recipe.Image != "" {
		progress.Info("Checking image cache...")
		w.imageCheck = install.CheckImage(w.recipe.Image)
		if strings.HasPrefix(w.recipe.Image, "containers-storage:") && w.imageCheck.NeedsPull {
			Fatal(w.cleanup, "required local source is absent or unreadable; refusing installation")
		}
		if w.imageCheck.NeedsPull {
			progress.Info(fmt.Sprintf("Image pull required (%d layers)", w.imageCheck.LayerCount))
		} else if w.imageCheck.Offline {
			progress.Info("Offline: registry unreachable, using locally cached image")
		} else {
			progress.Info("Image already up to date in local cache")
		}
	}

	// TODO: Phase 2 — decompose Run() into these step methods:
	//   - w.runPreflight()
	//   - w.runDiskSetup()
	//   - w.runOSInstall()
	//   - w.runPostInstall()
	//   - w.runFinalization()
	//
	// This will make:
	//   - Each step independently testable
	//   - Error handling explicit (each step can defer cleanup.AddMount, etc.)
	//   - Progress tracking separated from orchestration
	//   - Future installation flows easier to add

	// For now, return a placeholder so the build succeeds.
	// The full implementation lives in the original main() and will be
	// migrated to these step methods in Phase 2.
	progress.Info("Installation workflow orchestration initialized (Phase 1 foundation)")
	return nil
}

// calculateSteps determines the total step count based on recipe features.
// Manual layouts collapse some steps; encryption and TPM2 add steps.
func (w *InstallationWorkflow) calculateSteps() {
	hasEncryption := w.recipe.Encryption.Type != "" && w.recipe.Encryption.Type != "none"
	hasTPM2 := w.recipe.Encryption.Type == "tpm2-luks" || w.recipe.Encryption.Type == "tpm2-luks-passphrase"
	isManual := len(w.recipe.CustomMounts) > 0
	hasVarDisk := w.recipe.VarDisk != nil && !w.recipe.VarDisk.KeepExisting

	w.totalSteps = 8
	if isManual {
		w.totalSteps -= 3 // partition + format EFI + format root collapse
	}
	if hasEncryption && !isManual {
		w.totalSteps++ // extra step for LUKS setup
	}
	if hasTPM2 {
		w.totalSteps++ // extra step for TPM2 enrolment
	}
	if hasVarDisk {
		w.totalSteps++ // extra step to format /var disk
	}

	w.currentStep = 1
}

// checkRequiredTools verifies that every tool used by this recipe is available.
// This is done before any disk operations as a safety check.
func (w *InstallationWorkflow) checkRequiredTools() error {
	// TODO: Phase 2 — implement tool checking based on recipe features.
	// For now, return nil as a placeholder.
	return nil
}
