package post_test

// Disposable-filesystem evidence for tuna-os/fisherman#307: a mocked command
// order does not prove that the scratch bytes left the target. This test
// builds an ext4 filesystem in a file, mounts it as "the target", puts a
// self-bound scratch directory with data on it, tears down, mounts the
// filesystem AGAIN and looks at what is really on it.
//
// It needs root, mkfs.ext4 and a loop device, so it skips on a plain
// `go test`; CI's unit-tests job runs the suite under sudo.
// FISHERMAN_LOOPBACK_E2E=require turns the skip into a failure.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
)

func loopSkipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("FISHERMAN_LOOPBACK_E2E") == "require" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func sh(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// scratchTarget makes an ext4 image, mounts it at <tmp>/target and returns
// (image, mountpoint). The mount is released at test end if still there.
func scratchTarget(t *testing.T) (img, target string) {
	t.Helper()
	if os.Getuid() != 0 {
		loopSkipOrFail(t, "requires root: run as root or with sudo go test")
	}
	for _, tool := range []string{"mkfs.ext4", "mount", "umount"} {
		if _, err := exec.LookPath(tool); err != nil {
			loopSkipOrFail(t, "requires %s", tool)
		}
	}
	dir := t.TempDir()
	img = filepath.Join(dir, "target.img")
	f, err := os.Create(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	sh(t, "mkfs.ext4", "-q", "-F", img)
	target = filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mount", "-o", "loop", img, target).CombinedOutput(); err != nil {
		loopSkipOrFail(t, "cannot loop-mount here: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("umount", "-Rl", target).Run() })
	return img, target
}

// addScratch creates the self-bound scratch with a cache blob in it, the
// way prepareScratchDir and bootc leave it.
func addScratch(t *testing.T, target string) string {
	t.Helper()
	scratch := filepath.Join(target, ".fisherman-scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	sh(t, "mount", "--bind", scratch, scratch)
	blob := filepath.Join(scratch, "oci-cache", "blobs", "sha256")
	if err := os.MkdirAll(blob, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blob, "layer"), make([]byte, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	return scratch
}

// remaining mounts img again and reports what is under .fisherman-scratch.
func remaining(t *testing.T, img string) []string {
	t.Helper()
	again := t.TempDir()
	sh(t, "mount", "-o", "loop,ro", img, again)
	defer sh(t, "umount", again)
	var left []string
	scratch := filepath.Join(again, ".fisherman-scratch")
	_ = filepath.Walk(scratch, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			left = append(left, strings.TrimPrefix(p, again))
		}
		return nil
	})
	return left
}

// The fatal and cancel paths: Run alone, target still read-write.
func TestCleanup_TargetScratchLoopback_Run(t *testing.T) {
	img, target := scratchTarget(t)
	addScratch(t, target)

	var c post.Cleanup
	c.AddMount(target)
	c.AddTargetScratch(filepath.Join(target, ".fisherman-scratch"))
	if err := c.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if left := remaining(t, img); len(left) != 0 {
		t.Errorf("scratch is still on the target after teardown: %v", left)
	}
}

// The success path: scratch removed, THEN the target goes read-only (what
// finalize does), then teardown.
func TestCleanup_TargetScratchLoopback_BeforeFinalize(t *testing.T) {
	img, target := scratchTarget(t)
	addScratch(t, target)

	var c post.Cleanup
	c.AddMount(target)
	c.AddTargetScratch(filepath.Join(target, ".fisherman-scratch"))
	if err := c.RemoveTargetScratch(); err != nil {
		t.Fatalf("RemoveTargetScratch: %v", err)
	}
	sh(t, "mount", "-o", "remount,ro", target)
	if err := c.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if left := remaining(t, img); len(left) != 0 {
		t.Errorf("scratch is still on the target after a successful install: %v", left)
	}
}

// The old order, kept as proof of the defect: unmount first, remove after.
// The removal "succeeds" on the host's empty mountpoint and every byte of
// the scratch stays on the target.
func TestCleanup_TargetScratchLoopback_OldOrderLeaks(t *testing.T) {
	img, target := scratchTarget(t)
	scratch := addScratch(t, target)

	sh(t, "umount", "-Rl", scratch)
	sh(t, "umount", "-Rl", target)
	if err := os.RemoveAll(scratch); err != nil {
		t.Fatalf("RemoveAll after unmount: %v", err)
	}
	if left := remaining(t, img); len(left) == 0 {
		t.Fatal("the unmount-then-remove order removed the scratch; this test no longer shows the defect")
	}
}
