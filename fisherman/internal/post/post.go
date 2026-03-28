package post

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

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
		if err := luks.Close(c.luksMapper); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing LUKS device %s: %v\n", c.luksMapper, err)
		}
	}
}

// deploymentDir returns the ostree deployment directory inside sysroot using
// `ostree admin --sysroot=<sysroot> --print-current-dir`.
func deploymentDir(sysroot string) (string, error) {
	out, err := exec.Command("ostree", "admin", "--sysroot="+sysroot, "--print-current-dir").Output()
	if err != nil {
		return "", fmt.Errorf("ostree admin --print-current-dir: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("ostree admin --print-current-dir returned empty path")
	}
	return path, nil
}

// WriteHostname writes /etc/hostname into the ostree deployment inside target.
// It uses `ostree admin --print-current-dir` to locate the deployment directory,
// which is necessary because bootc installs into an ostree deployment subtree,
// not directly into the sysroot root.
func WriteHostname(target, hostname string) error {
	deployDir, err := deploymentDir(target)
	if err != nil {
		return fmt.Errorf("finding deployment dir: %w", err)
	}
	etcDir := filepath.Join(deployDir, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", etcDir, err)
	}
	hostnameFile := filepath.Join(etcDir, "hostname")
	if err := os.WriteFile(hostnameFile, []byte(hostname+"\n"), 0o644); err != nil {
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
func CopyFlatpaks(target string, wantedRefs []string) error {
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

	// Target /var/lib/flatpak is the shared var directory in ostree systems.
	dst := filepath.Join(target, "var", "lib", "flatpak")
	if err := os.MkdirAll(dst, 0o755); err != nil {
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
		cmd := exec.Command("flatpak", "install", "--system", "-y", "--noninteractive", ref)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stdout
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not install %s to system: %v\n", ref, err)
		}
	}

	// 4. Copy system flatpak directory to the target via tar pipe.
	src := "/var/lib/flatpak"
	if _, err := os.Stat(src); os.IsNotExist(err) {
		fmt.Fprintf(os.Stdout, "  no system flatpak dir (%s), skipping\n", src)
		return nil
	}

	allApps := flatpakList("--system", "--app")
	totalBytes := dirSize(src)
	progress.Substep(fmt.Sprintf("Copying %d Flatpak apps (%s)", len(allApps), humanBytes(totalBytes)))
	fmt.Fprintf(os.Stdout, "  copying flatpaks: %s → %s (%d bytes)\n", src, dst, totalBytes)

	// tar cf → countingReader → tar xf
	tarC := exec.Command("tar", "cf", "-", "-C", src, ".")
	tarX := exec.Command("tar", "xf", "-", "-C", dst)

	pr, pw := io.Pipe()
	var bytesRead atomic.Int64
	cr := &countingReader{r: pr, n: &bytesRead}

	tarC.Stdout = pw
	tarC.Stderr = os.Stdout
	tarX.Stdin = cr
	tarX.Stdout = os.Stdout
	tarX.Stderr = os.Stdout

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
	out, err := exec.Command("du", "-sb", path).Output()
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
	args := []string{"list", installFlag, "--columns=ref"}
	if typeFilter != "" {
		args = append(args, typeFilter)
	}
	out, err := exec.Command("flatpak", args...).Output()
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
