package repart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	espType  = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
	bootType = "BC13C2FF-59E6-4262-A352-B275FD6F7172"
	rootType = "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709"
)

const gib = 1024 * 1024 * 1024

// grubMedium is the shared-medium layout for GRUB images: ESP, ext4 /boot,
// root. Sizes mirror the fleet rule: 2 GiB ESP.
func grubMedium() Table {
	return Table{SectorSize: 512, Partitions: []Partition{
		{Index: 1, Type: espType, PartUUID: "11111111-2222-3333-4444-555555555555", Name: "ESP", SizeBytes: 2 * gib, Node: "/dev/vda1"},
		{Index: 2, Type: bootType, PartUUID: "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", Name: "boot", SizeBytes: 1 * gib, Node: "/dev/vda2"},
		{Index: 3, Type: rootType, PartUUID: "FFFFFFFF-0000-1111-2222-333333333333", Name: "root", SizeBytes: 20 * gib, Node: "/dev/vda3"},
	}}
}

// dakotaMedium is the systemd-boot layout: ESP and root only, the kernel
// and initrd live directly on the ESP.
func dakotaMedium() Table {
	return Table{SectorSize: 512, Partitions: []Partition{
		{Index: 1, Type: espType, PartUUID: "11111111-2222-3333-4444-555555555555", Name: "ESP", SizeBytes: 2 * gib, Node: "/dev/vda1"},
		{Index: 2, Type: rootType, PartUUID: "FFFFFFFF-0000-1111-2222-333333333333", Name: "root", SizeBytes: 20 * gib, Node: "/dev/vda2"},
	}}
}

func TestGenerateTPM2(t *testing.T) {
	defs, err := Generate(grubMedium(), "/dev/vda3", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 3 {
		t.Fatalf("got %d definitions, want 3", len(defs))
	}
	if defs[0].FileName != "01-copy.conf" || defs[2].FileName != "03-copy.conf" {
		t.Fatalf("file names keep the medium index: %+v", defs)
	}
	esp := defs[0].Text
	for _, want := range []string{
		"Type=" + strings.ToLower(espType),
		"CopyBlocks=/dev/disk/by-partuuid/11111111-2222-3333-4444-555555555555",
		"SizeMinBytes=2147483648",
		"SizeMaxBytes=2147483648",
	} {
		if !strings.Contains(esp, want) {
			t.Errorf("ESP definition misses %q:\n%s", want, esp)
		}
	}
	if strings.Contains(esp, "Encrypt=") {
		t.Errorf("ESP must not be encrypted:\n%s", esp)
	}
	root := defs[2].Text
	for _, want := range []string{"Encrypt=key-file+tpm2", "TPM2PCRs=7"} {
		if !strings.Contains(root, want) {
			t.Errorf("TPM2 root misses %q:\n%s", want, root)
		}
	}
	if strings.Contains(root, "SizeMinBytes=") {
		t.Errorf("root takes the rest of the disk, no fixed size:\n%s", root)
	}
}

func TestGenerateNoTPM(t *testing.T) {
	defs, err := Generate(grubMedium(), "/dev/vda3", false)
	if err != nil {
		t.Fatal(err)
	}
	root := defs[2].Text
	if !strings.Contains(root, "Encrypt=key-file\n") && !strings.HasSuffix(strings.TrimSpace(root), "Encrypt=key-file") {
		t.Errorf("no-TPM root wants plain Encrypt=key-file:\n%s", root)
	}
	if strings.Contains(root, "tpm2") || strings.Contains(root, "TPM2") {
		t.Errorf("no-TPM root must not mention tpm2:\n%s", root)
	}
}

func TestGenerateDakotaTwoPartitions(t *testing.T) {
	defs, err := Generate(dakotaMedium(), "/dev/vda2", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 {
		t.Fatalf("got %d definitions, want 2", len(defs))
	}
	if !strings.Contains(defs[1].Text, "Encrypt=key-file+tpm2") {
		t.Errorf("dakota root is LUKS2 too:\n%s", defs[1].Text)
	}
}

func TestGenerateSkipsBIOS(t *testing.T) {
	tab := grubMedium()
	tab.Partitions = append([]Partition{
		{Index: 1, Type: BIOSType, PartUUID: "99999999-9999-9999-9999-999999999999", SizeBytes: 1 << 20, Node: "/dev/vda0"},
	}, tab.Partitions...)
	for i := range tab.Partitions {
		tab.Partitions[i].Index = i + 1
	}
	defs, err := Generate(tab, "/dev/vda3", true)
	if err != nil {
		t.Fatal(err)
	}
	// The BIOS entry leaves a gap: 02, 03, 04 like the Utah script.
	if len(defs) != 3 || defs[0].FileName != "02-copy.conf" {
		t.Fatalf("BIOS skipped with index gap, got %+v", defs)
	}
	for _, d := range defs {
		if strings.Contains(strings.ToLower(d.Text), strings.ToLower(BIOSType)) {
			t.Errorf("BIOS type leaked into %s:\n%s", d.FileName, d.Text)
		}
	}
}

func TestGenerateErrors(t *testing.T) {
	if _, err := Generate(Table{}, "/dev/vda3", true); err == nil {
		t.Error("empty table must fail")
	}
	if _, err := Generate(grubMedium(), "/dev/vda9", true); err == nil {
		t.Error("unknown root node must fail")
	}
	biosOnly := Table{Partitions: []Partition{
		{Index: 1, Type: BIOSType, PartUUID: "99999999-9999-9999-9999-999999999999", SizeBytes: 1 << 20, Node: "/dev/vda0"},
	}}
	if _, err := Generate(biosOnly, "/dev/vda0", true); err == nil {
		t.Error("BIOS-only table must fail")
	}
}

func TestParseSfdisk(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "sfdisk-grub.json"))
	if err != nil {
		t.Fatal(err)
	}
	tab, err := ParseSfdisk(raw, "/dev/vda")
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Partitions) != 3 {
		t.Fatalf("got %d partitions, want 3", len(tab.Partitions))
	}
	if tab.Partitions[0].SizeBytes != 2*gib {
		t.Errorf("ESP size = %d, want %d", tab.Partitions[0].SizeBytes, 2*gib)
	}
	defs, err := Generate(tab, "/dev/vda3", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 3 {
		t.Fatalf("got %d definitions, want 3", len(defs))
	}
	if _, err := ParseSfdisk([]byte("{bad"), "/dev/vda"); err == nil {
		t.Error("bad JSON must fail")
	}
	if _, err := ParseSfdisk([]byte(`{"partitiontable":{"partitions":[]}}`), "/dev/vda"); err == nil {
		t.Error("empty partitions must fail")
	}
}

func TestRootFSUUID(t *testing.T) {
	uuid, err := RootFSUUID("BOOT_IMAGE=vmlinuz root=UUID=9f3a2c1d-aaaa-bbbb-cccc-ddeeff001122 ro quiet")
	if err != nil || uuid != "9f3a2c1d-aaaa-bbbb-cccc-ddeeff001122" {
		t.Errorf("got %q, %v", uuid, err)
	}
	if _, err := RootFSUUID("BOOT_IMAGE=vmlinuz ro quiet"); err == nil {
		t.Error("missing root=UUID= must fail")
	}
}

func TestRootNodeByFSUUID(t *testing.T) {
	dir := t.TempDir()
	uuid := "9f3a2c1d-aaaa-bbbb-cccc-ddeeff001122"
	if err := os.MkdirAll(filepath.Join(dir, "disk/by-uuid"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The target must exist: EvalSymlinks resolves it fully.
	if err := os.WriteFile(filepath.Join(dir, "vda3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../vda3", filepath.Join(dir, "disk/by-uuid", uuid)); err != nil {
		t.Fatal(err)
	}
	node, err := RootNodeByFSUUID(dir, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(node, "vda3") {
		t.Errorf("got %q, want a vda3 node", node)
	}
	if _, err := RootNodeByFSUUID(dir, ""); err == nil {
		t.Error("empty UUID must fail")
	}
	if _, err := RootNodeByFSUUID(dir, "no-such-uuid"); err == nil {
		t.Error("missing symlink must fail")
	}
}

func TestHasTPM2(t *testing.T) {
	mkSys := func(t *testing.T, content *string) string {
		t.Helper()
		dir := t.TempDir()
		if content == nil {
			return dir
		}
		if err := os.MkdirAll(filepath.Join(dir, "class/tpm/tpm0"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "class/tpm/tpm0/tpm_version_major"), []byte(*content), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	two, one := "2\n", "1\n"
	if !HasTPM2(mkSys(t, &two)) {
		t.Error("tpm_version_major 2 means TPM2")
	}
	if HasTPM2(mkSys(t, &one)) {
		t.Error("version 1 is not TPM2")
	}
	if HasTPM2(mkSys(t, nil)) {
		t.Error("missing sysfs file means no TPM2")
	}
}
