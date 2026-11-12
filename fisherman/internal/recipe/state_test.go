package recipe

import "testing"

// Each derived-state method, over the values that decide it.

func TestHasEncryption(t *testing.T) {
	for typ, want := range map[string]bool{
		"":                     false,
		"none":                 false,
		"luks-passphrase":      true,
		"tpm2-luks":            true,
		"tpm2-luks-passphrase": true,
		"luks2-made-up":        true, // fail closed; Problems refuses it
	} {
		r := &Recipe{Encryption: Encryption{Type: typ}}
		if got := r.HasEncryption(); got != want {
			t.Errorf("HasEncryption(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestHasTPM2(t *testing.T) {
	for typ, want := range map[string]bool{
		"":                     false,
		"none":                 false,
		"luks-passphrase":      false,
		"tpm2-luks":            true,
		"tpm2-luks-passphrase": true,
		"tpm2-made-up":         false,
	} {
		r := &Recipe{Encryption: Encryption{Type: typ}}
		if got := r.HasTPM2(); got != want {
			t.Errorf("HasTPM2(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestIsManualLayout(t *testing.T) {
	if (&Recipe{Disk: "/dev/sda"}).IsManualLayout() {
		t.Error("an auto-partition recipe is not a manual layout")
	}
	if (&Recipe{CustomMounts: []CustomMount{}}).IsManualLayout() {
		t.Error("an empty customMounts list is not a manual layout")
	}
	if !(&Recipe{CustomMounts: []CustomMount{{Partition: "/dev/sda2", Target: "/"}}}).IsManualLayout() {
		t.Error("a recipe with customMounts is a manual layout")
	}
}

func TestIsZFS(t *testing.T) {
	for fs, want := range map[string]bool{"": false, "xfs": false, "ext4": false, "btrfs": false, "zfs": true} {
		if got := (&Recipe{Filesystem: fs}).IsZFS(); got != want {
			t.Errorf("IsZFS(%q) = %v, want %v", fs, got, want)
		}
	}
}

func TestUsesSystemdBoot(t *testing.T) {
	cases := []struct {
		bootloader, fs string
		want           bool
	}{
		{"", "xfs", false},
		{"grub2", "btrfs", false},
		{"systemd", "ext4", true},
		{"systemd", "btrfs", true},
		{"", "zfs", true}, // ZFS always takes the systemd-boot layout
		{"grub2", "zfs", true},
	}
	for _, c := range cases {
		r := &Recipe{Bootloader: c.bootloader, Filesystem: c.fs}
		if got := r.UsesSystemdBoot(); got != c.want {
			t.Errorf("UsesSystemdBoot(bootloader=%q, fs=%q) = %v, want %v", c.bootloader, c.fs, got, c.want)
		}
	}
}

func TestUsesComposeFs(t *testing.T) {
	cases := []struct {
		composefs bool
		fs        string
		want      bool
	}{
		{false, "xfs", false},
		{true, "ext4", true},
		{false, "zfs", true}, // ZFS forces the composefs backend
		{true, "zfs", true},
	}
	for _, c := range cases {
		r := &Recipe{ComposeFsBackend: c.composefs, Filesystem: c.fs}
		if got := r.UsesComposeFs(); got != c.want {
			t.Errorf("UsesComposeFs(composeFsBackend=%v, fs=%q) = %v, want %v", c.composefs, c.fs, got, c.want)
		}
	}
}

func TestVarDiskState(t *testing.T) {
	cases := []struct {
		name         string
		varDisk      *VarDiskSpec
		has, formats bool
	}{
		{"none", nil, false, false},
		{"format", &VarDiskSpec{Disk: "/dev/sdb"}, true, true},
		{"keep", &VarDiskSpec{Disk: "/dev/sdb", KeepExisting: true}, true, false},
	}
	for _, c := range cases {
		r := &Recipe{VarDisk: c.varDisk}
		if got := r.HasVarDisk(); got != c.has {
			t.Errorf("%s: HasVarDisk = %v, want %v", c.name, got, c.has)
		}
		if got := r.FormatsVarDisk(); got != c.formats {
			t.Errorf("%s: FormatsVarDisk = %v, want %v", c.name, got, c.formats)
		}
	}
}

func TestEncryptsRootAndEnrollsTPM2(t *testing.T) {
	manual := []CustomMount{{Partition: "/dev/sda2", Target: "/"}}
	cases := []struct {
		name       string
		typ        string
		mounts     []CustomMount
		luks, tpm2 bool
	}{
		{"plain", "none", nil, false, false},
		{"luks", "luks-passphrase", nil, true, false},
		{"tpm2", "tpm2-luks", nil, true, true},
		{"tpm2+passphrase", "tpm2-luks-passphrase", nil, true, true},
		// Problems refuses these; the install path still never runs the
		// LUKS or TPM2 step on a manual layout.
		{"manual luks", "luks-passphrase", manual, false, false},
		{"manual tpm2", "tpm2-luks", manual, false, false},
	}
	for _, c := range cases {
		r := &Recipe{Encryption: Encryption{Type: c.typ}, CustomMounts: c.mounts}
		if got := r.EncryptsRoot(); got != c.luks {
			t.Errorf("%s: EncryptsRoot = %v, want %v", c.name, got, c.luks)
		}
		if got := r.EnrollsTPM2(); got != c.tpm2 {
			t.Errorf("%s: EnrollsTPM2 = %v, want %v", c.name, got, c.tpm2)
		}
	}
}

// The invariants state.go relies on: a TPM2 type always encrypts, and
// Problems refuses encryption on a manual layout and on ZFS.
func TestStateInvariants(t *testing.T) {
	for _, c := range EncryptionChoices {
		r := &Recipe{Encryption: Encryption{Type: c.Type}}
		if r.HasTPM2() && !r.HasEncryption() {
			t.Errorf("encryption type %q enrols a TPM2 but does not encrypt", c.Type)
		}
		if r.HasEncryption() != c.Encrypted {
			t.Errorf("encryption type %q: HasEncryption = %v, table says Encrypted = %v", c.Type, r.HasEncryption(), c.Encrypted)
		}
	}
	has := func(ps []Problem, code string) bool {
		for _, p := range ps {
			if p.Code == code {
				return true
			}
		}
		return false
	}
	for _, typ := range []string{"luks-passphrase", "tpm2-luks", "tpm2-luks-passphrase"} {
		manual := &Recipe{Encryption: Encryption{Type: typ, Passphrase: "x"},
			CustomMounts: []CustomMount{{Partition: "/dev/null", Target: "/", Fstype: "xfs"}}}
		if !has(manual.Problems(), CodeEncryptionUnsupportedOnManual) {
			t.Errorf("manual layout with encryption %q: want %s", typ, CodeEncryptionUnsupportedOnManual)
		}
		zfs := &Recipe{Filesystem: "zfs", Encryption: Encryption{Type: typ, Passphrase: "x"}}
		if !has(CheckLayoutCombos(zfs), CodeEncryptionUnsupportedOnZFS) {
			t.Errorf("zfs with encryption %q: want %s", typ, CodeEncryptionUnsupportedOnZFS)
		}
	}
}
