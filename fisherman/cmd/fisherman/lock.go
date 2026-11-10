package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// installLockPath is the host-wide install lock. /run/lock is the FHS place
// for lock files; it is a tmpfs, so a reboot clears it. (The rule against
// /run for scratch space is about multi-gigabyte image blobs, not this.)
var installLockPath = "/run/lock/fisherman-install.lock"

// errInstallRunning is acquireInstallLock's answer when another fisherman
// install holds the lock.
var errInstallRunning = errors.New("another fisherman install is running on this host")

// installLock is the held lock file. It stays open, and locked, for the
// life of the process; it is never unlocked or removed by hand.
var installLock *os.File

// acquireInstallLock is takeInstallLock; tests replace it.
var acquireInstallLock = takeInstallLock

// takeInstallLock takes the host-wide install lock, or fails at once.
//
// Two installs on one host are not isolated from each other, even with
// distinct targetMount and luksMapperName: both use the scratch directory
// /var/fisherman-tmp and bind it over the host's /var/tmp, and each one's
// teardown removes that directory and unmounts by path. One run can delete
// the other's image blobs or unmount its scratch (#309). Until each run has
// scratch and mounts of its own, a second install is refused before it
// reads its recipe.
//
// Ownership: the lock is a flock(2) on an open file. The kernel releases it
// when the process ends, however it ends (exit, fatal, cancel, SIGKILL, a
// crash), so a stale lock file never blocks the next install. Go opens files
// close-on-exec, so no child (bootc, podman) inherits the descriptor and
// keeps the lock alive after fisherman is gone.
func takeInstallLock() error {
	if err := os.MkdirAll(filepath.Dir(installLockPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(installLockPath), err)
	}
	f, err := os.OpenFile(installLockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := lockHolder(f)
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if holder != "" {
				return fmt.Errorf("%w (process %s)", errInstallRunning, holder)
			}
			return errInstallRunning
		}
		return fmt.Errorf("locking %s: %w", installLockPath, err)
	}
	// The PID is a courtesy for the message above; the flock is the lock.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	}
	installLock = f
	return nil
}

// lockHolder reads the PID the lock's holder wrote, or "".
func lockHolder(f *os.File) string {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid := strings.TrimSpace(string(buf[:n]))
	for _, c := range pid {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return pid
}
