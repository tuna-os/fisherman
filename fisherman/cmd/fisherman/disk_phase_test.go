package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The disk phase (#313), run against recorded operations: no command,
// mount or format runs. Each test drives diskPhase for a recipe from
// stepVariants (the step contract's variants) and reads back one ordered
// log of what it did: the step events, the info lines, every disk, LUKS
// and ZFS operation, and every teardown registration.

const (
	phaseTarget = "/mnt/fisherman-target"
	phaseMapper = "fisherman-root"
)

// phaseRecorder records the disk phase. fail makes the operation whose log
// line starts with the given prefix return an error.
type phaseRecorder struct {
	log     []string
	fail    string
	stale   bool // the LUKS mapper of a previous run is still open
	stepLog []string
}

func (p *phaseRecorder) rec(format string, args ...any) error {
	line := fmt.Sprintf(format, args...)
	p.log = append(p.log, line)
	if p.fail != "" && strings.HasPrefix(line, p.fail) {
		return errors.New("injected failure")
	}
	return nil
}

func (p *phaseRecorder) ops() diskOps {
	return diskOps{
		applyCustomLayout: func(specs []disk.MountSpec, target string, hooks disk.LayoutHooks) (string, string, []string, error) {
			var desc []string
			for _, s := range specs {
				desc = append(desc, s.Partition+":"+s.Target+":"+s.Fstype)
			}
			if err := p.rec("applyCustomLayout %s %s", strings.Join(desc, ","), target); err != nil {
				return "", "", nil, err
			}
			// The real layout mounts the shallowest target first and hands
			// each mount and the swap to the hooks as it goes.
			var efi string
			var mounted []string
			for _, s := range disk.OrderMounts(specs) {
				if s.Target == "swap" {
					hooks.SwapOn(s.Partition)
					continue
				}
				path := target
				if s.Target != "/" {
					path = target + s.Target
				}
				hooks.Mounted(path)
				mounted = append(mounted, path)
				if s.Target == "/boot/efi" {
					efi = s.Partition
				}
			}
			return target, efi, mounted, nil
		},
		partitionLayout: func(dev string, l disk.Layout) error { return p.rec("partition %s %s", dev, l) },
		formatEFI:       func(part string) error { return p.rec("formatEFI %s", part) },
		formatBoot:      func(part string) error { return p.rec("formatBoot %s", part) },
		mapperExists: func(path string) bool {
			p.log = append(p.log, "stat "+path)
			return p.stale
		},
		randomPassphrase: func() string { p.log = append(p.log, "randomPassphrase"); return "RANDOM-RECOVERY-KEY" },
		luksClose:        func(m string) error { return p.rec("luksClose %s", m) },
		luksFormat:       func(part, pass string) error { return p.rec("luksFormat %s %s", part, pass) },
		luksOpen:         func(part, pass, m string) error { return p.rec("luksOpen %s %s %s", part, pass, m) },
		luksUUID:         func(part string) string { p.log = append(p.log, "luksUUID "+part); return "LUKS-UUID" },
		createZFSPool:    func(pool, part, alt string) error { return p.rec("zpoolCreate %s %s %s", pool, part, alt) },
		createZFSRoot:    func(pool string) error { return p.rec("zfsCreateRoot %s", pool) },
		formatRoot:       func(dev, fs string) error { return p.rec("formatRoot %s %s", dev, fs) },
		mkdirAll:         func(path string, perm os.FileMode) error { return p.rec("mkdir %s %o", path, perm) },
		mountZFSRoot:     func(pool, alt string) error { return p.rec("zfsMountRoot %s %s", pool, alt) },
		setupBtrfsSubvols: func(dev, target string) error {
			return p.rec("btrfsSubvolumes %s %s", dev, target)
		},
		mountType:      func(dev, target, fs, opts string) error { return p.rec("mountType %s %s %s %q", dev, target, fs, opts) },
		mountBoot:      func(target, part string) error { return p.rec("mountBoot %s %s", target, part) },
		mountEFI:       func(target, part string) error { return p.rec("mountEFI %s %s", target, part) },
		prepareVarDisk: func(dev string) error { return p.rec("prepareVarDisk %s", dev) },
		mount:          func(dev, target, opts string) error { return p.rec("mount %s %s %q", dev, target, opts) },
		info:           func(msg string) { p.log = append(p.log, "info "+msg) },
		registerSecret: func(s string) { p.log = append(p.log, "secret "+s) },
		addMount:       func(path string) { p.log = append(p.log, "cleanup.AddMount "+path) },
		setLUKS:        func(m string) { p.log = append(p.log, "cleanup.SetLUKS "+m) },
		setZFSPool:     func(pool string) { p.log = append(p.log, "cleanup.SetZFSPool "+pool) },
		swapOn:         func(dev string) { p.log = append(p.log, "swaps.Add "+dev) },
	}
}

// runPhase runs diskPhase for r with a recording sequencer for planSteps(r,
// pull). It returns the result, the error and the sequencer.
func (p *phaseRecorder) run(r *recipe.Recipe, pull bool) (diskResult, *stepSequencer, error) {
	seq := newStepSequencer(planSteps(r, pull))
	seq.emitFn = func(step, total int, name string, cum, weight int) {
		line := fmt.Sprintf("step %d/%d %s [%s] %d+%d", step, total, name, progress.StepID(name), cum, weight)
		p.log = append(p.log, line)
		p.stepLog = append(p.stepLog, line)
	}
	res, err := diskPhase(r, seq, phaseTarget, phaseMapper, p.ops())
	return res, seq, err
}

// phaseVariant is a step-contract variant with its fixture paths replaced
// by stable ones, so the logs below can name them.
func phaseVariant(t *testing.T, name string) *recipe.Recipe {
	t.Helper()
	r, ok := stepVariants(t)[name]
	if !ok {
		t.Fatalf("no step variant %q", name)
	}
	dir := filepath.Dir(r.Disk)
	if r.Disk == "" {
		dir = filepath.Dir(r.CustomMounts[0].Partition)
	}
	fix := func(s string) string { return strings.Replace(s, dir+"/", "/dev/", 1) }
	r.Disk = fix(r.Disk)
	for i := range r.CustomMounts {
		r.CustomMounts[i].Partition = fix(r.CustomMounts[i].Partition)
	}
	if r.VarDisk != nil {
		r.VarDisk.Disk = fix(r.VarDisk.Disk)
	}
	return r
}

// diskPhaseGolden is what the disk phase does, in order, for each shape.
// Each line was checked against main() before the extraction: the same
// operations with the same arguments, the same registrations right after
// the same operations, the same info lines and the same steps.
var diskPhaseGolden = map[string][]string{
	"plain": {
		"step 1/8 Partitioning disk [partition] 0+0",
		"partition /dev/sda grub2",
		"step 2/8 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"formatBoot /dev/sda2",
		"step 3/8 Formatting root filesystem [format_root] 1+0",
		"formatRoot /dev/sda3 xfs",
		"step 4/8 Mounting filesystem [mount] 1+0",
		"mkdir /mnt/fisherman-target 755",
		`mountType /dev/sda3 /mnt/fisherman-target xfs ""`,
		"cleanup.AddMount /mnt/fisherman-target",
		"mountBoot /mnt/fisherman-target /dev/sda2",
		"cleanup.AddMount /mnt/fisherman-target/boot",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
	},
	"luks": {
		"step 1/9 Partitioning disk [partition] 0+0",
		"partition /dev/sda grub2",
		"step 2/9 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"formatBoot /dev/sda2",
		"step 3/9 Setting up disk encryption [luks] 1+1",
		"stat /dev/mapper/fisherman-root",
		"luksFormat /dev/sda3 correct horse",
		"luksOpen /dev/sda3 correct horse fisherman-root",
		"cleanup.SetLUKS fisherman-root",
		"luksUUID /dev/sda3",
		"step 4/9 Formatting root filesystem [format_root] 2+0",
		"formatRoot /dev/mapper/fisherman-root xfs",
		"step 5/9 Mounting filesystem [mount] 2+0",
		"mkdir /mnt/fisherman-target 755",
		`mountType /dev/mapper/fisherman-root /mnt/fisherman-target xfs ""`,
		"cleanup.AddMount /mnt/fisherman-target",
		"mountBoot /mnt/fisherman-target /dev/sda2",
		"cleanup.AddMount /mnt/fisherman-target/boot",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
	},
	"tpm2": {
		"step 1/10 Partitioning disk [partition] 0+0",
		"partition /dev/sda grub2",
		"step 2/10 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"formatBoot /dev/sda2",
		"step 3/10 Setting up disk encryption [luks] 1+1",
		"randomPassphrase",
		"secret RANDOM-RECOVERY-KEY",
		"info TPM2-LUKS: generated random recovery passphrase; TPM2 will be enrolled after install",
		"stat /dev/mapper/fisherman-root",
		"luksFormat /dev/sda3 RANDOM-RECOVERY-KEY",
		"luksOpen /dev/sda3 RANDOM-RECOVERY-KEY fisherman-root",
		"cleanup.SetLUKS fisherman-root",
		"luksUUID /dev/sda3",
		"step 4/10 Formatting root filesystem [format_root] 2+0",
		"formatRoot /dev/mapper/fisherman-root xfs",
		"step 5/10 Mounting filesystem [mount] 2+0",
		"mkdir /mnt/fisherman-target 755",
		`mountType /dev/mapper/fisherman-root /mnt/fisherman-target xfs ""`,
		"cleanup.AddMount /mnt/fisherman-target",
		"mountBoot /mnt/fisherman-target /dev/sda2",
		"cleanup.AddMount /mnt/fisherman-target/boot",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
	},
	"systemd-boot composefs": {
		"step 1/8 Partitioning disk [partition] 0+0",
		"partition /dev/sda systemd-boot",
		"step 2/8 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"step 3/8 Formatting root filesystem [format_root] 1+0",
		"formatRoot /dev/sda2 btrfs",
		"step 4/8 Mounting filesystem [mount] 1+0",
		"mkdir /mnt/fisherman-target 755",
		`mountType /dev/sda2 /mnt/fisherman-target btrfs "compress=zstd:1"`,
		"cleanup.AddMount /mnt/fisherman-target",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
	},
	"systemd-boot luks": {
		"step 1/9 Partitioning disk [partition] 0+0",
		"partition /dev/sda systemd-boot",
		"step 2/9 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"step 3/9 Setting up disk encryption [luks] 1+1",
		"stat /dev/mapper/fisherman-root",
		"luksFormat /dev/sda2 correct horse",
		"luksOpen /dev/sda2 correct horse fisherman-root",
		"cleanup.SetLUKS fisherman-root",
		"luksUUID /dev/sda2",
		"step 4/9 Formatting root filesystem [format_root] 2+0",
		"formatRoot /dev/mapper/fisherman-root btrfs",
		"step 5/9 Mounting filesystem [mount] 2+0",
		"mkdir /mnt/fisherman-target 755",
		`mountType /dev/mapper/fisherman-root /mnt/fisherman-target btrfs "compress=zstd:1"`,
		"cleanup.AddMount /mnt/fisherman-target",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
	},
	"zfs": {
		"step 1/8 Partitioning disk [partition] 0+0",
		"partition /dev/sda zfs",
		"step 2/8 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"step 3/8 Formatting root filesystem [format_root] 1+0",
		"zpoolCreate rpool /dev/sda2 /mnt/fisherman-target",
		"cleanup.SetZFSPool rpool",
		"zfsCreateRoot rpool",
		"step 4/8 Mounting filesystem [mount] 1+0",
		"mkdir /mnt/fisherman-target 755",
		"zfsMountRoot rpool /mnt/fisherman-target",
		"cleanup.AddMount /mnt/fisherman-target",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
	},
	"btrfs subvolumes": {
		"step 1/8 Partitioning disk [partition] 0+0",
		"partition /dev/sda grub2",
		"step 2/8 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"formatBoot /dev/sda2",
		"step 3/8 Formatting root filesystem [format_root] 1+0",
		"formatRoot /dev/sda3 btrfs",
		"step 4/8 Mounting filesystem [mount] 1+0",
		"mkdir /mnt/fisherman-target 755",
		"btrfsSubvolumes /dev/sda3 /mnt/fisherman-target",
		"cleanup.AddMount /mnt/fisherman-target",
		"mountBoot /mnt/fisherman-target /dev/sda2",
		"cleanup.AddMount /mnt/fisherman-target/boot",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
	},
	"varDisk format": {
		"step 1/9 Partitioning disk [partition] 0+0",
		"partition /dev/sda grub2",
		"step 2/9 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"formatBoot /dev/sda2",
		"step 3/9 Formatting root filesystem [format_root] 1+0",
		"formatRoot /dev/sda3 xfs",
		"step 4/9 Mounting filesystem [mount] 1+0",
		"mkdir /mnt/fisherman-target 755",
		`mountType /dev/sda3 /mnt/fisherman-target xfs ""`,
		"cleanup.AddMount /mnt/fisherman-target",
		"mountBoot /mnt/fisherman-target /dev/sda2",
		"cleanup.AddMount /mnt/fisherman-target/boot",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
		"mkdir /mnt/fisherman-target/var 755",
		"step 5/9 Formatting data disk (/var) [format_var] 1+0",
		"prepareVarDisk /dev/sdb",
		`mount /dev/sdb /mnt/fisherman-target/var ""`,
		"cleanup.AddMount /mnt/fisherman-target/var",
		"info Mounted /var disk /dev/sdb at /var",
	},
	"varDisk keep": {
		"step 1/8 Partitioning disk [partition] 0+0",
		"partition /dev/sda grub2",
		"step 2/8 Formatting EFI partition [format_efi] 0+1",
		"formatEFI /dev/sda1",
		"formatBoot /dev/sda2",
		"step 3/8 Formatting root filesystem [format_root] 1+0",
		"formatRoot /dev/sda3 xfs",
		"step 4/8 Mounting filesystem [mount] 1+0",
		"mkdir /mnt/fisherman-target 755",
		`mountType /dev/sda3 /mnt/fisherman-target xfs ""`,
		"cleanup.AddMount /mnt/fisherman-target",
		"mountBoot /mnt/fisherman-target /dev/sda2",
		"cleanup.AddMount /mnt/fisherman-target/boot",
		"mountEFI /mnt/fisherman-target /dev/sda1",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
		"mkdir /mnt/fisherman-target/var 755",
		"info Keeping existing data on /var disk /dev/sdb",
		`mount /dev/sdb /mnt/fisherman-target/var ""`,
		"cleanup.AddMount /mnt/fisherman-target/var",
		"info Mounted /var disk /dev/sdb at /var",
	},
	"manual": {
		"step 1/5 Preparing disk [prepare_disk] 0+1",
		"applyCustomLayout /dev/sda3:/:xfs,/dev/sda2:/boot:ext4,/dev/sda1:/boot/efi:unformatted /mnt/fisherman-target",
		"cleanup.AddMount /mnt/fisherman-target",
		"cleanup.AddMount /mnt/fisherman-target/boot",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
	},
	"manual+varDisk": {
		"step 1/6 Preparing disk [prepare_disk] 0+1",
		"applyCustomLayout /dev/sda3:/:xfs,/dev/sda2:/boot:ext4,/dev/sda1:/boot/efi:unformatted /mnt/fisherman-target",
		"cleanup.AddMount /mnt/fisherman-target",
		"cleanup.AddMount /mnt/fisherman-target/boot",
		"cleanup.AddMount /mnt/fisherman-target/boot/efi",
		"mkdir /mnt/fisherman-target/var 755",
		"step 2/6 Formatting data disk (/var) [format_var] 1+0",
		"prepareVarDisk /dev/sdb",
		`mount /dev/sdb /mnt/fisherman-target/var ""`,
		"cleanup.AddMount /mnt/fisherman-target/var",
		"info Mounted /var disk /dev/sdb at /var",
	},
}

// diskPhaseResults is what each shape hands to the rest of the install.
var diskPhaseResults = map[string]diskResult{
	"plain": {targetMount: phaseTarget, efiPart: "/dev/sda1", bootPart: "/dev/sda2", rootPart: "/dev/sda3", rootDev: "/dev/sda3"},
	"luks": {targetMount: phaseTarget, efiPart: "/dev/sda1", bootPart: "/dev/sda2", rootPart: "/dev/sda3",
		rootDev: "/dev/mapper/fisherman-root", luksUUID: "LUKS-UUID"},
	"tpm2": {targetMount: phaseTarget, efiPart: "/dev/sda1", bootPart: "/dev/sda2", rootPart: "/dev/sda3",
		rootDev: "/dev/mapper/fisherman-root", luksUUID: "LUKS-UUID", recoveryKey: "RANDOM-RECOVERY-KEY"},
	"systemd-boot composefs": {targetMount: phaseTarget, efiPart: "/dev/sda1", rootPart: "/dev/sda2", rootDev: "/dev/sda2"},
	"systemd-boot luks": {targetMount: phaseTarget, efiPart: "/dev/sda1", rootPart: "/dev/sda2",
		rootDev: "/dev/mapper/fisherman-root", luksUUID: "LUKS-UUID"},
	"zfs":              {targetMount: phaseTarget, efiPart: "/dev/sda1", rootPart: "/dev/sda2", rootDev: "/dev/sda2"},
	"btrfs subvolumes": {targetMount: phaseTarget, efiPart: "/dev/sda1", bootPart: "/dev/sda2", rootPart: "/dev/sda3", rootDev: "/dev/sda3"},
	"varDisk format":   {targetMount: phaseTarget, efiPart: "/dev/sda1", bootPart: "/dev/sda2", rootPart: "/dev/sda3", rootDev: "/dev/sda3"},
	"varDisk keep":     {targetMount: phaseTarget, efiPart: "/dev/sda1", bootPart: "/dev/sda2", rootPart: "/dev/sda3", rootDev: "/dev/sda3"},
	"manual":           {targetMount: phaseTarget, efiPart: "/dev/sda1"},
	"manual+varDisk":   {targetMount: phaseTarget, efiPart: "/dev/sda1"},
}

// For each shape the disk phase does exactly what the golden log says,
// returns what the later steps read, and announces exactly the plan's
// steps before "Installing OS", numbered against the whole plan, with
// their step ids and bar positions. With and without a pull.
func TestDiskPhaseGolden(t *testing.T) {
	for name, want := range diskPhaseGolden {
		for _, pull := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/pull=%v", name, pull), func(t *testing.T) {
				r := phaseVariant(t, name)
				p := &phaseRecorder{}
				res, seq, err := p.run(r, pull)
				if err != nil {
					t.Fatalf("diskPhase: %v", err)
				}
				// The weights differ with a pull only after the disk steps.
				if !reflect.DeepEqual(p.log, want) {
					t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(want, "\n  "))
				}
				if res != diskPhaseResults[name] {
					t.Errorf("result = %+v\nwant     %+v", res, diskPhaseResults[name])
				}

				plan := planSteps(r, pull)
				var wantSteps []string
				for i, s := range plan {
					if s.name == stepInstallOS {
						break
					}
					wantSteps = append(wantSteps, fmt.Sprintf("step %d/%d %s [%s] %d+%d",
						i+1, len(plan), s.name, progress.StepID(s.name), s.cumulativePct, s.weightPct))
				}
				if !reflect.DeepEqual(p.stepLog, wantSteps) {
					t.Errorf("steps = %v\nthe plan before the OS install is %v", p.stepLog, wantSteps)
				}
				if len(seq.errs) != 0 {
					t.Errorf("sequencer: %v", seq.errs)
				}
				// main() goes on with the OS install, the next planned step.
				if seq.next >= len(plan) || plan[seq.next].name != stepInstallOS {
					t.Errorf("after the disk phase the next step is %d of %d, want %q", seq.next, len(plan), stepInstallOS)
				}
			})
		}
	}
}

// Every shape in the golden table is a step-contract variant, and every
// variant but source reuse (which partitions like "plain") has a golden.
func TestDiskPhaseGoldenCoversTheVariants(t *testing.T) {
	for name := range stepVariants(t) {
		if _, ok := diskPhaseGolden[name]; !ok && name != "source reuse" && name != "tpm2+passphrase" {
			t.Errorf("variant %q has no disk-phase golden", name)
		}
	}
	for name := range diskPhaseGolden {
		if _, ok := diskPhaseResults[name]; !ok {
			t.Errorf("golden %q has no result", name)
		}
	}
}

// A stale mapper from an interrupted run is closed before luksFormat, and
// a tpm2-luks-passphrase install uses the recipe's passphrase and has no
// recovery key.
func TestDiskPhaseStaleMapperAndTPM2Passphrase(t *testing.T) {
	r := phaseVariant(t, "tpm2+passphrase")
	r.LuksMapperName = "custom-root"
	p := &phaseRecorder{stale: true}
	seq := newStepSequencer(planSteps(r, true))
	seq.emitFn = func(int, int, string, int, int) {}
	res, err := diskPhase(r, seq, "/mnt/other", "custom-root", p.ops())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"stat /dev/mapper/custom-root",
		"info Closing stale mapper custom-root from previous run",
		"luksClose custom-root",
		"luksFormat /dev/sda3 correct horse",
		"luksOpen /dev/sda3 correct horse custom-root",
		"cleanup.SetLUKS custom-root",
		"luksUUID /dev/sda3",
		"formatRoot /dev/mapper/custom-root xfs",
	}
	var got []string
	for _, l := range p.log {
		for _, w := range want {
			if l == w {
				got = append(got, l)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LUKS sequence = %v\nwant %v", got, want)
	}
	for _, l := range p.log {
		if strings.HasPrefix(l, "randomPassphrase") || strings.HasPrefix(l, "secret ") {
			t.Errorf("tpm2-luks-passphrase generated a key: %q", l)
		}
	}
	if res.recoveryKey != "" || res.luksUUID != "LUKS-UUID" || res.targetMount != "/mnt/other" {
		t.Errorf("result = %+v", res)
	}
	// A failed close of the stale mapper is ignored, as before.
	p = &phaseRecorder{stale: true, fail: "luksClose"}
	seq = newStepSequencer(planSteps(r, true))
	seq.emitFn = func(int, int, string, int, int) {}
	if _, err := diskPhase(r, seq, "/mnt/other", "custom-root", p.ops()); err != nil {
		t.Errorf("a failed stale-mapper close ended the phase: %v", err)
	}
}

// A failing operation ends the phase at once with the failure code and
// message the inline fatalCode had, and leaves registered for teardown
// exactly what was acquired before it.
func TestDiskPhaseFailures(t *testing.T) {
	tests := []struct {
		variant, fail string
		code, msg     string
	}{
		{"plain", "partition", progress.FailurePartitionFailed, "partitioning disk: injected failure"},
		{"plain", "formatEFI", progress.FailureFormatFailed, "formatting EFI: injected failure"},
		{"plain", "formatBoot", progress.FailureFormatFailed, "formatting /boot: injected failure"},
		{"plain", "formatRoot", progress.FailureFormatFailed, "formatting root filesystem: injected failure"},
		{"plain", "mkdir /mnt/fisherman-target ", progress.FailureMountFailed, "creating mount point /mnt/fisherman-target: injected failure"},
		{"plain", "mountType", progress.FailureMountFailed, "mounting root: injected failure"},
		{"plain", "mountBoot", progress.FailureMountFailed, "mounting /boot: injected failure"},
		{"plain", "mountEFI", progress.FailureMountFailed, "mounting EFI: injected failure"},
		{"luks", "luksFormat", progress.FailureLUKSFailed, "LUKS format: injected failure"},
		{"luks", "luksOpen", progress.FailureLUKSFailed, "LUKS open: injected failure"},
		{"luks", "formatRoot", progress.FailureFormatFailed, "formatting root filesystem: injected failure"},
		{"zfs", "zpoolCreate", progress.FailureZFSFailed, "creating ZFS pool: injected failure"},
		{"zfs", "zfsCreateRoot", progress.FailureZFSFailed, "creating ZFS root dataset: injected failure"},
		{"zfs", "zfsMountRoot", progress.FailureZFSFailed, "mounting ZFS root: injected failure"},
		{"btrfs subvolumes", "btrfsSubvolumes", progress.FailureMountFailed, "setting up btrfs subvolumes: injected failure"},
		{"systemd-boot composefs", "mountEFI", progress.FailureMountFailed, "mounting EFI: injected failure"},
		{"varDisk format", "mkdir /mnt/fisherman-target/var", progress.FailureMountFailed, "creating /var mount point: injected failure"},
		{"varDisk format", "prepareVarDisk", progress.FailureFormatFailed, "formatting /var disk: injected failure"},
		{"varDisk keep", "mount /dev/sdb", progress.FailureMountFailed, "mounting /var disk: injected failure"},
		{"manual", "applyCustomLayout", progress.FailureManualLayoutFailed, "manual disk layout: injected failure"},
		{"manual+varDisk", "prepareVarDisk", progress.FailureFormatFailed, "formatting /var disk: injected failure"},
	}
	for _, tt := range tests {
		t.Run(tt.variant+"/"+tt.fail, func(t *testing.T) {
			r := phaseVariant(t, tt.variant)
			p := &phaseRecorder{fail: tt.fail}
			_, _, err := p.run(r, true)
			var pe *phaseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v (%T), want a *phaseError", err, err)
			}
			if pe.code != tt.code || pe.msg != tt.msg {
				t.Errorf("failure = %q %q, want %q %q", pe.code, pe.msg, tt.code, tt.msg)
			}
			// The log is the golden log up to and including the failed
			// operation: nothing after it runs or is registered.
			golden := diskPhaseGolden[tt.variant]
			n := len(p.log)
			if n == 0 || !strings.HasPrefix(p.log[n-1], tt.fail) {
				t.Fatalf("the phase went on after %q failed: %v", tt.fail, p.log)
			}
			if !reflect.DeepEqual(p.log, golden[:n]) {
				t.Errorf("log before the failure:\n  %s\nwant the golden prefix:\n  %s",
					strings.Join(p.log, "\n  "), strings.Join(golden[:n], "\n  "))
			}
		})
	}
}

// failPhase ends the install with the same error event the inline
// fatalCode produced: the same message, the same code, the same step.
func TestFailPhaseMatchesFatalCode(t *testing.T) {
	inline, ri := newRecorded(t)
	inline.stepState = func() (string, bool) { return "mount", true }
	inline.failCode(fmt.Sprintf("mounting /boot: %v", errors.New("exit status 32")), progress.FailureMountFailed)

	phased, rp := newRecorded(t)
	phased.stepState = func() (string, bool) { return "mount", true }
	old := term
	term = phased
	t.Cleanup(func() { term = old })
	failPhase(phaseFail(progress.FailureMountFailed, "mounting /boot: %v", errors.New("exit status 32")))

	if a, b := lastFailure(t, ri), lastFailure(t, rp); !reflect.DeepEqual(a, b) {
		t.Errorf("failPhase event = %+v\nfatalCode event = %+v", b, a)
	}
	if !reflect.DeepEqual(ri.calls, rp.calls) {
		t.Errorf("failPhase sequence = %v, fatalCode sequence = %v", rp.calls, ri.calls)
	}
}

// liveDiskOps sets every operation, and registers on the install's own
// cleanup and swap registry, so teardown releases what the phase acquired.
func TestLiveDiskOpsBindsTeardown(t *testing.T) {
	v := reflect.ValueOf(liveDiskOps())
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Errorf("liveDiskOps leaves %s unset", v.Type().Field(i).Name)
		}
	}

	fake, target := manualTeardownFixture(t)
	ops := liveDiskOps()
	ops.addMount(target)
	ops.addMount(target + "/boot")
	ops.setLUKS("fisherman-root")
	if err := teardown(); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	want := []string{"umount -Rl " + target + "/boot", "umount -Rl " + target, "cryptsetup luksClose fisherman-root"}
	if got := fake.only("umount", "cryptsetup"); !reflect.DeepEqual(got, want) {
		t.Errorf("teardown ran %v, want %v", got, want)
	}
}
