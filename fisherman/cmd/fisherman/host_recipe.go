package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Host facts a recipe is checked and completed against. They come from the
// same Prober as `fisherman probe`, so the install, `validate` and the probe a
// frontend shows can never disagree about whether this is live media or where
// the offline stores are. Tests replace newProber (probe.go).
var (
	hostLive           = func() probe.Live { return newProber().Live() }
	hostOffline        = func() probe.Offline { return newProber().Offline() }
	hostMediaInput     = func() (*install.MediaInput, error) { return newProber().MediaInput() }
	hostMediaSandboxed = runner.InFlatpak
	hostMediaRequired  = func() (bool, error) {
		path := "/etc/bootc-installer/live-iso-mode"
		if runner.InFlatpak() {
			path = "/run/host" + path
		}
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		return err == nil, err
	}
)

// Test seam for consumer ordering; production uses the shared strict validator.
var preflightBoundMediaInput = install.PreflightBoundOCIMediaInput

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

// resolveMediaInput selects the host-owned source before any image-cache
// inspection. The live marker makes the embedded input mandatory, independent
// of the frontend's recipe spelling; an incompatible explicit request refuses.
// additionalImageStores remains exclusively containers-storage graphroots.
func resolveMediaInput(r *recipe.Recipe) (*install.MediaInput, error) {
	required, err := hostMediaRequired()
	if err != nil {
		return nil, fmt.Errorf("reading live media marker: %w", err)
	}
	if required && hostMediaSandboxed() {
		return nil, fmt.Errorf("required live media installation must run natively on the host, not inside a Flatpak sandbox")
	}
	mi, err := hostMediaInput()
	if err != nil {
		if !required {
			return nil, nil
		}
		return nil, err
	}
	if mi == nil {
		if required {
			return nil, fmt.Errorf("live media requires %s", probe.MediaInputPath)
		}
		return nil, nil
	}
	if mi.OCILayoutPath == "" {
		if !required {
			return nil, nil
		}
		return nil, fmt.Errorf("required media input has no OCI layout")
	}
	explicitOCI := install.IsOCISource(r.Image)
	exactOCI := explicitOCI && r.Image == "oci:"+mi.OCILayoutPath
	if explicitOCI && !exactOCI && filepath.Clean(strings.TrimPrefix(r.Image, "oci:")) == filepath.Clean(mi.OCILayoutPath) {
		return nil, fmt.Errorf("explicit OCI binding uses a noncanonical path alias")
	}
	if r.Image != "" && r.Image != mi.Image && !exactOCI {
		if required {
			return nil, fmt.Errorf("requested image %q does not match embedded source %q", r.Image, mi.Image)
		}
		return nil, nil
	}
	if r.TargetImgref != "" && r.TargetImgref != mi.TargetImgref {
		return nil, fmt.Errorf("requested target %q does not match media target %q", r.TargetImgref, mi.TargetImgref)
	}
	if err := preflightBoundMediaInput(mi); err != nil {
		return nil, err
	}
	return mi, nil
}
