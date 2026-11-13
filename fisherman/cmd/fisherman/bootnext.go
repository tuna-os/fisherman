package main

import (
	"fmt"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// setBootNextFn is post.SetBootNext; tests replace it.
var setBootNextFn = post.SetBootNext

// applyBootNext points the firmware's next boot at the installed system's
// boot entry, and returns the line to log. It never fails the install.
//
// A machine whose boot order still prefers the installer's USB stick
// restarts into the live medium after a successful install. BootNext fixes
// that for one boot. Writing it needs root, which fisherman is and the
// frontend is not (tuna-os/bootc-installer#318).
//
// It changes the firmware of the machine fisherman RUNS on. That is the
// machine being installed when the host is live media, so there it is the
// default. On any other host (CI, a workstation that writes a disk for
// another machine) the default is to leave the firmware alone. The recipe's
// setBootNext overrides both ways.
func applyBootNext(r *recipe.Recipe, bootID string, isLive func() bool) string {
	if bootID == "" {
		return "BootNext not set: no EFI boot entry for the installed system was found"
	}
	switch {
	case r.SetBootNext != nil && !*r.SetBootNext:
		return "BootNext not set: the recipe sets setBootNext to false"
	case r.SetBootNext == nil && !isLive():
		return fmt.Sprintf("BootNext not set: this host is not live media, and its firmware is not the installed machine's "+
			"(set setBootNext to true to boot Boot%s next)", bootID)
	}
	if err := setBootNextFn(bootID); err != nil {
		return fmt.Sprintf("Warning: could not set BootNext to Boot%s (the firmware's boot order decides the next boot): %v", bootID, err)
	}
	return fmt.Sprintf("BootNext set to Boot%s: the next boot starts the installed system", bootID)
}
