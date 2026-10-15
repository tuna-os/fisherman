package main

import (
	"io"
	"os"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// Host facts a recipe is checked and completed against. They come from the
// same Prober as `fisherman probe`, so the install, `validate` and the probe a
// frontend shows can never disagree about whether this is live media or where
// the offline stores are. Tests replace newProber (probe.go).
var (
	hostLive    = func() probe.Live { return newProber().Live() }
	hostOffline = func() probe.Offline { return newProber().Offline() }
)

// recipeStdin is where `fisherman -` reads its recipe; tests replace it.
var recipeStdin io.Reader = os.Stdin

// loadRecipe loads the recipe named on the command line: a file path, or
// recipe.StdinArg ("-") for stdin.
func loadRecipe(arg string) (*recipe.Recipe, error) {
	return recipe.LoadArg(arg, recipeStdin)
}

// checkImageSource rejects an empty image unless the host booted from live
// media, where empty means "install the running image". It asks the host
// only when the image is empty. The returned Live is the zero value when
// the image is set.
func checkImageSource(r *recipe.Recipe) (probe.Live, error) {
	if r.Image != "" {
		return probe.Live{}, nil
	}
	live := hostLive()
	return live, r.ValidateImage(live.IsLive)
}

// resolveImageStores returns the offline image stores to expose to bootc.
// A recipe that names additionalImageStores (even as []) is used as given;
// one that omits the field gets the stores found on the host, with probe's
// discovery. discovered reports which happened.
//
// The frontends used to do that discovery themselves, and four of them did
// it inside their Flatpak sandbox, where the host's /etc and /usr are not
// visible, so they found nothing or the wrong thing. fisherman runs on the
// host and sees the real paths.
func resolveImageStores(r *recipe.Recipe) (stores []string, discovered bool) {
	if r.AdditionalImageStores != nil {
		return r.AdditionalImageStores, false
	}
	return hostOffline().Stores, true
}
