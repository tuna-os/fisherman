package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/tuna-os/fisherman/internal/progress"
)

// InstallSystemdBoot ensures the systemd-boot EFI binary is present on the
// ESP. This is a fallback for newer bootctl (e.g. arch-bootc) which runs in
// --graceful mode inside the container and silently skips writing to the ESP.
//
// The binary is copied to both the vendor path (EFI/systemd/systemd-boot<arch>.efi)
// and the UEFI fallback path (EFI/BOOT/BOOT<ARCH>.EFI) so UEFI firmware can
// auto-detect the bootloader without an NVRAM entry.
//
// If the fallback loader already exists on the ESP (e.g. bootctl ran correctly for
// debian-bootc), this function is a no-op.
//
// File names follow the target architecture (x64 or aa64).
func InstallSystemdBoot(targetMount string) error {
	return installSystemdBoot(targetMount, runtime.GOARCH)
}

// systemdBootNames returns the systemd-boot vendor binary name and the UEFI
// fallback loader name for goarch.
func systemdBootNames(goarch string) (vendorName, fallbackName string) {
	if goarch == "arm64" {
		return "systemd-bootaa64.efi", "BOOTAA64.EFI"
	}
	return "systemd-bootx64.efi", "BOOTX64.EFI"
}

func installSystemdBoot(targetMount, goarch string) error {
	vendorName, fallbackName := systemdBootNames(goarch)
	efiDir := filepath.Join(targetMount, "boot", "efi")
	fallback := filepath.Join(efiDir, "EFI", "BOOT", fallbackName)

	if _, err := os.Stat(fallback); err == nil {
		progress.Info("systemd-boot EFI binary already present, skipping manual install")
		return nil
	}

	srcBin, err := findSystemdBootBinary(targetMount, vendorName)
	if err != nil {
		return err
	}
	progress.Info(fmt.Sprintf("Installing systemd-boot from %s", srcBin))

	dsts := []string{
		filepath.Join(efiDir, "EFI", "systemd", vendorName),
		fallback,
	}
	for _, dst := range dsts {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
		}
		if err := copyFile(srcBin, dst); err != nil {
			return fmt.Errorf("copying systemd-boot to %s: %w", dst, err)
		}
	}

	// Write loader.conf if not already created by bootc.
	loaderConf := filepath.Join(efiDir, "loader", "loader.conf")
	if _, err := os.Stat(loaderConf); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(loaderConf), 0o755); err != nil {
			return fmt.Errorf("mkdir loader dir: %w", err)
		}
		if err := os.WriteFile(loaderConf, []byte("timeout 5\n"), 0o644); err != nil {
			return fmt.Errorf("writing loader.conf: %w", err)
		}
	}

	progress.Info("systemd-boot installed successfully")
	return nil
}

// findSystemdBootBinary searches common ostree deployment paths for the
// systemd-boot EFI binary, preferring the .signed variant when available.
// Searches across any stateroot and deployment slot (not just "default" and
// "*.0") to handle non-standard bootc setups.
func findSystemdBootBinary(targetMount, vendorName string) (string, error) {
	// Search both the standard ostree prefix and the sysroot prefix used by
	// some composefs configurations.
	ostreeRoots := []string{
		filepath.Join(targetMount, "ostree"),
		filepath.Join(targetMount, "sysroot", "ostree"),
	}

	for _, root := range ostreeRoots {
		for _, variant := range []string{vendorName + ".signed", vendorName} {
			// Use a broad glob: any stateroot, any deployment slot.
			pattern := filepath.Join(root, "deploy", "*", "deploy", "*",
				"usr", "lib", "systemd", "boot", "efi", variant)
			matches, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			if len(matches) > 0 {
				return matches[0], nil
			}
		}
	}
	return "", fmt.Errorf("%s not found under %s/{ostree,sysroot/ostree}", vendorName, targetMount)
}

// copyFile copies src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
