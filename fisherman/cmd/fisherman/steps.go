package main

import (
	"fmt"
	"strings"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The install steps, by the name progress.Step reports (each one has a
// step_id in internal/progress/bar.go). planSteps puts them in order for a
// recipe; main() announces each one through the step sequencer (#316).
const (
	stepPrepareDisk   = "Preparing disk"
	stepPartition     = "Partitioning disk"
	stepFormatEFI     = "Formatting EFI partition"
	stepEncryption    = "Setting up disk encryption"
	stepFormatRoot    = "Formatting root filesystem"
	stepMount         = "Mounting filesystem"
	stepFormatVarDisk = "Formatting data disk (/var)"
	stepInstallOS     = "Installing OS"
	stepEnrollTPM2    = "Enrolling TPM2 auto-unlock"
	stepFlatpaks      = "Copying system Flatpaks"
	stepConfigure     = "Configuring installed system"
	stepFinalize      = "Finalizing installation"
)

// plannedStep is one slot of the install's step plan: the step's name and
// its share of the progress bar. Weights come from the timing of a
// yellowfin gnome-hwe loop-device install (264s uncached, ~111s cached)
// and sum to 100; cumulativePct is the bar position at the step's start.
type plannedStep struct {
	name          string
	cumulativePct int
	weightPct     int
}

// stepShape is what decides the step list: the recipe facts below (see
// shapeOf) and nothing else.
type stepShape struct {
	manual    bool // customMounts: "Preparing disk" stands in for the five auto-partition steps
	luks      bool // the LUKS step runs
	tpm2      bool // the TPM2 enrolment step runs
	varFormat bool // a /var disk is formatted
}

// shapeOf reads a recipe's step shape. It is the one place the step list
// looks at the recipe.
func shapeOf(r *recipe.Recipe) stepShape {
	return stepShape{
		manual:    r.IsManualLayout(),
		luks:      r.EncryptsRoot(),
		tpm2:      r.EnrollsTPM2(),
		varFormat: r.FormatsVarDisk(),
	}
}

// planSteps is the ordered step list of an install of r. needsPull (the
// image must be pulled) only moves weight between the OS install and the
// Flatpak copy. Its length is the step total the frontend shows up front.
func planSteps(r *recipe.Recipe, needsPull bool) []plannedStep {
	return planShape(shapeOf(r), needsPull)
}

// planShape builds the plan for a shape. A manual layout emits a single
// "Preparing disk" step in place of partition/EFI/LUKS/root/mount, which
// takes their combined weight, and never emits LUKS setup or TPM2
// enrolment (both need the auto-partitioned root).
func planShape(s stepShape, needsPull bool) []plannedStep {
	if s.manual {
		s.luks, s.tpm2 = false, false
	}
	osWeight := 87
	flatpakWeight := 11
	if !needsPull {
		osWeight = 68
		flatpakWeight = 29
	}
	if s.luks {
		osWeight--
	}
	if s.tpm2 {
		osWeight--
	}

	type slot struct {
		name   string
		weight int
	}
	var slots []slot
	add := func(name string, weight int) { slots = append(slots, slot{name, weight}) }
	if s.manual {
		add(stepPrepareDisk, 1) // = partition + EFI + root + mount
	} else {
		add(stepPartition, 0)
		add(stepFormatEFI, 1)
		if s.luks {
			add(stepEncryption, 1)
		}
		add(stepFormatRoot, 0)
		add(stepMount, 0)
	}
	if s.varFormat {
		add(stepFormatVarDisk, 0) // fast
	}
	add(stepInstallOS, osWeight)
	if s.tpm2 {
		add(stepEnrollTPM2, 1)
	}
	add(stepFlatpaks, flatpakWeight)
	add(stepConfigure, 0)
	sum := 0
	for _, sl := range slots {
		sum += sl.weight
	}
	add(stepFinalize, 100-sum)

	plan := make([]plannedStep, len(slots))
	cumulative := 0
	for i, sl := range slots {
		plan[i] = plannedStep{sl.name, cumulative, sl.weight}
		cumulative += sl.weight
	}
	return plan
}

// stepSequencer announces the planned steps in order. main() calls begin
// at the start of each step; the sequencer supplies the step number, the
// total and the bar position, so no caller counts. A step out of plan is
// still announced (the install goes on) and recorded, and check reports
// it: the tests in steps_test.go hold main() to the plan, so that is a bug.
type stepSequencer struct {
	plan   []plannedStep
	next   int
	errs   []string
	emitFn func(step, total int, name string, cumulativePct, weightPct int)
}

func newStepSequencer(plan []plannedStep) *stepSequencer {
	return &stepSequencer{plan: plan, emitFn: progress.Step}
}

// total is the number of steps the install announces.
func (s *stepSequencer) total() int { return len(s.plan) }

// begin announces step name, the next one in the plan.
func (s *stepSequencer) begin(name string) {
	total := len(s.plan)
	if s.next >= total {
		s.errs = append(s.errs, fmt.Sprintf("step %q is past the end of the %d-step plan", name, total))
		cum := 100
		if total > 0 {
			last := s.plan[total-1]
			cum = last.cumulativePct
		}
		s.emitFn(total, total, name, cum, 0)
		return
	}
	slot := s.plan[s.next]
	if slot.name != name {
		s.errs = append(s.errs, fmt.Sprintf("step %d is %q, the plan says %q", s.next+1, name, slot.name))
	}
	s.next++
	s.emitFn(s.next, total, name, slot.cumulativePct, slot.weightPct)
}

// check reports a step announced out of plan, or a planned step that was
// never announced.
func (s *stepSequencer) check() error {
	errs := s.errs
	if s.next < len(s.plan) {
		var missing []string
		for _, p := range s.plan[s.next:] {
			missing = append(missing, fmt.Sprintf("%q", p.name))
		}
		errs = append(errs, "planned steps never announced: "+strings.Join(missing, ", "))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("progress steps out of plan: %s", strings.Join(errs, "; "))
}
