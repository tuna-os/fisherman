// Package images loads and represents the fisherman image catalog (images.json).
package images

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Node is a single entry in the image catalog tree. Leaf nodes have Imgref
// set; group nodes have Children.
type Node struct {
	Name     string  `json:"name"`
	Imgref   string  `json:"imgref,omitempty"`
	Desc     string  `json:"desc,omitempty"`
	Children []*Node `json:"children,omitempty"`
}

// IsLeaf returns true if this node is an installable image (has an imgref).
func (n *Node) IsLeaf() bool { return n.Imgref != "" }

// Catalog is the top-level structure of images.json.
type Catalog struct {
	DefaultImage     string    `json:"default_image"`
	FallbackFlatpaks []string  `json:"fallback_flatpaks"`
	Images           []*Node   `json:"images"`
}

// Load reads the image catalog from the given path.
func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &c, nil
}

// FindPaths returns the search paths for images.json, in priority order.
// Callers should try each path until one succeeds.
func FindPaths() []string {
	var paths []string

	if v := os.Getenv("FISHERMAN_IMAGES_PATH"); v != "" {
		paths = append(paths, v)
	}

	// Relative to the running executable: works for both dev builds and
	// installed builds where the data dir is adjacent to the binary.
	if exe, err := os.Executable(); err == nil {
		exe = filepath.Dir(exe)
		paths = append(paths,
			filepath.Join(exe, "data", "images.json"),       // installed adjacent
			filepath.Join(exe, "..", "data", "images.json"), // submodule dev layout
		)
	}

	// Standard install locations.
	paths = append(paths,
		"/usr/share/fisherman/data/images.json",
		"/app/share/fisherman/data/images.json", // Flatpak
	)

	return paths
}

// LoadDefault tries each path from FindPaths and returns the first catalog
// that loads successfully.
func LoadDefault() (*Catalog, string, error) {
	for _, p := range FindPaths() {
		c, err := Load(p)
		if err == nil {
			return c, p, nil
		}
	}
	return nil, "", fmt.Errorf("images.json not found; set FISHERMAN_IMAGES_PATH or install fisherman data files")
}
