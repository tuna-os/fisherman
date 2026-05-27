package post

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed icons/*.svg
var oemIcons embed.FS

// vendorIconFiles maps vendor to their embedded icon filename.
var vendorIconFiles = map[string]string{
	"asus": "icons/asus-rog-symbolic.svg",
}

// installVendorIcon installs the vendor's symbolic icon into the target's
// hicolor icon theme so it can be referenced by name in dconf/extensions.
func installVendorIcon(target string, vendor string) error {
	iconFile, ok := vendorIconFiles[vendor]
	if !ok {
		return nil // no icon for this vendor
	}

	iconName := vendorMenuIcons[vendor]
	if iconName == "" {
		return nil
	}

	data, err := oemIcons.ReadFile(iconFile)
	if err != nil {
		return fmt.Errorf("reading embedded icon: %w", err)
	}

	// Resolve target paths.
	var usrDir string
	if isComposeFsNative(target) {
		usrDir = filepath.Join(target, "usr")
	} else {
		deployDir, err := DeploymentDirFn(target)
		if err != nil {
			return fmt.Errorf("finding deployment dir: %w", err)
		}
		usrDir = filepath.Join(deployDir, "usr")
	}

	// Install to hicolor scalable/apps — available system-wide.
	iconDir := filepath.Join(usrDir, "share", "icons", "hicolor", "scalable", "apps")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return fmt.Errorf("mkdir icon dir: %w", err)
	}

	iconPath := filepath.Join(iconDir, iconName+".svg")
	if err := os.WriteFile(iconPath, data, 0o644); err != nil {
		return fmt.Errorf("writing icon: %w", err)
	}

	return nil
}
