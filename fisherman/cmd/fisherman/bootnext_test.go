package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// tuna-os/bootc-installer#318: fisherman sets BootNext itself while it is
// root. On live media by default; elsewhere only when the recipe asks,
// because it writes the firmware of the machine fisherman runs on.
func TestApplyBootNext(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name     string
		field    *bool
		live     bool
		bootID   string
		setErr   error
		wantSet  bool
		wantLine string
	}{
		{"live media, default: set", nil, true, "0003", nil, true,
			"BootNext set to Boot0003: the next boot starts the installed system"},
		{"installed host, default: not set", nil, false, "0003", nil, false,
			"BootNext not set: this host is not live media"},
		{"installed host, recipe true: set", &yes, false, "0003", nil, true, "BootNext set to Boot0003"},
		{"live media, recipe false: not set", &no, true, "0003", nil, false,
			"BootNext not set: the recipe sets setBootNext to false"},
		{"no boot entry found", nil, true, "", nil, false,
			"BootNext not set: no EFI boot entry for the installed system was found"},
		{"efibootmgr fails: a warning, not an error", nil, true, "0003", errors.New("EFI variables are not supported"), true,
			"Warning: could not set BootNext to Boot0003 (the firmware's boot order decides the next boot): EFI variables are not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			old := setBootNextFn
			setBootNextFn = func(id string) error { got = append(got, id); return tt.setErr }
			t.Cleanup(func() { setBootNextFn = old })

			asked := 0
			line := applyBootNext(&recipe.Recipe{SetBootNext: tt.field}, tt.bootID, func() bool { asked++; return tt.live })
			if (len(got) == 1) != tt.wantSet || (tt.wantSet && got[0] != tt.bootID) {
				t.Errorf("efibootmgr calls = %v, want set=%v", got, tt.wantSet)
			}
			if !strings.Contains(line, tt.wantLine) {
				t.Errorf("line = %q, want it to contain %q", line, tt.wantLine)
			}
			// An explicit field, or no boot entry, needs no live detection.
			if (tt.field != nil || tt.bootID == "") && asked != 0 {
				t.Errorf("probed for live media %d times with nothing to decide", asked)
			}
		})
	}
}
