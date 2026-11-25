// Package postfix ports Utah's iso/raw/src/usr/libexec/utah-sysinstall-post
// (projectbluefin/utah, branch feat/gnome-setup-installer) to testable Go:
// after the medium is block-copied to the target, every copied filesystem
// gets a new UUID and every boot-config reference to an old one is
// rewritten, the live entry goes, rd.luks.uuid/crypttab land, the root
// grows, and a firmware boot entry is added.
//
// The file fixups operate on a target root directory and are pure: tests run
// them against fixture trees with no root. Anything that needs privilege
// (new filesystem UUIDs, resize, efibootmgr) goes through Runner, which
// tests fake.
package postfix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LiveEntry is the boot entry name of the live medium, and LiveKarg the
// kernel argument that marks live boots. Both must be gone afterwards.
const (
	LiveEntry = "utah-live"
	LiveKarg  = "utah.live"
)

// Runner runs a privileged helper (btrfs resize, efibootmgr). It must not
// log its arguments. The production implementation shells out; tests fake it.
type Runner func(ctx context.Context, name string, args ...string) error

// Target is the mounted target system. Boot is empty on the ESP-only
// (systemd-boot) layout, where the entries live directly on EFI.
type Target struct {
	Root string // mounted target root (deployments, crypttab)
	Boot string // mounted /boot; empty when the ESP holds the entries
	EFI  string // mounted ESP
}

// entriesDir is where the BLS entries live for the layout.
func (t Target) entriesDir() string {
	if t.Boot != "" {
		return filepath.Join(t.Boot, "loader", "entries")
	}
	return filepath.Join(t.EFI, "loader", "entries")
}

// Options carries one Apply run.
type Options struct {
	Target
	// UUIDMap maps each copied filesystem UUID to its fresh replacement.
	// The privileged step fills it before Apply runs.
	UUIDMap map[string]string
	// LUKSUUID is the target root LUKS UUID for rd.luks.uuid/crypttab.
	LUKSUUID string
	// TPM2 selects tpm2-device=auto in the unlock options.
	TPM2 bool
	// Disk and ESPPart feed the firmware boot entry (efibootmgr --disk and
	// --part). Empty Disk skips it. BootLabel is the entry label,
	// "TunaOS" when empty.
	Disk      string
	ESPPArt   string
	BootLabel string
	// Grow resizes the target root to its partition. RootFS names its
	// filesystem (btrfs when empty, xfs, ext4); ext4 also needs RootDev, the
	// unlocked root block device, because resize2fs works on the device.
	Grow    bool
	RootFS  string
	RootDev string
	// LiveEntry and LiveKarg override the live boot entry name and kernel
	// argument. The empty value means the Utah ones above; the tunaOS medium
	// uses MediumLiveEntry and MediumLiveKarg.
	LiveEntry string
	LiveKarg  string
}

// The tunaOS installer medium's live entry and kernel argument
// (installer-medium/add-live-entry.sh in tuna-os/tunaOS).
const (
	MediumLiveEntry = "medium-live"
	MediumLiveKarg  = "tunaos.live"
)

// Report is what Apply changed, for the install log.
type Report struct {
	Rewrote   []string // files with UUID references rewritten, target-relative
	Entries   []string // boot entries that gained rd.luks.uuid
	Crypttabs []string // deployments with a new crypttab
	Loader    string   // firmware boot entry loader, empty when skipped
	Removed   []string // medium-only files removed, target-relative
}

// luksOptions is the rd.luks.options/crypttab unlock set: discard always,
// tpm2-device=auto when a TPM2 token is enrolled.
func luksOptions(tpm2 bool) string {
	if tpm2 {
		return "tpm2-device=auto,discard"
	}
	return "discard"
}

// FindDeployments returns the deployment roots under root: ostree
// (/ostree/deploy/<stateroot>/deploy/<version>) and composefs
// (/state/deploy/<digest>) layouts. Each must carry etc/.
func FindDeployments(root string) ([]string, error) {
	var out []string
	ostree, _ := filepath.Glob(filepath.Join(root, "ostree", "deploy", "*", "deploy", "*"))
	composefs, _ := filepath.Glob(filepath.Join(root, "state", "deploy", "*"))
	for _, d := range append(ostree, composefs...) {
		fi, err := os.Stat(filepath.Join(d, "etc"))
		if err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("postfix: no ostree or composefs deployment under %s", root)
	}
	return out, nil
}

// CheckMachineID refuses a medium that baked in a machine-id: every copy
// would share it. A pristine medium carries "uninitialized" or nothing.
func CheckMachineID(deployment string) error {
	b, err := os.ReadFile(filepath.Join(deployment, "etc", "machine-id"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if mid := strings.TrimSpace(string(b)); mid != "" && mid != "uninitialized" {
		return fmt.Errorf("postfix: medium carries a machine-id (%s); it is not pristine", mid)
	}
	return nil
}

// bootFiles lists the boot configs that may name old UUIDs: every BLS
// entry, the GRUB configs on the ESP and in /boot, and each deployment's
// fstab when present. Missing files are skipped, so the ESP-only layout
// without GRUB or fstab works.
func bootFiles(t Target, deployments []string) []string {
	var out []string
	entries, _ := filepath.Glob(filepath.Join(t.entriesDir(), "*.conf"))
	out = append(out, entries...)
	grubcfgs, _ := filepath.Glob(filepath.Join(t.EFI, "EFI", "*", "*.cfg"))
	out = append(out, grubcfgs...)
	if t.Boot != "" {
		bootcfgs, _ := filepath.Glob(filepath.Join(t.Boot, "grub2", "*.cfg"))
		out = append(out, bootcfgs...)
	}
	for _, d := range deployments {
		if f := filepath.Join(d, "etc", "fstab"); isFile(f) {
			out = append(out, f)
		}
	}
	return out
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// RewriteRefs replaces every old UUID with its fresh one in files and
// returns the files that changed. It writes nothing when nothing matches.
func RewriteRefs(files []string, uuidMap map[string]string) ([]string, error) {
	var changed []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s := string(b)
		for old, fresh := range uuidMap {
			s = strings.ReplaceAll(s, old, fresh)
		}
		if s == string(b) {
			continue
		}
		fi, err := os.Stat(f)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(f, []byte(s), fi.Mode().Perm()); err != nil {
			return nil, err
		}
		changed = append(changed, f)
	}
	return changed, nil
}

// RemoveLiveEntry deletes the live boot entry and fails when any entry
// still carries the live kernel argument.
func RemoveLiveEntry(entriesDir, liveEntry, liveKarg string) error {
	if err := os.Remove(filepath.Join(entriesDir, liveEntry+".conf")); err != nil && !os.IsNotExist(err) {
		return err
	}
	matches, err := grepWord(entriesDir, liveKarg)
	if err != nil {
		return err
	}
	if len(matches) > 0 {
		return fmt.Errorf("postfix: a boot entry still carries %s: %s", liveKarg, strings.Join(matches, ", "))
	}
	return nil
}

// grepWord returns the *.conf files under dir containing word as a
// whitespace-delimited token.
func grepWord(dir, word string) ([]string, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.conf"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		b, err := os.ReadFile(e)
		if err != nil {
			return nil, err
		}
		for _, tok := range strings.Fields(string(b)) {
			if tok == word {
				out = append(out, e)
				break
			}
		}
	}
	return out, nil
}

// ScrubGrubenv drops the live saved_entry from the GRUB environment block
// and repads it to 1024 bytes. It reports whether it changed anything.
func ScrubGrubenv(grubenvPath, liveEntry string) (bool, error) {
	b, err := os.ReadFile(grubenvPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var keep []string
	changed := false
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if l == "saved_entry="+liveEntry {
			changed = true
			continue
		}
		keep = append(keep, l)
	}
	if !changed {
		return false, nil
	}
	body := "# GRUB Environment Block\n"
	for _, l := range keep {
		body += l + "\n"
	}
	if len(body) > 1024 {
		return false, fmt.Errorf("postfix: grubenv overflow after scrubbing %s", grubenvPath)
	}
	if err := os.WriteFile(grubenvPath, []byte(body+strings.Repeat("#", 1024-len(body))), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureLUKSOpts appends rd.luks.uuid and rd.luks.options to every options
// line of every entry that lacks them, and returns the entries it touched.
// It never duplicates: a second run changes nothing. Entries without a
// root= karg (gpt-auto, the dakota#1920 case) are fixed up the same way.
func EnsureLUKSOpts(entriesDir, luksUUID string, tpm2 bool) ([]string, error) {
	entries, err := filepath.Glob(filepath.Join(entriesDir, "*.conf"))
	if err != nil {
		return nil, err
	}
	suffix := fmt.Sprintf(" rd.luks.uuid=%s rd.luks.options=%s=%s", luksUUID, luksUUID, luksOptions(tpm2))
	var touched []string
	for _, e := range entries {
		b, err := os.ReadFile(e)
		if err != nil {
			return nil, err
		}
		var out []string
		changed := false
		for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			if strings.HasPrefix(l, "options ") && !strings.Contains(l, "rd.luks.uuid=") {
				l += suffix
				changed = true
			}
			out = append(out, l)
		}
		if !changed {
			continue
		}
		fi, err := os.Stat(e)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(e, []byte(strings.Join(out, "\n")+"\n"), fi.Mode().Perm()); err != nil {
			return nil, err
		}
		touched = append(touched, e)
	}
	return touched, nil
}

// WriteCrypttab writes the LUKS unlock line into a deployment's /etc/crypttab.
func WriteCrypttab(deployment, luksUUID string, tpm2 bool) error {
	line := fmt.Sprintf("luks-%s UUID=%s none %s\n", luksUUID, luksUUID, luksOptions(tpm2))
	return os.WriteFile(filepath.Join(deployment, "etc", "crypttab"), []byte(line), 0o644)
}

// ShimLoader returns the efibootmgr loader path (backslashes) for shim,
// skipping EFI/BOOT fallbacks. Empty means no shim: skip the firmware entry
// (the systemd-boot layout boots without one).
func ShimLoader(efiDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(efiDir, "EFI", "*", "shim*.efi"))
	if err != nil {
		return "", err
	}
	for _, m := range matches {
		rel, err := filepath.Rel(efiDir, m)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(rel, filepath.Join("EFI", "BOOT")) {
			continue
		}
		return "\\" + strings.ReplaceAll(rel, "/", "\\"), nil
	}
	return "", nil
}

// mediumSysext is the medium-only sysext tunaOS places under
// /var/lib/extensions on the medium (installer-medium/sysext/place-sysext.sh).
const mediumSysext = "medium-sysinstall.raw"

// RemoveMediumExtras deletes what only the live medium carries: the live
// kernel and initrd copy on the ESP (GRUB cells, EFI/tunaos-live) and the
// medium sysext. The sysext sits in /var/lib/extensions of the physical
// root, of an ostree stateroot, or of a composefs state dir, so every
// candidate is tried. A path that is absent is not an error. The result
// lists what was removed, relative to root.
func RemoveMediumExtras(root, efi string) ([]string, error) {
	var gone []string
	rm := func(p string) error {
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			gone = append(gone, rel)
		} else {
			gone = append(gone, p)
		}
		return nil
	}
	if efi != "" {
		if err := rm(filepath.Join(efi, "EFI", "tunaos-live")); err != nil {
			return gone, err
		}
	}
	candidates := []string{filepath.Join(root, "var", "lib", "extensions", mediumSysext)}
	for _, pat := range []string{
		filepath.Join(root, "ostree", "deploy", "*", "var", "lib", "extensions", mediumSysext),
		filepath.Join(root, "state", "os", "*", "var", "lib", "extensions", mediumSysext),
		filepath.Join(root, "state", "deploy", "*", "var", "lib", "extensions", mediumSysext),
	} {
		m, _ := filepath.Glob(pat)
		candidates = append(candidates, m...)
	}
	for _, c := range candidates {
		if err := rm(c); err != nil {
			return gone, err
		}
	}
	return gone, nil
}

// Apply runs the target-root fixups: machine-id check, UUID reference
// rewrite, live entry removal, grubenv scrub, rd.luks.uuid/crypttab, root
// grow and the firmware boot entry. The privileged filesystem-UUID step
// fills opts.UUIDMap before Apply runs.
func Apply(ctx context.Context, opts Options, run Runner) (Report, error) {
	var rep Report
	if opts.LUKSUUID == "" {
		return rep, fmt.Errorf("postfix: no LUKS UUID")
	}
	deployments, err := FindDeployments(opts.Root)
	if err != nil {
		return rep, err
	}
	for _, d := range deployments {
		if err := CheckMachineID(d); err != nil {
			return rep, err
		}
	}
	changed, err := RewriteRefs(bootFiles(opts.Target, deployments), opts.UUIDMap)
	if err != nil {
		return rep, err
	}
	for _, f := range changed {
		if rel, err := filepath.Rel(opts.Root, f); err == nil {
			rep.Rewrote = append(rep.Rewrote, rel)
		} else {
			rep.Rewrote = append(rep.Rewrote, f)
		}
	}
	liveEntry, liveKarg := LiveEntry, LiveKarg
	if opts.LiveEntry != "" {
		liveEntry = opts.LiveEntry
	}
	if opts.LiveKarg != "" {
		liveKarg = opts.LiveKarg
	}
	if err := RemoveLiveEntry(opts.entriesDir(), liveEntry, liveKarg); err != nil {
		return rep, err
	}
	grubenv := filepath.Join(opts.EFI, "grub2", "grubenv")
	if opts.Boot != "" {
		grubenv = filepath.Join(opts.Boot, "grub2", "grubenv")
	}
	if _, err := ScrubGrubenv(grubenv, liveEntry); err != nil {
		return rep, err
	}
	touched, err := EnsureLUKSOpts(opts.entriesDir(), opts.LUKSUUID, opts.TPM2)
	if err != nil {
		return rep, err
	}
	for _, e := range touched {
		if rel, err := filepath.Rel(opts.Root, e); err == nil {
			rep.Entries = append(rep.Entries, rel)
		} else {
			rep.Entries = append(rep.Entries, e)
		}
	}
	for _, d := range deployments {
		if err := WriteCrypttab(d, opts.LUKSUUID, opts.TPM2); err != nil {
			return rep, err
		}
		if rel, err := filepath.Rel(opts.Root, filepath.Join(d, "etc", "crypttab")); err == nil {
			rep.Crypttabs = append(rep.Crypttabs, rel)
		}
	}
	removed, err := RemoveMediumExtras(opts.Root, opts.EFI)
	if err != nil {
		return rep, err
	}
	rep.Removed = removed
	if opts.Grow {
		if err := growRoot(ctx, opts, run); err != nil {
			return rep, err
		}
	}
	loader, err := ShimLoader(opts.EFI)
	if err != nil {
		return rep, err
	}
	rep.Loader = loader
	label := opts.BootLabel
	if label == "" {
		label = "TunaOS"
	}
	if loader != "" && opts.Disk != "" && opts.ESPPArt != "" {
		if err := run(ctx, "efibootmgr", "--create", "--disk", opts.Disk,
			"--part", opts.ESPPArt, "--label", label, "--loader", loader); err != nil {
			return rep, fmt.Errorf("postfix: firmware boot entry: %w", err)
		}
	}
	return rep, nil
}

// growRoot grows the mounted root filesystem to its partition.
func growRoot(ctx context.Context, opts Options, run Runner) error {
	var name string
	var args []string
	switch opts.RootFS {
	case "", "btrfs":
		name, args = "btrfs", []string{"filesystem", "resize", "max", opts.Root}
	case "xfs":
		name, args = "xfs_growfs", []string{opts.Root}
	case "ext4":
		if opts.RootDev == "" {
			return fmt.Errorf("postfix: growing an ext4 root needs its device")
		}
		name, args = "resize2fs", []string{opts.RootDev}
	default:
		return fmt.Errorf("postfix: cannot grow a %s root", opts.RootFS)
	}
	if err := run(ctx, name, args...); err != nil {
		return fmt.Errorf("postfix: growing %s: %w", opts.Root, err)
	}
	return nil
}
