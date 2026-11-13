package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/tuna-os/fisherman/internal/runner"
)

// swapSet is the swap a manual layout activates on the target
// (disk.ApplyCustomLayout runs mkswap and swapon for a customMounts entry
// with target "swap"). The live session must not keep using swap on the disk
// it just installed to: a frontend that offers "install again" or an eject
// would find the device busy, and the installed system's swap would carry
// the installer's pages. teardown() turns it off on every exit path.
//
// It lives beside post.Cleanup rather than in it; Off is idempotent and safe
// for concurrent use for the same reason Cleanup.Run is.
type swapSet struct {
	mu      sync.Mutex
	devices []string
	done    bool
	err     error
}

// swaps is the process-wide swap registry, like cleanup.
var swaps = &swapSet{}

// procSwaps is /proc/swaps; tests point it at a fixture.
var procSwaps = "/proc/swaps"

// Add registers dev for swapoff. Register before swapon runs, so a failure
// between the two still turns it off.
func (s *swapSet) Add(dev string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices = append(s.devices, dev)
}

// Off runs `swapoff` for every registered device that is active swap. Only
// the first call does any work; every call returns the first call's result.
func (s *swapSet) Off() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return s.err
	}
	s.done = true
	if len(s.devices) == 0 {
		return nil
	}
	active := activeSwaps()
	var errs []error
	for _, dev := range s.devices {
		// A device that never reached swapon (mkswap failed, or the run
		// stopped first) is not in /proc/swaps; swapoff would only fail.
		if active != nil && !active[resolveDevice(dev)] {
			continue
		}
		if err := runner.Run("swapoff", dev); err != nil {
			fmt.Fprintf(os.Stderr, "warning: swapoff %s: %v\n", dev, err)
			errs = append(errs, fmt.Errorf("swapoff %s: %w", dev, err))
		}
	}
	s.err = errors.Join(errs...)
	return s.err
}

// activeSwaps is the set of active swap devices from /proc/swaps, by
// resolved path, or nil when the file cannot be read (then every registered
// device is tried).
func activeSwaps() map[string]bool {
	data, err := os.ReadFile(procSwaps)
	if err != nil {
		return nil
	}
	active := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) == 0 {
			continue // header
		}
		active[resolveDevice(fields[0])] = true
	}
	return active
}
