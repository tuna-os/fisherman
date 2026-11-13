package config

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tuna-os/fisherman/tui/internal/catalog"
)

// InstallConfig holds all collected installer state.
type InstallConfig struct {
	// Image is the selected image with the boot stack it needs. Its
	// settings go into the recipe as they are (#367).
	Image catalog.Image

	// Disk
	DiskDevice string // e.g. "/dev/sda"
	DiskLabel  string // human-readable label for display

	// Filesystem
	Filesystem string // "ext4", "xfs", "btrfs"
	Hostname   string

	// Encryption
	EncryptionEnabled bool
	Passphrase        string

	// User. Collected only when Image.NeedsUserCreation.
	FullName string
	Username string
	Password string

	// RecoveryKey is the LUKS recovery key fisherman emitted during the
	// install (a `recovery_key` event), to show before the reboot.
	RecoveryKey string
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
	User             *userSpec  `json:"user,omitempty"`
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

// Redacted replaces a secret in the recipe shown to the user.
const Redacted = "********"

// ErrXFSWithComposeFS is the one cross-field rule the TUI checks itself, so
// that it holds with no backend to ask: composefs needs fs-verity, and XFS
// has none. fisherman refuses the pair too (composefs_requires_fsverity).
var ErrXFSWithComposeFS = errors.New("this image needs the composefs backend, which cannot use XFS (no fs-verity): choose ext4 or btrfs")

// Check reports a combination of settings that must not reach the confirm
// page or the backend.
func (c *InstallConfig) Check() error {
	if c.Image.ComposeFsBackend && c.Filesystem == "xfs" {
		return ErrXFSWithComposeFS
	}
	if c.Filesystem != "" && !c.Image.SupportsFilesystem(c.Filesystem) {
		return fmt.Errorf("the selected image does not support the %s filesystem", c.Filesystem)
	}
	return nil
}

func (c *InstallConfig) recipe() fishermanRecipe {
	r := fishermanRecipe{
		Disk:             c.DiskDevice,
		Filesystem:       c.Filesystem,
		Image:            c.Image.Ref,
		Hostname:         c.Hostname,
		ComposeFsBackend: c.Image.ComposeFsBackend,
		Bootloader:       c.Image.Bootloader,
		Encryption:       encryption{Type: "none"},
	}
	if c.EncryptionEnabled {
		r.Encryption = encryption{Type: "luks-passphrase", Passphrase: c.Passphrase}
	}
	// An image with its own first-boot setup creates the account there: no
	// `user` block, so fisherman creates none.
	if c.Image.NeedsUserCreation {
		r.User = &userSpec{
			Username: c.Username,
			Fullname: c.FullName,
			Password: c.Password,
			Groups:   []string{"wheel"},
		}
	}
	return r
}

// Recipe returns the fisherman recipe as JSON, for fisherman's stdin. It
// holds the passphrase and the password: never write it to a file, put it in
// argv, or show it. Use RedactedRecipe for display.
func (c *InstallConfig) Recipe() ([]byte, error) {
	if err := c.Check(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(c.recipe())
	if err != nil {
		return nil, fmt.Errorf("marshaling recipe: %w", err)
	}
	return data, nil
}

// RedactedRecipe returns the recipe for display: indented, with the LUKS
// passphrase and the user's password replaced by Redacted.
func (c *InstallConfig) RedactedRecipe() (string, error) {
	if err := c.Check(); err != nil {
		return "", err
	}
	r := c.recipe()
	if r.Encryption.Passphrase != "" {
		r.Encryption.Passphrase = Redacted
	}
	if r.User != nil && r.User.Password != "" {
		r.User.Password = Redacted
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling recipe: %w", err)
	}
	return string(data), nil
}

// Fields returns a partial recipe with only the named top-level keys, for
// `fisherman validate --json --partial -`. composeFsBackend and encryption
// ride along with filesystem, because fisherman's filesystem rule reads them.
func (c *InstallConfig) Fields(keys ...string) ([]byte, error) {
	full, err := json.Marshal(c.recipe())
	if err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(full, &all); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, k := range keys {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return json.Marshal(out)
}
