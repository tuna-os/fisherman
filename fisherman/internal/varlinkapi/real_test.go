package varlinkapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/crypt"
	"github.com/tuna-os/fisherman/internal/handoff"
)

const realTestDump = `LUKS header information
Version:       	2

Keyslots:
  0: luks2
	Key:       	512 bits
	Priority:  	normal
Tokens:
`

type fakeCryptRunner struct {
	dumps int
}

func (f *fakeCryptRunner) Run(_ context.Context, name string, args []string, _ []byte, _ crypt.Unlock) ([]byte, error) {
	switch {
	case name == "cryptsetup" && len(args) == 2 && args[0] == "luksUUID":
		return []byte("uuid\n"), nil
	case name == "cryptsetup" && len(args) == 2 && args[0] == "luksDump":
		f.dumps++
		if f.dumps == 1 {
			return []byte(realTestDump), nil
		}
		return []byte(strings.Replace(realTestDump, "  0: luks2", "  0: luks2\n  1: luks2", 1)), nil
	}
	return nil, nil
}

func TestRealEnrollPassword(t *testing.T) {
	r := Real{Enroller: crypt.NewEnroller(&fakeCryptRunner{})}
	res, err := r.Enroll(context.Background(), EnrollParams{
		Node:           "/dev/vda3",
		Mechanism:      MechPassword,
		Password:       ptr(Secret("new passphrase")),
		UnlockPassword: ptr(Secret("")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.State == nil || *res.State != "enrolled" {
		t.Errorf("state = %+v", res.State)
	}
	if res.Slot == nil || *res.Slot != 1 {
		t.Errorf("slot = %+v, want 1", res.Slot)
	}
}

func TestRealEnrollNeedsUnlock(t *testing.T) {
	r := Real{Enroller: crypt.NewEnroller(&fakeCryptRunner{})}
	_, err := r.Enroll(context.Background(), EnrollParams{
		Node: "n", Mechanism: MechPassword, Password: ptr(Secret("x")),
	})
	if !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("err = %v", err)
	}
}

func TestRealMakeRecoveryKeyShape(t *testing.T) {
	r := Real{}
	key, err := r.MakeRecoveryKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if groups := strings.Split(string(key), "-"); len(groups) != 8 {
		t.Errorf("recovery key has %d groups: %q", len(groups), key.Reveal())
	}
}

func TestRealListSlots(t *testing.T) {
	r := Real{Enroller: crypt.NewEnroller(&fakeCryptRunner{})}
	slots, err := r.ListSlots(context.Background(), "/dev/vda3")
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 || slots[0].Slot != 0 || slots[0].Type != "password" {
		t.Errorf("slots = %+v", slots)
	}
}

type fakeHandoffRunner struct {
	calls [][]string
}

func (f *fakeHandoffRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil
}

func TestRealHandoff(t *testing.T) {
	f := &fakeHandoffRunner{}
	r := Real{HandoffRunner: f}
	keeps, err := r.Handoff(context.Background(), handoff.ModeReboot)
	if err != nil || keeps {
		t.Errorf("reboot: keeps=%v err=%v", keeps, err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %v", f.calls)
	}
	if _, err := r.Handoff(context.Background(), handoff.ModeMigrate); !errors.Is(err, handoff.ErrDeferred) {
		t.Errorf("migrate: err = %v", err)
	}
}

func TestRealDecideHandoff(t *testing.T) {
	// The fact-gathering Probe slice is not landed: zero facts reboot.
	d, err := Real{}.DecideHandoff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != handoff.ModeReboot || d.Reason != handoff.ReasonDefault {
		t.Errorf("got %+v", d)
	}
}
