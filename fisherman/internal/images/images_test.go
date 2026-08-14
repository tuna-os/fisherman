package images_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/images"
)

func boolp(b bool) *bool { return &b }

// ── EffectiveImgref ────────────────────────────────────────────────────────

func TestEffectiveImgref(t *testing.T) {
	tests := []struct {
		name string
		node images.Node
		want string
	}{
		{name: "direct imgref wins", node: images.Node{Imgref: "ghcr.io/tuna-os/bonito:40"}, want: "ghcr.io/tuna-os/bonito:40"},
		{name: "registry+tag combined", node: images.Node{Registry: "ghcr.io/canonical/ubuntu", Tag: "26.04"}, want: "ghcr.io/canonical/ubuntu:26.04"},
		{name: "direct imgref beats registry+tag", node: images.Node{Imgref: "a:b", Registry: "c", Tag: "d"}, want: "a:b"},
		{name: "registry only", node: images.Node{Registry: "ghcr.io/tuna-os"}, want: ""},
		{name: "tag only", node: images.Node{Tag: "40"}, want: ""},
		{name: "empty node", node: images.Node{}, want: ""},
		{name: "nvidia imgref does not count", node: images.Node{NvidiaImgref: "nvidia:x"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.node.EffectiveImgref(); got != tt.want {
				t.Errorf("EffectiveImgref() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── IsLeaf ─────────────────────────────────────────────────────────────────

func TestIsLeaf(t *testing.T) {
	tests := []struct {
		name string
		node images.Node
		want bool
	}{
		{name: "leaf with imgref", node: images.Node{Imgref: "a:b"}, want: true},
		{name: "leaf with registry+tag", node: images.Node{Registry: "r", Tag: "t"}, want: true},
		{name: "leaf with nvidia imgref only", node: images.Node{NvidiaImgref: "nvidia:x"}, want: true},
		{name: "group with children is not leaf even with imgref", node: images.Node{Imgref: "a:b", Children: []*images.Node{{Name: "child"}}}, want: false},
		{name: "empty node is not leaf", node: images.Node{}, want: false},
		{name: "node with only name is not leaf", node: images.Node{Name: "Group"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.node.IsLeaf(); got != tt.want {
				t.Errorf("IsLeaf() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ── Resolve ────────────────────────────────────────────────────────────────

func TestResolve(t *testing.T) {
	// root ── group ── leaf
	root := &images.Node{
		Name:       "Root",
		Registry:   "ghcr.io/tuna-os",
		Tag:        "40",
		Desc:       "root desc",
		Flatpaks:   "org.gnome.Calculator",
		Bootloader: "grub2",
	}
	group := &images.Node{
		Name:              "Group",
		Tag:               "41",
		ComposeFsBackend:  boolp(true),
		NeedsUserCreation: boolp(false),
	}
	leaf := &images.Node{
		Name:              "Leaf",
		Imgref:            "custom.example/org/image:latest",
		Desc:              "leaf desc",
		Filesystem:        "btrfs",
		SearchExtra:       "extra",
		NeedsUserCreation: boolp(true),
	}
	result := &images.NodeResult{Path: []*images.Node{root, group}, Node: leaf}

	t.Run("deepest values win and direct imgref preserved", func(t *testing.T) {
		r := result.Resolve(nil)
		if r.Name != "Leaf" {
			t.Errorf("Name = %q, want Leaf", r.Name)
		}
		if r.Imgref != "custom.example/org/image:latest" {
			t.Errorf("Imgref = %q, want direct imgref", r.Imgref)
		}
		if r.Desc != "leaf desc" {
			t.Errorf("Desc = %q, want leaf desc", r.Desc)
		}
		if r.Flatpaks != "org.gnome.Calculator" {
			t.Errorf("Flatpaks = %q, want inherited root value", r.Flatpaks)
		}
		if r.Bootloader != "grub2" {
			t.Errorf("Bootloader = %q, want inherited root value", r.Bootloader)
		}
		if r.Filesystem != "btrfs" {
			t.Errorf("Filesystem = %q, want leaf value", r.Filesystem)
		}
		if !r.ComposeFsBackend {
			t.Errorf("ComposeFsBackend = %v, want true (inherited from group)", r.ComposeFsBackend)
		}
		if !r.NeedsUserCreation {
			t.Errorf("NeedsUserCreation = %v, want true (deepest explicit value)", r.NeedsUserCreation)
		}
	})

	t.Run("imgref built from deepest registry:tag", func(t *testing.T) {
		leaf2 := &images.Node{Name: "Leaf2"}
		r := (&images.NodeResult{Path: []*images.Node{root, group}, Node: leaf2}).Resolve(nil)
		// group overrides tag with 41; registry inherited from root
		if r.Imgref != "ghcr.io/tuna-os:41" {
			t.Errorf("Imgref = %q, want ghcr.io/tuna-os:41", r.Imgref)
		}
	})

	t.Run("nil booleans inherit ancestor", func(t *testing.T) {
		r := (&images.NodeResult{Path: []*images.Node{root}, Node: &images.Node{Name: "L"}}).Resolve(nil)
		if r.ComposeFsBackend {
			t.Errorf("ComposeFsBackend should stay false when nothing sets it")
		}
		if r.NeedsUserCreation {
			t.Errorf("NeedsUserCreation should stay false when nothing sets it")
		}
	})

	t.Run("alias flatpaks resolved via catalog", func(t *testing.T) {
		cat := &images.Catalog{
			Aliases: map[string]string{"aurora_brew": "https://example/aurora.Brewfile"},
		}
		r := (&images.NodeResult{Node: &images.Node{Name: "L", Flatpaks: "@aurora_brew"}}).Resolve(cat)
		if r.Flatpaks != "https://example/aurora.Brewfile" {
			t.Errorf("Flatpaks = %q, want resolved alias", r.Flatpaks)
		}
	})

	t.Run("unknown alias kept as-is", func(t *testing.T) {
		cat := &images.Catalog{Aliases: map[string]string{}}
		r := (&images.NodeResult{Node: &images.Node{Name: "L", Flatpaks: "@missing"}}).Resolve(cat)
		if r.Flatpaks != "@missing" {
			t.Errorf("Flatpaks = %q, want unchanged @missing", r.Flatpaks)
		}
	})

	t.Run("no registry anywhere yields empty imgref", func(t *testing.T) {
		r := (&images.NodeResult{Node: &images.Node{Name: "L"}}).Resolve(nil)
		if r.Imgref != "" {
			t.Errorf("Imgref = %q, want empty", r.Imgref)
		}
	})
}

// ── Breadcrumb ─────────────────────────────────────────────────────────────

func TestBreadcrumb(t *testing.T) {
	t.Run("single node", func(t *testing.T) {
		r := &images.NodeResult{Node: &images.Node{Name: "Bonito"}}
		if got := r.Breadcrumb(); got != "Bonito" {
			t.Errorf("Breadcrumb() = %q, want Bonito", got)
		}
	})
	t.Run("nested path", func(t *testing.T) {
		r := &images.NodeResult{
			Path: []*images.Node{{Name: "Ubuntu"}, {Name: "26.04 Desktop"}},
			Node: &images.Node{Name: "GNOME"},
		}
		if got := r.Breadcrumb(); got != "Ubuntu › 26.04 Desktop › GNOME" {
			t.Errorf("Breadcrumb() = %q", got)
		}
	})
}

// ── Find ───────────────────────────────────────────────────────────────────

func testCatalog() *images.Catalog {
	return &images.Catalog{
		Images: []*images.Node{
			{
				Name:        "Ubuntu",
				SearchExtra: "ubuntu canonical desktop gnome",
				Desc:        "Ubuntu LTS with GNOME",
				Children: []*images.Node{
					{Name: "24.04 Desktop", Registry: "ghcr.io/canonical/ubuntu", Tag: "24.04"},
					{Name: "26.04 Desktop", Desc: "ZFS support", Registry: "ghcr.io/canonical/ubuntu:26.04"},
				},
			},
			{
				Name:         "TunaOS",
				Imgref:       "ghcr.io/tuna-os/bonito:40",
				NvidiaImgref: "nvidia/tuna:40",
				SearchExtra:  "bonito rolling",
			},
		},
	}
}

func TestFind(t *testing.T) {
	cat := testCatalog()

	t.Run("match by name", func(t *testing.T) {
		res := cat.Find("ubuntu")
		if len(res) != 1 {
			t.Fatalf("Find(ubuntu) = %d results, want 1 (group match stops recursion)", len(res))
		}
		if res[0].Node.Name != "Ubuntu" {
			t.Errorf("matched node = %q, want Ubuntu", res[0].Node.Name)
		}
		if got := res[0].Breadcrumb(); got != "Ubuntu" {
			t.Errorf("breadcrumb = %q, want Ubuntu", got)
		}
	})

	t.Run("match by search_extra", func(t *testing.T) {
		res := cat.Find("bonito")
		if len(res) != 1 || res[0].Node.Name != "TunaOS" {
			t.Fatalf("Find(bonito) = %+v, want TunaOS", res)
		}
	})

	t.Run("match by imgref", func(t *testing.T) {
		res := cat.Find("tuna-os/bonito:40")
		if len(res) != 1 || res[0].Node.Name != "TunaOS" {
			t.Fatalf("Find(imgref) = %+v, want TunaOS", res)
		}
	})

	t.Run("match by nvidia imgref", func(t *testing.T) {
		res := cat.Find("nvidia/tuna")
		if len(res) != 1 || res[0].Node.Name != "TunaOS" {
			t.Fatalf("Find(nvidia) = %+v, want TunaOS", res)
		}
	})

	t.Run("match nested leaf with breadcrumb", func(t *testing.T) {
		res := cat.Find("24.04")
		if len(res) != 1 {
			t.Fatalf("Find(24.04) = %d results, want 1", len(res))
		}
		if got := res[0].Breadcrumb(); got != "Ubuntu › 24.04 Desktop" {
			t.Errorf("breadcrumb = %q, want Ubuntu › 24.04 Desktop", got)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		if len(cat.Find("TUNAOS")) != 1 {
			t.Errorf("Find(TUNAOS) should match TunaOS case-insensitively")
		}
	})

	t.Run("no match", func(t *testing.T) {
		if res := cat.Find("nonexistent-thing"); len(res) != 0 {
			t.Errorf("Find(nonexistent) = %d results, want 0", len(res))
		}
	})

	t.Run("empty query matches every root", func(t *testing.T) {
		if res := cat.Find(""); len(res) != 2 {
			t.Errorf("Find(empty) = %d results, want 2 roots", len(res))
		}
	})

	t.Run("multiple matches across roots", func(t *testing.T) {
		cat2 := &images.Catalog{Images: []*images.Node{
			{Name: "Alpha", Desc: "shared keyword"},
			{Name: "Beta", Desc: "shared keyword"},
		}}
		if res := cat2.Find("shared keyword"); len(res) != 2 {
			t.Errorf("Find(keyword) = %d results, want 2", len(res))
		}
	})
}

// ── Load ───────────────────────────────────────────────────────────────────

func TestLoad(t *testing.T) {
	writeJSON := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "images.json")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("parses full catalog", func(t *testing.T) {
		p := writeJSON(t, `{
			"aliases": {"aurora_brew": "https://example/Brewfile"},
			"default_image": "tuna-os/bonito:40",
			"fallback_flatpaks": ["org.gnome.Calculator"],
			"images": [
				{"name": "TunaOS", "imgref": "ghcr.io/tuna-os/bonito:40",
				 "children": [{"name": "GNOME", "tag": "40", "composefs": true, "needs_user_creation": false}]}
			]
		}`)
		c, err := images.Load(p)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if c.DefaultImage != "tuna-os/bonito:40" {
			t.Errorf("DefaultImage = %q", c.DefaultImage)
		}
		if len(c.FallbackFlatpaks) != 1 || c.FallbackFlatpaks[0] != "org.gnome.Calculator" {
			t.Errorf("FallbackFlatpaks = %v", c.FallbackFlatpaks)
		}
		if c.Aliases["aurora_brew"] != "https://example/Brewfile" {
			t.Errorf("Aliases = %v", c.Aliases)
		}
		if len(c.Images) != 1 || len(c.Images[0].Children) != 1 {
			t.Fatalf("unexpected tree: %+v", c.Images)
		}
		child := c.Images[0].Children[0]
		if child.ComposeFsBackend == nil || !*child.ComposeFsBackend {
			t.Errorf("composefs should be true (explicit)")
		}
		if child.NeedsUserCreation == nil || *child.NeedsUserCreation {
			t.Errorf("needs_user_creation should be false (explicit)")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := images.Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
			t.Errorf("Load(missing) should error")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		p := writeJSON(t, `{"images": [`)
		if _, err := images.Load(p); err == nil {
			t.Errorf("Load(invalid) should error")
		}
	})

	t.Run("empty images array", func(t *testing.T) {
		p := writeJSON(t, `{"images": []}`)
		c, err := images.Load(p)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if len(c.Images) != 0 {
			t.Errorf("Images = %v, want empty", c.Images)
		}
	})
}

// ── FindPaths / LoadDefault ────────────────────────────────────────────────

func TestFindPathsEnv(t *testing.T) {
	t.Setenv("FISHERMAN_IMAGES_PATH", "/tmp/custom/images.json")
	paths := images.FindPaths()
	if len(paths) == 0 || paths[0] != "/tmp/custom/images.json" {
		t.Fatalf("FindPaths() = %v, want env path first", paths)
	}
	// Install locations must still be present after the env path.
	joined := strings.Join(paths, "\n")
	if !strings.Contains(joined, "/usr/share/fisherman/data/images.json") {
		t.Errorf("FindPaths() missing standard install path: %v", paths)
	}
}

func TestLoadDefault(t *testing.T) {
	t.Run("loads from env path", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "images.json")
		if err := os.WriteFile(p, []byte(`{"images": [{"name": "Bonito", "imgref": "a:b"}]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("FISHERMAN_IMAGES_PATH", p)
		c, used, err := images.LoadDefault()
		if err != nil {
			t.Fatalf("LoadDefault() error = %v", err)
		}
		if used != p {
			t.Errorf("LoadDefault() used %q, want %q", used, p)
		}
		if len(c.Images) != 1 || c.Images[0].Name != "Bonito" {
			t.Errorf("catalog = %+v", c.Images)
		}
	})

	t.Run("errors when no path resolves", func(t *testing.T) {
		t.Setenv("FISHERMAN_IMAGES_PATH", filepath.Join(t.TempDir(), "missing.json"))
		if _, _, err := images.LoadDefault(); err == nil {
			t.Errorf("LoadDefault() should error when no path loads")
		}
	})
}
