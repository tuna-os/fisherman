package install

// Tests for internal/install/systemdboot.go (0% coverage): the systemd-boot
// ESP fallback install (InstallSystemdBoot), the ostree deployment search
// (findSystemdBootBinary), and copyFile. All three are pure filesystem logic
// and are exercised with temp dirs — no exec, no root.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeDeployTree builds an ostree deployment tree containing a systemd-boot
// binary under target/ostree/deploy/<stateroot>/deploy/<slot>/usr/lib/...
func makeDeployTree(t *testing.T, target string) string {
	t.Helper()
	bin := filepath.Join(target, "ostree", "deploy", "fedora", "deploy", "abc123",
		"usr", "lib", "systemd", "boot", "efi", "systemd-bootx64.efi")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatalf("mkdir deploy tree: %v", err)
	}
	if err := os.WriteFile(bin, []byte("EFI"), 0o755); err != nil {
		t.Fatalf("write efi binary: %v", err)
	}
	return bin
}

// TestInstallSystemdBoot_FullInstall verifies a complete install: the binary
// is copied to both the vendor path and the UEFI fallback path, loader.conf is
// created with the default timeout, and the copy preserves the source mode.
func TestInstallSystemdBoot_FullInstall(t *testing.T) {
	target := t.TempDir()
	srcBin := makeDeployTree(t, target)

	if err := InstallSystemdBoot(target); err != nil {
		t.Fatalf("InstallSystemdBoot: %v", err)
	}

	vendor := filepath.Join(target, "boot", "efi", "EFI", "systemd", "systemd-bootx64.efi")
	fallback := filepath.Join(target, "boot", "efi", "EFI", "BOOT", "BOOTX64.EFI")
	loaderConf := filepath.Join(target, "boot", "efi", "loader", "loader.conf")

	for _, p := range []string{vendor, fallback, loaderConf} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	// Binary content copied.
	got, err := os.ReadFile(vendor)
	if err != nil || string(got) != "EFI" {
		t.Errorf("vendor binary content = %q, err=%v; want EFI", got, err)
	}
	// Mode preserved from the source (0755).
	info, err := os.Stat(vendor)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		t.Errorf("vendor binary mode = %v, want at least owner rwx", info.Mode().Perm())
	}
	// loader.conf defaults.
	lc, err := os.ReadFile(loaderConf)
	if err != nil || string(lc) != "timeout 5\n" {
		t.Errorf("loader.conf = %q, err=%v; want %q", lc, err, "timeout 5\n")
	}
	_ = srcBin
}

// TestInstallSystemdBoot_NoopWhenFallbackExists verifies the function skips
// work when BOOTX64.EFI is already present on the ESP.
func TestInstallSystemdBoot_NoopWhenFallbackExists(t *testing.T) {
	target := t.TempDir()
	fallback := filepath.Join(target, "boot", "efi", "EFI", "BOOT", "BOOTX64.EFI")
	if err := os.MkdirAll(filepath.Dir(fallback), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallback, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallSystemdBoot(target); err != nil {
		t.Fatalf("InstallSystemdBoot: %v", err)
	}

	// The pre-existing file must be untouched and no vendor copy created.
	got, _ := os.ReadFile(fallback)
	if string(got) != "existing" {
		t.Errorf("fallback content = %q, want untouched 'existing'", got)
	}
	vendor := filepath.Join(target, "boot", "efi", "EFI", "systemd", "systemd-bootx64.efi")
	if _, err := os.Stat(vendor); !os.IsNotExist(err) {
		t.Errorf("vendor binary should not have been created, err=%v", err)
	}
}

// TestInstallSystemdBoot_PreservesExistingLoaderConf verifies an existing
// loader.conf is not overwritten.
func TestInstallSystemdBoot_PreservesExistingLoaderConf(t *testing.T) {
	target := t.TempDir()
	makeDeployTree(t, target)

	loaderConf := filepath.Join(target, "boot", "efi", "loader", "loader.conf")
	if err := os.MkdirAll(filepath.Dir(loaderConf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loaderConf, []byte("timeout 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallSystemdBoot(target); err != nil {
		t.Fatalf("InstallSystemdBoot: %v", err)
	}

	got, _ := os.ReadFile(loaderConf)
	if string(got) != "timeout 30\n" {
		t.Errorf("loader.conf = %q, want preserved 'timeout 30\\n'", got)
	}
}

// TestInstallSystemdBoot_BinaryNotFound verifies the error surfaced when no
// systemd-boot binary exists under either ostree root.
func TestInstallSystemdBoot_BinaryNotFound(t *testing.T) {
	target := t.TempDir() // empty target, no ostree tree

	err := InstallSystemdBoot(target)
	if err == nil {
		t.Fatal("expected error when systemd-boot binary is absent, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want a 'not found' error", err)
	}
}

// TestFindSystemdBootBinary_PrefersSigned verifies the .signed variant wins
// when both variants exist in the deployment.
func TestFindSystemdBootBinary_PrefersSigned(t *testing.T) {
	target := t.TempDir()
	base := filepath.Join(target, "ostree", "deploy", "fedora", "deploy", "abc123",
		"usr", "lib", "systemd", "boot", "efi")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	signed := filepath.Join(base, "systemd-bootx64.efi.signed")
	unsigned := filepath.Join(base, "systemd-bootx64.efi")
	for _, p := range []string{signed, unsigned} {
		if err := os.WriteFile(p, []byte("EFI"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := findSystemdBootBinary(target)
	if err != nil {
		t.Fatalf("findSystemdBootBinary: %v", err)
	}
	if got != signed {
		t.Errorf("got %s, want signed variant %s", got, signed)
	}
}

// TestFindSystemdBootBinary_SysrootRoot verifies the sysroot/ostree search
// root is consulted (composefs layouts).
func TestFindSystemdBootBinary_SysrootRoot(t *testing.T) {
	target := t.TempDir()
	bin := filepath.Join(target, "sysroot", "ostree", "deploy", "fedora", "deploy", "abc123",
		"usr", "lib", "systemd", "boot", "efi", "systemd-bootx64.efi")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("EFI"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findSystemdBootBinary(target)
	if err != nil {
		t.Fatalf("findSystemdBootBinary: %v", err)
	}
	if got != bin {
		t.Errorf("got %s, want %s", got, bin)
	}
}

// TestFindSystemdBootBinary_NotFound verifies the error for an empty tree.
func TestFindSystemdBootBinary_NotFound(t *testing.T) {
	if _, err := findSystemdBootBinary(t.TempDir()); err == nil {
		t.Fatal("expected error for empty target, got nil")
	}
}

// TestCopyFile verifies content and permission preservation.
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	if err := os.WriteFile(src, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "hello" {
		t.Errorf("dst content = %q, err=%v; want hello", got, err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("dst mode = %v, want 0640", info.Mode().Perm())
	}
}

// TestCopyFile_MissingSource verifies the error path for a nonexistent src.
func TestCopyFile_MissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := copyFile(filepath.Join(dir, "nope"), filepath.Join(dir, "dst")); err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
}
