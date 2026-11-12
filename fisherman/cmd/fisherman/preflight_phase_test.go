package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/reuse"
)

// The pre-flight (#313), run against recorded host queries: nothing is
// probed, locked, pulled or built. Each test drives preflightPhase for a
// step-contract variant and reads back one ordered log of every query,
// registration and info line, then checks what it hands the phases.

type preflightRecorder struct {
	phaseRecorder
	t          *testing.T
	r          *recipe.Recipe
	euid       int
	lockErr    error
	loadErr    error
	media      *install.MediaInput
	mediaErr   error
	live       probe.Live
	problems   []recipe.Problem
	toolErr    error
	kept       []recipe.Problem
	reused     *reuse.Result
	reuseProb  *recipe.Problem
	stores     []string
	discovered bool
	check      install.ImageCheck
	context    string
	shim       [2]string
	shimErr    error
}

func (p *preflightRecorder) ops() preflightOps {
	t := p.t
	return preflightOps{
		geteuid:     func() int { p.log = append(p.log, "geteuid"); return p.euid },
		acquireLock: func() error { p.log = append(p.log, "acquireLock"); return p.lockErr },
		loadRecipe: func(arg string) (*recipe.Recipe, error) {
			p.log = append(p.log, "loadRecipe "+arg)
			if p.loadErr != nil {
				return nil, p.loadErr
			}
			return p.r, nil
		},
		registerSecret: func(s string) { p.log = append(p.log, fmt.Sprintf("secret %q", s)) },
		resolveMediaInput: func(*recipe.Recipe) (*install.MediaInput, error) {
			p.log = append(p.log, "resolveMediaInput")
			return p.media, p.mediaErr
		},
		hostLive: func() probe.Live { p.log = append(p.log, "hostLive"); return p.live },
		installProblems: func(r *recipe.Recipe, imageErr error) []recipe.Problem {
			p.log = append(p.log, fmt.Sprintf("installProblems imageErr=%v", imageErr))
			return p.problems
		},
		resolveTargets: func(*recipe.Recipe) { p.log = append(p.log, "resolveTargets") },
		expandPath:     func() { p.log = append(p.log, "expandPath") },
		checkTools:     func(*recipe.Recipe) error { p.log = append(p.log, "checkTools"); return p.toolErr },
		keptFilesystemProblems: func(*recipe.Recipe) []recipe.Problem {
			p.log = append(p.log, "keptFilesystemProblems")
			return p.kept
		},
		acquireSourceReuse: func(r *recipe.Recipe, register func(func() error)) (*reuse.Result, *recipe.Problem) {
			p.log = append(p.log, "acquireSourceReuse")
			if p.reused != nil {
				register(func() error { return nil })
			}
			return p.reused, p.reuseProb
		},
		addRelease: func(func() error) { p.log = append(p.log, "cleanup.AddRelease") },
		resolveImageStores: func(*recipe.Recipe) ([]string, bool) {
			p.log = append(p.log, "resolveImageStores")
			return p.stores, p.discovered
		},
		checkImage: func(ref string) install.ImageCheck {
			p.log = append(p.log, "checkImage "+ref)
			return p.check
		},
		establishSourceRoot: func(o install.Options, stores []string) install.SourceRoot {
			p.log = append(p.log, fmt.Sprintf("establishSourceRoot src=%s stores=%v in-source=%v", o.SourceImgref, stores, o.RunInSourceContainer))
			return install.SourceRoot{Established: true, Reason: "digest_match"}
		},
		localSourceContextLine: func(o install.Options) string {
			p.log = append(p.log, fmt.Sprintf("localSourceContextLine root=%v", o.SourceRoot.Established))
			return p.context
		},
		prepareSelinuxWorkaround: func(o install.Options) (string, string, error) {
			p.log = append(p.log, fmt.Sprintf("prepareSelinuxWorkaround src=%s", o.SourceImgref))
			return p.shim[0], p.shim[1], p.shimErr
		},
		addPostRemoval: func(path string) { p.log = append(p.log, "cleanup.AddPostRemoval "+path) },
		resolveExtras: func(r *recipe.Recipe, refs ...string) (recipe.Extras, string) {
			p.log = append(p.log, fmt.Sprintf("resolveExtras %q", refs))
			return recipe.Extras{AudioNames: true}, "Desktop extras: audioNames"
		},
		liveOnce: func(known probe.Live) func() bool {
			p.log = append(p.log, fmt.Sprintf("liveOnce live=%v", known.IsLive))
			return func() bool { return known.IsLive }
		},
		newSequencer: func(plan []plannedStep) *stepSequencer {
			seq := newStepSequencer(plan)
			seq.emitFn = func(int, int, string, int, int) { t.Error("the pre-flight announced a step") }
			return seq
		},
		info:   func(msg string) { p.log = append(p.log, "info "+msg) },
		stderr: func(msg string) { p.log = append(p.log, "stderr "+msg) },
	}
}

func newPreflightRecorder(t *testing.T, variant string) *preflightRecorder {
	t.Helper()
	return &preflightRecorder{
		t:     t,
		r:     phaseVariant(t, variant),
		check: install.ImageCheck{NeedsPull: true, LayerCount: 7, Source: install.ImageSource{Ref: "ghcr.io/example/os:stable"}},
	}
}

func (p *preflightRecorder) run() (preflightResult, error) {
	return preflightPhase("-", p.ops())
}

// The pre-flight of a registry image that needs a pull.
var preflightPlainGolden = []string{
	"geteuid",
	"acquireLock",
	"loadRecipe -",
	`secret ""`,
	`secret ""`,
	"resolveMediaInput",
	"installProblems imageErr=<nil>",
	"resolveTargets",
	"stderr [fisherman] version: " + version + "\n",
	"expandPath",
	"checkTools",
	"keptFilesystemProblems",
	"acquireSourceReuse",
	"resolveImageStores",
	"info Checking image cache...",
	"checkImage ghcr.io/example/os:stable",
	"info Source image ghcr.io/example/os:stable; a pull can still fail after this check, and a tag can move",
	"info Image pull required (7 layers)",
	"establishSourceRoot src=ghcr.io/example/os:stable stores=[] in-source=false",
	"localSourceContextLine root=true",
	"prepareSelinuxWorkaround src=ghcr.io/example/os:stable",
	`resolveExtras ["" "ghcr.io/example/os:stable" ""]`,
	"info Desktop extras: audioNames",
	"liveOnce live=false",
}

func TestPreflightPhaseGolden(t *testing.T) {
	p := newPreflightRecorder(t, "plain")
	res, err := p.run()
	if err != nil {
		t.Fatalf("preflightPhase: %v", err)
	}
	if !reflect.DeepEqual(p.log, preflightPlainGolden) {
		t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(preflightPlainGolden, "\n  "))
	}
	if res.r != p.r || res.targetMount != defaultTargetMount || res.luksMapper != defaultLuksMapper ||
		res.sourceImgref != "ghcr.io/example/os:stable" || res.mediaInput != nil || res.reused != nil ||
		!res.imageCheck.NeedsPull || !res.sourceRoot.Established || !res.extras.AudioNames || res.hostIsLive() {
		t.Errorf("result = %+v", res)
	}
	// The plan is the step plan for the recipe and the pull, and nothing
	// is announced yet.
	if !reflect.DeepEqual(res.seq.plan, planSteps(p.r, true)) || res.seq.next != 0 {
		t.Errorf("sequencer plan %v at %d, want planSteps(r, true) at 0", res.seq.plan, res.seq.next)
	}
}

// The image cache lines, the store line, the SELinux shim registration
// and the local-source line, as main() wrote them.
func TestPreflightPhaseImageLines(t *testing.T) {
	tests := []struct {
		name string
		mut  func(p *preflightRecorder)
		want []string
	}{
		{"cached", func(p *preflightRecorder) { p.check = install.ImageCheck{Source: install.ImageSource{Ref: "x"}} },
			[]string{"info Image found in local cache, no pull needed"}},
		{"offline", func(p *preflightRecorder) {
			p.check = install.ImageCheck{Offline: true, Source: install.ImageSource{Ref: "x"}}
		},
			[]string{"info Offline: registry unreachable, using locally cached image"}},
		{"stores", func(p *preflightRecorder) { p.stores, p.discovered = []string{"/a", "/b"}, true },
			[]string{"info Offline image stores found on the host: /a, /b"}},
		{"shim", func(p *preflightRecorder) { p.shim = [2]string{"/run/shim/x.so", "/run/shim"} },
			[]string{"cleanup.AddPostRemoval /run/shim",
				"info SELinux: the target disables SELinux and this host enforces a policy; the label workaround is ready"}},
		{"local source", func(p *preflightRecorder) { p.context = "bootc: the host booted the source image (digest_match)" },
			[]string{"info bootc: the host booted the source image (digest_match)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPreflightRecorder(t, "plain")
			tt.mut(p)
			res, err := p.run()
			if err != nil {
				t.Fatal(err)
			}
			log := strings.Join(p.log, "\n")
			if !strings.Contains(log, strings.Join(tt.want, "\n")) {
				t.Errorf("log lacks %q:\n%s", tt.want, log)
			}
			if tt.name == "shim" && res.selinuxShim != "/run/shim/x.so" {
				t.Errorf("selinuxShim = %q", res.selinuxShim)
			}
		})
	}
}

// A verified media input is the source: no live probe, no store
// discovery, no cache check, and the media's refs reach the extras.
func TestPreflightPhaseMediaInput(t *testing.T) {
	p := newPreflightRecorder(t, "plain")
	p.r.Image = ""
	p.media = &install.MediaInput{Image: "ghcr.io/example/os:1", TargetImgref: "ghcr.io/example/os:stable", OCILayoutPath: "/run/media/oci"}
	res, err := p.run()
	if err != nil {
		t.Fatal(err)
	}
	log := strings.Join(p.log, "\n")
	for _, absent := range []string{"hostLive", "resolveImageStores", "checkImage"} {
		if strings.Contains(log, absent) {
			t.Errorf("a media install ran %s:\n%s", absent, log)
		}
	}
	for _, w := range []string{
		"info Offline: using embedded media image /run/media/oci",
		"establishSourceRoot src=oci:/run/media/oci stores=[] in-source=false",
		`resolveExtras ["" "" "ghcr.io/example/os:1" "ghcr.io/example/os:stable" "ghcr.io/example/os:1"]`,
		"liveOnce live=true",
	} {
		if !strings.Contains(log, w) {
			t.Errorf("log lacks %q:\n%s", w, log)
		}
	}
	if res.sourceImgref != "oci:/run/media/oci" || !res.live.IsLive || res.live.DetectedBy != "verified media input" || !res.imageCheck.Offline {
		t.Errorf("result = %+v", res)
	}
	// A containers-storage media input.
	p = newPreflightRecorder(t, "plain")
	p.media = &install.MediaInput{Image: "ghcr.io/example/os:1", ContainersStoragePath: "/run/media/storage"}
	if res, err := p.run(); err != nil || res.sourceImgref != "containers-storage:ghcr.io/example/os:1" {
		t.Errorf("sourceImgref = %q, %v", res.sourceImgref, err)
	}
}

// Live media with no image: the host is probed and the line names the
// running image.
func TestPreflightPhaseLiveImage(t *testing.T) {
	p := newPreflightRecorder(t, "plain")
	p.r.Image = ""
	p.live = probe.Live{IsLive: true, LiveImage: "ghcr.io/example/os:live", DetectedBy: "bootc status"}
	if _, err := p.run(); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(p.log, "\n")
	for _, w := range []string{"resolveMediaInput\nhostLive\n", "info Live media (bootc status): installing the running image ghcr.io/example/os:live"} {
		if !strings.Contains(log, w) {
			t.Errorf("log lacks %q:\n%s", w, log)
		}
	}
}

// Source reuse: no media input, the copy is released at teardown, its
// presence (not a registry) is checked, and the options run in the source
// container.
func TestPreflightPhaseSourceReuse(t *testing.T) {
	p := newPreflightRecorder(t, "source reuse")
	p.reused = &reuse.Result{SourceImgref: "containers-storage:localhost/reuse:1", Destination: "localhost/reuse:1", DestinationDigest: "sha256:2"}
	p.check = install.ImageCheck{}
	res, err := p.run()
	if err != nil {
		t.Fatal(err)
	}
	log := strings.Join(p.log, "\n")
	if strings.Contains(log, "resolveMediaInput") || strings.Contains(log, "resolveImageStores") {
		t.Errorf("a reuse install resolved media or stores:\n%s", log)
	}
	for _, w := range []string{"acquireSourceReuse\ncleanup.AddRelease\n", "checkImage containers-storage:localhost/reuse:1\n",
		"establishSourceRoot src=containers-storage:localhost/reuse:1 stores=[] in-source=true"} {
		if !strings.Contains(log, w) {
			t.Errorf("log lacks %q:\n%s", w, log)
		}
	}
	if res.sourceImgref != "containers-storage:localhost/reuse:1" || res.reused != p.reused {
		t.Errorf("result = %+v", res)
	}
}

// The recipe's mount point and mapper name override the defaults, and the
// passphrase and password are registered as secrets.
func TestPreflightPhaseOverridesAndSecrets(t *testing.T) {
	p := newPreflightRecorder(t, "luks")
	p.r.TargetMount, p.r.LuksMapperName = "/mnt/other", "other-root"
	p.r.User = recipe.UserSpec{Username: "ada", Password: "pw"}
	res, err := p.run()
	if err != nil {
		t.Fatal(err)
	}
	if res.targetMount != "/mnt/other" || res.luksMapper != "other-root" {
		t.Errorf("paths = %q %q", res.targetMount, res.luksMapper)
	}
	if p.log[3] != `secret "correct horse"` || p.log[4] != `secret "pw"` {
		t.Errorf("secrets = %v", p.log[3:5])
	}
}

// Every refusal ends the pre-flight at once with main()'s problem (and
// prefix), failure code or load error; nothing after it runs.
func TestPreflightPhaseFailures(t *testing.T) {
	missing := errors.New("missing tool: sgdisk (gdisk)")
	tests := []struct {
		name    string
		mut     func(p *preflightRecorder)
		last    string
		code    string // a failure code (fatalCode)
		problem string // a problem code (fatalProblem)
		prefix  string
		msg     string
		load    bool
	}{
		{name: "not root", mut: func(p *preflightRecorder) { p.euid = 1000 }, last: "geteuid",
			problem: recipe.CodeNotRoot, prefix: "cannot install",
			msg: "cannot install: fisherman must run as root to install (run it through pkexec or sudo); `fisherman validate` and `fisherman probe` need no root"},
		{name: "lock held", mut: func(p *preflightRecorder) { p.lockErr = errInstallRunning }, last: "acquireLock",
			problem: recipe.CodeInstallAlreadyRunning, prefix: "cannot install",
			msg: "cannot install: another fisherman install is running on this host; wait for it to finish, or cancel it, then start this install again"},
		{name: "lock failed", mut: func(p *preflightRecorder) { p.lockErr = errors.New("read-only") }, last: "acquireLock",
			code: progress.FailureLockFailed, msg: "cannot take the install lock " + installLockPath + ": read-only"},
		{name: "load", mut: func(p *preflightRecorder) { p.loadErr = recipe.ErrEmptyRecipe }, last: "loadRecipe -",
			load: true, msg: "loading recipe: " + recipe.ErrEmptyRecipe.Error()},
		{name: "media", mut: func(p *preflightRecorder) { p.mediaErr = errors.New("digest mismatch") }, last: "resolveMediaInput",
			code: progress.FailureLocalSourceUnavailable, msg: "required local source is absent or unreadable; refusing installation: digest mismatch"},
		{name: "invalid", mut: func(p *preflightRecorder) {
			p.problems = []recipe.Problem{{Code: "disk_missing", Field: "disk", Message: "disk is required"}}
		}, last: "installProblems imageErr=<nil>", problem: "disk_missing", prefix: "invalid recipe", msg: "invalid recipe: disk is required"},
		{name: "tools", mut: func(p *preflightRecorder) { p.toolErr = missing }, last: "checkTools",
			problem: recipe.CodeHostToolMissing, prefix: "missing required host tool", msg: "missing required host tool: " + missing.Error()},
		{name: "kept", mut: func(p *preflightRecorder) {
			p.kept = []recipe.Problem{{Code: "kept_filesystem_unreadable", Message: "no filesystem on /dev/sdb"}}
		}, last: "keptFilesystemProblems", problem: "kept_filesystem_unreadable", prefix: "cannot keep the existing filesystem",
			msg: "cannot keep the existing filesystem: no filesystem on /dev/sdb"},
		{name: "reuse", mut: func(p *preflightRecorder) {
			p.reuseProb = &recipe.Problem{Code: recipe.CodeSourceReuseVerifyFailed, Message: "copy differs"}
		}, last: "acquireSourceReuse", problem: recipe.CodeSourceReuseVerifyFailed, prefix: "source reuse", msg: "source reuse: copy differs"},
		{name: "reuse copy gone", mut: func(p *preflightRecorder) {
			p.reused = &reuse.Result{SourceImgref: "containers-storage:localhost/reuse:1", Destination: "localhost/reuse:1"}
			p.check = install.ImageCheck{NeedsPull: true}
		}, last: "checkImage containers-storage:localhost/reuse:1", problem: recipe.CodeSourceReuseVerifyFailed, prefix: "source reuse",
			msg: "source reuse: the verified copy localhost/reuse:1 is no longer readable in containers-storage"},
		{name: "oci absent", mut: func(p *preflightRecorder) { p.r.Image = "oci:/srv/os" }, last: "checkImage oci:/srv/os",
			code: progress.FailureLocalSourceUnavailable, msg: "required local source is absent or unreadable; refusing installation"},
		{name: "storage absent", mut: func(p *preflightRecorder) { p.r.Image = "containers-storage:localhost/os" },
			last: "checkImage containers-storage:localhost/os",
			code: progress.FailureLocalSourceUnavailable, msg: "required local source is absent or unreadable; refusing installation"},
		{name: "image source", mut: func(p *preflightRecorder) {
			p.check = install.ImageCheck{Err: &install.SourceError{Code: "image_not_bootc", Message: "not a bootc image"}}
		}, last: "checkImage ghcr.io/example/os:stable", problem: "image_not_bootc", prefix: "image source", msg: "image source: not a bootc image"},
		{name: "selinux", mut: func(p *preflightRecorder) { p.shimErr = errors.New("no compiler") },
			last: "prepareSelinuxWorkaround src=ghcr.io/example/os:stable", problem: recipe.CodeSelinuxWorkaroundUnavailable,
			prefix: "cannot prepare the install", msg: "cannot prepare the install: no compiler"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPreflightRecorder(t, "plain")
			tt.mut(p)
			_, err := p.run()
			var pe *phaseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v (%T), want a *phaseError", err, err)
			}
			if pe.msg != tt.msg {
				t.Errorf("message = %q\nwant      %q", pe.msg, tt.msg)
			}
			switch {
			case tt.load:
				if !errors.Is(pe.load, p.loadErr) {
					t.Errorf("load = %v, want %v", pe.load, p.loadErr)
				}
			case tt.problem != "":
				if pe.problem == nil || pe.problem.Code != tt.problem || pe.prefix != tt.prefix {
					t.Errorf("problem = %+v prefix %q, want %s %q", pe.problem, pe.prefix, tt.problem, tt.prefix)
				}
			default:
				if pe.code != tt.code || pe.problem != nil || pe.load != nil {
					t.Errorf("code = %q, want %q", pe.code, tt.code)
				}
			}
			if last := p.log[len(p.log)-1]; last != tt.last {
				t.Errorf("the pre-flight went on after %q: %v", tt.last, p.log)
			}
		})
	}
}

// failPhase ends a load failure with the same event as fatalLoad.
func TestFailPhaseMatchesFatalLoad(t *testing.T) {
	for _, loadErr := range []error{recipe.ErrEmptyRecipe, errors.New("open x.json: no such file or directory"),
		recipe.Problem{Code: "unknown_field", Field: "foo", Message: `unknown field "foo"`}} {
		inline, ri := newRecorded(t)
		old := term
		term = inline
		fatalLoad(loadErr)

		phased, rp := newRecorded(t)
		term = phased
		failPhase(phaseLoad(loadErr))
		term = old

		if a, b := lastFailure(t, ri), lastFailure(t, rp); !reflect.DeepEqual(a, b) {
			t.Errorf("%v: failPhase event = %+v\nfatalLoad event = %+v", loadErr, b, a)
		}
	}
}

func TestLivePreflightOpsSetsEveryOperation(t *testing.T) {
	v := reflect.ValueOf(livePreflightOps())
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Errorf("livePreflightOps leaves %s unset", v.Type().Field(i).Name)
		}
	}
}
