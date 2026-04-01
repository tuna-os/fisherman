package recipe

import (
	"encoding/json"
	"fmt"
	"os"
)

// Recipe describes a fisherman installation.
type Recipe struct {
	Disk            string        `json:"disk"`            // block device, e.g. "/dev/sda" (auto-partition)
	Filesystem      string        `json:"filesystem"`      // "xfs" or "btrfs"
	BtrfsSubvolumes bool          `json:"btrfsSubvolumes"` // create @, @home, @snapshots
	Encryption      Encryption    `json:"encryption"`
	Image           string        `json:"image"`           // source OCI image reference
	TargetImgref    string        `json:"targetImgref"`    // update-tracking ref (optional)
	SelinuxDisabled  bool          `json:"selinuxDisabled"`
	UnifiedStorage   bool          `json:"unifiedStorage"`  // pass --experimental-unified-storage
	// ComposeFsBackend passes --composefs-backend to bootc install to-filesystem.
	// Required for composefs-native images (e.g. ghcr.io/bootcrew/*).
	// Independent of UnifiedStorage — these are different bootc features.
	// Note: composefs requires fs-verity support; xfs does not support fs-verity.
	ComposeFsBackend bool          `json:"composeFsBackend"`
	// ImageType selects the install backend: "bootc" (default) or "ostree".
	// "ostree" support is not yet implemented and will be rejected by Validate().
	ImageType        string        `json:"imageType"`
	Hostname         string        `json:"hostname"`
	Flatpaks        []string      `json:"flatpaks"`        // flatpak app IDs to install; empty = fallback
	User            UserSpec      `json:"user"`            // optional user account to create
	// CustomMounts is set for manual partitioning. When non-empty, Disk/Filesystem/
	// BtrfsSubvolumes and the auto-partition steps are skipped; fisherman formats and
	// mounts the listed partitions directly.
	CustomMounts    []CustomMount `json:"customMounts,omitempty"`
}

// UserSpec describes a user account to create during installation.
// If Username is empty the user creation step is skipped.
type UserSpec struct {
	Username string   `json:"username"`
	Fullname string   `json:"fullname"`
	Password string   `json:"password"`
	Groups   []string `json:"groups"`
}

// CustomMount describes a single partition → mountpoint mapping for manual layouts.
type CustomMount struct {
	Partition string `json:"partition"` // e.g. "/dev/sda1"
	Target    string `json:"target"`    // mountpoint, e.g. "/" or "/boot/efi"
	Fstype    string `json:"fstype"`    // e.g. "xfs", "fat32", "ext4", "unformatted"
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
	if len(r.CustomMounts) > 0 {
		// Manual layout: validate each mount spec instead of the auto-partition fields.
		hasRoot := false
		for i, cm := range r.CustomMounts {
			if cm.Partition == "" {
				return fmt.Errorf("customMounts[%d]: partition is required", i)
			}
			if cm.Target != "swap" && cm.Target != "" {
				if _, err := os.Stat(cm.Partition); err != nil {
					return fmt.Errorf("customMounts[%d]: partition %s: %w", i, cm.Partition, err)
				}
			}
			if cm.Target == "/" {
				hasRoot = true
			}
		}
		if !hasRoot {
			return fmt.Errorf("customMounts: no root (/) partition specified")
		}
	} else {
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
		if r.ComposeFsBackend && r.Filesystem == "xfs" {
			return fmt.Errorf("composeFsBackend requires a filesystem with fs-verity support (ext4 or btrfs); xfs does not support fs-verity")
		}
	}
	switch r.ImageType {
	case "", "bootc":
		// ok
	case "ostree":
		return fmt.Errorf("imageType \"ostree\" is not yet supported; only \"bootc\" is implemented")
	default:
		return fmt.Errorf("imageType must be \"bootc\" (or empty), got %q", r.ImageType)
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
