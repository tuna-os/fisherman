package main

import (
	"github.com/tuna-os/fisherman/internal/probe"
)

// The tests in this package drive the install and `validate` with fixture
// "disks" that are regular files or /dev/null, as an unprivileged user, on
// whatever firmware the CI runner has. By default they therefore run on a
// stand-in host: root, UEFI, and every existing path an eligible whole disk.
// The re-executed child processes (TestStdinRecipeCLI and the preflight
// tests) get the same stand-in, because init runs in them too.
//
// The tests of the rules these seams hide set them explicitly: see
// target_test.go (realTarget, and the root and firmware cases) and
// lock_test.go.
func init() {
	geteuid = func() int { return 0 }
	// An unprivileged test cannot write /run; lock_test.go tests the real
	// lock on a path of its own.
	acquireInstallLock = func() error { return nil }
	hostUEFI = func() bool { return true }
	hostTarget = func(path string) (probe.Target, error) {
		return probe.Target{Path: path, Device: path, SizeBytes: probe.MinDiskBytes}, nil
	}
}
