package main

// Tests for the previously uncovered `fisherman images` CLI surface
// (cmd/fisherman/images.go was 0%: runImages, printNode, printNodeDetail).
// The tree renderer is pure stdout logic on top of internal/images, so it is
// exercised in-process with a temp catalog file and the shared
// captureOutput helper; os.Exit error paths are not reachable in-process and
// are left to the failure-mode pattern used by cli_test.go.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/images"
)

// ansiRe matches ANSI SGR escape sequences so coloured output can be
// asserted on its visible text.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// writeTestCatalog writes a small catalog exercising every renderer branch:
// a family node, a default leaf with a direct imgref, a leaf inheriting
// registry:tag + inheritance fields, and a standalone leaf.
func writeTestCatalog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "images.json")
	body := `{
  "aliases": {"@base": "org.gnome.Platform//50"},
  "default_image": "ghcr.io/tuna-os/bluefin:stable",
  "images": [
    {
      "name": "Bluefin",
      "children": [
        {"name": "Stable", "imgref": "ghcr.io/tuna-os/bluefin:stable"},
        {
          "name": "Dev",
          "registry": "ghcr.io",
          "tag": "bluefin:dev",
          "desc": "Development image",
          "subtitle": "for developers",
          "bootloader": "systemd-boot",
          "filesystem": "btrfs",
          "composefs": true,
          "needs_user_creation": true,
          "flatpaks": "@base",
          "search_extra": "developer container"
        }
      ]
    },
    {"name": "Yellowfin", "imgref": "ghcr.io/tuna-os/yellowfin:stable"}
  ]
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return path
}

// TestRunImages_PlainTree asserts the --plain tree output: family lines have
// no dash, leaf lines carry the effective imgref, and only the default image
// gets the '*' marker. Plain mode must not emit the "# path" header.
func TestRunImages_PlainTree(t *testing.T) {
	path := writeTestCatalog(t)
	out, errOut := captureOutput(t, func() {
		runImages([]string{"--file", path, "--plain"})
	})
	if errOut != "" {
		t.Fatalf("unexpected stderr: %q", errOut)
	}
	for _, want := range []string{
		"Bluefin",                                    // family line, no dash
		"- Stable  ghcr.io/tuna-os/bluefin:stable *", // default marker
		"- Dev  ghcr.io:bluefin:dev",                 // registry:tag composition, no marker
		"- Yellowfin  ghcr.io/tuna-os/yellowfin:stable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plain tree missing %q\n--- output ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "# "+path) {
		t.Errorf("plain mode printed the '# path' header:\n%s", out)
	}
}

// TestRunImages_QueryDetail asserts the --plain detail view for a matched
// leaf: breadcrumb, name, image ref, and the needs-user-creation flag.
func TestRunImages_QueryDetail(t *testing.T) {
	path := writeTestCatalog(t)
	out, _ := captureOutput(t, func() {
		runImages([]string{"--file", path, "--plain", "Yellowfin"})
	})
	for _, want := range []string{
		"Name: Yellowfin",
		"Image: ghcr.io/tuna-os/yellowfin:stable",
		"Needs user creation: no",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plain detail missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestRunImages_QueryMatchesSearchExtra asserts queries also match search
// keywords and that inherited flags render as yes.
func TestRunImages_QueryMatchesSearchExtra(t *testing.T) {
	path := writeTestCatalog(t)
	out, _ := captureOutput(t, func() {
		runImages([]string{"--file", path, "--plain", "developer"})
	})
	for _, want := range []string{
		"Path: Bluefin › Dev",
		"Name: Dev",
		"Description: Development image",
		"Bootloader: systemd-boot",
		"Filesystem: btrfs",
		"Needs user creation: yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("search-extra detail missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestPrintNode_AnsiTree asserts the coloured tree renderer: box-drawing
// connectors (├─/└─) with the │ prefix for children, the "★ default" marker,
// and the boot/fs fallbacks (grub2 / xfs) when a node does not set them.
func TestPrintNode_AnsiTree(t *testing.T) {
	cat := &images.Catalog{
		DefaultImage: "ghcr.io/tuna-os/bluefin:stable",
		Images: []*images.Node{
			{Name: "Bluefin", Children: []*images.Node{
				{Name: "Stable", Imgref: "ghcr.io/tuna-os/bluefin:stable"},
				{Name: "Dev", Registry: "ghcr.io", Tag: "bluefin:dev", Bootloader: "systemd-boot", Filesystem: "btrfs", ComposeFsBackend: boolPtr(true), NeedsUserCreation: boolPtr(true)},
			}},
			{Name: "Yellowfin", Imgref: "ghcr.io/tuna-os/yellowfin:stable"},
		},
	}
	out, _ := captureOutput(t, func() {
		for i, root := range cat.Images {
			last := i == len(cat.Images)-1
			printNode(root, "", last, cat.DefaultImage, false)
		}
	})
	out = stripANSI(out)
	for _, want := range []string{
		"├─ Bluefin",
		"│  ├─ Stable",
		"ghcr.io/tuna-os/bluefin:stable ★ default",
		"│  └─ Dev",
		"└─ Yellowfin",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ansi tree missing %q\n--- output ---\n%s", want, out)
		}
	}
	// Tree view must not leak leaf detail fields.
	if strings.Contains(out, "grub2 (default)") || strings.Contains(out, "systemd-boot") || strings.Contains(out, "btrfs") {
		t.Errorf("ansi tree leaked leaf details into the tree view:\n%s", out)
	}
}

// TestPrintNodeDetail_PlainDefaultMarker asserts the plain detail view marks
// the default image with "[default]" and lists child variants.
func TestPrintNodeDetail_PlainDefaultMarker(t *testing.T) {
	cat := &images.Catalog{
		DefaultImage: "ghcr.io/tuna-os/bluefin:stable",
		Images: []*images.Node{{Name: "Bluefin", Children: []*images.Node{
			{Name: "Stable", Imgref: "ghcr.io/tuna-os/bluefin:stable"},
		}}},
	}
	parent := cat.Images[0]
	leaf := parent.Children[0]
	out, _ := captureOutput(t, func() {
		printNodeDetail(&images.NodeResult{Path: []*images.Node{parent}, Node: leaf}, cat, cat.DefaultImage, true)
	})
	for _, want := range []string{
		"Path: Bluefin › Stable",
		"Name: Stable",
		"Image: ghcr.io/tuna-os/bluefin:stable [default]",
		"Needs user creation: no",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plain detail missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestPrintNodeDetail_ColoredVariants asserts the coloured detail view emits
// the image/boot/fs/composefs/keyword fields and the "Variants:" section for
// a family node with children.
func TestPrintNodeDetail_ColoredVariants(t *testing.T) {
	cat := &images.Catalog{
		DefaultImage: "ghcr.io/tuna-os/bluefin:stable",
		Images: []*images.Node{{Name: "Bluefin", Children: []*images.Node{
			{Name: "Dev", Registry: "ghcr.io", Tag: "bluefin:dev", Desc: "Development image", Subtitle: "for developers", Bootloader: "systemd-boot", Filesystem: "btrfs", ComposeFsBackend: boolPtr(true), NeedsUserCreation: boolPtr(true), SearchExtra: "developer container"},
		}}},
	}
	parent := cat.Images[0]
	dev := parent.Children[0]
	out, _ := captureOutput(t, func() {
		printNodeDetail(&images.NodeResult{Path: []*images.Node{parent}, Node: dev}, cat, cat.DefaultImage, false)
	})
	out = stripANSI(out)
	for _, want := range []string{
		"Bluefin › Dev",
		"desc:    Development image",
		"boot:    systemd-boot",
		"fs:      btrfs",
		"composefs: enabled",
		"needs user creation: yes",
		"keywords: developer container",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("colored detail missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestPrintNodeDetail_ColoredDefaultsFallback asserts a leaf with no
// bootloader/filesystem renders the documented defaults and no default star
// for a non-default image.
func TestPrintNodeDetail_ColoredDefaultsFallback(t *testing.T) {
	cat := &images.Catalog{
		DefaultImage: "other:image",
		Images:       []*images.Node{{Name: "Yellowfin", Imgref: "ghcr.io/tuna-os/yellowfin:stable"}},
	}
	leaf := cat.Images[0]
	out, _ := captureOutput(t, func() {
		printNodeDetail(&images.NodeResult{Node: leaf}, cat, cat.DefaultImage, false)
	})
	out = stripANSI(out)
	for _, want := range []string{
		"image:   ghcr.io/tuna-os/yellowfin:stable",
		"boot:    grub2 (default)",
		"fs:      xfs (default)",
		"needs user creation: no",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("defaults fallback missing %q\n--- output ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "★ default") {
		t.Errorf("non-default image got the default star:\n%s", out)
	}
}

func boolPtr(b bool) *bool { return &b }
