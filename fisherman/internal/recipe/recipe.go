package recipe

import (
	"encoding/json"
	"fmt"
	"os"
)

// Recipe describes a fisherman installation.
type Recipe struct {
	Disk            string     `json:"disk"`            // block device, e.g. "/dev/sda"
	Filesystem      string     `json:"filesystem"`      // "xfs" or "btrfs"
	BtrfsSubvolumes bool       `json:"btrfsSubvolumes"` // create @, @home, @snapshots
	Encryption      Encryption `json:"encryption"`
	Image           string     `json:"image"`           // source OCI image reference
	TargetImgref    string     `json:"targetImgref"`    // update-tracking ref (optional)
	SelinuxDisabled  bool       `json:"selinuxDisabled"`
	UnifiedStorage   bool       `json:"unifiedStorage"`  // pass --experimental-unified-storage
	// ComposeFsBackend passes --composefs-backend to bootc install to-filesystem.
	// Required for composefs-native images (e.g. ghcr.io/bootcrew/*).
	// Independent of UnifiedStorage — these are different bootc features.
	ComposeFsBackend bool       `json:"composeFsBackend"`
	Hostname         string     `json:"hostname"`
	Flatpaks        []string   `json:"flatpaks"`        // flatpak app IDs to install; empty = fallback
	User            UserSpec   `json:"user"`            // optional user account to create
}

// UserSpec describes a user account to create during installation.
// If Username is empty the user creation step is skipped.
type UserSpec struct {
	Username string   `json:"username"`
	Fullname string   `json:"fullname"`
	Password string   `json:"password"`
	Groups   []string `json:"groups"`
}

// Encryption describes the disk encryption configuration.
type Encryption struct {
	Type       string `json:"type"`       // "none", "luks-passphrase", "tpm2-luks", "tpm2-luks-passphrase"
	Passphrase string `json:"passphrase"` // required for luks-passphrase and tpm2-luks-passphrase
}

// Load reads and parses a recipe JSON file.
func Load(path string) (*Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading recipe: %w", err)
	}
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing recipe: %w", err)
	}
	return &r, nil
}

// Validate checks that the recipe fields are coherent and that the disk exists.
func (r *Recipe) Validate() error {
	if r.Disk == "" {
		return fmt.Errorf("disk is required")
	}
	if _, err := os.Stat(r.Disk); err != nil {
		return fmt.Errorf("disk %s: %w", r.Disk, err)
	}
	switch r.Filesystem {
	case "xfs", "btrfs":
	default:
		return fmt.Errorf("filesystem must be \"xfs\" or \"btrfs\", got %q", r.Filesystem)
	}
	if r.BtrfsSubvolumes && r.Filesystem != "btrfs" {
		return fmt.Errorf("btrfsSubvolumes requires filesystem=btrfs")
	}
	switch r.Encryption.Type {
	case "", "none", "tpm2-luks", "luks-passphrase", "tpm2-luks-passphrase":
	default:
		return fmt.Errorf("encryption.type must be \"none\", \"luks-passphrase\", \"tpm2-luks\", or \"tpm2-luks-passphrase\"")
	}
	if (r.Encryption.Type == "luks-passphrase" || r.Encryption.Type == "tpm2-luks-passphrase") && r.Encryption.Passphrase == "" {
		return fmt.Errorf("encryption.passphrase required for %s", r.Encryption.Type)
	}
	// image may be empty in live-ISO mode; bootc auto-detects the running container.
	if r.Hostname == "" {
		return fmt.Errorf("hostname is required")
	}
	return nil
}
