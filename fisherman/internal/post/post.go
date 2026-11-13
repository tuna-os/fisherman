package post

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tuna-os/fisherman/internal/deploy"
	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Exec is the executor used by this package for all host commands.
// Tests replace this with a mock; restore with runner.DefaultExecutor.
var Exec runner.Executor = runner.DefaultExecutor

// RemoveAllFn is a hook for tests to override os.RemoveAll behavior.
// Normally set to os.RemoveAll, but tests can replace it.
var RemoveAllFn = os.RemoveAll

// IsMountPointFn reports whether path is a mount point in this process's
// mount namespace. Tests replace it. It reads /proc/self/mountinfo and
// starts no subprocess, so it also works after runner.Halt, when teardown
// may run only a short list of commands.
var IsMountPointFn = isMountPoint

func isMountPoint(path string) bool {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw
		fields := strings.Fields(line)
		if len(fields) > 4 && unescapeMountinfo(fields[4]) == path {
			return true
		}
	}
	return false
}

// unescapeMountinfo undoes the kernel's octal escapes (\040 for a space,
// \011 tab, \012 newline, \134 backslash) in a mountinfo path.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Cleanup tracks mounted filesystems and an open LUKS device so they can be
// torn down in the correct order on both success and error paths.
//
// It is safe for concurrent use: a cancel signal runs Run on the signal
// goroutine while the install pipeline may still be registering resources or
// reaching its own Run on the main goroutine. Run holds the lock for the
// whole teardown, so a second caller waits for the first to finish and then
// gets the same result instead of tearing down a half-torn-down disk.
type Cleanup struct {
	mu            sync.Mutex
	mounts        []string
	targetScratch []string
	postRemovals  []string
	releases      []func() error
	luksMapper    string
	zfsPool       string
	done          bool
	err           error
}

func (c *Cleanup) AddMount(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mounts = append(c.mounts, path)
}

func (c *Cleanup) SetLUKS(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.luksMapper = name
}

// SetZFSPool registers a ZFS pool the install created and imported. Run
// exports it after the unmounts: an imported pool stays imported on the
// installer host when the process ends, a retry then fails with "pool
// already exists" or "device busy", and a pool that was never exported
// needs a forced import on the installed system's first boot (#413).
func (c *Cleanup) SetZFSPool(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.zfsPool = name
}

// ExportZFSPoolFn exports a pool. Tests replace it.
var ExportZFSPoolFn = func(pool string) error {
	err := runner.Run("zpool", "export", pool)
	if err == nil {
		return nil
	}
	// A dataset still busy after the lazy unmounts: force it. The install
	// is over either way, and an imported pool is the worse outcome.
	if ferr := runner.Run("zpool", "export", "-f", pool); ferr != nil {
		return fmt.Errorf("%w (forced: %w)", err, ferr)
	}
	return nil
}

// AddPostRemoval registers a HOST path to be removed after all unmounts and
// the LUKS device close have completed. Use this for a host-resident scratch
// directory (/var/fisherman-tmp) whose contents must stay until every
// post-install step has run, including the fatal-error path, where
// os.Exit(1) would otherwise skip a deferred RemoveAll.
//
// Never register a path inside the target here: once the target is
// unmounted the path names the host's empty mountpoint, the removal
// "succeeds", and the data stays on the installed disk. Use
// AddTargetScratch.
func (c *Cleanup) AddPostRemoval(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.postRemovals = append(c.postRemovals, path)
}

// AddRelease registers a function that releases a HOST resource the
// install created outside the target (the source reuse copy and its
// /var/tmp bind, #384). Run calls it after the LUKS close and before the
// post-removals, on success and on every failure or cancel. It must be
// safe to call when there is nothing to release.
func (c *Cleanup) AddRelease(fn func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases = append(c.releases, fn)
}

// AddTargetScratch registers a scratch directory that lives ON the target
// filesystem and is bind-mounted onto itself. It must be removed while the
// target is still mounted read-write: RemoveTargetScratch does that on the
// success path, before finalize; Run does it first thing on every other
// path, before any target unmount and before the LUKS close.
func (c *Cleanup) AddTargetScratch(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.targetScratch = append(c.targetScratch, path)
}

// RemoveTargetScratch releases the self-bind of every registered target
// scratch directory and deletes the directory. Call it when nothing uses the
// scratch any more and the target is still mounted read-write. It is
// idempotent: a directory is handled once, here or in Run.
func (c *Cleanup) RemoveTargetScratch() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removeTargetScratchLocked()
}

func (c *Cleanup) removeTargetScratchLocked() error {
	var errs []error
	for i := len(c.targetScratch) - 1; i >= 0; i-- {
		p := c.targetScratch[i]
		// Lazy and recursive, like the other unmounts: a busy bind is
		// detached from the namespace at once, so the path below names the
		// directory on the target filesystem again.
		//
		// The bind may already be gone: the GPT retag of a systemd-boot
		// install unmounts everything on the root partition and mounts the
		// root again. Then there is nothing to release, only to delete.
		if IsMountPointFn(p) {
			if err := runner.Run("umount", "-Rl", p); err != nil {
				fmt.Fprintf(os.Stderr, "warning: unmounting %s: %v\n", p, err)
				errs = append(errs, fmt.Errorf("unmounting %s: %w", p, err))
			}
		}
		if err := RemoveAllFn(p); err != nil {
			fmt.Fprintf(os.Stderr, "warning: removing %s: %v\n", p, err)
			errs = append(errs, fmt.Errorf("removing %s: %w", p, err))
		}
	}
	c.targetScratch = nil
	return errors.Join(errs...)
}

// Run removes target-resident scratch, unmounts all registered mount points
// in reverse order, exports a registered ZFS pool, then closes any open LUKS
// device, then deletes any registered host post-removal paths. It is idempotent: only the first call
// does any work, and every call returns the first call's result.
//
// Every step is attempted even when an earlier one fails. Failures are
// printed to stderr as they happen and returned joined, so the caller can
// say in its final message what was left behind.
func (c *Cleanup) Run() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return c.err
	}
	c.done = true
	var errs []error
	// Target scratch first: its filesystem is still mounted. On the success
	// path RemoveTargetScratch already ran (before finalize made the target
	// read-only) and this is a no-op.
	if err := c.removeTargetScratchLocked(); err != nil {
		errs = append(errs, err)
	}
	for i := len(c.mounts) - 1; i >= 0; i-- {
		mp := c.mounts[i]
		// Use lazy recursive unmount so that busy mounts (e.g. held by
		// container processes after bootc install) are detached immediately
		// rather than blocking the LUKS close below.  Mirrors the umount -l
		// fallback in internal/disk/partition.go:unmountAll().
		if err := runner.Run("umount", "-Rl", mp); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unmounting %s: %v\n", mp, err)
			errs = append(errs, fmt.Errorf("unmounting %s: %w", mp, err))
		}
	}
	if c.zfsPool != "" {
		// After the unmounts (the ESP is mounted inside the pool's root
		// dataset), before the LUKS close.
		if err := ExportZFSPoolFn(c.zfsPool); err != nil {
			fmt.Fprintf(os.Stderr, "warning: exporting ZFS pool %s: %v\n", c.zfsPool, err)
			errs = append(errs, fmt.Errorf("exporting ZFS pool %s: %w", c.zfsPool, err))
		}
	}
	if c.luksMapper != "" {
		// Before closing LUKS device, flush pending I/O and release device references
		// to prevent "Device or resource busy" errors. Mirrors the strategy in
		// internal/disk/partition.go:unmountAll().

		// Kill any processes still holding file descriptors on the LUKS device.
		// fuser exits non-zero when no processes are found — that is fine.
		_ = runner.Run("fuser", "-km", luks.MapperPath(c.luksMapper))

		// Flush pending I/O so the kernel can drop its internal references.
		_ = runner.Run("blockdev", "--flushbufs", luks.MapperPath(c.luksMapper))

		// Give udev and udisksd time to release all device references.
		_ = runner.Run("udevadm", "settle")

		// Now close the LUKS device.
		if err := luks.Close(c.luksMapper); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing LUKS device %s: %v\n", c.luksMapper, err)
			errs = append(errs, fmt.Errorf("closing LUKS device %s: %w", c.luksMapper, err))
		}
	}
	for i := len(c.releases) - 1; i >= 0; i-- {
		if err := c.releases[i](); err != nil {
			fmt.Fprintf(os.Stderr, "warning: releasing a host resource: %v\n", err)
			errs = append(errs, err)
		}
	}
	// Host post-removals run last: nothing reads them any more.
	for _, p := range c.postRemovals {
		if err := RemoveAllFn(p); err != nil {
			fmt.Fprintf(os.Stderr, "warning: removing %s: %v\n", p, err)
			errs = append(errs, fmt.Errorf("removing %s: %w", p, err))
		}
	}
	c.err = errors.Join(errs...)
	return c.err
}

// DefaultComposeFsDeployEtcDir finds the writable /etc directory for the
// composefs-native deployment installed at target: state/deploy/<verity>/etc,
// by the boot entry's composefs=<verity>, else the newest deployment. The
// logic is deploy.FindComposeFsEtc; this name stays for the test hook below.
func DefaultComposeFsDeployEtcDir(target string) (string, error) {
	return deploy.FindComposeFsEtc(target)
}

// ComposeFsDeployEtcDirFn is called by composefs-native post-install functions
// to locate the writable /etc for the active deployment. Tests replace this with
// a stub; restore with DefaultComposeFsDeployEtcDir.
var ComposeFsDeployEtcDirFn = DefaultComposeFsDeployEtcDir

// ComposeFsVarDir returns the writable /var directory for composefs-native
// installs. The bootc composefs-native layout bind-mounts this path at /var
// at boot.
func ComposeFsVarDir(target string) string {
	return deploy.ComposeFsVar(target)
}

// DefaultDeploymentDir returns the ostree deployment directory inside sysroot.
// It first tries `ostree --sysroot=<sysroot> admin --print-current-dir` (which
// works on a booted system). On a freshly-installed target (never booted),
// --print-current-dir exits 1 because there is no booted-deployment state;
// we fall back to walking ostree/deploy/ to find the single deployment that
// bootc install to-filesystem just created.
func DefaultDeploymentDir(sysroot string) (string, error) {
	// --sysroot is a global option that must precede the admin subcommand.
	out, err := Exec.Command("ostree", "--sysroot="+sysroot, "admin", "--print-current-dir").Output()
	if err == nil {
		path := strings.TrimSpace(string(out))
		if path != "" {
			return path, nil
		}
	}
	// Fallback: freshly-installed target has no booted-deployment state.
	// Walk ostree/deploy/*/deploy/ to find the single deployment that
	// bootc install to-filesystem just created at
	// <sysroot>/ostree/deploy/<osname>/deploy/<hash>.<n>.
	path, walkErr := deploy.FindOstreeDeployment(sysroot)
	if walkErr != nil {
		if err != nil {
			return "", fmt.Errorf("ostree admin --print-current-dir: %w (also: %w)", err, walkErr)
		}
		return "", walkErr
	}
	return path, nil
}

// DeploymentDirFn is called by WriteHostname to locate the ostree deployment
// directory. Tests replace this with a stub; restore with post.DefaultDeploymentDir.
var DeploymentDirFn = DefaultDeploymentDir

// isComposeFsNative reports whether the installed system at sysroot uses the
// composefs-native backend (bootc install to-filesystem --composefs-backend).
//
// Detection is POSITIVE, via the composefs-native deploy base
// <sysroot>/state/deploy/<hash>/ (the same path DefaultComposeFsDeployEtcDir
// resolves against). The earlier heuristic keyed off "/ostree absent", but the
// composefs-native backend ALSO creates an /ostree directory — so that check
// mis-classified composefs-native installs as ostree. The concrete failure:
// WriteHostname (and every other isComposeFsNative caller) then took the ostree
// path, calling `ostree admin --print-current-dir`, which exits 1 on a
// freshly-installed --skip-finalize target, and whose glob fallback over
// /ostree/deploy/*/deploy/* finds nothing because the deployment is under
// /state/ — a fatal `finding deployment dir` crash on composefs images.
func isComposeFsNative(sysroot string) bool {
	// Use ls via runner (not os.Stat), which runs in the host mount namespace
	// rather than any sandbox. composefs-native ⟺ state/deploy exists.
	if err := runner.Run("ls", filepath.Join(sysroot, "state", "deploy")); err == nil {
		return true
	}
	// Legacy signal retained: a deployment with no /ostree at all is composefs.
	return runner.Run("ls", filepath.Join(sysroot, "ostree")) != nil
}

// IsComposeFsNativeExported is a public wrapper for isComposeFsNative, for
// callers outside this package that need to determine the target system
// layout (and for this package's external tests).
func IsComposeFsNativeExported(sysroot string) bool {
	return isComposeFsNative(sysroot)
}

// bootedVar, when set, is the directory the booted system mounts at /var in
// place of the stateroot's: a recipe varDisk, mounted there during the
// install. See SetBootedVar.
var bootedVar string

// SetBootedVar tells the post-install steps that the booted system's /var
// is the filesystem mounted at dir (a separate /var disk), not the
// stateroot var the layout would give. An empty dir restores the default.
func SetBootedVar(dir string) { bootedVar = dir }

// ResolveDeployment is the single place post-install steps learn where the
// booted system's root, /etc and /var are on the mounted target. It is
// deploy.Resolve with this package's hooks (DeploymentDirFn,
// ComposeFsDeployEtcDirFn, the runner-backed layout probe) and the /var
// override of SetBootedVar.
//
// Never join a path of the installed system onto target directly: target is
// the physical sysroot, which no booted system reads.
func ResolveDeployment(target string) (*deploy.Deployment, error) {
	d, err := deploy.Resolver{
		IsComposeFs:      isComposeFsNative,
		OstreeDeployment: func(sysroot string) (string, error) { return DeploymentDirFn(sysroot) },
		ComposeFsEtc:     func(sysroot string) (string, error) { return ComposeFsDeployEtcDirFn(sysroot) },
	}.Resolve(target)
	if err != nil {
		return nil, err
	}
	if bootedVar != "" {
		d.Var = bootedVar
	}
	return d, nil
}

// WriteHostname writes /etc/hostname into the installed system at target,
// in the deployment's /etc (ResolveDeployment).
func WriteHostname(target, hostname string) error {
	d, err := ResolveDeployment(target)
	if err != nil {
		return err
	}
	etcDir := d.Etc
	if err := runner.Run("mkdir", "-p", etcDir); err != nil {
		return fmt.Errorf("mkdir %s: %w", etcDir, err)
	}
	hostnameFile := filepath.Join(etcDir, "hostname")
	data := []byte(hostname + "\n")
	if err := runner.RunWithStdin(bytes.NewReader(data), "tee", hostnameFile); err != nil {
		return fmt.Errorf("write %s: %w", hostnameFile, err)
	}
	fmt.Fprintf(os.Stdout, "  wrote hostname %q to %s\n", hostname, hostnameFile)
	return nil
}

// hostFlatpakDir is the host's system Flatpak installation. A variable so
// tests can point it at a fixture.
var hostFlatpakDir = "/var/lib/flatpak"

// bootedVarDir returns the /var the booted system mounts. It needs less
// than ResolveDeployment and does not fail: the composefs /var has a fixed
// place, and so has the ostree one once the stateroot is known.
func bootedVarDir(target string) string {
	if bootedVar != "" {
		return bootedVar
	}
	if isComposeFsNative(target) {
		return ComposeFsVarDir(target)
	}
	if d, err := ResolveDeployment(target); err == nil {
		return d.Var
	}
	// No deployment to read the stateroot from: bootc's is "default".
	return filepath.Join(target, "ostree", "deploy", deploy.DefaultStateroot, "var")
}

// flatpakDest is where CopyFlatpaks puts the Flatpak installation. A
// separate /var (a varDisk, or a manual /var partition: SetBootedVar) wins
// over flatpakVarPath: that path is relative to the target root, which is
// the deployment's own /var and not the disk the booted system mounts.
func flatpakDest(target, flatpakVarPath string) string {
	switch {
	case bootedVar != "":
		if flatpakVarPath != "" {
			progress.Info(fmt.Sprintf("Flatpaks: flatpakVarPath %q is not used: /var is a separate disk", flatpakVarPath))
		}
		return filepath.Join(bootedVar, "lib", "flatpak")
	case flatpakVarPath != "":
		return filepath.Join(target, flatpakVarPath, "lib", "flatpak")
	default:
		return filepath.Join(bootedVarDir(target), "lib", "flatpak")
	}
}

// CopyFlatpaks copies the host's system Flatpak installation
// (/var/lib/flatpak) into the installed system.
//
// The recipe's `flatpaks` field decides whether, not what:
//
//	absent (nil)   copy the host's Flatpaks
//	[]             copy nothing
//	["a.b.C", ..]  copy the host's Flatpaks. The list is NOT a filter and
//	               nothing is downloaded: the installation is one ostree
//	               repository with shared runtimes, and it is copied whole.
//	               Each listed app the host does not have is reported.
//
// Every app and runtime on the host is copied, also the ones no list
// names. Nothing is installed on the host: an earlier version ran
// `flatpak install --system` there to "promote" user apps.
//
// flatpakVarPath optionally overrides the path, relative to target, of the
// installed system's /var (e.g. "state/os/default/var"). When empty the
// deployment resolver decides. A separate /var disk wins over it.
//
// The installer's own Flatpak is removed from the copy.
func CopyFlatpaks(target string, wantedRefs []string, flatpakVarPath string) error {
	if wantedRefs != nil && len(wantedRefs) == 0 {
		progress.Info("Flatpaks: skipped, the recipe's flatpaks list is empty")
		return nil
	}

	dst := flatpakDest(target, flatpakVarPath)
	if err := runner.Run("mkdir", "-p", dst); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}

	src := hostFlatpakDir
	totalBytes := dirSize(src)
	if totalBytes == 0 {
		progress.Info(fmt.Sprintf("Flatpaks: skipped, the host has no system Flatpaks at %s", src))
		reportMissingFlatpaks(wantedRefs, nil)
		// The image may ship Flatpaks of its own in /var: the installer's
		// must not be among them.
		removeInstallerFlatpaks(dst)
		return nil
	}

	hostApps := flatpakList("--system", "--app")
	names := make([]string, 0, len(hostApps))
	for _, ref := range hostApps {
		names = append(names, flatpakAppName(ref))
	}
	if len(wantedRefs) > 0 {
		progress.Info(fmt.Sprintf(
			"Flatpaks: the recipe lists %d app(s); the list is not a filter, the host's whole Flatpak installation is copied (%d app(s), %s)",
			len(wantedRefs), len(names), humanBytes(totalBytes)))
		reportMissingFlatpaks(wantedRefs, names)
	} else {
		progress.Info(fmt.Sprintf("Flatpaks: copying the host's Flatpak installation (%d app(s), %s)",
			len(names), humanBytes(totalBytes)))
	}
	// One substep per app that is really copied, so the UI can show names.
	for i, name := range names {
		progress.Substep(fmt.Sprintf("Copying app %d/%d: %s", i+1, len(names), name))
	}
	fmt.Fprintf(os.Stdout, "  copying flatpaks: %s → %s (%d bytes)\n", src, dst, totalBytes)

	tarC := Exec.Command("tar", "cf", "-", "-C", src, ".")
	tarX := Exec.Command("tar", "xf", "-", "-C", dst)
	tarC.SetStderr(os.Stdout)
	tarX.SetStdout(os.Stdout)
	tarX.SetStderr(os.Stdout)

	var bytesRead atomic.Int64
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		lastPct := -1
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				pct := int(bytesRead.Load() * 100 / totalBytes)
				if pct > 100 {
					pct = 100
				}
				if pct > lastPct+4 {
					lastPct = pct
					progress.Substep(fmt.Sprintf("Copying Flatpak data: %d%%", pct))
				}
			}
		}
	}()
	err := pipeCommands(tarC, tarX, &bytesRead)
	close(stopProgress)
	<-progressDone
	if err != nil {
		return err
	}

	removeInstallerFlatpaks(dst)

	progress.Substep(fmt.Sprintf("Copied %d Flatpak apps", len(names)))
	return nil
}

// reportMissingFlatpaks says, for each app the recipe lists, that the host
// does not have it and so the installed system will not either.
func reportMissingFlatpaks(wanted, hostApps []string) {
	have := make(map[string]bool, len(hostApps))
	for _, n := range hostApps {
		have[n] = true
	}
	for _, w := range wanted {
		if !have[w] {
			progress.Info(fmt.Sprintf(
				"Warning: Flatpak %s is in the recipe but not on the host; it is not downloaded and will not be on the installed system", w))
		}
	}
}

// removeInstallerFlatpaks deletes all known installer Flatpak app IDs from dst
// so the installer is not present on the installed system.
func removeInstallerFlatpaks(dst string) {
	installerAppIDs := []string{
		"org.bootcinstaller.Installer",
		"org.bootcinstaller.Installer.Devel",
		"org.tunaos.Installer",
		"org.tunaos.Installer.Devel",
	}
	for _, appID := range installerAppIDs {
		// Remove app files
		appDir := filepath.Join(dst, "app", appID)
		if err := os.RemoveAll(appDir); err == nil {
			fmt.Fprintf(os.Stdout, "  removed installer app files for %s\n", appID)
		}
		// Remove exported desktop entry
		desktopFile := filepath.Join(dst, "exports", "share", "applications", appID+".desktop")
		if err := os.Remove(desktopFile); err == nil {
			fmt.Fprintf(os.Stdout, "  removed installer desktop entry for %s\n", appID)
		}
		// Remove exported dbus service
		dbusFile := filepath.Join(dst, "exports", "share", "dbus-1", "services", appID+".service")
		_ = os.Remove(dbusFile)
	}
}

// countingReader wraps an io.Reader and atomically counts bytes read.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// dirSize returns the total size in bytes of a directory tree.
func dirSize(path string) int64 {
	// Fast path: use du -sb which handles hardlinks correctly.
	out, err := Exec.Command("du", "-sb", path).Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.ParseInt(fields[0], 10, 64)
	return n
}

// humanBytes formats a byte count as a human-readable string.
func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// flatpakAppName extracts a short display name from a flatpak ref like
// "org.mozilla.Firefox/x86_64/stable" → "org.mozilla.Firefox".
func flatpakAppName(ref string) string {
	if idx := strings.IndexByte(ref, '/'); idx > 0 {
		return ref[:idx]
	}
	return ref
}

// flatpakList returns installed flatpak refs for the given installation flag
// (--system or --user) and optional type filter (--app, --runtime, or "").
func flatpakList(installFlag, typeFilter string) []string {
	fargs := []string{"list", installFlag, "--columns=ref"}
	if typeFilter != "" {
		fargs = append(fargs, typeFilter)
	}
	out, err := Exec.Command("flatpak", fargs...).Output()
	if err != nil {
		return nil
	}
	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && line != "Ref" {
			refs = append(refs, line)
		}
	}
	return refs
}

// Host sources of CopyBluetoothPairings and CopyWiFiConnections. Variables
// so tests can point them at a fixture.
var (
	hostBluetoothDir     = "/var/lib/bluetooth"
	hostNMConnectionsDir = "/etc/NetworkManager/system-connections"
)

// CopyBluetoothPairings copies the host's Bluetooth pairing data
// (/var/lib/bluetooth, which holds the link keys) into the installed
// system's /var, so paired keyboards and mice reconnect on first boot.
//
// It returns the number of adapters copied. Zero with a nil error means
// there was nothing to copy; the caller reports that as skipped.
//
// The caller decides WHETHER to copy (live media, or the recipe's
// copyBluetoothPairings): on an installed host these are the operator's
// keys, and the target may be for another person (#411).
func CopyBluetoothPairings(target string) (int, error) {
	src := hostBluetoothDir
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, nil //nolint:nilerr // no bluetooth data on the host: nothing to do
	}
	adapters := 0
	for _, e := range entries {
		if e.IsDir() {
			adapters++
		}
	}
	if adapters == 0 {
		return 0, nil
	}

	// The booted /var: the stateroot's on ostree, state/os/default/var on
	// composefs. <target>/var is the physical sysroot.
	d, err := ResolveDeployment(target)
	if err != nil {
		return 0, err
	}
	dst := filepath.Join(d.Var, "lib", "bluetooth")
	if err := runner.Run("mkdir", "-p", dst); err != nil {
		return 0, fmt.Errorf("mkdir %s: %w", dst, err)
	}
	// The directory holds link keys: bluetoothd keeps it 0700.
	_ = os.Chmod(dst, 0o700)

	// Copy preserving permissions and ownership.
	if err := runner.Run("cp", "-a", src+"/.", dst); err != nil {
		return 0, fmt.Errorf("copying bluetooth pairings: %w", err)
	}

	// Fix SELinux context if restorecon is available.
	_ = runner.Run("restorecon", "-R", dst)

	fmt.Fprintf(os.Stdout, "  copied Bluetooth pairings to %s\n", dst)
	return adapters, nil
}

// CopyWiFiConnections copies the host's NetworkManager connection profiles
// (*.nmconnection: Wi-Fi passwords, 802.1X credentials, VPN secrets) into
// the installed system's /etc so the network reconnects on first boot.
//
// It returns the number of profiles copied. Zero with a nil error means the
// host has none; the caller reports that as skipped.
//
// The caller decides WHETHER to copy (live media, or the recipe's
// copyNetworkProfiles): see CopyBluetoothPairings.
func CopyWiFiConnections(target string) (int, error) {
	src := hostNMConnectionsDir
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, nil //nolint:nilerr // no NM connections on the host: nothing to do
	}
	var profiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".nmconnection") {
			profiles = append(profiles, e.Name())
		}
	}
	if len(profiles) == 0 {
		return 0, nil
	}

	d, err := ResolveDeployment(target)
	if err != nil {
		return 0, err
	}
	dst := filepath.Join(d.Etc, "NetworkManager", "system-connections")
	if err := runner.Run("mkdir", "-p", dst); err != nil {
		return 0, fmt.Errorf("mkdir %s: %w", dst, err)
	}

	// cp -a keeps the mode: NetworkManager ignores a profile that is not 0600.
	for _, name := range profiles {
		if err := runner.Run("cp", "-a", filepath.Join(src, name), dst+"/"); err != nil {
			return 0, fmt.Errorf("copying %s: %w", name, err)
		}
	}

	// Fix SELinux context if restorecon is available.
	_ = runner.Run("restorecon", "-R", dst)

	fmt.Fprintf(os.Stdout, "  copied %d network profile(s) to %s\n", len(profiles), dst)
	return len(profiles), nil
}

// PrintServices are the units EnablePrintServices enables: printer
// auto-discovery for a desktop. They listen on the network (cups-browsed was
// the CVE-2024-47176 vector), so the install enables them only when the
// recipe or the image catalog asks (extras.printServices), never for an
// image it knows nothing about (#412).
var PrintServices = []string{
	"cups-browsed.service",
	"avahi-daemon.service",
	"ipp-usb.service",
}

// EnablePrintServices enables the PrintServices units in the installed
// system, each one only when the installed image has it. It returns what it
// enabled and what the image does not have, and logs exactly that: a run
// that enabled nothing says "skipped".
//
// On composefs the image's /usr cannot be inspected offline. A unit is then
// enabled without the check, and the log line says so.
func EnablePrintServices(target string) (enabled, missing []string) {
	unchecked := false
	for _, svc := range PrintServices {
		switch enableSystemService(target, svc) {
		case unitEnabled:
			enabled = append(enabled, svc)
		case unitEnabledUnchecked:
			enabled = append(enabled, svc)
			unchecked = true
		case unitMissing:
			missing = append(missing, svc)
		case unitFailed:
			// already logged
		}
	}
	switch {
	case len(enabled) == 0 && len(missing) > 0:
		progress.Info("Print services: skipped, the image has none of " + strings.Join(missing, ", "))
	case len(enabled) == 0:
		progress.Info("Print services: skipped, could not enable any unit")
	default:
		msg := "Print services enabled: " + strings.Join(enabled, ", ")
		if unchecked {
			msg += " (not checked: the image's /usr cannot be read on a composefs target)"
		}
		if len(missing) > 0 {
			msg += "; not in the image: " + strings.Join(missing, ", ")
		}
		progress.Info(msg)
	}
	return enabled, missing
}

// AppendFstabEntry appends an fstab entry to the installed system at target.
// Works for both composefs-native and ostree-based deployments.
func AppendFstabEntry(target, uuid, mountpoint, fstype, options string) error {
	d, err := ResolveDeployment(target)
	if err != nil {
		return fmt.Errorf("finding the deployment for fstab: %w", err)
	}
	etcDir := d.Etc
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", etcDir, err)
	}
	if options == "" {
		options = "defaults"
	}
	fstabPath := filepath.Join(etcDir, "fstab")
	f, err := os.OpenFile(fstabPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("opening fstab %s: %w", fstabPath, err)
	}
	defer f.Close()
	entry := fmt.Sprintf("UUID=%s\t%s\t%s\t%s\t0 0\n", uuid, mountpoint, fstype, options)
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("writing fstab entry: %w", err)
	}
	return nil
}

// SetHostSecretDirs points CopyBluetoothPairings and CopyWiFiConnections at
// other host directories and returns a function that restores them. For
// tests.
func SetHostSecretDirs(bluetooth, nmConnections string) (restore func()) {
	oldBT, oldNM := hostBluetoothDir, hostNMConnectionsDir
	hostBluetoothDir, hostNMConnectionsDir = bluetooth, nmConnections
	return func() { hostBluetoothDir, hostNMConnectionsDir = oldBT, oldNM }
}
