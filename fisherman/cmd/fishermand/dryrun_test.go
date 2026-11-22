//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureDevDir builds a /dev tree whose disk/by-uuid/<uuid> points at vda3.
func fixtureDevDir(t *testing.T, uuid string) string {
	t.Helper()
	dir := t.TempDir()
	dev := filepath.Join(dir, "dev")
	if err := os.MkdirAll(filepath.Join(dev, "disk/by-uuid"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dev, "vda3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../vda3", filepath.Join(dev, "disk/by-uuid", uuid)); err != nil {
		t.Fatal(err)
	}
	return dev
}

// fixtureSysRoot builds a /sys tree with (or without) a TPM 2.0.
func fixtureSysRoot(t *testing.T, tpm2 bool) string {
	t.Helper()
	dir := t.TempDir()
	if !tpm2 {
		return dir
	}
	if err := os.MkdirAll(filepath.Join(dir, "class/tpm/tpm0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "class/tpm/tpm0/tpm_version_major"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAssembleDryRunTPM2(t *testing.T) {
	sfdisk, err := os.ReadFile(filepath.Join("..", "..", "internal", "repart", "testdata", "sfdisk-grub.json"))
	if err != nil {
		t.Fatal(err)
	}
	uuid := "9f3a2c1d-aaaa-bbbb-cccc-ddeeff001122"
	dev := fixtureDevDir(t, uuid)
	// The fixture table names /dev nodes; point it at the fixture tree.
	sfdisk = []byte(strings.ReplaceAll(string(sfdisk), "/dev/", dev+"/"))
	text, err := assembleDryRun(
		"BOOT_IMAGE=vmlinuz root=UUID="+uuid+" ro quiet",
		dev, fixtureSysRoot(t, true),
		sfdisk, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### 01-copy.conf",
		"### 03-copy.conf",
		"CopyBlocks=/dev/disk/by-partuuid/11111111-2222-3333-4444-555555555555",
		"SizeMinBytes=2147483648",
		"Encrypt=key-file+tpm2",
		"TPM2PCRs=7",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("dry-run output misses %q:\n%s", want, text)
		}
	}
}

func TestAssembleDryRunNoTPMOverrides(t *testing.T) {
	sfdisk, err := os.ReadFile(filepath.Join("..", "..", "internal", "repart", "testdata", "sfdisk-grub.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Explicit disk and root node skip cmdline detection entirely.
	text, err := assembleDryRun("", "/nonexistent-dev", fixtureSysRoot(t, false),
		sfdisk, "/dev/vda", "/dev/vda3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Encrypt=key-file\n") {
		t.Errorf("no-TPM dry-run wants plain Encrypt=key-file:\n%s", text)
	}
	if strings.Contains(text, "tpm2") {
		t.Errorf("no-TPM dry-run must not mention tpm2:\n%s", text)
	}
}

func TestDiskOf(t *testing.T) {
	for node, want := range map[string]string{
		"/dev/vda3":      "/dev/vda",
		"/dev/sda1":      "/dev/sda",
		"/dev/nvme0n1p2": "/dev/nvme0n1",
		"/dev/vda":       "/dev/vda",
	} {
		if got := diskOf(node); got != want {
			t.Errorf("diskOf(%q) = %q, want %q", node, got, want)
		}
	}
}
