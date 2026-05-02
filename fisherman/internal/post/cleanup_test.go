package post_test

import (
	"io"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/runner"
)

type execCall struct {
	name string
	args []string
}

type recorder struct {
	calls []execCall
	err   error
}

func (r *recorder) run(stdin io.Reader, name string, args ...string) error {
	r.calls = append(r.calls, execCall{name: name, args: args})
	return r.err
}

func setupRecorder(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	runner.RunFn = rec.run
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })
	return rec
}

// TestCleanup_Empty verifies that Cleanup.Run() with no mounts or LUKS is a no-op.
func TestCleanup_Empty(t *testing.T) {
	rec := setupRecorder(t)

	var c post.Cleanup
	c.Run()

	if len(rec.calls) != 0 {
		t.Errorf("expected no calls for empty Cleanup, got %d: %v", len(rec.calls), rec.calls)
	}
}

// TestCleanup_MountsInReverseOrder verifies that mounts are unmounted in reverse
// registration order (LIFO), matching the mount hierarchy (deepest first).
func TestCleanup_MountsInReverseOrder(t *testing.T) {
	rec := setupRecorder(t)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.AddMount("/mnt/target/boot")
	c.AddMount("/mnt/target/boot/efi")

	c.Run()

	// Filter to umount calls only.
	var umounts []execCall
	for _, call := range rec.calls {
		if call.name == "umount" {
			umounts = append(umounts, call)
		}
	}

	if len(umounts) != 3 {
		t.Fatalf("expected 3 umount calls, got %d: %v", len(umounts), rec.calls)
	}

	// Reverse order: efi first, then boot, then target.
	wantOrder := []string{
		"/mnt/target/boot/efi",
		"/mnt/target/boot",
		"/mnt/target",
	}
	for i, want := range wantOrder {
		got := umounts[i].args[len(umounts[i].args)-1] // last arg is the mount point
		if got != want {
			t.Errorf("umount[%d] = %q, want %q (must be reverse-order)", i, got, want)
		}
	}
}

// TestCleanup_UmountArgs verifies the exact arguments passed to umount.
func TestCleanup_UmountArgs(t *testing.T) {
	rec := setupRecorder(t)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.Run()

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(rec.calls))
	}
	call := rec.calls[0]
	if call.name != "umount" {
		t.Errorf("name = %q, want umount", call.name)
	}
	// Cleanup uses -R (recursive) to handle bind mounts and submounts.
	if len(call.args) < 2 || call.args[0] != "-R" {
		t.Errorf("args = %v, want [-R <path>]", call.args)
	}
}

// TestCleanup_Idempotent verifies that calling Run() twice only unmounts once.
func TestCleanup_Idempotent(t *testing.T) {
	rec := setupRecorder(t)

	var c post.Cleanup
	c.AddMount("/mnt/target")

	c.Run() // first call — should emit umount
	firstCount := len(rec.calls)

	c.Run() // second call — must be a no-op

	if len(rec.calls) != firstCount {
		t.Errorf("second Run() emitted %d additional calls (must be idempotent)",
			len(rec.calls)-firstCount)
	}
}

// TestCleanup_WithLUKS verifies that a registered LUKS mapper is closed after
// all mounts are unmounted, with proper I/O flushing and reference release.
func TestCleanup_WithLUKS(t *testing.T) {
	rec := setupRecorder(t)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.SetLUKS("fisherman-root")

	c.Run()

	// Expect: umount /mnt/target, fuser -km, blockdev --flushbufs, udevadm settle,
	// then cryptsetup luksClose fisherman-root.
	if len(rec.calls) < 5 {
		t.Fatalf("expected at least 5 calls, got %d: %v", len(rec.calls), rec.calls)
	}

	umount := rec.calls[0]
	if umount.name != "umount" {
		t.Errorf("first call: name = %q, want umount", umount.name)
	}

	// Verify cleanup sequence before luksClose
	callNames := make([]string, len(rec.calls))
	for i, call := range rec.calls {
		callNames[i] = call.name
	}

	// Should have fuser, blockdev, udevadm before cryptsetup
	var fuserIdx, blockdevIdx, udevadmIdx, cryptsetupIdx int
	for i, name := range callNames {
		if name == "fuser" {
			fuserIdx = i
		}
		if name == "blockdev" {
			blockdevIdx = i
		}
		if name == "udevadm" {
			udevadmIdx = i
		}
		if name == "cryptsetup" {
			cryptsetupIdx = i
		}
	}

	if fuserIdx == 0 || blockdevIdx == 0 || udevadmIdx == 0 || cryptsetupIdx == 0 {
		t.Errorf("missing cleanup calls: fuser=%d blockdev=%d udevadm=%d cryptsetup=%d",
			fuserIdx, blockdevIdx, udevadmIdx, cryptsetupIdx)
	}
	if fuserIdx > cryptsetupIdx || blockdevIdx > cryptsetupIdx || udevadmIdx > cryptsetupIdx {
		t.Errorf("cleanup sequence out of order: fuser=%d blockdev=%d udevadm=%d cryptsetup=%d (cryptsetup must be last)",
			fuserIdx, blockdevIdx, udevadmIdx, cryptsetupIdx)
	}

	luksClose := rec.calls[cryptsetupIdx]
	if len(luksClose.args) < 2 || luksClose.args[0] != "luksClose" {
		t.Errorf("cryptsetup call: args = %v, want [luksClose fisherman-root]", luksClose.args)
	}
	if luksClose.args[1] != "fisherman-root" {
		t.Errorf("luksClose mapper = %q, want fisherman-root", luksClose.args[1])
	}
}

// TestCleanup_NoLUKS verifies that no cryptsetup call is made when SetLUKS was
// not called.
func TestCleanup_NoLUKS(t *testing.T) {
	rec := setupRecorder(t)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.Run()

	for _, call := range rec.calls {
		if call.name == "cryptsetup" {
			t.Errorf("unexpected cryptsetup call when no LUKS mapper registered: %v", call)
		}
	}
}
