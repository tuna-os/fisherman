package post

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

// OEM hardware vendor detection and first-boot package provisioning.
// Detects the laptop/desktop vendor from DMI data and writes a systemd
// user service that installs vendor-specific packages (via brew) on the
// user's first login.

// vendorPackages maps DMI vendor names to brew packages to install.
var vendorPackages = map[string][]string{
	"asus": {
		"ublue-os/tap/asusctl-linux",
		"ublue-os/tap/rog-control-center-linux",
	},
	"framework": {
		"ublue-os/tap/framework-tool",
	},
}

// vendorServices maps DMI vendor names to systemd services to enable.
var vendorServices = map[string][]string{
	"asus": {
		"asusd.service",
	},
}

// vendorMenuIcons maps vendor to their custom-command-menu icon name.
// Only vendors with a recognizable non-wordmark symbol get an icon.
var vendorMenuIcons = map[string]string{
	"asus":      "asus-rog-symbolic",
	"framework": "framework-symbolic",
	"dell":      "dell-symbolic",
	"alienware": "alienware-symbolic",
	"hp":        "hp-symbolic",
	"system76":  "system76-symbolic",
	"razer":     "razer-symbolic",
	"msi":       "msi-symbolic",
	"nvidia":    "nvidia-symbolic",
	"arm":       "arm-symbolic",
	"legion":    "legion-symbolic",
	"yoga":      "yoga-symbolic",
	"thinkpad":  "thinkpad-symbolic",
	"thinkpadx": "thinkpadx-symbolic",
}

// detectVendor reads the system vendor from DMI sysfs.
func detectVendor() string {
	data, err := os.ReadFile("/sys/devices/virtual/dmi/id/sys_vendor")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// detectSubBrand reads the product name from DMI sysfs and returns a
// sub-brand if one is recognized (e.g. "Legion", "Yoga", "Alienware").
func detectSubBrand() string {
	data, err := os.ReadFile("/sys/devices/virtual/dmi/id/product_name")
	if err != nil {
		return ""
	}
	product := strings.ToLower(strings.TrimSpace(string(data)))

	subBrands := []struct {
		keyword string
		brand   string
	}{
		{"legion", "legion"},
		{"yoga", "yoga"},
		{"alienware", "alienware"},
		{"thinkpad x", "thinkpadx"},
		{"thinkpad", "thinkpad"},
	}

	for _, sb := range subBrands {
		if strings.Contains(product, sb.keyword) {
			return sb.brand
		}
	}
	return ""
}

// normalizeVendor maps raw DMI vendor strings to a canonical short name.
func normalizeVendor(raw string) string {
	vendorMap := map[string]string{
		"asustek computer inc.":              "asus",
		"asus":                               "asus",
		"framework":                          "framework",
		"tuxedo":                             "tuxedo",
		"tuxedo computers":                   "tuxedo",
		"tuxedo computers gmbh":              "tuxedo",
		"dell inc.":                          "dell",
		"dell":                               "dell",
		"lenovo":                             "lenovo",
		"hp":                                 "hp",
		"hewlett-packard":                    "hp",
		"hewlett packard":                    "hp",
		"system76":                           "system76",
		"razer":                              "razer",
		"micro-star international co., ltd.": "msi",
		"msi":                                "msi",
	}
	return vendorMap[strings.ToLower(strings.TrimSpace(raw))]
}

// InstallOEMPackages detects the hardware vendor and writes a first-login
// systemd user service that installs vendor-specific packages with brew
// (the casks are from the ublue-os tap), enables the vendor's system service
// when the image has it, and sets the vendor's logo as the shell menu icon.
//
// It is a product step: it assumes an image with Homebrew and a GNOME
// extension, so the install runs it only when the recipe or the image
// catalog asks (extras.oemSetup), never for an unknown image (#412).
//
// distroID is used to name paths/services (defaults to "bootc" if empty).
// brewTap is an optional Homebrew tap to add before installing packages.
// It logs what it did, or "skipped" when the hardware has no OEM setup.
func InstallOEMPackages(target, distroID, brewTap string) error {
	if distroID == "" {
		distroID = "bootc"
	}
	raw := detectVendor()
	vendor := normalizeVendor(raw)
	did := false

	if vendor != "" {
		pkgs, hasPkgs := vendorPackages[vendor]
		svcs := vendorServices[vendor]

		// Write a first-boot systemd user service that installs brew packages.
		if hasPkgs && len(pkgs) > 0 {
			if err := writeOEMBrewService(target, vendor, pkgs, distroID, brewTap); err != nil {
				return fmt.Errorf("writing OEM brew service: %w", err)
			}
			did = true
		}

		// Enable system-level services (e.g. asusd), when the image has them.
		var enabled, missing []string
		for _, svc := range svcs {
			switch enableSystemService(target, svc) {
			case unitEnabled, unitEnabledUnchecked:
				enabled = append(enabled, svc)
			case unitMissing:
				missing = append(missing, svc)
			case unitFailed:
				// already logged
			}
		}

		if hasPkgs || len(svcs) > 0 {
			msg := fmt.Sprintf("OEM setup for %s: %d packages queued, %d services enabled",
				vendor, len(pkgs), len(enabled))
			if len(missing) > 0 {
				msg += "; not in the image: " + strings.Join(missing, ", ")
			}
			progress.Info(msg)
			did = did || len(enabled) > 0
		}
	}

	// Set menu icon. Priority: sub-brand > vendor > NVIDIA GPU > ARM arch.
	iconVendor := ""
	if sub := detectSubBrand(); sub != "" {
		if _, ok := vendorMenuIcons[sub]; ok {
			iconVendor = sub
		}
	}
	if iconVendor == "" {
		iconVendor = vendor
	}
	if _, ok := vendorMenuIcons[iconVendor]; !ok {
		if gpu := detectGPUVendor(); gpu != "" {
			iconVendor = gpu
		} else if arch := detectArch(); arch != "" {
			iconVendor = arch
		}
	}

	if icon, ok := vendorMenuIcons[iconVendor]; ok {
		installed, err := installVendorIcon(target, iconVendor)
		switch {
		case err != nil:
			progress.Info(fmt.Sprintf("Warning: could not install icon for %s: %v", iconVendor, err))
		case !installed:
			// No icon file in the image and none installed: an override
			// would name an icon that does not exist.
			progress.Info(fmt.Sprintf("Menu icon for %s: skipped, the icon cannot be added to this image", iconVendor))
		default:
			if err := writeMenuIconOverride(target, icon); err != nil {
				progress.Info(fmt.Sprintf("Warning: could not set menu icon for %s: %v", iconVendor, err))
			} else {
				progress.Info(fmt.Sprintf("Menu icon set to %s", icon))
				did = true
			}
		}
	}

	if !did {
		progress.Info("OEM setup: skipped, nothing applies to this hardware")
	}
	return nil
}

// writeOEMBrewService creates a systemd user unit + script that runs once
// on first login to install vendor-specific brew packages.
func writeOEMBrewService(target string, vendor string, pkgs []string, distroID, brewTap string) error {
	d, err := ResolveDeployment(target)
	if err != nil {
		return err
	}
	etcDir := d.Etc

	// Write the install script.
	scriptDir := filepath.Join(etcDir, distroID, "oem")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", scriptDir, err)
	}

	brewInstallCmds := ""
	for _, pkg := range pkgs {
		// Determine if it's a cask (ends with -linux) or formula
		if strings.HasSuffix(pkg, "-linux") {
			brewInstallCmds += fmt.Sprintf("brew install --cask %s 2>/dev/null || true\n", pkg)
		} else {
			brewInstallCmds += fmt.Sprintf("brew install %s 2>/dev/null || true\n", pkg)
		}
	}

	tapCmd := ""
	if brewTap != "" {
		tapCmd = fmt.Sprintf("brew tap %s 2>/dev/null || true\n", brewTap)
	}

	script := fmt.Sprintf(`#!/bin/bash
# OEM package installer for %s hardware.
# Auto-generated by fisherman during installation.
# Runs once on first login, then disables itself.

set -euo pipefail

MARKER="$HOME/.config/%s/oem-setup-done"
if [[ -f "$MARKER" ]]; then
    exit 0
fi

# Wait for brew to be available (ujust may set it up)
for i in $(seq 1 30); do
    if command -v brew &>/dev/null; then
        break
    fi
    sleep 2
done

if ! command -v brew &>/dev/null; then
    echo "OEM setup: brew not available, skipping"
    exit 0
fi

%s# Install vendor packages
%s
# Mark as done
mkdir -p "$(dirname "$MARKER")"
touch "$MARKER"

# Disable the service so it doesn't run again
systemctl --user disable %s-oem-setup.service 2>/dev/null || true
`, vendor, distroID, tapCmd, brewInstallCmds, distroID)

	scriptPath := filepath.Join(scriptDir, "install-packages.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		return fmt.Errorf("writing script: %w", err)
	}

	// Write the systemd user service unit.
	// Goes to /etc/systemd/user/ so it's available for all users.
	unitDir := filepath.Join(etcDir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", unitDir, err)
	}

	unit := fmt.Sprintf(`[Unit]
Description=Install OEM-specific packages (first login only)
After=network-online.target
Wants=network-online.target
ConditionPathExists=!%%h/.config/%s/oem-setup-done

[Service]
Type=oneshot
ExecStart=/etc/%s/oem/install-packages.sh
RemainAfterExit=no

[Install]
WantedBy=default.target
`, distroID, distroID)

	unitPath := filepath.Join(unitDir, distroID+"-oem-setup.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing unit: %w", err)
	}

	// Enable the service for all users via a preset file.
	presetDir := filepath.Join(unitDir + "-preset")
	if err := os.MkdirAll(presetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir preset dir: %w", err)
	}
	preset := fmt.Sprintf("enable %s-oem-setup.service\n", distroID)
	presetPath := filepath.Join(presetDir, "50-"+distroID+"-oem.preset")
	if err := os.WriteFile(presetPath, []byte(preset), 0o644); err != nil {
		return fmt.Errorf("writing preset: %w", err)
	}

	return nil
}

// unitResult is what enableSystemService did.
type unitResult int

const (
	// unitFailed: the wants link could not be written (already logged).
	unitFailed unitResult = iota
	// unitMissing: the installed image does not have the unit; not enabled.
	unitMissing
	// unitEnabled: the image has the unit and it is enabled.
	unitEnabled
	// unitEnabledUnchecked: enabled, but the image's /usr could not be
	// inspected (composefs), so the unit may not exist.
	unitEnabledUnchecked
)

// enableSystemService enables a systemd system service in the installed
// system, in the deployment's /etc, and only when the installed image has
// the unit (#412: the links were written for every image, and the log said
// "enabled" for units that did not exist).
func enableSystemService(target string, service string) unitResult {
	d, err := ResolveDeployment(target)
	if err != nil {
		progress.Info(fmt.Sprintf("Warning: could not enable %s: %v", service, err))
		return unitFailed
	}
	present, known := d.HasUnit(service)
	if known && !present {
		return unitMissing
	}
	etcDir := d.Etc

	wantsDir := filepath.Join(etcDir, "systemd", "system", "multi-user.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		progress.Info(fmt.Sprintf("Warning: could not create wants dir for %s: %v", service, err))
		return unitFailed
	}

	// Symlink to the unit file in /usr/lib/systemd/system/
	link := filepath.Join(wantsDir, service)
	unitTarget := filepath.Join("/usr/lib/systemd/system", service)
	// Remove existing symlink if present
	os.Remove(link)
	if err := os.Symlink(unitTarget, link); err != nil {
		// Fallback: use runner to create the symlink (handles permission issues)
		if err2 := runner.Run("ln", "-sf", unitTarget, link); err2 != nil {
			progress.Info(fmt.Sprintf("Warning: could not enable %s: %v", service, err2))
			return unitFailed
		}
	}
	if !known {
		return unitEnabledUnchecked
	}
	return unitEnabled
}

// writeMenuIconOverride writes a dconf override that sets the custom-command-menu
// icon to the vendor's logo. This persists across reboots as a system dconf lock.
func writeMenuIconOverride(target string, iconName string) error {
	d, err := ResolveDeployment(target)
	if err != nil {
		return fmt.Errorf("dconf override: %w", err)
	}
	etcDir := d.Etc
	// Write dconf local.d override (higher priority than system defaults).
	dconfDir := filepath.Join(etcDir, "dconf", "db", "local.d")
	if err := os.MkdirAll(dconfDir, 0o755); err != nil {
		return fmt.Errorf("mkdir dconf dir: %w", err)
	}

	override := fmt.Sprintf(`[org/gnome/shell/extensions/custom-command-list]
menuicon-setting='%s'
`, iconName)

	overridePath := filepath.Join(dconfDir, "02-oem-menu-icon")
	if err := os.WriteFile(overridePath, []byte(override), 0o644); err != nil {
		return fmt.Errorf("writing dconf override: %w", err)
	}

	return nil
}
