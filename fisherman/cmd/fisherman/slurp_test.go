package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy"
	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/runner"

	"github.com/tuna-os/wootc/slurp"
)

// recordRunner swaps runner.RunFn for a recorder for the test's duration.
func recordRunner(t *testing.T) func() []string {
	t.Helper()
	orig := runner.RunFn
	var calls []string
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { runner.RunFn = orig })
	return func() []string { return calls }
}

// The adapters hand the slurp module fisherman's runner: its commands go
// through runner.RunFn/OutputFn (host wrapping, halt rules), not os/exec.
func TestSlurpAdapters_UseFishermansRunner(t *testing.T) {
	calls := recordRunner(t)
	origOut := runner.OutputFn
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		if name != "lsblk" {
			return nil, errors.New("unexpected " + name)
		}
		return []byte("/dev/sda1 vfat\n/dev/sda2 ntfs\n"), nil
	}
	t.Cleanup(func() { runner.OutputFn = origOut })

	parts, err := slurper.DetectNTFS("/dev/sda")
	if err != nil || len(parts) != 1 || parts[0] != "/dev/sda2" {
		t.Fatalf("DetectNTFS = %v, %v; want [/dev/sda2]", parts, err)
	}
	if err := (slurpRunner{}).Run("umount", "/x"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); len(got) != 1 || got[0] != "umount /x" {
		t.Errorf("runner calls = %v", got)
	}
	if slurper.ScratchBase != slurp.DefaultScratchBase || slurper.MountPoint != slurp.DefaultMountPoint ||
		slurper.ScanMountPoint != slurp.DefaultScanMountPoint {
		t.Errorf("slurper paths moved off fisherman's /run defaults: %+v", slurper)
	}
}

// The wallpapers go to the /var the booted system sees, on both layouts,
// and nothing is written into the physical sysroot (tuna-os/fisherman#407:
// <target>/var/home on ostree is never mounted).
func TestInjectWallpapers_GoesToTheBootedVar(t *testing.T) {
	for name, fx := range map[string]deploytest.Sysroot{
		"ostree":    deploytest.Ostree(t),
		"composefs": deploytest.ComposeFs(t),
	} {
		t.Run(name, func(t *testing.T) {
			d, err := deploy.Resolve(fx.Path)
			if err != nil {
				t.Fatal(err)
			}
			scratch := t.TempDir()
			if err := os.WriteFile(filepath.Join(scratch, "x.jpg"), make([]byte, 100), 0o644); err != nil {
				t.Fatal(err)
			}

			calls := recordRunner(t)
			result := &slurp.WallpaperResult{Found: true, ScratchDir: scratch, Count: 1, TotalBytes: 100}
			if err := slurper.InjectWallpapers(d.Var, result); err != nil {
				t.Fatalf("InjectWallpapers: %v", err)
			}
			bgDir := filepath.Join(fx.Var, "home", ".windows-wallpapers")
			if _, err := os.Stat(bgDir); err != nil {
				t.Errorf("wallpaper dir not created in the booted /var: %v", err)
			}
			if s := deploytest.Strays(fx); len(s) != 0 {
				t.Errorf("wrote %v into the physical sysroot", s)
			}
			joined := strings.Join(calls(), " ")
			if !strings.Contains(joined, "cp -a "+scratch+"/. "+bgDir+"/") {
				t.Errorf("expected cp -a from scratch into bgDir, calls: %s", joined)
			}
			// A shared directory belongs to root; uid 1000 is nobody yet.
			if strings.Contains(joined, "1000:1000") {
				t.Errorf("wallpapers given to a hardcoded uid: %s", joined)
			}
			if !strings.Contains(joined, "chown -R 0:0 "+bgDir) {
				t.Errorf("expected root ownership of the wallpaper dir, calls: %s", joined)
			}
		})
	}
}
