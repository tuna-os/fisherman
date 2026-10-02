package install

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tuna-os/fisherman/internal/progress"
)

// fallbackBootNames returns the UEFI fallback loader binary name and
// the corresponding GRUB binary name for the current architecture.
func fallbackBootNames() (fallbackName, grubName, mmName string) {
	switch runtime.GOARCH {
	case "arm64":
		return "BOOTAA64.EFI", "grubaa64.efi", "mmaa64.efi"
	case "386":
		return "BOOTIA32.EFI", "grubia32.efi", "mmia32.efi"
	case "riscv64":
		return "BOOTRISCV64.EFI", "grubriscv64.efi", "mmriscv64.efi"
	default:
		return "BOOTX64.EFI", "grubx64.efi", "mmx64.efi"
	}
}

// vendorCandidate tracks EFI binaries discovered in an ESP vendor directory.
type vendorCandidate struct {
	vendorDir string
	shimPath  string
	grubPath  string
	cfgPath   string
	mmPath    string
	csvPath   string
	score     int
}

// InstallGrubFallback ensures the UEFI fallback bootloader (EFI/BOOT/BOOTX64.EFI
// and grubx64.efi alongside it) is present on the ESP for GRUB 2 installations.
//
// After bootc/bootupctl installs GRUB, the ESP contains vendor directories such as
// EFI/fedora or EFI/hummingbird (with shimx64.efi and grubx64.efi) and an NVRAM
// entry is registered. However, no fallback loader is placed at EFI/BOOT/BOOTX64.EFI.
// If NVRAM is reset or the disk is moved to another system, UEFI firmware fails to
// boot without the standard fallback loader.
//
// This function copies the signed shim to EFI/BOOT/BOOTX64.EFI and grubx64.efi
// alongside it, enabling UEFI auto-discovery to boot the disk (#233).
func InstallGrubFallback(targetMount string, preferredDistro ...string) error {
	efiDir := filepath.Join(targetMount, "boot", "efi")
	if _, err := os.Stat(efiDir); os.IsNotExist(err) {
		// Non-EFI system or /boot/efi not mounted — skip gracefully.
		return nil
	}

	// Locate the EFI directory under /boot/efi (check EFI then efi).
	efiRoot := filepath.Join(efiDir, "EFI")
	if _, err := os.Stat(efiRoot); os.IsNotExist(err) {
		efiRoot = filepath.Join(efiDir, "efi")
		if _, err := os.Stat(efiRoot); os.IsNotExist(err) {
			progress.Info("No EFI directory found on ESP, skipping GRUB fallback install")
			return nil
		}
	}

	fallbackName, grubName, mmName := fallbackBootNames()
	bootDir := filepath.Join(efiRoot, "BOOT")
	fallbackPath := filepath.Join(bootDir, fallbackName)
	grubDest := filepath.Join(bootDir, grubName)

	// If both fallback shim and grub binary already exist, skip.
	if _, err := os.Stat(fallbackPath); err == nil {
		if _, err := os.Stat(grubDest); err == nil {
			progress.Info("GRUB EFI fallback binaries already present, skipping")
			return nil
		}
	}

	distroHint := ""
	if len(preferredDistro) > 0 {
		distroHint = strings.ToLower(preferredDistro[0])
	}

	cand, err := findGrubEFICandidate(efiRoot, targetMount, distroHint, fallbackName, grubName)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", bootDir, err)
	}

	// 1. Copy shim to BOOTX64.EFI (or architecture equivalent).
	// If no shim exists but grub exists, grub is copied as the fallback binary.
	if cand.shimPath != "" {
		progress.Info(fmt.Sprintf("Installing GRUB fallback shim from %s to %s", cand.shimPath, fallbackPath))
		if err := copyFile(cand.shimPath, fallbackPath); err != nil {
			return fmt.Errorf("copying shim fallback to %s: %w", fallbackPath, err)
		}
	} else if cand.grubPath != "" {
		progress.Info(fmt.Sprintf("Installing GRUB fallback from %s to %s", cand.grubPath, fallbackPath))
		if err := copyFile(cand.grubPath, fallbackPath); err != nil {
			return fmt.Errorf("copying grub fallback to %s: %w", fallbackPath, err)
		}
	}

	// 2. Copy grubx64.efi alongside BOOTX64.EFI so shim can load it.
	if cand.grubPath != "" {
		if err := copyFile(cand.grubPath, grubDest); err != nil {
			return fmt.Errorf("copying grub binary to %s: %w", grubDest, err)
		}
	}

	// 3. Copy grub.cfg to EFI/BOOT/grub.cfg if present so GRUB can locate /boot.
	if cand.cfgPath != "" {
		cfgDest := filepath.Join(bootDir, "grub.cfg")
		if err := copyFile(cand.cfgPath, cfgDest); err != nil {
			progress.Info(fmt.Sprintf("Warning: could not copy grub.cfg to %s: %v", cfgDest, err))
		}
	}

	// 4. Copy MokManager if present.
	if cand.mmPath != "" {
		mmDest := filepath.Join(bootDir, mmName)
		if err := copyFile(cand.mmPath, mmDest); err != nil {
			progress.Info(fmt.Sprintf("Warning: could not copy MokManager to %s: %v", mmDest, err))
		}
	}

	// 5. Copy BOOT.CSV if present.
	if cand.csvPath != "" {
		csvDest := filepath.Join(bootDir, "BOOT.CSV")
		if err := copyFile(cand.csvPath, csvDest); err != nil {
			progress.Info(fmt.Sprintf("Warning: could not copy BOOT.CSV to %s: %v", csvDest, err))
		}
	}

	progress.Info(fmt.Sprintf("GRUB EFI fallback loader installed successfully at %s", fallbackPath))
	return nil
}

// findGrubEFICandidate searches the ESP vendor directories and ostree deployments
// for shim and grub EFI binaries.
func findGrubEFICandidate(efiRoot, targetMount, distroHint, fallbackName, grubName string) (*vendorCandidate, error) {
	candidates := scanESPVendorDirs(efiRoot, distroHint, grubName)
	if len(candidates) > 0 {
		best := candidates[0]
		for _, c := range candidates[1:] {
			if c.score > best.score {
				best = c
			}
		}
		if best.shimPath != "" || best.grubPath != "" {
			return &best, nil
		}
	}

	// Fallback: search ostree deployment trees under targetMount
	cand, err := findGrubInOstree(targetMount, distroHint, grubName)
	if err == nil && (cand.shimPath != "" || cand.grubPath != "") {
		return cand, nil
	}

	return nil, fmt.Errorf("neither shim nor grub EFI binaries found under %s or %s/ostree", efiRoot, targetMount)
}

// scanESPVendorDirs scans subdirectories of efiRoot (excluding BOOT, systemd, Linux).
func scanESPVendorDirs(efiRoot, distroHint, grubName string) []vendorCandidate {
	entries, err := os.ReadDir(efiRoot)
	if err != nil {
		return nil
	}

	var candidates []vendorCandidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if lower == "boot" || lower == "systemd" || lower == "linux" {
			continue
		}

		vdir := filepath.Join(efiRoot, name)
		cand := inspectVendorDir(vdir, lower, distroHint, grubName)
		if cand.shimPath != "" || cand.grubPath != "" {
			candidates = append(candidates, cand)
		}
	}
	return candidates
}

// inspectVendorDir inspects a single vendor directory for EFI binaries.
func inspectVendorDir(vdir, lowerName, distroHint, grubName string) vendorCandidate {
	cand := vendorCandidate{vendorDir: vdir}
	files, err := os.ReadDir(vdir)
	if err != nil {
		return cand
	}

	expectedGrub := strings.ToLower(grubName)

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		fname := f.Name()
		flower := strings.ToLower(fname)
		fullPath := filepath.Join(vdir, fname)

		if isShimBinary(flower) {
			if cand.shimPath == "" || flower == "shimx64.efi" || flower == "shimaa64.efi" {
				cand.shimPath = fullPath
			}
		} else if isGrubBinary(flower, expectedGrub) {
			if cand.grubPath == "" || flower == expectedGrub {
				cand.grubPath = fullPath
			}
		} else if flower == "grub.cfg" {
			cand.cfgPath = fullPath
		} else if isMokManager(flower) {
			cand.mmPath = fullPath
		} else if strings.HasSuffix(flower, ".csv") {
			cand.csvPath = fullPath
		}
	}

	// Score candidate
	if distroHint != "" && strings.Contains(lowerName, distroHint) {
		cand.score += 20
	}
	if cand.shimPath != "" && cand.grubPath != "" {
		cand.score += 15
	} else if cand.shimPath != "" || cand.grubPath != "" {
		cand.score += 5
	}
	if cand.cfgPath != "" {
		cand.score += 3
	}
	if cand.csvPath != "" {
		cand.score += 2
	}

	return cand
}

func isShimBinary(flower string) bool {
	if flower == "shimx64.efi" || flower == "shimaa64.efi" || flower == "shim.efi" {
		return true
	}
	return strings.HasPrefix(flower, "shim") && strings.HasSuffix(flower, ".efi")
}

func isGrubBinary(flower, expected string) bool {
	if flower == expected || flower == "grub.efi" || flower == "grubx64.efi" || flower == "grubaa64.efi" {
		return true
	}
	return strings.HasPrefix(flower, "grub") && strings.HasSuffix(flower, ".efi")
}

func isMokManager(flower string) bool {
	return flower == "mmx64.efi" || flower == "mmaa64.efi" || (strings.HasPrefix(flower, "mm") && strings.HasSuffix(flower, ".efi"))
}

// findGrubInOstree searches ostree deployments for shim and grub binaries.
func findGrubInOstree(targetMount, distroHint, grubName string) (*vendorCandidate, error) {
	ostreeRoots := []string{
		filepath.Join(targetMount, "ostree"),
		filepath.Join(targetMount, "sysroot", "ostree"),
	}

	for _, root := range ostreeRoots {
		deployPattern := filepath.Join(root, "deploy", "*", "deploy", "*")
		matches, err := filepath.Glob(deployPattern)
		if err != nil || len(matches) == 0 {
			continue
		}

		for _, deployRoot := range matches {
			searchDirs := []string{
				filepath.Join(deployRoot, "boot", "efi", "EFI"),
				filepath.Join(deployRoot, "usr", "lib", "ostree-boot", "efi", "EFI"),
			}
			for _, sdir := range searchDirs {
				cands := scanESPVendorDirs(sdir, distroHint, grubName)
				if len(cands) > 0 {
					return &cands[0], nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no EFI vendor binaries found in ostree deployments")
}
