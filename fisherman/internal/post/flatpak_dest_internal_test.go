package post

import (
	"path/filepath"
	"testing"
)

// A varDisk is the booted /var, so the Flatpak copy goes there even when the
// recipe also names a flatpakVarPath: that path is under the target root,
// which the booted system does not mount on /var.
func TestFlatpakDest_SeparateVarWinsOverFlatpakVarPath(t *testing.T) {
	target := t.TempDir()
	disk := filepath.Join(target, "var")
	SetBootedVar(disk)
	t.Cleanup(func() { SetBootedVar("") })

	want := filepath.Join(disk, "lib", "flatpak")
	for _, p := range []string{"", "state/os/default/var"} {
		if got := flatpakDest(target, p); got != want {
			t.Errorf("flatpakVarPath %q: dest %s, want %s", p, got, want)
		}
	}
}

func TestFlatpakDest_FlatpakVarPathWithoutSeparateVar(t *testing.T) {
	SetBootedVar("")
	target := t.TempDir()
	want := filepath.Join(target, "state/os/default/var", "lib", "flatpak")
	if got := flatpakDest(target, "state/os/default/var"); got != want {
		t.Errorf("dest %s, want %s", got, want)
	}
}
