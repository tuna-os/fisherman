package post

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tuna-os/fisherman/internal/deploy"
)

// Keyboard is an XKB keyboard configuration (the recipe's `keyboard`).
type Keyboard struct {
	Layout  string
	Variant string
	Model   string
}

// LocaleConfig is what the recipe says about language, keyboard and time
// zone. A zero field is left as the image has it.
type LocaleConfig struct {
	Locale   string    // e.g. "de_DE.UTF-8"
	Keyboard *Keyboard // nil: not set
	Timezone string    // IANA name, e.g. "Europe/Berlin"
}

// LocaleResult says what WriteLocaleSettings did.
type LocaleResult struct {
	// Wrote lists the files written, as the installed system names them.
	Wrote []string
	// ConsoleKeymap is the virtual-console keymap chosen for the keyboard
	// layout (the KEYMAP of vconsole.conf). Empty when no keyboard was set.
	ConsoleKeymap string
	// Skipped lists settings that were asked for and not applied, with the
	// reason.
	Skipped []string
}

// hostKbdModelMap is systemd's X11-layout-to-console-keymap table on the
// installer host, used when the installed image's own cannot be read.
var hostKbdModelMap = "/usr/share/systemd/kbd-model-map"

// WriteLocaleSettings writes the language, keyboard and time zone of the
// recipe into the installed system (tuna-os/fisherman#405), in the
// deployment's /etc from the resolver:
//
//	/etc/locale.conf                       LANG=
//	/etc/vconsole.conf                     KEYMAP=, XKBLAYOUT=, XKBVARIANT=, XKBMODEL=
//	/etc/X11/xorg.conf.d/00-keyboard.conf  the InputClass localectl writes
//	/etc/localtime                         -> ../usr/share/zoneinfo/<zone>
//
// These are the files systemd-localed and systemd-timedated own, so
// localectl and timedatectl show and change the same values afterwards.
// Other keys already in locale.conf and vconsole.conf are kept. Every file
// is written to a temporary name and renamed: on ostree an /etc file can
// share its inode with the repository.
//
// Values are assumed valid (recipe.CheckLocale, CheckKeyboard,
// CheckTimezone ran before any disk was touched).
func WriteLocaleSettings(target string, c LocaleConfig) (LocaleResult, error) {
	var res LocaleResult
	if c.Locale == "" && c.Keyboard == nil && c.Timezone == "" {
		return res, nil
	}
	d, err := ResolveDeployment(target)
	if err != nil {
		return res, err
	}

	if c.Locale != "" {
		if err := setEnvFileKeys(filepath.Join(d.Etc, "locale.conf"), map[string]string{"LANG": c.Locale}, nil); err != nil {
			return res, fmt.Errorf("locale.conf: %w", err)
		}
		res.Wrote = append(res.Wrote, "/etc/locale.conf")
	}

	if kb := c.Keyboard; kb != nil && kb.Layout != "" {
		res.ConsoleKeymap = ConsoleKeymap(*kb, kbdModelMapPaths(d)...)
		set := map[string]string{"KEYMAP": res.ConsoleKeymap, "XKBLAYOUT": kb.Layout}
		var unset []string
		for key, val := range map[string]string{"XKBVARIANT": kb.Variant, "XKBMODEL": kb.Model} {
			if val != "" {
				set[key] = val
			} else {
				unset = append(unset, key)
			}
		}
		if err := setEnvFileKeys(filepath.Join(d.Etc, "vconsole.conf"), set, unset); err != nil {
			return res, fmt.Errorf("vconsole.conf: %w", err)
		}
		res.Wrote = append(res.Wrote, "/etc/vconsole.conf")

		x11 := filepath.Join(d.Etc, "X11", "xorg.conf.d", "00-keyboard.conf")
		if err := writeFileAtomic(x11, []byte(x11KeyboardConf(*kb)), 0o644); err != nil {
			return res, fmt.Errorf("00-keyboard.conf: %w", err)
		}
		res.Wrote = append(res.Wrote, "/etc/X11/xorg.conf.d/00-keyboard.conf")
	}

	if c.Timezone != "" {
		// The zone must be in the image. An ostree deployment can be
		// looked at; a composefs /usr cannot, and the link is written.
		if present, known := d.HasUsrFile(filepath.Join("share", "zoneinfo", c.Timezone)); known && !present {
			res.Skipped = append(res.Skipped,
				fmt.Sprintf("time zone %s: the installed image has no /usr/share/zoneinfo/%s", c.Timezone, c.Timezone))
		} else {
			link := filepath.Join(d.Etc, "localtime")
			if err := os.MkdirAll(d.Etc, 0o755); err != nil {
				return res, err
			}
			tmp := link + ".fisherman-tmp"
			_ = os.Remove(tmp)
			// Relative, as timedatectl writes it: it resolves in the booted
			// root and in the mounted deployment alike.
			if err := os.Symlink("../usr/share/zoneinfo/"+c.Timezone, tmp); err != nil {
				return res, fmt.Errorf("localtime: %w", err)
			}
			if err := os.Rename(tmp, link); err != nil {
				_ = os.Remove(tmp)
				return res, fmt.Errorf("localtime: %w", err)
			}
			deploy.InheritLabel(link)
			res.Wrote = append(res.Wrote, "/etc/localtime")
		}
	}
	return res, nil
}

// kbdModelMapPaths lists where to look for systemd's kbd-model-map: the
// installed image first (its keymap names are the ones its initrd has),
// then the installer host.
func kbdModelMapPaths(d *deploy.Deployment) []string {
	var paths []string
	if usr, ok := d.Usr(); ok {
		paths = append(paths, filepath.Join(usr, "share", "systemd", "kbd-model-map"))
	}
	return append(paths, hostKbdModelMap)
}

// ConsoleKeymap returns the virtual-console keymap for an X11 keyboard
// configuration, the way systemd-localed converts one: from the first
// kbd-model-map (keymap, layout, model, variant, options per line) that has
// the layout, preferring the line whose variant matches. With no table, or
// no line for the layout, it is the layout's own name (with several
// layouts, the first), which is the console keymap's name for most layouts.
func ConsoleKeymap(kb Keyboard, mapPaths ...string) string {
	layout := kb.Layout
	if i := strings.IndexByte(layout, ','); i >= 0 {
		layout = layout[:i]
	}
	variant := kb.Variant
	if i := strings.IndexByte(variant, ','); i >= 0 {
		variant = variant[:i]
	}
	for _, p := range mapPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		best, bestScore := "", 0
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) < 4 || strings.HasPrefix(f[0], "#") {
				continue
			}
			mapLayout, mapVariant := f[1], f[3]
			if i := strings.IndexByte(mapLayout, ','); i >= 0 {
				mapLayout = mapLayout[:i]
			}
			if mapVariant == "-" {
				mapVariant = ""
			}
			if i := strings.IndexByte(mapVariant, ','); i >= 0 {
				mapVariant = mapVariant[:i]
			}
			if mapLayout != layout {
				continue
			}
			score := 1
			switch mapVariant {
			case variant:
				score = 3
			case "":
				score = 2
			}
			if score > bestScore {
				best, bestScore = f[0], score
			}
		}
		if best != "" {
			return best
		}
	}
	return layout
}

// x11KeyboardConf is the file `localectl set-x11-keymap` writes.
func x11KeyboardConf(kb Keyboard) string {
	var b strings.Builder
	b.WriteString("# Written by fisherman at install time, in the format of systemd-localed(8).\n")
	b.WriteString("# Use localectl(1) to change it: localectl set-x11-keymap LAYOUT [MODEL [VARIANT]]\n")
	b.WriteString("Section \"InputClass\"\n")
	b.WriteString("        Identifier \"system-keyboard\"\n")
	b.WriteString("        MatchIsKeyboard \"on\"\n")
	fmt.Fprintf(&b, "        Option \"XkbLayout\" \"%s\"\n", kb.Layout)
	if kb.Model != "" {
		fmt.Fprintf(&b, "        Option \"XkbModel\" \"%s\"\n", kb.Model)
	}
	if kb.Variant != "" {
		fmt.Fprintf(&b, "        Option \"XkbVariant\" \"%s\"\n", kb.Variant)
	}
	b.WriteString("EndSection\n")
	return b.String()
}

// setEnvFileKeys sets KEY=value lines in an environment file (locale.conf,
// vconsole.conf), removes the keys in unset, and keeps every other line.
func setEnvFileKeys(path string, set map[string]string, unset []string) error {
	drop := map[string]bool{}
	for _, k := range unset {
		drop[k] = true
	}
	var out []string
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			key, _, isAssign := strings.Cut(strings.TrimSpace(line), "=")
			if isAssign && !strings.HasPrefix(key, "#") {
				if _, replaced := set[key]; replaced || drop[key] {
					continue
				}
			}
			if line != "" || len(out) > 0 {
				out = append(out, line)
			}
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+set[k])
	}
	return writeFileAtomic(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// writeFileAtomic writes data to a temporary file beside path and renames
// it over path. It never writes through an existing file, which on ostree
// can be a hard link into the repository.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".fisherman-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, mode)
	}
	if werr == nil {
		werr = os.Rename(tmpName, path)
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return werr
	}
	deploy.InheritLabel(path)
	return nil
}

// SetHostKbdModelMap points the host fallback of ConsoleKeymap's table at
// another file and returns a function that restores it. For tests.
func SetHostKbdModelMap(path string) (restore func()) {
	old := hostKbdModelMap
	hostKbdModelMap = path
	return func() { hostKbdModelMap = old }
}
