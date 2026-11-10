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
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// realTarget undoes the stand-in host of seams_test.go for one test: the
// target verdict comes from probe.Target again, over a Prober whose lsblk
// answers with the given device tree.
func realTarget(t *testing.T, lsblk string, uefi bool) {
	t.Helper()
	oldTarget, oldUEFI := hostTarget, hostUEFI
	t.Cleanup(func() { hostTarget, hostUEFI = oldTarget, oldUEFI })
	hostTarget = func(path string) (probe.Target, error) {
		p := &probe.Prober{Root: "/", Getenv: func(string) string { return "" },
			Run: func(name string, args ...string) ([]byte, error) {
				if name != "lsblk" || lsblk == "" {
					return nil, errors.New("lsblk: exit status 1")
				}
				return []byte(lsblk), nil
			}}
		return p.Target(path)
	}
	hostUEFI = func() bool { return uefi }
}

func problemCodes(ps []recipe.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Field+":"+p.Code)
	}
	return out
}

// tuna-os/fisherman#402: `{"disk":"/etc/hostname"}` validated clean, and a
// recipe could name the live USB, a partition, a read-only or undersized
// disk, on a BIOS host. Each is now a coded problem from installProblems,
// which both `validate` and the install run before any disk step.
func TestInstallProblemsTargetEligibility(t *testing.T) {
	stubTPM(t, false)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good, live, ro, small, part, loop, file := node("sda"), node("sdb"), node("sdc"), node("sdd"), node("sda1"), node("loop3"), node("plain-file")
	link := filepath.Join(dir, "by-id-link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	lsblk := fmt.Sprintf(`{"blockdevices":[
	 {"name":%q,"type":"disk","size":500107862016,"ro":false,"mountpoints":[null],
	  "children":[{"name":%q,"type":"part","size":1,"ro":false,"mountpoints":[null]}]},
	 {"name":%q,"type":"disk","size":500107862016,"ro":false,"mountpoints":[null],
	  "children":[{"name":"x1","type":"part","size":1,"ro":false,"mountpoints":["/run/initramfs/live"]}]},
	 {"name":%q,"type":"disk","size":500107862016,"ro":true,"mountpoints":[null]},
	 {"name":%q,"type":"disk","size":16000000000,"ro":false,"mountpoints":[null]},
	 {"name":%q,"type":"loop","size":2147483648,"ro":false,"mountpoints":[null]}]}`,
		good, part, live, ro, small, loop)

	base := func(disk string) *recipe.Recipe {
		return &recipe.Recipe{Disk: disk, Filesystem: "xfs", Hostname: "h", Image: "ghcr.io/example/os:stable"}
	}
	tests := []struct {
		name string
		r    *recipe.Recipe
		uefi bool
		want []string
	}{
		{"whole disk", base(good), true, nil},
		{"symlink to a whole disk", base(link), true, nil},
		{"regular file", base(file), true, []string{"disk:disk_not_disk"}},
		{"partition", base(part), true, []string{"disk:disk_not_disk"}},
		{"live medium", base(live), true, []string{"disk:disk_boot_disk"}},
		{"read-only", base(ro), true, []string{"disk:disk_read_only"}},
		{"too small", base(small), true, []string{"disk:disk_too_small"}},
		{"bios host", base(good), false, []string{":firmware_not_uefi"}},
		{"too small and bios, overridden", func() *recipe.Recipe {
			r := base(small)
			r.Override = []string{"too_small", "uefi"}
			return r
		}(), false, nil},
		{"override cannot reach the boot disk", func() *recipe.Recipe {
			r := base(live)
			r.Override = []string{"too_small", "uefi"}
			return r
		}(), true, []string{"disk:disk_boot_disk"}},
		{"small loop device on a bios host", base(loop), false, nil},
		{"varDisk is a partition", func() *recipe.Recipe {
			r := base(good)
			r.VarDisk = &recipe.VarDiskSpec{Disk: part}
			return r
		}(), true, []string{"varDisk.disk:var_disk_not_disk"}},
		{"varDisk is the live medium", func() *recipe.Recipe {
			r := base(good)
			r.VarDisk = &recipe.VarDiskSpec{Disk: live}
			return r
		}(), true, []string{"varDisk.disk:var_disk_boot_disk"}},
		{"varDisk is the disk under another name", func() *recipe.Recipe {
			r := base(good)
			r.VarDisk = &recipe.VarDiskSpec{Disk: link}
			return r
		}(), true, []string{"varDisk.disk:var_disk_same_as_disk"}},
		{"missing disk: one problem, not two", base(filepath.Join(dir, "absent")), true, []string{"disk:disk_not_found"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			realTarget(t, lsblk, tt.uefi)
			if got := problemCodes(installProblems(tt.r)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("installProblems = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("lsblk unavailable fails closed", func(t *testing.T) {
		realTarget(t, "", true)
		if got := problemCodes(installProblems(base(good))); !reflect.DeepEqual(got, []string{"disk:disk_unverified"}) {
			t.Errorf("installProblems = %v, want disk_unverified", got)
		}
	})

	t.Run("validate --json reports the code", func(t *testing.T) {
		realTarget(t, lsblk, true)
		raw, _ := json.Marshal(map[string]any{"disk": live, "filesystem": "xfs", "hostname": "h", "image": "x"})
		var out bytes.Buffer
		if code := runValidateJSON([]string{"--json", "-"}, bytes.NewReader(raw), &out, io.Discard); code != 1 {
			t.Fatalf("exit %d, want 1: %s", code, out.String())
		}
		var res validateResult
		if err := json.Unmarshal(out.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if got := problemCodes(res.Errors); !reflect.DeepEqual(got, []string{"disk:disk_boot_disk"}) {
			t.Errorf("errors = %v", got)
		}
	})

	t.Run("--partial reads no machine", func(t *testing.T) {
		oldTarget, oldUEFI := hostTarget, hostUEFI
		t.Cleanup(func() { hostTarget, hostUEFI = oldTarget, oldUEFI })
		hostTarget = func(string) (probe.Target, error) { t.Error("--partial asked for a target verdict"); return probe.Target{}, nil }
		hostUEFI = func() bool { t.Error("--partial asked for the firmware"); return false }
		ps, err := partialProblems([]byte(`{"disk":"` + live + `","override":["boot_disk"]}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := problemCodes(ps); !reflect.DeepEqual(got, []string{"override[0]:override_unsupported"}) {
			t.Errorf("partialProblems = %v", got)
		}
	})
}

// A symlinked disk path must reach the disk steps as the device node:
// disk.PartName builds "/dev/disk/by-id/foo1" from a by-id path.
func TestResolveTargets(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(dir, "sda")
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "by-id")
	if err := os.Symlink(node, link); err != nil {
		t.Fatal(err)
	}
	r := &recipe.Recipe{Disk: link, VarDisk: &recipe.VarDiskSpec{Disk: link}}
	resolveTargets(r)
	if r.Disk != node || r.VarDisk.Disk != node {
		t.Errorf("resolved to %q and %q, want %q", r.Disk, r.VarDisk.Disk, node)
	}
}

// The real CLI, re-executed. A non-root install is refused before the recipe
// is read; an ineligible target is refused, with its code in the error
// event, before the host-tool check or any step.
func TestInstallRefusesBeforeDiskWork(t *testing.T) {
	if mode := os.Getenv("FISHERMAN_TARGET_CHILD"); mode != "" {
		marker := os.Getenv("FISHERMAN_TARGET_MARKER")
		touch := func(what string) { _ = os.WriteFile(marker, []byte(what), 0o600) }
		lookPath = func(name string) (string, error) { touch("tool lookup: " + name); return "", errors.New("refused") }
		switch mode {
		case "non-root":
			geteuid = func() int { return 1000 }
			recipeStdin = readerFunc(func([]byte) (int, error) { touch("recipe read"); return 0, io.EOF })
			os.Args = []string{os.Args[0], "-"}
		default:
			hostTarget = func(path string) (probe.Target, error) {
				return probe.Target{Path: path, Device: path, Reasons: []string{mode}}, nil
			}
			os.Args = []string{os.Args[0], os.Getenv("FISHERMAN_TARGET_RECIPE")}
		}
		main()
		os.Exit(99)
	}

	dir := t.TempDir()
	disk := filepath.Join(dir, "disk")
	if err := os.WriteFile(disk, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"disk": disk, "filesystem": "ext4", "hostname": "h", "image": "fixture.invalid/os:tag"})
	recipePath := filepath.Join(dir, "recipe.json")
	if err := os.WriteFile(recipePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct{ mode, wantCode, wantField, wantMessage string }{
		{"non-root", recipe.CodeNotRoot, "", "cannot install: fisherman must run as root"},
		{probe.ExcludedBootDisk, recipe.CodeDiskBootDisk, "disk", "invalid recipe: disk "},
		{probe.ExcludedNotDisk, recipe.CodeDiskNotDisk, "disk", "invalid recipe: disk "},
		{probe.ExcludedReadOnly, recipe.CodeDiskReadOnly, "disk", "invalid recipe: disk "},
		{probe.ExcludedTooSmall, recipe.CodeDiskTooSmall, "disk", "invalid recipe: disk "},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "marker")
			cmd := exec.Command(os.Args[0], "-test.run=^TestInstallRefusesBeforeDiskWork$", "-test.count=1")
			cmd.Env = append(os.Environ(), "FISHERMAN_TARGET_CHILD="+tt.mode,
				"FISHERMAN_TARGET_MARKER="+marker, "FISHERMAN_TARGET_RECIPE="+recipePath)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitFailure {
				t.Fatalf("exit = %v, want %d\nstdout:\n%s\nstderr:\n%s", err, exitFailure, stdout.String(), stderr.String())
			}
			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("want exactly one event, got:\n%s", stdout.String())
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
				t.Fatal(err)
			}
			field, _ := ev["field"].(string)
			msg, _ := ev["message"].(string)
			if ev["type"] != "error" || ev["code"] != tt.wantCode || field != tt.wantField || !strings.HasPrefix(msg, tt.wantMessage) {
				t.Errorf("event = %v, want error %s (field %q) starting %q", ev, tt.wantCode, tt.wantField, tt.wantMessage)
			}
			if what, err := os.ReadFile(marker); err == nil {
				t.Errorf("the install went past the refusal: %s", what)
			}
			if after, err := os.ReadFile(disk); err != nil || string(after) != "precious" {
				t.Error("the target was modified")
			}
		})
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
