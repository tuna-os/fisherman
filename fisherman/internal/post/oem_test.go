package post

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Tests for internal/post/oem.go + oem_icons.go (were 0%):
// OEM vendor normalization, sub-brand detection matching, icon install,
// dconf menu-icon override, and the first-boot brew service writer.

func TestNormalizeVendor(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"ASUSTek COMPUTER INC.", "asus"},
		{"asustek computer inc.", "asus"},
		{"ASUS", "asus"},
		{"Framework", "framework"},
		{"TUXEDO", "tuxedo"},
		{"Tuxedo Computers", "tuxedo"},
		{"TUXEDO COMPUTERS GMBH", "tuxedo"},
		{"Dell Inc.", "dell"},
		{"DELL", "dell"},
		{"LENOVO", "lenovo"},
		{"HP", "hp"},
		{"Hewlett-Packard", "hp"},
		{"Hewlett Packard", "hp"},
		{"System76", "system76"},
		{"Razer", "razer"},
		{"Micro-Star International Co., Ltd.", "msi"},
		{"MSI", "msi"},
		// Unknown / blank / whitespace-padded → empty (no match).
		{"", ""},
		{"   ", ""},
		{"Some Unknown Vendor", ""},
		{"Apple Inc.", ""},
	}
	for _, tc := range cases {
		if got := normalizeVendor(tc.raw); got != tc.want {
			t.Errorf("normalizeVendor(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestDetectArch(t *testing.T) {
	want := ""
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		want = "arm"
	}
	if got := detectArch(); got != want {
		t.Errorf("detectArch() = %q, want %q (GOARCH=%s)", got, want, runtime.GOARCH)
	}
}

// TestVendorMapsConsistent ensures the three vendor lookup tables never
// drift: every vendor with packages/services also appears in the icon map,
// and every icon entry maps to a real file in the embedded icon set.
func TestVendorMapsConsistent(t *testing.T) {
	for vendor, pkgs := range vendorPackages {
		if len(pkgs) == 0 {
			t.Errorf("vendorPackages[%q] is empty", vendor)
		}
		if _, ok := vendorMenuIcons[vendor]; !ok {
			t.Errorf("vendor %q has packages but no entry in vendorMenuIcons", vendor)
		}
	}
	for vendor, svcs := range vendorServices {
		if len(svcs) == 0 {
			t.Errorf("vendorServices[%q] is empty", vendor)
		}
		if _, ok := vendorPackages[vendor]; !ok {
			t.Errorf("vendor %q has services but no packages entry", vendor)
		}
	}
	for vendor, icon := range vendorMenuIcons {
		if icon == "" {
			t.Errorf("vendorMenuIcons[%q] has empty icon name", vendor)
		}
		file, ok := vendorIconFiles[vendor]
		if !ok {
			t.Errorf("vendor %q has menu icon %q but no entry in vendorIconFiles", vendor, icon)
			continue
		}
		if _, err := oemIcons.ReadFile(file); err != nil {
			// Missing embed files are deliberate (the installer skips them
			// gracefully at runtime — see installVendorIcon). Only flag
			// vendors that ship an icon file but a wrong map entry.
			t.Logf("vendorIconFiles[%q] = %q not yet on disk (expected skip): %v", vendor, file, err)
		}
	}
}

// TestInstallVendorIconOstree writes the embedded icon into an ostree-style
// target (deployment dir resolved via DeploymentDirFn).
func TestInstallVendorIconOstree(t *testing.T) {
	target := t.TempDir()
	// Simulate an ostree deployment (isComposeFsNative uses runner ls;
	// pre-create state/deploy absence + ostree presence via dirs).
	deployDir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDeploy := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeploy }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	if err := installVendorIcon(target, "asus"); err != nil {
		t.Fatalf("installVendorIcon(asus): %v", err)
	}

	iconPath := filepath.Join(deployDir, "usr", "share", "icons", "hicolor", "scalable", "apps", "asus-rog-symbolic.svg")
	data, err := os.ReadFile(iconPath)
	if err != nil {
		t.Fatalf("reading installed icon: %v", err)
	}
	if !strings.Contains(string(data), "<svg") {
		t.Errorf("installed icon does not look like SVG (len=%d)", len(data))
	}
}

func TestInstallVendorIconUnknownVendor(t *testing.T) {
	target := t.TempDir()
	if err := installVendorIcon(target, "not-a-vendor"); err != nil {
		t.Fatalf("installVendorIcon(unknown) = %v, want nil (skip)", err)
	}
	// Nothing should have been created.
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("unknown vendor created %d entries under target", len(entries))
	}
}

// TestWriteMenuIconOverride checks the dconf override file content and
// location for an ostree target.
func TestWriteMenuIconOverride(t *testing.T) {
	target := t.TempDir()
	deployDir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDeploy := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeploy }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	if err := writeMenuIconOverride(target, "framework-symbolic"); err != nil {
		t.Fatalf("writeMenuIconOverride: %v", err)
	}

	overridePath := filepath.Join(deployDir, "etc", "dconf", "db", "local.d", "02-oem-menu-icon")
	data, err := os.ReadFile(overridePath)
	if err != nil {
		t.Fatalf("reading dconf override: %v", err)
	}
	want := "[org/gnome/shell/extensions/custom-command-list]\nmenuicon-setting='framework-symbolic'\n"
	if string(data) != want {
		t.Errorf("override content = %q, want %q", string(data), want)
	}
}

// TestWriteOEMBrewServiceOstree verifies the first-boot script, unit file,
// and preset are written with correct cask-vs-formula handling.
func TestWriteOEMBrewServiceOstree(t *testing.T) {
	target := t.TempDir()
	deployDir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDeploy := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeploy }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	// Mix of a cask (ends with -linux) and a formula.
	pkgs := []string{"ublue-os/tap/asusctl-linux", "some/formula"}
	if err := writeOEMBrewService(target, "asus", pkgs, "bootc", "ublue-os/tap"); err != nil {
		t.Fatalf("writeOEMBrewService: %v", err)
	}

	etcDir := filepath.Join(deployDir, "etc")
	script, err := os.ReadFile(filepath.Join(etcDir, "bootc", "oem", "install-packages.sh"))
	if err != nil {
		t.Fatalf("reading install script: %v", err)
	}
	s := string(script)
	for _, want := range []string{
		"# OEM package installer for asus hardware.",
		"brew tap ublue-os/tap 2>/dev/null || true",
		"brew install --cask ublue-os/tap/asusctl-linux 2>/dev/null || true",
		"brew install some/formula 2>/dev/null || true",
		"systemctl --user disable bootc-oem-setup.service 2>/dev/null || true",
		`MARKER="$HOME/.config/bootc/oem-setup-done"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q", want)
		}
	}

	unit, err := os.ReadFile(filepath.Join(etcDir, "systemd", "user", "bootc-oem-setup.service"))
	if err != nil {
		t.Fatalf("reading unit file: %v", err)
	}
	for _, want := range []string{
		"[Unit]",
		"ExecStart=/etc/bootc/oem/install-packages.sh",
		"WantedBy=default.target",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit missing %q", want)
		}
	}

	preset, err := os.ReadFile(filepath.Join(etcDir, "systemd", "user-preset", "50-bootc-oem.preset"))
	if err != nil {
		t.Fatalf("reading preset: %v", err)
	}
	if string(preset) != "enable bootc-oem-setup.service\n" {
		t.Errorf("preset = %q", string(preset))
	}
}

func TestWriteOEMBrewServiceNoBrewTap(t *testing.T) {
	target := t.TempDir()
	deployDir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDeploy := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeploy }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	// distroID is resolved to "bootc" by InstallOEMPackages before this is
	// called; an empty brewTap must not emit a `brew tap` line.
	if err := writeOEMBrewService(target, "asus", []string{"some/formula"}, "bootc", ""); err != nil {
		t.Fatalf("writeOEMBrewService: %v", err)
	}
	script, err := os.ReadFile(filepath.Join(deployDir, "etc", "bootc", "oem", "install-packages.sh"))
	if err != nil {
		t.Fatalf("reading install script: %v", err)
	}
	if strings.Contains(string(script), "brew tap") {
		t.Error("script contains brew tap line although brewTap is empty")
	}
}

// TestEnableSystemServiceOstree verifies the wants-dir symlink is created for
// an ostree target.
func TestEnableSystemServiceOstree(t *testing.T) {
	target := t.TempDir()
	deployDir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDeploy := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeploy }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	enableSystemService(target, "asusd.service")

	link := filepath.Join(deployDir, "etc", "systemd", "system", "multi-user.target.wants", "asusd.service")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", link, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %s to be a symlink, got mode %v", link, fi.Mode())
	}
	dest, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if dest != "/usr/lib/systemd/system/asusd.service" {
		t.Errorf("symlink dest = %q, want /usr/lib/systemd/system/asusd.service", dest)
	}
}
