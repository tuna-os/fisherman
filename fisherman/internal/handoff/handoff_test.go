package handoff

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fake struct {
	calls [][]string
	err   error
}

func (f *fake) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.err
}

func TestRebootShellsOut(t *testing.T) {
	f := &fake{}
	keeps, err := Handoff(context.Background(), f, ModeReboot)
	if err != nil {
		t.Fatal(err)
	}
	if keeps {
		t.Error("reboot ends the live session")
	}
	if len(f.calls) != 1 || strings.Join(f.calls[0], " ") != "systemctl reboot" {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestMigrateAndSoftRebootDeferred(t *testing.T) {
	for _, mode := range []string{ModeMigrate, ModeSoftReboot} {
		f := &fake{}
		_, err := Handoff(context.Background(), f, mode)
		if !errors.Is(err, ErrDeferred) {
			t.Errorf("%s: err = %v, want ErrDeferred", mode, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s ran a command before its mechanism lands: %v", mode, f.calls)
		}
	}
}

func TestUnknownMode(t *testing.T) {
	if _, err := Handoff(context.Background(), &fake{}, "halt"); err == nil {
		t.Error("unknown mode must fail")
	}
}
