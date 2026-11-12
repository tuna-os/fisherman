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
	oldVar := varIsSpaceConstrained
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
		varIsSpaceConstrained = oldVar
	})
	return g
}

// planDisks creates the fixture "disks" the recipes name under @DIR@.
func planDisks(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"sda", "sdb", "nvme0n1", "sda1", "sda2", "sda3", "sda4"} {
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
	for _, typ := range []any{planResult{}, installPlan{}, planSource{}, planTarget{}, planBoot{}, planEncryption{},
		planPartition{}, planVarDisk{}, planDestroy{}, planScratch{}, planStep{}, planUnknown{}} {
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
