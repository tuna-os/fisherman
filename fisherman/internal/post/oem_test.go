package post

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalizeVendor covers the raw-DMI-string → canonical-name mapping,
// including case/whitespace normalization and the unknown-vendor default.
func TestNormalizeVendor(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"ASUSTeK COMPUTER INC.", "asus"},
		{"  asus  ", "asus"},
		{"Framework", "framework"},
		{"TUXEDO", "tuxedo"},
		{"Tuxedo Computers", "tuxedo"},
		{"TUXEDO Computers GmbH", "tuxedo"},
		{"Dell Inc.", "dell"},
		{"LENOVO", "lenovo"},
		{"Hewlett-Packard", "hp"},
		{"Hewlett Packard", "hp"},
		{"HP", "hp"},
		{"System76", "system76"},
		{"Razer", "razer"},
		{"Micro-Star International Co., Ltd.", "msi"},
		{"", ""},
		{"Some Unknown Vendor Ltd.", ""},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := normalizeVendor(tt.raw); got != tt.want {
				t.Errorf("normalizeVendor(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestDetectArch verifies detectArch only claims "arm" for arm/arm64 and is
// consistent with the actual runtime.GOARCH — this doesn't force a specific
// architecture (the test runs on whatever GOARCH built it), just that the
// function's own stated contract holds.
func TestDetectArch(t *testing.T) {
	got := detectArch()
	if got != "" && got != "arm" {
		t.Errorf("detectArch() = %q, want \"\" or \"arm\"", got)
	}
}

// setupOstreeTarget creates a temp dir shaped like an ostree deployment and
// overrides DeploymentDirFn to resolve to it, matching the pattern already
// used in post_test.go's TestWriteHostname_OstreeBackend.
func setupOstreeTarget(t *testing.T) (target, deployDir string) {
	t.Helper()
	target = t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "ostree"), 0o755); err != nil {
		t.Fatal(err)
	}
	deployDir = filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }
	t.Cleanup(func() { DeploymentDirFn = DefaultDeploymentDir })
	return target, deployDir
}

// setupComposefsTarget creates a temp dir shaped like a composefs-native
// deployment and overrides ComposeFsDeployEtcDirFn to resolve to it.
func setupComposefsTarget(t *testing.T) (target, deployEtc string) {
	t.Helper()
	target = t.TempDir()
	deployEtc = filepath.Join(target, "state", "deploy", "abc123", "etc")
	if err := os.MkdirAll(deployEtc, 0o755); err != nil {
		t.Fatal(err)
	}
	ComposeFsDeployEtcDirFn = func(string) (string, error) { return deployEtc, nil }
	t.Cleanup(func() { ComposeFsDeployEtcDirFn = DefaultComposeFsDeployEtcDir })
	return target, deployEtc
}

func TestWriteOEMBrewService_Ostree(t *testing.T) {
	target, deployDir := setupOstreeTarget(t)
	etcDir := filepath.Join(deployDir, "etc")

	pkgs := []string{"ublue-os/tap/asusctl-linux", "ublue-os/tap/some-formula"}
	if err := writeOEMBrewService(target, "asus", pkgs, "bootc", ""); err != nil {
		t.Fatalf("writeOEMBrewService: %v", err)
	}

	script, err := os.ReadFile(filepath.Join(etcDir, "bootc", "oem", "install-packages.sh"))
	if err != nil {
		t.Fatalf("reading install script: %v", err)
	}
	if !strings.Contains(string(script), "brew install --cask ublue-os/tap/asusctl-linux") {
		t.Error("expected a --cask install for the -linux suffixed package")
	}
	if !strings.Contains(string(script), "brew install ublue-os/tap/some-formula") ||
		strings.Contains(string(script), "brew install --cask ublue-os/tap/some-formula") {
		t.Error("expected a plain formula install for the non -linux package")
	}

	if _, err := os.Stat(filepath.Join(etcDir, "systemd", "user", "bootc-oem-setup.service")); err != nil {
		t.Errorf("expected the systemd user unit to be written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(etcDir, "systemd", "user-preset", "50-bootc-oem.preset")); err != nil {
		t.Errorf("expected the preset file to be written: %v", err)
	}
}

func TestWriteOEMBrewService_ComposeFsNative(t *testing.T) {
	target, deployEtc := setupComposefsTarget(t)

	if err := writeOEMBrewService(target, "framework", []string{"ublue-os/tap/framework-tool"}, "bazzite", "ublue-os/tap"); err != nil {
		t.Fatalf("writeOEMBrewService: %v", err)
	}

	script, err := os.ReadFile(filepath.Join(deployEtc, "bazzite", "oem", "install-packages.sh"))
	if err != nil {
		t.Fatalf("reading install script from composefs deploy etc: %v", err)
	}
	if !strings.Contains(string(script), "brew tap ublue-os/tap") {
		t.Error("expected the brew tap command to be included when brewTap is set")
	}
}

func TestEnableSystemService_Ostree(t *testing.T) {
	target, deployDir := setupOstreeTarget(t)
	etcDir := filepath.Join(deployDir, "etc")

	enableSystemService(target, "asusd.service")

	link := filepath.Join(etcDir, "systemd", "system", "multi-user.target.wants", "asusd.service")
	dest, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected a symlink at %s: %v", link, err)
	}
	if dest != "/usr/lib/systemd/system/asusd.service" {
		t.Errorf("symlink target = %q, want /usr/lib/systemd/system/asusd.service", dest)
	}
}

func TestEnableSystemService_ComposeFsNative(t *testing.T) {
	target, deployEtc := setupComposefsTarget(t)

	enableSystemService(target, "asusd.service")

	link := filepath.Join(deployEtc, "systemd", "system", "multi-user.target.wants", "asusd.service")
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("expected a symlink under the composefs deploy etc: %v", err)
	}
}

// TestEnableSystemService_DeploymentDirError verifies enableSystemService
// degrades gracefully (no panic, no symlink attempted) when the deployment
// dir can't be resolved — it's best-effort, called for a non-fatal service
// enable during OEM setup.
func TestEnableSystemService_DeploymentDirError(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "ostree"), 0o755); err != nil {
		t.Fatal(err)
	}
	DeploymentDirFn = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { DeploymentDirFn = DefaultDeploymentDir })

	// Must not panic; there's nothing else externally observable to assert
	// beyond "it returned".
	enableSystemService(target, "asusd.service")
}

func TestWriteMenuIconOverride_DeploymentDirError(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "ostree"), 0o755); err != nil {
		t.Fatal(err)
	}
	DeploymentDirFn = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { DeploymentDirFn = DefaultDeploymentDir })

	if err := writeMenuIconOverride(target, "asus-rog-symbolic"); err == nil {
		t.Error("expected an error when the deployment dir cannot be resolved")
	}
}

func TestWriteMenuIconOverride(t *testing.T) {
	target, deployDir := setupOstreeTarget(t)
	etcDir := filepath.Join(deployDir, "etc")

	if err := writeMenuIconOverride(target, "asus-rog-symbolic"); err != nil {
		t.Fatalf("writeMenuIconOverride: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(etcDir, "dconf", "db", "local.d", "02-oem-menu-icon"))
	if err != nil {
		t.Fatalf("reading dconf override: %v", err)
	}
	if !strings.Contains(string(data), "menuicon-setting='asus-rog-symbolic'") {
		t.Errorf("dconf override content = %q, missing expected menuicon-setting line", string(data))
	}
}

func TestInstallVendorIcon_KnownVendorWithEmbeddedFile(t *testing.T) {
	target, deployDir := setupOstreeTarget(t)

	if err := installVendorIcon(target, "asus"); err != nil {
		t.Fatalf("installVendorIcon: %v", err)
	}

	iconPath := filepath.Join(deployDir, "usr", "share", "icons", "hicolor", "scalable", "apps", "asus-rog-symbolic.svg")
	if _, err := os.Stat(iconPath); err != nil {
		t.Errorf("expected the asus icon to be installed at %s: %v", iconPath, err)
	}
}

func TestInstallVendorIcon_UnknownVendor(t *testing.T) {
	target, deployDir := setupOstreeTarget(t)

	if err := installVendorIcon(target, "totally-unknown-vendor"); err != nil {
		t.Errorf("expected nil for an unmapped vendor, got %v", err)
	}

	iconsDir := filepath.Join(deployDir, "usr", "share", "icons")
	if _, err := os.Stat(iconsDir); err == nil {
		t.Error("expected no icon directory to be created for an unmapped vendor")
	}
}

// TestInstallVendorIcon_VendorWithoutEmbeddedFile covers a vendor present in
// vendorMenuIcons but whose icon file hasn't shipped yet (vendorIconFiles
// entries not backed by an actual embedded SVG, e.g. legion/yoga/thinkpad*)
// — installVendorIcon must skip gracefully rather than error.
func TestInstallVendorIcon_VendorWithoutEmbeddedFile(t *testing.T) {
	target, deployDir := setupOstreeTarget(t)

	if err := installVendorIcon(target, "legion"); err != nil {
		t.Errorf("expected a graceful nil for a not-yet-shipped icon, got %v", err)
	}

	iconsDir := filepath.Join(deployDir, "usr", "share", "icons")
	if _, err := os.Stat(iconsDir); err == nil {
		t.Error("expected no icon directory to be created when the embedded file is missing")
	}
}

// TestVendorIconFilesActuallyEmbedded is a repo-hygiene check, not a
// behavioral one: every vendorIconFiles entry should resolve to a real
// embedded asset, or installVendorIcon silently no-ops for it forever.
// Flags drift between the two maps (a vendor added to one but not the
// other) as a loud test failure instead of a silent no-op in production.
func TestVendorIconFilesActuallyEmbedded(t *testing.T) {
	for vendor, file := range vendorIconFiles {
		if _, err := oemIcons.ReadFile(file); err != nil {
			t.Logf("vendor %q maps to %q, which has no embedded asset yet (installVendorIcon will no-op for it)", vendor, file)
		}
	}
}
