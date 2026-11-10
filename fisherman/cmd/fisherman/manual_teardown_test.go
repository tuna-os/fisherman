package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/runner"
)

// fakeHostRunner records every subprocess the manual-layout path and the
// teardown start, and lets a test fail or intercept one of them.
type fakeHostRunner struct {
	calls []string
	on    func(call string, n int) error // n counts calls of the same program, from 1
	count map[string]int
}

func (f *fakeHostRunner) run(_ io.Reader, name string, args ...string) error {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.count == nil {
		f.count = map[string]int{}
	}
	f.count[name]++
	if f.on != nil {
		return f.on(call, f.count[name])
	}
	return nil
}

func (f *fakeHostRunner) only(prefixes ...string) []string {
	var out []string
	for _, c := range f.calls {
		for _, p := range prefixes {
			if strings.HasPrefix(c, p+" ") {
				out = append(out, c)
			}
		}
	}
	return out
}

// manualTeardownFixture swaps in a fresh cleanup and swap registry, a fake
// runner and a /proc/swaps fixture, the way main() uses them.
func manualTeardownFixture(t *testing.T, activeSwap ...string) (*fakeHostRunner, string) {
	t.Helper()
	oldCleanup, oldSwaps, oldProc := cleanup, swaps, procSwaps
	cleanup, swaps = &post.Cleanup{}, &swapSet{}
	fake := &fakeHostRunner{}
	runner.RunFn = fake.run
	dir := t.TempDir()
	procSwaps = filepath.Join(dir, "swaps")
	content := "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"
	for _, dev := range activeSwap {
		content += dev + "                               partition\t8388604\t\t0\t\t-2\n"
	}
	if err := os.WriteFile(procSwaps, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, swaps, procSwaps = oldCleanup, oldSwaps, oldProc
		runner.RunFn = runner.DefaultRun
	})
	return fake, filepath.Join(dir, "target")
}

var manualSpecs = []disk.MountSpec{
	{Partition: "/dev/sda3", Target: "/", Fstype: "xfs"},
	{Partition: "/dev/sda2", Target: "/boot", Fstype: "ext4"},
	{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "unformatted"},
	{Partition: "/dev/sda4", Target: "swap", Fstype: "swap"},
}

// applyManual is the call main() makes for a manual layout.
func applyManual(target string) error {
	_, _, _, err := disk.ApplyCustomLayoutWith(manualSpecs, target,
		disk.LayoutHooks{Mounted: cleanup.AddMount, SwapOn: swaps.Add})
	return err
}

// tuna-os/fisherman#362: ApplyCustomLayout failed on a later partition and
// main() called fatal() before registering the mounts it had already made,
// so the root stayed mounted. Each mount is now registered as it is made.
func TestManualLayoutPartialFailureTearsDownWhatWasMounted(t *testing.T) {
	tests := []struct {
		name       string
		failMount  int
		wantUmount func(target string) []string
	}{
		{"first mount fails: nothing to unmount", 1, func(string) []string { return nil }},
		{"second mount fails", 2, func(tg string) []string { return []string{"umount -Rl " + tg} }},
		{"third mount fails", 3, func(tg string) []string {
			return []string{"umount -Rl " + tg + "/boot", "umount -Rl " + tg}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, target := manualTeardownFixture(t)
			fake.on = func(call string, n int) error {
				if strings.HasPrefix(call, "mount ") && n == tt.failMount {
					return errors.New("mount: wrong fs type")
				}
				return nil
			}
			if err := applyManual(target); err == nil {
				t.Fatal("ApplyCustomLayoutWith succeeded though a mount failed")
			}
			if err := teardown(); err != nil {
				t.Fatalf("teardown: %v", err)
			}
			// Exactly the acquired mounts, in reverse order; never a path
			// that was not mounted.
			if got := fake.only("umount"); !reflect.DeepEqual(got, tt.wantUmount(target)) {
				t.Errorf("umount calls = %q, want %q", got, tt.wantUmount(target))
			}
		})
	}
}

// A cancel signal runs the teardown on another goroutine while
// ApplyCustomLayout is still working. Whatever is mounted at that moment
// must already be registered.
func TestManualLayoutCancelAfterFirstMountTearsItDown(t *testing.T) {
	fake, target := manualTeardownFixture(t)
	var teardownErr error
	fake.on = func(call string, n int) error {
		if strings.HasPrefix(call, "mkfs.ext4 ") {
			// The root is mounted, /boot is being formatted: cancel now.
			teardownErr = teardown()
			return runner.ErrHalted
		}
		return nil
	}
	if err := applyManual(target); err == nil {
		t.Fatal("the layout went on after the cancel")
	}
	if teardownErr != nil {
		t.Fatalf("teardown: %v", teardownErr)
	}
	if got, want := fake.only("umount"), []string{"umount -Rl " + target}; !reflect.DeepEqual(got, want) {
		t.Errorf("umount calls = %q, want %q", got, want)
	}
	// Teardown is idempotent: the fatal() that follows does not unmount twice.
	_ = teardown()
	if got := fake.only("umount"); len(got) != 1 {
		t.Errorf("a second teardown unmounted again: %q", got)
	}
}

// tuna-os/fisherman#415: the live session kept its swap on the new disk
// after "complete". Success and failure both turn it off, before the unmounts.
func TestManualLayoutSwapIsTurnedOff(t *testing.T) {
	fake, target := manualTeardownFixture(t, "/dev/sda4")
	if err := applyManual(target); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.only("mkswap", "swapon"), []string{"mkswap /dev/sda4", "swapon /dev/sda4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("swap setup = %q, want %q", got, want)
	}
	if err := teardown(); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	want := []string{"swapoff /dev/sda4",
		"umount -Rl " + target + "/boot/efi", "umount -Rl " + target + "/boot", "umount -Rl " + target}
	if got := fake.only("swapoff", "umount"); !reflect.DeepEqual(got, want) {
		t.Errorf("teardown calls = %q, want %q", got, want)
	}
	_ = teardown()
	if got := fake.only("swapoff"); len(got) != 1 {
		t.Errorf("swapoff ran %d times, want once", len(got))
	}
}

func TestSwapOffSkipsSwapThatNeverBecameActive(t *testing.T) {
	// mkswap failed (swap setup is non-fatal), so /proc/swaps has no entry.
	fake, target := manualTeardownFixture(t)
	fake.on = func(call string, _ int) error {
		if strings.HasPrefix(call, "mkswap ") {
			return errors.New("mkswap: device busy")
		}
		return nil
	}
	if err := applyManual(target); err != nil {
		t.Fatal(err)
	}
	if err := teardown(); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if got := fake.only("swapoff"); got != nil {
		t.Errorf("swapoff ran for inactive swap: %q", got)
	}
}

func TestSwapOffFailureIsReportedAndUnmountsStillRun(t *testing.T) {
	fake, target := manualTeardownFixture(t, "/dev/sda4")
	if err := applyManual(target); err != nil {
		t.Fatal(err)
	}
	fake.on = func(call string, _ int) error {
		if strings.HasPrefix(call, "swapoff ") {
			return errors.New("swapoff: cannot allocate memory")
		}
		return nil
	}
	err := teardown()
	if err == nil || !strings.Contains(err.Error(), "swapoff /dev/sda4") {
		t.Errorf("teardown error = %v, want the swapoff failure", err)
	}
	if got := fake.only("umount"); len(got) != 3 {
		t.Errorf("unmounts after a failed swapoff = %q, want all three", got)
	}
}

func TestSwapOffTriesEveryDeviceWhenProcSwapsIsUnreadable(t *testing.T) {
	fake, _ := manualTeardownFixture(t)
	procSwaps = filepath.Join(t.TempDir(), "absent")
	swaps.Add("/dev/sda4")
	if err := swaps.Off(); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.only("swapoff"), []string{"swapoff /dev/sda4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("swapoff calls = %q, want %q", got, want)
	}
}
