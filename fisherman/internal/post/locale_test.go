package post_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/post"
)

const kbdModelMap = `# Generated from system-config-keyboard's model list
# consolelayout		xlayout	xmodel		xvariant	xoptions
us			us	pc105+inet	-		terminate:ctrl_alt_bksp
de			de	pc105+inet	-		terminate:ctrl_alt_bksp
de-nodeadkeys		de	pc105+inet	nodeadkeys	terminate:ctrl_alt_bksp
fr-latin9		fr	pc105+inet	latin9		terminate:ctrl_alt_bksp
ruwin_alt_sh-UTF-8	us,ru	pc105+inet	-		terminate:ctrl_alt_bksp,grp:alt_shift_toggle
`

func TestConsoleKeymap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kbd-model-map")
	if err := os.WriteFile(path, []byte(kbdModelMap), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		kb   post.Keyboard
		want string
	}{
		{post.Keyboard{Layout: "de"}, "de"},
		{post.Keyboard{Layout: "de", Variant: "nodeadkeys"}, "de-nodeadkeys"},
		{post.Keyboard{Layout: "fr", Variant: "latin9"}, "fr-latin9"},
		{post.Keyboard{Layout: "fr"}, "fr-latin9"},          // the only line for fr
		{post.Keyboard{Layout: "de", Variant: "neo"}, "de"}, // unknown variant: the plain layout's line
		{post.Keyboard{Layout: "us,de"}, "us"},              // several layouts: the first
		{post.Keyboard{Layout: "cz"}, "cz"},                 // not in the table: the layout's name
	}
	for _, tt := range tests {
		if got := post.ConsoleKeymap(tt.kb, "/nonexistent/map", path); got != tt.want {
			t.Errorf("ConsoleKeymap(%+v) = %q, want %q", tt.kb, got, tt.want)
		}
	}
	if got := post.ConsoleKeymap(post.Keyboard{Layout: "de", Variant: "nodeadkeys"}); got != "de" {
		t.Errorf("no table: %q, want the layout name", got)
	}
}

// tuna-os/fisherman#405: the four files go to the deployment's /etc on both
// layouts, in the formats systemd-localed and timedated own.
func TestWriteLocaleSettings_Fixtures(t *testing.T) {
	for name, fx := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			realFileCommands(t)
			if name == "ostree" {
				zone := filepath.Join(fx.Root, "usr", "share", "zoneinfo", "Europe", "Berlin")
				if err := os.MkdirAll(filepath.Dir(zone), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(zone, []byte("TZif"), 0o644); err != nil {
					t.Fatal(err)
				}
				mapPath := filepath.Join(fx.Root, "usr", "share", "systemd", "kbd-model-map")
				if err := os.MkdirAll(filepath.Dir(mapPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mapPath, []byte(kbdModelMap), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				// composefs has no /usr to read: the host's table is used.
				mapPath := filepath.Join(t.TempDir(), "kbd-model-map")
				if err := os.WriteFile(mapPath, []byte(kbdModelMap), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(post.SetHostKbdModelMap(mapPath))
			}
			// The image ships a locale.conf with another key, and a US keymap.
			if err := os.WriteFile(filepath.Join(fx.Etc, "locale.conf"), []byte("LANG=en_US.UTF-8\nLC_MESSAGES=C\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fx.Etc, "vconsole.conf"), []byte("KEYMAP=us\nFONT=eurlatgr\nXKBVARIANT=intl\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			res, err := post.WriteLocaleSettings(fx.Path, post.LocaleConfig{
				Locale:   "de_DE.UTF-8",
				Keyboard: &post.Keyboard{Layout: "de", Variant: "nodeadkeys", Model: "pc105"},
				Timezone: "Europe/Berlin",
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.ConsoleKeymap != "de-nodeadkeys" {
				t.Errorf("console keymap = %q, want de-nodeadkeys", res.ConsoleKeymap)
			}
			wantWrote := "/etc/locale.conf,/etc/vconsole.conf,/etc/X11/xorg.conf.d/00-keyboard.conf,/etc/localtime"
			if strings.Join(res.Wrote, ",") != wantWrote || len(res.Skipped) != 0 {
				t.Errorf("wrote %v, skipped %v", res.Wrote, res.Skipped)
			}

			read := func(rel string) string {
				b, err := os.ReadFile(filepath.Join(fx.Etc, rel))
				if err != nil {
					t.Fatalf("%s in the deployment etc: %v", rel, err)
				}
				return string(b)
			}
			if got := read("locale.conf"); got != "LC_MESSAGES=C\nLANG=de_DE.UTF-8\n" {
				t.Errorf("locale.conf = %q", got)
			}
			// KEYMAP replaced, FONT kept, XKBVARIANT replaced, keys sorted.
			if got := read("vconsole.conf"); got != "FONT=eurlatgr\nKEYMAP=de-nodeadkeys\nXKBLAYOUT=de\nXKBMODEL=pc105\nXKBVARIANT=nodeadkeys\n" {
				t.Errorf("vconsole.conf = %q", got)
			}
			x11 := read("X11/xorg.conf.d/00-keyboard.conf")
			for _, want := range []string{
				`Section "InputClass"`, `Identifier "system-keyboard"`, `MatchIsKeyboard "on"`,
				`Option "XkbLayout" "de"`, `Option "XkbModel" "pc105"`, `Option "XkbVariant" "nodeadkeys"`, "EndSection",
			} {
				if !strings.Contains(x11, want) {
					t.Errorf("00-keyboard.conf lacks %s:\n%s", want, x11)
				}
			}
			dest, err := os.Readlink(filepath.Join(fx.Etc, "localtime"))
			if err != nil || dest != "../usr/share/zoneinfo/Europe/Berlin" {
				t.Errorf("localtime -> %q, %v", dest, err)
			}
			assertNoStrays(t, fx)
			// No temporary file is left beside the real ones.
			entries, _ := os.ReadDir(fx.Etc)
			for _, e := range entries {
				if strings.Contains(e.Name(), "fisherman") {
					t.Errorf("left %s in /etc", e.Name())
				}
			}
		})
	}
}

// An existing /etc file is replaced by rename, never written through: on
// ostree it can share its inode with the repository object.
func TestWriteLocaleSettings_DoesNotWriteThroughHardLinks(t *testing.T) {
	fx := deploytest.Ostree(t)
	realFileCommands(t)
	object := filepath.Join(fx.Path, "ostree", "repo", "objects", "locale.file")
	if err := os.WriteFile(object, []byte("LANG=en_US.UTF-8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(object, filepath.Join(fx.Etc, "locale.conf")); err != nil {
		t.Fatal(err)
	}
	if _, err := post.WriteLocaleSettings(fx.Path, post.LocaleConfig{Locale: "fr_FR.UTF-8"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(object); string(got) != "LANG=en_US.UTF-8\n" {
		t.Errorf("the repository object was changed: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(fx.Etc, "locale.conf")); string(got) != "LANG=fr_FR.UTF-8\n" {
		t.Errorf("locale.conf = %q", got)
	}
}

// Only what the recipe sets is written; the rest of the image stays.
func TestWriteLocaleSettings_Partial(t *testing.T) {
	fx := deploytest.Ostree(t)
	realFileCommands(t)

	res, err := post.WriteLocaleSettings(fx.Path, post.LocaleConfig{})
	if err != nil || len(res.Wrote) != 0 {
		t.Errorf("empty config: %v, %v", res, err)
	}
	res, err = post.WriteLocaleSettings(fx.Path, post.LocaleConfig{Keyboard: &post.Keyboard{Layout: "us"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Wrote, ",") != "/etc/vconsole.conf,/etc/X11/xorg.conf.d/00-keyboard.conf" {
		t.Errorf("wrote %v", res.Wrote)
	}
	for _, rel := range []string{"locale.conf", "localtime"} {
		if _, err := os.Lstat(filepath.Join(fx.Etc, rel)); err == nil {
			t.Errorf("wrote %s although the recipe did not set it", rel)
		}
	}
	x11, _ := os.ReadFile(filepath.Join(fx.Etc, "X11", "xorg.conf.d", "00-keyboard.conf"))
	if strings.Contains(string(x11), "XkbVariant") || strings.Contains(string(x11), "XkbModel") {
		t.Errorf("empty variant/model written:\n%s", x11)
	}
}

// A zone the installed image does not have is not linked: a dangling
// /etc/localtime is worse than the image's own.
func TestWriteLocaleSettings_ZoneNotInImage(t *testing.T) {
	fx := deploytest.Ostree(t)
	realFileCommands(t)
	res, err := post.WriteLocaleSettings(fx.Path, post.LocaleConfig{Timezone: "Mars/Olympus_Mons"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Wrote) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "Mars/Olympus_Mons") {
		t.Errorf("wrote %v, skipped %v", res.Wrote, res.Skipped)
	}
	if _, err := os.Lstat(filepath.Join(fx.Etc, "localtime")); err == nil {
		t.Error("linked /etc/localtime to a zone the image does not have")
	}
}

func TestEnsureKernelArgs(t *testing.T) {
	dir := t.TempDir()
	entries := filepath.Join(dir, "boot", "loader", "entries")
	if err := os.MkdirAll(entries, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(entries, "a.conf")
	write := func(s string) {
		if err := os.WriteFile(entry, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string { b, _ := os.ReadFile(entry); return string(b) }

	write("title X\noptions root=UUID=1  rw\nlinux /vmlinuz\n")
	n, err := post.EnsureKernelArgs(dir, []string{"rd.vconsole.keymap=de"})
	if err != nil || n != 1 {
		t.Fatalf("add: %d, %v", n, err)
	}
	// Appended; the rest of the line is untouched, double space included.
	if got := read(); got != "title X\noptions root=UUID=1  rw rd.vconsole.keymap=de\nlinux /vmlinuz\n" {
		t.Errorf("after add: %q", got)
	}
	// Idempotent.
	if n, err := post.EnsureKernelArgs(dir, []string{"rd.vconsole.keymap=de"}); err != nil || n != 0 {
		t.Errorf("second run: %d, %v", n, err)
	}
	// Another value replaces the key; it is not there twice.
	if n, err := post.EnsureKernelArgs(dir, []string{"rd.vconsole.keymap=fr"}); err != nil || n != 1 {
		t.Errorf("replace: %d, %v", n, err)
	}
	if got := read(); strings.Count(got, "rd.vconsole.keymap=") != 1 || !strings.Contains(got, "rd.vconsole.keymap=fr") {
		t.Errorf("after replace: %q", got)
	}
	// No entries at all is an error the caller reports.
	if _, err := post.EnsureKernelArgs(t.TempDir(), []string{"x=1"}); err == nil {
		t.Error("no error without BLS entries")
	}
}
