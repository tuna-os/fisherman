package disk_test

// Tests for tuna-os/fisherman#365: sfdisk is retried with --force only for
// the known in-use state, a failed kernel re-read stops the install, and the
// kernel's start and size of every partition must match the table on disk
// before Partition returns (the caller formats right after).
//
// The fake keeps two views apart, as a real machine does: the table on the
// disk (what `sfdisk --json` prints) and the kernel's partitions (sysfs).
// sfdisk without --no-reread and partprobe copy the first to the second.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
)

var errBusy = errors.New("blockdev --rereadpt /dev/sda: exit status 1: blockdev: ioctl error on BLKRRPART: Device or resource busy")

type fakePart struct {
	Node  string `json:"node"`
	Start uint64 `json:"start"`
	Size  uint64 `json:"size"`
	Type  string `json:"type"`
	Name  string `json:"name"`
}

const fakeDiskSectors = 64 << 21 // 64 GiB in 512-byte sectors

// tableFromScript lays the script out the way sfdisk does on an empty disk:
// 1 MiB alignment, the last partition takes the rest.
func tableFromScript(diskPath, script string) []fakePart {
	var parts []fakePart
	next := uint64(2048)
	for _, line := range strings.Split(script, "\n") {
		if !strings.Contains(line, "=") {
			continue
		}
		p := fakePart{Node: disk.PartName(diskPath, len(parts)+1), Start: next, Type: "0FC63DAF-8483-4772-8E79-3D69D8477DE4"}
		p.Size = fakeDiskSectors - 2048 - next
		for _, f := range strings.Split(line, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
			switch k {
			case "size":
				gib, err := strconv.ParseUint(strings.TrimSuffix(v, "GiB"), 10, 64)
				if err != nil {
					panic("fake sfdisk: size " + v)
				}
				p.Size = gib << 21
			case "name":
				p.Name = strings.Trim(v, `"`)
			}
		}
		next = p.Start + p.Size
		parts = append(parts, p)
	}
	return parts
}

// observe is called by the recorders after every mutating command.
func (h *fakeHost) observe(c execCall, err error) {
	if err != nil || len(c.args) == 0 {
		return
	}
	target := c.args[len(c.args)-1]
	switch {
	case c.name == "sfdisk" && c.stdin != "":
		h.onDisk = tableFromScript(target, c.stdin)
		if !containsArg(c.args, "--no-reread") {
			h.kernelReread(target)
		}
	case c.name == "partprobe":
		h.kernelReread(target)
	}
}

// kernelReread replaces the kernel's partitions of diskPath with the table
// on disk.
func (h *fakeHost) kernelReread(diskPath string) {
	h.setKernel(diskPath, h.onDisk)
}

func (h *fakeHost) setKernel(diskPath string, parts []fakePart) {
	if h.mutateKernel != nil {
		parts = h.mutateKernel(append([]fakePart(nil), parts...))
	}
	if real, err := filepath.EvalSymlinks(diskPath); err == nil {
		diskPath = real
	}
	dir := filepath.Join(h.sysDir, filepath.Base(diskPath))
	if err := os.RemoveAll(dir); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	for i, p := range parts {
		pd := filepath.Join(dir, filepath.Base(disk.PartName(diskPath, i+1)))
		if err := os.MkdirAll(pd, 0o755); err != nil {
			panic(err)
		}
		for attr, v := range map[string]uint64{"start": p.Start, "size": p.Size} {
			if err := os.WriteFile(filepath.Join(pd, attr), []byte(fmt.Sprintf("%d\n", v)), 0o644); err != nil {
				panic(err)
			}
		}
	}
}

func (h *fakeHost) tableJSON(diskPath string) ([]byte, error) {
	if h.tableErr != nil {
		return nil, h.tableErr
	}
	parts := h.onDisk
	if h.mutateTable != nil {
		parts = h.mutateTable(append([]fakePart(nil), parts...))
	}
	return json.Marshal(map[string]any{"partitiontable": map[string]any{
		"label": "gpt", "device": diskPath, "unit": "sectors", "sectorsize": 512, "partitions": parts,
	}})
}

// failFirst returns a recorder hook that fails the first call of command name.
func failFirst(name string, err error) func(execCall) error {
	n := 0
	return func(c execCall) error {
		if c.name != name {
			return nil
		}
		n++
		if n == 1 {
			return err
		}
		return nil
	}
}

func failAlways(name string, err error) func(execCall) error {
	return func(c execCall) error {
		if c.name == name {
			return err
		}
		return nil
	}
}

// assertNoFormat checks that nothing was formatted, encrypted or pooled.
func assertNoFormat(t *testing.T, rec *recorder) {
	t.Helper()
	for _, c := range rec.calls {
		if strings.HasPrefix(c.name, "mkfs") || c.name == "mkswap" || c.name == "cryptsetup" || c.name == "zpool" {
			t.Errorf("%s %v ran after a failed partition step", c.name, c.args)
		}
	}
}

// partitionThenFormat mirrors the caller in cmd/fisherman: format only when
// Partition returned nil.
func partitionThenFormat(d string) error {
	if err := disk.Partition(d); err != nil {
		return err
	}
	return disk.FormatEFI(disk.PartName(d, 1))
}

func TestPartition_SfdiskRetryPolicy(t *testing.T) {
	sfdiskErr := errors.New("sfdisk --wipe=always /dev/sda: exit status 1")
	tests := []struct {
		name       string
		rereadErr  error
		fail       func(execCall) error
		wantErr    string // "" = success
		wantSfdisk int
		wantForce  bool
	}{
		{"busy disk: forced retry, then re-read", errBusy, failFirst("sfdisk", sfdiskErr), "", 2, true},
		{"disk no longer busy: plain retry", nil, failFirst("sfdisk", sfdiskErr), "", 2, false},
		{"not busy and sfdisk fails again: no force", nil, failAlways("sfdisk", sfdiskErr), "not retried with --force", 2, false},
		{"in-use check fails for another reason: no retry",
			errors.New("blockdev: cannot open /dev/sda: No such device or address"),
			failFirst("sfdisk", sfdiskErr), "not retried with --force", 1, false},
		{"busy disk and forced retry fails", errBusy, failAlways("sfdisk", sfdiskErr), "sfdisk", 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emptyMountsFile(t)
			rec := setupRecorder(t)
			rec.host.rereadErr = tt.rereadErr
			rec.fail = tt.fail

			err := partitionThenFormat("/dev/sda")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Partition: %v", err)
				}
				if n := len(findCalls(rec, "mkfs.fat")); n != 1 {
					t.Errorf("mkfs.fat ran %d time(s) after a verified layout, want 1", n)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				assertNoFormat(t, rec)
			}
			sf := findCalls(rec, "sfdisk")
			if len(sf) != tt.wantSfdisk {
				t.Fatalf("sfdisk ran %d time(s), want %d", len(sf), tt.wantSfdisk)
			}
			forced := anyCall(sf, "sfdisk", "--force")
			if forced != tt.wantForce {
				t.Errorf("--force used = %v, want %v (calls %+v)", forced, tt.wantForce, sf)
			}
			if forced && !anyCall(sf, "sfdisk", "--no-reread") {
				t.Error("--force must come with --no-reread, as before")
			}
		})
	}
}

// TestPartition_FailedPartprobeStopsBeforeFormat: after --force --no-reread
// the kernel still has the old table. If partprobe cannot replace it, the
// caller must never reach mkfs.
func TestPartition_FailedPartprobeStopsBeforeFormat(t *testing.T) {
	emptyMountsFile(t)
	rec := setupRecorder(t)
	rec.host.rereadErr = errBusy
	// The kernel still has the previous, smaller layout.
	rec.host.setKernel("/dev/sda", []fakePart{{Start: 2048, Size: 1 << 20}, {Start: 2048 + 1<<20, Size: 1 << 20}})
	sfdiskN := 0
	rec.fail = func(c execCall) error {
		switch c.name {
		case "sfdisk":
			sfdiskN++
			if sfdiskN == 1 {
				return errors.New("sfdisk: exit status 1")
			}
		case "partprobe":
			return errors.New("partprobe: Error informing the kernel about modifications to partition /dev/sda1 -- Device or resource busy")
		}
		return nil
	}

	err := partitionThenFormat("/dev/sda")
	if err == nil {
		t.Fatal("Partition succeeded although partprobe failed")
	}
	if !strings.Contains(err.Error(), "could not re-read") {
		t.Errorf("error %q does not tell the user that the kernel could not re-read the table", err)
	}
	assertNoFormat(t, rec)
	if last := rec.calls[len(rec.calls)-1]; last.name != "partprobe" {
		t.Errorf("%s %v ran after the failed partprobe", last.name, last.args)
	}
}

// TestPartition_FailedSettleStopsBeforeFormat: a udev settle that fails after
// the table is written is no longer ignored.
func TestPartition_FailedSettleStopsBeforeFormat(t *testing.T) {
	emptyMountsFile(t)
	rec := setupRecorder(t)
	sawSfdisk := false
	rec.fail = func(c execCall) error {
		if c.name == "sfdisk" {
			sawSfdisk = true
		}
		if c.name == "udevadm" && sawSfdisk {
			return errors.New("udevadm settle: exit status 1")
		}
		return nil
	}
	err := partitionThenFormat("/dev/sda")
	if err == nil || !strings.Contains(err.Error(), "udev") {
		t.Fatalf("error = %v, want a udev settle failure", err)
	}
	assertNoFormat(t, rec)
}

// TestPartition_LayoutMismatchStopsBeforeFormat: every refresh command
// reports success, but what the kernel has, or what is on disk, is not the
// plan.
func TestPartition_LayoutMismatchStopsBeforeFormat(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(h *fakeHost)
		busy    bool
		wantErr string
	}{
		{"kernel kept an old partition size", func(h *fakeHost) {
			h.mutateKernel = func(p []fakePart) []fakePart { p[2].Size = 1 << 20; return p }
		}, false, "old layout for sda3"},
		{"kernel kept an old partition start", func(h *fakeHost) {
			h.mutateKernel = func(p []fakePart) []fakePart { p[1].Start += 2048; return p }
		}, false, "old layout for sda2"},
		{"kernel has no node for the last partition", func(h *fakeHost) {
			h.mutateKernel = func(p []fakePart) []fakePart { return p[:2] }
		}, false, "no partition sda3"},
		{"kernel still has a partition from the old table", func(h *fakeHost) {
			h.mutateKernel = func(p []fakePart) []fakePart { return append(p, fakePart{Start: 1, Size: 1}) }
		}, false, "sda4 from the previous table"},
		{"forced write, partprobe reports success, kernel unchanged", func(h *fakeHost) {
			h.mutateKernel = func(p []fakePart) []fakePart {
				return []fakePart{{Start: 2048, Size: 1 << 20}, {Start: 2048 + 1<<20, Size: 1 << 20}, {Start: 2048 + 2<<20, Size: 1 << 20}}
			}
		}, true, "old layout for sda1"},
		{"table on disk has fewer partitions than the plan", func(h *fakeHost) {
			h.mutateTable = func(p []fakePart) []fakePart { return p[:2] }
		}, false, "2 partition(s), the plan has 3"},
		{"table on disk has a wrong ESP size", func(h *fakeHost) {
			h.mutateTable = func(p []fakePart) []fakePart { p[0].Size = 1 << 21; return p }
		}, false, "partition 1 on disk is 1073741824 bytes"},
		{"table on disk has a wrong name", func(h *fakeHost) {
			h.mutateTable = func(p []fakePart) []fakePart { p[1].Name = "old-data"; return p }
		}, false, `named "old-data"`},
		{"table on disk overlaps", func(h *fakeHost) {
			h.mutateTable = func(p []fakePart) []fakePart { p[2].Start = p[1].Start; return p }
		}, false, "overlaps"},
		{"table cannot be read back", func(h *fakeHost) {
			h.tableErr = errors.New("sfdisk --json /dev/sda: exit status 1")
		}, false, "reading the partition table back"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emptyMountsFile(t)
			rec := setupRecorder(t)
			tt.setup(rec.host)
			if tt.busy {
				rec.host.rereadErr = errBusy
				rec.fail = failFirst("sfdisk", errors.New("sfdisk: exit status 1"))
			}
			err := partitionThenFormat("/dev/sda")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "nothing was formatted") {
				t.Errorf("error %q does not say that nothing was formatted", err)
			}
			assertNoFormat(t, rec)
		})
	}
}

// TestPartition_VerifiedLayouts: each layout this package writes passes the
// check on a disk whose kernel view is current, on plain and "p"-suffixed
// names.
func TestPartition_VerifiedLayouts(t *testing.T) {
	for name, fn := range map[string]func(string) error{
		"grub":         disk.Partition,
		"systemd-boot": disk.PartitionSystemdBoot,
		"zfs":          disk.PartitionZFS,
	} {
		for _, d := range []string{"/dev/sda", "/dev/nvme0n1", "/dev/nbd0"} {
			t.Run(name+" "+d, func(t *testing.T) {
				emptyMountsFile(t)
				rec := setupRecorder(t)
				if err := fn(d); err != nil {
					t.Fatalf("partition: %v", err)
				}
				var sawJSON bool
				for _, q := range rec.host.queries {
					if q.name == "sfdisk" && equalSlice(q.args, []string{"--json", d}) {
						sawJSON = true
					}
				}
				if !sawJSON {
					t.Error("the table was not read back with sfdisk --json")
				}
			})
		}
	}
}
