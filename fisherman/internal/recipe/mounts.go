package recipe

import (
	"fmt"
	"path/filepath"
	"strings"
)

// MountKind says what fisherman does with one customMounts entry after the
// install session (#372).
type MountKind int

const (
	// MountIgnored: an empty target. The entry is neither formatted nor
	// mounted (a BIOS boot partition, for example).
	MountIgnored MountKind = iota
	// MountBootStack: "/", "/boot" and "/boot/efi". bootc and the boot
	// loader make these persistent themselves (the root kernel argument,
	// the /boot entry bootc writes, the ESP). fisherman adds no fstab line,
	// so it cannot overwrite what the image manages.
	MountBootStack
	// MountSwap: target "swap". Activated for the install, and written to
	// the installed system's fstab.
	MountSwap
	// MountData: any other target. Mounted for the install, and written to
	// the installed system's fstab, or refused before any disk work when
	// that cannot work (CheckCustomMountsPersistence).
	MountData
)

// ClassifyCustomMount returns the MountKind of a customMounts target.
func ClassifyCustomMount(target string) MountKind {
	switch target {
	case "":
		return MountIgnored
	case "swap":
		return MountSwap
	}
	switch filepath.Clean(target) {
	case "/", "/boot", "/boot/efi":
		return MountBootStack
	}
	return MountData
}

// persistableAliases are the top-level directories a bootc image usually
// keeps as symlinks into /var. A data mount on one of them is resolved in
// the installed deployment after bootc ran, not assumed (#372).
var persistableAliases = []string{"/home", "/root", "/srv", "/opt", "/mnt", "/media", "/usr/local"}

func isUnder(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// IsVarPath reports whether a cleaned absolute path is /var or below it.
func IsVarPath(path string) bool { return isUnder(path, "/var") }

// CheckCustomMountsPersistence applies the rules that make a manual layout
// keep its mounts after the install session (#372):
//
//   - No two entries name the same mountpoint, and no partition is named
//     twice (a second path to the same device node counts).
//   - A data mount (ClassifyCustomMount) has a target the installed system
//     can mount at boot. The root of a bootc system is read-only, so that is
//     /var or a path below it, or one of the directories an image keeps as a
//     symlink into /var (/home, /root, /srv, /opt, /mnt, /media, /usr/local).
//   - On a composefs-native target (composeFsBackend) the target is below
//     /var, written as the /var path. "/var" itself is refused there for the
//     reason varDisk is (#252): the initramfs mounts the deployment's own
//     /var. A symlinked directory is refused because the deployment has no
//     root filesystem to resolve it in at install time.
//
// It reports each problem on the entry that causes it, so a frontend can mark
// the row.
//
// It reads nothing from the machine: two partition entries are the same
// device when their paths are equal. Problems, which may read the machine,
// also resolves symlinks (checkCustomMountsPersistence with EvalSymlinks),
// so /dev/disk/by-id/… and /dev/sda1 count as one device there.
func CheckCustomMountsPersistence(r *Recipe) []Problem {
	return checkCustomMountsPersistence(r, func(path string) string { return path })
}

// resolveSymlinks follows symlinks to the device node; a path that cannot be
// resolved is compared as it is.
func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func checkCustomMountsPersistence(r *Recipe, device func(path string) string) []Problem {
	var ps []Problem
	targets := map[string]int{}
	parts := map[string]int{}
	for i, cm := range r.CustomMounts {
		kind := ClassifyCustomMount(cm.Target)
		if cm.Partition != "" {
			dev := device(cm.Partition)
			if first, dup := parts[dev]; dup {
				ps = append(ps, Problem{fmt.Sprintf("customMounts[%d].partition", i), CodeCustomMountPartitionDuplicate,
					fmt.Sprintf("customMounts[%d]: partition %s is the same device as customMounts[%d]", i, cm.Partition, first)})
			} else {
				parts[dev] = i
			}
		}
		if kind == MountIgnored || kind == MountSwap {
			continue
		}
		if !strings.HasPrefix(cm.Target, "/") || hasDotDot(cm.Target) {
			continue // custom_mount_target_invalid reports it
		}
		target := filepath.Clean(cm.Target)
		if first, dup := targets[target]; dup {
			ps = append(ps, Problem{fmt.Sprintf("customMounts[%d].target", i), CodeCustomMountTargetDuplicate,
				fmt.Sprintf("customMounts[%d]: target %s is already used by customMounts[%d]", i, target, first)})
			continue
		}
		targets[target] = i
		if kind != MountData {
			continue
		}
		field := fmt.Sprintf("customMounts[%d].target", i)
		alias := false
		for _, a := range persistableAliases {
			alias = alias || isUnder(target, a)
		}
		switch {
		case !IsVarPath(target) && !alias:
			ps = append(ps, Problem{field, CodeCustomMountNotPersistable,
				fmt.Sprintf("customMounts[%d]: target %s cannot be mounted again on the installed system: "+
					"its root is read-only. Use /var, a path below /var, or one of %s",
					i, target, strings.Join(persistableAliases, ", "))})
		case r.ComposeFsBackend && target == "/var":
			ps = append(ps, Problem{field, CodeCustomMountNotPersistable,
				fmt.Sprintf("customMounts[%d]: a separate /var is not supported on composefs-native targets (composeFsBackend): "+
					"the initramfs mounts the deployment's own /var, so the partition would not be used "+
					"(see https://github.com/tuna-os/fisherman/issues/252). Mount it below /var, for example /var/home", i)})
		case r.ComposeFsBackend && alias:
			ps = append(ps, Problem{field, CodeCustomMountNotPersistable,
				fmt.Sprintf("customMounts[%d]: target %s cannot be resolved on a composefs-native target (composeFsBackend); "+
					"name the path below /var that the image links it to (for example /var/home for /home)", i, target)})
		}
	}
	return ps
}
