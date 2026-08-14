package images_test

// Tests for the image catalog package (internal/images).
//
// The package had zero coverage: it drives which images the installer offers,
// how those images resolve their effective configuration (registry/tag
// inheritance, flatpak aliases, composefs flags), and how the catalog is
// searched and loaded. All of it is pure logic — no external services — so
// the whole surface is exercised here with inline fixtures plus the shipped
// data/images.json as an end-to-end check.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/images"
)

func boolp(b bool) *bool { return &b }

// ── EffectiveImgref ──────────────────────────────────────────────────────────

func TestEffectiveImgref(t *testing.T) {
	tests := []struct {
		name string
		n    images.Node
		want string
	}{
		{name: "direct imgref wins", n: images.Node{Imgref: "ghcr.io/x/y:1", Registry: "ghcr.io/x", Tag: "2"}, want: "ghcr.io/x/y:1"},
		{name: "registry+tag combined", n: images.Node{Registry: "ghcr.io/x", Tag: "stable"}, want: "ghcr.io/x:stable"},
		{name: "registry only", n: images.Node{Registry: "ghcr.io/x"}, want: ""},
		{name: "tag only", n: images.Node{Tag: "stable"}, want: ""},
		{name: "empty", n: images.Node{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.n.EffectiveImgref(); got != tt.want {
				t.Errorf("EffectiveImgref() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── IsLeaf ───────────────────────────────────────────────────────────────────

func TestIsLeaf(t *testing.T) {
	tests := []struct {
		name string
		n    images.Node
		want bool
	}{
		{name: "group with children", n: images.Node{Name: "G", Children: []*images.Node{{Name: "C"}}}, want: false},
		{name: "leaf with imgref", n: images.Node{Imgref: "ghcr.io/x/y:1"}, want: true},
		{name: "leaf with registry+tag", n: images.Node{Registry: "ghcr.io/x", Tag: "stable"}, want: true},
		{name: "leaf with nvidia imgref", n: images.Node{NvidiaImgref: "ghcr.io/x/y:nvidia"}, want: true},
		{name: "node with children and imgref", n: images.Node{Imgref: "ghcr.io/x", Children: []*images.Node{{Name: "C"}}}, want: false},
		{name: "empty node", n: images.Node{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.n.IsLeaf(); got != tt.want {
				t.Errorf("IsLeaf() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ── Resolve ──────────────────────────────────────────────────────────────────

// fixtureCatalog builds a small three-level tree exercising inheritance.
func fixtureCatalog() *images.Catalog {
	return &images.Catalog{
		Aliases: map[string]string{"aurora_brew": "https://example.invalid/aurora/Brewfile"},
		Images: []*images.Node{
			{
				Name:             "Root",
				Registry:         "ghcr.io/ublue-os/base",
				Flatpaks:         "org.foo.app",
				Bootloader:       "grub",
				ComposeFsBackend: boolp(true),
				Children: []*images.Node{
					{
						Name:       "Child",
						Tag:        "stable",
						Filesystem: "xfs",
						Children: []*images.Node{
							{Name: "Leaf", Desc: "leaf desc"},
						},
					},
				},
			},
		},
	}
}

func leafResult(c *images.Catalog, names ...string) *images.NodeResult {
	// Walk the fixture tree to the node named by the last element, carrying
	// the ancestor path root-first (matching findIn's Path contract).
	var path []*images.Node
	var cur []*images.Node
	cur = c.Images
	for i, name := range names {
		for _, n := range cur {
			if n.Name == name {
				if i == len(names)-1 {
					cp := make([]*images.Node, len(path))
					copy(cp, path)
					return &images.NodeResult{Path: cp, Node: n}
				}
				path = append(path, n)
				cur = n.Children
				break
			}
		}
	}
	return nil
}

func TestResolve_InheritsAncestorFields(t *testing.T) {
	c := fixtureCatalog()
	res := leafResult(c, "Root", "Child", "Leaf").Resolve(c)

	if res.Name != "Leaf" {
		t.Errorf("Name = %q, want %q", res.Name, "Leaf")
	}
	// registry inherited from Root, tag from Child → combined imgref.
	if res.Imgref != "ghcr.io/ublue-os/base:stable" {
		t.Errorf("Imgref = %q, want %q", res.Imgref, "ghcr.io/ublue-os/base:stable")
	}
	if res.Flatpaks != "org.foo.app" {
		t.Errorf("Flatpaks = %q, want inherited %q", res.Flatpaks, "org.foo.app")
	}
	if res.Bootloader != "grub" {
		t.Errorf("Bootloader = %q, want inherited %q", res.Bootloader, "grub")
	}
	if res.Filesystem != "xfs" {
		t.Errorf("Filesystem = %q, want %q", res.Filesystem, "xfs")
	}
	if !res.ComposeFsBackend {
		t.Error("ComposeFsBackend = false, want inherited true")
	}
	if res.NeedsUserCreation {
		t.Error("NeedsUserCreation = true, want false (unset)")
	}
	if res.Desc != "leaf desc" {
		t.Errorf("Desc = %q, want %q", res.Desc, "leaf desc")
	}
}

func TestResolve_ExplicitBoolOverridesAncestor(t *testing.T) {
	c := fixtureCatalog()
	// Explicit false on the leaf must beat the ancestor's true.
	c.Images[0].Children[0].Children[0].ComposeFsBackend = boolp(false)
	res := leafResult(c, "Root", "Child", "Leaf").Resolve(c)
	if res.ComposeFsBackend {
		t.Error("ComposeFsBackend = true, want explicit false to override ancestor")
	}
}

func TestResolve_DeepestStringWins(t *testing.T) {
	c := fixtureCatalog()
	leaf := c.Images[0].Children[0].Children[0]
	leaf.Flatpaks = "org.deep.leaf"
	leaf.Bootloader = "systemd-boot"
	res := leafResult(c, "Root", "Child", "Leaf").Resolve(c)
	if res.Flatpaks != "org.deep.leaf" {
		t.Errorf("Flatpaks = %q, want deepest %q", res.Flatpaks, "org.deep.leaf")
	}
	if res.Bootloader != "systemd-boot" {
		t.Errorf("Bootloader = %q, want deepest %q", res.Bootloader, "systemd-boot")
	}
}

func TestResolve_DirectImgrefBeatsInherited(t *testing.T) {
	c := fixtureCatalog()
	leaf := c.Images[0].Children[0].Children[0]
	leaf.Imgref = "ghcr.io/custom/leaf:1"
	res := leafResult(c, "Root", "Child", "Leaf").Resolve(c)
	if res.Imgref != "ghcr.io/custom/leaf:1" {
		t.Errorf("Imgref = %q, want direct %q", res.Imgref, "ghcr.io/custom/leaf:1")
	}
}

func TestResolve_AliasFlatpaks(t *testing.T) {
	c := fixtureCatalog()
	leaf := c.Images[0].Children[0].Children[0]
	leaf.Flatpaks = "@aurora_brew"
	res := leafResult(c, "Root", "Child", "Leaf").Resolve(c)
	if res.Flatpaks != "https://example.invalid/aurora/Brewfile" {
		t.Errorf("Flatpaks = %q, want alias-resolved URL", res.Flatpaks)
	}
}

func TestResolve_AliasMissingFromCatalog(t *testing.T) {
	c := fixtureCatalog()
	leaf := c.Images[0].Children[0].Children[0]
	leaf.Flatpaks = "@nonexistent_alias"
	res := leafResult(c, "Root", "Child", "Leaf").Resolve(c)
	if res.Flatpaks != "@nonexistent_alias" {
		t.Errorf("Flatpaks = %q, want unresolved alias preserved", res.Flatpaks)
	}
}

func TestResolve_NoAliasLookupWithoutCatalog(t *testing.T) {
	c := fixtureCatalog()
	leaf := c.Images[0].Children[0].Children[0]
	leaf.Flatpaks = "@aurora_brew"
	res := leafResult(c, "Root", "Child", "Leaf").Resolve(nil)
	if res.Flatpaks != "@aurora_brew" {
		t.Errorf("Flatpaks = %q, want @-prefix preserved when catalog is nil", res.Flatpaks)
	}
}

// ── Breadcrumb ───────────────────────────────────────────────────────────────

func TestBreadcrumb(t *testing.T) {
	c := fixtureCatalog()
	if got := leafResult(c, "Root", "Child", "Leaf").Breadcrumb(); got != "Root › Child › Leaf" {
		t.Errorf("Breadcrumb() = %q, want %q", got, "Root › Child › Leaf")
	}
	// Single-node path.
	if got := leafResult(c, "Root").Breadcrumb(); got != "Root" {
		t.Errorf("Breadcrumb() = %q, want %q", got, "Root")
	}
}

// ── Find ─────────────────────────────────────────────────────────────────────

func TestFind(t *testing.T) {
	c := &images.Catalog{Images: []*images.Node{
		{
			Name:        "Aurora",
			SearchExtra: "ublue kde plasma",
			Registry:    "ghcr.io/ublue-os/aurora",
			Children: []*images.Node{
				{Name: "Aurora DX", Registry: "ghcr.io/ublue-os/aurora-dx", Tag: "stable"},
				{Name: "Beta", Tag: "beta"},
			},
		},
		{Name: "Ubuntu", Imgref: "ghcr.io/tuna-os/ubuntu:stable"},
	}}

	tests := []struct {
		name    string
		query   string
		wantLen int
		wantBc  string // breadcrumb of first result, when non-empty
	}{
		{name: "name match, case-insensitive", query: "aurora", wantLen: 1, wantBc: "Aurora"},
		{name: "name match on child", query: "aurora dx", wantLen: 1, wantBc: "Aurora › Aurora DX"},
		{name: "search_extra match", query: "KDE", wantLen: 1, wantBc: "Aurora"},
		{name: "imgref match", query: "tuna-os/ubuntu", wantLen: 1, wantBc: "Ubuntu"},
		// Contract as implemented: an empty query matches every top-level
		// group (a group match short-circuits recursion into its subtree).
		{name: "empty query matches all top-level groups", query: "", wantLen: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Find(tt.query)
			if len(got) != tt.wantLen {
				t.Fatalf("Find(%q) returned %d results, want %d", tt.query, len(got), tt.wantLen)
			}
			if tt.wantBc != "" && len(got) > 0 && got[0].Breadcrumb() != tt.wantBc {
				t.Errorf("first result breadcrumb = %q, want %q", got[0].Breadcrumb(), tt.wantBc)
			}
		})
	}
}

func TestFind_MatchedGroupIsNotRecursed(t *testing.T) {
	c := &images.Catalog{Images: []*images.Node{
		{
			Name: "Aurora",
			Children: []*images.Node{
				{Name: "Aurora DX"},
			},
		},
	}}
	got := c.Find("aurora")
	if len(got) != 1 {
		t.Fatalf("Find(%q) returned %d results, want 1 (group match stops recursion)", "aurora", len(got))
	}
	if got[0].Node.Name != "Aurora" {
		t.Errorf("matched node = %q, want the group node itself", got[0].Node.Name)
	}
}

// ── Load / FindPaths / LoadDefault ──────────────────────────────────────────

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	valid := filepath.Join(dir, "images.json")
	if err := os.WriteFile(valid, []byte(`{"default_image":"A","images":[{"name":"A","imgref":"ghcr.io/x/a:1"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := images.Load(valid)
	if err != nil {
		t.Fatalf("Load(valid): %v", err)
	}
	if c.DefaultImage != "A" || len(c.Images) != 1 || c.Images[0].Imgref != "ghcr.io/x/a:1" {
		t.Errorf("Load parsed catalog incorrectly: %+v", c)
	}

	if _, err := images.Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("Load(missing) expected error")
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := images.Load(bad); err == nil || !strings.Contains(err.Error(), "parsing") {
		t.Errorf("Load(malformed) error = %v, want parse error mentioning 'parsing'", err)
	}
}

func TestFindPaths_EnvVarFirst(t *testing.T) {
	t.Setenv("FISHERMAN_IMAGES_PATH", "/custom/images.json")
	paths := images.FindPaths()
	if len(paths) == 0 || paths[0] != "/custom/images.json" {
		t.Fatalf("FindPaths()[0] = %q, want env var value first", paths[0])
	}
}

func TestFindPaths_StandardLocationsPresent(t *testing.T) {
	t.Setenv("FISHERMAN_IMAGES_PATH", "")
	paths := images.FindPaths()
	joined := strings.Join(paths, "\n")
	for _, want := range []string{"/usr/share/fisherman/data/images.json", "/app/share/fisherman/data/images.json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("FindPaths() missing standard location %q; got %v", want, paths)
		}
	}
}

// shippedCatalogPath returns the repo's data/images.json relative to the
// package dir (tests run with cwd = internal/images), skipping when absent.
func shippedCatalogPath(t *testing.T) string {
	t.Helper()
	real := filepath.Join("..", "..", "..", "data", "images.json")
	if _, err := os.Stat(real); err != nil {
		t.Skipf("shipped data/images.json not found at %s", real)
	}
	return real
}

func TestLoadDefault_UsesEnvPath(t *testing.T) {
	real := shippedCatalogPath(t)
	abs, err := filepath.Abs(real)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FISHERMAN_IMAGES_PATH", abs)

	c, path, err := images.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault(): %v", err)
	}
	if path != abs {
		t.Errorf("LoadDefault() path = %q, want %q", path, abs)
	}
	if len(c.Images) == 0 || len(c.Aliases) == 0 {
		t.Errorf("shipped catalog parsed empty: images=%d aliases=%d", len(c.Images), len(c.Aliases))
	}
}

func TestLoadDefault_MissingAllPaths(t *testing.T) {
	t.Setenv("FISHERMAN_IMAGES_PATH", filepath.Join(t.TempDir(), "nope.json"))
	if _, _, err := images.LoadDefault(); err == nil {
		t.Error("LoadDefault() expected error when no catalog path resolves")
	}
}

// ── Shipped catalog end-to-end ──────────────────────────────────────────────

func TestShippedCatalog_EveryLeafResolvesAnImgref(t *testing.T) {
	real := shippedCatalogPath(t)
	t.Setenv("FISHERMAN_IMAGES_PATH", real)

	c, _, err := images.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault(): %v", err)
	}

	// Walk the whole tree; every leaf must resolve to an installable imgref
	// (registry+tag inheritance is what the installer depends on).
	var walk func(nodes []*images.Node, path []*images.Node, bad *[]string)
	walk = func(nodes []*images.Node, path []*images.Node, bad *[]string) {
		for _, n := range nodes {
			if len(n.Children) == 0 {
				cp := make([]*images.Node, len(path))
				copy(cp, path)
				res := (&images.NodeResult{Path: cp, Node: n}).Resolve(c)
				if res.Imgref == "" {
					*bad = append(*bad, n.Name)
				}
				continue
			}
			walk(n.Children, append(path, n), bad)
		}
	}
	// knownUnresolvable: leaves that resolve to an empty imgref in the shipped
	// catalog. '26.04 Desktop' embeds its tag in the registry field
	// ("ghcr.io/canonical/ubuntu:26.04") instead of using the separate tag
	// field, so both Resolve() and EffectiveImgref() yield "" — tuna-os/
	// fisherman#99 (data bug filed by quality). Remove the entry here when
	// the catalog data is fixed.
	knownUnresolvable := map[string]bool{"26.04 Desktop": true}
	var bad []string
	walk(c.Images, nil, &bad)
	var unexpected []string
	for _, name := range bad {
		if !knownUnresolvable[name] {
			unexpected = append(unexpected, name)
		}
	}
	if len(unexpected) > 0 {
		t.Errorf("%d leaf(ves) resolve to an empty imgref: %v", len(unexpected), unexpected)
	}
}

func TestShippedCatalog_SearchAndBreadcrumb(t *testing.T) {
	real := shippedCatalogPath(t)
	t.Setenv("FISHERMAN_IMAGES_PATH", real)

	c, _, err := images.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault(): %v", err)
	}

	got := c.Find("aurora")
	if len(got) == 0 {
		t.Fatal("Find(\"aurora\") on shipped catalog returned no results")
	}
	if got[0].Node.Name != "Aurora" {
		t.Errorf("Find(\"aurora\")[0] = %q, want the Aurora group", got[0].Node.Name)
	}
	if got[0].Breadcrumb() != "Aurora" {
		t.Errorf("Find(\"aurora\")[0] breadcrumb = %q, want %q", got[0].Breadcrumb(), "Aurora")
	}
}
