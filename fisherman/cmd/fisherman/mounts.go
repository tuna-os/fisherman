package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// Mounts that outlive the install session (#406, #372).
//
// A varDisk and every data or swap entry of a manual layout are mounted for
// the install, and the installed system must mount them again. bootc writes
// nothing for them, so fisherman writes one fstab line each, in the
// deployment's /etc. The line carries the filesystem UUID and the type blkid
// reads from the device (a kept disk is not always XFS), and `nofail` with a
// device timeout, so a disk that is missing later delays the boot for a
// bounded time instead of ending it in emergency mode.
//
// A mount that cannot be made persistent is refused: by the recipe rules
// before any disk work where the recipe alone shows it
// (recipe.CheckCustomMountsPersistence, CheckVarDiskBackend), by
// keptFilesystemProblems before any disk work where blkid shows it, and here,
// after bootc ran, for what only the installed image shows.

const (
	// persistOptions are the fstab options of a data mount.
	persistOptions = "defaults,nofail,x-systemd.device-timeout=30s"
	// varOptions are the fstab options of /var. `nofail` alone drops the
	// Before=local-fs.target ordering, and services that write to /var
	// would race the mount; x-systemd.before puts the ordering back while
	// a failed mount still does not fail the boot.
	varOptions = persistOptions + ",x-systemd.before=local-fs.target"
	// swapOptions are the fstab options of a swap partition.
	swapOptions = "defaults,nofail"
)

// persistentMount is one fstab line fisherman owes the installed system.
type persistentMount struct {
	Field      string // recipe field, for messages: "varDisk" or "customMounts[2]"
	Device     string // block device
	Mountpoint string // on the installed system; "none" for swap
	Fstype     string // from blkid
	UUID       string // filesystem UUID, from blkid
	Options    string
}

// deployment is where the installed system keeps the files fisherman needs
// for a persistent mount.
type deployment struct {
	EtcDir string // the deployment's /etc, where fstab goes
	VarDir string // what the installed system sees as /var
	Root   string // the deployment's root filesystem; "" on composefs-native
}

// resolveDeployment finds the deployment bootc installed at target.
func resolveDeployment(target string) (deployment, error) {
	if post.IsComposeFsNativeExported(target) {
		etc, err := post.ComposeFsDeployEtcDirFn(target)
		if err != nil {
			return deployment{}, fmt.Errorf("finding the composefs deployment: %w", err)
		}
		return deployment{EtcDir: etc, VarDir: post.ComposeFsVarDir(target)}, nil
	}
	deployDir, err := post.DeploymentDirFn(target)
	if err != nil {
		return deployment{}, fmt.Errorf("finding the deployment: %w", err)
	}
	// <sysroot>/ostree/deploy/<stateroot>/deploy/<checksum>.<n>; the
	// stateroot's var is what ostree mounts at /var.
	stateroot := filepath.Dir(filepath.Dir(deployDir))
	return deployment{
		EtcDir: filepath.Join(deployDir, "etc"),
		VarDir: filepath.Join(stateroot, "var"),
		Root:   deployDir,
	}, nil
}

// resolveInRoot resolves an absolute path inside root the way the booted
// system would: it follows symlinks, and an absolute link target stays
// inside root. Components that do not exist are kept as they are. The result
// is an absolute path as the installed system sees it.
func resolveInRoot(root, path string) (string, error) {
	pending := strings.Split(strings.Trim(filepath.Clean(path), "/"), "/")
	resolved := "/"
	for hops := 0; len(pending) > 0; {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, c)
		fi, err := os.Lstat(filepath.Join(root, next))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		if hops++; hops > 40 {
			return "", fmt.Errorf("too many symbolic links in %s", path)
		}
		link, err := os.Readlink(filepath.Join(root, next))
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(link) {
			resolved = "/"
		}
		pending = append(strings.Split(link, "/"), pending...)
	}
	return resolved, nil
}

// transientEtc reports whether the ostree deployment at root discards /etc
// at every boot ([etc] transient = true in prepare-root.conf). An fstab line
// written at install time does not survive the first boot there.
func transientEtc(root string) bool {
	for _, rel := range []string{"etc/ostree/prepare-root.conf", "usr/lib/ostree/prepare-root.conf"} {
		f, err := os.Open(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		section, transient := "", false
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.TrimSpace(line[1 : len(line)-1])
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if ok && section == "etc" && strings.TrimSpace(k) == "transient" {
				transient = strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"'`), "true")
			}
		}
		f.Close()
		// /etc overrides /usr/lib: the first file that exists decides.
		return transient
	}
	return false
}

// keptFilesystemProblems checks, before any disk is changed, every device
// the recipe asks to mount without formatting: a varDisk with keepExisting,
// and customMounts entries with fstype "unformatted" or "". Each must hold a
// filesystem that can be mounted. Otherwise the mount fails after the system
// disk is wiped. It needs blkid, so it runs in the install, not in
// `fisherman validate`.
func keptFilesystemProblems(r *recipe.Recipe) []recipe.Problem {
	var ps []recipe.Problem
	if r.VarDisk != nil && r.VarDisk.KeepExisting {
		if err := disk.CheckKeptFilesystem(r.VarDisk.Disk, disk.FSType(r.VarDisk.Disk), true); err != nil {
			ps = append(ps, recipe.Problem{Field: "varDisk.disk", Code: recipe.CodeKeptFilesystemUnusable,
				Message: "varDisk.keepExisting: " + err.Error()})
		}
	}
	for i, cm := range r.CustomMounts {
		kind := recipe.ClassifyCustomMount(cm.Target)
		if kind == recipe.MountIgnored || kind == recipe.MountSwap || (cm.Fstype != "" && cm.Fstype != "unformatted") {
			continue
		}
		posix := filepath.Clean(cm.Target) == "/var"
		if err := disk.CheckKeptFilesystem(cm.Partition, disk.FSType(cm.Partition), posix); err != nil {
			ps = append(ps, recipe.Problem{Field: fmt.Sprintf("customMounts[%d].partition", i), Code: recipe.CodeKeptFilesystemUnusable,
				Message: fmt.Sprintf("customMounts[%d]: %v", i, err)})
		}
	}
	return ps
}

// planPersistentMounts lists the fstab lines the recipe needs, with the
// facts read from the devices and from the installed deployment. It changes
// nothing. An error names a mount that cannot be made persistent.
func planPersistentMounts(r *recipe.Recipe, dep deployment) ([]persistentMount, error) {
	var plan []persistentMount
	add := func(field, dev, mountpoint, options string) error {
		m := persistentMount{Field: field, Device: dev, Mountpoint: mountpoint, Options: options,
			Fstype: disk.FSType(dev), UUID: disk.UUID(dev)}
		if m.UUID == "" || m.Fstype == "" {
			return fmt.Errorf("%s: blkid reports no filesystem UUID or type for %s, so it cannot be mounted at boot", field, dev)
		}
		if mountpoint == "none" && m.Fstype != "swap" {
			return fmt.Errorf("%s: %s is not swap space (blkid: %s)", field, dev, m.Fstype)
		}
		plan = append(plan, m)
		return nil
	}
	if r.VarDisk != nil {
		if err := add("varDisk", r.VarDisk.Disk, "/var", varOptions); err != nil {
			return nil, err
		}
	}
	for i, cm := range r.CustomMounts {
		field := fmt.Sprintf("customMounts[%d]", i)
		switch recipe.ClassifyCustomMount(cm.Target) {
		case recipe.MountSwap:
			// Swap setup is not fatal during the install (ApplyCustomLayout),
			// so a partition that did not become swap is skipped, loudly.
			if cm.Fstype != "swap" {
				continue
			}
			if err := add(field, cm.Partition, "none", swapOptions); err != nil {
				progress.Info(fmt.Sprintf("Warning: swap will not be active on the installed system: %v", err))
			}
		case recipe.MountIgnored, recipe.MountBootStack:
			// Nothing to write: see recipe.MountKind.
		case recipe.MountData:
			mountpoint := filepath.Clean(cm.Target)
			if dep.Root != "" {
				resolved, err := resolveInRoot(dep.Root, mountpoint)
				if err != nil {
					return nil, fmt.Errorf("%s: resolving %s in the installed image: %w", field, mountpoint, err)
				}
				mountpoint = resolved
			}
			if !recipe.IsVarPath(mountpoint) {
				// Outside /var the root is read-only: systemd cannot create
				// the mountpoint, so the image must ship it.
				fi, err := os.Stat(filepath.Join(dep.Root, mountpoint))
				if dep.Root == "" || err != nil || !fi.IsDir() {
					return nil, fmt.Errorf("%s: %s is not below /var and is not a directory of the installed image, "+
						"so nothing can be mounted there at boot", field, mountpoint)
				}
			}
			options := persistOptions
			if mountpoint == "/var" {
				options = varOptions
			}
			if err := add(field, cm.Partition, mountpoint, options); err != nil {
				return nil, err
			}
		}
	}
	if len(plan) > 0 && dep.Root != "" && transientEtc(dep.Root) {
		return nil, fmt.Errorf("%s: the installed image discards /etc at every boot ([etc] transient in prepare-root.conf), "+
			"so an fstab entry cannot make the mount persistent", plan[0].Field)
	}
	return plan, nil
}

// fstabMounts reads an fstab and returns device spec by mountpoint. Swap
// lines are keyed by their spec, since they all mount at "none".
func fstabMounts(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		key := filepath.Clean(f[1])
		if f[2] == "swap" {
			key = "swap:" + f[0]
		}
		out[key] = f[0]
	}
	return out
}

// writePersistentMounts adds the planned lines to the deployment's fstab. A
// line that is there already (the same UUID at the same mountpoint) is left
// alone, so a second call adds nothing. A mountpoint the image's fstab
// already gives to another device is an error: fisherman does not overwrite
// mount configuration the image manages.
func writePersistentMounts(target string, dep deployment, plan []persistentMount) error {
	existing := fstabMounts(filepath.Join(dep.EtcDir, "fstab"))
	for _, m := range plan {
		spec := "UUID=" + m.UUID
		key := m.Mountpoint
		if m.Fstype == "swap" {
			key = "swap:" + spec
		}
		if have, ok := existing[key]; ok {
			if have == spec {
				continue
			}
			return fmt.Errorf("%s: the installed image's fstab already mounts %s at %s", m.Field, have, m.Mountpoint)
		}
		if err := post.AppendFstabEntry(target, m.UUID, m.Mountpoint, m.Fstype, m.Options); err != nil {
			return fmt.Errorf("%s: writing the fstab entry for %s: %w", m.Field, m.Mountpoint, err)
		}
		existing[key] = spec
		progress.Info(fmt.Sprintf("Added fstab entry: UUID=%s %s %s %s", m.UUID, m.Mountpoint, m.Fstype, m.Options))
	}
	return nil
}

// mountDataInDeployment mounts every planned /var mount a second time, where
// the installed system will see it: under the deployment's var directory.
// The post-install steps write the user's home and the Flatpaks there. On
// the first boot the fstab line mounts the device over that directory, so
// without this the files written during the install would sit on the root
// filesystem, hidden under the mount. mounted is called for each new mount.
func mountDataInDeployment(dep deployment, plan []persistentMount, mounted func(path string)) error {
	// A parent first: /var before /var/home.
	ordered := append([]persistentMount(nil), plan...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.Count(ordered[i].Mountpoint, "/") < strings.Count(ordered[j].Mountpoint, "/")
	})
	for _, m := range ordered {
		if !recipe.IsVarPath(m.Mountpoint) {
			continue
		}
		rel, _ := filepath.Rel("/var", m.Mountpoint)
		dir := filepath.Join(dep.VarDir, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("%s: creating %s: %w", m.Field, dir, err)
		}
		opts := ""
		if m.Fstype == "btrfs" {
			opts = "compress=zstd:1"
		}
		if err := disk.MountType(m.Device, dir, m.Fstype, opts); err != nil {
			return fmt.Errorf("%s: mounting %s at %s: %w", m.Field, m.Device, dir, err)
		}
		mounted(dir)
		progress.Info(fmt.Sprintf("Mounted %s at the installed system's %s", m.Device, m.Mountpoint))
	}
	return nil
}

// needsPersistentMounts reports whether the recipe has a mount fisherman
// must make persistent.
func needsPersistentMounts(r *recipe.Recipe) bool {
	if r.VarDisk != nil {
		return true
	}
	for _, cm := range r.CustomMounts {
		if k := recipe.ClassifyCustomMount(cm.Target); k == recipe.MountData || k == recipe.MountSwap {
			return true
		}
	}
	return false
}

// persistMounts runs after bootc installed the image: it plans the
// persistent mounts, mounts the /var ones where the post-install steps will
// write, and writes the fstab lines. Any error is fatal for the install: a
// mount the recipe asked for would be missing after the reboot.
func persistMounts(r *recipe.Recipe, target string, mounted func(path string)) error {
	if !needsPersistentMounts(r) {
		return nil
	}
	dep, err := resolveDeployment(target)
	if err != nil {
		return err
	}
	plan, err := planPersistentMounts(r, dep)
	if err != nil {
		return err
	}
	if err := mountDataInDeployment(dep, plan, mounted); err != nil {
		return err
	}
	return writePersistentMounts(target, dep, plan)
}
