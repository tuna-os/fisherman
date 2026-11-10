package post

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tuna-os/fisherman/internal/deploy"
)

//go:embed icons/*.svg
var oemIcons embed.FS

// vendorIconFiles maps vendor to their embedded icon filename.
// Icons not yet on disk will be skipped gracefully at runtime.
var vendorIconFiles = map[string]string{
	"asus":      "icons/asus-rog-symbolic.svg",
	"framework": "icons/framework-symbolic.svg",
	"dell":      "icons/dell-symbolic.svg",
	"alienware": "icons/alienware-symbolic.svg",
	"hp":        "icons/hp-symbolic.svg",
	"system76":  "icons/system76-symbolic.svg",
	"razer":     "icons/razer-symbolic.svg",
	"msi":       "icons/msi-symbolic.svg",
	"nvidia":    "icons/nvidia-symbolic.svg",
	"arm":       "icons/arm-symbolic.svg",
	"legion":    "icons/legion-symbolic.svg",
	"yoga":      "icons/yoga-symbolic.svg",
	"thinkpad":  "icons/thinkpad-symbolic.svg",
	"thinkpadx": "icons/thinkpadx-symbolic.svg",
}

// detectGPUVendor checks for NVIDIA GPU presence via sysfs/PCI.
func detectGPUVendor() string {
	// NVIDIA PCI vendor ID is 0x10de
	entries, err := os.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		vendor, err := os.ReadFile(filepath.Join("/sys/bus/pci/devices", e.Name(), "vendor"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(vendor)) == "0x10de" {
			class, err := os.ReadFile(filepath.Join("/sys/bus/pci/devices", e.Name(), "class"))
			if err != nil {
				continue
			}
			// VGA controller class: 0x030000, 3D controller: 0x030200
			cls := strings.TrimSpace(string(class))
			if strings.HasPrefix(cls, "0x0302") || strings.HasPrefix(cls, "0x0300") {
				return "nvidia"
			}
		}
	}
	return ""
}

// detectArch returns "arm" if running on ARM/aarch64.
func detectArch() string {
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		return "arm"
	}
	return ""
}

// installVendorIcon installs the vendor's symbolic icon into the installed
// image's hicolor icon theme so it can be referenced by name in
// dconf/extensions. It reports whether the icon is now there.
//
// On ostree the deployment is a checkout: a new file in it is a new file,
// not a write through a hard link, and the booted system sees it until the
// next image update replaces the deployment. On composefs /usr is in the
// object store and cannot be added to: nothing is written (it used to go to
// <target>/usr, which nothing reads).
func installVendorIcon(target string, vendor string) (bool, error) {
	iconFile, ok := vendorIconFiles[vendor]
	if !ok {
		return false, nil // no icon for this vendor
	}

	iconName := vendorMenuIcons[vendor]
	if iconName == "" {
		return false, nil
	}

	data, err := oemIcons.ReadFile(iconFile)
	if err != nil {
		// Icon file not yet available — skip gracefully (placeholder for future icons)
		return false, nil //nolint:nilerr // best-effort: absent/unreadable source → skip, continue
	}

	d, err := ResolveDeployment(target)
	if err != nil {
		return false, err
	}
	if d.Layout == deploy.ComposeFs {
		return false, nil
	}
	usrDir := filepath.Join(d.Root, "usr")

	// Install to hicolor scalable/apps — available system-wide.
	iconDir := filepath.Join(usrDir, "share", "icons", "hicolor", "scalable", "apps")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return false, fmt.Errorf("mkdir icon dir: %w", err)
	}

	iconPath := filepath.Join(iconDir, iconName+".svg")
	// Never write through an existing file: on ostree it is a hard link
	// into the repository.
	if _, err := os.Lstat(iconPath); err == nil {
		return true, nil
	}
	if err := os.WriteFile(iconPath, data, 0o644); err != nil {
		return false, fmt.Errorf("writing icon: %w", err)
	}

	return true, nil
}
