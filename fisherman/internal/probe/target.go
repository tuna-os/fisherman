package probe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Target is the verdict on one path a recipe names as a disk to install to
// (`disk`, `varDisk.disk`). It is the same judgement Disks makes for the
// disk list a frontend shows, asked about a single path: a recipe can name a
// disk the probe never offered, so the install and `validate` ask again.
type Target struct {
	// Path is the path as the recipe gave it.
	Path string
	// Device is Path with symlinks resolved (/dev/disk/by-id/… → /dev/sda).
	// It is the node the install must use: partition names are built from it.
	Device string
	// Loop: Device is a whole loop device, an image file attached with
	// losetup. Disks never offers one, but a recipe may name one: that is
	// how CI and image builds install. A loop device is an image for another
	// machine, so the two checks about this machine's hardware policy
	// (ExcludedTooSmall, UnmetUEFI) do not apply to it. The others do.
	Loop      bool
	SizeBytes int64
	// Serial and WWN are the disk's identity as lsblk reads it from udev
	// (ID_SERIAL_SHORT, ID_WWN), whitespace trimmed; Model is lsblk's
	// MODEL. Each is empty when the device reports none: loop devices,
	// most virtual disks and some USB bridges have no serial or WWN.
	Serial, WWN, Model string
	// ByID lists the /dev/disk/by-id links that resolve to Device, sorted.
	// Empty when udev made none or /dev/disk/by-id cannot be read.
	ByID []string
	// Reasons lists every reason Device is not eligible, in the order Disks
	// tests them: ExcludedNotDisk, ExcludedBootDisk, ExcludedReadOnly,
	// ExcludedTooSmall. Empty means eligible. ExcludedNotDisk stands alone:
	// nothing else can be said about a path that is not a whole disk.
	Reasons []string
}

// Eligible reports whether the target may be installed to.
func (t Target) Eligible() bool { return len(t.Reasons) == 0 }

// Target judges path as an install target. It resolves symlinks, then looks
// the device up among the top-level block devices lsblk reports:
//
//   - not there (a partition such as /dev/sda1, a regular file, a
//     character device), or there with a type other than "disk" or "loop"
//     (RAM disks, zram, optical drives, device-mapper nodes): ExcludedNotDisk;
//   - it, or anything stacked on it, is mounted at one of systemMounts:
//     ExcludedBootDisk (the live medium or the running system);
//   - the kernel reports it read-only: ExcludedReadOnly;
//   - smaller than MinDiskBytes, and not a loop device: ExcludedTooSmall.
//
// An error means the verdict could not be reached (the path does not
// resolve, or lsblk failed); callers must treat that as "not eligible".
func (p *Prober) Target(path string) (Target, error) {
	t := Target{Path: path, Device: path}
	resolved, err := p.resolve(path)
	if err != nil {
		return t, err
	}
	t.Device = resolved

	devs, err := p.lsblkTree()
	if err != nil {
		return t, err
	}
	for _, dev := range devs {
		if dev.Name != t.Device && dev.KName != t.Device {
			continue
		}
		t.SizeBytes = int64(dev.Size)
		t.Serial, t.WWN, t.Model = strings.TrimSpace(str(dev.Serial)), strings.TrimSpace(str(dev.WWN)), squash(str(dev.Model))
		t.ByID = p.byIDLinks(t.Device)
		switch excludedReason(lsblkDevice{Name: dev.Name, KName: dev.KName, Type: dev.Type, Size: flexInt(MinDiskBytes)}) {
		case "":
		case ExcludedLoop:
			t.Loop = true
		default:
			t.Reasons = []string{ExcludedNotDisk}
			return t, nil
		}
		if holdsSystem(dev) {
			t.Reasons = append(t.Reasons, ExcludedBootDisk)
		}
		if bool(dev.RO) {
			t.Reasons = append(t.Reasons, ExcludedReadOnly)
		}
		if !t.Loop && t.SizeBytes < MinDiskBytes {
			t.Reasons = append(t.Reasons, ExcludedTooSmall)
		}
		return t, nil
	}
	t.Reasons = []string{ExcludedNotDisk}
	return t, nil
}

// resolve follows the symlinks of path, a host path, and returns the node
// it names as the host would name it.
func (p *Prober) resolve(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p.local(path))
	if err != nil {
		return "", err
	}
	if root := filepath.Clean(p.root()); root != "/" {
		// A fixture root (tests): report the path as the host would name it.
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		resolved = "/" + strings.TrimPrefix(strings.TrimPrefix(resolved, root), "/")
	}
	return resolved, nil
}

// byIDLinks lists the /dev/disk/by-id links that resolve to device, sorted.
// It only reads the directory and follows the links.
func (p *Prober) byIDLinks(device string) []string {
	const dir = "/dev/disk/by-id"
	entries, err := os.ReadDir(p.local(dir))
	if err != nil {
		return nil
	}
	var links []string
	for _, e := range entries {
		link := dir + "/" + e.Name()
		if dest, err := p.resolve(link); err == nil && dest == device {
			links = append(links, link)
		}
	}
	sort.Strings(links)
	return links
}

// lsblkTree is the device tree Disks parses, unjudged.
func (p *Prober) lsblkTree() ([]lsblkDevice, error) {
	out, err := p.run("lsblk", "-J", "-b", "-p", "-o", lsblkColumns)
	if err != nil {
		var legacyErr error
		out, legacyErr = p.run("lsblk", "-J", "-b", "-p", "-o", lsblkColumnsLegacy)
		if legacyErr != nil {
			return nil, fmt.Errorf("lsblk: %w", err)
		}
	}
	var parsed struct {
		Blockdevices []lsblkDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}
	return parsed.Blockdevices, nil
}

// UEFI reports whether the host booted through UEFI: System's `uefi`, without
// the RAM and CPU reads System also does.
func (p *Prober) UEFI() bool { return p.uefi() }
