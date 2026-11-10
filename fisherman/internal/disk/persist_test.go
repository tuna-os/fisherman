package disk_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/runner"
)

// TestPrepareVarDisk_ReleasesBeforeFormat: the /var disk is unmounted (and
// its swap turned off) before wipefs and mkfs.xfs touch it, and only its own
// devices are touched (#406). Before, mkfs.xfs ran on a mounted disk and
// failed after the system disk was already wiped.
func TestPrepareVarDisk_ReleasesBeforeFormat(t *testing.T) {
	rec := setupRecorder(t)
	writeMounts(t, strings.Join([]string{
		"/dev/sdb1 /run/media/user/old-data ext4 rw 0 0",
		"/dev/sda2 / xfs rw 0 0",
		"",
	}, "\n"))

	if err := disk.PrepareVarDisk("/dev/sdb"); err != nil {
		t.Fatalf("PrepareVarDisk: %v", err)
	}

	index := func(prefix string) int {
		for i, c := range rec.calls {
			if strings.HasPrefix(c.name+" "+strings.Join(c.args, " "), prefix) {
				return i
			}
		}
		return -1
	}
	unmount := index("udisksctl unmount --no-user-interaction --block-device /dev/sdb1")
	wipe := index("wipefs -a /dev/sdb")
	mkfs := index("mkfs.xfs -f -L var /dev/sdb")
	if unmount < 0 || wipe < 0 || mkfs < 0 {
		t.Fatalf("want unmount, wipefs and mkfs.xfs on the var disk, got %+v", rec.calls)
	}
	if unmount >= wipe || wipe >= mkfs {
		t.Errorf("order: unmount=%d wipefs=%d mkfs=%d, want unmount < wipefs < mkfs", unmount, wipe, mkfs)
	}
	assertNeverTouches(t, rec, "/dev/sda", "/dev/sda2", "/")
}

// A disk whose block devices cannot be established is not formatted.
func TestPrepareVarDisk_FailsClosed(t *testing.T) {
	rec := setupRecorder(t)
	rec.host.lsblkErr = errors.New("lsblk: not found")
	if err := disk.PrepareVarDisk("/dev/sdb"); err == nil {
		t.Fatal("PrepareVarDisk must fail when lsblk cannot describe the disk")
	}
	for _, c := range rec.calls {
		if c.name == "mkfs.xfs" || c.name == "wipefs" {
			t.Errorf("%s %v ran on a disk that could not be released", c.name, c.args)
		}
	}
}

// A wipefs failure stops before mkfs.
func TestPrepareVarDisk_WipeFailure(t *testing.T) {
	rec := setupRecorder(t)
	writeMounts(t, "")
	rec.fail = func(c execCall) error {
		if c.name == "wipefs" {
			return errors.New("wipefs: device busy")
		}
		return nil
	}
	if err := disk.PrepareVarDisk("/dev/sdb"); err == nil || !strings.Contains(err.Error(), "wipefs") {
		t.Fatalf("PrepareVarDisk = %v, want the wipefs error", err)
	}
	for _, c := range rec.calls {
		if c.name == "mkfs.xfs" {
			t.Error("mkfs.xfs ran after wipefs failed")
		}
	}
}

func TestFSType(t *testing.T) {
	orig := runner.OutputFn
	t.Cleanup(func() { runner.OutputFn = orig })

	var asked [][]string
	answers := map[string]string{}
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		asked = append(asked, append([]string{name}, args...))
		key := strings.Join(args, " ")
		if out, ok := answers[key]; ok {
			return []byte(out), nil
		}
		return nil, errors.New("exit status 2")
	}

	answers["-p -s TYPE -o value /dev/sdb"] = "ext4\n"
	if got := disk.FSType("/dev/sdb"); got != "ext4" {
		t.Errorf("FSType = %q, want ext4 from the low-level probe", got)
	}
	if len(asked) != 1 {
		t.Errorf("the cache must not be asked when the probe answers, got %v", asked)
	}

	// The probe fails (an old blkid, an ambivalent result): the cached
	// answer is used.
	delete(answers, "-p -s TYPE -o value /dev/sdb")
	answers["-s TYPE -o value /dev/sdb"] = "btrfs\n"
	if got := disk.FSType("/dev/sdb"); got != "btrfs" {
		t.Errorf("FSType = %q, want btrfs from the fallback", got)
	}

	if got := disk.FSType("/dev/empty"); got != "" {
		t.Errorf("FSType of a device with no filesystem = %q, want empty", got)
	}
}

func TestCheckKeptFilesystem(t *testing.T) {
	cases := []struct {
		fstype string
		posix  bool
		ok     bool
	}{
		{"xfs", true, true},
		{"ext4", true, true},
		{"btrfs", true, true},
		{"vfat", false, true},
		{"vfat", true, false}, // cannot hold /var
		{"ntfs", true, false},
		{"", false, false}, // nothing to mount
		{"crypto_LUKS", false, false},
		{"LVM2_member", false, false},
		{"swap", false, false},
	}
	for _, c := range cases {
		err := disk.CheckKeptFilesystem("/dev/sdb", c.fstype, c.posix)
		if (err == nil) != c.ok {
			t.Errorf("CheckKeptFilesystem(%q, posix=%v) = %v, want ok=%v", c.fstype, c.posix, err, c.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "/dev/sdb") {
			t.Errorf("the error must name the device: %v", err)
		}
	}
}
