package varlinkapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/sha512crypt"
)

// configureTarget applies ConfigureTarget's choices to every deployment of
// the mounted copy: locale, keymap, time zone and hostname through
// systemd-firstboot --root, the user account through useradd and chpasswd,
// autologin, and the privacy choice. Each deployment gets the same settings,
// because the installed system boots whichever deployment is current.
//
// root is the mounted target root and every deployment lies under it. The
// function refuses anything else: systemd-firstboot and useradd with a
// wrong --root would edit the machine that runs the installer.
//
// The returned strings are warnings (settings that could not be applied
// without failing the install).
func (r Real) configureTarget(ctx context.Context, root string, deployments []string, t TargetConfig) ([]string, error) {
	work := filepath.Clean(r.workDir())
	root = filepath.Clean(root)
	if root == "/" || root == "." || !strings.HasPrefix(root, work+string(filepath.Separator)) {
		return nil, fmt.Errorf("refusing to configure %q: it is not under %s", root, work)
	}
	var warns []string
	for _, d := range deployments {
		d = filepath.Clean(d)
		if !strings.HasPrefix(d, root+string(filepath.Separator)) {
			return warns, fmt.Errorf("refusing to configure %q: it is not under the target root", d)
		}
		w, err := r.configureDeployment(ctx, d, t)
		warns = append(warns, w...)
		if err != nil {
			return warns, err
		}
	}
	return dedupe(warns), nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (r Real) configureDeployment(ctx context.Context, d string, t TargetConfig) ([]string, error) {
	var warns []string
	c := r.cmd()

	// Locale, keymap, time zone, hostname. stdin stays empty so firstboot
	// can never prompt.
	args := []string{"--root=" + d, "--force"}
	// A value from ConfigureTarget wins over a copy from the running system.
	// systemd-firstboot copies a setting only when the running system has
	// one (/etc/locale.conf, /etc/vconsole.conf, /etc/localtime).
	switch {
	case t.Locale != "":
		args = append(args, "--locale="+t.Locale)
	case t.CopyLocale:
		args = append(args, "--copy-locale")
	}
	switch {
	case t.Keymap != "":
		args = append(args, "--keymap="+t.Keymap)
	case t.CopyKeymap:
		args = append(args, "--copy-keymap")
	}
	switch {
	case t.TimeZone != "":
		args = append(args, "--timezone="+t.TimeZone)
	case t.CopyTimezone:
		args = append(args, "--copy-timezone")
	}
	if t.Hostname != "" {
		args = append(args, "--hostname="+t.Hostname)
	}
	if len(args) > 2 {
		if _, err := c.Run(ctx, "systemd-firstboot", args, CmdOpts{}); err != nil {
			return warns, err
		}
		// Only after the copy succeeded: a carried language and keyboard
		// are not asked again on the first boot.
		if _, err := r.gisHandoff(d, t); err != nil {
			warns = append(warns, "gnome-initial-setup still asks for language and keyboard: "+err.Error())
		}
	}

	if u := t.User; u != nil && u.Username != "" {
		if err := r.createUser(ctx, d, u); err != nil {
			return warns, err
		}
		if t.Autologin {
			if w := autologin(d, u.Username); w != "" {
				warns = append(warns, w)
			}
		}
	} else if t.Autologin {
		warns = append(warns, "autologin needs a user account; none was configured")
	}

	if t.Privacy != nil {
		if err := writePrivacy(d, *t.Privacy); err != nil {
			return warns, err
		}
	}
	if t.Accessibility != nil {
		if err := writeAccessibility(d, *t.Accessibility); err != nil {
			return warns, err
		}
	}
	return warns, nil
}

// createUser adds the account to the deployment's /etc. The home directory
// is not created with useradd -m: on an ostree system /home is a symlink
// into /var, which is not part of the deployment. A tmpfiles.d entry
// creates it from /etc/skel at first boot instead, owned by the new user.
func (r Real) createUser(ctx context.Context, d string, u *User) error {
	home := "/home/" + u.Username
	args := []string{"--root", d, "--no-create-home", "--home-dir", home, "--shell", "/bin/bash"}
	if u.FullName != nil && *u.FullName != "" {
		args = append(args, "--comment", *u.FullName)
	}
	admin := u.Admin == nil || *u.Admin
	if admin {
		if g := adminGroup(d); g != "" {
			args = append(args, "--groups", g)
		}
	}
	args = append(args, u.Username)
	if _, err := r.cmd().Run(ctx, "useradd", args, CmdOpts{}); err != nil {
		return err
	}
	if u.Password != nil && u.Password.Reveal() != "" {
		// chpasswd --root chroots into the deployment and fails to lock its
		// /etc/passwd on a hardened host, and a hash on useradd's argument
		// list would show in the process table. Hash in process and edit
		// the shadow file instead. A pre-hashed setting, which validation
		// accepts, is stored as it is.
		hash, err := sha512crypt.ShadowHash(u.Password.Reveal())
		if err != nil {
			return fmt.Errorf("hashing the password: %w", err)
		}
		if err := setShadowHash(filepath.Join(d, "etc", "shadow"), u.Username, hash); err != nil {
			return err
		}
	}
	dir := filepath.Join(d, "etc", "tmpfiles.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("# Written by the installer: the home directory of %[1]s.\n"+
		"C %[2]s 0700 %[1]s %[1]s - /etc/skel\n"+
		"Z %[2]s - %[1]s %[1]s -\n", u.Username, home)
	return os.WriteFile(filepath.Join(dir, "tunaos-home-"+u.Username+".conf"), []byte(tmp), 0o644)
}

// adminGroup is the group that grants administrator rights on the image:
// wheel on Fedora-like systems, sudo on Debian-like ones.
func adminGroup(d string) string {
	b, err := os.ReadFile(filepath.Join(d, "etc", "group"))
	if err != nil {
		return ""
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if name, _, ok := strings.Cut(l, ":"); ok {
			have[name] = true
		}
	}
	for _, g := range []string{"wheel", "sudo"} {
		if have[g] {
			return g
		}
	}
	return ""
}

// autologin configures GDM, the one display manager the images share. It
// returns a warning when there is nothing to configure.
func autologin(d, user string) string {
	hasGDM := false
	for _, p := range []string{"usr/lib/systemd/system/gdm.service", "usr/sbin/gdm", "usr/bin/gdm"} {
		if fileExists(filepath.Join(d, p)) {
			hasGDM = true
		}
	}
	if !hasGDM {
		return "autologin is only configured for GDM; this image uses another display manager"
	}
	path := filepath.Join(d, "etc", "gdm", "custom.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "autologin could not be configured: " + err.Error()
	}
	existing, _ := os.ReadFile(path)
	out := setINI(string(existing), "daemon", map[string]string{
		"AutomaticLoginEnable": "True", "AutomaticLogin": user,
	})
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return "autologin could not be configured: " + err.Error()
	}
	return ""
}

// setINI sets keys in a section of an INI file, replacing a key that is
// there and adding the section when it is missing. Order of other lines is
// kept.
func setINI(text, section string, kv map[string]string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	hdr := "[" + section + "]"
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == hdr {
			start = i
			break
		}
	}
	if start < 0 {
		lines = append(lines, hdr)
		start = len(lines) - 1
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	// new keys go after the section's last line that says something
	insert := end
	for insert > start+1 && strings.TrimSpace(lines[insert-1]) == "" {
		insert--
	}
	for _, k := range sortedKeys(kv) {
		set := false
		for i := start + 1; i < end; i++ {
			name, _, ok := strings.Cut(lines[i], "=")
			if ok && strings.TrimSpace(name) == k {
				lines[i] = k + "=" + kv[k]
				set = true
				break
			}
		}
		if !set {
			lines = append(lines[:insert], append([]string{k + "=" + kv[k]}, lines[insert:]...)...)
			insert++
			end++
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// PrivacyConf is where the install records the privacy choices on the
// target. Images that honour them (location services for GNOME, geoclue)
// read this file; it is the one place the daemon's decision is stored.
const PrivacyConf = "etc/tunaos/privacy.conf"

func writePrivacy(d string, p Privacy) error {
	path := filepath.Join(d, PrivacyConf)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	val := "no"
	if p.LocationServices {
		val = "yes"
	}
	return os.WriteFile(path, []byte("[Privacy]\nLocationServices="+val+"\n"), 0o644)
}

// The accessibility choices of the live session (a screen reader, a display
// scale) reach each account of the installed system at its first login:
//
//   - AccessibilityConf records them, as PrivacyConf records privacy.
//   - AccessibilityScript applies them in the session, once per account. The
//     screen reader is the at-spi status that the session watches (COSMIC
//     starts orca when it turns on) and the GSettings key that at-spi keeps it
//     in (measured: at-spi-bus-launcher writes screen-reader-enabled and reads
//     it at its start). The scale goes to each enabled output through
//     cosmic-randr, where the image has it.
//   - AccessibilityAutostart starts the script in each session.
//
// Nothing here is per user: the daemon makes the account, but the first
// session makes the home's settings, and the choice is of the person who
// installs.
const (
	AccessibilityConf      = "etc/tunaos/accessibility.conf"
	AccessibilityScript    = "etc/tunaos/first-login-accessibility"
	AccessibilityAutostart = "etc/xdg/autostart/io.tunaos.FirstLoginAccessibility.desktop"
)

func writeAccessibility(d string, a Accessibility) error {
	if a.ScalePercent != 0 && (a.ScalePercent < 100 || a.ScalePercent > 300) {
		return fmt.Errorf("accessibility: scale %d%% is not 100 to 300", a.ScalePercent)
	}
	reader := "no"
	if a.ScreenReader {
		reader = "yes"
	}
	scale := ""
	if a.ScalePercent != 0 {
		scale = fmt.Sprintf("%d.%02d", a.ScalePercent/100, a.ScalePercent%100)
	}
	files := []struct {
		path string
		mode os.FileMode
		text string
	}{
		{AccessibilityConf, 0o644, fmt.Sprintf("[Accessibility]\nScreenReader=%s\nScalePercent=%d\n", reader, a.ScalePercent)},
		{AccessibilityScript, 0o755, accessibilityScript(a.ScreenReader, scale)},
		{AccessibilityAutostart, 0o644, "[Desktop Entry]\nType=Application\nName=Accessibility settings of the installer\n" +
			"Exec=/" + AccessibilityScript + "\nNoDisplay=true\nX-GNOME-Autostart-Phase=Applications\n"},
	}
	for _, f := range files {
		path := filepath.Join(d, f.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(f.text), f.mode); err != nil {
			return err
		}
		if err := os.Chmod(path, f.mode); err != nil {
			return err
		}
	}
	return nil
}

// accessibilityScript is the first-login script. scale is "" to leave the
// display scale, else "1.50".
func accessibilityScript(reader bool, scale string) string {
	var b strings.Builder
	b.WriteString(`#!/bin/sh
# The accessibility choices of the installer, once for each account
# (fisherman, ConfigureTarget accessibility). Delete this file and its
# autostart entry to stop it.
done="${XDG_STATE_HOME:-$HOME/.local/state}/tunaos/first-login-accessibility.done"
[ -e "$done" ] && exit 0
`)
	if reader {
		b.WriteString(`gsettings set org.gnome.desktop.a11y.applications screen-reader-enabled true
busctl --user set-property org.a11y.Bus /org/a11y/bus org.a11y.Status ScreenReaderEnabled b true
`)
	}
	if scale != "" {
		b.WriteString(`if command -v cosmic-randr >/dev/null; then
	# One line for each enabled output: NAME WIDTH HEIGHT REFRESH(mHz)
	cosmic-randr list --kdl | awk '
		/^output "/ { split($0, q, "\""); name = q[2]; on = ($0 ~ /enabled=#true/) }
		/^ *mode / && /current=#true/ && on { print name, $2, $3, $4 }
	' | while read -r name width height refresh; do
		cosmic-randr mode --scale ` + scale + ` --refresh "$((refresh / 1000)).$(printf %03d $((refresh % 1000)))" "$name" "$width" "$height"
	done
fi
`)
	}
	b.WriteString(`mkdir -p "$(dirname "$done")" && : >"$done"
`)
	return b.String()
}

// setShadowHash replaces the password field of user in a shadow file and
// keeps the file's mode.
func setShadowHash(path, user, hash string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	found := false
	for i, l := range lines {
		f := strings.Split(l, ":")
		if len(f) > 1 && f[0] == user {
			f[1] = hash
			lines[i] = strings.Join(f, ":")
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%s has no entry for %s", path, user)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), fi.Mode().Perm())
}
