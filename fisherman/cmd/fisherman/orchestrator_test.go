package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The orchestrator (#313): installer.run with every phase's operations
// recorded. It runs the phases in order, hands each one what the earlier
// ones returned, announces the whole plan, and stops at the first failing
// phase with that phase's error.

type pipeline struct {
	order    []string // the first operation of each phase, the arming, the end
	steps    []string
	pre      *preflightRecorder
	prep     *prepartitionRecorder
	disk     *phaseRecorder
	install  *installRecorder
	post     *postRecorder
	finalize *finalizeRecorder
	failed   error
	// sourceRoot: the pre-flight establishes the source root (the goldens
	// have it unset).
	sourceRoot bool
}

// errFailed stops run where the live failPhase would end the process.
type errFailed struct{ err error }

func newPipeline(t *testing.T, variant string) *pipeline {
	t.Helper()
	pl := &pipeline{
		pre:      newPreflightRecorder(t, variant),
		prep:     &prepartitionRecorder{},
		disk:     &phaseRecorder{},
		install:  &installRecorder{},
		post:     &postRecorder{},
		finalize: &finalizeRecorder{bootID: "0003"},
	}
	return pl
}

func (pl *pipeline) installer() installer {
	mark := func(s string) { pl.order = append(pl.order, s) }
	in := installer{
		arm:          func() { mark("arm") },
		fail:         func(err error) { panic(errFailed{err}) },
		preflight:    pl.pre.ops(),
		prepartition: pl.prep.ops(),
		disk:         pl.disk.ops(),
		install:      pl.install.ops(),
		post:         pl.post.ops(),
		finalize:     pl.finalize.ops(),
	}
	geteuid := in.preflight.geteuid
	in.preflight.geteuid = func() int { mark("preflight"); return geteuid() }
	in.preflight.newSequencer = func(plan []plannedStep) *stepSequencer {
		seq := newStepSequencer(plan)
		seq.emitFn = func(step, total int, name string, cum, weight int) {
			pl.steps = append(pl.steps, fmt.Sprintf("%d/%d %s %d+%d", step, total, progress.StepID(name), cum, weight))
		}
		return seq
	}
	establish := in.preflight.establishSourceRoot
	in.preflight.establishSourceRoot = func(o install.Options, stores []string) install.SourceRoot {
		if root := establish(o, stores); pl.sourceRoot {
			return root
		}
		return install.SourceRoot{}
	}
	// No desktop extras, so each phase's log is its golden.
	in.preflight.resolveExtras = func(*recipe.Recipe, ...string) (recipe.Extras, string) {
		return recipe.Extras{}, "Desktop extras: none"
	}
	detect := in.prepartition.detectNTFS
	in.prepartition.detectNTFS = func(d string) ([]string, error) { mark("prepartition"); return detect(d) }
	partition := in.disk.partitionLayout
	in.disk.partitionLayout = func(dev string, l disk.Layout) error { mark("disk"); return partition(dev, l) }
	scratch := in.install.spaceConstrained
	in.install.spaceConstrained = func() bool { mark("install"); return scratch() }
	persist := in.post.persistMounts
	in.post.persistMounts = func(r *recipe.Recipe, target string, m func(string)) error {
		mark("post")
		return persist(r, target, m)
	}
	scratchRm := in.finalize.removeTargetScratch
	in.finalize.removeTargetScratch = func() error { mark("finalize"); return scratchRm() }
	complete := in.finalize.complete
	in.finalize.complete = func(msg, id string) { mark("complete"); complete(msg, id) }
	return in
}

func (pl *pipeline) run(t *testing.T) {
	t.Helper()
	defer func() {
		if v := recover(); v != nil {
			f, ok := v.(errFailed)
			if !ok {
				panic(v)
			}
			pl.failed = f.err
		}
	}()
	pl.installer().run("-")
}

func TestInstallerRunsThePhasesInOrder(t *testing.T) {
	for _, variant := range []string{"plain", "tpm2", "zfs", "systemd-boot composefs"} {
		t.Run(variant, func(t *testing.T) {
			pl := newPipeline(t, variant)
			pl.pre.r.SlurpWallpapers = true
			pl.run(t)
			if pl.failed != nil {
				t.Fatalf("the install failed: %v", pl.failed)
			}
			want := []string{"arm", "preflight", "prepartition", "disk", "install", "post", "finalize", "complete"}
			if !reflect.DeepEqual(pl.order, want) {
				t.Errorf("order = %v, want %v", pl.order, want)
			}
			// The whole plan, once, in order, from the one sequencer.
			plan := planSteps(pl.pre.r, true)
			var wantSteps []string
			for i, s := range plan {
				wantSteps = append(wantSteps, fmt.Sprintf("%d/%d %s %d+%d", i+1, len(plan), progress.StepID(s.name), s.cumulativePct, s.weightPct))
			}
			if !reflect.DeepEqual(pl.steps, wantSteps) {
				t.Errorf("steps = %v\nwant    %v", pl.steps, wantSteps)
			}
			// Each phase did what its own golden says.
			if g, ok := diskPhaseGolden[variant]; ok && !reflect.DeepEqual(withoutSteps(pl.disk.log), withoutSteps(g)) {
				t.Errorf("disk log = %v", pl.disk.log)
			}
			if g, ok := installPhaseGolden[variant]; ok && !reflect.DeepEqual(withoutSteps(pl.install.log), withoutSteps(g)) {
				t.Errorf("install log = %v", pl.install.log)
			}
			if g, ok := postPhaseGolden[variant]; ok && !reflect.DeepEqual(withoutSteps(pl.post.log), withoutSteps(g)) {
				t.Errorf("post log = %v", pl.post.log)
			}
			if g, ok := finalizePhaseGolden[variant]; ok && !reflect.DeepEqual(withoutSteps(pl.finalize.log), withoutSteps(g)) {
				t.Errorf("finalize log = %v", pl.finalize.log)
			}
		})
	}
}

// The recipe's mount point and mapper name reach the disk phase, and the
// disk phase's target and ESP reach the later phases.
func TestInstallerHandsResultsOn(t *testing.T) {
	pl := newPipeline(t, "luks")
	pl.pre.r.TargetMount, pl.pre.r.LuksMapperName = "/mnt/other", "other-root"
	pl.sourceRoot = true
	pl.pre.shim = [2]string{"/run/shim/x.so", "/run/shim"}
	pl.run(t)
	if pl.failed != nil {
		t.Fatal(pl.failed)
	}
	for _, w := range []string{"luksOpen /dev/sda3 correct horse other-root", "mkdir /mnt/other 755"} {
		if !strings.Contains(strings.Join(pl.disk.log, "\n"), w) {
			t.Errorf("disk log lacks %q: %v", w, pl.disk.log)
		}
	}
	if !strings.Contains(strings.Join(pl.install.log, "\n"), `kargs=[rd.luks.name=LUKS-UUID=root] shim="/run/shim/x.so" source-root=true`) {
		t.Errorf("the install phase did not get the LUKS UUID, the shim and the source root: %v", pl.install.log)
	}
	for _, w := range []string{"persistMounts /mnt/other", "luksBoot /mnt/other LUKS-UUID required=true",
		"stageReceipt /mnt/other ghcr.io/example/os:stable ghcr.io/example/os:stable media=false"} {
		if !strings.Contains(strings.Join(pl.post.log, "\n"), w) {
			t.Errorf("post log lacks %q: %v", w, pl.post.log)
		}
	}
	for _, w := range []string{"finalizeFS /mnt/other", "findBootNext /dev/sda1"} {
		if !strings.Contains(strings.Join(pl.finalize.log, "\n"), w) {
			t.Errorf("finalize log lacks %q: %v", w, pl.finalize.log)
		}
	}
}

// A failing phase ends the install with its own error; no later phase runs.
func TestInstallerStopsAtTheFailingPhase(t *testing.T) {
	tests := []struct {
		name  string
		mut   func(pl *pipeline)
		code  string
		order []string
	}{
		{"preflight", func(pl *pipeline) { pl.pre.euid = 1000 }, "", []string{"arm", "preflight"}},
		{"disk", func(pl *pipeline) { pl.disk.fail = "formatEFI" }, progress.FailureFormatFailed,
			[]string{"arm", "preflight", "disk"}},
		{"install", func(pl *pipeline) { pl.install.fail = "bootcInstall" }, progress.FailureBootcInstallFailed,
			[]string{"arm", "preflight", "disk", "install"}},
		{"post", func(pl *pipeline) { pl.post.fail = "writeHostname" }, progress.FailureHostnameFailed,
			[]string{"arm", "preflight", "disk", "install", "post"}},
		{"finalize", func(pl *pipeline) { pl.finalize.fail = "finalizeFS" }, progress.FailureFinalizeFailed,
			[]string{"arm", "preflight", "disk", "install", "post", "finalize"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pl := newPipeline(t, "plain")
			tt.mut(pl)
			pl.run(t)
			var pe *phaseError
			if !errors.As(pl.failed, &pe) {
				t.Fatalf("failed with %v, want a *phaseError", pl.failed)
			}
			if tt.code != "" && pe.code != tt.code {
				t.Errorf("code = %q, want %q", pe.code, tt.code)
			}
			if tt.code == "" && (pe.problem == nil || pe.problem.Code != recipe.CodeNotRoot) {
				t.Errorf("problem = %+v, want not_root", pe.problem)
			}
			if !reflect.DeepEqual(pl.order, tt.order) {
				t.Errorf("order = %v, want %v", pl.order, tt.order)
			}
		})
	}
}

func TestLiveInstallerSetsEveryPhase(t *testing.T) {
	in := liveInstaller()
	if in.arm == nil || in.fail == nil {
		t.Error("liveInstaller leaves arm or fail unset")
	}
}

// withoutSteps drops the step lines of a phase log: the pipeline's
// sequencer records the steps itself.
func withoutSteps(log []string) []string {
	var out []string
	for _, l := range log {
		if !strings.HasPrefix(l, "step ") {
			out = append(out, l)
		}
	}
	return out
}
