package recipe_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// TestValidateVarDiskEncryption: fisherman has no LUKS step for the /var
// disk, so every encrypted type is refused with a varDisk, before any disk
// work, instead of leaving /var/home in plain text (#406).
func TestValidateVarDiskEncryption(t *testing.T) {
	dir := t.TempDir()
	disk, varDisk := filepath.Join(dir, "disk"), filepath.Join(dir, "var-disk")
	for _, p := range []string{disk, varDisk} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range recipe.EncryptionChoices {
		r := recipe.Recipe{Disk: disk, Hostname: "h", Filesystem: "xfs", VarDisk: &recipe.VarDiskSpec{Disk: varDisk}}
		r.Encryption = recipe.Encryption{Type: c.Type, Passphrase: "correct horse"}
		got := hasCode(r.Problems(), "varDisk", recipe.CodeVarDiskEncryptionUnsupported)
		if got != c.Encrypted {
			t.Errorf("encryption %q with varDisk: refused = %v, want %v", c.Type, got, c.Encrypted)
		}
		// keepExisting does not change it: the kept disk is plain text too.
		r.VarDisk.KeepExisting = true
		if got := hasCode(r.Problems(), "varDisk", recipe.CodeVarDiskEncryptionUnsupported); got != c.Encrypted {
			t.Errorf("encryption %q with a kept varDisk: refused = %v, want %v", c.Type, got, c.Encrypted)
		}
		r.VarDisk = nil
		if hasCode(r.Problems(), "varDisk", recipe.CodeVarDiskEncryptionUnsupported) {
			t.Errorf("encryption %q without varDisk must not be refused", c.Type)
		}
	}
}

func TestClassifyCustomMount(t *testing.T) {
	for target, want := range map[string]recipe.MountKind{
		"":           recipe.MountIgnored,
		"swap":       recipe.MountSwap,
		"/":          recipe.MountBootStack,
		"/boot":      recipe.MountBootStack,
		"/boot/efi/": recipe.MountBootStack,
		"/var":       recipe.MountData,
		"/var/home":  recipe.MountData,
		"/home":      recipe.MountData,
		"/data":      recipe.MountData,
	} {
		if got := recipe.ClassifyCustomMount(target); got != want {
			t.Errorf("ClassifyCustomMount(%q) = %v, want %v", target, got, want)
		}
	}
}

// TestCustomMountsPersistence: a data mount is accepted only where the
// installed system can mount it again, and a target or a device is named
// once (#372).
func TestCustomMountsPersistence(t *testing.T) {
	cases := []struct {
		name      string
		target    string
		composefs bool
		want      string // code on customMounts[3].target, "" for none
	}{
		{"ostree /var", "/var", false, ""},
		{"ostree /var/home", "/var/home", false, ""},
		{"ostree /home is resolved after the install", "/home", false, ""},
		{"ostree /usr/local", "/usr/local", false, ""},
		{"ostree /srv/data", "/srv/data", false, ""},
		{"ostree /data: read-only root", "/data", false, recipe.CodeCustomMountNotPersistable},
		{"ostree /etc/foo", "/etc/foo", false, recipe.CodeCustomMountNotPersistable},
		{"ostree /usr", "/usr", false, recipe.CodeCustomMountNotPersistable},
		{"ostree /boot/extra", "/boot/extra", false, recipe.CodeCustomMountNotPersistable},
		{"ostree /variable is not /var", "/variable", false, recipe.CodeCustomMountNotPersistable},
		{"composefs /var: #252", "/var", true, recipe.CodeCustomMountNotPersistable},
		{"composefs /var/", "/var/", true, recipe.CodeCustomMountNotPersistable},
		{"composefs /var/home", "/var/home", true, ""},
		{"composefs /home cannot be resolved", "/home", true, recipe.CodeCustomMountNotPersistable},
		{"swap", "swap", true, ""},
		{"no target", "", true, ""},
	}
	for _, c := range cases {
		r := manualRecipe(t, "ext4", "none")
		r.ComposeFsBackend = c.composefs
		r.CustomMounts = append(r.CustomMounts, recipe.CustomMount{Partition: extraPartition(r), Target: c.target, Fstype: "ext4"})
		ps := r.Problems()
		if c.want == "" {
			for _, p := range ps {
				t.Errorf("%s: unexpected problem %s: %s", c.name, p.Code, p.Message)
			}
			continue
		}
		if !hasCode(ps, "customMounts[3].target", c.want) {
			t.Errorf("%s: want %s on customMounts[3].target, got %+v", c.name, c.want, ps)
		}
	}
}

func TestCustomMountsDuplicates(t *testing.T) {
	t.Run("same target twice", func(t *testing.T) {
		r := manualRecipe(t, "ext4", "none")
		r.CustomMounts = append(r.CustomMounts, recipe.CustomMount{Partition: extraPartition(r), Target: "/boot/", Fstype: "ext4"})
		if !hasCode(r.Problems(), "customMounts[3].target", recipe.CodeCustomMountTargetDuplicate) {
			t.Errorf("want %s, got %+v", recipe.CodeCustomMountTargetDuplicate, r.Problems())
		}
	})
	t.Run("same partition twice", func(t *testing.T) {
		r := manualRecipe(t, "ext4", "none")
		r.CustomMounts = append(r.CustomMounts, recipe.CustomMount{Partition: r.CustomMounts[0].Partition, Target: "/var", Fstype: "ext4"})
		if !hasCode(r.Problems(), "customMounts[3].partition", recipe.CodeCustomMountPartitionDuplicate) {
			t.Errorf("want %s, got %+v", recipe.CodeCustomMountPartitionDuplicate, r.Problems())
		}
	})
	t.Run("an alias of a partition already used", func(t *testing.T) {
		r := manualRecipe(t, "ext4", "none")
		alias := filepath.Join(t.TempDir(), "by-id-alias")
		if err := os.Symlink(r.CustomMounts[0].Partition, alias); err != nil {
			t.Fatal(err)
		}
		r.CustomMounts = append(r.CustomMounts, recipe.CustomMount{Partition: alias, Target: "/var", Fstype: "ext4"})
		if !hasCode(r.Problems(), "customMounts[3].partition", recipe.CodeCustomMountPartitionDuplicate) {
			t.Errorf("want %s for a symlink to the root partition, got %+v", recipe.CodeCustomMountPartitionDuplicate, r.Problems())
		}
	})
	t.Run("two swap partitions are fine", func(t *testing.T) {
		r := manualRecipe(t, "ext4", "none")
		second := filepath.Join(t.TempDir(), "swap2")
		if err := os.WriteFile(second, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		r.CustomMounts = append(r.CustomMounts,
			recipe.CustomMount{Partition: extraPartition(r), Target: "swap", Fstype: "swap"},
			recipe.CustomMount{Partition: second, Target: "swap", Fstype: "swap"})
		for _, p := range r.Problems() {
			t.Errorf("unexpected problem %s: %s", p.Code, p.Message)
		}
	})
}
