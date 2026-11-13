package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// pciDevice adds one PCI device's sysfs attributes to a fake root.
func pciDevice(files map[string]string, addr, class, vendor, device string) {
	dir := "sys/bus/pci/devices/" + addr + "/"
	files[dir+"class"] = class + "\n"
	files[dir+"vendor"] = vendor + "\n"
	files[dir+"device"] = device + "\n"
}

func TestGPUs(t *testing.T) {
	tests := []struct {
		name  string
		setup func(files map[string]string)
		want  []GPU
	}{
		{"intel", func(f map[string]string) {
			pciDevice(f, "0000:00:02.0", "0x030000", "0x8086", "0x9a49")
		}, []GPU{{"0x8086", "0x9a49", "0x030000"}}},
		{"amd", func(f map[string]string) {
			pciDevice(f, "0000:03:00.0", "0x030000", "0x1002", "0x744c")
		}, []GPU{{"0x1002", "0x744c", "0x030000"}}},
		{"nvidia, open-module era (RTX 4090)", func(f map[string]string) {
			pciDevice(f, "0000:01:00.0", "0x030000", "0x10de", "0x2684")
		}, []GPU{{"0x10de", "0x2684", "0x030000"}}},
		{"nvidia, legacy (GTX 780)", func(f map[string]string) {
			pciDevice(f, "0000:01:00.0", "0x030000", "0x10de", "0x1004")
		}, []GPU{{"0x10de", "0x1004", "0x030000"}}},
		{"mixed laptop, in address order", func(f map[string]string) {
			pciDevice(f, "0000:01:00.0", "0x030200", "0x10de", "0x28a0")
			pciDevice(f, "0000:00:02.0", "0x030000", "0x8086", "0xa7a0")
			pciDevice(f, "0000:00:1f.3", "0x040300", "0x8086", "0x51ca") // audio
			pciDevice(f, "0000:02:00.0", "0x010802", "0x144d", "0xa80a") // NVMe
		}, []GPU{{"0x8086", "0xa7a0", "0x030000"}, {"0x10de", "0x28a0", "0x030200"}}},
		{"virtual machine (virtio-gpu)", func(f map[string]string) {
			pciDevice(f, "0000:00:01.0", "0x038000", "0x1af4", "0x1050")
		}, []GPU{{"0x1af4", "0x1050", "0x038000"}}},
		{"upper-case ids are normalised", func(f map[string]string) {
			pciDevice(f, "0000:00:02.0", "0x030000", "0x10DE", "0x1C8D")
		}, []GPU{{"0x10de", "0x1c8d", "0x030000"}}},
		{"no PCI bus (an ARM board)", func(f map[string]string) {}, []GPU{}},
		{"PCI bus without a display controller", func(f map[string]string) {
			pciDevice(f, "0000:00:14.0", "0x0c0330", "0x8086", "0x51ed")
		}, []GPU{}},
		{"malformed and incomplete devices are left out", func(f map[string]string) {
			pciDevice(f, "0000:00:02.0", "0x030000", "8086", "0xa7a0")                    // vendor without 0x
			pciDevice(f, "0000:00:03.0", "0x030000", "0x8086", "0xa7a0; cat /etc/shadow") // junk
			pciDevice(f, "0000:00:04.0", "0x03", "0x8086", "0xa7a0")                      // short class
			f["sys/bus/pci/devices/0000:00:05.0/class"] = "0x030000\n"                    // no vendor/device
			f["sys/bus/pci/devices/0000:00:06.0/vendor/"] = ""                            // unreadable attributes
			pciDevice(f, "0000:00:07.0", "0x030000", "0x1002", "0x15bf")
		}, []GPU{{"0x1002", "0x15bf", "0x030000"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{}
			tt.setup(files)
			p := &Prober{Root: fakeRoot(t, files), Getenv: noEnv}
			got := p.GPUs()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GPUs() = %+v, want %+v", got, tt.want)
			}
			if b, _ := json.Marshal(got); string(b) == "null" {
				t.Error("an empty GPU list serialised as null")
			}
		})
	}
}

func TestDMI(t *testing.T) {
	long := strings.Repeat("A", 500)
	tests := []struct {
		name  string
		files map[string]string
		want  DMI
	}{
		{"laptop", map[string]string{
			"sys/class/dmi/id/sys_vendor":   "Framework\n",
			"sys/class/dmi/id/product_name": "Laptop 13 (AMD Ryzen 7040Series)\n",
		}, DMI{"Framework", "Laptop 13 (AMD Ryzen 7040Series)"}},
		{"virtual machine", map[string]string{
			"sys/class/dmi/id/sys_vendor":   "QEMU\n",
			"sys/class/dmi/id/product_name": "Standard PC (Q35 + ICH9, 2009)\n",
		}, DMI{"QEMU", "Standard PC (Q35 + ICH9, 2009)"}},
		{"absent (no DMI on this machine)", map[string]string{}, DMI{}},
		{"only one value present", map[string]string{
			"sys/class/dmi/id/sys_vendor": "ASUSTeK COMPUTER INC.\n",
		}, DMI{Vendor: "ASUSTeK COMPUTER INC."}},
		{"denied (attribute cannot be read)", map[string]string{
			"sys/class/dmi/id/sys_vendor/":   "",
			"sys/class/dmi/id/product_name/": "",
		}, DMI{}},
		{"empty and placeholder whitespace", map[string]string{
			"sys/class/dmi/id/sys_vendor":   "\n",
			"sys/class/dmi/id/product_name": "   \t \n",
		}, DMI{}},
		{"malformed: control characters, padding, invalid UTF-8", map[string]string{
			"sys/class/dmi/id/sys_vendor":   "  Micro-Star\x00\x1b[31m  International\xff\xfe \n",
			"sys/class/dmi/id/product_name": "MS-7D25\r\n\tREV 1\n",
		}, DMI{"Micro-Star [31m International", "MS-7D25 REV 1"}},
		{"malformed: over-long value is bounded", map[string]string{
			"sys/class/dmi/id/sys_vendor":   long,
			"sys/class/dmi/id/product_name": "ok",
		}, DMI{strings.Repeat("A", 128), "ok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Prober{Root: fakeRoot(t, tt.files), Getenv: noEnv}
			if got := p.DMI(); got != tt.want {
				t.Errorf("DMI() = %+q, want %+q", got, tt.want)
			}
		})
	}
}

// The probe must not read, and the output must not carry, any identifier of
// one particular machine.
func TestHardwareNoSerialsOrUUIDs(t *testing.T) {
	secrets := map[string]string{
		"product_serial":  "PF-SERIAL-0001",
		"product_uuid":    "4c4c4544-0000-1000-8000-aabbccddeeff",
		"board_serial":    "BOARD-SERIAL-0002",
		"chassis_serial":  "CHASSIS-SERIAL-0003",
		"board_asset_tag": "ASSET-0004",
	}
	res, err := goldenProber(t).Probe()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(res)
	for _, v := range secrets {
		if strings.Contains(string(out), v) {
			t.Errorf("probe output contains %q", v)
		}
	}
	src, err := os.ReadFile("hardware.go")
	if err != nil {
		t.Fatal(err)
	}
	for name := range secrets {
		if strings.Contains(string(src), `"`+name+`"`) {
			t.Errorf("hardware.go reads %s", filepath.Join(dmiDir, name))
		}
	}
}
