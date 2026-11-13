package post_test

import (
	"errors"
	"io"
	"strings"
	"sync"
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

func setupRemoveAllRecorder(t *testing.T, rec *recorder) {
	t.Helper()
	old := post.RemoveAllFn
	post.RemoveAllFn = func(path string) error {
		rec.calls = append(rec.calls, execCall{name: "removeAll", args: []string{path}})
		return nil
	}
	t.Cleanup(func() { post.RemoveAllFn = old })
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
	// Cleanup uses -Rl (recursive + lazy) to handle bind mounts and submounts
	// and detach even when a mount is busy.  Mirrors the umount -l fallback
	// in internal/disk/partition.go:unmountAll().
	if len(call.args) < 2 || call.args[0] != "-Rl" {
		t.Errorf("args = %v, want [-Rl <path>]", call.args)
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

// TestCleanup_HostPostRemovalsHappenAfterUnmountsAndLUKS verifies the
// post-removal contract for HOST paths: a path registered via AddPostRemoval
// (/var/fisherman-tmp) is deleted after every unmount and after the LUKS
// device is closed, never interleaved with them, including on the
// fatal()/os.Exit(1) error path.
//
// This test used to register <target>/.fisherman-scratch here and so encoded
// the wrong order for a target-resident path: after the target is unmounted
// that path names the host's empty mountpoint, and the scratch stays on the
// installed disk (tuna-os/fisherman#307). Target scratch has its own
// registration and its own test below.
func TestCleanup_HostPostRemovalsHappenAfterUnmountsAndLUKS(t *testing.T) {
	rec := setupRecorder(t)
	setupRemoveAllRecorder(t, rec)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.SetLUKS("fisherman-root")
	c.AddPostRemoval("/var/fisherman-tmp")

	c.Run()

	// Locate the first removeAll and the last umount / cryptsetup luksClose.
	lastTeardownIdx := -1
	firstRemoveIdx := -1
	for i, call := range rec.calls {
		switch call.name {
		case "umount", "cryptsetup", "fuser", "blockdev", "udevadm":
			if i > lastTeardownIdx {
				lastTeardownIdx = i
			}
		case "removeAll":
			if firstRemoveIdx == -1 {
				firstRemoveIdx = i
			}
		}
	}
	if firstRemoveIdx == -1 {
		t.Fatalf("no removeAll call recorded; got calls: %v", rec.calls)
	}
	if firstRemoveIdx < lastTeardownIdx {
		t.Errorf("post-removal at idx %d ran before final teardown call at idx %d: %v",
			firstRemoveIdx, lastTeardownIdx, rec.calls)
	}
	if rec.calls[firstRemoveIdx].args[0] != "/var/fisherman-tmp" {
		t.Errorf("removeAll target = %q, want /var/fisherman-tmp",
			rec.calls[firstRemoveIdx].args[0])
	}
}

func stubMountPoint(t *testing.T, mounted bool) {
	t.Helper()
	old := post.IsMountPointFn
	post.IsMountPointFn = func(string) bool { return mounted }
	t.Cleanup(func() { post.IsMountPointFn = old })
}

// TestCleanup_TargetScratchGoesBeforeTheTargetIsUnmounted is the order #307
// asks for: release the scratch's own bind, delete the directory, and only
// then unmount the target and close LUKS. The command order is necessary
// but not sufficient; TestCleanup_TargetScratchLoopback checks the bytes.
func TestCleanup_TargetScratchGoesBeforeTheTargetIsUnmounted(t *testing.T) {
	rec := setupRecorder(t)
	setupRemoveAllRecorder(t, rec)
	stubMountPoint(t, true)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.AddMount("/mnt/target/boot")
	c.AddTargetScratch("/mnt/target/.fisherman-scratch")
	c.SetLUKS("fisherman-root")
	c.AddPostRemoval("/var/fisherman-tmp")

	if err := c.Run(); err != nil {
		t.Fatal(err)
	}

	var seq []string
	for _, call := range rec.calls {
		switch call.name {
		case "umount", "removeAll":
			seq = append(seq, call.name+" "+call.args[len(call.args)-1])
		case "cryptsetup":
			seq = append(seq, "luksClose")
		}
	}
	want := []string{
		"umount /mnt/target/.fisherman-scratch",
		"removeAll /mnt/target/.fisherman-scratch",
		"umount /mnt/target/boot",
		"umount /mnt/target",
		"luksClose",
		"removeAll /var/fisherman-tmp",
	}
	if strings.Join(seq, "\n") != strings.Join(want, "\n") {
		t.Errorf("teardown order:\n%s\nwant:\n%s", strings.Join(seq, "\n"), strings.Join(want, "\n"))
	}
}

// On the success path main calls RemoveTargetScratch before finalize makes
// the target read-only. Run must then not touch the path a second time.
func TestCleanup_RemoveTargetScratchIsDoneOnce(t *testing.T) {
	rec := setupRecorder(t)
	setupRemoveAllRecorder(t, rec)
	stubMountPoint(t, true)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.AddTargetScratch("/mnt/target/.fisherman-scratch")
	if err := c.RemoveTargetScratch(); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 2 || rec.calls[0].name != "umount" || rec.calls[1].name != "removeAll" {
		t.Fatalf("RemoveTargetScratch calls = %v, want umount then removeAll", rec.calls)
	}
	if err := c.RemoveTargetScratch(); err != nil || len(rec.calls) != 2 {
		t.Errorf("second RemoveTargetScratch did work: %v, %v", err, rec.calls)
	}
	c.Run()
	removes := 0
	for _, call := range rec.calls {
		if call.name == "removeAll" {
			removes++
		}
	}
	if removes != 1 {
		t.Errorf("scratch removed %d times, want 1: %v", removes, rec.calls)
	}
}

// The bind can already be gone (the GPT retag of a systemd-boot install
// unmounts the whole root partition). Then nothing is unmounted, no failure
// is reported, and the directory is still deleted.
func TestCleanup_TargetScratchWithoutItsBind(t *testing.T) {
	rec := setupRecorder(t)
	setupRemoveAllRecorder(t, rec)
	stubMountPoint(t, false)

	var c post.Cleanup
	c.AddTargetScratch("/mnt/target/.fisherman-scratch")
	if err := c.RemoveTargetScratch(); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 || rec.calls[0].name != "removeAll" {
		t.Errorf("calls = %v, want only removeAll", rec.calls)
	}
}

// TestCleanup_NoPostRemovalWhenNotRegistered ensures we don't blindly remove
// the mount paths themselves — only paths explicitly registered.
func TestCleanup_NoPostRemovalWhenNotRegistered(t *testing.T) {
	rec := setupRecorder(t)
	setupRemoveAllRecorder(t, rec)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.Run()

	for _, call := range rec.calls {
		if call.name == "removeAll" {
			t.Errorf("unexpected removeAll call: %v", call)
		}
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

// TestCleanup_ReportsFailuresAndKeepsGoing verifies that Run attempts every
// step even when one fails, and returns the failures so the caller's final
// error event can say what was left behind.
func TestCleanup_ReportsFailuresAndKeepsGoing(t *testing.T) {
	rec := setupRecorder(t)
	rec.err = errors.New("target is busy")
	setupRemoveAllRecorder(t, rec)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.AddMount("/mnt/target/boot")
	c.AddPostRemoval("/var/fisherman-tmp")
	err := c.Run()
	if err == nil {
		t.Fatal("Run returned nil although every umount failed")
	}
	for _, want := range []string{"/mnt/target/boot", "/mnt/target", "target is busy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if last := rec.calls[len(rec.calls)-1]; last.name != "removeAll" {
		t.Errorf("post-removal skipped after umount failure; calls = %v", rec.calls)
	}
	if again := c.Run(); again == nil || again.Error() != err.Error() {
		t.Errorf("second Run = %v, want the first result %v", again, err)
	}
}

// TestCleanup_ConcurrentRunTearsDownOnce verifies that a cancel signal's Run
// racing the pipeline's own Run unmounts each path exactly once.
func TestCleanup_ConcurrentRunTearsDownOnce(t *testing.T) {
	var mu sync.Mutex
	umounts := 0
	runner.RunFn = func(_ io.Reader, name string, _ ...string) error {
		if name == "umount" {
			mu.Lock()
			umounts++
			mu.Unlock()
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	var c post.Cleanup
	c.AddMount("/mnt/target")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Run() }()
	}
	wg.Wait()
	if umounts != 1 {
		t.Errorf("umount ran %d times, want 1", umounts)
	}
}

// tuna-os/fisherman#413: a ZFS pool the install imported is exported by
// teardown, on every exit path, after the unmounts (the ESP is mounted
// inside the pool's root dataset) and before the host scratch is removed.
func TestCleanup_ExportsTheZFSPool(t *testing.T) {
	rec := setupRecorder(t)
	setupRemoveAllRecorder(t, rec)

	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.AddMount("/mnt/target/boot/efi")
	c.SetZFSPool("rpool")
	c.AddPostRemoval("/var/fisherman-tmp")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}

	var seq []string
	for _, call := range rec.calls {
		seq = append(seq, call.name+" "+strings.Join(call.args, " "))
	}
	want := []string{
		"umount -Rl /mnt/target/boot/efi",
		"umount -Rl /mnt/target",
		"zpool export rpool",
		"removeAll /var/fisherman-tmp",
	}
	if strings.Join(seq, "\n") != strings.Join(want, "\n") {
		t.Errorf("teardown:\n%s\nwant:\n%s", strings.Join(seq, "\n"), strings.Join(want, "\n"))
	}

	// Once: a second Run (a cancel racing the normal exit) does not export
	// a pool that is already gone.
	c.Run()
	exports := 0
	for _, call := range rec.calls {
		if call.name == "zpool" {
			exports++
		}
	}
	if exports != 1 {
		t.Errorf("zpool ran %d times, want 1", exports)
	}
}

// A busy pool is forced, and a pool that cannot be exported at all is
// reported: the caller's final message says what was left behind.
func TestCleanup_ZFSExportFailureIsForcedThenReported(t *testing.T) {
	var calls []string
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "zpool" {
			return errors.New("pool is busy")
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	var c post.Cleanup
	c.SetZFSPool("tank")
	err := c.Run()
	if err == nil || !strings.Contains(err.Error(), "exporting ZFS pool tank") {
		t.Errorf("err = %v, want the export failure", err)
	}
	if strings.Join(calls, "; ") != "zpool export tank; zpool export -f tank" {
		t.Errorf("calls = %v", calls)
	}
}

// No pool registered: zpool is never run (a host without ZFS has no zpool).
func TestCleanup_NoZFSPool(t *testing.T) {
	rec := setupRecorder(t)
	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.Run()
	for _, call := range rec.calls {
		if call.name == "zpool" {
			t.Errorf("unexpected zpool call: %v", call)
		}
	}
}

// TestCleanup_ReleasesRunAfterLUKSBeforePostRemovals: a host release (the
// source reuse copy, #384) runs on every teardown, after the target is
// unmounted and closed and before the host scratch is removed, and its
// failure is reported without stopping the rest.
func TestCleanup_ReleasesRunAfterLUKSBeforePostRemovals(t *testing.T) {
	rec := setupRecorder(t)
	setupRemoveAllRecorder(t, rec)
	var c post.Cleanup
	c.AddMount("/mnt/target")
	c.SetLUKS("fisherman-root")
	c.AddPostRemoval("/var/fisherman-tmp")
	c.AddRelease(func() error {
		rec.calls = append(rec.calls, execCall{name: "release"})
		return errors.New("rmi failed")
	})
	err := c.Run()
	if err == nil || !strings.Contains(err.Error(), "rmi failed") {
		t.Errorf("Run error %v, want the release failure", err)
	}
	var names []string
	for _, call := range rec.calls {
		names = append(names, call.name)
	}
	got := strings.Join(names, ",")
	if !strings.HasSuffix(got, "cryptsetup,release,removeAll") {
		t.Errorf("order %s: want ... cryptsetup, release, removeAll", got)
	}
}
