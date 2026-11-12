package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
// zpool, podman pull, bootc install, ...) fails the test.
var readOnlyCommands = map[string]bool{
	"lsblk": true, "blkid": true, "findmnt": true, "test": true, "cat": true, "stat": true,
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
	if !readOnlyCommands[name] {
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
	oldVar, oldTarget := varIsSpaceConstrained, hostTarget
	hostTarget = planHost(nil)
	runner.RunFn = func(_ io.Reader, name string, args ...string) error { return g.check(name, args) }
	runner.OutputFn = func(name string, args ...string) ([]byte, error) { return nil, g.check(name, args) }
	runner.DefaultExecutor = guardedExecutor{g}
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
		varIsSpaceConstrained, hostTarget = oldVar, oldTarget
	})
	return g
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

// planDisks creates the fixture "disks" the recipes name under @DIR@.
func planDisks(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"sda", "sdb", "nvme0n1", "loop0", "sda1", "sda2", "sda3", "sda4"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runPlanFixture runs `plan --json -` on testdata/plan/<name>.json and
// returns the output with the fixture directory and the architecture's root
// type GUID replaced by placeholders.
func runPlanFixture(t *testing.T, dir, name string) (string, int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "plan", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin := strings.NewReader(strings.ReplaceAll(string(raw), "@DIR@", dir))
	var out, errb bytes.Buffer
	code := runPlan([]string{"--json", "-"}, stdin, &out, &errb)
	if errb.Len() != 0 {
		t.Errorf("%s: stderr not empty: %q", name, errb.String())
	}
	got := strings.ReplaceAll(out.String(), dir, "@DIR@")
	got = strings.ReplaceAll(got, disk.LinuxRootPartType(), "@LINUX_ROOT_TYPE@")
	return got, code
}

var planCases = []struct {
	name string
	exit int
	tpm  bool
	live bool
}{
	{"grub2-plain", 0, false, false},
	{"grub2-luks", 0, false, false},
	{"grub2-tpm2", 0, true, false},
	{"grub2-tpm2-passphrase", 0, true, false},
	{"btrfs-subvolumes", 0, false, false},
	{"systemd-boot-composefs", 0, false, false},
	{"systemd-boot-luks", 0, false, false},
	{"zfs", 0, false, false},
	{"manual", 0, false, false},
	{"manual-var-disk", 0, false, false},
	{"var-disk-keep", 0, false, false},
	{"containers-storage", 0, false, false},
	{"live", 0, false, true},
	{"loop-image", 0, false, false},
	{"identity-bound", 0, false, false},
	{"identity-serial-changed", 1, false, false},
	{"identity-size-changed", 1, false, false},
	{"identity-invalid", 1, false, false},
	{"invalid", 1, false, false},
	{"unknown-field", 1, false, false},
	{"malformed", 1, false, false},
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

			got, code := runPlanFixture(t, dir, c.name)
			if code != c.exit {
				t.Fatalf("exit %d, want %d:\n%s", code, c.exit, got)
			}
			for _, s := range planSecrets {
				if strings.Contains(got, s) {
					t.Errorf("the output holds the secret %q:\n%s", s, got)
				}
			}
			golden := filepath.Join("testdata", "plan", c.name+".golden")
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
			if !strings.Contains(out.String(), "usage: fisherman plan --json -") {
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
		p := buildPlan(r)
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
	for _, typ := range []any{planResult{}, installPlan{}, planSource{}, planTarget{}, planIdentity{}, planBoot{}, planEncryption{},
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
		g.mu.Lock()
		bad, g.bad = g.bad, nil // seen here; the cleanup must not fail this test
		g.mu.Unlock()
	})
	want := map[string]bool{"sfdisk": false, "mkfs.fat": false, "cryptsetup": false, "zpool": false}
	for _, line := range bad {
		want[strings.Fields(line)[0]] = true
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
