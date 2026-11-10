package probe

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GPU is one PCI display controller of the machine that runs the installer.
// It is identified by its PCI ids only: no name lookup, no serial, no slot.
type GPU struct {
	VendorID string `json:"vendor_id"` // "0x10de"
	DeviceID string `json:"device_id"` // "0x2684"
	// Class is the 24-bit PCI class code: "0x030000" VGA, "0x030200" 3D,
	// "0x038000" other display controller.
	Class string `json:"class"`
}

// DMI is the firmware's name for the machine model. Both values are shared
// by every unit of a model. The serial numbers and the machine UUID that
// sit next to them in sysfs are never read.
type DMI struct {
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
}

const (
	pciDevicesDir = "/sys/bus/pci/devices"
	dmiDir        = "/sys/class/dmi/id"
	// pciDisplayClass is the PCI base class of display controllers.
	pciDisplayClass = "0x03"
	// maxDMILen bounds a firmware string. Real ones are far shorter.
	maxDMILen = 128
)

var (
	pciIDRe    = regexp.MustCompile(`^0x[0-9a-f]{4}$`)
	pciClassRe = regexp.MustCompile(`^0x[0-9a-f]{6}$`)
)

// GPUs lists the display controllers under /sys/bus/pci/devices, in PCI
// address order. A device whose class, vendor or device file is missing or
// not a PCI id is left out. No PCI bus, or none readable, is an empty list.
func (p *Prober) GPUs() []GPU {
	gpus := []GPU{}
	entries, err := os.ReadDir(p.local(pciDevicesDir))
	if err != nil {
		return gpus
	}
	for _, e := range entries { // ReadDir sorts by name, i.e. by address
		dir := filepath.Join(pciDevicesDir, e.Name())
		class := p.pciValue(dir+"/class", pciClassRe)
		if !strings.HasPrefix(class, pciDisplayClass) {
			continue
		}
		vendor := p.pciValue(dir+"/vendor", pciIDRe)
		device := p.pciValue(dir+"/device", pciIDRe)
		if vendor == "" || device == "" {
			continue
		}
		gpus = append(gpus, GPU{VendorID: vendor, DeviceID: device, Class: class})
	}
	return gpus
}

// pciValue reads a sysfs PCI attribute and returns it lower-cased when it
// has the expected shape, "" otherwise.
func (p *Prober) pciValue(path string, shape *regexp.Regexp) string {
	v, err := p.readKernelFile(path)
	if err != nil {
		return ""
	}
	v = strings.ToLower(v)
	if !shape.MatchString(v) {
		return ""
	}
	return v
}

// DMI reads the system vendor and product name. A machine without DMI (most
// ARM boards), an unreadable file or an empty value gives "".
func (p *Prober) DMI() DMI {
	return DMI{
		Vendor:  p.dmiValue("sys_vendor"),
		Product: p.dmiValue("product_name"),
	}
}

func (p *Prober) dmiValue(name string) string {
	raw, err := os.ReadFile(p.local(filepath.Join(dmiDir, name)))
	if err != nil {
		return ""
	}
	if len(raw) > 4*maxDMILen {
		raw = raw[:4*maxDMILen]
	}
	// Firmware strings are free text: keep printable characters only,
	// collapse whitespace, bound the length.
	clean := strings.Map(func(r rune) rune {
		if r == '�' || r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(string(raw), " "))
	clean = strings.Join(strings.Fields(clean), " ")
	if r := []rune(clean); len(r) > maxDMILen {
		clean = strings.TrimSpace(string(r[:maxDMILen]))
	}
	return clean
}
