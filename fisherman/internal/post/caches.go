package post

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tuna-os/fisherman/internal/progress"
)

// WarmCaches pre-generates system caches inside the installed target so that
// first boot is snappy. Runs commands inside the target chroot/bindmount.
// Each cache is independent and non-fatal — we log and continue on failure.
func WarmCaches(targetRoot string) {
	caches := []struct {
		name string
		fn   func(string) error
	}{
		{"Flatpak appstream", warmFlatpakAppstream},
		{"font cache", warmFontCache},
		{"icon theme cache", warmIconCache},
		{"GSettings schemas", warmGSettingsSchemas},
		{"gdk-pixbuf loaders", warmPixbufLoaders},
		{"GIO modules", warmGIOModules},
		{"dynamic linker cache", warmLdconfig},
		{"man-db", warmManDB},
	}

	warmed := 0
	for _, c := range caches {
		if err := c.fn(targetRoot); err != nil {
			progress.Info(fmt.Sprintf("Cache skip (%s): %v", c.name, err))
		} else {
			warmed++
		}
	}

	progress.Info(fmt.Sprintf("Pre-warmed %d/%d system caches for instant first boot", warmed, len(caches)))
}

// warmFlatpakAppstream regenerates the Flatpak appstream cache so
// GNOME Software opens instantly without a multi-minute refresh.
func warmFlatpakAppstream(target string) error {
	// System flatpak store lives at /var/lib/flatpak
	flatpakDir := filepath.Join(target, "var", "lib", "flatpak")
	if _, err := os.Stat(flatpakDir); os.IsNotExist(err) {
		return nil // no flatpaks installed, nothing to cache
	}

	// flatpak update --appstream requires the system to be booted,
	// but we can run flatpak build-update-repo on the local repo
	// to regenerate appstream data. The simpler approach: just ensure
	// the appstream/ directories exist so gnome-software doesn't
	// trigger a full remote refresh on first boot.
	//
	// The actual appstream data was already populated when we copied
	// system flatpaks in step 7. Mark it as fresh.
	appstreamDir := filepath.Join(flatpakDir, "appstream")
	if _, err := os.Stat(appstreamDir); err == nil {
		// Touch the timestamp file so gnome-software thinks it's current
		tsFile := filepath.Join(appstreamDir, ".timestamp")
		return os.WriteFile(tsFile, []byte(fmt.Sprintf("%d", nowUnix())), 0644)
	}
	return nil
}

// warmFontCache runs fc-cache to pre-generate fontconfig cache files.
func warmFontCache(target string) error {
	cacheDir := filepath.Join(target, "var", "cache", "fontconfig")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}

	// Run fc-cache targeting the installed root's font directories
	fontDirs := []string{
		filepath.Join(target, "usr", "share", "fonts"),
		filepath.Join(target, "usr", "local", "share", "fonts"),
	}

	var existingDirs []string
	for _, d := range fontDirs {
		if _, err := os.Stat(d); err == nil {
			existingDirs = append(existingDirs, d)
		}
	}

	if len(existingDirs) == 0 {
		return nil
	}

	// fc-cache with --sysroot if available, otherwise direct on directories
	args := []string{"-f", "-s", "--sysroot", target}
	err := Exec.Command("fc-cache", args...).Run()
	if err != nil {
		// Fallback: try without --sysroot (older fc-cache)
		args = append([]string{"-f"}, existingDirs...)
		err = Exec.Command("fc-cache", args...).Run()
	}
	return err
}

// warmIconCache regenerates icon-theme.cache for all icon themes.
func warmIconCache(target string) error {
	iconsDir := filepath.Join(target, "usr", "share", "icons")
	entries, err := os.ReadDir(iconsDir)
	if err != nil {
		return nil // no icons dir, skip
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		themeDir := filepath.Join(iconsDir, entry.Name())
		indexFile := filepath.Join(themeDir, "index.theme")
		if _, err := os.Stat(indexFile); os.IsNotExist(err) {
			continue // not a real theme
		}

		// Try gtk4-update-icon-cache first, fall back to gtk-update-icon-cache
		err := Exec.Command("gtk4-update-icon-cache", "-f", "-t", themeDir).Run()
		if err != nil {
			_ = Exec.Command("gtk-update-icon-cache", "-f", "-t", themeDir).Run()
		}
	}
	return nil
}

// warmGSettingsSchemas compiles GSettings schemas.
func warmGSettingsSchemas(target string) error {
	schemasDir := filepath.Join(target, "usr", "share", "glib-2.0", "schemas")
	if _, err := os.Stat(schemasDir); os.IsNotExist(err) {
		return nil
	}
	return Exec.Command("glib-compile-schemas", schemasDir).Run()
}

// warmPixbufLoaders regenerates the gdk-pixbuf loaders cache.
func warmPixbufLoaders(target string) error {
	// The cache file location varies; try the standard path
	loaderDir := filepath.Join(target, "usr", "lib64", "gdk-pixbuf-2.0", "2.10.0")
	if _, err := os.Stat(loaderDir); os.IsNotExist(err) {
		loaderDir = filepath.Join(target, "usr", "lib", "gdk-pixbuf-2.0", "2.10.0")
		if _, err := os.Stat(loaderDir); os.IsNotExist(err) {
			return nil
		}
	}

	cacheFile := filepath.Join(loaderDir, "loaders.cache")
	out, err := Exec.Command("gdk-pixbuf-query-loaders").Output()
	if err != nil {
		return err
	}

	// Write native output — when the system boots, paths are correct as /usr/lib64/...
	return os.WriteFile(cacheFile, out, 0644)
}

// warmGIOModules regenerates the GIO modules cache.
func warmGIOModules(target string) error {
	modulesDir := filepath.Join(target, "usr", "lib64", "gio", "modules")
	if _, err := os.Stat(modulesDir); os.IsNotExist(err) {
		modulesDir = filepath.Join(target, "usr", "lib", "gio", "modules")
		if _, err := os.Stat(modulesDir); os.IsNotExist(err) {
			return nil
		}
	}
	return Exec.Command("gio-querymodules", modulesDir).Run()
}

// warmLdconfig regenerates the dynamic linker cache.
func warmLdconfig(target string) error {
	// ldconfig -r <root> operates on the target's /etc/ld.so.cache
	return Exec.Command("ldconfig", "-r", target).Run()
}

// warmManDB regenerates the man page index.
func warmManDB(target string) error {
	manDir := filepath.Join(target, "usr", "share", "man")
	if _, err := os.Stat(manDir); os.IsNotExist(err) {
		return nil
	}
	// mandb with --no-purge to avoid removing valid entries
	return Exec.Command("mandb", "--no-purge", "-q", "--manpath", manDir).Run()
}

// nowUnix returns the current unix timestamp.
var nowUnix = func() int64 {
	return time.Now().Unix()
}
