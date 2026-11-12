package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// `fisherman plan --json -` (#371): the same checks as `validate --json`,
// then the install's own decisions as JSON, with no write, no pull and no
// secret. The golden files pin the schema in docs/PLAN.md.

// planSecrets are the sentinel values the fixtures carry in the passphrase,
// the password and an unknown recoveryKey field. No output may hold one.
var planSecrets = []string{"SENTINEL-LUKS-PASSPHRASE", "SENTINEL-USER-PASSWORD", "SENTINEL-RECOVERY-KEY"}

// readOnlyCommands are the host commands a plan may run: the probe's
// read-only queries. Anything else (sfdisk, mkfs.*, mount, cryptsetup,
// zpool, podman pull, bootc install, ...) fails the test, except the local
// image reads readOnlyImageCommand allows.
var readOnlyCommands = map[string]bool{
	"lsblk": true, "blkid": true, "findmnt": true, "test": true, "cat": true, "stat": true,
}

// readOnlyImageCommand reports whether a skopeo or podman command only
// reads local image metadata: `skopeo inspect` of a local transport and
// `podman image inspect`. A registry read (docker://) is not allowed: the
// tests fake it (fakeImages) and never reach the network.
func readOnlyImageCommand(name string, args []string) bool {
	switch {
	case name == "skopeo" && len(args) > 0 && args[0] == "inspect":
		for _, a := range args {
			if strings.HasPrefix(a, "docker://") {
				return false
			}
		}
		return true
	case name == "podman" && len(args) > 1 && args[0] == "image" && args[1] == "inspect":
		return true
	}
	return false
}

// commandGuard records every command the code under test asks for, through
// each way fisherman runs one, and fails the test on any that is not
// read-only. The mutating seams (bind mounts, bootc install, the SELinux
// workaround) fail on any call.
type commandGuard struct {
	t    *testing.T
	mu   sync.Mutex
	seen []string
	bad  []string // commands that are not read-only
}

func (g *commandGuard) check(name string, args []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	g.seen = append(g.seen, line)
	if !readOnlyCommands[name] && !readOnlyImageCommand(name, args) {
		g.bad = append(g.bad, line)
	}
	return os.ErrPermission // nothing really runs
}

type guardedCommand struct{ err error }

func (c guardedCommand) Run() error              { return c.err }
func (c guardedCommand) Output() ([]byte, error) { return nil, c.err }
func (c guardedCommand) Start() error            { return c.err }
func (c guardedCommand) Wait() error             { return c.err }
func (c guardedCommand) SetStdin(io.Reader)      {}
func (c guardedCommand) SetStdout(io.Writer)     {}
func (c guardedCommand) SetStderr(io.Writer)     {}

type guardedExecutor struct{ g *commandGuard }

func (e guardedExecutor) Command(name string, args ...string) runner.Command {
	return guardedCommand{e.g.check(name, args)}
}

func guardCommands(t *testing.T) *commandGuard {
	t.Helper()
	g := &commandGuard{t: t}
	oldRun, oldOut, oldExec := runner.RunFn, runner.OutputFn, runner.DefaultExecutor
	oldProber, oldBind, oldBootc, oldShim := newProber, bindMount, bootcInstall, prepareSelinuxWorkaround
	oldVar, oldTarget, oldBlock, oldFree := varIsSpaceConstrained, hostTarget, hostBlock, hostScratchFree
	oldInstallExec, oldPlanInspect := install.Exec, install.PlanInspectFn
	hostTarget = planHost(nil)
	hostBlock = planBlocks
	hostScratchFree = planScratchFree(planFixtureFreeBytes)
	runner.RunFn = func(_ io.Reader, name string, args ...string) error { return g.check(name, args) }
	runner.OutputFn = func(name string, args ...string) ([]byte, error) { return nil, g.check(name, args) }
	runner.DefaultExecutor = guardedExecutor{g}
	install.Exec = guardedExecutor{g}
	install.PlanInspectFn = func(name string, args ...string) ([]byte, error) { return nil, g.check(name, args) }
	root := t.TempDir()
	newProber = func() *probe.Prober {
		return &probe.Prober{Root: root, Getenv: func(string) string { return "" },
			Sandboxed: func() bool { return false }, Euid: func() int { return 1000 },
			Run: func(name string, args ...string) ([]byte, error) { return nil, g.check(name, args) }}
	}
	bindMount = func(src, dst string) error { t.Errorf("plan bind-mounted %s at %s", src, dst); return os.ErrPermission }
	bootcInstall = func(install.Options) error { t.Error("plan ran bootc install"); return os.ErrPermission }
	prepareSelinuxWorkaround = func(install.Options) (string, string, error) {
		t.Error("plan prepared the SELinux workaround")
		return "", "", os.ErrPermission
	}
	varIsSpaceConstrained = func() bool { return false }
	t.Cleanup(func() {
		for _, line := range g.bad {
			t.Errorf("plan ran %q, which is not a read-only command", line)
		}
		runner.RunFn, runner.OutputFn, runner.DefaultExecutor = oldRun, oldOut, oldExec
		newProber, bindMount, bootcInstall, prepareSelinuxWorkaround = oldProber, oldBind, oldBootc, oldShim
		varIsSpaceConstrained, hostTarget, hostBlock, hostScratchFree = oldVar, oldTarget, oldBlock, oldFree
		install.Exec, install.PlanInspectFn = oldInstallExec, oldPlanInspect
	})
	return g
}

// Fixture image digests and the fake image sources `plan --read-image`
// reads: local containers-storage (skopeo inspect containers-storage:...,
// podman image inspect) and a registry (skopeo inspect docker://...), which
// never touches the network.
const (
	fixtureIndexDigest     = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	fixtureAmd64Digest     = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	fixtureArm64Digest     = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	fixtureCachedDigest    = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	fixtureLocalDigest     = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	fixtureBigDigest       = "sha256:6666666666666666666666666666666666666666666666666666666666666666"
	fixtureAnnotatedDigest = "sha256:8888888888888888888888888888888888888888888888888888888888888888"
	fixtureLayerDigestHex  = "7777777777777777777777777777777777777777777777777777777777777777"
)

// fixtureInspect is `skopeo inspect` output with the given layer sizes.
func fixtureInspect(digest string, sizes ...int64) string {
	return fixtureInspectAnnotated(digest, nil, sizes...)
}

// fixtureInspectAnnotated is fixtureInspect with each layer's
// uncompressed-size annotation from unpacked (none past its end).
func fixtureInspectAnnotated(digest string, unpacked []int64, sizes ...int64) string {
	var layers, data []string
	for i, n := range sizes {
		d := fmt.Sprintf("\"sha256:%d%s\"", i, fixtureLayerDigestHex[1:])
		layers = append(layers, d)
		ann := "null"
		if i < len(unpacked) {
			ann = fmt.Sprintf(`{%q:"%d"}`, install.UncompressedSizeAnnotation, unpacked[i])
		}
		data = append(data, fmt.Sprintf(`{"MIMEType":"application/vnd.oci.image.layer.v1.tar+zstd","Digest":%s,"Size":%d,"Annotations":%s}`, d, n, ann))
	}
	return fmt.Sprintf(`{"Name":"fixture","Digest":%q,"RepoTags":[],"Created":"2026-11-01T00:00:00Z","Architecture":"amd64","Os":"linux",`+
		`"Labels":{"containers.bootc":"1"},"Layers":[%s],"LayersData":[%s]}`, digest, strings.Join(layers, ","), strings.Join(data, ","))
}

type fakeImageAnswer struct {
	out string
	err error
}

// fakeImages is a stand-in for local containers-storage and the registry,
// keyed by the command line after the program name. registry records each
// registry read; none of them leaves the process.
type fakeImages struct {
	g        *commandGuard
	answers  map[string]fakeImageAnswer
	mu       sync.Mutex
	registry []string
}

func (f *fakeImages) inspect(name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	if name == "skopeo" && strings.Contains(line, " docker://") {
		f.mu.Lock()
		f.registry = append(f.registry, line) // faked: it never leaves the process
		f.mu.Unlock()
	} else {
		_ = f.g.check(name, args) // the guard decides
	}
	if a, ok := f.answers[line]; ok {
		if a.err != nil {
			return nil, a.err
		}
		return []byte(a.out), nil
	}
	return nil, errors.New("fixture: no such image")
}

// stubImages makes `plan --read-image` read the fixture images.
func stubImages(t *testing.T, g *commandGuard) *fakeImages {
	t.Helper()
	const gib = int64(1) << 30
	f := &fakeImages{g: g, answers: map[string]fakeImageAnswer{
		// A multi-platform image that only the registry has.
		"skopeo inspect --no-tags docker://ghcr.io/example/os:stable": {out: fixtureInspect(fixtureIndexDigest, 1200<<20, 900<<20, 300<<20)},
		"skopeo inspect --raw docker://ghcr.io/example/os:stable": {out: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
			`{"digest":"` + fixtureArm64Digest + `","platform":{"architecture":"arm64","os":"linux"}},` +
			`{"digest":"` + fixtureAmd64Digest + `","platform":{"architecture":"amd64","os":"linux"}}]}`},
		// An image in local containers-storage: the registry is not asked.
		"skopeo inspect containers-storage:ghcr.io/example/cached:stable":       {out: fixtureInspect(fixtureCachedDigest, 2*gib, 1*gib)},
		"podman image inspect --format {{.Size}} ghcr.io/example/cached:stable": {out: fmt.Sprintf("%d\n", 8*gib)},
		"skopeo inspect containers-storage:ghcr.io/example/local:stable":        {out: fixtureInspect(fixtureLocalDigest, 3*gib)},
		"podman image inspect --format {{.Size}} ghcr.io/example/local:stable":  {out: fmt.Sprintf("%d\n", 9*gib)},
		"skopeo inspect --no-tags docker://ghcr.io/example/big:stable":          {out: fixtureInspect(fixtureBigDigest, 4*gib, 2*gib)},
		"skopeo inspect --raw docker://ghcr.io/example/big:stable":              {out: `{"schemaVersion":2,"layers":[]}`},
		// A registry image whose layers record their uncompressed size.
		"skopeo inspect --no-tags docker://ghcr.io/example/annotated:stable": {out: fixtureInspectAnnotated(fixtureAnnotatedDigest,
			[]int64{5 * gib, 2 * gib}, 2*gib, 1*gib)},
		"skopeo inspect --raw docker://ghcr.io/example/annotated:stable": {out: `{"schemaVersion":2,"layers":[]}`},
		"skopeo inspect --no-tags docker://ghcr.io/example/offline:stable": {err: &exec.ExitError{ProcessState: &os.ProcessState{},
			Stderr: []byte("time=\"2026-11-12T00:00:00Z\" level=fatal msg=\"Error parsing image name: pinging container registry ghcr.io: dial tcp: lookup ghcr.io: no such host\"\n")}},
	}}
	oldInspect, oldArch := install.PlanInspectFn, install.HostArchitecture
	install.PlanInspectFn = f.inspect
	install.HostArchitecture = func() string { return "amd64" }
	t.Cleanup(func() { install.PlanInspectFn, install.HostArchitecture = oldInspect, oldArch })
	return f
}

// planFixtureTargets is what the stand-in host reports about each fixture
// disk, by base name: sda has a serial, a WWN and a by-id link; nvme0n1
// reports no identity; loop0 is a small loop device (an image for another
// machine). Any other existing path is an eligible disk with no identity.
var planFixtureTargets = map[string]probe.Target{
	"sda": {SizeBytes: 500107862016, Serial: "FIXTURE-SDA-SERIAL", WWN: "0x5000c500a1b2c3d4", Model: "Fixture Disk",
		ByID: []string{"/dev/disk/by-id/ata-Fixture_Disk_FIXTURE-SDA-SERIAL", "/dev/disk/by-id/wwn-0x5000c500a1b2c3d4"}},
	"sdb":     {SizeBytes: 256060514304, Serial: "FIXTURE-SDB-SERIAL", Model: "Fixture USB"},
	"nvme0n1": {SizeBytes: probe.MinDiskBytes},
	"loop0":   {SizeBytes: 8 << 30, Loop: true},
}

// planHost is a hostTarget for the plan fixtures: planFixtureTargets, with
// changed replacing the entries it names (a device that changed after the
// plan).
func planHost(changed map[string]probe.Target) func(string) (probe.Target, error) {
	return func(path string) (probe.Target, error) {
		t, ok := changed[filepath.Base(path)]
		if !ok {
			t, ok = planFixtureTargets[filepath.Base(path)]
		}
		if !ok {
			t = probe.Target{SizeBytes: probe.MinDiskBytes}
		}
		t.Path, t.Device = path, path
		return t, nil
	}
}

// planFixturePartitions is what the stand-in lsblk reports about each
// fixture partition a manual layout names, by base name. sda4 (swap) is
// not reported: its size is unknown.
var planFixturePartitions = map[string]int64{
	"sda1": 2 << 30,
	"sda2": 2 << 30,
	"sda3": 100 << 30,
	"sda5": 4 << 30,
}

// planBlocks is a hostBlock for the plan fixtures.
func planBlocks(path string) (probe.Block, error) {
	n, ok := planFixturePartitions[filepath.Base(path)]
	if !ok {
		return probe.Block{Path: path, Device: path}, errors.New("fixture: lsblk reports no block device " + path)
	}
	return probe.Block{Path: path, Device: path, Type: "part", SizeBytes: n}, nil
}

// planFixtureFreeBytes is the free space of the stand-in host's /var: 20 GiB.
const planFixtureFreeBytes int64 = 20 << 30

// planScratchFree is a hostScratchFree for the plan fixtures: /var holds
// free bytes, and /var/fisherman-tmp does not exist yet, so the free space
// is read at /var. No real statfs reaches a golden.
func planScratchFree(free int64) func(string) (int64, string, error) {
	return func(path string) (int64, string, error) {
		if path != "/var/fisherman-tmp" {
			return 0, path, errors.New("fixture: not the host scratch directory")
		}
		return free, "/var", nil
	}
}

// planDisks creates the fixture "disks" the recipes name under @DIR@.
func planDisks(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"sda", "sdb", "nvme0n1", "loop0", "sda1", "sda2", "sda3", "sda4", "sda5"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runPlanFixture runs `plan --json -` on testdata/plan/<name>.json and
// returns the output with the fixture directory and the architecture's root
// type GUID replaced by placeholders.
func runPlanFixture(t *testing.T, dir, name string, extra ...string) (string, int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "plan", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin := strings.NewReader(strings.ReplaceAll(string(raw), "@DIR@", dir))
	var out, errb bytes.Buffer
	code := runPlan(append(append([]string{"--json"}, extra...), "-"), stdin, &out, &errb)
	if errb.Len() != 0 {
		t.Errorf("%s: stderr not empty: %q", name, errb.String())
	}
	got := strings.ReplaceAll(out.String(), dir, "@DIR@")
	got = strings.ReplaceAll(got, disk.LinuxRootPartType(), "@LINUX_ROOT_TYPE@")
	// The digest covers the device paths, which hold the temporary
	// directory; TestPlanBinding* pin what it covers.
	got = planDigestInOutput.ReplaceAllString(got, `$1"@PLAN_DIGEST@"`)
	return got, code
}

// planDigestInOutput is plan.binding.digest as writeJSON prints it.
var planDigestInOutput = regexp.MustCompile(`("binding": \{\s+"digest": )"sha256:[0-9a-f]{64}"`)

var planCases = []struct {
	name string
	exit int
	tpm  bool
	live bool
	// readImage runs `plan --json --read-image -` against fakeImages.
	readImage bool
}{
	{"grub2-plain", 0, false, false, false},
	{"grub2-luks", 0, false, false, false},
	{"grub2-tpm2", 0, true, false, false},
	{"grub2-tpm2-passphrase", 0, true, false, false},
	{"btrfs-subvolumes", 0, false, false, false},
	{"systemd-boot-composefs", 0, false, false, false},
	{"systemd-boot-luks", 0, false, false, false},
	{"zfs", 0, false, false, false},
	{"manual", 0, false, false, false},
	{"manual-var-disk", 0, false, false, false},
	{"var-disk-keep", 0, false, false, false},
	{"containers-storage", 0, false, false, false},
	{"live", 0, false, true, false},
	{"loop-image", 0, false, false, false},
	{"identity-bound", 0, false, false, false},
	{"identity-serial-changed", 1, false, false, false},
	{"identity-size-changed", 1, false, false, false},
	{"identity-invalid", 1, false, false, false},
	{"plan-stale", 1, false, false, false},
	{"plan-binding-invalid", 1, false, false, false},
	{"invalid", 1, false, false, false},
	{"unknown-field", 1, false, false, false},
	{"malformed", 1, false, false, false},
	{"read-image-registry", 0, false, false, true},
	{"read-image-registry-annotated", 0, false, false, true},
	{"read-image-cached", 0, false, false, true},
	{"read-image-local", 0, false, false, true},
	{"read-image-too-big", 0, false, false, true},
	{"read-image-offline", 0, false, false, true},
	{"read-image-manual", 0, false, false, true},
	{"read-image-manual-small-root", 0, false, false, true},
	{"read-image-manual-kept-root", 0, false, false, true},
	{"live", 0, false, true, true},
}

func TestPlanJSONGolden(t *testing.T) {
	dir := planDisks(t)
	for _, c := range planCases {
		t.Run(c.name, func(t *testing.T) {
			g := guardCommands(t)
			stubTPM(t, c.tpm)
			oldLive := hostLive
			hostLive = func() probe.Live {
				if c.live {
					return probe.Live{IsLive: true, LiveImage: "ghcr.io/example/live:1", DetectedBy: "fixture"}
				}
				return probe.Live{}
			}
			t.Cleanup(func() { hostLive = oldLive })

			var images *fakeImages
			var extra []string
			if c.readImage {
				images = stubImages(t, g)
				extra = []string{"--read-image"}
			}
			got, code := runPlanFixture(t, dir, c.name, extra...)
			if code != c.exit {
				t.Fatalf("exit %d, want %d:\n%s", code, c.exit, got)
			}
			for _, s := range planSecrets {
				if strings.Contains(got, s) {
					t.Errorf("the output holds the secret %q:\n%s", s, got)
				}
			}
			golden := filepath.Join("testdata", "plan", c.name+".golden")
			if c.readImage && !strings.HasPrefix(c.name, "read-image-") {
				golden = filepath.Join("testdata", "plan", c.name+".read-image.golden")
			}
			if *updateGolden {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("output differs from %s:\ngot:\n%s\nwant:\n%s", golden, got, want)
			}

			var res planResult
			if err := json.Unmarshal([]byte(got), &res); err != nil {
				t.Fatalf("output is not JSON: %v", err)
			}
			if res.ProtocolVersion != planProtocolVersion || res.Valid != (c.exit == 0) || res.Errors == nil || (res.Plan != nil) != res.Valid {
				t.Errorf("inconsistent result: valid=%v errors=%v plan=%v", res.Valid, res.Errors, res.Plan != nil)
			}
			if len(g.seen) > 0 {
				t.Logf("read-only commands: %v", g.seen)
			}
			if !c.readImage {
				for _, line := range g.seen {
					if strings.HasPrefix(line, "skopeo") || strings.HasPrefix(line, "podman") {
						t.Errorf("plan without --read-image read the image: %q", line)
					}
				}
			} else if images != nil {
				// A registry is asked only for a registry source that local
				// containers-storage does not have.
				wantRegistry := strings.HasPrefix(c.name, "read-image-registry") || c.name == "read-image-too-big" || c.name == "read-image-offline"
				if (len(images.registry) > 0) != wantRegistry {
					t.Errorf("registry reads %v; want any: %v", images.registry, wantRegistry)
				}
			}
		})
	}
}

// An invalid recipe gets exactly the problems `validate --json` reports.
func TestPlanRefusesWhatValidateRefuses(t *testing.T) {
	dir := planDisks(t)
	for _, c := range planCases {
		guardCommands(t)
		stubTPM(t, c.tpm)
		raw, err := os.ReadFile(filepath.Join("testdata", "plan", c.name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		in := strings.ReplaceAll(string(raw), "@DIR@", dir)
		if c.live {
			continue // validate's live answer is the host's; the golden test covers it
		}
		var vout, pout bytes.Buffer
		vcode := runValidateJSON([]string{"--json", "-"}, strings.NewReader(in), &vout, io.Discard)
		pcode := runPlan([]string{"--json", "-"}, strings.NewReader(in), &pout, io.Discard)
		if vcode != pcode {
			t.Errorf("%s: validate exits %d, plan %d", c.name, vcode, pcode)
		}
		var v validateResult
		var p planResult
		if err := json.Unmarshal(vout.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(pout.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if v.Valid != p.Valid || !reflect.DeepEqual(v.Errors, p.Errors) {
			t.Errorf("%s: validate says %v %v, plan %v %v", c.name, v.Valid, v.Errors, p.Valid, p.Errors)
		}
	}
}

func TestPlanArguments(t *testing.T) {
	for _, c := range []struct {
		args []string
		exit int
		want string
	}{
		{[]string{"--json"}, exitUsage, "needs --json and -"},
		{[]string{"-"}, exitUsage, "needs --json and -"},
		{[]string{"--json", "recipe.json"}, exitUsage, "stdin only"},
		{[]string{"--json", "-", "-"}, exitUsage, "twice"},
		{[]string{"--json", "--partial", "-"}, exitUsage, "unknown argument"},
		{[]string{"--help"}, 0, ""},
	} {
		var out, errb bytes.Buffer
		if got := runPlan(c.args, strings.NewReader("{}"), &out, &errb); got != c.exit {
			t.Errorf("%v: exit %d, want %d", c.args, got, c.exit)
		}
		if c.exit == 0 {
			if !strings.Contains(out.String(), "usage: fisherman plan --json [--read-image] -") {
				t.Errorf("%v: no usage on stdout: %q", c.args, out.String())
			}
			continue
		}
		if out.Len() != 0 || !strings.Contains(errb.String(), c.want) {
			t.Errorf("%v: stdout %q, stderr %q, want %q on stderr", c.args, out.String(), errb.String(), c.want)
		}
	}
}

// The plan's steps are the install's step plan, in order, for every recipe
// shape the step contract covers (TestMainAnnouncesThePlan holds main() to
// the same plan), and its partitions are the ones main() creates.
func TestPlanAgreesWithTheInstall(t *testing.T) {
	guardCommands(t)
	stubTPM(t, true)
	for name, r := range stepVariants(t) {
		if r.SourceReuse != nil {
			continue // its host checks need a bootc store; the plan's source half is pinned above
		}
		p := buildPlan(r, false)
		var got, want []string
		for _, s := range p.Steps {
			got = append(got, s.Name)
		}
		for _, s := range planSteps(r, true) {
			want = append(want, s.name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: plan steps %v, install plan %v", name, got, want)
		}
		if r.IsManualLayout() {
			if len(p.Partitions) != len(r.CustomMounts) || p.Destroys == nil {
				t.Errorf("%s: %d partitions for %d customMounts", name, len(p.Partitions), len(r.CustomMounts))
			}
			continue
		}
		// main() formats and mounts an ext4 /boot exactly when the recipe
		// does not use systemd-boot; the layout has a /boot partition then.
		hasBoot := false
		for _, pp := range p.Partitions {
			hasBoot = hasBoot || pp.Role == disk.RoleBoot
		}
		if hasBoot != !r.UsesSystemdBoot() {
			t.Errorf("%s: plan has /boot=%v, the install formats /boot=%v", name, hasBoot, !r.UsesSystemdBoot())
		}
		if p.Encryption.LUKS != r.EncryptsRoot() || p.Encryption.TPM2 != r.EnrollsTPM2() {
			t.Errorf("%s: plan encryption %+v", name, p.Encryption)
		}
	}
}

// docs/PLAN.md names every top-level and plan field the output has.
func TestPlanDocCoversEveryField(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "PLAN.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, typ := range []any{planResult{}, installPlan{}, planSource{}, planImage{}, planTarget{}, planIdentity{}, planBoot{}, planEncryption{},
		planPartition{}, planVarDisk{}, planDestroy{}, planCapacity{}, planScratch{}, planStep{}, planUnknown{}} {
		rt := reflect.TypeOf(typ)
		for i := 0; i < rt.NumField(); i++ {
			tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
			name := "`" + tag + "`"
			if rt == reflect.TypeOf(installPlan{}) {
				name = "### `plan." + tag + "`" // each has its own section
			}
			if !strings.Contains(doc, name) {
				t.Errorf("docs/PLAN.md does not name %s.%s (%s)", rt.Name(), rt.Field(i).Name, name)
			}
		}
	}
}

// The guard sees a mutation through each path the install takes to run one,
// so the tests above would catch a plan that partitions or formats.
func TestPlanGuardCatchesMutations(t *testing.T) {
	var bad []string
	t.Run("guarded", func(t *testing.T) {
		g := guardCommands(t)
		_ = runner.RunWithStdin(strings.NewReader("label: gpt\n"), "sfdisk", "--wipe=always", "/dev/fisherman-test-nonexistent")
		_ = disk.FormatEFI("/dev/fisherman-test-nonexistent1")
		_, _ = runner.Output("cryptsetup", "luksFormat", "/dev/fisherman-test-nonexistent3")
		_ = runner.DefaultExecutor.Command("zpool", "create", "tank").Run()
		// The image reads of --read-image: a registry read and a pull are
		// refused; a local inspect is allowed.
		_, _ = install.PlanInspectFn("skopeo", "inspect", "--no-tags", "docker://example.invalid/os:1")
		_ = install.Exec.Command("podman", "pull", "example.invalid/os:1").Run()
		_, _ = install.PlanInspectFn("skopeo", "inspect", "containers-storage:example.invalid/os:1")
		g.mu.Lock()
		bad, g.bad = g.bad, nil // seen here; the cleanup must not fail this test
		g.mu.Unlock()
	})
	want := map[string]bool{"sfdisk": false, "mkfs.fat": false, "cryptsetup": false, "zpool": false, "skopeo": false, "podman": false}
	for _, line := range bad {
		want[strings.Fields(line)[0]] = true
	}
	for _, line := range bad {
		if strings.Contains(line, "containers-storage:") {
			t.Errorf("the guard refused a local image read: %q", line)
		}
	}
	for cmd, seen := range want {
		if !seen {
			t.Errorf("the guard did not see %s (it saw %v)", cmd, bad)
		}
	}
}

// A frontend binds the install to the disk it planned on by copying
// plan.target.identity into diskIdentity. When the device changes after the
// plan (another disk on the same node, a resized image, a lost WWN), plan,
// validate and the install's pre-flight (installProblems, which main()
// runs before any disk step) all refuse it with disk_identity_mismatch.
func TestPlanChangedDevice(t *testing.T) {
	dir := planDisks(t)
	guardCommands(t)
	base := `{"disk":"` + dir + `/sda","filesystem":"xfs","image":"ghcr.io/example/os:stable","hostname":"fixture",` +
		`"encryption":{"type":"luks-passphrase","passphrase":"SENTINEL-LUKS-PASSPHRASE"}`
	plan := func(in string) (planResult, int) {
		var out bytes.Buffer
		code := runPlan([]string{"--json", "-"}, strings.NewReader(in), &out, io.Discard)
		var res planResult
		if err := json.Unmarshal(out.Bytes(), &res); err != nil {
			t.Fatalf("plan output is not JSON: %v\n%s", err, out.String())
		}
		for _, s := range planSecrets {
			if strings.Contains(out.String(), s) {
				t.Errorf("the plan holds the secret %q", s)
			}
		}
		return res, code
	}

	first, code := plan(base + "}")
	if code != 0 || first.Plan.Target.Identity == nil {
		t.Fatalf("first plan: exit %d, %+v", code, first)
	}
	id := first.Plan.Target.Identity
	if id.Bound || id.Serial != "FIXTURE-SDA-SERIAL" || first.Plan.Target.StableID != "0x5000c500a1b2c3d4" {
		t.Errorf("first plan identity: %+v, stable_id %q", id, first.Plan.Target.StableID)
	}
	binding, _ := json.Marshal(map[string]any{"serial": id.Serial, "wwn": id.WWN, "sizeBytes": id.SizeBytes})
	bound := base + `,"diskIdentity":` + string(binding) + "}"

	if again, code := plan(bound); code != 0 || !again.Plan.Target.Identity.Bound {
		t.Fatalf("the unchanged disk: exit %d, %+v", code, again.Errors)
	}

	for _, c := range []struct {
		name   string
		now    probe.Target
		fields []string
	}{
		{"another disk on the same node", probe.Target{SizeBytes: 500107862016, Serial: "OTHER-SERIAL", WWN: "0x5000c500deadbeef"},
			[]string{"diskIdentity.serial", "diskIdentity.wwn"}},
		{"the size changed", probe.Target{SizeBytes: 1 << 40, Serial: "FIXTURE-SDA-SERIAL", WWN: "0x5000c500a1b2c3d4"},
			[]string{"diskIdentity.sizeBytes"}},
		{"the WWN is gone", probe.Target{SizeBytes: 500107862016, Serial: "FIXTURE-SDA-SERIAL"},
			[]string{"diskIdentity.wwn"}},
		{"nothing matches", probe.Target{SizeBytes: probe.MinDiskBytes},
			[]string{"diskIdentity.serial", "diskIdentity.wwn", "diskIdentity.sizeBytes"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			hostTarget = planHost(map[string]probe.Target{"sda": c.now})
			t.Cleanup(func() { hostTarget = planHost(nil) })

			res, code := plan(bound)
			if code != exitFailure || res.Valid || res.Plan != nil {
				t.Fatalf("plan: exit %d valid %v", code, res.Valid)
			}
			var got []string
			for _, p := range res.Errors {
				if p.Code != recipe.CodeDiskIdentityMismatch {
					t.Errorf("unexpected problem %+v", p)
				}
				got = append(got, p.Field)
			}
			if !reflect.DeepEqual(got, c.fields) {
				t.Errorf("mismatched fields %v, want %v", got, c.fields)
			}

			var vout bytes.Buffer
			if vcode := runValidateJSON([]string{"--json", "-"}, strings.NewReader(bound), &vout, io.Discard); vcode != code {
				t.Errorf("validate exits %d, plan %d", vcode, code)
			}
			var v validateResult
			if err := json.Unmarshal(vout.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(v.Errors, res.Errors) {
				t.Errorf("validate %v, plan %v", v.Errors, res.Errors)
			}

			r, err := recipe.Parse([]byte(bound))
			if err != nil {
				t.Fatal(err)
			}
			if ps := installProblems(r); !reflect.DeepEqual(ps, res.Errors) {
				t.Errorf("the install's pre-flight reports %v, plan %v", ps, res.Errors)
			}
		})
	}
}

func TestSfdiskBytes(t *testing.T) {
	for in, want := range map[string]int64{"2GiB": 2 << 30, "1GiB": 1 << 30, "512MiB": 512 << 20, "4KiB": 4 << 10, "1TiB": 1 << 40} {
		if got, ok := sfdiskBytes(in); !ok || got != want {
			t.Errorf("sfdiskBytes(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "rest", "2G", "-1GiB", "0GiB", "xGiB", "99999999999999TiB"} {
		if got, ok := sfdiskBytes(in); ok {
			t.Errorf("sfdiskBytes(%q) = %d, want not ok", in, got)
		}
	}
}

// The capacity the plan reports adds up: the partitions before the root and
// the alignment reserve, then the root, are the disk.
func TestPlanCapacityAddsUp(t *testing.T) {
	dir := planDisks(t)
	guardCommands(t)
	for _, name := range []string{"grub2-plain", "systemd-boot-composefs", "zfs", "loop-image"} {
		got, code := runPlanFixture(t, dir, name)
		if code != 0 {
			t.Fatalf("%s: exit %d", name, code)
		}
		var res planResult
		if err := json.Unmarshal([]byte(got), &res); err != nil {
			t.Fatal(err)
		}
		c := res.Plan.Capacity
		var sum int64
		for _, pp := range res.Plan.Partitions {
			sum += pp.SizeBytes
		}
		if c == nil || c.RootBytes+c.ReservedBytes != res.Plan.Target.SizeBytes || sum+planAlignmentBytes != res.Plan.Target.SizeBytes {
			t.Errorf("%s: capacity %+v, partitions sum %d, disk %d", name, c, sum, res.Plan.Target.SizeBytes)
		}
	}
}

// planImageFit reports enough only with the unpacked size, folds the
// scratch estimate in when scratch is on the target, and reports
// not_enough from the compressed size alone, a lower bound.
func TestPlanImageFit(t *testing.T) {
	const gib = int64(1) << 30
	for _, c := range []struct {
		name      string
		root      int64
		sizeKnown bool
		img       planImage
		onTarget  bool
		want      string
	}{
		{"no disk size", 0, false, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 8 * gib}, false, planUnknownValue},
		{"no image size", 40 * gib, true, planImage{}, false, planUnknownValue},
		{"compressed only, root larger", 40 * gib, true, planImage{CompressedBytes: 3 * gib}, false, planUnknownValue},
		{"compressed only, root smaller", 2 * gib, true, planImage{CompressedBytes: 3 * gib}, false, "not_enough"},
		{"unpacked fits", 40 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 8 * gib}, false, "enough"},
		{"unpacked does not fit", 6 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 8 * gib}, false, "not_enough"},
		{"scratch on target fits", 12 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 8 * gib}, true, "enough"},
		{"scratch on target may not fit", 10 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 8 * gib}, true, planUnknownValue},
		{"scratch on target, no compressed size", 40 * gib, true, planImage{UnpackedBytes: 8 * gib}, true, planUnknownValue},
		// An estimated unpacked size makes it enough, never not_enough:
		// only the compressed lower bound does.
		{"estimate fits", 40 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 9 * gib, UnpackedFrom: "estimate"}, false, "enough"},
		{"estimate does not fit", 6 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 9 * gib, UnpackedFrom: "estimate"}, false, planUnknownValue},
		{"estimate, below the compressed size", 2 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 9 * gib, UnpackedFrom: "estimate"}, false, "not_enough"},
		{"estimate and scratch on target fit", 12 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 9 * gib, UnpackedFrom: "estimate"}, true, "enough"},
		{"annotations do not fit", 6 * gib, true, planImage{CompressedBytes: 3 * gib, UnpackedBytes: 8 * gib, UnpackedFrom: "annotations"}, false, "not_enough"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := planScratch{OnTarget: c.onTarget}
			if c.img.CompressedBytes > 0 && c.img.UnpackedBytes > 0 {
				s.NeedBytes = c.img.CompressedBytes + c.img.UnpackedBytes
			}
			var reasons []string
			why := ""
			if !c.sizeKnown {
				why = "the target disk size is unknown"
			}
			got := planImageFit(&planCapacity{RootBytes: c.root}, c.img, s, why, func(f, r string) { reasons = append(reasons, f) })
			if got != c.want {
				t.Errorf("image_fit %q, want %q", got, c.want)
			}
			if (got == planUnknownValue) != (len(reasons) == 1 && reasons[0] == "capacity.image_fit") {
				t.Errorf("image_fit %q with unknown entries %v", got, reasons)
			}
		})
	}
}

// planScratchFit reports enough when the host's free space holds the
// upper estimate, not_enough only when an OCI export of the compressed
// layers alone does not fit, and leaves scratch on the target to
// capacity.image_fit.
func TestPlanScratchFit(t *testing.T) {
	const gib = int64(1) << 30
	img := planImage{CompressedBytes: 3 * gib, UnpackedBytes: 8 * gib}
	host := func(free int64) planScratch {
		return planScratch{Path: "/var/fisherman-tmp", NeedBytes: 11 * gib, FreeBytes: free, FreePath: "/var"}
	}
	for _, c := range []struct {
		name       string
		s          planScratch
		img        planImage
		exportsOCI bool
		want       string
	}{
		{"on target", planScratch{OnTarget: true, NeedBytes: 11 * gib}, img, true, planUnknownValue},
		{"free space unknown", planScratch{NeedBytes: 11 * gib}, img, true, planUnknownValue},
		{"holds the estimate", host(20 * gib), img, false, "enough"},
		{"holds the estimate exactly", host(11 * gib), img, true, "enough"},
		{"less than the estimate", host(5 * gib), img, false, planUnknownValue},
		{"less than the estimate, OCI export fits", host(5 * gib), img, true, planUnknownValue},
		{"OCI export does not fit", host(2 * gib), img, true, "not_enough"},
		{"no OCI export, little space", host(2 * gib), img, false, planUnknownValue},
		{"no estimate", planScratch{FreeBytes: 20 * gib, FreePath: "/var"}, planImage{}, true, planUnknownValue},
	} {
		t.Run(c.name, func(t *testing.T) {
			var fields []string
			got := planScratchFit(c.s, c.img, c.exportsOCI, func(f, _ string) { fields = append(fields, f) })
			if got != c.want {
				t.Errorf("fit %q, want %q", got, c.want)
			}
			if (got == planUnknownValue) != (len(fields) == 1 && fields[0] == "scratch.fit") {
				t.Errorf("fit %q with unknown entries %v", got, fields)
			}
		})
	}
}

// The plan reads the host scratch's free space only when scratch is on
// the host, and reports what it compares it with.
func TestPlanScratchFree(t *testing.T) {
	dir := planDisks(t)
	composefs := `{"disk":"` + dir + `/sda","filesystem":"btrfs","bootloader":"systemd","composeFsBackend":true,` +
		`"image":"ghcr.io/example/cached:stable","hostname":"fixture","encryption":{"type":"none"}}`
	plan := func(t *testing.T) *installPlan {
		t.Helper()
		var out bytes.Buffer
		if code := runPlan([]string{"--json", "--read-image", "-"}, strings.NewReader(composefs), &out, io.Discard); code != 0 {
			t.Fatalf("exit %d:\n%s", code, out.String())
		}
		var res planResult
		if err := json.Unmarshal(out.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res.Plan
	}
	unknownField := func(p *installPlan, field string) bool {
		for _, u := range p.Unknown {
			if u.Field == field {
				return true
			}
		}
		return false
	}

	t.Run("OCI export does not fit", func(t *testing.T) {
		stubImages(t, guardCommands(t))
		hostScratchFree = planScratchFree(1 << 30)
		p := plan(t)
		if p.Scratch.FreeBytes != 1<<30 || p.Scratch.FreePath != "/var" || p.Scratch.Fit != "not_enough" {
			t.Errorf("scratch %+v", p.Scratch)
		}
	})
	t.Run("holds the estimate", func(t *testing.T) {
		stubImages(t, guardCommands(t))
		p := plan(t)
		if p.Scratch.FreeBytes != planFixtureFreeBytes || p.Scratch.Fit != "enough" || unknownField(p, "scratch.fit") {
			t.Errorf("scratch %+v", p.Scratch)
		}
	})
	t.Run("statfs fails", func(t *testing.T) {
		stubImages(t, guardCommands(t))
		hostScratchFree = func(string) (int64, string, error) { return 0, "/var", errors.New("statfs /var: permission denied") }
		p := plan(t)
		if p.Scratch.FreeBytes != 0 || p.Scratch.FreePath != "" || p.Scratch.Fit != planUnknownValue ||
			!unknownField(p, "scratch.free_bytes") || !unknownField(p, "scratch.fit") {
			t.Errorf("scratch %+v, unknown %v", p.Scratch, p.Unknown)
		}
	})
	t.Run("scratch on the target", func(t *testing.T) {
		stubImages(t, guardCommands(t))
		varIsSpaceConstrained = func() bool { return true }
		hostScratchFree = func(path string) (int64, string, error) {
			t.Errorf("plan read the free space of %s for scratch on the target", path)
			return 0, path, errors.New("unexpected")
		}
		p := plan(t)
		if !p.Scratch.OnTarget || p.Scratch.FreeBytes != 0 || p.Scratch.Fit != planUnknownValue || unknownField(p, "scratch.free_bytes") {
			t.Errorf("scratch %+v", p.Scratch)
		}
	})
}

// scratchFreeBytes reads the nearest existing parent of a scratch
// directory that does not exist yet, and creates nothing.
func TestScratchFreeBytesCreatesNothing(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "a", "b", "fisherman-tmp")
	free, at, err := scratchFreeBytes(missing)
	if err != nil || at != base || free <= 0 {
		t.Errorf("scratchFreeBytes(%s) = %d, %q, %v; want the free space of %s", missing, free, at, err, base)
	}
	if _, err := os.Stat(filepath.Join(base, "a")); !os.IsNotExist(err) {
		t.Errorf("scratchFreeBytes created %s/a (%v)", base, err)
	}
	if _, at, err := scratchFreeBytes(base); err != nil || at != base {
		t.Errorf("an existing directory: %q, %v", at, err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scratchFreeBytes(filepath.Join(file, "fisherman-tmp")); err == nil {
		t.Error("a scratch path under a regular file has free space")
	}
}

// The reads a manual layout's capacity adds go through the guarded prober
// (lsblk only) and the real scratch statfs, which creates no directory.
func TestPlanCapacityReadsAreReadOnly(t *testing.T) {
	dir := planDisks(t)
	g := guardCommands(t)
	// The fixture partitions are real files, so the prober reads them at
	// the host root; its commands still go through the guard.
	hostBlock = func(path string) (probe.Block, error) {
		pr := newProber()
		pr.Root = "/"
		return pr.Block(path)
	}
	hostScratchFree = scratchFreeBytes
	_, statErr := os.Stat("/var/fisherman-tmp")
	got, code := runPlanFixture(t, dir, "manual")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, got)
	}
	if _, err := os.Stat("/var/fisherman-tmp"); os.IsNotExist(statErr) != os.IsNotExist(err) {
		t.Errorf("plan changed whether /var/fisherman-tmp exists (%v, then %v)", statErr, err)
	}
	lsblk := 0
	for _, line := range g.seen {
		if strings.HasPrefix(line, "lsblk ") {
			lsblk++
		}
	}
	if lsblk == 0 {
		t.Errorf("the partition sizes were not read through lsblk: %v", g.seen)
	}
	var res planResult
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatal(err)
	}
	for _, pp := range res.Plan.Partitions {
		if pp.SizeBytes != 0 {
			t.Errorf("%s: a size from a guarded lsblk that answered nothing: %d", pp.Device, pp.SizeBytes)
		}
	}
	if res.Plan.Capacity == nil || res.Plan.Capacity.ImageFit != planUnknownValue {
		t.Errorf("capacity %+v", res.Plan.Capacity)
	}
}
