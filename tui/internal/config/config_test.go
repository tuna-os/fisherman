package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRecipe_NoEncryption(t *testing.T) {
	cfg := &InstallConfig{
		DiskDevice: "/dev/sda",
		Filesystem: "ext4",
		Image:      "ghcr.io/projectbluefin/dakota:latest",
		Hostname:   "myhost",
		Username:   "alice",
		FullName:   "Alice Smith",
		Password:   "hunter2",
	}

	path := filepath.Join(t.TempDir(), "recipe.json")
	if err := cfg.WriteRecipe(path); err != nil {
		t.Fatalf("WriteRecipe: %v", err)
	}

	data, _ := os.ReadFile(path)
	var r fishermanRecipe
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if r.Disk != "/dev/sda" {
		t.Errorf("disk: got %q want /dev/sda", r.Disk)
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
	if r.User.Username != "alice" {
		t.Errorf("user.username: got %q want alice", r.User.Username)
	}
	if len(r.User.Groups) != 1 || r.User.Groups[0] != "wheel" {
		t.Errorf("user.groups: got %v want [wheel]", r.User.Groups)
	}
}

func TestWriteRecipe_WithLUKS(t *testing.T) {
	cfg := &InstallConfig{
		DiskDevice:        "/dev/nvme0n1",
		Filesystem:        "xfs",
		Image:             "ghcr.io/projectbluefin/dakota:latest",
		Hostname:          "encrypted-host",
		Username:          "bob",
		FullName:          "Bob Jones",
		Password:          "s3cr3t",
		EncryptionEnabled: true,
		Passphrase:        "my-luks-passphrase",
	}

	path := filepath.Join(t.TempDir(), "recipe.json")
	if err := cfg.WriteRecipe(path); err != nil {
		t.Fatalf("WriteRecipe: %v", err)
	}

	data, _ := os.ReadFile(path)
	var r fishermanRecipe
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if r.Encryption.Type != "luks-passphrase" {
		t.Errorf("encryption.type: got %q want luks-passphrase", r.Encryption.Type)
	}
	if r.Encryption.Passphrase != "my-luks-passphrase" {
		t.Errorf("encryption.passphrase: got %q want my-luks-passphrase", r.Encryption.Passphrase)
	}
}

func TestWriteRecipe_FilePermissions(t *testing.T) {
	cfg := &InstallConfig{
		DiskDevice: "/dev/sda",
		Filesystem: "btrfs",
		Image:      "ghcr.io/example/image:latest",
		Hostname:   "host",
		Username:   "user",
		Password:   "pass",
	}

	path := filepath.Join(t.TempDir(), "recipe.json")
	if err := cfg.WriteRecipe(path); err != nil {
		t.Fatalf("WriteRecipe: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions: got %o want 600", perm)
	}
}

func TestWriteRecipe_ValidJSON(t *testing.T) {
	cfg := &InstallConfig{
		DiskDevice: "/dev/sda",
		Filesystem: "ext4",
		Image:      "ghcr.io/example/image:latest",
		Hostname:   "host",
		Username:   "user",
		Password:   "pass",
	}

	path := filepath.Join(t.TempDir(), "recipe.json")
	if err := cfg.WriteRecipe(path); err != nil {
		t.Fatalf("WriteRecipe: %v", err)
	}

	data, _ := os.ReadFile(path)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, data)
	}
}
