package post

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// rootFlagsKey is the kernel argument that carries the root filesystem's
// mount options (the kernel's own root mount, and systemd's sysroot.mount).
const rootFlagsKey = "rootflags="

// rootFlagsWriteFile writes a patched loader entry. Tests replace it.
var rootFlagsWriteFile = os.WriteFile

// RootFlagsResult is what RequireRootFlags found.
type RootFlagsResult struct {
	Entries int // loader entries that carry the argument now
	Patched int // of those, the entries this call had to change
}

// RequireRootFlags makes every BLS loader entry of the installed system mount
// the root with options (recipe.RootMountOptions): each options line carries
// exactly one rootflags=, and it is rootflags=<options>. It proves it by
// reading each entry back.
//
// fisherman passes the same argument to bootc as --karg, and bootc puts its
// own rootflags=subvol=<subvol of the mount> first. That is not enough:
//
//   - bootc 1.15.x writes the composefs BLS entry from the root's own
//     arguments (root=, rootflags=subvol=..., rw), composefs= and kargs.d,
//     and never reads --karg (crates/lib/src/bootc_composefs/boot.rs,
//     setup_composefs_bls_boot). The entry then has rootflags=subvol=/@ (or
//     subvol=/ without subvolumes) and no compress=zstd:1, and the booted
//     system mounts the root uncompressed.
//   - where bootc does honour --karg (ostree, newer bootc on composefs), the
//     entry has two rootflags=, bootc's and the recipe's, and which one wins
//     is up to whoever parses the command line.
//
// Replacing bootc's rootflags= in place with the recipe's keeps one value,
// in bootc's position. The recipe's options name the same subvolume (subvol=@
// and bootc's subvol=/@ are the same path to the kernel), so nothing that
// bootc derived is lost. A composefs upgrade copies the booted entry's
// arguments, so the value stays.
//
// It returns ErrNoLoaderEntries when there is no loader entry: an image that
// boots a unified kernel image has a fixed command line, which fisherman
// cannot change. An entry without an options line, or one that cannot be
// read, written or verified, is an error.
func RequireRootFlags(sysroot, options string) (RootFlagsResult, error) {
	var res RootFlagsResult
	if options == "" || strings.ContainsAny(options, " \t\n") {
		return res, fmt.Errorf("invalid root mount options %q", options)
	}
	arg := rootFlagsKey + options
	found := 0
	for _, dir := range luksEntryDirs(sysroot) {
		entries, err := filepath.Glob(filepath.Join(dir, "*.conf"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			found++
			changed, err := setRootFlagsInEntry(entry, arg)
			if err != nil {
				return res, fmt.Errorf("patching %s: %w", entry, err)
			}
			ok, err := entryHasOnlyRootFlags(entry, arg)
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

// withRootFlags returns the options of one BLS options line with every
// rootflags= removed and arg put where the first one was, or at the end.
func withRootFlags(opts, arg string) string {
	var out []string
	placed := false
	for _, tok := range strings.Fields(opts) {
		if !strings.HasPrefix(tok, rootFlagsKey) {
			out = append(out, tok)
			continue
		}
		if !placed {
			out = append(out, arg)
			placed = true
		}
	}
	if !placed {
		out = append(out, arg)
	}
	return strings.Join(out, " ")
}

// setRootFlagsInEntry rewrites every options line of a BLS entry with
// withRootFlags. It returns true if the file was modified.
func setRootFlagsInEntry(path, arg string) (bool, error) {
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
		next := "options " + withRootFlags(line[len("options "):], arg)
		if next != line {
			lines[i] = next
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, rootFlagsWriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// entryHasOnlyRootFlags reports whether every options line of a BLS entry
// carries arg as its only rootflags=, and there is at least one.
func entryHasOnlyRootFlags(path, arg string) (bool, error) {
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
		var flags []string
		for _, tok := range strings.Fields(line[len("options "):]) {
			if strings.HasPrefix(tok, rootFlagsKey) {
				flags = append(flags, tok)
			}
		}
		if len(flags) != 1 || flags[0] != arg {
			return false, nil
		}
	}
	return lines > 0, nil
}
