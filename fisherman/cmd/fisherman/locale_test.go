package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/recipe"
)

func bootEntries(t *testing.T, fx deploytest.Sysroot) string {
	t.Helper()
	var all []string
	for _, dir := range []string{"boot/loader/entries", "boot/efi/loader/entries"} {
		matches, _ := filepath.Glob(filepath.Join(fx.Path, dir, "*.conf"))
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, string(b))
		}
	}
	return strings.Join(all, "\n")
}

// tuna-os/fisherman#405, through the install's own call, on both layouts:
// the settings land in the deployment's /etc, and for an encrypted install
// the keymap reaches the initrd through the kernel command line.
func TestApplyLocale(t *testing.T) {
	r := &recipe.Recipe{
		Locale:   "de_DE.UTF-8",
		Keyboard: &recipe.KeyboardSpec{Layout: "de"},
		Timezone: "UTC",
	}
	for name, mk := range map[string]func(testing.TB) deploytest.Sysroot{
		"ostree": deploytest.Ostree, "composefs": deploytest.ComposeFs,
	} {
		for _, encrypted := range []bool{true, false} {
			fx := mk(t)
			if name == "ostree" {
				zone := filepath.Join(fx.Root, "usr", "share", "zoneinfo", "UTC")
				if err := os.MkdirAll(filepath.Dir(zone), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(zone, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			recordingRunner(t, "")
			out, _ := captureOutput(t, func() { applyLocale(fx.Path, r, encrypted) })

			if got, _ := os.ReadFile(filepath.Join(fx.Etc, "locale.conf")); string(got) != "LANG=de_DE.UTF-8\n" {
				t.Errorf("%s: locale.conf = %q", name, got)
			}
			if got, _ := os.ReadFile(filepath.Join(fx.Etc, "vconsole.conf")); !strings.Contains(string(got), "KEYMAP=de\n") {
				t.Errorf("%s: vconsole.conf = %q", name, got)
			}
			if _, err := os.Lstat(filepath.Join(fx.Etc, "localtime")); err != nil {
				t.Errorf("%s: localtime: %v", name, err)
			}
			if s := deploytest.Strays(fx); len(s) != 0 {
				t.Errorf("%s: wrote %v into the physical sysroot", name, s)
			}
			if !strings.Contains(out, "Language, keyboard and time zone: wrote /etc/locale.conf, /etc/vconsole.conf") {
				t.Errorf("%s: log:\n%s", name, out)
			}

			hasArg := strings.Contains(bootEntries(t, fx), "rd.vconsole.keymap=de")
			if hasArg != encrypted {
				t.Errorf("%s encrypted=%v: rd.vconsole.keymap in the boot entries = %v", name, encrypted, hasArg)
			}
			if encrypted && !strings.Contains(out, "Added rd.vconsole.keymap=de to 1 boot entry") {
				t.Errorf("%s: no log line for the kernel argument:\n%s", name, out)
			}
		}
	}
}

// A recipe with none of the three changes nothing and says nothing.
func TestApplyLocale_NothingSet(t *testing.T) {
	fx := deploytest.Ostree(t)
	recordingRunner(t, "")
	before := bootEntries(t, fx)
	out, _ := captureOutput(t, func() { applyLocale(fx.Path, &recipe.Recipe{}, true) })
	if out != "" {
		t.Errorf("logged for a recipe with no locale settings:\n%s", out)
	}
	for _, rel := range []string{"locale.conf", "vconsole.conf", "localtime", "X11"} {
		if _, err := os.Lstat(filepath.Join(fx.Etc, rel)); err == nil {
			t.Errorf("wrote %s", rel)
		}
	}
	if bootEntries(t, fx) != before {
		t.Error("boot entries changed")
	}
}

// An encrypted install whose boot entries cannot be edited (a unified
// kernel image carries the command line) says what the user will meet.
func TestApplyLocale_KeymapCannotReachTheInitrd(t *testing.T) {
	fx := deploytest.ComposeFs(t)
	if err := os.RemoveAll(filepath.Join(fx.Path, "boot", "efi", "loader")); err != nil {
		t.Fatal(err)
	}
	recordingRunner(t, "")
	r := &recipe.Recipe{Keyboard: &recipe.KeyboardSpec{Layout: "fr"}}
	out, _ := captureOutput(t, func() { applyLocale(fx.Path, r, true) })
	if !strings.Contains(out, "Warning: could not pass the keyboard layout to the initrd") ||
		!strings.Contains(out, "the disk passphrase prompt uses the image's default keymap") {
		t.Errorf("log:\n%s", out)
	}
}

// `validate --partial` runs the three rules when their keys are present, so
// a page can check a value while the user types it.
func TestPartialProblems_Locale(t *testing.T) {
	ps, err := partialProblems([]byte(`{"locale":"x y","keyboard":{"variant":"nodeadkeys"},"timezone":"a b"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Field+":"+p.Code)
	}
	want := "locale:locale_invalid keyboard.layout:keyboard_layout_required timezone:timezone_invalid"
	if strings.Join(got, " ") != want {
		t.Errorf("partial problems = %v\nwant %s", got, want)
	}
	ps, err = partialProblems([]byte(`{"locale":"de_DE.UTF-8","keyboard":{"layout":"de"},"timezone":"Europe/Berlin"}`))
	if err != nil || len(ps) != 0 {
		t.Errorf("valid fragment: %v, %v", ps, err)
	}
	// Absent keys are not checked; hostname alone stays hostname alone.
	ps, err = partialProblems([]byte(`{"hostname":"ok"}`))
	if err != nil || len(ps) != 0 {
		t.Errorf("hostname only: %v, %v", ps, err)
	}
	// zfs in a partial fragment needs the opt-in in the same fragment.
	ps, _ = partialProblems([]byte(`{"filesystem":"zfs"}`))
	if len(ps) != 1 || ps[0].Code != recipe.CodeZFSExperimental {
		t.Errorf("partial zfs: %v", ps)
	}
	ps, _ = partialProblems([]byte(`{"filesystem":"zfs","experimental":["zfs"]}`))
	if len(ps) != 0 {
		t.Errorf("partial zfs with the opt-in: %v", ps)
	}
}
