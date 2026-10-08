package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// MinDiskBytes is the smallest disk offered for installation: 50 GiB. It is
// bootc-installer's GNOME `min_disk_size` default (51200 MiB,
// bootc_installer/defaults/disk.py), the only frontend that enforced one.
const MinDiskBytes int64 = 50 << 30

// Reasons a disk is not eligible, in the order they are tested. Exactly one
// is reported per disk; the first that applies wins.
const (
	ExcludedLoop         = "loop"          // loop device (an attached image file)
	ExcludedRAM          = "ram"           // /dev/ramN RAM disk
	ExcludedZram         = "zram"          // compressed-RAM swap device
	ExcludedOptical      = "optical"       // srN / type "rom"
	ExcludedDeviceMapper = "device_mapper" // dm-N, LVM, dm-crypt
	ExcludedNotDisk      = "not_disk"      // any other lsblk TYPE
	ExcludedBootDisk     = "boot_disk"     // the running (live or installed) system is on it
	ExcludedReadOnly     = "read_only"     // the kernel reports it read-only
	ExcludedTooSmall     = "too_small"     // below MinDiskBytes
)

// systemMounts are mount points that mean "the system running this probe
// lives on this disk". Any of them on a disk or on anything stacked on it
// (a partition, LUKS, LVM) excludes the disk.
//
//   - / and /sysroot: an installed system. On bootc/ostree / is a composefs
//     overlay and the real root partition is mounted at /sysroot.
//   - /boot, /boot/efi, /efi, /usr, /var: the rest of an installed system.
//   - /run/initramfs/live, /run/initramfs/isoscan: dracut dmsquash-live
//     mounts the live medium (or the disk holding the ISO file) here.
//   - /run/media/iso: the live mount XFCE's frontend excluded.
var systemMounts = map[string]bool{
	"/":                      true,
	"/sysroot":               true,
	"/boot":                  true,
	"/boot/efi":              true,
	"/efi":                   true,
	"/usr":                   true,
	"/var":                   true,
	"/run/initramfs/live":    true,
	"/run/initramfs/isoscan": true,
	"/run/media/iso":         true,
}

// Disk is one top-level block device.
type Disk struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	SizeLabel      string `json:"size_label"`
	Model          string `json:"model"`
	Vendor         string `json:"vendor"`
	Transport      string `json:"transport"`
	TransportLabel string `json:"transport_label"`
	Removable      bool   `json:"removable"`
	ReadOnly       bool   `json:"read_only"`
	Eligible       bool   `json:"eligible"`
	ExcludedReason string `json:"excluded_reason,omitempty"`
}

// lsblkColumns are the columns Disks asks for. MOUNTPOINTS needs util-linux
// 2.37; lsblkColumnsLegacy is the fallback for older hosts.
const (
	lsblkColumns       = "NAME,KNAME,TYPE,SIZE,MODEL,VENDOR,TRAN,RM,HOTPLUG,RO,MOUNTPOINTS"
	lsblkColumnsLegacy = "NAME,KNAME,TYPE,SIZE,MODEL,VENDOR,TRAN,RM,HOTPLUG,RO,MOUNTPOINT"
)

// lsblkDevice is one node of `lsblk -J -b -p` tree output. Booleans and the
// size are flexible because lsblk before 2.33 printed them as strings.
type lsblkDevice struct {
	Name        string        `json:"name"`
	KName       string        `json:"kname"`
	Type        string        `json:"type"`
	Size        flexInt       `json:"size"`
	Model       *string       `json:"model"`
	Vendor      *string       `json:"vendor"`
	Tran        *string       `json:"tran"`
	RM          flexBool      `json:"rm"`
	Hotplug     flexBool      `json:"hotplug"`
	RO          flexBool      `json:"ro"`
	Mountpoint  *string       `json:"mountpoint"`
	Mountpoints []*string     `json:"mountpoints"`
	Children    []lsblkDevice `json:"children"`
}

// Disks lists every top-level block device lsblk reports, eligible or not.
// lsblk runs on the host (through the runner), so mount points come from the
// host's mount namespace even when the probe runs in a sandbox.
func (p *Prober) Disks() ([]Disk, error) {
	out, err := p.run("lsblk", "-J", "-b", "-p", "-o", lsblkColumns)
	if err != nil {
		var legacyErr error
		out, legacyErr = p.run("lsblk", "-J", "-b", "-p", "-o", lsblkColumnsLegacy)
		if legacyErr != nil {
			return nil, err
		}
	}
	return ParseLsblk(out)
}

// ParseLsblk turns `lsblk -J -b -p` tree output into Disks.
func ParseLsblk(out []byte) ([]Disk, error) {
	var parsed struct {
		Blockdevices []lsblkDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}
	disks := make([]Disk, 0, len(parsed.Blockdevices))
	for _, dev := range parsed.Blockdevices {
		disks = append(disks, toDisk(dev))
	}
	return disks, nil
}

func toDisk(dev lsblkDevice) Disk {
	tran := strings.TrimSpace(str(dev.Tran))
	d := Disk{
		Path:           dev.Name,
		SizeBytes:      int64(dev.Size),
		SizeLabel:      SizeLabel(int64(dev.Size)),
		Model:          squash(str(dev.Model)),
		Vendor:         squash(str(dev.Vendor)),
		Transport:      tran,
		TransportLabel: TransportLabel(tran),
		Removable:      bool(dev.RM) || bool(dev.Hotplug),
		ReadOnly:       bool(dev.RO),
	}
	d.ExcludedReason = excludedReason(dev)
	d.Eligible = d.ExcludedReason == ""
	return d
}

func excludedReason(dev lsblkDevice) string {
	kname := dev.KName
	if kname == "" {
		kname = dev.Name
	}
	base := filepath.Base(kname)
	switch {
	case dev.Type == "loop" || strings.HasPrefix(base, "loop"):
		return ExcludedLoop
	case strings.HasPrefix(base, "zram"):
		return ExcludedZram
	case strings.HasPrefix(base, "ram"):
		return ExcludedRAM
	case dev.Type == "rom" || strings.HasPrefix(base, "sr"):
		return ExcludedOptical
	case strings.HasPrefix(base, "dm-") || dev.Type == "dm" || dev.Type == "lvm" || dev.Type == "crypt":
		return ExcludedDeviceMapper
	case dev.Type != "disk":
		return ExcludedNotDisk
	case holdsSystem(dev):
		return ExcludedBootDisk
	case bool(dev.RO):
		return ExcludedReadOnly
	case int64(dev.Size) < MinDiskBytes:
		return ExcludedTooSmall
	}
	return ""
}

// holdsSystem reports whether dev or anything stacked on it is mounted at
// one of systemMounts.
func holdsSystem(dev lsblkDevice) bool {
	mounts := dev.Mountpoints
	if dev.Mountpoint != nil {
		mounts = append(mounts, dev.Mountpoint)
	}
	for _, m := range mounts {
		if m != nil && systemMounts[*m] {
			return true
		}
	}
	for _, c := range dev.Children {
		if holdsSystem(c) {
			return true
		}
	}
	return false
}

// SizeLabel formats a byte count with binary (IEC) units: 1 GiB = 2^30 bytes,
// labelled GiB/TiB so the unit is honest. One decimal place, dropped when it
// is zero ("50 GiB", "476.9 GiB", "1.8 TiB").
func SizeLabel(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + units[i]
}

// TransportLabel is the human name for an lsblk TRAN value. Unknown values
// are upper-cased; an empty value stays empty so a frontend can render its
// own "unknown bus" copy.
func TransportLabel(tran string) string {
	switch strings.ToLower(tran) {
	case "":
		return ""
	case "nvme":
		return "NVMe"
	case "sata", "ata":
		return "SATA"
	case "usb":
		return "USB"
	case "scsi":
		return "SCSI"
	case "sas":
		return "SAS"
	case "virtio":
		return "VirtIO"
	case "mmc":
		return "MMC"
	case "ufs":
		return "UFS"
	case "fc":
		return "Fibre Channel"
	case "iscsi":
		return "iSCSI"
	case "ieee1394":
		return "FireWire"
	}
	return strings.ToUpper(tran)
}

// squash trims and collapses the space padding SCSI/ATA inquiry strings carry.
func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// flexBool accepts true/false, "1"/"0" and null.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(data)), `"`)
	*b = flexBool(s == "true" || s == "1")
	return nil
}

// flexInt accepts a number, a numeric string and null.
type flexInt int64

func (n *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(data)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("size %q: %w", s, err)
	}
	*n = flexInt(v)
	return nil
}
