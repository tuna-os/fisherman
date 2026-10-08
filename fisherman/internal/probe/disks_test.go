package probe

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func loadDisks(t *testing.T, fixture string) map[string]Disk {
	t.Helper()
	raw, err := os.ReadFile("testdata/lsblk/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	disks, err := ParseLsblk(raw)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Disk{}
	for _, d := range disks {
		byPath[d.Path] = d
	}
	return byPath
}

func TestDisksExclusionRules(t *testing.T) {
	disks := loadDisks(t, "machine.json")
	tests := []struct {
		name      string
		path      string
		eligible  bool
		reason    string
		removable bool
		transport string
		model     string
		sizeLabel string
	}{
		{"nvme", "/dev/nvme0n1", true, "", false, "NVMe", "WD_BLACK SN850X 1000GB", "953.9 GiB"},
		{"sata", "/dev/sda", true, "", false, "SATA", "Samsung SSD 870 EVO 500GB", "465.8 GiB"},
		{"usb removable stick, eligible but flagged", "/dev/sdd", true, "", true, "USB", "Cruzer Blade", "59.6 GiB"},
		{"usb ssd, hotplug only", "/dev/sdc", true, "", true, "USB", "Extreme SSD", "931.5 GiB"},
		{"live usb", "/dev/sdb", false, ExcludedBootDisk, true, "USB", "Ultra Fit", "14.8 GiB"},
		{"installed system under luks", "/dev/nvme1n1", false, ExcludedBootDisk, false, "NVMe", "", "1.8 TiB"},
		{"zram", "/dev/zram0", false, ExcludedZram, false, "", "", "8 GiB"},
		{"ram disk", "/dev/ram0", false, ExcludedRAM, false, "", "", "64 MiB"},
		{"loop", "/dev/loop0", false, ExcludedLoop, false, "", "", "4 GiB"},
		{"optical", "/dev/sr0", false, ExcludedOptical, true, "SATA", "DVD-RW DRW-24F1ST", "2 GiB"},
		{"device mapper", "/dev/mapper/vg-data", false, ExcludedDeviceMapper, false, "", "", "100 GiB"},
		{"too small, missing model", "/dev/mmcblk0", false, ExcludedTooSmall, false, "MMC", "", "29.7 GiB"},
		{"empty card reader", "/dev/sde", false, ExcludedTooSmall, true, "USB", "Card Reader", "0 B"},
		{"read only", "/dev/sdf", false, ExcludedReadOnly, false, "SATA", "WP Disk", "119.2 GiB"},
	}
	if len(disks) != len(tests) {
		t.Errorf("parsed %d disks, table covers %d", len(disks), len(tests))
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := disks[tt.path]
			if !ok {
				t.Fatalf("%s missing from probe output", tt.path)
			}
			if d.Eligible != tt.eligible || d.ExcludedReason != tt.reason {
				t.Errorf("eligible=%v reason=%q, want %v %q", d.Eligible, d.ExcludedReason, tt.eligible, tt.reason)
			}
			if d.Removable != tt.removable {
				t.Errorf("removable=%v, want %v", d.Removable, tt.removable)
			}
			if d.TransportLabel != tt.transport {
				t.Errorf("transport_label=%q, want %q", d.TransportLabel, tt.transport)
			}
			if d.Model != tt.model {
				t.Errorf("model=%q, want %q", d.Model, tt.model)
			}
			if d.SizeLabel != tt.sizeLabel {
				t.Errorf("size_label=%q, want %q", d.SizeLabel, tt.sizeLabel)
			}
		})
	}
}

func TestDisksVendorTrimmed(t *testing.T) {
	if v := loadDisks(t, "machine.json")["/dev/sda"].Vendor; v != "ATA" {
		t.Errorf("vendor=%q, want trimmed ATA", v)
	}
}

func TestDisksKeepLsblkOrder(t *testing.T) {
	raw, _ := os.ReadFile("testdata/lsblk/machine.json")
	disks, err := ParseLsblk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if disks[0].Path != "/dev/loop0" || disks[len(disks)-1].Path != "/dev/sdf" {
		t.Errorf("order not preserved: first %s last %s", disks[0].Path, disks[len(disks)-1].Path)
	}
}

// lsblk older than 2.33 prints every value as a string, and older than 2.37
// has MOUNTPOINT instead of MOUNTPOINTS.
func TestDisksLegacyLsblk(t *testing.T) {
	disks := loadDisks(t, "legacy.json")
	if d := disks["/dev/sda"]; d.Eligible || d.ExcludedReason != ExcludedBootDisk {
		t.Errorf("sda (root on sda2) = %+v, want boot_disk", d)
	}
	d := disks["/dev/sdb"]
	if !d.Eligible || !d.Removable || d.SizeBytes != 256060514304 {
		t.Errorf("sdb = %+v, want eligible removable 256060514304", d)
	}
}

func TestDisksBoundaryAtMinimum(t *testing.T) {
	at := lsblkDevice{Name: "/dev/sda", Type: "disk", Size: flexInt(MinDiskBytes)}
	below := lsblkDevice{Name: "/dev/sdb", Type: "disk", Size: flexInt(MinDiskBytes - 1)}
	if r := excludedReason(at); r != "" {
		t.Errorf("exactly the minimum excluded: %q", r)
	}
	if r := excludedReason(below); r != ExcludedTooSmall {
		t.Errorf("one byte under the minimum: %q", r)
	}
	if MinDiskBytes != 51200*1024*1024 {
		t.Errorf("MinDiskBytes drifted from GNOME's 51200 MiB")
	}
}

func TestDisksRunsLsblkThroughRunner(t *testing.T) {
	raw, _ := os.ReadFile("testdata/lsblk/machine.json")
	var calls [][]string
	p := &Prober{Root: t.TempDir(), Run: func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return raw, nil
	}}
	disks, err := p.Disks()
	if err != nil || len(disks) == 0 {
		t.Fatalf("Disks() = %d, %v", len(disks), err)
	}
	if got := strings.Join(calls[0], " "); got != "lsblk -J -b -p -o "+lsblkColumns {
		t.Errorf("ran %q", got)
	}
}

func TestDisksFallsBackToLegacyColumns(t *testing.T) {
	raw, _ := os.ReadFile("testdata/lsblk/legacy.json")
	p := &Prober{Run: func(name string, args ...string) ([]byte, error) {
		if args[len(args)-1] == lsblkColumns {
			return nil, errors.New("lsblk: unknown column: MOUNTPOINTS")
		}
		return raw, nil
	}}
	disks, err := p.Disks()
	if err != nil || len(disks) != 2 {
		t.Fatalf("Disks() = %v, %v", disks, err)
	}
}

func TestDisksErrors(t *testing.T) {
	p := &Prober{Run: func(string, ...string) ([]byte, error) { return nil, errors.New("no lsblk") }}
	if _, err := p.Disks(); err == nil {
		t.Error("lsblk failure was not an error")
	}
	if _, err := ParseLsblk([]byte("not json")); err == nil {
		t.Error("garbage was not an error")
	}
	if _, err := ParseLsblk([]byte(`{"blockdevices":[{"name":"/dev/sda","size":"huge"}]}`)); err == nil {
		t.Error("non-numeric size was not an error")
	}
}

func TestSizeLabel(t *testing.T) {
	tests := map[int64]string{
		0:             "0 B",
		512:           "512 B",
		1536:          "1.5 KiB",
		MinDiskBytes:  "50 GiB",
		512110190592:  "476.9 GiB", // a "512 GB" SSD
		2000398934016: "1.8 TiB",
		1 << 40:       "1 TiB",
		1<<60 + 1<<59: "1536 PiB",
	}
	for n, want := range tests {
		if got := SizeLabel(n); got != want {
			t.Errorf("SizeLabel(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTransportLabel(t *testing.T) {
	tests := map[string]string{
		"nvme": "NVMe", "sata": "SATA", "ata": "SATA", "usb": "USB", "scsi": "SCSI",
		"sas": "SAS", "virtio": "VirtIO", "mmc": "MMC", "ufs": "UFS", "fc": "Fibre Channel",
		"iscsi": "iSCSI", "ieee1394": "FireWire", "": "", "spi": "SPI",
	}
	for in, want := range tests {
		if got := TransportLabel(in); got != want {
			t.Errorf("TransportLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
