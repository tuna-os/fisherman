package progress

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// This file owns the progress bar. fisherman used to emit only each step's
// cumulative_pct and weight_pct, and every frontend in tuna-os/bootc-installer
// re-derived the bar from them and from the substep messages below — five
// implementations in Python, C++, Rust and QML/JS of the same arithmetic.
// The Tracker is a port of the canonical one,
// bootc-installer/shared/progress/progress_parser.py, so fisherman can emit
// the finished number as overall_pct and the frontends only render it.
// testdata/fraction-cases.json is that parser's golden output; the Tracker
// must reproduce it (see bar_test.go).

// stepIDs maps each step name fisherman emits to its stable step_id.
//
// The ids are a public contract: frontends key their translated, rebrandable
// step labels on them. Never rename or reuse one. A renamed step keeps its
// old id; a new step gets a new id. step_name stays fisherman's English name.
var stepIDs = map[string]string{
	"Preparing disk":               "prepare_disk",
	"Partitioning disk":            "partition",
	"Formatting EFI partition":     "format_efi",
	"Setting up disk encryption":   "luks",
	"Formatting root filesystem":   "format_root",
	"Mounting filesystem":          "mount",
	"Formatting data disk (/var)":  "format_var",
	"Installing OS":                "install_os",
	"Enrolling TPM2 auto-unlock":   "tpm2_enroll",
	"Copying system Flatpaks":      "flatpaks",
	"Configuring installed system": "configure",
	"Finalizing installation":      "finalize",
}

// StepID returns the stable step_id for a step name, or "" for a name that
// is not in the table (the step event then omits step_id).
func StepID(name string) string {
	return stepIDs[name]
}

// StepIDs returns a copy of the step-name to step_id table.
func StepIDs() map[string]string {
	out := make(map[string]string, len(stepIDs))
	for k, v := range stepIDs {
		out[k] = v
	}
	return out
}

// pullShare is the share of a step's weight that the image layer pull
// covers. The rest is left for the export, deploy and bootloader phases that
// follow it, which have no counter of their own. (_PULL_SHARE)
const pullShare = 0.6

// maxBeforeComplete caps overall_pct until the complete event, which alone
// reports 100. The weight profile already never starts a step above 99.
const maxBeforeComplete = 99.0

// phaseMilestones gives the silent phases after the pull a fixed position, as
// a share of what is left of the step once the pull is over (all of it on an
// offline install, where nothing is pulled). Matched as message prefixes in
// this order; the messages are the ones ClassifyLine and the install path in
// internal/install/bootc.go emit. (_PHASE_MILESTONES)
var phaseMilestones = []struct {
	prefix string
	share  float64
}{
	{"Exporting image to OCI layout", 0.05},
	{"OCI export complete", 0.30},
	{"Using ", 0.32}, // "Using overlay storage driver ..."
	{"Initializing ostree layout", 0.35},
	{"Writing ", 0.35}, // "Writing 64 (3.7 GB) to disk ..."
	{"Deploying image", 0.35},
	{"OS deployed, installing bootloader", 0.90},
	{"Detected bootloader", 0.92},
	{"Installing bootloader", 0.92},
	{"Configuring EFI boot entry", 0.95},
	{"Configuring GRUB", 0.95},
	{"Configuring SELinux", 0.95},
	{"Generating initramfs", 0.95},
	{"bootc installation complete", 1.0},
}

var (
	// "Pulling image: layer 23/71" (internal/install/bootc.go). Prefix match,
	// like Python's re.match.
	reLayer = regexp.MustCompile(`^Pulling image: layer (\d+)/(\d+)`)
	// "Copying Flatpak data: 42%" (internal/post/post.go). Not in the
	// reference parser: see Tracker.Substep.
	reFlatpakCopy = regexp.MustCompile(`^Copying Flatpak data: (\d+)%`)
)

func milestone(msg string) (float64, bool) {
	for _, m := range phaseMilestones {
		if strings.HasPrefix(msg, m.prefix) {
			return m.share, true
		}
	}
	return 0, false
}

// Tracker computes the overall progress bar from the step and substep events
// fisherman emits. It is not safe for concurrent use; the package-level
// emitters serialise access to theirs.
type Tracker struct {
	step         int
	cumulative   float64
	weight       float64
	stepFrac     float64 // progress through the current step, 0..1
	postPullBase float64 // stepFrac when the first post-pull phase arrived
	hasBase      bool
	bar          float64 // 0..100, never decreases
	complete     bool
}

// NewTracker returns a Tracker at 0%.
func NewTracker() *Tracker { return &Tracker{} }

// Percent is the current bar position, 0..100, at full precision.
func (t *Tracker) Percent() float64 { return t.bar }

// set moves the bar, never backwards, and below 100 until complete.
func (t *Tracker) set(pct float64) {
	if !t.complete {
		pct = math.Min(pct, maxBeforeComplete)
	}
	t.bar = math.Max(t.bar, pct)
}

// Step records the start of a step and returns the bar position: the step's
// cumulative_pct. A step number that does not advance is ignored, as in the
// reference parser.
func (t *Tracker) Step(step, cumulativePct, weightPct int) float64 {
	if t.complete || (t.step > 0 && step <= t.step) {
		return t.bar
	}
	t.step = step
	t.cumulative = float64(cumulativePct)
	t.weight = float64(weightPct)
	t.stepFrac = 0
	t.postPullBase = 0
	t.hasBase = false
	t.set(t.cumulative)
	return t.bar
}

// Substep advances the bar within the current step from a substep message and
// returns the bar position. Messages it does not recognise, and any message in
// a zero-weight step, hold the bar where it is.
//
//   - "Pulling image: layer N/M" moves through the first 60% (pullShare) of
//     the step.
//   - The post-pull phases (phaseMilestones) sit at fixed points in what is
//     left of the step after the pull.
//   - "Copying Flatpak data: N%" moves through the whole step. This is a
//     deliberate extension of the reference parser, which ignored it, so the
//     bar used to stand still for the whole Flatpak copy (11-29% of the bar).
func (t *Tracker) Substep(msg string) float64 {
	if t.complete || msg == "" || t.weight <= 0 {
		return t.bar
	}
	frac, ok := t.stepFracFor(msg)
	if !ok {
		return t.bar
	}
	// Never move backwards: a retried pull restarts its layer count.
	t.stepFrac = math.Max(t.stepFrac, frac)
	t.set(t.cumulative + t.stepFrac*t.weight)
	return t.bar
}

func (t *Tracker) stepFracFor(msg string) (float64, bool) {
	if m := reLayer.FindStringSubmatch(msg); m != nil {
		done, err1 := strconv.Atoi(m[1])
		total, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil || total <= 0 {
			return 0, false
		}
		return math.Min(float64(done)/float64(total), 1) * pullShare, true
	}
	if m := reFlatpakCopy.FindStringSubmatch(msg); m != nil {
		pct, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, false
		}
		return math.Min(float64(pct)/100, 1), true
	}
	share, ok := milestone(msg)
	if !ok {
		return 0, false
	}
	if !t.hasBase {
		t.postPullBase = t.stepFrac
		t.hasBase = true
	}
	return t.postPullBase + share*(1-t.postPullBase), true
}

// Complete takes the bar to 100.
func (t *Tracker) Complete() float64 {
	t.complete = true
	t.bar = 100
	return t.bar
}

// roundPct rounds a bar position to the precision overall_pct is emitted at:
// two decimals, finer than any bar can draw and short on the wire.
func roundPct(pct float64) float64 {
	return math.Round(pct*100) / 100
}
