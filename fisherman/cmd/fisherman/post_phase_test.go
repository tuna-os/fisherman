package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The post-install phase (#313), run against recorded operations: nothing
// is written to a deployment. Each test drives postPhase for a
// step-contract variant and reads back one ordered log of the step
// events, the info lines, the recovery key and receipt events, and every
// operation with its arguments.

type postRecorder struct {
	phaseRecorder
	plymouth int // loader entries EnsurePlymouthArgs patched
}

func (p *postRecorder) ops() postOps {
	return postOps{
		persistMounts: func(r *recipe.Recipe, target string, mounted func(string)) error {
			err := p.rec("persistMounts %s", target)
			if err == nil && r.HasVarDisk() {
				mounted(target + "/ostree/deploy/default/var")
			}
			return err
		},
		addMount: func(path string) { p.log = append(p.log, "cleanup.AddMount "+path) },
		stageTPM2: func(target, rootPart, uuid, pass string) string {
			p.log = append(p.log, fmt.Sprintf("stageTPM2 %s %s %s %s", target, rootPart, uuid, pass))
			return "TPM2 auto-unlock will be enrolled on first boot"
		},
		recoveryKey: func(key string) { p.log = append(p.log, "event recovery_key "+key) },
		copyFlatpaks: func(target string, refs []string, varPath string) error {
			return p.rec("copyFlatpaks %s %v %q", target, refs, varPath)
		},
		writeHostname: func(target, host string) error { return p.rec("writeHostname %s %s", target, host) },
		createUser: func(target string, u post.UserConfig) error {
			return p.rec("createUser %s %s %q %s %v", target, u.Username, u.Fullname, u.Password, u.Groups)
		},
		applyLocale: func(target string, r *recipe.Recipe, enc bool) {
			p.log = append(p.log, fmt.Sprintf("applyLocale %s initrd=%v", target, enc))
		},
		plymouthArgs: func(target string) (int, error) {
			return p.plymouth, p.rec("plymouthArgs %s", target)
		},
		rootFlags: func(target, opts string) (string, error) {
			if err := p.rec("rootFlags %s %q", target, opts); err != nil {
				return "", err
			}
			if opts == "" {
				return "", nil
			}
			return "rootflags=" + opts + " is in 1 boot entry", nil
		},
		luksBoot: func(target, uuid string, required bool) (string, error) {
			return "rd.luks.name is in 1 boot entry (0 patched here)", p.rec("luksBoot %s %s required=%v", target, uuid, required)
		},
		copyHostSecrets: func(target string, r *recipe.Recipe, isLive func() bool) {
			p.log = append(p.log, "copyHostSecrets "+target)
		},
		audioConfig: func(target string) error { return p.rec("audioConfig %s", target) },
		oemPackages: func(target, distro, tap string) error {
			return p.rec("oemPackages %s %q %q", target, distro, tap)
		},
		printServices: func(target string) ([]string, []string) {
			p.log = append(p.log, "printServices "+target)
			return nil, nil
		},
		stageReceipt: func(target, src, ref string, media *install.MediaInput) (progress.ReceiptStatus, string) {
			p.log = append(p.log, fmt.Sprintf("stageReceipt %s %s %s media=%v", target, src, ref, media != nil))
			return progress.ReceiptStatus{Written: true}, "Installation receipt written"
		},
		receipt: func(st progress.ReceiptStatus) {
			p.log = append(p.log, fmt.Sprintf("event receipt written=%v", st.Written))
		},
		info: func(msg string) { p.log = append(p.log, "info "+msg) },
	}
}

// postVariant is a variant with the input main() hands the post phase.
func postVariant(t *testing.T, name string) (*recipe.Recipe, postInput) {
	t.Helper()
	r := phaseVariant(t, name)
	dr, ok := diskPhaseResults[name]
	if !ok {
		dr = diskPhaseResults["plain"]
		if r.Encryption.Type == "tpm2-luks-passphrase" {
			dr = diskPhaseResults["luks"]
		}
	}
	return r, postInput{
		disk:         dr,
		hostIsLive:   func() bool { return false },
		sourceImgref: r.Image,
	}
}

func (p *postRecorder) run(r *recipe.Recipe, in postInput, pull bool) (*stepSequencer, error) {
	seq := p.sequencerAt(r, pull, stepInstallOS)
	seq.next++ // the install phase announced the OS install
	err := postPhase(r, seq, in, p.ops())
	return seq, err
}

const (
	postTarget  = "/mnt/fisherman-target"
	postReceipt = "stageReceipt /mnt/fisherman-target ghcr.io/example/os:stable ghcr.io/example/os:stable media=false"
)

// postPhaseGolden is what the post-install phase does, in order, for each
// shape (no extras, no user), checked against main() before the
// extraction.
var postPhaseGolden = map[string][]string{
	"plain": {
		"persistMounts " + postTarget,
		"step 6/8 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [] ""`,
		"step 7/8 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"applyLocale /mnt/fisherman-target initrd=false",
		`rootFlags /mnt/fisherman-target ""`,
		"copyHostSecrets " + postTarget,
		postReceipt,
		"info Installation receipt written",
		"event receipt written=true",
	},
	"luks": {
		"persistMounts " + postTarget,
		"step 7/9 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [] ""`,
		"step 8/9 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"applyLocale /mnt/fisherman-target initrd=true",
		`rootFlags /mnt/fisherman-target ""`,
		"luksBoot /mnt/fisherman-target LUKS-UUID required=true",
		"info rd.luks.name is in 1 boot entry (0 patched here)",
		"copyHostSecrets " + postTarget,
		postReceipt,
		"info Installation receipt written",
		"event receipt written=true",
	},
	"tpm2": {
		"persistMounts " + postTarget,
		"step 7/10 Enrolling TPM2 auto-unlock [tpm2_enroll] 87+1",
		"stageTPM2 /mnt/fisherman-target /dev/sda3 LUKS-UUID RANDOM-RECOVERY-KEY",
		"info TPM2 auto-unlock will be enrolled on first boot",
		"event recovery_key RANDOM-RECOVERY-KEY",
		"step 8/10 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [] ""`,
		"step 9/10 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"applyLocale /mnt/fisherman-target initrd=true",
		`rootFlags /mnt/fisherman-target ""`,
		"luksBoot /mnt/fisherman-target LUKS-UUID required=true",
		"info rd.luks.name is in 1 boot entry (0 patched here)",
		"copyHostSecrets " + postTarget,
		postReceipt,
		"info Installation receipt written",
		"event receipt written=true",
	},
	"tpm2+passphrase": {
		"persistMounts " + postTarget,
		"step 7/10 Enrolling TPM2 auto-unlock [tpm2_enroll] 87+1",
		"stageTPM2 /mnt/fisherman-target /dev/sda3 LUKS-UUID correct horse",
		"info TPM2 auto-unlock will be enrolled on first boot",
		"step 8/10 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [] ""`,
		"step 9/10 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"applyLocale /mnt/fisherman-target initrd=true",
		`rootFlags /mnt/fisherman-target ""`,
		"luksBoot /mnt/fisherman-target LUKS-UUID required=true",
		"info rd.luks.name is in 1 boot entry (0 patched here)",
		"copyHostSecrets " + postTarget,
		postReceipt,
		"info Installation receipt written",
		"event receipt written=true",
	},
	"systemd-boot luks": {
		"persistMounts " + postTarget,
		"step 7/9 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [] ""`,
		"step 8/9 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"applyLocale /mnt/fisherman-target initrd=true",
		`rootFlags /mnt/fisherman-target "compress=zstd:1"`,
		"info rootflags=compress=zstd:1 is in 1 boot entry",
		"luksBoot /mnt/fisherman-target LUKS-UUID required=false",
		"info rd.luks.name is in 1 boot entry (0 patched here)",
		"copyHostSecrets " + postTarget,
		postReceipt,
		"info Installation receipt written",
		"event receipt written=true",
	},
	"btrfs subvolumes": {
		"persistMounts " + postTarget,
		"step 6/8 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [] ""`,
		"step 7/8 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"applyLocale /mnt/fisherman-target initrd=false",
		`rootFlags /mnt/fisherman-target "subvol=@,compress=zstd:1"`,
		"info rootflags=subvol=@,compress=zstd:1 is in 1 boot entry",
		"copyHostSecrets " + postTarget,
		postReceipt,
		"info Installation receipt written",
		"event receipt written=true",
	},
	"varDisk format": {
		"persistMounts " + postTarget,
		"cleanup.AddMount /mnt/fisherman-target/ostree/deploy/default/var",
		"step 7/9 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [] ""`,
		"step 8/9 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"applyLocale /mnt/fisherman-target initrd=false",
		`rootFlags /mnt/fisherman-target ""`,
		"copyHostSecrets " + postTarget,
		postReceipt,
		"info Installation receipt written",
		"event receipt written=true",
	},
	"manual": {
		"persistMounts " + postTarget,
		"step 3/5 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [] ""`,
		"step 4/5 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"applyLocale /mnt/fisherman-target initrd=false",
		`rootFlags /mnt/fisherman-target ""`,
		"copyHostSecrets " + postTarget,
		postReceipt,
		"info Installation receipt written",
		"event receipt written=true",
	},
}

// For each shape the post phase does what the golden log says and
// announces exactly the plan's steps between the OS install and finalize,
// at their planned numbers and bar positions.
func TestPostPhaseGolden(t *testing.T) {
	for name, want := range postPhaseGolden {
		t.Run(name, func(t *testing.T) {
			r, in := postVariant(t, name)
			p := &postRecorder{}
			seq, err := p.run(r, in, true)
			if err != nil {
				t.Fatalf("postPhase: %v", err)
			}
			if !reflect.DeepEqual(p.log, want) {
				t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(want, "\n  "))
			}
			plan := planSteps(r, true)
			var wantSteps []string
			after := false
			for i, s := range plan {
				if s.name == stepFinalize {
					break
				}
				if after {
					wantSteps = append(wantSteps, fmt.Sprintf("step %d/%d %s [%s] %d+%d",
						i+1, len(plan), s.name, progress.StepID(s.name), s.cumulativePct, s.weightPct))
				}
				after = after || s.name == stepInstallOS
			}
			if !reflect.DeepEqual(p.stepLog, wantSteps) {
				t.Errorf("steps = %v\nthe plan between the OS install and finalize is %v", p.stepLog, wantSteps)
			}
			if len(seq.errs) != 0 {
				t.Errorf("sequencer: %v", seq.errs)
			}
			if seq.next >= len(plan) || plan[seq.next].name != stepFinalize {
				t.Errorf("after the post phase the next step is %d of %d, want %q", seq.next, len(plan), stepFinalize)
			}
		})
	}
}

// Everything a recipe and an image can turn on: a user, every desktop
// extra, the media's receipt inputs.
func TestPostPhaseEverything(t *testing.T) {
	r, in := postVariant(t, "plain")
	r.User = recipe.UserSpec{Username: "ada", Fullname: "Ada L", Password: "pw", Groups: []string{"wheel"}}
	r.Flatpaks = []string{"org.example.App"}
	r.FlatpakVarPath = "/var/lib/flatpak"
	r.DistroID, r.BrewTap = "bluefin", "ublue-os/tap"
	in.extras = recipe.Extras{AudioNames: true, BootSplash: true, OEMSetup: true, PrintServices: true}
	in.sourceImgref, in.targetImgref, in.liveImage = "oci:/run/media/oci", "ghcr.io/example/os:stable", "ghcr.io/example/os:live"
	in.mediaInput = &install.MediaInput{Image: "ghcr.io/example/os:stable", OCILayoutPath: "/run/media/oci"}
	p := &postRecorder{plymouth: 1}
	if _, err := p.run(r, in, true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"persistMounts " + postTarget,
		"step 6/8 Copying system Flatpaks [flatpaks] 88+11",
		`copyFlatpaks /mnt/fisherman-target [org.example.App] "/var/lib/flatpak"`,
		"step 7/8 Configuring installed system [configure] 99+0",
		"info Writing hostname: fixture",
		"writeHostname /mnt/fisherman-target fixture",
		"info Creating user: ada",
		`createUser /mnt/fisherman-target ada "Ada L" pw [wheel]`,
		"applyLocale /mnt/fisherman-target initrd=false",
		"plymouthArgs " + postTarget,
		"info Added Plymouth boot args to 1 loader entry",
		`rootFlags /mnt/fisherman-target ""`,
		"copyHostSecrets " + postTarget,
		"audioConfig " + postTarget,
		`oemPackages /mnt/fisherman-target "bluefin" "ublue-os/tap"`,
		"printServices " + postTarget,
		"stageReceipt /mnt/fisherman-target oci:/run/media/oci ghcr.io/example/os:stable media=true",
		"info Installation receipt written",
		"event receipt written=true",
	}
	if !reflect.DeepEqual(p.log, want) {
		t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(want, "\n  "))
	}
}

// The non-fatal steps only warn, with main()'s lines, and the install goes
// on to the receipt.
func TestPostPhaseWarnings(t *testing.T) {
	tests := []struct {
		fail, line string
		plymouth   int
	}{
		{fail: "copyFlatpaks", line: "info Warning: could not copy flatpaks: injected failure"},
		{fail: "plymouthArgs", line: "info Warning: could not set Plymouth kernel args: injected failure"},
		{fail: "audioConfig", line: "info Warning: could not configure audio devices: injected failure"},
		{fail: "oemPackages", line: "info Warning: OEM package setup: injected failure"},
		{plymouth: 3, line: "info Added Plymouth boot args to 3 loader entries"},
		{line: "info Plymouth boot args: already in every loader entry"},
	}
	for _, tt := range tests {
		r, in := postVariant(t, "plain")
		in.extras = recipe.Extras{AudioNames: true, BootSplash: true, OEMSetup: true}
		p := &postRecorder{plymouth: tt.plymouth}
		p.fail = tt.fail
		if _, err := p.run(r, in, true); err != nil {
			t.Errorf("%s: the phase ended: %v", tt.line, err)
			continue
		}
		found := false
		for _, l := range p.log {
			found = found || l == tt.line
		}
		if !found || p.log[len(p.log)-1] != "event receipt written=true" {
			t.Errorf("want %q and the receipt, got %v", tt.line, p.log)
		}
	}
}

// A failing operation ends the phase at once with main()'s failure code
// and message; nothing after it runs.
func TestPostPhaseFailures(t *testing.T) {
	tests := []struct {
		variant, fail string
		code, msg     string
	}{
		{"plain", "persistMounts", progress.FailurePersistentMountsFailed, "persistent mounts: injected failure"},
		{"plain", "writeHostname", progress.FailureHostnameFailed, "writing hostname: injected failure"},
		{"plain", "createUser", progress.FailureUserFailed, "creating user: injected failure"},
		{"plain", "rootFlags", progress.FailureRootFlagsFailed, "root mount options: injected failure"},
		{"luks", "luksBoot", progress.FailureLUKSBootArgsFailed, "LUKS boot arguments: injected failure"},
	}
	for _, tt := range tests {
		t.Run(tt.variant+"/"+tt.fail, func(t *testing.T) {
			r, in := postVariant(t, tt.variant)
			r.User = recipe.UserSpec{Username: "ada"}
			p := &postRecorder{}
			p.fail = tt.fail
			_, err := p.run(r, in, true)
			var pe *phaseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v (%T), want a *phaseError", err, err)
			}
			if pe.code != tt.code || pe.msg != tt.msg {
				t.Errorf("failure = %q %q, want %q %q", pe.code, pe.msg, tt.code, tt.msg)
			}
			if last := p.log[len(p.log)-1]; !strings.HasPrefix(last, tt.fail) {
				t.Errorf("the phase went on after %q failed: %v", tt.fail, p.log)
			}
		})
	}
}

func TestLivePostOpsSetsEveryOperation(t *testing.T) {
	v := reflect.ValueOf(livePostOps())
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Errorf("livePostOps leaves %s unset", v.Type().Field(i).Name)
		}
	}
}
