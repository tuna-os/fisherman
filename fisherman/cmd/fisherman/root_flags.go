package main

import (
	"errors"
	"fmt"

	"github.com/tuna-os/fisherman/internal/post"
)

// ensureRootFlags makes the installed system's loader entries mount the
// root with the recipe's options (post.RequireRootFlags): bootc 1.15.x
// drops --karg on the composefs path, so rootflags=compress=zstd:1 and the
// recipe's subvolume never reached the entry. It returns the line to log;
// "" when the recipe has no root mount options. An image without loader
// entries (a unified kernel image) is a warning: its command line is fixed.
// Any other failure is an error: the entries exist and do not say what the
// recipe asked for.
func ensureRootFlags(target, options string) (string, error) {
	if options == "" {
		return "", nil
	}
	res, err := post.RequireRootFlags(target, options)
	switch {
	case err == nil:
		return fmt.Sprintf("rootflags=%s is in %d boot entr%s (%d patched here)", options,
			res.Entries, map[bool]string{true: "y", false: "ies"}[res.Entries == 1], res.Patched), nil
	case errors.Is(err, post.ErrNoLoaderEntries):
		return fmt.Sprintf("Warning: no BLS loader entry to carry rootflags=%s; the image's own command line decides how the root is mounted", options), nil
	}
	return "", fmt.Errorf("the root would not be mounted with %s: %w", options, err)
}
