package post

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// plymouthArgs are the kernel arguments required for a graphical Plymouth boot splash.
// rhgb    — Red Hat Graphical Boot: triggers Plymouth at early userspace
// quiet   — suppresses verbose kernel messages so the splash is visible
var plymouthArgs = []string{"rhgb", "quiet"}

// EnsurePlymouthArgs ensures the Plymouth graphical boot arguments are present
// in every BLS (Boot Loader Specification) entry under
// <sysroot>/boot/loader/entries/. It is non-destructive: arguments that are
// already present are not duplicated, and entries that already contain all
// required arguments are left untouched.
//
// Returns the number of entries modified, and any first error encountered.
// Errors are non-fatal — Plymouth is cosmetic and the system will still boot.
func EnsurePlymouthArgs(sysroot string) (int, error) {
	entriesDir := filepath.Join(sysroot, "boot", "loader", "entries")
	entries, err := filepath.Glob(filepath.Join(entriesDir, "*.conf"))
	if err != nil {
		return 0, fmt.Errorf("glob loader entries: %w", err)
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("no BLS loader entries found under %s", entriesDir)
	}

	modified := 0
	for _, entry := range entries {
		changed, err := ensurePlymouthInEntry(entry)
		if err != nil {
			return modified, fmt.Errorf("patching %s: %w", entry, err)
		}
		if changed {
			modified++
		}
	}
	return modified, nil
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
		// Check which plymouthArgs are missing and add them.
		toAdd := []string{}
		for _, arg := range plymouthArgs {
			// Match whole word: the arg must appear as a standalone token.
			tokens := strings.Fields(opts)
			found := false
			for _, tok := range tokens {
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
