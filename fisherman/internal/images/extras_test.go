package images_test

import (
	"testing"

	"github.com/tuna-os/fisherman/internal/images"
)

func extrasCatalog() *images.Catalog {
	return &images.Catalog{Images: []*images.Node{
		{Name: "Product", Registry: "ghcr.io/acme/product",
			Extras: &images.Extras{PrintServices: true, AudioNames: true},
			Children: []*images.Node{
				{Name: "Stable", Tag: "stable", NvidiaImgref: "ghcr.io/acme/product-nvidia:stable"},
				{Name: "Server", Tag: "server", Extras: &images.Extras{}},
			}},
		{Name: "Plain", Imgref: "quay.io/acme/plain:1"},
	}}
}

func TestResolve_Extras(t *testing.T) {
	c := extrasCatalog()
	product := c.Images[0]

	// A leaf inherits the ancestor's object.
	res := (&images.NodeResult{Path: []*images.Node{product}, Node: product.Children[0]}).Resolve(c)
	if res.Extras == nil || !res.Extras.PrintServices || !res.Extras.AudioNames || res.Extras.OEMSetup || res.Extras.BootSplash {
		t.Errorf("inherited extras = %+v", res.Extras)
	}
	if res.NvidiaImgref != "ghcr.io/acme/product-nvidia:stable" {
		t.Errorf("NvidiaImgref = %q", res.NvidiaImgref)
	}
	// A leaf's own object replaces it whole: {} turns everything off.
	res = (&images.NodeResult{Path: []*images.Node{product}, Node: product.Children[1]}).Resolve(c)
	if res.Extras == nil || *res.Extras != (images.Extras{}) {
		t.Errorf("own empty extras = %+v, want all off", res.Extras)
	}
	// No ancestor sets it: nil, not a zero object.
	res = (&images.NodeResult{Node: c.Images[1]}).Resolve(c)
	if res.Extras != nil {
		t.Errorf("extras = %+v, want nil", res.Extras)
	}
	// The resolved object is a copy.
	res = (&images.NodeResult{Path: []*images.Node{product}, Node: product.Children[0]}).Resolve(c)
	res.Extras.PrintServices = false
	if !product.Extras.PrintServices {
		t.Error("Resolve handed out the catalog's own object")
	}
}

func TestNormalizeRef(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/Acme/Product:Stable":                              "ghcr.io/acme/product:stable",
		"docker://ghcr.io/acme/product:stable":                     "ghcr.io/acme/product:stable",
		"containers-storage:ghcr.io/acme/product:stable":           "ghcr.io/acme/product:stable",
		"ostree-unverified-registry:ghcr.io/acme/product:stable":   "ghcr.io/acme/product:stable",
		"ostree-image-signed:docker://ghcr.io/acme/product:stable": "ghcr.io/acme/product:stable",
		"ghcr.io/acme/product@sha256:abcd":                         "ghcr.io/acme/product",
		"ghcr.io/acme/product:stable@sha256:abcd":                  "ghcr.io/acme/product:stable",
		"  ": "",
	} {
		if got := images.NormalizeRef(in); got != want {
			t.Errorf("NormalizeRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupRef(t *testing.T) {
	c := extrasCatalog()
	tests := []struct {
		ref, want string
	}{
		{"ghcr.io/acme/product:stable", "Stable"},
		{"ghcr.io/acme/product:server", "Server"},
		{"docker://ghcr.io/acme/product:stable", "Stable"},
		{"ghcr.io/acme/product-nvidia:stable", "Stable"},  // the NVIDIA variant of the leaf
		{"ghcr.io/acme/product@sha256:0123", "Stable"},    // digest pin: first leaf of the repository
		{"ghcr.io/acme/product:some-other-tag", "Stable"}, // unknown tag of a known repository
		{"quay.io/acme/plain:1", "Plain"},
		{"quay.io/centos-bootc/centos-bootc:stream10", ""}, // not in the catalog
		{"ghcr.io/acme/product-extra:stable", ""},          // a prefix of a name is not the name
		{"localhost:5000/acme/product:stable", ""},         // another registry
		{"oci:/run/media/layout", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := ""
		if leaf := c.LookupRef(tt.ref); leaf != nil {
			got = leaf.Node.Name
		}
		if got != tt.want {
			t.Errorf("LookupRef(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestLeaves(t *testing.T) {
	leaves := extrasCatalog().Leaves()
	if len(leaves) != 3 {
		t.Fatalf("leaves = %d, want 3", len(leaves))
	}
	if leaves[0].Breadcrumb() != "Product › Stable" {
		t.Errorf("breadcrumb = %q", leaves[0].Breadcrumb())
	}
}

// Every image in the shipped catalog got these steps before `extras`
// existed, on every install. The catalog keeps that for them: each leaf
// resolves all four. An image that is not in the catalog gets none.
func TestShippedCatalog_EveryLeafKeepsItsExtras(t *testing.T) {
	c, err := images.Load(shippedCatalogPath(t))
	if err != nil {
		t.Fatal(err)
	}
	leaves := c.Leaves()
	if len(leaves) == 0 {
		t.Fatal("no leaves")
	}
	all := images.Extras{PrintServices: true, OEMSetup: true, AudioNames: true, BootSplash: true}
	for _, leaf := range leaves {
		res := leaf.Resolve(c)
		if res.Extras == nil || *res.Extras != all {
			t.Errorf("%s: extras = %+v, want all four", leaf.Breadcrumb(), res.Extras)
		}
	}
	if c.LookupRef("quay.io/centos-bootc/centos-bootc:stream10") != nil {
		t.Error("centos-bootc is in the catalog")
	}
	if leaf := c.LookupRef("ghcr.io/ublue-os/bluefin:stable"); leaf == nil {
		t.Error("bluefin:stable not found by reference")
	}
}
