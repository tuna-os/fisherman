package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/runner"
)

// freezeFixture is manualTeardownFixture plus a fresh freeze registry and a
// mounted target, the state main() is in when it reaches the finalize step.
func freezeFixture(t *testing.T) (*fakeHostRunner, string) {
	t.Helper()
	fake, target := manualTeardownFixture(t)
	old := freezes
	freezes = &freezeSet{}
	t.Cleanup(func() { freezes = old })
	cleanup.AddMount(target)
	return fake, target
}

func finalize(target string) error {
	return disk.FinalizeFilesystemWith(target, freezes.Hooks())
}

// The normal path: one freeze, one thaw, and the teardown that follows does
// not thaw again.
func TestFinalizeThawsExactlyOnce(t *testing.T) {
	fake, target := freezeFixture(t)
	if err := finalize(target); err != nil {
		t.Fatal(err)
	}
	if err := teardown(); err != nil {
		t.Fatal(err)
	}
	want := []string{"fsfreeze -f " + target, "fsfreeze -u " + target, "umount -Rl " + target}
	if got := fake.only("fsfreeze", "umount"); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

// tuna-os/fisherman#368: a cancel after the freeze and before the thaw. The
// halted runner refused the thaw and cleanup had none registered, so a
// frozen filesystem was lazily unmounted. The teardown must thaw, before the
// unmount, and the finalize step must not thaw a second time.
func TestCancelBetweenFreezeAndThawThawsBeforeUnmount(t *testing.T) {
	fake, target := freezeFixture(t)
	// Run the freeze by hand up to the thaw, as FinalizeFilesystemWith does.
	hooks := freezes.Hooks()
	hooks.WillFreeze(target)
	if err := runner.Run("fsfreeze", "-f", target); err != nil {
		t.Fatal(err)
	}
	hooks.Froze(target)
	// The cancel: teardown before the finalize step's own thaw.
	if err := teardown(); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	// The finalize step resumes and asks for its thaw: already done.
	if err := hooks.Thaw(target); err != nil {
		t.Errorf("second thaw = %v, want nil", err)
	}
	want := []string{"fsfreeze -f " + target, "fsfreeze -u " + target, "umount -Rl " + target}
	if got := fake.only("fsfreeze", "umount"); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q, want %q (one thaw, before the unmount)", got, want)
	}
}

// The cancel lands while `fsfreeze -f` is running: the teardown kills it and
// cannot know whether the freeze took effect. It thaws anyway, and a thaw
// that fails because nothing was frozen is not a teardown failure.
func TestCancelDuringFreezeThawsAndToleratesNotFrozen(t *testing.T) {
	for _, thawFails := range []bool{false, true} {
		fake, target := freezeFixture(t)
		fake.on = func(call string, _ int) error {
			switch {
			case call == "fsfreeze -f "+target:
				if err := teardown(); err != nil {
					t.Errorf("teardown (thaw fails: %v): %v", thawFails, err)
				}
				return errors.New("signal: terminated")
			case call == "fsfreeze -u "+target && thawFails:
				return errors.New("fsfreeze: unfreeze failed: Invalid argument")
			}
			return nil
		}
		if err := finalize(target); err == nil || !strings.Contains(err.Error(), "fsfreeze") {
			t.Errorf("finalize = %v, want the freeze's error", err)
		}
		want := []string{"fsfreeze -f " + target, "fsfreeze -u " + target, "umount -Rl " + target}
		if got := fake.only("fsfreeze", "umount"); !reflect.DeepEqual(got, want) {
			t.Errorf("thaw fails %v: calls = %q, want %q", thawFails, got, want)
		}
	}
}

// A confirmed freeze that cannot be thawed is reported, and the unmount is
// still attempted.
func TestThawFailureAfterConfirmedFreezeIsReported(t *testing.T) {
	fake, target := freezeFixture(t)
	fake.on = func(call string, _ int) error {
		if call == "fsfreeze -u "+target {
			return errors.New("fsfreeze: unfreeze failed: Input/output error")
		}
		return nil
	}
	if err := finalize(target); err == nil || !strings.Contains(err.Error(), "fsfreeze -u") {
		t.Fatalf("finalize = %v, want the thaw's error", err)
	}
	// fatal() follows: the teardown does not retry the thaw, and unmounts.
	if err := teardown(); err != nil {
		t.Errorf("teardown = %v; the failed thaw was already reported by the finalize step", err)
	}
	want := []string{"fsfreeze -f " + target, "fsfreeze -u " + target, "umount -Rl " + target}
	if got := fake.only("fsfreeze", "umount"); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}

	// The same failure met by the teardown itself (cancel after the freeze).
	fake, target = freezeFixture(t)
	fake.on = func(call string, _ int) error {
		if call == "fsfreeze -u "+target {
			return errors.New("fsfreeze: unfreeze failed: Input/output error")
		}
		return nil
	}
	hooks := freezes.Hooks()
	hooks.WillFreeze(target)
	hooks.Froze(target)
	err := teardown()
	if err == nil || !strings.Contains(err.Error(), "thawing "+target) {
		t.Errorf("teardown = %v, want the thaw failure", err)
	}
	if got := fake.only("umount"); len(got) != 1 {
		t.Errorf("unmount after a failed thaw = %q, want one", got)
	}
}

// A cancel before the freeze: nothing is registered, and no thaw runs.
func TestCancelBeforeFreezeDoesNotThaw(t *testing.T) {
	fake, target := freezeFixture(t)
	fake.on = func(call string, _ int) error {
		if strings.HasPrefix(call, "mount -o remount,ro") {
			if err := teardown(); err != nil {
				t.Errorf("teardown: %v", err)
			}
			return runner.ErrHalted
		}
		return nil
	}
	if err := finalize(target); err == nil {
		t.Fatal("finalize went on after the cancel")
	}
	if got := fake.only("fsfreeze"); got != nil {
		t.Errorf("fsfreeze ran though the cancel came before the freeze: %q", got)
	}
}
