package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/runner"
)

// tuna-os/fisherman#291: a bind that fails leaves no directory on the
// target and registers nothing for teardown.
func TestPrepareScratchDir_BindFailureLeavesNothing(t *testing.T) {
	oldCleanup := cleanup
	cleanup = &post.Cleanup{}
	t.Cleanup(func() { cleanup = oldCleanup })
	oldBind := bindMount
	bindMount = func(src, dst string) error { return errors.New("mount: permission denied") }
	t.Cleanup(func() { bindMount = oldBind })
	var removed []string
	oldRemove := post.RemoveAllFn
	post.RemoveAllFn = func(p string) error { removed = append(removed, p); return nil }
	t.Cleanup(func() { post.RemoveAllFn = oldRemove })
	calls := recordingRunner(t, "")

	target := t.TempDir()
	if _, err := prepareScratchDir(target, true); err == nil {
		t.Fatal("no error although the bind mount failed")
	}
	if _, err := os.Stat(filepath.Join(target, ".fisherman-scratch")); err == nil {
		t.Error("scratch directory left on the target after the bind failed")
	}
	if err := cleanup.Run(); err != nil {
		t.Errorf("teardown after a failed bind: %v", err)
	}
	if len(removed) != 0 || len(*calls) != 0 {
		t.Errorf("teardown worked on a scratch that was never set up: removed=%v calls=%v", removed, *calls)
	}
}

// The live-media scratch is on the target: it is registered as target
// scratch (removed while the target is mounted), not as a host post-removal
// (removed after the unmount, which leaks it: #307).
func TestPrepareScratchDir_LiveScratchIsRemovedBeforeTargetUnmount(t *testing.T) {
	oldCleanup := cleanup
	cleanup = &post.Cleanup{}
	t.Cleanup(func() { cleanup = oldCleanup })
	oldBind := bindMount
	bindMount = func(src, dst string) error { return nil }
	t.Cleanup(func() { bindMount = oldBind })
	oldMP := post.IsMountPointFn
	post.IsMountPointFn = func(string) bool { return true }
	t.Cleanup(func() { post.IsMountPointFn = oldMP })

	var seq []string
	oldRemove := post.RemoveAllFn
	post.RemoveAllFn = func(p string) error { seq = append(seq, "remove "+p); return nil }
	t.Cleanup(func() { post.RemoveAllFn = oldRemove })
	oldRun := runner.RunFn
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		if name == "umount" {
			seq = append(seq, "umount "+args[len(args)-1])
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = oldRun })

	target := filepath.Join(t.TempDir(), "fisherman-target")
	cleanup.AddMount(target)
	scratch, err := prepareScratchDir(target, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup.Run(); err != nil {
		t.Fatal(err)
	}
	want := []string{"umount " + scratch, "remove " + scratch, "umount " + target}
	if strings.Join(seq, "\n") != strings.Join(want, "\n") {
		t.Errorf("teardown order:\n%s\nwant:\n%s", strings.Join(seq, "\n"), strings.Join(want, "\n"))
	}
}
