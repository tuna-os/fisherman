package post

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Exec is the executor used by this package for all host commands.
// Tests replace this with a mock; restore with runner.DefaultExecutor.
var Exec runner.Executor = runner.DefaultExecutor

// Cleanup tracks mounted filesystems and an open LUKS device so they can be
// torn down in the correct order on both success and error paths.
type Cleanup struct {
	mounts     []string
	luksMapper string
	done       bool
}

func (c *Cleanup) AddMount(path string) { c.mounts = append(c.mounts, path) }
func (c *Cleanup) SetLUKS(name string)  { c.luksMapper = name }

// Run unmounts all registered mount points in reverse order, then closes any
// open LUKS device. It is idempotent.
func (c *Cleanup) Run() {
	if c.done {
		return
	}
	c.done = true
	for i := len(c.mounts) - 1; i >= 0; i-- {
		mp := c.mounts[i]
		if err := runner.Run("umount", "-R", mp); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unmounting %s: %v\n", mp, err)
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
		}
	}
}

// DefaultDeploymentDir returns the ostree deployment directory inside sysroot
// using `ostree admin --sysroot=<sysroot> --print-current-dir`.
func DefaultDeploymentDir(sysroot string) (string, error) {
	out, err := Exec.Command("ostree", "admin", "--sysroot="+sysroot, "--print-current-dir").Output()
	if err != nil {
		return "", fmt.Errorf("ostree admin --print-current-dir: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("ostree admin --print-current-dir returned empty path")
	}
	return path, nil
}

// DeploymentDirFn is called by WriteHostname to locate the ostree deployment
// directory. Tests replace this with a stub; restore with post.DefaultDeploymentDir.
var DeploymentDirFn = DefaultDeploymentDir

// isComposeFsNative reports whether the installed system at sysroot uses the
// composefs-native backend. Composefs-native deployments have no /ostree/
// directory; ostree-based deployments always create one.
func isComposeFsNative(sysroot string) bool {
	// Use ls via runner to check existence, as os.Stat might look in the sandbox.
	err := runner.Run("ls", filepath.Join(sysroot, "ostree"))
	return err != nil
}

// WriteHostname writes /etc/hostname into the installed system at target.
// For ostree-based deployments the hostname goes into the ostree deployment
// subtree (found via DeploymentDirFn). For composefs-native deployments it goes
// directly at $TARGET/etc/hostname.
func WriteHostname(target, hostname string) error {
	var etcDir string
	if isComposeFsNative(target) {
		etcDir = filepath.Join(target, "etc")
	} else {
		deployDir, err := DeploymentDirFn(target)
		if err != nil {
			return fmt.Errorf("finding deployment dir: %w", err)
		}
		etcDir = filepath.Join(deployDir, "etc")
	}
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

// fallbackFlatpaks is the core set installed when the recipe doesn't specify
// a per-image list. Ensures a usable system with browser and terminal.
var fallbackFlatpaks = []string{
	"org.mozilla.firefox",
	"org.gnome.Console",
	"org.gnome.TextEditor",
}

// CopyFlatpaks copies flatpaks to the installed system.
// If wantedRefs is non-empty, only those app IDs are installed to the target
// (downloading any that aren't cached locally). If empty, the fallback core
// set is used. In either case, any locally-available system flatpaks matching
// the wanted set are copied via tar rather than re-downloaded.
//
// flatpakVarPath optionally overrides the relative path within target where
// the writable /var lives (e.g. "state/os/default/var" for GnomeOS/Dakota).
// When empty, auto-detection is used: composefs-native → "state/os/default/var",
// ostree → "ostree/deploy/default/var".
func CopyFlatpaks(target string, wantedRefs []string, flatpakVarPath string) error {
	// Resolve wanted list: use recipe's per-image list, or fall back to defaults.
	if len(wantedRefs) == 0 {
		wantedRefs = fallbackFlatpaks
		progress.Substep(fmt.Sprintf("No per-image flatpak list; using %d fallback apps", len(wantedRefs)))
	} else {
		progress.Substep(fmt.Sprintf("Installing %d per-image flatpak apps", len(wantedRefs)))
	}
	wantedSet := make(map[string]bool, len(wantedRefs))
	for _, ref := range wantedRefs {
		wantedSet[ref] = true
	}

	// Target /var/lib/flatpak: resolve the writable var path.
	// Priority: explicit flatpakVarPath from recipe > auto-detect from layout.
	// composefs-native (GnomeOS/Dakota): state/os/default/var
	// ostree/bootc standard:             ostree/deploy/default/var
	var dst string
	switch {
	case flatpakVarPath != "":
		dst = filepath.Join(target, flatpakVarPath, "lib", "flatpak")
	case isComposeFsNative(target):
		dst = filepath.Join(target, "state", "os", "default", "var", "lib", "flatpak")
	default:
		dst = filepath.Join(target, "ostree", "deploy", "default", "var", "lib", "flatpak")
	}
	if err := runner.Run("mkdir", "-p", dst); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}

	// 1. Collect all system-installed flatpak refs (apps + runtimes).
	sysApps := flatpakList("--system", "--app")
	sysAll := flatpakList("--system", "")
	userApps := flatpakList("--user", "--app")

	// Filter to only wanted apps.
	var wantedSysApps []string
	for _, r := range sysApps {
		if wantedSet[flatpakAppName(r)] {
			wantedSysApps = append(wantedSysApps, r)
		}
	}

	if len(sysAll) == 0 && len(userApps) == 0 {
		fmt.Fprintln(os.Stdout, "  no flatpaks installed locally; wanted list will be downloaded")
	}

	// Report what we found.
	progress.Substep(fmt.Sprintf("Found %d/%d wanted apps in system install", len(wantedSysApps), len(wantedRefs)))

	// 2. Figure out which user-only app refs are not in the system install,
	//    filtered to only wanted apps.
	sysSet := make(map[string]bool, len(sysAll))
	for _, r := range sysAll {
		sysSet[r] = true
	}
	var userOnly []string
	for _, r := range userApps {
		if !sysSet[r] && wantedSet[flatpakAppName(r)] {
			userOnly = append(userOnly, r)
		}
	}

	// 3. Install user-only app refs into the system repo so tar picks them up.
	for i, ref := range userOnly {
		name := flatpakAppName(ref)
		progress.Substep(fmt.Sprintf("Promoting user app %d/%d: %s", i+1, len(userOnly), name))
		fmt.Fprintf(os.Stdout, "  installing user flatpak to system: %s\n", ref)
		if err := Exec.Command("flatpak", "install", "--system", "-y", "--noninteractive", ref).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not install %s to system: %v\n", ref, err)
		}
	}

	// 4. Copy system flatpak directory to the target via tar pipe.
	src := "/var/lib/flatpak"
	totalBytes := dirSize(src)
	if totalBytes == 0 {
		fmt.Fprintf(os.Stdout, "  no system flatpak data found at %s, skipping copy\n", src)
		return nil
	}

	allApps := flatpakList("--system", "--app")
	// Emit a substep for each wanted app so the UI shows individual names.
	copiedNames := make([]string, 0, len(wantedRefs))
	for _, ref := range allApps {
		name := flatpakAppName(ref)
		if wantedSet[name] {
			copiedNames = append(copiedNames, name)
		}
	}
	for i, name := range copiedNames {
		progress.Substep(fmt.Sprintf("Copying app %d/%d: %s", i+1, len(copiedNames), name))
	}
	if len(copiedNames) == 0 {
		progress.Substep(fmt.Sprintf("Copying %d Flatpak apps (%s)", len(allApps), humanBytes(totalBytes)))
	}
	fmt.Fprintf(os.Stdout, "  copying flatpaks: %s → %s (%d bytes)\n", src, dst, totalBytes)

	// tar cf → countingReader → tar xf
	tarC := Exec.Command("tar", "cf", "-", "-C", src, ".")
	tarX := Exec.Command("tar", "xf", "-", "-C", dst)

	pr, pw := io.Pipe()
	var bytesRead atomic.Int64
	cr := &countingReader{r: pr, n: &bytesRead}

	tarC.SetStdout(pw)
	tarC.SetStderr(os.Stdout)
	tarX.SetStdin(cr)
	tarX.SetStdout(os.Stdout)
	tarX.SetStderr(os.Stdout)

	if err := tarX.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("tar extract start: %w", err)
	}
	if err := tarC.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("tar create start: %w", err)
	}

	// Progress reporter goroutine.
	stopProgress := make(chan struct{})
	go func() {
		lastPct := -1
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				if totalBytes > 0 {
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
		}
	}()

	// Wait for tar create to finish, then close the pipe so tar extract sees EOF.
	errC := tarC.Wait()
	pw.Close()
	errX := tarX.Wait()
	close(stopProgress)

	if errC != nil {
		return fmt.Errorf("tar create: %w", errC)
	}
	if errX != nil {
		return fmt.Errorf("tar extract: %w", errX)
	}

	fmt.Fprintf(os.Stdout, "  copied %d apps (%d promoted from user)\n",
		len(allApps), len(userOnly))
	progress.Substep(fmt.Sprintf("Copied %d Flatpak apps", len(allApps)))
	return nil
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
