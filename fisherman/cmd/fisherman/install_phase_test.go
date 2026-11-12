package main

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/reuse"
)

// The install phase (#313), run against recorded operations: no bootc,
// sfdisk or mount runs. Like the disk phase tests, each test drives
// installPhase for a step-contract variant and reads back one ordered log:
// the step events, the info and stderr lines, every operation with its
// arguments (bootc's options in full), and the cleanup registration.

type installRecorder struct {
	phaseRecorder
	live      bool            // /var is space-constrained (live media)
	retag     bool            // the root partition has a stale GPT type
	reuseDiff *recipe.Problem // checkReused reports a different digest
	bootcErr  error           // bootcInstall fails with this
}

func (p *installRecorder) ops() installOps {
	return installOps{
		spaceConstrained: func() bool { p.log = append(p.log, "varIsSpaceConstrained"); return p.live },
		prepareScratch: func(target string, liveISO bool) (string, error) {
			dir := "/var/fisherman-tmp"
			if liveISO {
				dir = target + "/.fisherman-scratch"
			}
			return dir, p.rec("prepareScratch %s live=%v", target, liveISO)
		},
		addPostRemoval: func(path string) { p.log = append(p.log, "cleanup.AddPostRemoval "+path) },
		bootcInstall: func(o install.Options) error {
			err := p.rec("bootcInstall src=%s target-imgref=%q target=%s scratch=%s pull=%v layers=%d stores=%v kargs=%v shim=%q source-root=%v in-source=%v",
				o.SourceImgref, o.TargetImgref, o.Target, o.ScratchDir, o.NeedsPull, o.LayerCount,
				o.AdditionalImageStores, o.KernelArguments, o.SelinuxShimPath, o.SourceRoot.Established, o.RunInSourceContainer)
			if err == nil && p.bootcErr != nil {
				return p.bootcErr
			}
			return err
		},
		checkReused: func(target string, res *reuse.Result) (string, *recipe.Problem) {
			p.log = append(p.log, "checkReused "+target+" "+res.DestinationDigest)
			if p.reuseDiff != nil {
				return "", p.reuseDiff
			}
			return "Source reuse: the installed image is the verified copy (" + res.DestinationDigest + ")", nil
		},
		ensureSystemdBoot:  func(target string) error { return p.rec("ensureSystemdBoot %s", target) },
		ensureGrubFallback: func(target, distro string) error { return p.rec("ensureGrubFallback %s %q", target, distro) },
		partTypeRewrite: func(dev string, n int, want string) (bool, string) {
			p.log = append(p.log, fmt.Sprintf("partTypeRewrite %s %d", dev, n))
			if p.retag {
				return true, "0fc63daf-8483-4772-8e79-3d69d8477de4"
			}
			return false, want
		},
		unmountPartition: func(dev string, n int) error { return p.rec("unmountPartition %s %d", dev, n) },
		setPartitionType: func(dev string, n int, typ string) error { return p.rec("setPartitionType %s %d", dev, n) },
		mkdirAll:         func(path string, perm os.FileMode) error { return p.rec("mkdir %s %o", path, perm) },
		waitForNode:      func(part string, d time.Duration) error { return p.rec("waitForNode %s %v", part, d) },
		mountWithRetry: func(dev, target, opts string, n int, b time.Duration) error {
			return p.rec("mountWithRetry %s %s %q %d %v", dev, target, opts, n, b)
		},
		mountEFI: func(target, part string) error { return p.rec("mountEFI %s %s", target, part) },
		info:     func(msg string) { p.log = append(p.log, "info "+msg) },
		stderr:   func(msg string) { p.log = append(p.log, "stderr "+msg) },
	}
}

// installVariant is a variant with the input main() hands the install
// phase for it: the disk phase's golden result and the pre-flight's.
func installVariant(t *testing.T, name string) (*recipe.Recipe, installInput) {
	t.Helper()
	r := phaseVariant(t, name)
	in := installInput{
		sourceImgref: r.Image,
		imageCheck:   install.ImageCheck{NeedsPull: true, LayerCount: 7},
	}
	dr, ok := diskPhaseResults[name]
	if !ok {
		dr = diskPhaseResults["plain"]
	}
	in.disk = dr
	if r.SourceReuse != nil {
		in.reused = &reuse.Result{SourceImgref: "containers-storage:localhost/fisherman-reuse:1111",
			Destination: "localhost/fisherman-reuse:1111", DestinationDigest: "sha256:2222"}
		in.sourceImgref = in.reused.SourceImgref
		in.imageCheck = install.ImageCheck{}
	}
	return r, in
}

// sequencerAt is a recording sequencer for planSteps(r, pull) that has
// already announced every step before from, as main() has when it reaches
// the phase that begins with from.
func (p *phaseRecorder) sequencerAt(r *recipe.Recipe, pull bool, from string) *stepSequencer {
	seq := newStepSequencer(planSteps(r, pull))
	seq.emitFn = func(step, total int, name string, cum, weight int) {
		line := fmt.Sprintf("step %d/%d %s [%s] %d+%d", step, total, name, progress.StepID(name), cum, weight)
		p.log = append(p.log, line)
		p.stepLog = append(p.stepLog, line)
	}
	for seq.next < len(seq.plan) && seq.plan[seq.next].name != from {
		seq.next++
	}
	return seq
}

func (p *installRecorder) run(r *recipe.Recipe, in installInput) (installResult, *stepSequencer, error) {
	seq := p.sequencerAt(r, in.imageCheck.NeedsPull, stepInstallOS)
	res, err := installPhase(r, seq, in, p.ops())
	return res, seq, err
}

const (
	plainBootc = `bootcInstall src=ghcr.io/example/os:stable target-imgref="" target=/mnt/fisherman-target scratch=/var/fisherman-tmp pull=true layers=7 stores=[] kargs=[] shim="" source-root=false in-source=false`
	// btrfs without subvolumes: the root's compression reaches the entries.
	btrfsBootc = `bootcInstall src=ghcr.io/example/os:stable target-imgref="" target=/mnt/fisherman-target scratch=/var/fisherman-tmp pull=true layers=7 stores=[] kargs=[rootflags=compress=zstd:1] shim="" source-root=false in-source=false`
)

// installPhaseGolden is what the install phase does, in order, for each
// shape, checked line by line against main() before the extraction.
var installPhaseGolden = map[string][]string{
	"plain": {
		"varIsSpaceConstrained",
		"prepareScratch /mnt/fisherman-target live=false",
		"cleanup.AddPostRemoval /var/fisherman-tmp",
		"step 5/8 Installing OS [install_os] 1+87",
		plainBootc,
		`ensureGrubFallback /mnt/fisherman-target ""`,
	},
	"luks": {
		"varIsSpaceConstrained",
		"prepareScratch /mnt/fisherman-target live=false",
		"cleanup.AddPostRemoval /var/fisherman-tmp",
		"step 6/9 Installing OS [install_os] 2+86",
		`bootcInstall src=ghcr.io/example/os:stable target-imgref="" target=/mnt/fisherman-target scratch=/var/fisherman-tmp pull=true layers=7 stores=[] kargs=[rd.luks.name=LUKS-UUID=root] shim="" source-root=false in-source=false`,
		`ensureGrubFallback /mnt/fisherman-target ""`,
	},
	"btrfs subvolumes": {
		"varIsSpaceConstrained",
		"prepareScratch /mnt/fisherman-target live=false",
		"cleanup.AddPostRemoval /var/fisherman-tmp",
		"step 5/8 Installing OS [install_os] 1+87",
		`bootcInstall src=ghcr.io/example/os:stable target-imgref="" target=/mnt/fisherman-target scratch=/var/fisherman-tmp pull=true layers=7 stores=[] kargs=[rootflags=subvol=@,compress=zstd:1] shim="" source-root=false in-source=false`,
		`ensureGrubFallback /mnt/fisherman-target ""`,
	},
	"systemd-boot composefs": {
		"varIsSpaceConstrained",
		"prepareScratch /mnt/fisherman-target live=false",
		"cleanup.AddPostRemoval /var/fisherman-tmp",
		"step 5/8 Installing OS [install_os] 1+87",
		btrfsBootc,
		"ensureSystemdBoot /mnt/fisherman-target",
		"partTypeRewrite /dev/sda 2",
		"info Root partition already tagged for systemd GPT auto-discovery (" + disk.LinuxRootPartType() + ")",
	},
	"systemd-boot luks": {
		"varIsSpaceConstrained",
		"prepareScratch /mnt/fisherman-target live=false",
		"cleanup.AddPostRemoval /var/fisherman-tmp",
		"step 6/9 Installing OS [install_os] 2+86",
		btrfsBootc,
		"ensureSystemdBoot /mnt/fisherman-target",
	},
	"manual": {
		"varIsSpaceConstrained",
		"prepareScratch /mnt/fisherman-target live=false",
		"cleanup.AddPostRemoval /var/fisherman-tmp",
		"step 2/5 Installing OS [install_os] 1+87",
		plainBootc,
	},
	"source reuse": {
		"varIsSpaceConstrained",
		"prepareScratch /mnt/fisherman-target live=false",
		"cleanup.AddPostRemoval /var/fisherman-tmp",
		"step 5/8 Installing OS [install_os] 1+68",
		`bootcInstall src=containers-storage:localhost/fisherman-reuse:1111 target-imgref="ghcr.io/example/os:stable" target=/mnt/fisherman-target scratch=/var/fisherman-tmp pull=false layers=0 stores=[] kargs=[] shim="" source-root=false in-source=true`,
		"checkReused /mnt/fisherman-target sha256:2222",
		"info Source reuse: the installed image is the verified copy (sha256:2222)",
		`ensureGrubFallback /mnt/fisherman-target ""`,
	},
}

func TestInstallPhaseGolden(t *testing.T) {
	for name, want := range installPhaseGolden {
		t.Run(name, func(t *testing.T) {
			r, in := installVariant(t, name)
			p := &installRecorder{}
			res, seq, err := p.run(r, in)
			if err != nil {
				t.Fatalf("installPhase: %v", err)
			}
			if !reflect.DeepEqual(p.log, want) {
				t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(want, "\n  "))
			}
			wantRes := installResult{scratchDir: "/var/fisherman-tmp"}
			if r.SourceReuse != nil {
				wantRes.targetImgref = r.TargetImgref
			}
			if res != wantRes {
				t.Errorf("result = %+v, want %+v", res, wantRes)
			}
			// One step, the OS install, at its planned number and position.
			plan := planSteps(r, in.imageCheck.NeedsPull)
			if len(p.stepLog) != 1 || len(seq.errs) != 0 {
				t.Fatalf("steps %v, sequencer %v", p.stepLog, seq.errs)
			}
			for i, s := range plan {
				if s.name == stepInstallOS {
					w := fmt.Sprintf("step %d/%d %s [%s] %d+%d", i+1, len(plan), s.name, progress.StepID(s.name), s.cumulativePct, s.weightPct)
					if p.stepLog[0] != w {
						t.Errorf("step = %q, want %q", p.stepLog[0], w)
					}
				}
			}
		})
	}
}

// The retag of an unencrypted systemd-boot composefs root whose GPT type
// is stale: unmount, rewrite, wait for the node, remount with the root's
// options, remount the ESP.
func TestInstallPhaseRetag(t *testing.T) {
	r, in := installVariant(t, "systemd-boot composefs")
	p := &installRecorder{retag: true}
	if _, _, err := p.run(r, in); err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, installPhaseGolden["systemd-boot composefs"][:7]...),
		"info Retagging root partition for systemd GPT auto-discovery",
		"unmountPartition /dev/sda 1",
		"unmountPartition /dev/sda 2",
		"setPartitionType /dev/sda 2",
		"mkdir /mnt/fisherman-target 755",
		"waitForNode /dev/sda2 15s",
		`mountWithRetry /dev/sda2 /mnt/fisherman-target "compress=zstd:1" 5 500ms`,
		"mountEFI /mnt/fisherman-target /dev/sda1",
	)
	if !reflect.DeepEqual(p.log, want) {
		t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(want, "\n  "))
	}

	// The unmounts and the ESP remount only warn, on stderr.
	for _, fail := range []string{"unmountPartition /dev/sda 1", "unmountPartition /dev/sda 2", "mountEFI"} {
		p := &installRecorder{retag: true}
		p.fail = fail
		if _, _, err := p.run(r, in); err != nil {
			t.Errorf("%s failing ended the phase: %v", fail, err)
		}
		var warned bool
		for _, l := range p.log {
			if strings.HasPrefix(l, "stderr warning: ") && strings.HasSuffix(l, "injected failure\n") {
				warned = true
			}
		}
		if !warned {
			t.Errorf("%s failing wrote no warning: %v", fail, p.log)
		}
	}
}

// Live media: scratch on the target, registered by prepareScratchDir, not
// as a post-removal.
func TestInstallPhaseLiveScratch(t *testing.T) {
	r, in := installVariant(t, "plain")
	p := &installRecorder{live: true}
	res, _, err := p.run(r, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.log[1] != "prepareScratch /mnt/fisherman-target live=true" || strings.HasPrefix(p.log[2], "cleanup.") {
		t.Errorf("log = %v", p.log)
	}
	if !strings.Contains(p.log[3], "scratch=/mnt/fisherman-target/.fisherman-scratch") {
		t.Errorf("bootc scratch: %q", p.log[3])
	}
	if !res.liveISO || res.scratchDir != "/mnt/fisherman-target/.fisherman-scratch" {
		t.Errorf("result = %+v", res)
	}
}

// The target image reference, as main() computed it.
func TestInstallPhaseTargetImgref(t *testing.T) {
	tests := []struct {
		name                string
		image, targetImgref string
		media               *install.MediaInput
		source              string
		want                string
	}{
		{"same as the image", "ghcr.io/a/os:1", "ghcr.io/a/os:1", nil, "ghcr.io/a/os:1", ""},
		{"different", "ghcr.io/a/os:1", "ghcr.io/a/os:stable", nil, "ghcr.io/a/os:1", "ghcr.io/a/os:stable"},
		{"media day-2 ref", "", "", &install.MediaInput{Image: "ghcr.io/a/os:1", TargetImgref: "ghcr.io/a/os:stable", OCILayoutPath: "/run/media/oci"},
			"oci:/run/media/oci", "ghcr.io/a/os:stable"},
		{"media image", "", "", &install.MediaInput{Image: "ghcr.io/a/os:1", OCILayoutPath: "/run/media/oci"},
			"oci:/run/media/oci", "ghcr.io/a/os:1"},
		{"media recipe image", "ghcr.io/a/os:2", "", &install.MediaInput{Image: "ghcr.io/a/os:1", OCILayoutPath: "/run/media/oci"},
			"oci:/run/media/oci", "ghcr.io/a/os:2"},
		{"media source", "", "", &install.MediaInput{Image: "ghcr.io/a/os:1", ContainersStoragePath: "/x"},
			"ghcr.io/a/os:1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, in := installVariant(t, "plain")
			r.Image, r.TargetImgref = tt.image, tt.targetImgref
			in.mediaInput, in.sourceImgref = tt.media, tt.source
			p := &installRecorder{}
			res, _, err := p.run(r, in)
			if err != nil {
				t.Fatal(err)
			}
			if res.targetImgref != tt.want {
				t.Errorf("targetImgref = %q, want %q", res.targetImgref, tt.want)
			}
			if !strings.Contains(strings.Join(p.log, "\n"), fmt.Sprintf("target-imgref=%q", tt.want)) {
				t.Errorf("bootc did not get %q: %v", tt.want, p.log)
			}
		})
	}
}

// The pre-flight's shim and source root reach bootc; a reused source
// drops the image stores.
func TestInstallPhasePassesPreflight(t *testing.T) {
	r, in := installVariant(t, "plain")
	in.selinuxShim = "/run/fisherman-shim/x.so"
	in.sourceRoot = install.SourceRoot{Established: true}
	in.imageStores = []string{"/usr/lib/containers/storage"}
	p := &installRecorder{}
	if _, _, err := p.run(r, in); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(p.log, "\n")
	for _, w := range []string{`shim="/run/fisherman-shim/x.so"`, "source-root=true", "stores=[/usr/lib/containers/storage]"} {
		if !strings.Contains(log, w) {
			t.Errorf("bootc options lack %s: %v", w, p.log)
		}
	}
}

// A failing operation ends the phase at once with main()'s failure code
// and message; nothing after it runs.
func TestInstallPhaseFailures(t *testing.T) {
	pullErr := &install.PullError{Image: "ghcr.io/example/os:stable", Err: errors.New("manifest unknown")}
	diff := reuseProblem(recipe.CodeSourceReuseInstalledDigestDiffs, "source reuse: bootc installed sha256:3, not the verified copy sha256:2222")
	tests := []struct {
		variant, fail string
		retag         bool
		bootcErr      error
		reuseDiff     *recipe.Problem
		noUUID        bool
		code, msg     string
		problem       string
	}{
		{variant: "plain", fail: "prepareScratch", code: progress.FailureScratchFailed, msg: "preparing scratch dir: injected failure"},
		{variant: "plain", fail: "bootcInstall", code: progress.FailureBootcInstallFailed, msg: "bootc install: injected failure"},
		{variant: "plain", bootcErr: pullErr, code: progress.FailurePullFailed, msg: "bootc install: manifest unknown"},
		{variant: "luks", noUUID: true, code: progress.FailureLUKSFailed, msg: "LUKS: could not read the UUID of the encrypted root partition /dev/sda3"},
		{variant: "source reuse", reuseDiff: &diff, problem: recipe.CodeSourceReuseInstalledDigestDiffs,
			msg: "source reuse: source reuse: bootc installed sha256:3, not the verified copy sha256:2222"},
		{variant: "systemd-boot composefs", retag: true, fail: "setPartitionType", code: progress.FailureRetagFailed, msg: "retagging root partition: injected failure"},
		{variant: "systemd-boot composefs", retag: true, fail: "mkdir", code: progress.FailureRetagFailed, msg: "recreating target mountpoint before remount: injected failure"},
		{variant: "systemd-boot composefs", retag: true, fail: "waitForNode", code: progress.FailureRetagFailed, msg: "remounting root partition after retagging: injected failure"},
		{variant: "systemd-boot composefs", retag: true, fail: "mountWithRetry", code: progress.FailureRetagFailed, msg: "remounting root partition after retagging: injected failure"},
	}
	for _, tt := range tests {
		t.Run(tt.variant+"/"+tt.msg, func(t *testing.T) {
			r, in := installVariant(t, tt.variant)
			if tt.noUUID {
				in.disk.luksUUID = ""
			}
			p := &installRecorder{retag: tt.retag, bootcErr: tt.bootcErr, reuseDiff: tt.reuseDiff}
			p.fail = tt.fail
			_, _, err := p.run(r, in)
			var pe *phaseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v (%T), want a *phaseError", err, err)
			}
			if pe.msg != tt.msg {
				t.Errorf("message = %q, want %q", pe.msg, tt.msg)
			}
			if tt.problem != "" {
				if pe.problem == nil || pe.problem.Code != tt.problem || pe.prefix != "source reuse" {
					t.Errorf("problem = %+v (prefix %q), want %s", pe.problem, pe.prefix, tt.problem)
				}
			} else if pe.code != tt.code || pe.problem != nil {
				t.Errorf("code = %q, want %q", pe.code, tt.code)
			}
			last := p.log[len(p.log)-1]
			switch {
			case tt.fail != "":
				if !strings.HasPrefix(last, tt.fail) {
					t.Errorf("the phase went on after %q failed: %v", tt.fail, p.log)
				}
			case tt.noUUID:
				if !strings.HasPrefix(last, "step ") {
					t.Errorf("the phase went on without a LUKS UUID: %v", p.log)
				}
			case tt.reuseDiff != nil:
				if !strings.HasPrefix(last, "checkReused") {
					t.Errorf("the phase went on after a digest mismatch: %v", p.log)
				}
			default:
				if !strings.HasPrefix(last, "bootcInstall") {
					t.Errorf("the phase went on after bootc failed: %v", p.log)
				}
			}
		})
	}
	// The fallback loaders only warn.
	for _, tt := range []struct{ variant, fail, line string }{
		{"plain", "ensureGrubFallback", "info Warning: could not ensure the UEFI fallback loader: injected failure"},
		{"systemd-boot luks", "ensureSystemdBoot", "info Warning: could not ensure systemd-boot EFI binary: injected failure"},
	} {
		r, in := installVariant(t, tt.variant)
		p := &installRecorder{}
		p.fail = tt.fail
		if _, _, err := p.run(r, in); err != nil {
			t.Errorf("%s failing ended the phase: %v", tt.fail, err)
		}
		if p.log[len(p.log)-1] != tt.line {
			t.Errorf("%s failing: last line %q, want %q", tt.fail, p.log[len(p.log)-1], tt.line)
		}
	}
}

// failPhase ends a problem failure with the same event as fatalProblem.
func TestFailPhaseMatchesFatalProblem(t *testing.T) {
	p := reuseProblem(recipe.CodeSourceReuseInstalledDigestDiffs, "source reuse: bootc installed a, not b")

	inline, ri := newRecorded(t)
	inline.failProblem("source reuse: "+p.Message, p)

	phased, rp := newRecorded(t)
	old := term
	term = phased
	t.Cleanup(func() { term = old })
	failPhase(phaseProblem("source reuse", p))

	if a, b := lastFailure(t, ri), lastFailure(t, rp); !reflect.DeepEqual(a, b) {
		t.Errorf("failPhase event = %+v\nfatalProblem event = %+v", b, a)
	}
	if !reflect.DeepEqual(ri.calls, rp.calls) {
		t.Errorf("failPhase sequence = %v, fatalProblem sequence = %v", rp.calls, ri.calls)
	}
}

func TestLiveInstallOpsSetsEveryOperation(t *testing.T) {
	v := reflect.ValueOf(liveInstallOps())
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Errorf("liveInstallOps leaves %s unset", v.Type().Field(i).Name)
		}
	}
	// bootc is read through the seam at the call.
	old := bootcInstall
	t.Cleanup(func() { bootcInstall = old })
	called := false
	bootcInstall = func(install.Options) error { called = true; return nil }
	if err := liveInstallOps().bootcInstall(install.Options{}); err != nil || !called {
		t.Errorf("liveInstallOps does not call the bootcInstall seam")
	}
}
