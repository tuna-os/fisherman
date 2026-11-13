package main

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/tuna-os/fisherman/internal/disk"
)

// freezeSet owns the filesystem freeze of the finalize step
// (disk.FinalizeFilesystemWith: fstrim, remount read-only, fsfreeze -f,
// fsfreeze -u). The freeze outlives the fsfreeze process: the kernel keeps
// the superblock frozen, and holds a reference to it, until a thaw. A cancel
// between the two fsfreeze calls used to refuse the thaw (the runner is
// halted) and then lazily unmount a frozen filesystem, which stays pinned
// and blocks the LUKS close and any retry.
//
// So the freeze is registered before `fsfreeze -f` starts, with no window,
// and every thaw goes through Thaw, which runs at most once per path
// whoever calls it: the finalize step, or teardown() on a cancel or failure.
type freezeSet struct {
	mu    sync.Mutex
	paths []string
	state map[string]freezeState
	// thaw is disk.Thaw; tests replace it.
	thaw func(path string) error
}

type freezeState int

const (
	// freezePending: `fsfreeze -f` was started and has not reported
	// success. The filesystem may or may not be frozen (the process can be
	// killed by the cancel after the ioctl took effect).
	freezePending freezeState = iota + 1
	// freezeHeld: `fsfreeze -f` returned success.
	freezeHeld
	// freezeReleased: a thaw was attempted. It is not tried again.
	freezeReleased
)

// freezes is the process-wide freeze registry, like cleanup.
var freezes = &freezeSet{}

// Hooks are the hooks main() gives disk.FinalizeFilesystemWith.
func (f *freezeSet) Hooks() disk.FreezeHooks {
	return disk.FreezeHooks{WillFreeze: f.willFreeze, Froze: f.froze, Thaw: f.Thaw}
}

func (f *freezeSet) willFreeze(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		f.state = map[string]freezeState{}
	}
	if _, known := f.state[path]; !known {
		f.paths = append(f.paths, path)
	}
	f.state[path] = freezePending
}

func (f *freezeSet) froze(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A teardown that ran while fsfreeze -f was still returning has already
	// thawed; do not mark the path frozen again.
	if f.state[path] == freezePending {
		f.state[path] = freezeHeld
	}
}

// Thaw thaws path unless that was already done, and returns the result of
// the attempt. A second caller waits for the first and gets nil.
func (f *freezeSet) Thaw(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.thawLocked(path)
}

func (f *freezeSet) thawLocked(path string) error {
	state := f.state[path]
	if state == freezeReleased {
		return nil
	}
	if f.state == nil {
		f.state = map[string]freezeState{}
	}
	f.state[path] = freezeReleased
	thaw := f.thaw
	if thaw == nil {
		thaw = disk.Thaw
	}
	err := thaw(path)
	if err != nil && state == freezePending {
		// The freeze was never confirmed, so "not frozen" (EINVAL) is the
		// likely reason, and not a teardown failure. Say so and go on.
		fmt.Fprintf(os.Stderr, "fisherman: thaw of %s after an unconfirmed freeze: %v\n", path, err)
		return nil
	}
	return err
}

// ThawAll thaws every registered path that has not been thawed. teardown()
// calls it before the unmounts.
func (f *freezeSet) ThawAll() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var errs []error
	for _, path := range f.paths {
		if err := f.thawLocked(path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: thawing %s: %v\n", path, err)
			errs = append(errs, fmt.Errorf("thawing %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}
