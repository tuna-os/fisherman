package main

import (
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/images"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
)

func withCatalog(t *testing.T, cs ...*images.Catalog) {
	t.Helper()
	old := loadCatalogs
	loadCatalogs = func() []*images.Catalog { return cs }
	t.Cleanup(func() { loadCatalogs = old })
}

func productCatalog() *images.Catalog {
	return &images.Catalog{Images: []*images.Node{
		{Name: "Product", Registry: "ghcr.io/acme/product",
			Extras: &images.Extras{PrintServices: true, OEMSetup: true, AudioNames: true, BootSplash: true},
			Children: []*images.Node{
				{Name: "Stable", Tag: "stable"},
				{Name: "Server", Tag: "server", Extras: &images.Extras{}},
			}},
		{Name: "NoOpinion", Imgref: "quay.io/acme/plain:1"},
	}}
}

// tuna-os/fisherman#412: the desktop and product steps are off for an image
// fisherman knows nothing about. The recipe decides first, then the catalog.
func TestResolveExtras(t *testing.T) {
	all := recipe.Extras{PrintServices: true, OEMSetup: true, AudioNames: true, BootSplash: true}
	tests := []struct {
		name     string
		r        recipe.Recipe
		refs     []string
		want     recipe.Extras
		wantLine string
	}{
		{"unknown image, no extras: nothing",
			recipe.Recipe{Image: "quay.io/centos-bootc/centos-bootc:stream10"}, nil,
			recipe.Extras{}, "Desktop extras: none (the recipe has no `extras` and the image is not in the catalog)"},
		{"catalog image: its extras",
			recipe.Recipe{Image: "ghcr.io/acme/product:stable"}, nil,
			all, "Desktop extras: printServices, oemSetup, audioNames, bootSplash (from the image catalog: Product › Stable)"},
		{"catalog image by targetImgref, source is a local store",
			recipe.Recipe{Image: "containers-storage:localhost/x", TargetImgref: "ghcr.io/acme/product:stable"}, nil,
			all, "from the image catalog: Product › Stable"},
		{"catalog image by the live image (empty recipe image)",
			recipe.Recipe{}, []string{"ghcr.io/acme/product:stable"},
			all, "from the image catalog: Product › Stable"},
		{"catalog leaf that turns them off",
			recipe.Recipe{Image: "ghcr.io/acme/product:server"}, nil,
			recipe.Extras{}, "Desktop extras: none (from the image catalog: Product › Server)"},
		{"catalog leaf with no opinion",
			recipe.Recipe{Image: "quay.io/acme/plain:1"}, nil,
			recipe.Extras{}, "Desktop extras: none (the catalog entry NoOpinion sets none)"},
		{"recipe wins over the catalog: {} is off",
			recipe.Recipe{Image: "ghcr.io/acme/product:stable", Extras: &recipe.Extras{}}, nil,
			recipe.Extras{}, "Desktop extras: none (from the recipe)"},
		{"recipe turns one on for an unknown image",
			recipe.Recipe{Image: "quay.io/centos-bootc/centos-bootc:stream10", Extras: &recipe.Extras{BootSplash: true}}, nil,
			recipe.Extras{BootSplash: true}, "Desktop extras: bootSplash (from the recipe)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withCatalog(t, productCatalog())
			refs := append([]string{tt.r.TargetImgref, tt.r.Image}, tt.refs...)
			got, line := resolveExtras(&tt.r, refs...)
			if got != tt.want {
				t.Errorf("extras = %+v, want %+v", got, tt.want)
			}
			if !strings.Contains(line, tt.wantLine) {
				t.Errorf("line = %q, want it to contain %q", line, tt.wantLine)
			}
		})
	}
}

// No catalog on the host at all (a bare fisherman binary): nothing is on.
func TestResolveExtras_NoCatalog(t *testing.T) {
	withCatalog(t)
	got, _ := resolveExtras(&recipe.Recipe{Image: "ghcr.io/ublue-os/bluefin:stable"}, "ghcr.io/ublue-os/bluefin:stable")
	if got != (recipe.Extras{}) {
		t.Errorf("extras = %+v with no catalog", got)
	}
}

// An operator's catalog is read before the bundled one.
func TestResolveExtras_FirstCatalogWins(t *testing.T) {
	operator := &images.Catalog{Images: []*images.Node{
		{Name: "Stable", Imgref: "ghcr.io/acme/product:stable", Extras: &images.Extras{BootSplash: true}},
	}}
	withCatalog(t, operator, productCatalog())
	got, _ := resolveExtras(&recipe.Recipe{Image: "ghcr.io/acme/product:stable"}, "ghcr.io/acme/product:stable")
	if got != (recipe.Extras{BootSplash: true}) {
		t.Errorf("extras = %+v, want the operator catalog's", got)
	}
}

func TestLiveOnce(t *testing.T) {
	old := hostLive
	t.Cleanup(func() { hostLive = old })
	probes := 0
	hostLive = func() probe.Live { probes++; return probe.Live{IsLive: true} }

	// Already known to be live: no probe.
	f := liveOnce(probe.Live{IsLive: true})
	first, second := f(), f()
	if !first || !second || probes != 0 {
		t.Errorf("known live: %v %v, probes = %d", first, second, probes)
	}
	// Not known: one probe, however often it is asked; none if never asked.
	f = liveOnce(probe.Live{})
	if probes != 0 {
		t.Error("probed before being asked")
	}
	first, second = f(), f()
	if !first || !second || probes != 1 {
		t.Errorf("%v %v, probes = %d, want 1", first, second, probes)
	}
	hostLive = func() probe.Live { return probe.Live{} }
	if liveOnce(probe.Live{})() {
		t.Error("an installed host reported as live")
	}
}
