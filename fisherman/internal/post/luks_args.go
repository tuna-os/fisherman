package post

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LuksKarg is the kernel argument that makes the initrd unlock the LUKS
// container luksUUID and map it to /dev/mapper/root.
//
// rd.luks.name=<UUID>=root (not rd.luks.uuid=<UUID>) is required so that the
// LUKS container is mapped as /dev/mapper/root.  systemd-gpt-auto-generator
// looks for the root filesystem either on a partition typed Linux root (x86-64)
// or behind /dev/mapper/root; rd.luks.uuid maps to /dev/mapper/luks-<UUID>
// which the generator cannot find, causing a ~90s hang before the emergency
// shell (projectbluefin/dakota#270).
func LuksKarg(luksUUID string) string {
	return "rd.luks.name=" + luksUUID + "=root"
}

// ErrNoLoaderEntries is returned by RequireLuksArgs when the installed
// system has no BLS loader entry to check or patch.
var ErrNoLoaderEntries = errors.New("no BLS loader entry found under boot/loader/entries or boot/efi/loader/entries")

// LuksArgsResult is what RequireLuksArgs found.
type LuksArgsResult struct {
	Entries int // loader entries that carry the argument now
	Patched int // of those, the entries this call had to change
}

// luksWriteFile writes a patched loader entry. Tests replace it.
var luksWriteFile = os.WriteFile

// luksEntryDirs are the BLS entry directories: <sysroot>/boot/loader/entries
// (GRUB, 3-partition layout) and <sysroot>/boot/efi/loader/entries
// (systemd-boot, 2-partition layout).
func luksEntryDirs(sysroot string) []string {
	return []string{
		filepath.Join(sysroot, "boot", "loader", "entries"),
		filepath.Join(sysroot, "boot", "efi", "loader", "entries"),
	}
}

// RequireLuksArgs makes sure every BLS loader entry of the installed system
// carries rd.luks.name=<luksUUID>=root, and proves it by reading each entry
// back.
//
// bootc install to-filesystem only sees the already-opened device mapper node,
// so it writes root=UUID=<fs-uuid> without any LUKS parameters. Without the
// rd.luks.* argument the initrd will fail to find the root device and the
// system will not boot. fisherman also passes the argument to bootc as a
// --karg where the layout has no other way to find the root; this function
// is the check that it arrived, and the patch when it did not.
//
// It returns an error when there is no loader entry (ErrNoLoaderEntries),
// when an entry has no options line to carry the argument, or when an entry
// cannot be read, written or verified. The caller decides whether that ends
// the install: it must, unless the layout finds its root by GPT
// auto-discovery (#410).
func RequireLuksArgs(sysroot, luksUUID string) (LuksArgsResult, error) {
	var res LuksArgsResult
	if luksUUID == "" {
		return res, errors.New("no LUKS UUID to inject")
	}
	arg := LuksKarg(luksUUID)
	found := 0
	for _, dir := range luksEntryDirs(sysroot) {
		entries, err := filepath.Glob(filepath.Join(dir, "*.conf"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			found++
			changed, err := ensureArgInEntry(entry, arg)
			if err != nil {
				return res, fmt.Errorf("patching %s: %w", entry, err)
			}
			ok, err := entryHasArg(entry, arg)
			if err != nil {
				return res, fmt.Errorf("verifying %s: %w", entry, err)
			}
			if !ok {
				return res, fmt.Errorf("%s has no options line that carries %s", entry, arg)
			}
			res.Entries++
			if changed {
				res.Patched++
			}
		}
	}
	if found == 0 {
		return res, ErrNoLoaderEntries
	}
	return res, nil
}

// EnsureLuksArgs injects rd.luks.name=<luksUUID>=root into every BLS loader
// entry under the installed system that has an options line. It returns the
// number of entries modified and the first error. It does not say whether
// the system can unlock its root: no entry at all, or an entry with no
// options line, is not an error here. An install calls RequireLuksArgs.
func EnsureLuksArgs(sysroot, luksUUID string) (int, error) {
	if luksUUID == "" {
		return 0, nil
	}
	arg := LuksKarg(luksUUID)
	total := 0
	for _, dir := range luksEntryDirs(sysroot) {
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

// entryHasArg reports whether every options line of a BLS entry carries arg,
// and there is at least one.
func entryHasArg(path, arg string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	lines := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "options ") {
			continue
		}
		lines++
		has := false
		for _, tok := range strings.Fields(line[len("options "):]) {
			has = has || tok == arg
		}
		if !has {
			return false, nil
		}
	}
	return lines > 0, nil
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
	return true, luksWriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
