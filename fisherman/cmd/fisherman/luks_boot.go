package main

import (
	"fmt"

	"github.com/tuna-os/fisherman/internal/post"
)

// luksNeedsKarg reports whether an encrypted install depends on the
// rd.luks.name kernel argument to find its root. systemd-boot with composefs
// does not: the root partition carries the Linux root GPT type, and
// systemd-gpt-auto-generator unlocks it. Every other layout (GRUB, or
// systemd-boot on ostree) has no such fallback.
func luksNeedsKarg(isSystemdBoot, composeFsBackend bool) bool {
	return !isSystemdBoot || !composeFsBackend
}

// ensureLuksBoot checks, and repairs where it can, that the installed
// system's loader entries unlock the LUKS root (post.RequireLuksArgs). It
// returns the line to log. The error is non-nil only when the install must
// stop: the argument is required (luksNeedsKarg) and is not proven to be in
// every loader entry. Reporting "complete" then would hand over a system that
// cannot find its root at the first boot (#410).
func ensureLuksBoot(target, luksUUID string, required bool) (string, error) {
	res, err := post.RequireLuksArgs(target, luksUUID)
	if err == nil {
		return fmt.Sprintf("rd.luks.name is in %d boot entr%s (%d patched here)",
			res.Entries, map[bool]string{true: "y", false: "ies"}[res.Entries == 1], res.Patched), nil
	}
	if !required {
		return fmt.Sprintf("Warning: could not inject LUKS boot args (GPT auto-discovery still finds the root): %v", err), nil
	}
	return "", fmt.Errorf("the encrypted root would not unlock at boot: %w", err)
}
