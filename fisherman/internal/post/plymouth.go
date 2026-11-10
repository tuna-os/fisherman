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
	return EnsureKernelArgs(sysroot, plymouthArgs)
}

// EnsureKernelArgs ensures each of args is on the options line of every BLS
// loader entry of the installed system. An argument already there, with the
// same value, is not added again; an argument of the form key=value replaces
// an existing key=other.
//
// Returns the number of entries modified. No BLS entry at all is an error:
// the installed system then takes its command line from somewhere this
// cannot edit (a unified kernel image), and the caller must say so.
func EnsureKernelArgs(sysroot string, args []string) (int, error) {
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
			changed, err := ensureArgsInEntry(entry, args)
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

// ensureArgsInEntry modifies a single BLS entry file to include args on its
// options line. Returns true if the file was changed.
func ensureArgsInEntry(path string, args []string) (bool, error) {
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
		tokens := strings.Fields(line[len("options "):])
		var toAdd []string
		replaced := false
		for _, arg := range args {
			key, _, hasValue := strings.Cut(arg, "=")
			present := false
			for j, tok := range tokens {
				if tok == arg {
					present = true
					break
				}
				if hasValue && strings.HasPrefix(tok, key+"=") {
					tokens[j] = arg
					present, replaced = true, true
					break
				}
			}
			if !present {
				toAdd = append(toAdd, arg)
			}
		}
		switch {
		case replaced:
			lines[i] = "options " + strings.Join(append(tokens, toAdd...), " ")
			changed = true
		case len(toAdd) > 0:
			// Appending keeps the rest of the line byte-identical.
			lines[i] = line + " " + strings.Join(toAdd, " ")
			changed = true
		}
	}

	if !changed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
