package post

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tuna-os/fisherman/internal/progress"
)

// SnapDataPath is the root of the snapd data directory in the live environment.
// Override in tests to point at a fixture directory.
var SnapDataPath = "/var/lib/snapd"

// snapExcludeDirs are snapd subdirectories that hold only runtime state and
// should not be copied to the installed system. snapd recreates them on first boot.
var snapExcludeDirs = []string{
	"./cache",   // download cache — large, not needed
	"./mount",   // mount-namespace info — rebuilt by snapd at boot
	"./inhibit", // snap inhibit flags — runtime
	"./cookie",  // session cookies — runtime
	"./void",    // scratch space — runtime
}

// CopySnaps copies the snapd state from the running live environment into the
// installed system so that snaps are available offline on first boot without
// re-downloading. It is intentionally parallel to CopyFlatpaks.
//
// The copy includes snap package files (.snap), assertions, state.json, and
// sequence info. Runtime-only directories (cache, mount, inhibit, cookie, void)
// are excluded. snapd reconciles loop-mount points on first boot from the
// copied state without network access.
//
// snapVarPath optionally overrides the relative path within target where the
// writable /var lives (e.g. "state/os/default/var" for GnomeOS/Dakota).
// When empty, auto-detection is used identically to CopyFlatpaks.
//
// Snap copying is skipped cleanly when no snapd data is present in the live
// environment (e.g. on distros that do not use snaps).
func CopySnaps(target, snapVarPath string) error {
	totalBytes := dirSize(SnapDataPath)
	if totalBytes == 0 {
		fmt.Fprintf(os.Stdout, "  no snapd data found at %s, skipping snap copy\n", SnapDataPath)
		return nil
	}

	// Resolve the writable /var path inside the target — same logic as CopyFlatpaks.
	var varBase string
	switch {
	case snapVarPath != "":
		varBase = filepath.Join(target, snapVarPath)
	case isComposeFsNative(target):
		varBase = filepath.Join(target, "state", "os", "default", "var")
	default:
		varBase = filepath.Join(target, "ostree", "deploy", "default", "var")
	}

	dst := filepath.Join(varBase, "lib", "snapd")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}

	snaps := snapNames(filepath.Join(SnapDataPath, "snaps"))
	if len(snaps) > 0 {
		progress.Substep(fmt.Sprintf("Copying %d snap package(s) (%s)", len(snaps), humanBytes(totalBytes)))
		for i, name := range snaps {
			progress.Substep(fmt.Sprintf("Copying snap %d/%d: %s", i+1, len(snaps), name))
		}
	} else {
		progress.Substep(fmt.Sprintf("Copying snap data (%s)", humanBytes(totalBytes)))
	}

	fmt.Fprintf(os.Stdout, "  copying snapd state: %s → %s (%d bytes)\n", SnapDataPath, dst, totalBytes)

	// Build tar exclude args then stream: tar cf → countingReader → tar xf.
	tarArgs := []string{"cf", "-", "-C", SnapDataPath}
	for _, d := range snapExcludeDirs {
		tarArgs = append(tarArgs, "--exclude="+d)
	}
	tarArgs = append(tarArgs, ".")

	tarC := Exec.Command("tar", tarArgs...)
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
						progress.Substep(fmt.Sprintf("Copying snap data: %d%%", pct))
					}
				}
			}
		}
	}()

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

	fmt.Fprintf(os.Stdout, "  snap copy complete (%d snaps)\n", len(snaps))
	progress.Substep(fmt.Sprintf("Copied %d snap(s)", len(snaps)))
	return nil
}

// snapNames returns the deduplicated snap names present in snapsDir by
// inspecting the .snap filenames (<name>_<revision>.snap).
func snapNames(snapsDir string) []string {
	entries, err := os.ReadDir(snapsDir)
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".snap") {
			continue
		}
		base := strings.TrimSuffix(n, ".snap")
		// Strip the trailing _<revision> to get the snap name.
		if idx := strings.LastIndexByte(base, '_'); idx > 0 {
			base = base[:idx]
		}
		if !seen[base] {
			seen[base] = true
			names = append(names, base)
		}
	}
	return names
}
