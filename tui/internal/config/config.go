package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// InstallConfig holds all collected installer state.
type InstallConfig struct {
	// Network
	NetworkInterface string
	StaticIP         string
	Gateway          string
	DNS              string

	// Image
	Image string

	// Disk
	DiskDevice string // e.g. "/dev/sda"
	DiskLabel  string // human-readable label for display

	// Filesystem
	Filesystem string // "ext4", "xfs", "btrfs"
	Hostname   string

	// Encryption
	EncryptionEnabled bool
	EncryptionType    string // "none", "luks-passphrase"
	Passphrase        string

	// User
	FullName string
	Username string
	Password string

	// SSH Keys
	SSHKeys []string // collected but not in recipe (no recipe field)
}

// fishermanRecipe is the JSON structure fisherman expects.
type fishermanRecipe struct {
	Disk             string     `json:"disk"`
	Filesystem       string     `json:"filesystem"`
	Image            string     `json:"image"`
	Hostname         string     `json:"hostname"`
	ComposeFsBackend bool       `json:"composeFsBackend"`
	Bootloader       string     `json:"bootloader"`
	Encryption       encryption `json:"encryption"`
	User             userSpec   `json:"user"`
}

type encryption struct {
	Type       string `json:"type"`
	Passphrase string `json:"passphrase,omitempty"`
}

type userSpec struct {
	Username string   `json:"username"`
	Fullname string   `json:"fullname"`
	Password string   `json:"password"`
	Groups   []string `json:"groups"`
}

// WriteRecipe serializes the config as a fisherman recipe JSON to path.
func (c *InstallConfig) WriteRecipe(path string) error {
	encType := "none"
	encPass := ""
	if c.EncryptionEnabled {
		encType = "luks-passphrase"
		encPass = c.Passphrase
	}

	groups := []string{"wheel"}

	r := fishermanRecipe{
		Disk:             c.DiskDevice,
		Filesystem:       c.Filesystem,
		Image:            c.Image,
		Hostname:         c.Hostname,
		ComposeFsBackend: false,
		Bootloader:       "",
		Encryption: encryption{
			Type:       encType,
			Passphrase: encPass,
		},
		User: userSpec{
			Username: c.Username,
			Fullname: c.FullName,
			Password: c.Password,
			Groups:   groups,
		},
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling recipe: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing recipe: %w", err)
	}
	return nil
}
