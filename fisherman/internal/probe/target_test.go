package probe

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// targetProber is a Prober whose lsblk answers with the machine.json fixture
// and whose /dev holds a node for each named device, plus a by-id symlink.
func targetProber(t *testing.T) *Prober {
	t.Helper()
	raw, err := os.ReadFile("testdata/lsblk/machine.json")
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{canned: map[string]string{"lsblk -J -b -p -o " + lsblkColumns: string(raw)}}
	files := map[string]string{"etc/hostname": "host\n", "dev/disk/by-id/": "", "dev/mapper/": ""}
	for _, dev := range []string{"loop0", "sda", "sda1", "sdb", "sdb1", "sdc", "sdd", "sr0", "zram0", "ram0",
		"mapper/vg-data", "nvme0n1", "nvme1n1", "nvme1n1p3", "mmcblk0", "sde", "sdf", "null"} {
		files["dev/"+dev] = ""
	}
	p := newProber(t, files, host, nil)
	for link, dest := range map[string]string{
		"dev/disk/by-id/ata-Samsung_SSD_870": "../../sda",
		"dev/disk/by-id/usb-live-part1":      "../../sdb1",
		"dev/disk/by-id/dangling":            "../../gone",
	} {
		if err := os.Symlink(dest, filepath.Join(p.Root, link)); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestTargetVerdicts(t *testing.T) {
	p := targetProber(t)
	tests := []struct {
		name    string
		path    string
		device  string
		loop    bool
		reasons []string
	}{
		{"eligible disk", "/dev/nvme0n1", "/dev/nvme0n1", false, nil},
		{"by-id symlink resolves to the disk", "/dev/disk/by-id/ata-Samsung_SSD_870", "/dev/sda", false, nil},
		{"partition", "/dev/sda1", "/dev/sda1", false, []string{ExcludedNotDisk}},
		{"by-id symlink to a partition", "/dev/disk/by-id/usb-live-part1", "/dev/sdb1", false, []string{ExcludedNotDisk}},
		{"regular file", "/etc/hostname", "/etc/hostname", false, []string{ExcludedNotDisk}},
		{"character device", "/dev/null", "/dev/null", false, []string{ExcludedNotDisk}},
		{"optical", "/dev/sr0", "/dev/sr0", false, []string{ExcludedNotDisk}},
		{"zram", "/dev/zram0", "/dev/zram0", false, []string{ExcludedNotDisk}},
		{"ram disk", "/dev/ram0", "/dev/ram0", false, []string{ExcludedNotDisk}},
		{"device mapper", "/dev/mapper/vg-data", "/dev/mapper/vg-data", false, []string{ExcludedNotDisk}},
		{"live usb: boot disk and too small, both reported", "/dev/sdb", "/dev/sdb", false,
			[]string{ExcludedBootDisk, ExcludedTooSmall}},
		{"installed system under luks", "/dev/nvme1n1", "/dev/nvme1n1", false, []string{ExcludedBootDisk}},
		{"read only", "/dev/sdf", "/dev/sdf", false, []string{ExcludedReadOnly}},
		{"too small", "/dev/mmcblk0", "/dev/mmcblk0", false, []string{ExcludedTooSmall}},
		// A whole loop device is how CI and image builds install. It is
		// 4 GiB here and still not "too small"; read-only still counts.
		{"loop device: size does not apply, read-only does", "/dev/loop0", "/dev/loop0", true,
			[]string{ExcludedReadOnly}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.Target(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != tt.path || got.Device != tt.device || got.Loop != tt.loop {
				t.Errorf("Target = %+v, want device %s loop %v", got, tt.device, tt.loop)
			}
			if !reflect.DeepEqual(got.Reasons, tt.reasons) {
				t.Errorf("Reasons = %v, want %v", got.Reasons, tt.reasons)
			}
			if got.Eligible() != (len(tt.reasons) == 0) {
				t.Errorf("Eligible() = %v with reasons %v", got.Eligible(), got.Reasons)
			}
		})
	}
}

// The loop devices the VM and loopback harnesses create (`losetup --find
// --show`, justfile setup-loop, bootcrew-nightly.yml) are writable whole
// devices with nothing mounted: they must stay eligible at any size.
func TestTargetWritableLoopIsEligible(t *testing.T) {
	host := &fakeHost{canned: map[string]string{"lsblk -J -b -p -o " + lsblkColumns: `{"blockdevices":[
		{"name":"/dev/loop7","kname":"/dev/loop7","type":"loop","size":21474836480,"ro":false,"mountpoints":[null]},
		{"name":"/dev/loop8","kname":"/dev/loop8","type":"loop","size":2147483648,"ro":false,"mountpoints":[null],
		 "children":[{"name":"/dev/loop8p1","kname":"/dev/loop8p1","type":"part","size":1,"ro":false,"mountpoints":["/run/initramfs/live"]}]}]}`}}
	p := newProber(t, map[string]string{"dev/loop7": "", "dev/loop8": "", "dev/loop8p1": ""}, host, nil)

	got, err := p.Target("/dev/loop7")
	if err != nil || !got.Eligible() || !got.Loop {
		t.Errorf("Target(/dev/loop7) = %+v, %v; want an eligible loop device", got, err)
	}
	// A loop device the live system runs from is still the boot disk.
	got, err = p.Target("/dev/loop8")
	if err != nil || !reflect.DeepEqual(got.Reasons, []string{ExcludedBootDisk}) {
		t.Errorf("Target(/dev/loop8) = %+v, %v; want boot_disk", got, err)
	}
	// Its partition is not a whole device.
	got, err = p.Target("/dev/loop8p1")
	if err != nil || !reflect.DeepEqual(got.Reasons, []string{ExcludedNotDisk}) {
		t.Errorf("Target(/dev/loop8p1) = %+v, %v; want not_disk", got, err)
	}
}

func TestTargetErrors(t *testing.T) {
	p := targetProber(t)
	if _, err := p.Target("/dev/disk/by-id/dangling"); err == nil {
		t.Error("a dangling symlink resolved")
	}
	if _, err := p.Target("/dev/absent"); err == nil {
		t.Error("a missing path resolved")
	}
	p.Run = func(string, ...string) ([]byte, error) { return nil, errors.New("lsblk: not found") }
	if got, err := p.Target("/dev/sda"); err == nil {
		t.Errorf("Target with a failing lsblk = %+v, want an error", got)
	}
}

func TestUEFI(t *testing.T) {
	if p := newProber(t, map[string]string{"sys/firmware/efi/": ""}, &fakeHost{}, nil); !p.UEFI() {
		t.Error("UEFI() = false with /sys/firmware/efi")
	}
	if p := newProber(t, map[string]string{"sys/firmware/": ""}, &fakeHost{}, nil); p.UEFI() {
		t.Error("UEFI() = true without /sys/firmware/efi")
	}
}

// Target reports the identity a frontend can bind an install to: lsblk's
// SERIAL, WWN and MODEL (trimmed) and the by-id links of the device. A disk
// that reports none has empty fields, not guessed ones.
func TestTargetIdentity(t *testing.T) {
	p := targetProber(t)
	got, err := p.Target("/dev/disk/by-id/ata-Samsung_SSD_870")
	if err != nil {
		t.Fatal(err)
	}
	want := Target{Path: "/dev/disk/by-id/ata-Samsung_SSD_870", Device: "/dev/sda", SizeBytes: 500107862016,
		Serial: "S5Y1NJ0R123456", WWN: "0x5002538F4231ABCD", Model: "Samsung SSD 870 EVO 500GB",
		ByID: []string{"/dev/disk/by-id/ata-Samsung_SSD_870"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Target = %+v\nwant     %+v", got, want)
	}

	got, err = p.Target("/dev/nvme0n1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Serial != "" || got.WWN != "" || got.ByID != nil || got.Model != "WD_BLACK SN850X 1000GB" {
		t.Errorf("a disk with no serial, WWN or link: %+v", got)
	}

	// An unreadable /dev/disk/by-id is no links, not an error.
	if err := os.RemoveAll(filepath.Join(p.Root, "dev", "disk", "by-id")); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Target("/dev/sda"); err != nil || got.ByID != nil || got.Serial == "" {
		t.Errorf("without by-id: %+v, %v", got, err)
	}
}
