package post

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// plymouthArgs are the kernel arguments required for a graphical Plymouth boot splash.
var plymouthArgs = []string{"rhgb", "quiet"}

// EnsurePlymouthArgs ensures the Plymouth graphical boot arguments are present
// in every BLS loader entry. Checks both <sysroot>/boot/loader/entries/
// (GRUB/3-partition) and <sysroot>/boot/efi/loader/entries/ (systemd-boot).
// Non-destructive: existing arguments are not duplicated.
//
// Returns the number of entries modified, and any first error encountered.
func EnsurePlymouthArgs(sysroot string) (int, error) {
	candidates := []string{
		filepath.Join(sysroot, "boot", "loader", "entries"),
		filepath.Join(sysroot, "boot", "efi", "loader", "entries"),
	}

	total := 0
	found := false
	for _, dir := range candidates {
		entries, err := filepath.Glob(filepath.Join(dir, "*.conf"))
		if err != nil || len(entries) == 0 {
			continue
		}
		found = true
		for _, entry := range entries {
			changed, err := ensurePlymouthInEntry(entry)
			if err != nil {
				return total, fmt.Errorf("patching %s: %w", entry, err)
			}
			if changed {
				total++
			}
		}
	}
	if !found {
		return 0, fmt.Errorf("no BLS loader entries found under %s", filepath.Join(sysroot, "boot"))
	}
	return total, nil
}

// ensurePlymouthInEntry modifies a single BLS entry file to include plymouthArgs
// on its options line. Returns true if the file was changed.
func ensurePlymouthInEntry(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	changed := false

	for i, line := range lines {
		if !strings.HasPrefix(line, "options ") {
			continue
		}
		opts := line[len("options "):]
		toAdd := []string{}
		for _, arg := range plymouthArgs {
			found := false
			for _, tok := range strings.Fields(opts) {
				if tok == arg {
					found = true
					break
				}
			}
			if !found {
				toAdd = append(toAdd, arg)
			}
		}
		if len(toAdd) > 0 {
			lines[i] = line + " " + strings.Join(toAdd, " ")
			changed = true
		}
	}

	if !changed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
