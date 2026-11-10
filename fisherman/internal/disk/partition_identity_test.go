package disk_test

// Tests for tuna-os/fisherman#357: the teardown before partitioning acts on
// the selected disk's real descendants (from lsblk), never on devices whose
// names merely start with the same characters.
//
// Nothing here touches a block device. lsblk, pvs and dmsetup are answered by
// fakeHost through runner.OutputFn; every mutating command lands in the
// shared recorder.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/runner"
)

// fakeHost answers the read-only host queries the partition code makes
// through runner.Output.
type fakeHost struct {
	lsblk     string // JSON for `lsblk -J -o NAME,PKNAME,MAJ:MIN,TYPE`
	lsblkErr  error
	pvs       string // `pvs --separator | -o pv_name,vg_name,pv_major,pv_minor`
	pvsErr    error
	dmsetupLs string
	queries   []execCall

	// Partition-table state for #365; see partition_verify_test.go.
	rereadErr    error                       // result of `blockdev --rereadpt`
	tableErr     error                       // result of `sfdisk --json`
	onDisk       []fakePart                  // the table sfdisk last wrote
	sysDir       string                      // fake /sys/class/block
	mutateTable  func([]fakePart) []fakePart // what `sfdisk --json` reports
	mutateKernel func([]fakePart) []fakePart // what the kernel ends up with
}

// defaultLsblk is an ordinary machine: the disks the older tests in this
// package partition, each with nothing stacked on it.
const defaultLsblk = `{"blockdevices":[
 {"name":"sda","pkname":null,"maj:min":"8:0","type":"disk","children":[
   {"name":"sda1","pkname":"sda","maj:min":"8:1","type":"part"},
   {"name":"sda2","pkname":"sda","maj:min":"8:2","type":"part"}]},
 {"name":"sdb","pkname":null,"maj:min":"8:16","type":"disk","children":[
   {"name":"sdb1","pkname":"sdb","maj:min":"8:17","type":"part"}]},
 {"name":"vda","pkname":null,"maj:min":"252:0","type":"disk"},
 {"name":"nbd0","pkname":null,"maj:min":"43:0","type":"disk"},
 {"name":"nvme0n1","pkname":null,"maj:min":"259:0","type":"disk","children":[
   {"name":"nvme0n1p1","pkname":"nvme0n1","maj:min":"259:1","type":"part"}]}
]}`

// newFakeHost installs a fakeHost as runner.OutputFn for the test.
func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	h := &fakeHost{lsblk: defaultLsblk, pvsErr: errors.New("pvs: command not found"), sysDir: t.TempDir()}
	orig := runner.OutputFn
	runner.OutputFn = h.output
	disk.SetSysBlockPath(h.sysDir)
	t.Cleanup(func() {
		runner.OutputFn = orig
		disk.SetSysBlockPath("/sys/class/block")
	})
	return h
}

func (h *fakeHost) output(name string, args ...string) ([]byte, error) {
	h.queries = append(h.queries, execCall{name: name, args: args})
	switch {
	case name == "lsblk":
		return []byte(h.lsblk), h.lsblkErr
	case name == "pvs":
		return []byte(h.pvs), h.pvsErr
	case name == "dmsetup" && len(args) > 0 && args[0] == "ls":
		return []byte(h.dmsetupLs), nil
	case name == "blockdev" && len(args) == 2 && args[0] == "--rereadpt":
		return nil, h.rereadErr
	case name == "sfdisk" && len(args) == 2 && args[0] == "--json":
		return h.tableJSON(args[1])
	}
	return nil, fmt.Errorf("fakeHost: unexpected query %s %v", name, args)
}

func writeMounts(t *testing.T, content string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "mounts")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	disk.SetProcMountsPath(f.Name())
	t.Cleanup(func() { disk.SetProcMountsPath("/proc/mounts") })
}

// mutating are the commands that change host state during teardown.
var mutating = map[string]bool{
	"udisksctl": true, "umount": true, "swapoff": true, "vgchange": true,
	"dmsetup": true, "fuser": true, "blockdev": true, "sfdisk": true,
}

// assertNeverTouches fails if any recorded command names one of the
// forbidden devices or mount points in its arguments.
func assertNeverTouches(t *testing.T, rec *recorder, forbidden ...string) {
	t.Helper()
	for _, c := range rec.calls {
		for _, a := range c.args {
			for _, f := range forbidden {
				if a == f {
					t.Errorf("%s %v touched %s, which is not on the selected disk", c.name, c.args, f)
				}
			}
		}
	}
}

func assertNoMutation(t *testing.T, rec *recorder) {
	t.Helper()
	for _, c := range rec.calls {
		if mutating[c.name] {
			t.Errorf("%s %v ran although the disk's block devices could not be established", c.name, c.args)
		}
	}
}

// TestUnmountAll_PrefixCollisions selects a disk whose kernel name is a
// prefix of another disk's name. Before #357 the unrelated disk's mounts,
// swap, volume group and LUKS mapping were torn down too.
func TestUnmountAll_PrefixCollisions(t *testing.T) {
	tests := []struct {
		selected, other string // kernel names
		sep             string // partition separator
		major           int
	}{
		{"nvme0n1", "nvme0n10", "p", 259},
		{"mmcblk1", "mmcblk10", "p", 179},
		{"loop1", "loop10", "p", 7},
	}
	for _, tt := range tests {
		t.Run(tt.selected+"_vs_"+tt.other, func(t *testing.T) {
			selP1 := tt.selected + tt.sep + "1"
			selP2 := tt.selected + tt.sep + "2"
			othP1 := tt.other + tt.sep + "1"
			othP2 := tt.other + tt.sep + "2"
			othP3 := tt.other + tt.sep + "3"

			rec := setupRecorder(t)
			rec.host.lsblk = strings.NewReplacer(
				"SELP1", selP1, "SELP2", selP2, "SEL", tt.selected,
				"OTHP1", othP1, "OTHP2", othP2, "OTHP3", othP3, "OTH", tt.other,
				"MAJ", fmt.Sprint(tt.major),
			).Replace(`{"blockdevices":[
 {"name":"SEL","pkname":null,"maj:min":"MAJ:0","type":"disk","children":[
   {"name":"SELP1","pkname":"SEL","maj:min":"MAJ:1","type":"part"},
   {"name":"SELP2","pkname":"SEL","maj:min":"MAJ:2","type":"part","children":[
     {"name":"luks-mine","pkname":"SELP2","maj:min":"253:0","type":"crypt"}]}]},
 {"name":"OTH","pkname":null,"maj:min":"MAJ:100","type":"disk","children":[
   {"name":"OTHP1","pkname":"OTH","maj:min":"MAJ:101","type":"part"},
   {"name":"OTHP2","pkname":"OTH","maj:min":"MAJ:102","type":"part","children":[
     {"name":"luks-theirs","pkname":"OTHP2","maj:min":"253:1","type":"crypt"}]},
   {"name":"OTHP3","pkname":"OTH","maj:min":"MAJ:103","type":"part","children":[
     {"name":"othervg-data","pkname":"OTHP3","maj:min":"253:2","type":"lvm"}]}]}
]}`)
			rec.host.pvs, rec.host.pvsErr = fmt.Sprintf("  /dev/%s|othervg|%d|103\n", othP3, tt.major), nil
			rec.host.dmsetupLs = "luks-mine\t(253:0)\nluks-theirs\t(253:1)\nothervg-data\t(253:2)\n"
			writeMounts(t, strings.Join([]string{
				"/dev/" + selP1 + " /run/media/u/MINE vfat rw 0 0",
				"/dev/mapper/luks-mine /mnt/mine ext4 rw 0 0",
				"/dev/" + othP1 + " /run/media/u/THEIRS vfat rw 0 0",
				"/dev/" + othP2 + " none swap sw 0 0",
				"/dev/mapper/luks-theirs /mnt/theirs ext4 rw 0 0",
				"/dev/mapper/othervg-data /srv ext4 rw 0 0",
				"/dev/" + tt.other + " /mnt/whole ext4 rw 0 0",
			}, "\n")+"\n")
			// Loop devices are re-attached through losetup after sfdisk; keep
			// that off the real host. The call then fails, after the teardown
			// under test has run.
			fakeBinOnPATH(t, "losetup", "#!/bin/sh\nexit 1\n")
			_ = disk.Partition("/dev/" + tt.selected)

			assertNeverTouches(t, rec,
				"/dev/"+tt.other, "/dev/"+othP1, "/dev/"+othP2, "/dev/"+othP3,
				"/dev/mapper/luks-theirs", "/dev/mapper/othervg-data",
				"/run/media/u/THEIRS", "/mnt/theirs", "/srv", "/mnt/whole",
				"luks-theirs", "othervg-data", "othervg")

			// The selected disk's own mount and mapping are still released.
			if !anyCall(rec.calls, "udisksctl", "/dev/"+selP1) {
				t.Errorf("mount on /dev/%s was not released", selP1)
			}
			if !anyCall(rec.calls, "udisksctl", "/dev/mapper/luks-mine") {
				t.Error("filesystem on the selected disk's LUKS mapping was not unmounted")
			}
			if !anyCall(rec.calls, "dmsetup", "luks-mine") {
				t.Error("LUKS mapping on the selected disk was not removed")
			}
			if n := len(findCalls(rec, "vgchange")); n != 0 {
				t.Errorf("vgchange ran %d time(s); the only volume group is on the other disk", n)
			}
		})
	}
}

// TestUnmountAll_WholeDiskAndPartitionMounts covers a filesystem directly on
// the whole disk next to one on a partition.
func TestUnmountAll_WholeDiskAndPartitionMounts(t *testing.T) {
	rec := setupRecorder(t)
	writeMounts(t, "/dev/sda /mnt/whole ext4 rw 0 0\n/dev/sda2 /mnt/part ext4 rw 0 0\n/dev/sda2x /mnt/no ext4 rw 0 0\n")
	if err := disk.Partition("/dev/sda"); err != nil {
		t.Fatalf("Partition: %v", err)
	}
	for _, dev := range []string{"/dev/sda", "/dev/sda2"} {
		if !anyCall(findCalls(rec, "udisksctl"), "udisksctl", dev) {
			t.Errorf("mount on %s was not released", dev)
		}
	}
	assertNeverTouches(t, rec, "/dev/sda2x", "/mnt/no")
}

// TestUnmountAll_ByIDAlias selects the disk through a symlink, as a frontend
// that lists /dev/disk/by-id does.
func TestUnmountAll_ByIDAlias(t *testing.T) {
	// The link target does not have to exist: only its name is resolved.
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/sda", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := dir + "/ata-EXAMPLE_DISK"
	if err := os.Symlink(dir+"/sda", alias); err != nil {
		t.Fatal(err)
	}
	rec := setupRecorder(t)
	writeMounts(t, "/dev/sda1 /run/media/u/A vfat rw 0 0\n/dev/sdb1 /run/media/u/B vfat rw 0 0\n")

	if err := disk.Partition(alias); err != nil {
		t.Fatalf("Partition: %v", err)
	}
	if !anyCall(rec.calls, "udisksctl", "/dev/sda1") {
		t.Error("mount on the aliased disk's partition was not released")
	}
	assertNeverTouches(t, rec, "/dev/sdb1", "/run/media/u/B")
}

// TestUnmountAll_VolumeGroupOnSelectedDisk: a group that lives only on the
// selected disk is deactivated, and its logical volume is unmounted.
func TestUnmountAll_VolumeGroupOnSelectedDisk(t *testing.T) {
	rec := setupRecorder(t)
	rec.host.lsblk = `{"blockdevices":[
 {"name":"sda","pkname":null,"maj:min":"8:0","type":"disk","children":[
   {"name":"sda1","pkname":"sda","maj:min":"8:1","type":"part","children":[
     {"name":"vg0-root","pkname":"sda1","maj:min":"253:0","type":"lvm"}]}]},
 {"name":"sdb","pkname":null,"maj:min":"8:16","type":"disk","children":[
   {"name":"sdb1","pkname":"sdb","maj:min":"8:17","type":"part","children":[
     {"name":"vg1-data","pkname":"sdb1","maj:min":"253:1","type":"lvm"}]}]}
]}`
	rec.host.pvs, rec.host.pvsErr = "  /dev/sda1|vg0|8|1\n  /dev/sdb1|vg1|8|17\n  /dev/sda2||8|2\n", nil
	rec.host.dmsetupLs = "vg0-root\t(253:0)\nvg1-data\t(253, 1)\n"
	writeMounts(t, "/dev/mapper/vg0-root /mnt/old ext4 rw 0 0\n/dev/mapper/vg1-data /srv ext4 rw 0 0\n")

	if err := disk.Partition("/dev/sda"); err != nil {
		t.Fatalf("Partition: %v", err)
	}
	vg := findCalls(rec, "vgchange")
	if len(vg) != 1 || !equalSlice(vg[0].args, []string{"-an", "vg0"}) {
		t.Errorf("vgchange calls = %+v, want exactly [-an vg0]", vg)
	}
	if !anyCall(rec.calls, "udisksctl", "/dev/mapper/vg0-root") {
		t.Error("logical volume on the selected disk was not unmounted")
	}
	assertNeverTouches(t, rec, "vg1", "vg1-data", "/dev/mapper/vg1-data", "/srv")
}

// TestUnmountAll_RefusesSharedVolumeGroup: a group with a physical volume on
// another disk must stop the install before any teardown command runs.
func TestUnmountAll_RefusesSharedVolumeGroup(t *testing.T) {
	rec := setupRecorder(t)
	rec.host.pvs, rec.host.pvsErr = "  /dev/sda2|bigvg|8|2\n  /dev/sdb1|bigvg|8|17\n", nil
	writeMounts(t, "/dev/sda1 /run/media/u/A vfat rw 0 0\n")

	err := disk.Partition("/dev/sda")
	if err == nil {
		t.Fatal("Partition succeeded on a disk whose volume group spans another disk")
	}
	for _, want := range []string{"bigvg", "/dev/sdb1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	assertNoMutation(t, rec)
}

// TestUnmountAll_RefusesSharedHolder: a logical volume (or md array) that
// sits on the selected disk and on another one is shared storage.
func TestUnmountAll_RefusesSharedHolder(t *testing.T) {
	rec := setupRecorder(t)
	rec.host.lsblk = `{"blockdevices":[
 {"name":"sda","pkname":null,"maj:min":"8:0","type":"disk","children":[
   {"name":"sda1","pkname":"sda","maj:min":"8:1","type":"part","children":[
     {"name":"md0","pkname":"sda1","maj:min":"9:0","type":"raid1"}]}]},
 {"name":"sdb","pkname":null,"maj:min":"8:16","type":"disk","children":[
   {"name":"sdb1","pkname":"sdb","maj:min":"8:17","type":"part","children":[
     {"name":"md0","pkname":"sdb1","maj:min":"9:0","type":"raid1"}]}]}
]}`
	writeMounts(t, "/dev/md0 /srv ext4 rw 0 0\n")

	err := disk.Partition("/dev/sda")
	if err == nil || !strings.Contains(err.Error(), "md0") {
		t.Fatalf("Partition error = %v, want a refusal that names md0", err)
	}
	assertNoMutation(t, rec)
}

// TestUnmountAll_FailsClosed: when the block-device tree cannot be
// established, nothing is unmounted, deactivated, killed or partitioned.
func TestUnmountAll_FailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *fakeHost)
		disk  string
	}{
		{"lsblk fails", func(h *fakeHost) { h.lsblkErr = errors.New("lsblk: exit status 32") }, "/dev/sda"},
		{"lsblk output is not JSON", func(h *fakeHost) { h.lsblk = "NAME PKNAME\nsda\n" }, "/dev/sda"},
		{"disk is not listed", func(h *fakeHost) {}, "/dev/sdz"},
		{"only a longer name is listed", func(h *fakeHost) {
			h.lsblk = `{"blockdevices":[{"name":"nvme0n10","pkname":null,"maj:min":"259:9","type":"disk"}]}`
		}, "/dev/nvme0n1"},
		{"device without major:minor", func(h *fakeHost) {
			h.lsblk = `{"blockdevices":[{"name":"sda","pkname":null,"maj:min":"8:0","type":"disk","children":[{"name":"sda1","pkname":"sda","type":"part"}]}]}`
		}, "/dev/sda"},
		{"pvs fails while a logical volume is on the disk", func(h *fakeHost) {
			h.lsblk = `{"blockdevices":[{"name":"sda","pkname":null,"maj:min":"8:0","type":"disk","children":[{"name":"vg0-root","pkname":"sda","maj:min":"253:0","type":"lvm"}]}]}`
			h.pvsErr = errors.New("pvs: exit status 5")
		}, "/dev/sda"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := setupRecorder(t)
			tt.setup(rec.host)
			writeMounts(t, "/dev/sda1 /run/media/u/A vfat rw 0 0\n/dev/nvme0n10p1 /data ext4 rw 0 0\n")
			if err := disk.Partition(tt.disk); err == nil {
				t.Fatal("Partition succeeded although the block-device tree could not be established")
			}
			assertNoMutation(t, rec)
		})
	}
}

// TestUnmountAll_QueriesGoThroughRunner pins the lsblk argv. runner.Output
// applies the flatpak-spawn --host wrapping, so the tree is always the
// host's, not the sandbox's.
func TestUnmountAll_QueriesGoThroughRunner(t *testing.T) {
	rec := setupRecorder(t)
	emptyMountsFile(t)
	if err := disk.Partition("/dev/sda"); err != nil {
		t.Fatalf("Partition: %v", err)
	}
	if len(rec.host.queries) == 0 || rec.host.queries[0].name != "lsblk" ||
		!equalSlice(rec.host.queries[0].args, []string{"-J", "-o", "NAME,PKNAME,MAJ:MIN,TYPE"}) {
		t.Errorf("first host query = %+v, want lsblk -J -o NAME,PKNAME,MAJ:MIN,TYPE", rec.host.queries)
	}
}
