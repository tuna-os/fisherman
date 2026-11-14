package main

import (
	"fmt"
	"strings"

	"github.com/tuna-os/fisherman/internal/images"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// extraCatalogPaths are where the frontends in tuna-os/bootc-installer look
// for an operator's catalog, before their bundled one. fisherman reads them
// too, so an ISO that ships its own catalog decides its own extras.
var extraCatalogPaths = []string{
	"/etc/bootc-installer/images.json",
	"/etc/tuna-installer/images.json",
}

// loadCatalogs returns every image catalog fisherman can find, in priority
// order. Replaceable in tests.
var loadCatalogs = func() []*images.Catalog {
	var out []*images.Catalog
	for _, p := range append(append([]string{}, extraCatalogPaths...), images.FindPaths()...) {
		if c, err := images.Load(p); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// resolveExtras decides which desktop and product post-install steps run,
// and returns them with a line that says where the decision came from.
//
//  1. The recipe's `extras` object, {} included, is used exactly as given.
//  2. Otherwise the image is looked up in the catalog (by any of refs: the
//     recipe's image and targetImgref, and the live or media image). The
//     catalog entry's `extras` apply.
//  3. Otherwise nothing is on: fisherman knows nothing about the image, and
//     does not enable network services or write product units
//     (tuna-os/fisherman#412). One exception: an encrypted root turns
//     bootSplash on. The passphrase prompt is the one boot-time dialog every
//     install has, and an image's Plymouth theme draws it only when the
//     kernel command line asks for the splash; without `rhgb quiet` the
//     prompt is the bare text console. An image that ships the arguments in
//     kargs.d is unaffected: an argument already present is not added again.
func resolveExtras(r *recipe.Recipe, refs ...string) (recipe.Extras, string) {
	describe := func(e recipe.Extras, from string) string {
		on := e.Names()
		if len(on) == 0 {
			return "Desktop extras: none (" + from + ")"
		}
		return "Desktop extras: " + strings.Join(on, ", ") + " (" + from + ")"
	}
	if r.Extras != nil {
		return *r.Extras, describe(*r.Extras, "from the recipe")
	}
	for _, c := range loadCatalogs() {
		for _, ref := range refs {
			if ref == "" {
				continue
			}
			leaf := c.LookupRef(ref)
			if leaf == nil {
				continue
			}
			res := leaf.Resolve(c)
			if res.Extras == nil {
				return recipe.Extras{}, describe(recipe.Extras{},
					fmt.Sprintf("the catalog entry %s sets none", leaf.Breadcrumb()))
			}
			e := recipe.Extras{
				PrintServices: res.Extras.PrintServices,
				OEMSetup:      res.Extras.OEMSetup,
				AudioNames:    res.Extras.AudioNames,
				BootSplash:    res.Extras.BootSplash,
			}
			return e, describe(e, "from the image catalog: "+leaf.Breadcrumb())
		}
	}
	if r.EncryptsRoot() {
		e := recipe.Extras{BootSplash: true}
		return e, describe(e, "the recipe has no `extras`, the image is not in the catalog and the root is encrypted: the passphrase prompt needs the splash arguments")
	}
	return recipe.Extras{}, describe(recipe.Extras{},
		"the recipe has no `extras` and the image is not in the catalog")
}

// liveOnce returns a function that says whether the host booted from live
// media. known is what the install already found out (it probes only for a
// recipe with no image, and a verified media input is live media); when that
// is not "live", the host is probed, at most once, and only if asked.
func liveOnce(known probe.Live) func() bool {
	if known.IsLive {
		return func() bool { return true }
	}
	var asked, isLive bool
	return func() bool {
		if !asked {
			isLive, asked = hostLive().IsLive, true
		}
		return isLive
	}
}
