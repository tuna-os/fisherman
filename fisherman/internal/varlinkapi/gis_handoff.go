package varlinkapi

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// gnome-initial-setup reads the first vendor file that exists: the one in
// /etc, else the one in /usr/share. Its [pages] skip key lists pages it
// never shows (gnome-initial-setup.c, pages_to_skip_from_file).
const (
	gisEtcVendorConf = "etc/gnome-initial-setup/vendor.conf"
	gisUsrVendorConf = "usr/share/gnome-initial-setup/vendor.conf"
)

// gisHandoff hides the language and keyboard pages of gnome-initial-setup
// on the installed system when the install carried that setting from the
// live session. gnome-setup asks for both in the live session; without this,
// the first boot asks again (owner decision 2026-11-26).
//
// A page is hidden only when its copy was requested, the running system
// had a real value to copy (a LANG other than C or POSIX in
// /etc/locale.conf, a KEYMAP or XKBLAYOUT in /etc/vconsole.conf), and the
// deployment now holds that same value. Otherwise nothing is written and
// the page stays. The skip list in the deployment's
// vendor file is kept and extended. A deployment without gnome-initial-setup
// is left alone. It returns the pages it added.
func (r Real) gisHandoff(d string, t TargetConfig) ([]string, error) {
	host := r.Root
	if host == "" {
		host = "/"
	}
	var pages []string
	carried := func(rel, key string) bool {
		v := envValue(filepath.Join(host, rel), key)
		return v != "" && envValue(filepath.Join(d, rel), key) == v
	}
	if t.Locale == "" && t.CopyLocale && realLocale(envValue(filepath.Join(host, "etc/locale.conf"), "LANG")) &&
		carried("etc/locale.conf", "LANG") {
		pages = append(pages, "language")
	}
	if t.Keymap == "" && t.CopyKeymap && (carried("etc/vconsole.conf", "KEYMAP") || carried("etc/vconsole.conf", "XKBLAYOUT")) {
		pages = append(pages, "keyboard")
	}
	if len(pages) == 0 || !hasGIS(d) {
		return nil, nil
	}

	etc := filepath.Join(d, gisEtcVendorConf)
	text, err := os.ReadFile(etc)
	if errors.Is(err, fs.ErrNotExist) {
		// The /etc file replaces the /usr/share one, so start from it.
		text, err = os.ReadFile(filepath.Join(d, gisUsrVendorConf))
		if errors.Is(err, fs.ErrNotExist) {
			text, err = nil, nil
		}
	}
	if err != nil {
		return nil, err
	}
	skip := splitList(iniValue(string(text), "pages", "skip"))
	var added []string
	for _, p := range pages {
		if !inList(skip, p) {
			skip = append(skip, p)
			added = append(added, p)
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(etc), 0o755); err != nil {
		return nil, err
	}
	out := setINI(string(text), "pages", map[string]string{"skip": strings.Join(skip, ";")})
	return added, os.WriteFile(etc, []byte(out), 0o644)
}

func hasGIS(d string) bool {
	for _, p := range []string{"usr/libexec/gnome-initial-setup", "usr/lib/gnome-initial-setup/gnome-initial-setup", "usr/share/gnome-initial-setup"} {
		if _, err := os.Stat(filepath.Join(d, p)); err == nil {
			return true
		}
	}
	return false
}

func realLocale(lang string) bool {
	switch strings.SplitN(lang, ".", 2)[0] {
	case "", "C", "POSIX":
		return false
	}
	return true
}

// envValue reads KEY=value from an environment file such as locale.conf.
// Quotes around the value are removed. A missing file gives "".
func envValue(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	val := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && strings.TrimSpace(k) == key {
			val = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return val
}

// iniValue returns the value of key in [section], or "".
func iniValue(text, section, key string) string {
	in := false
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			in = l == "["+section+"]"
			continue
		}
		if k, v, ok := strings.Cut(l, "="); in && ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
