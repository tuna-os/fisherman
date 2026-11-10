package main

import (
	"fmt"
	"strings"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// applyLocale writes the recipe's locale, keyboard layout and time zone
// into the installed system, and logs what it wrote. With none of the three
// in the recipe it does nothing and says nothing: the image's settings stay.
//
// For an encrypted install the console keymap is also put on the kernel
// command line as rd.vconsole.keymap=. The passphrase prompt runs in the
// initrd, before /etc/vconsole.conf of the installed system can be read; a
// user who typed the passphrase on a German layout in the installer would
// otherwise face a US keymap at first boot and could not unlock the disk
// (tuna-os/fisherman#405).
//
// Nothing here is fatal: the OS is installed. A failure is a warning that
// says what the user will see instead.
func applyLocale(target string, r *recipe.Recipe, encrypted bool) {
	cfg := post.LocaleConfig{Locale: r.Locale, Timezone: r.Timezone}
	if r.Keyboard != nil {
		cfg.Keyboard = &post.Keyboard{Layout: r.Keyboard.Layout, Variant: r.Keyboard.Variant, Model: r.Keyboard.Model}
	}
	if cfg.Locale == "" && cfg.Keyboard == nil && cfg.Timezone == "" {
		return
	}
	res, err := post.WriteLocaleSettings(target, cfg)
	if len(res.Wrote) > 0 {
		progress.Info("Language, keyboard and time zone: wrote " + strings.Join(res.Wrote, ", "))
	}
	for _, s := range res.Skipped {
		progress.Info("Warning: " + s + "; the image's setting is kept")
	}
	if err != nil {
		progress.Info(fmt.Sprintf("Warning: could not write language, keyboard and time zone settings: %v", err))
	}

	if !encrypted || res.ConsoleKeymap == "" {
		return
	}
	arg := "rd.vconsole.keymap=" + res.ConsoleKeymap
	n, err := post.EnsureKernelArgs(target, []string{arg})
	switch {
	case err != nil:
		progress.Info(fmt.Sprintf(
			"Warning: could not pass the keyboard layout to the initrd (%v): the disk passphrase prompt uses the image's default keymap", err))
	case n > 0:
		progress.Info(fmt.Sprintf("Added %s to %d boot entr%s, for the disk passphrase prompt",
			arg, n, map[bool]string{true: "y", false: "ies"}[n == 1]))
	default:
		progress.Info(arg + " is already in every boot entry")
	}
}
