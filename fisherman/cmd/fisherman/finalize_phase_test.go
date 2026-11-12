package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The finalize phase (#313), run against recorded operations: nothing is
// trimmed, frozen, torn down or written to NVRAM. Each test drives
// finalizePhase for a step-contract variant and reads back one ordered
// log of the step event, the info lines, the operations, the success
// claim, the teardown and the complete event.

type finalizeRecorder struct {
	phaseRecorder
	bootID      string
	bootErr     error
	teardownErr error
	deployErr   error
}

func (p *finalizeRecorder) ops() finalizeOps {
	return finalizeOps{
		removeTargetScratch: func() error { return p.rec("cleanup.RemoveTargetScratch") },
		finalizeFS:          func(target string) error { return p.rec("finalizeFS %s", target) },
		resolveDeployment: func(target string) (*deploy.Deployment, error) {
			p.log = append(p.log, "resolveDeployment "+target)
			if p.deployErr != nil {
				return nil, p.deployErr
			}
			return &deploy.Deployment{Etc: target + "/state/deploy/etc"}, nil
		},
		writeHostID:     func(etc string) error { return p.rec("writeHostID %s", etc) },
		setZFSCachefile: func(pool, etc string) error { return p.rec("setZFSCachefile %s %s", pool, etc) },
		claimSuccess:    func() { p.log = append(p.log, "term.claimSuccess") },
		teardown: func() error {
			p.log = append(p.log, "teardown")
			return p.teardownErr
		},
		findBootNext: func(efi string) (string, error) {
			p.log = append(p.log, "findBootNext "+efi)
			return p.bootID, p.bootErr
		},
		applyBootNext: func(r *recipe.Recipe, id string, isLive func() bool) string {
			p.log = append(p.log, fmt.Sprintf("applyBootNext %q", id))
			return "BootNext: set to Boot" + id
		},
		complete: func(msg, id string) { p.log = append(p.log, fmt.Sprintf("event complete %q %q", msg, id)) },
		info:     func(msg string) { p.log = append(p.log, "info "+msg) },
	}
}

func (p *finalizeRecorder) run(t *testing.T, name string) (*stepSequencer, error) {
	t.Helper()
	r := phaseVariant(t, name)
	seq := p.sequencerAt(r, true, stepFinalize)
	in := finalizeInput{targetMount: phaseTarget, efiPart: "/dev/sda1", hostIsLive: func() bool { return false }}
	return seq, finalizePhase(r, seq, in, p.ops())
}

var finalizePhaseGolden = map[string][]string{
	"plain": {
		"cleanup.RemoveTargetScratch",
		"step 8/8 Finalizing installation [finalize] 99+1",
		"finalizeFS /mnt/fisherman-target",
		"term.claimSuccess",
		"teardown",
		"findBootNext /dev/sda1",
		"info EFI boot entry for installed system: Boot0003",
		`applyBootNext "0003"`,
		"info BootNext: set to Boot0003",
		`event complete "Installation complete!" "0003"`,
	},
	"zfs": {
		"cleanup.RemoveTargetScratch",
		"step 8/8 Finalizing installation [finalize] 99+1",
		"resolveDeployment /mnt/fisherman-target",
		"info ZFS post-install: writing hostid and zpool.cache",
		"writeHostID /mnt/fisherman-target/state/deploy/etc",
		"setZFSCachefile rpool /mnt/fisherman-target/state/deploy/etc",
		"term.claimSuccess",
		"teardown",
		"findBootNext /dev/sda1",
		"info EFI boot entry for installed system: Boot0003",
		`applyBootNext "0003"`,
		"info BootNext: set to Boot0003",
		`event complete "Installation complete!" "0003"`,
	},
}

func TestFinalizePhaseGolden(t *testing.T) {
	for name, want := range finalizePhaseGolden {
		t.Run(name, func(t *testing.T) {
			p := &finalizeRecorder{bootID: "0003"}
			seq, err := p.run(t, name)
			if err != nil {
				t.Fatalf("finalizePhase: %v", err)
			}
			if !reflect.DeepEqual(p.log, want) {
				t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(want, "\n  "))
			}
			plan := planSteps(phaseVariant(t, name), true)
			last := plan[len(plan)-1]
			w := fmt.Sprintf("step %d/%d %s [%s] %d+%d", len(plan), len(plan), last.name, progress.StepID(last.name), last.cumulativePct, last.weightPct)
			if len(p.stepLog) != 1 || p.stepLog[0] != w || last.name != stepFinalize {
				t.Errorf("steps = %v, want [%s]", p.stepLog, w)
			}
			if err := seq.check(); err != nil {
				t.Errorf("sequencer: %v", err)
			}
		})
	}
}

// The success claim comes before the teardown, and both after the last
// write to the target; the complete event is the last thing.
func TestFinalizePhaseOrder(t *testing.T) {
	p := &finalizeRecorder{}
	if _, err := p.run(t, "luks"); err != nil {
		t.Fatal(err)
	}
	idx := map[string]int{}
	for i, l := range p.log {
		idx[strings.Fields(l)[0]] = i
	}
	order := []string{"cleanup.RemoveTargetScratch", "finalizeFS", "term.claimSuccess", "teardown", "findBootNext", "event"}
	for i := 1; i < len(order); i++ {
		if idx[order[i-1]] >= idx[order[i]] {
			t.Errorf("%s does not come before %s: %v", order[i-1], order[i], p.log)
		}
	}
	if idx["event"] != len(p.log)-1 {
		t.Errorf("the complete event is not last: %v", p.log)
	}
}

// A step announced out of plan before finalize is a warning, not a failure.
func TestFinalizePhaseReportsDrift(t *testing.T) {
	r := phaseVariant(t, "plain")
	p := &finalizeRecorder{}
	seq := p.sequencerAt(r, true, stepInstallOS) // flatpaks and configure never announced
	in := finalizeInput{targetMount: phaseTarget, efiPart: "/dev/sda1", hostIsLive: func() bool { return false }}
	if err := finalizePhase(r, seq, in, p.ops()); err != nil {
		t.Fatal(err)
	}
	var warned bool
	for _, l := range p.log {
		warned = warned || strings.HasPrefix(l, "info Warning: progress steps out of plan: ")
	}
	if !warned {
		t.Errorf("no drift warning: %v", p.log)
	}
}

// Only the filesystem finalize is fatal, with main()'s code and message;
// nothing after it runs. Everything else warns with main()'s lines and the
// install still completes.
func TestFinalizePhaseFailures(t *testing.T) {
	p := &finalizeRecorder{}
	p.fail = "finalizeFS"
	_, err := p.run(t, "plain")
	var pe *phaseError
	if !errors.As(err, &pe) || pe.code != progress.FailureFinalizeFailed || pe.msg != "finalizing target filesystem: injected failure" {
		t.Fatalf("err = %#v", err)
	}
	if last := p.log[len(p.log)-1]; last != "finalizeFS /mnt/fisherman-target" {
		t.Errorf("the phase went on after finalize failed: %v", p.log)
	}

	tests := []struct {
		name    string
		variant string
		rec     finalizeRecorder
		line    string
		bootID  string
	}{
		{"scratch", "plain", finalizeRecorder{phaseRecorder: phaseRecorder{fail: "cleanup.RemoveTargetScratch"}},
			"info Warning: could not remove the install scratch from the target: injected failure", ""},
		{"teardown", "plain", finalizeRecorder{teardownErr: errors.New("umount busy")},
			"info Warning: teardown after install was incomplete: umount busy", ""},
		{"boot entry", "plain", finalizeRecorder{bootID: "0004", bootErr: errors.New("no efibootmgr")},
			"info Warning: could not determine EFI boot entry: no efibootmgr", ""},
		{"zfs deployment", "zfs", finalizeRecorder{deployErr: errors.New("no deployment")},
			"info Warning: ZFS host ID and zpool.cache not written: no deployment", ""},
		{"zfs host id", "zfs", finalizeRecorder{phaseRecorder: phaseRecorder{fail: "writeHostID"}},
			"info Warning: ZFS host ID: injected failure", ""},
		{"zfs cachefile", "zfs", finalizeRecorder{phaseRecorder: phaseRecorder{fail: "setZFSCachefile"}},
			"info Warning: ZFS cachefile: injected failure", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.rec
			if _, err := p.run(t, tt.variant); err != nil {
				t.Fatalf("the phase ended: %v", err)
			}
			found := false
			for _, l := range p.log {
				found = found || l == tt.line
			}
			want := fmt.Sprintf("event complete %q %q", "Installation complete!", tt.bootID)
			if !found || p.log[len(p.log)-1] != want {
				t.Errorf("want %q and %s, got %v", tt.line, want, p.log)
			}
		})
	}
}

func TestLiveFinalizeOpsSetsEveryOperation(t *testing.T) {
	v := reflect.ValueOf(liveFinalizeOps())
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Errorf("liveFinalizeOps leaves %s unset", v.Type().Field(i).Name)
		}
	}
}
