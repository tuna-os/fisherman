package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tuna-os/fisherman/internal/progress"
)

// InstallSystemdBoot installs the systemd-boot EFI binary into the ESP from
// the ostree deployment found inside targetMount. This is necessary because
// bootc install --skip-finalize skips bootupd, which would otherwise install
// the bootloader binary to the EFI partition.
//
// The binary is copied to both the vendor path (EFI/systemd/systemd-bootx64.efi)
// and the UEFI fallback path (EFI/BOOT/BOOTX64.EFI) so UEFI firmware can
// auto-detect the bootloader without an NVRAM entry.
func InstallSystemdBoot(targetMount string) error {
	deployRoot := filepath.Join(targetMount, "ostree", "deploy", "default", "deploy")
	pattern := filepath.Join(deployRoot, "*.0", "usr", "lib", "systemd", "boot", "efi", "systemd-bootx64.efi")

	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return fmt.Errorf("systemd-bootx64.efi not found in ostree deployment (pattern: %s)", pattern)
	}
	srcBin := matches[0]
	progress.Info(fmt.Sprintf("Installing systemd-boot from %s", srcBin))

	efiDir := filepath.Join(targetMount, "boot", "efi")

	dsts := []string{
		filepath.Join(efiDir, "EFI", "systemd", "systemd-bootx64.efi"),
		filepath.Join(efiDir, "EFI", "BOOT", "BOOTX64.EFI"),
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
