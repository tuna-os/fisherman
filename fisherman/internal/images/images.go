// Package images loads and represents the fisherman image catalog (images.json).
package images

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Node is a single entry in the image catalog tree. Leaf nodes have an
// Imgref (or Registry+Tag, or NvidiaImgref) and no Children.
//
// Boolean fields use *bool so that JSON absence (nil) is distinguishable from
// an explicit false — enabling correct inheritance when Resolve() is called.
type Node struct {
	Name              string  `json:"name"`
	Imgref            string  `json:"imgref,omitempty"`
	Registry          string  `json:"registry,omitempty"`
	Tag               string  `json:"tag,omitempty"`
	NvidiaImgref      string  `json:"nvidia_imgref,omitempty"`
	Desc              string  `json:"desc,omitempty"`
	Subtitle          string  `json:"subtitle,omitempty"`
	Icon              string  `json:"icon,omitempty"`
	Flatpaks          string  `json:"flatpaks,omitempty"`
	SearchExtra       string  `json:"search_extra,omitempty"`
	Bootloader        string  `json:"bootloader,omitempty"`
	Filesystem        string  `json:"filesystem,omitempty"`
	ComposeFsBackend  *bool   `json:"composefs,omitempty"`
	NeedsUserCreation *bool   `json:"needs_user_creation,omitempty"`
	Children          []*Node `json:"children,omitempty"`
}

// EffectiveImgref returns the effective image reference for this node.
// If Imgref is set directly it is returned; otherwise it is constructed
// from Registry:Tag. Returns "" when no image reference is available.
func (n *Node) EffectiveImgref() string {
	if n.Imgref != "" {
		return n.Imgref
	}
	if n.Registry != "" && n.Tag != "" {
		return n.Registry + ":" + n.Tag
	}
	return ""
}

// IsLeaf returns true if this node is an installable image (has a resolvable
// image reference and no children).
func (n *Node) IsLeaf() bool {
	if len(n.Children) > 0 {
		return false
	}
	return n.Imgref != "" || (n.Registry != "" && n.Tag != "") || n.NvidiaImgref != ""
}

// ResolvedNode holds the effective configuration for a node after inheriting
// fields from its ancestor group nodes (root → node). String fields inherit
// the deepest non-empty ancestor value; boolean fields inherit the deepest
// explicitly-set (*bool != nil) ancestor value.
type ResolvedNode struct {
	Name              string
	Imgref            string
	Desc              string
	Subtitle          string
	Flatpaks          string
	Bootloader        string
	Filesystem        string
	ComposeFsBackend  bool
	NeedsUserCreation bool
	SearchExtra       string
}

// Resolve computes the effective configuration by walking ancestors root-first
// and applying the most-specific (deepest) value for each field.
// Registry and Tag are inherited from ancestors and combined into Imgref when
// the node itself does not set Imgref directly.
// Alias references (e.g. "@aurora_brew") in Flatpaks are resolved against the
// catalog's aliases map when catalog is non-nil.
func (r *NodeResult) Resolve(catalog *Catalog) ResolvedNode {
	all := make([]*Node, 0, len(r.Path)+1)
	all = append(all, r.Path...)
	all = append(all, r.Node)

	var registry, tag string
	res := ResolvedNode{
		Name:        r.Node.Name,
		Imgref:      r.Node.Imgref,
		Desc:        r.Node.Desc,
		Subtitle:    r.Node.Subtitle,
		SearchExtra: r.Node.SearchExtra,
	}
	for _, n := range all {
		if n.Registry != "" {
			registry = n.Registry
		}
		if n.Tag != "" {
			tag = n.Tag
		}
		if n.Flatpaks != "" {
			res.Flatpaks = n.Flatpaks
		}
		if n.Bootloader != "" {
			res.Bootloader = n.Bootloader
		}
		if n.Filesystem != "" {
			res.Filesystem = n.Filesystem
		}
		if n.ComposeFsBackend != nil {
			res.ComposeFsBackend = *n.ComposeFsBackend
		}
		if n.NeedsUserCreation != nil {
			res.NeedsUserCreation = *n.NeedsUserCreation
		}
	}
	// Build imgref from registry:tag if no direct imgref set on this node.
	if res.Imgref == "" && registry != "" && tag != "" {
		res.Imgref = registry + ":" + tag
	}
	// Resolve alias references in flatpaks.
	if catalog != nil && strings.HasPrefix(res.Flatpaks, "@") {
		aliasKey := res.Flatpaks[1:]
		if resolved, ok := catalog.Aliases[aliasKey]; ok {
			res.Flatpaks = resolved
		}
	}
	return res
}

// NodeResult is a matched node with its ancestor path (root-first, not including the node itself).
type NodeResult struct {
	Path []*Node
	Node *Node
}

// Breadcrumb returns a human-readable "A › B › C" path string.
func (r *NodeResult) Breadcrumb() string {
	parts := make([]string, 0, len(r.Path)+1)
	for _, p := range r.Path {
		parts = append(parts, p.Name)
	}
	parts = append(parts, r.Node.Name)
	return strings.Join(parts, " › ")
}

// Find searches the catalog for nodes whose name, imgref, or search_extra
// contain query (case-insensitive). Returns all matching nodes with breadcrumbs.
func (c *Catalog) Find(query string) []*NodeResult {
	q := strings.ToLower(query)
	var results []*NodeResult
	for _, root := range c.Images {
		findIn(root, nil, q, &results)
	}
	return results
}

func findIn(n *Node, path []*Node, q string, out *[]*NodeResult) {
	if nodeMatches(n, q) {
		cp := make([]*Node, len(path))
		copy(cp, path)
		*out = append(*out, &NodeResult{Path: cp, Node: n})
		return // don't recurse into a matched group — the group itself is the result
	}
	for _, child := range n.Children {
		findIn(child, append(path, n), q, out)
	}
}

func nodeMatches(n *Node, q string) bool {
	if strings.Contains(strings.ToLower(n.Name), q) ||
		strings.Contains(strings.ToLower(n.SearchExtra), q) ||
		strings.Contains(strings.ToLower(n.Desc), q) {
		return true
	}
	// Search against all possible image references.
	for _, ref := range []string{n.Imgref, n.Registry, n.Tag, n.NvidiaImgref, n.EffectiveImgref()} {
		if ref != "" && strings.Contains(strings.ToLower(ref), q) {
			return true
		}
	}
	return false
}

// Catalog is the top-level structure of images.json.
type Catalog struct {
	Aliases          map[string]string `json:"aliases"`
	DefaultImage     string            `json:"default_image"`
	FallbackFlatpaks []string          `json:"fallback_flatpaks"`
	Images           []*Node           `json:"images"`
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
