// Package catalog gives the TUI its image list, and for each image the
// settings fisherman must be told: the boot loader, whether the image needs
// the composefs backend, the filesystems it supports and whether an account
// must be created. A recipe that leaves these out installs a Dakota, Arch or
// Debian image with the GRUB/ostree layout, which fails after the disk is
// wiped (#367).
//
// The values come from the fisherman image catalog (images.json) when it is
// installed and lists the image, with the same inheritance fisherman uses.
// Images the catalog does not list take them from the built-in table.
package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// Image is one installable image with the recipe settings it needs.
type Image struct {
	Label            string   // shown in the list
	Ref              string   // image reference
	Bootloader       string   // "", "grub2" or "systemd"
	ComposeFsBackend bool     // the image needs bootc's composefs backend
	Filesystem       string   // default root filesystem
	Filesystems      []string // the choices; never empty
	// NeedsUserCreation: the image has no first-boot setup that creates an
	// account, so the installer must create one.
	NeedsUserCreation bool
}

// AllFilesystems are the root filesystems the TUI can ask for.
var AllFilesystems = []string{"ext4", "xfs", "btrfs"}

// SupportsFilesystem reports whether fs can hold the root of this image.
// composefs needs fs-verity, which XFS does not have: fisherman refuses the
// pair (composefs_requires_fsverity), so the TUI never offers it.
func (i Image) SupportsFilesystem(fs string) bool {
	if i.ComposeFsBackend && fs == "xfs" {
		return false
	}
	return slices.Contains(i.Filesystems, fs)
}

// normalize fills the filesystem choices and the default, by the catalog
// contract: the choices are `filesystems`, else `[filesystem]`; the default
// is `filesystem`, else the first choice. With neither, every filesystem
// the backend supports for the image is a choice.
func (i Image) normalize() Image {
	if len(i.Filesystems) == 0 {
		if i.Filesystem != "" {
			i.Filesystems = []string{i.Filesystem}
		} else {
			i.Filesystems = slices.Clone(AllFilesystems)
		}
	}
	if i.ComposeFsBackend {
		i.Filesystems = slices.DeleteFunc(slices.Clone(i.Filesystems), func(fs string) bool { return fs == "xfs" })
	}
	if !slices.Contains(i.Filesystems, i.Filesystem) {
		i.Filesystem = ""
		if len(i.Filesystems) > 0 {
			i.Filesystem = i.Filesystems[0]
		}
	}
	return i
}

// Custom returns the Image for a reference the user typed. Nothing is
// assumed about it: the caller asks for the boot stack.
func Custom(ref string, composeFsBackend bool, bootloader string) Image {
	return Image{Label: ref, Ref: ref, Bootloader: bootloader, ComposeFsBackend: composeFsBackend,
		NeedsUserCreation: true}.normalize()
}

// builtin are the images the TUI offers. The settings here are used when
// the installed catalog does not list the image. Arch and Debian are the
// bootcrew images: composefs-native with no ostree binary, installed on
// ext4 with systemd-boot (tests/bootcrew-matrix.yaml).
var builtin = []Image{
	{Label: "Bluefin Dakota (GNOME OS based, composefs + systemd-boot)", Ref: "ghcr.io/projectbluefin/dakota:latest",
		Bootloader: "systemd", ComposeFsBackend: true, Filesystem: "btrfs", Filesystems: []string{"btrfs", "ext4"}},
	{Label: "Aurora (Fedora based, KDE Plasma)", Ref: "ghcr.io/ublue-os/aurora:latest",
		Filesystem: "btrfs", Filesystems: []string{"btrfs", "xfs", "ext4"}, NeedsUserCreation: true},
	{Label: "CentOS Stream 10", Ref: "quay.io/centos-bootc/centos-bootc:stream10",
		Filesystem: "xfs", Filesystems: []string{"xfs", "ext4", "btrfs"}, NeedsUserCreation: true},
	{Label: "Debian (bootcrew, composefs + systemd-boot)", Ref: "ghcr.io/bootcrew/debian-bootc:latest",
		Bootloader: "systemd", ComposeFsBackend: true, Filesystem: "ext4", Filesystems: []string{"ext4", "btrfs"}, NeedsUserCreation: true},
	{Label: "Arch Linux (bootcrew, composefs + systemd-boot)", Ref: "ghcr.io/bootcrew/arch-bootc:latest",
		Bootloader: "systemd", ComposeFsBackend: true, Filesystem: "ext4", Filesystems: []string{"ext4", "btrfs"}, NeedsUserCreation: true},
}

// node is one entry of images.json. Pointers tell an absent key from an
// explicit false, which the inheritance needs.
type node struct {
	Name              string   `json:"name"`
	Imgref            string   `json:"imgref"`
	Registry          string   `json:"registry"`
	Tag               string   `json:"tag"`
	Bootloader        string   `json:"bootloader"`
	Filesystem        string   `json:"filesystem"`
	Filesystems       []string `json:"filesystems"`
	ComposeFsBackend  *bool    `json:"composefs"`
	NeedsUserCreation *bool    `json:"needs_user_creation"`
	Children          []*node  `json:"children"`
}

// Catalog is the resolved leaves of an images.json, by image reference.
type Catalog struct {
	leaves map[string]Image
}

// Parse reads an images.json. Each leaf takes, for every setting, the value
// of the deepest node on its path that sets it (fisherman's
// images.NodeResult.Resolve). needs_user_creation that no node sets is
// TRUE: an account is created unless the catalog says the image has its own
// first-boot setup.
func Parse(data []byte) (*Catalog, error) {
	var doc struct {
		Images []*node `json:"images"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	c := &Catalog{leaves: map[string]Image{}}
	var walk func(n *node, inherited Image, registry, tag, path string)
	walk = func(n *node, img Image, registry, tag, path string) {
		if n.Registry != "" {
			registry = n.Registry
		}
		if n.Tag != "" {
			tag = n.Tag
		}
		if n.Bootloader != "" {
			img.Bootloader = n.Bootloader
		}
		if n.Filesystem != "" {
			img.Filesystem = n.Filesystem
		}
		if len(n.Filesystems) > 0 {
			img.Filesystems = n.Filesystems
		}
		if n.ComposeFsBackend != nil {
			img.ComposeFsBackend = *n.ComposeFsBackend
		}
		if n.NeedsUserCreation != nil {
			img.NeedsUserCreation = *n.NeedsUserCreation
		}
		if path != "" {
			path += " › "
		}
		path += n.Name
		if len(n.Children) > 0 {
			for _, child := range n.Children {
				walk(child, img, registry, tag, path)
			}
			return
		}
		img.Ref = n.Imgref
		if img.Ref == "" && registry != "" && tag != "" {
			img.Ref = registry + ":" + tag
		}
		if img.Ref == "" {
			return
		}
		img.Label = path
		if _, dup := c.leaves[img.Ref]; !dup {
			c.leaves[img.Ref] = img.normalize()
		}
	}
	for _, root := range doc.Images {
		walk(root, Image{NeedsUserCreation: true}, "", "", "")
	}
	return c, nil
}

// Lookup returns the catalog's settings for an image reference.
func (c *Catalog) Lookup(ref string) (Image, bool) {
	if c == nil {
		return Image{}, false
	}
	img, ok := c.leaves[ref]
	return img, ok
}

// searchPaths are where images.json is looked for: an operator's file
// first, then the one fisherman installs (fisherman's images.FindPaths).
func searchPaths() []string {
	var paths []string
	if v := os.Getenv("FISHERMAN_IMAGES_PATH"); v != "" {
		paths = append(paths, v)
	}
	paths = append(paths, "/etc/bootc-installer/images.json", "/etc/tuna-installer/images.json")
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		paths = append(paths, filepath.Join(dir, "data", "images.json"), filepath.Join(dir, "..", "data", "images.json"))
	}
	return append(paths, "/usr/share/fisherman/data/images.json")
}

// LoadDefault returns the first images.json that can be read, or nil when
// there is none. nil is a usable Catalog: Images then returns the built-in
// table.
func LoadDefault() *Catalog {
	for _, p := range searchPaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if c, err := Parse(data); err == nil {
			return c
		}
	}
	return nil
}

// Images returns the images the TUI offers. The catalog's settings replace
// the built-in ones for an image it lists; the label stays the TUI's own.
func (c *Catalog) Images() []Image {
	out := make([]Image, 0, len(builtin))
	for _, b := range builtin {
		img := b
		if leaf, ok := c.Lookup(b.Ref); ok {
			leaf.Label = b.Label
			img = leaf
		}
		out = append(out, img.normalize())
	}
	return out
}

// Resolve returns the settings for any image reference: the catalog's, then
// the built-in table's. ok is false for a reference neither knows; the
// caller must then ask the user (Custom).
func (c *Catalog) Resolve(ref string) (Image, bool) {
	if img, ok := c.Lookup(ref); ok {
		return img, true
	}
	for _, b := range builtin {
		if b.Ref == ref {
			return b.normalize(), true
		}
	}
	return Image{}, false
}
