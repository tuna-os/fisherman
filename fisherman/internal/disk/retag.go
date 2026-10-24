package disk

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/runner"
)

// Seams for the wait/retry helpers below. Tests replace them so the
// deadline and backoff run on a fake clock instead of real sleeps.
var (
	nowFn   = time.Now
	sleepFn = time.Sleep
	statFn  = os.Stat
	mountFn = Mount
)

// DefaultPartitionNodeTimeout bounds how long WaitForPartitionNode waits for a
// partition node to come back after the kernel re-reads a partition table.
const DefaultPartitionNodeTimeout = 15 * time.Second

// DefaultMountAttempts and DefaultMountBackoff are MountWithRetry's settings
// for a remount right after a partition-table write: five tries, waiting
// 0.5s, 1s, 1.5s and 2s between them.
const (
	DefaultMountAttempts = 5
	DefaultMountBackoff  = 500 * time.Millisecond
)

// partitionNodePoll is the polling interval of WaitForPartitionNode.
const partitionNodePoll = 100 * time.Millisecond

// GetPartitionType returns the GPT type GUID of partition partNum on disk, as
// printed by `sfdisk --part-type <disk> <n>` (no type argument = read only).
// The result is trimmed but otherwise as sfdisk printed it (upper case).
func GetPartitionType(disk string, partNum int) (string, error) {
	out, err := runner.Output("sfdisk", "--part-type", disk, strconv.Itoa(partNum))
	if err != nil {
		return "", fmt.Errorf("reading partition type: %w", err)
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return "", errors.New("reading partition type: sfdisk printed nothing")
	}
	return t, nil
}

// PartitionTypeNeedsRewrite reports whether partition partNum on disk must be
// rewritten to carry want. It is false only when the current type was read
// successfully and already equals want (case-insensitively); a failed or
// empty read counts as "needs rewrite" so the caller falls back to writing.
// It returns the current type (empty on a failed read) for logging.
func PartitionTypeNeedsRewrite(disk string, partNum int, want string) (bool, string) {
	cur, err := GetPartitionType(disk, partNum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; rewriting partition type anyway\n", err)
		return true, ""
	}
	return !strings.EqualFold(cur, want), cur
}

// WaitForPartitionNode runs `udevadm settle` and then polls until path exists
// and is a block device, or timeout elapses. A partition-table write makes the
// kernel and udev drop and re-add every partition node of the disk, so a node
// that existed a moment ago can be missing (or not yet a block device) when
// the next command runs. On timeout it returns an error naming the last
// observed state; it never reports success for a node it did not see.
func WaitForPartitionNode(path string, timeout time.Duration) error {
	fmt.Fprintf(os.Stdout, "+ udevadm settle (waiting for %s)\n", path)
	_ = runner.Run("udevadm", "settle")

	deadline := nowFn().Add(timeout)
	var last error
	for {
		fi, err := statFn(path)
		switch {
		case err != nil:
			last = err
		case fi.Mode()&os.ModeDevice == 0 || fi.Mode()&os.ModeCharDevice != 0:
			last = fmt.Errorf("%s is not a block device (mode %s)", path, fi.Mode())
		default:
			return nil
		}
		if !nowFn().Before(deadline) {
			return fmt.Errorf("partition node %s did not appear within %s: %w", path, timeout, last)
		}
		sleepFn(partitionNodePoll)
	}
}

// MountWithRetry mounts dev at target, retrying up to attempts times with a
// linearly growing backoff (backoff, 2*backoff, ...). If a failed attempt
// nevertheless left dev mounted at target (Mount re-runs mount for its
// diagnostics), that counts as mounted rather than retrying into "already
// mounted". After the last attempt it returns the last mount error.
func MountWithRetry(dev, target, opts string, attempts int, backoff time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 1; i <= attempts; i++ {
		if err = mountFn(dev, target, opts); err == nil {
			return nil
		}
		if mountedAt(dev, target) {
			return nil
		}
		if i < attempts {
			fmt.Fprintf(os.Stderr, "warning: mount %s %s failed (attempt %d/%d): %v\n", dev, target, i, attempts, err)
			sleepFn(time.Duration(i) * backoff)
		}
	}
	return fmt.Errorf("mounting %s at %s after %d attempts: %w", dev, target, attempts, err)
}

// mountedAt reports whether /proc/mounts lists dev mounted at target.
func mountedAt(dev, target string) bool {
	data, err := os.ReadFile(procMountsPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == dev && f[1] == target {
			return true
		}
	}
	return false
}
