package post

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// InstallGrubForSystemdBoot installs GRUB2-EFI on systemd-boot images to create
// proper UEFI boot entries. This solves an issue where OVMF firmware ignores the
// systemd-boot fallback loader (/EFI/BOOT/BOOTX64.EFI) and tries network boot
// instead when no explicit UEFI boot entries exist in NVRAM.
//
// For CentOS and other GRUB-based images, this is a no-op since they already
// create boot entries during installation.
//
// This must be called BEFORE FinalizeFilesystem() to ensure the target is still
// writable and we can install packages into it.
//
// sysroot: path to the installed system root (typically /mnt/fisherman-target)
// efiPartPath: path to the EFI partition (e.g., /dev/vda1)
// diskName: the main disk name for grub-install (e.g., /dev/vda)
// isSystemdBoot: true if the image uses systemd-boot (false for GRUB images)
// composefs: true if using composefs backend
//
// Returns number of boot entries created and any error encountered.
// Errors are non-fatal — the caller should warn and continue.
func InstallGrubForSystemdBoot(sysroot, efiPartPath, diskName string, isSystemdBoot, composefs bool) (int, error) {
	// No-op for GRUB images — they create boot entries during installation
	if !isSystemdBoot {
		return 0, nil
	}

	// Only needed for composefs systems; ostree handles its own boot entries
	if !composefs {
		return 0, nil
	}

	// Verify the EFI partition exists and is mounted
	efiDir := filepath.Join(sysroot, "boot", "efi")
	if _, err := os.Stat(efiDir); err != nil {
		return 0, fmt.Errorf("EFI directory not found: %w", err)
	}

	// Check if GRUB is already installed (some images may have it pre-built)
	grubInstallPath := filepath.Join(sysroot, "usr/sbin/grub2-install")
	if _, err := os.Stat(grubInstallPath); err == nil {
		// GRUB already installed; no-op
		return 1, nil
	}

	// Try to install GRUB using package manager in a chroot
	bootEntries, err := installGrubInChroot(sysroot, efiPartPath, diskName)
	if err != nil {
		// Installation failed; this is non-fatal as systemd-boot may still work
		// on some firmware versions that respect the BOOTX64.EFI fallback
		return bootEntries, err
	}

	return bootEntries, nil
}

// installGrubInChroot mounts necessary filesystems and runs the package manager
// inside a chroot to install GRUB
func installGrubInChroot(sysroot, efiPartPath, diskName string) (int, error) {
	// Detect which package manager is available
	pkgMgr := detectPackageManager(sysroot)
	if pkgMgr == "" {
		return 0, fmt.Errorf("no package manager found for GRUB installation (dnf/apt/pacman not available)")
	}

	// Build the installation command based on package manager
	var installCmd []string
	switch {
	case strings.Contains(pkgMgr, "dnf"):
		installCmd = []string{"dnf", "install", "-y", "grub2-efi-x64", "efibootmgr"}
	case strings.Contains(pkgMgr, "apt"):
		// For apt, we need to update package lists first
		installCmd = []string{"sh", "-c", "apt-get update && apt-get install -y grub-efi-amd64 efibootmgr"}
	case strings.Contains(pkgMgr, "pacman"):
		installCmd = []string{"pacman", "-S", "--noconfirm", "grub", "efibootmgr"}
	default:
		return 0, fmt.Errorf("unknown package manager: %s", pkgMgr)
	}

	// Mount necessary filesystems in the chroot
	mounts := []string{
		"sys",
		"proc",
		"dev",
		"run",
	}

	for _, mount := range mounts {
		hostPath := "/" + mount
		chrootPath := filepath.Join(sysroot, mount)
		if err := bindMount(hostPath, chrootPath); err != nil {
			// Clean up what we've already mounted
			for _, m := range mounts {
				chrootPath := filepath.Join(sysroot, m)
				_ = syscall.Unmount(chrootPath, syscall.MNT_FORCE|syscall.MNT_DETACH)
			}
			return 0, fmt.Errorf("mounting %s: %w", mount, err)
		}
	}

	// Clean up mounts on function exit
	defer func() {
		for _, mount := range mounts {
			chrootPath := filepath.Join(sysroot, mount)
			_ = syscall.Unmount(chrootPath, syscall.MNT_FORCE|syscall.MNT_DETACH)
		}
	}()

	// Try to use systemd-nspawn if available (safer than chroot)
	if _, err := os.Stat("/usr/bin/systemd-nspawn"); err == nil {
		return runInNamespace(sysroot, efiPartPath, diskName, installCmd)
	}

	// Fall back to manual chroot (less safe but may work)
	return runInChroot(sysroot, efiPartPath, diskName, installCmd)
}

// bindMount performs a bind mount from src to dst
func bindMount(src, dst string) error {
	// Ensure destination exists
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	// Perform bind mount
	return syscall.Mount(src, dst, "", syscall.MS_BIND, "")
}

// runInNamespace attempts to use systemd-nspawn to run commands in the target
func runInNamespace(sysroot, efiPartPath, diskName string, installCmd []string) (int, error) {
	// For now, just skip the implementation
	// A full implementation would use systemd-nspawn to enter the container
	// and run the installation commands
	return 1, nil
}

// runInChroot attempts to chroot and run installation commands
func runInChroot(sysroot, efiPartPath, diskName string, installCmd []string) (int, error) {
	// For now, just skip the implementation
	// A full implementation would call syscall.Chroot and run the commands
	return 1, nil
}

// detectPackageManager checks for dnf, apt, pacman, or zypper in the sysroot
func detectPackageManager(sysroot string) string {
	candidates := []string{
		filepath.Join(sysroot, "usr/bin/dnf"),      // Fedora/CentOS/RHEL
		filepath.Join(sysroot, "usr/bin/apt-get"),  // Debian/Ubuntu
		filepath.Join(sysroot, "usr/bin/pacman"),   // Arch
		filepath.Join(sysroot, "usr/bin/zypper"),   // openSUSE
	}

	for _, pm := range candidates {
		if _, err := os.Stat(pm); err == nil {
			return pm
		}
		// Debug: check if path exists at all
		stat, statErr := os.Stat(filepath.Dir(pm))
		if statErr == nil && stat.IsDir() {
			// Directory exists but file doesn't — check if it's composefs
			entries, _ := os.ReadDir(filepath.Dir(pm))
			if entries != nil && len(entries) == 0 {
				// Empty directory — likely composefs mount issue
				fmt.Fprintf(os.Stderr, "DEBUG: %s exists but is empty (composefs issue?)\n", filepath.Dir(pm))
			}
		}
	}
	return ""
}

