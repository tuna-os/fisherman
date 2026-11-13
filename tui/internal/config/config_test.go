package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/tui/internal/catalog"
)

var (
	grubImage = catalog.Custom("ghcr.io/example/marlin:latest", false, "")
	dakota    = catalog.Image{Ref: "ghcr.io/projectbluefin/dakota:latest", Bootloader: "systemd", ComposeFsBackend: true,
		Filesystem: "btrfs", Filesystems: []string{"btrfs", "ext4"}}
)

func parse(t *testing.T, data []byte) fishermanRecipe {
	t.Helper()
	var r fishermanRecipe
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("the recipe is not valid JSON: %v\n%s", err, data)
	}
	return r
}

func TestRecipe_NoEncryption(t *testing.T) {
	cfg := &InstallConfig{
		DiskDevice: "/dev/sda",
		Filesystem: "ext4",
		Image:      grubImage,
		Hostname:   "myhost",
		Username:   "alice",
		FullName:   "Alice Smith",
		Password:   "hunter2",
	}
	data, err := cfg.Recipe()
	if err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	r := parse(t, data)

	if r.Disk != "/dev/sda" {
		t.Errorf("disk: got %q want /dev/sda", r.Disk)
	}
	if r.Image != "ghcr.io/example/marlin:latest" {
		t.Errorf("image: got %q", r.Image)
	}
	if r.Filesystem != "ext4" {
		t.Errorf("filesystem: got %q want ext4", r.Filesystem)
	}
	if r.Hostname != "myhost" {
		t.Errorf("hostname: got %q want myhost", r.Hostname)
	}
	if r.Encryption.Type != "none" {
		t.Errorf("encryption.type: got %q want none", r.Encryption.Type)
	}
	if r.Encryption.Passphrase != "" {
		t.Errorf("encryption.passphrase should be empty, got %q", r.Encryption.Passphrase)
	}
	if r.User == nil || r.User.Username != "alice" || r.User.Password != "hunter2" {
		t.Fatalf("user: got %+v", r.User)
	}
	if len(r.User.Groups) != 1 || r.User.Groups[0] != "wheel" {
		t.Errorf("user.groups: got %v want [wheel]", r.User.Groups)
	}
}

func TestRecipe_WithLUKS(t *testing.T) {
	cfg := &InstallConfig{
		DiskDevice:        "/dev/nvme0n1",
		Filesystem:        "xfs",
		Image:             grubImage,
		Hostname:          "encrypted-host",
		Username:          "bob",
		FullName:          "Bob Jones",
		Password:          "s3cr3t",
		EncryptionEnabled: true,
		Passphrase:        "my-luks-passphrase",
	}
	data, err := cfg.Recipe()
	if err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	r := parse(t, data)
	if r.Encryption.Type != "luks-passphrase" {
		t.Errorf("encryption.type: got %q want luks-passphrase", r.Encryption.Type)
	}
	if r.Encryption.Passphrase != "my-luks-passphrase" {
		t.Errorf("encryption.passphrase: got %q want my-luks-passphrase", r.Encryption.Passphrase)
	}
}

// The image's boot stack reaches the recipe. Before #367 every recipe said
// composeFsBackend=false and bootloader="", whatever the image.
func TestRecipe_CarriesImageBootStack(t *testing.T) {
	cfg := &InstallConfig{DiskDevice: "/dev/sda", Filesystem: "btrfs", Image: dakota, Hostname: "h",
		Username: "ignored", Password: "ignored-password"}
	data, err := cfg.Recipe()
	if err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	r := parse(t, data)
	if !r.ComposeFsBackend || r.Bootloader != "systemd" || r.Filesystem != "btrfs" {
		t.Errorf("got composeFsBackend=%v bootloader=%q filesystem=%q, want true systemd btrfs",
			r.ComposeFsBackend, r.Bootloader, r.Filesystem)
	}
	// Dakota creates its account at first boot: no user block, and none of
	// what an earlier image's user page collected.
	if r.User != nil {
		t.Errorf("user: got %+v, want none for an image with needs_user_creation=false", r.User)
	}
	if strings.Contains(string(data), "ignored") {
		t.Errorf("the recipe leaks user data that must not be sent: %s", data)
	}
}

// XFS with composefs never becomes a recipe.
func TestCheck_RejectsXFSWithComposeFS(t *testing.T) {
	cfg := &InstallConfig{DiskDevice: "/dev/sda", Filesystem: "xfs", Image: dakota, Hostname: "h"}
	if err := cfg.Check(); !errors.Is(err, ErrXFSWithComposeFS) {
		t.Fatalf("Check = %v, want ErrXFSWithComposeFS", err)
	}
	if _, err := cfg.Recipe(); !errors.Is(err, ErrXFSWithComposeFS) {
		t.Errorf("Recipe = %v, want ErrXFSWithComposeFS", err)
	}
	if _, err := cfg.RedactedRecipe(); !errors.Is(err, ErrXFSWithComposeFS) {
		t.Errorf("RedactedRecipe = %v, want ErrXFSWithComposeFS", err)
	}
	// A custom image the user declared composefs is held to the same rule.
	cfg.Image = catalog.Custom("registry.example.com/os:1", true, "systemd")
	if err := cfg.Check(); !errors.Is(err, ErrXFSWithComposeFS) {
		t.Errorf("custom composefs image: Check = %v", err)
	}
	cfg.Filesystem = "ext4"
	if err := cfg.Check(); err != nil {
		t.Errorf("ext4 with composefs: Check = %v, want nil", err)
	}
	// XFS is fine on a GRUB/ostree image.
	cfg.Image, cfg.Filesystem = grubImage, "xfs"
	if err := cfg.Check(); err != nil {
		t.Errorf("xfs on ostree: Check = %v, want nil", err)
	}
}

// The recipe shown in a dry run has no secret in it (#417).
func TestRedactedRecipe_MasksSecrets(t *testing.T) {
	cfg := &InstallConfig{
		DiskDevice: "/dev/sda", Filesystem: "ext4", Image: grubImage, Hostname: "host",
		Username: "alice", Password: "user-password-123",
		EncryptionEnabled: true, Passphrase: "luks-passphrase-456",
	}
	shown, err := cfg.RedactedRecipe()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"user-password-123", "luks-passphrase-456"} {
		if strings.Contains(shown, secret) {
			t.Errorf("the redacted recipe shows %q:\n%s", secret, shown)
		}
	}
	r := parse(t, []byte(shown))
	if r.Encryption.Passphrase != Redacted || r.User.Password != Redacted {
		t.Errorf("want both secrets replaced by %q, got %q and %q", Redacted, r.Encryption.Passphrase, r.User.Password)
	}
	if r.User.Username != "alice" || r.Disk != "/dev/sda" {
		t.Errorf("only the secrets change: %+v", r)
	}
	// The real recipe still carries them, for stdin.
	real, _ := cfg.Recipe()
	if !strings.Contains(string(real), "user-password-123") || !strings.Contains(string(real), "luks-passphrase-456") {
		t.Error("Recipe() must carry the secrets")
	}
	// No encryption: nothing to mask, and no placeholder invented.
	cfg.EncryptionEnabled = false
	shown, _ = cfg.RedactedRecipe()
	if parse(t, []byte(shown)).Encryption.Passphrase != "" {
		t.Errorf("an absent passphrase must stay absent:\n%s", shown)
	}
}

func TestFields(t *testing.T) {
	cfg := &InstallConfig{DiskDevice: "/dev/sda", Filesystem: "ext4", Image: dakota, Hostname: "host"}
	data, err := cfg.Fields("filesystem", "composeFsBackend", "user", "no-such-key")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	// user is absent for Dakota, and unknown keys are not invented.
	if len(got) != 2 || got["filesystem"] != "ext4" || got["composeFsBackend"] != true {
		t.Errorf("Fields = %s", data)
	}
}
