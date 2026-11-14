package main

import (
	"strings"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// bootupdRequired is what `bootc install to-filesystem` prints, and exits 1
// on, when it deploys with the ostree layout an image that has no bootupd
// payload and --generic-image was not given.
const bootupdRequired = "bootupd is required for ostree-based installs"

// layoutHint explains a bootc failure that is a mismatch between the recipe's
// layout and the image. It returns "" for any other failure.
//
// The mismatch the fleet met (flounder-sid and guppy XFCE, tunaOS#4634): the
// live ISO's recipe.json says composeFsBackend=true, and an installer bundle
// older than that key ignored it, so fisherman ran the ostree layout on a
// composefs-native image. bootc then stopped at the bootloader with the one
// line above, after the disk was partitioned, and nothing said that the
// recipe, not the image, was wrong. fisherman cannot tell a stale bundle from
// a wrong recipe (it only sees the recipe it was given), so the hint names
// both and says what to look at.
func layoutHint(r *recipe.Recipe, err error) string {
	if err == nil || r == nil || r.ComposeFsBackend || r.GenericImage {
		return ""
	}
	if !strings.Contains(err.Error(), bootupdRequired) {
		return ""
	}
	return "the recipe asks for the ostree layout (composeFsBackend=false, genericImage=false) " +
		"but the image has no bootupd payload. A composefs-native image needs " +
		"composeFsBackend=true and bootloader=systemd; an ostree image without bootupd needs " +
		"genericImage=true. If the live ISO's recipe.json already says composeFsBackend=true, " +
		"the installer that built this recipe is older than that key: rebuild the ISO's installer bundle"
}
