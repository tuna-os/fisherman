package recipe_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

func TestValidate(t *testing.T) {
	// Create a real file to satisfy os.Stat in Validate.
	dir := t.TempDir()
	diskPath := filepath.Join(dir, "fake-disk")
	if err := os.WriteFile(diskPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		r       recipe.Recipe
		wantErr string // substring; empty means no error expected
	}{
		// ── Valid recipes ──────────────────────────────────────────────────────
		{
			name: "valid xfs",
			r:    recipe.Recipe{Disk: diskPath, Filesystem: "xfs", Hostname: "host1"},
		},
		{
			name: "valid btrfs",
			r:    recipe.Recipe{Disk: diskPath, Filesystem: "btrfs", Hostname: "host1"},
		},
		{
			name: "valid btrfs subvolumes",
			r:    recipe.Recipe{Disk: diskPath, Filesystem: "btrfs", BtrfsSubvolumes: true, Hostname: "host1"},
		},
		{
			name: "valid encryption none",
			r:    recipe.Recipe{Disk: diskPath, Filesystem: "xfs", Hostname: "h", Encryption: recipe.Encryption{Type: "none"}},
		},
		{
			name: "valid empty encryption type",
			r:    recipe.Recipe{Disk: diskPath, Filesystem: "xfs", Hostname: "h"},
		},
		{
			name: "valid luks-passphrase",
			r: recipe.Recipe{
				Disk: diskPath, Filesystem: "xfs", Hostname: "h",
				Encryption: recipe.Encryption{Type: "luks-passphrase", Passphrase: "secret"},
			},
		},
		{
			name: "valid tpm2-luks (no passphrase required)",
			r: recipe.Recipe{
				Disk: diskPath, Filesystem: "xfs", Hostname: "h",
				Encryption: recipe.Encryption{Type: "tpm2-luks"},
			},
		},
		{
			name: "valid tpm2-luks-passphrase",
			r: recipe.Recipe{
				Disk: diskPath, Filesystem: "xfs", Hostname: "h",
				Encryption: recipe.Encryption{Type: "tpm2-luks-passphrase", Passphrase: "secret"},
			},
		},
		{
			name: "valid composefs_backend true",
			r:    recipe.Recipe{Disk: diskPath, Filesystem: "btrfs", Hostname: "h", ComposeFsBackend: true},
		},
		{
			name: "valid composefs_backend with luks-passphrase",
			r: recipe.Recipe{
				Disk: diskPath, Filesystem: "btrfs", Hostname: "h",
				ComposeFsBackend: true,
				Encryption:       recipe.Encryption{Type: "luks-passphrase", Passphrase: "secret"},
			},
		},
		{
			name: "valid composefs_backend with btrfs",
			r:    recipe.Recipe{Disk: diskPath, Filesystem: "btrfs", Hostname: "h", ComposeFsBackend: true},
		},

		// ── Invalid: disk ─────────────────────────────────────────────────────
		{
			name:    "empty disk",
			r:       recipe.Recipe{Filesystem: "xfs", Hostname: "h"},
			wantErr: "disk is required",
		},
		{
			name:    "nonexistent disk",
			r:       recipe.Recipe{Disk: "/dev/definitely-does-not-exist-xyzzy", Filesystem: "xfs", Hostname: "h"},
			wantErr: "disk /dev/definitely-does-not-exist-xyzzy",
		},

		// ── Invalid: filesystem ───────────────────────────────────────────────
		{
			name:    "empty filesystem",
			r:       recipe.Recipe{Disk: diskPath, Hostname: "h"},
			wantErr: `filesystem must be`,
		},
		{
			name:    "unsupported filesystem ext4",
			r:       recipe.Recipe{Disk: diskPath, Filesystem: "ext4", Hostname: "h"},
			wantErr: `filesystem must be`,
		},
		{
			name:    "btrfsSubvolumes without btrfs",
			r:       recipe.Recipe{Disk: diskPath, Filesystem: "xfs", BtrfsSubvolumes: true, Hostname: "h"},
			wantErr: "btrfsSubvolumes requires filesystem=btrfs",
		},
		{
			name:    "composefs with xfs (no fs-verity)",
			r:       recipe.Recipe{Disk: diskPath, Filesystem: "xfs", Hostname: "h", ComposeFsBackend: true},
			wantErr: "composeFsBackend requires a filesystem with fs-verity support",
		},

		// ── Invalid: imageType ────────────────────────────────────────────────
		{
			name:    "imageType ostree not yet supported",
			r:       recipe.Recipe{Disk: diskPath, Filesystem: "xfs", Hostname: "h", ImageType: "ostree"},
			wantErr: "imageType \"ostree\" is not yet supported",
		},
		{
			name:    "imageType unknown value",
			r:       recipe.Recipe{Disk: diskPath, Filesystem: "xfs", Hostname: "h", ImageType: "flatpak"},
			wantErr: "imageType must be",
		},

		// ── Invalid: encryption ───────────────────────────────────────────────
		{
			name: "luks-passphrase empty passphrase",
			r: recipe.Recipe{
				Disk: diskPath, Filesystem: "xfs", Hostname: "h",
				Encryption: recipe.Encryption{Type: "luks-passphrase"},
			},
			wantErr: "passphrase required",
		},
		{
			name: "tpm2-luks-passphrase empty passphrase",
			r: recipe.Recipe{
				Disk: diskPath, Filesystem: "xfs", Hostname: "h",
				Encryption: recipe.Encryption{Type: "tpm2-luks-passphrase"},
			},
			wantErr: "passphrase required",
		},
		{
			name: "unknown encryption type",
			r: recipe.Recipe{
				Disk: diskPath, Filesystem: "xfs", Hostname: "h",
				Encryption: recipe.Encryption{Type: "invalid-type"},
			},
			wantErr: "encryption.type must be",
		},

		// ── Invalid: hostname ─────────────────────────────────────────────────
		{
			name:    "empty hostname",
			r:       recipe.Recipe{Disk: diskPath, Filesystem: "xfs"},
			wantErr: "hostname is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.r.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("Validate() expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Validate() error = %q, want containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	t.Run("valid JSON", func(t *testing.T) {
		r := &recipe.Recipe{
			Disk:       "/dev/sda",
			Filesystem: "xfs",
			Hostname:   "myhost",
			Flatpaks:   []string{"org.mozilla.firefox"},
		}
		data, _ := json.Marshal(r)
		path := filepath.Join(dir, "recipe.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		loaded, err := recipe.Load(path)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if loaded.Disk != "/dev/sda" {
			t.Errorf("Disk = %q, want /dev/sda", loaded.Disk)
		}
		if loaded.Filesystem != "xfs" {
			t.Errorf("Filesystem = %q, want xfs", loaded.Filesystem)
		}
		if loaded.Hostname != "myhost" {
			t.Errorf("Hostname = %q, want myhost", loaded.Hostname)
		}
		if len(loaded.Flatpaks) != 1 || loaded.Flatpaks[0] != "org.mozilla.firefox" {
			t.Errorf("Flatpaks = %v, want [org.mozilla.firefox]", loaded.Flatpaks)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := recipe.Load(filepath.Join(dir, "nonexistent.json"))
		if err == nil {
			t.Fatal("expected error for missing file")
		}
		if !strings.Contains(err.Error(), "reading recipe") {
			t.Errorf("error = %q, want containing 'reading recipe'", err.Error())
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := recipe.Load(path)
		if err == nil {
			t.Fatal("expected error for malformed JSON")
		}
		if !strings.Contains(err.Error(), "parsing recipe") {
			t.Errorf("error = %q, want containing 'parsing recipe'", err.Error())
		}
	})
}
