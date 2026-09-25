package install

import (
	"os"
	"path/filepath"
	"testing"
)

// makeGrubESPTree creates a vendor directory under target/boot/efi/EFI/<vendor>.
func makeGrubESPTree(t *testing.T, target, vendor string) {
	t.Helper()
	vdir := filepath.Join(target, "boot", "efi", "EFI", vendor)
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatalf("mkdir vendor tree: %v", err)
	}

	files := map[string]string{
		"shimx64.efi": "SHIM-" + vendor,
		"grubx64.efi": "GRUB-" + vendor,
		"grub.cfg":    "search.fs_uuid 1234\n",
		"mmx64.efi":   "MM-" + vendor,
		"BOOT.CSV":    "shimx64.efi,Hummingbird\n",
	}

	for name, content := range files {
		p := filepath.Join(vdir, name)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatalf("write file %s: %v", name, err)
		}
		_ = os.Chmod(p, 0o755)
	}
}

// TestInstallGrubFallback_FullInstall verifies that shim, grub, grub.cfg,
// MokManager, and BOOT.CSV are all copied to EFI/BOOT.
func TestInstallGrubFallback_FullInstall(t *testing.T) {
	target := t.TempDir()
	makeGrubESPTree(t, target, "fedora")

	fallbackName, grubName, mmName := fallbackBootNames()
	bootDir := filepath.Join(target, "boot", "efi", "EFI", "BOOT")
	fallbackPath := filepath.Join(bootDir, fallbackName)
	grubPath := filepath.Join(bootDir, grubName)
	cfgPath := filepath.Join(bootDir, "grub.cfg")
	mmPath := filepath.Join(bootDir, mmName)
	csvPath := filepath.Join(bootDir, "BOOT.CSV")

	if err := InstallGrubFallback(target); err != nil {
		t.Fatalf("InstallGrubFallback: %v", err)
	}

	// Verify all expected files exist
	for _, p := range []string{fallbackPath, grubPath, cfgPath, mmPath, csvPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	// Verify content
	shimGot, err := os.ReadFile(fallbackPath)
	if err != nil || string(shimGot) != "SHIM-fedora" {
		t.Errorf("fallback shim = %q, want SHIM-fedora (err=%v)", shimGot, err)
	}

	grubGot, err := os.ReadFile(grubPath)
	if err != nil || string(grubGot) != "GRUB-fedora" {
		t.Errorf("fallback grub = %q, want GRUB-fedora (err=%v)", grubGot, err)
	}

	cfgGot, err := os.ReadFile(cfgPath)
	if err != nil || string(cfgGot) != "search.fs_uuid 1234\n" {
		t.Errorf("fallback grub.cfg = %q, want cfg content (err=%v)", cfgGot, err)
	}
}

// TestInstallGrubFallback_MultipleVendorsPreferredDistro verifies that when
// multiple vendor directories exist (e.g. fedora and hummingbird in #233),
// the preferred distro is chosen.
func TestInstallGrubFallback_MultipleVendorsPreferredDistro(t *testing.T) {
	target := t.TempDir()
	makeGrubESPTree(t, target, "fedora")
	makeGrubESPTree(t, target, "hummingbird")

	fallbackName, grubName, _ := fallbackBootNames()
	bootDir := filepath.Join(target, "boot", "efi", "EFI", "BOOT")
	fallbackPath := filepath.Join(bootDir, fallbackName)
	grubPath := filepath.Join(bootDir, grubName)

	if err := InstallGrubFallback(target, "hummingbird"); err != nil {
		t.Fatalf("InstallGrubFallback: %v", err)
	}

	shimGot, err := os.ReadFile(fallbackPath)
	if err != nil || string(shimGot) != "SHIM-hummingbird" {
		t.Errorf("fallback shim = %q, want SHIM-hummingbird (err=%v)", shimGot, err)
	}

	grubGot, err := os.ReadFile(grubPath)
	if err != nil || string(grubGot) != "GRUB-hummingbird" {
		t.Errorf("fallback grub = %q, want GRUB-hummingbird (err=%v)", grubGot, err)
	}
}

// TestInstallGrubFallback_NoopWhenFallbackExists verifies that existing
// fallback binaries are not overwritten.
func TestInstallGrubFallback_NoopWhenFallbackExists(t *testing.T) {
	target := t.TempDir()
	makeGrubESPTree(t, target, "fedora")

	fallbackName, grubName, _ := fallbackBootNames()
	bootDir := filepath.Join(target, "boot", "efi", "EFI", "BOOT")
	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fallbackPath := filepath.Join(bootDir, fallbackName)
	grubPath := filepath.Join(bootDir, grubName)

	if err := os.WriteFile(fallbackPath, []byte("existing-shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grubPath, []byte("existing-grub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := InstallGrubFallback(target); err != nil {
		t.Fatalf("InstallGrubFallback: %v", err)
	}

	got, _ := os.ReadFile(fallbackPath)
	if string(got) != "existing-shim" {
		t.Errorf("fallbackPath overwritten: got %q, want existing-shim", got)
	}
}

// TestInstallGrubFallback_NonEFISkipsGracefully verifies that a non-EFI system
// (no /boot/efi directory) returns nil.
func TestInstallGrubFallback_NonEFISkipsGracefully(t *testing.T) {
	target := t.TempDir()
	if err := InstallGrubFallback(target); err != nil {
		t.Errorf("expected nil for non-EFI system, got %v", err)
	}
}

// TestInstallGrubFallback_GrubOnlyFallback verifies that if no shim binary is
// present, grub is installed directly as the fallback loader.
func TestInstallGrubFallback_GrubOnlyFallback(t *testing.T) {
	target := t.TempDir()
	vdir := filepath.Join(target, "boot", "efi", "EFI", "custom")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	grubSrc := filepath.Join(vdir, "grubx64.efi")
	if err := os.WriteFile(grubSrc, []byte("RAW-GRUB"), 0o755); err != nil {
		t.Fatal(err)
	}

	fallbackName, _, _ := fallbackBootNames()
	fallbackPath := filepath.Join(target, "boot", "efi", "EFI", "BOOT", fallbackName)

	if err := InstallGrubFallback(target); err != nil {
		t.Fatalf("InstallGrubFallback: %v", err)
	}

	got, err := os.ReadFile(fallbackPath)
	if err != nil || string(got) != "RAW-GRUB" {
		t.Errorf("fallbackPath = %q, want RAW-GRUB (err=%v)", got, err)
	}
}

// TestInstallGrubFallback_OstreeFallback verifies fallback discovery in ostree.
func TestInstallGrubFallback_OstreeFallback(t *testing.T) {
	target := t.TempDir()
	// Create /boot/efi/EFI so ESP is recognized, but leave it empty of vendor dirs.
	if err := os.MkdirAll(filepath.Join(target, "boot", "efi", "EFI"), 0o755); err != nil {
		t.Fatal(err)
	}

	deployVdir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "slot0",
		"boot", "efi", "EFI", "fedora")
	if err := os.MkdirAll(deployVdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployVdir, "shimx64.efi"), []byte("OSTREE-SHIM"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployVdir, "grubx64.efi"), []byte("OSTREE-GRUB"), 0o755); err != nil {
		t.Fatal(err)
	}

	fallbackName, grubName, _ := fallbackBootNames()
	fallbackPath := filepath.Join(target, "boot", "efi", "EFI", "BOOT", fallbackName)
	grubPath := filepath.Join(target, "boot", "efi", "EFI", "BOOT", grubName)

	if err := InstallGrubFallback(target); err != nil {
		t.Fatalf("InstallGrubFallback: %v", err)
	}

	shimGot, _ := os.ReadFile(fallbackPath)
	if string(shimGot) != "OSTREE-SHIM" {
		t.Errorf("fallbackPath = %q, want OSTREE-SHIM", shimGot)
	}
	grubGot, _ := os.ReadFile(grubPath)
	if string(grubGot) != "OSTREE-GRUB" {
		t.Errorf("grubPath = %q, want OSTREE-GRUB", grubGot)
	}
}

// TestInstallGrubFallback_NotFoundErrors verifies that an error is returned
// when no EFI binaries can be found anywhere.
func TestInstallGrubFallback_NotFoundErrors(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "boot", "efi", "EFI", "emptyvendor"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := InstallGrubFallback(target); err == nil {
		t.Error("expected error when no EFI binaries found, got nil")
	}
}
