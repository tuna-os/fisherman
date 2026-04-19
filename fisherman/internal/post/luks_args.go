package post

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureLuksArgs injects rd.luks.name=<luksUUID>=root into every BLS loader
// entry under the installed system so the initrd unlocks the LUKS container
// and maps it to /dev/mapper/root before mounting the root filesystem.
//
// bootc install to-filesystem only sees the already-opened device mapper node,
// so it writes root=UUID=<fs-uuid> without any LUKS parameters. Without the
// rd.luks.* argument the initrd will fail to find the root device and the
// system will not boot.
//
// rd.luks.name=<UUID>=root (not rd.luks.uuid=<UUID>) is required so that the
// LUKS container is mapped as /dev/mapper/root.  systemd-gpt-auto-generator
// looks for the root filesystem either on a partition typed Linux root (x86-64)
// or behind /dev/mapper/root; rd.luks.uuid maps to /dev/mapper/luks-<UUID>
// which the generator cannot find, causing a ~90s hang before the emergency
// shell (projectbluefin/dakota#270).
//
// Checks both <sysroot>/boot/loader/entries/ (GRUB/3-partition layout) and
// <sysroot>/boot/efi/loader/entries/ (systemd-boot/2-partition layout).
//
// Returns the number of entries modified and any first error encountered.
// Errors are non-fatal — the caller should warn and continue.
func EnsureLuksArgs(sysroot, luksUUID string) (int, error) {
	if luksUUID == "" {
		return 0, nil
	}
	// rd.luks.name=<UUID>=root maps the LUKS container to /dev/mapper/root,
	// which is required for systemd-gpt-auto-generator to locate the root fs.
	arg := "rd.luks.name=" + luksUUID + "=root"

	candidates := []string{
		filepath.Join(sysroot, "boot", "loader", "entries"),
		filepath.Join(sysroot, "boot", "efi", "loader", "entries"),
	}

	total := 0
	for _, dir := range candidates {
		entries, err := filepath.Glob(filepath.Join(dir, "*.conf"))
		if err != nil || len(entries) == 0 {
			continue
		}
		for _, entry := range entries {
			changed, err := ensureArgInEntry(entry, arg)
			if err != nil {
				return total, fmt.Errorf("patching %s: %w", entry, err)
			}
			if changed {
				total++
			}
		}
	}
	return total, nil
}

// ensureArgInEntry adds arg to the options line of a BLS entry if not already
// present. Returns true if the file was modified.
func ensureArgInEntry(path, arg string) (bool, error) {
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
		found := false
		for _, tok := range strings.Fields(opts) {
			if tok == arg {
				found = true
				break
			}
		}
		if !found {
			lines[i] = line + " " + arg
			changed = true
		}
	}

	if !changed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
