// Package repart generates the systemd-repart definitions for the
// shared-medium install layout. It is a Go port of Utah's
// iso/raw/src/usr/libexec/utah-live-definitions (projectbluefin/utah,
// branch feat/gnome-setup-installer): the live medium's GPT is mirrored
// partition by partition with CopyBlocks=, named by PARTUUID, and the root
// goes into LUKS2.
//
// Every function here is pure and root-free: the callers hand in a
// partition table (parsed from sfdisk --json) and get definition texts back.
// Table tests cover the fixture layouts; no disk is ever touched.
package repart

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BIOSType is the BIOS boot partition type. The target is UEFI-only, so a
// medium BIOS partition is never mirrored.
const BIOSType = "21686148-6449-6e6f-744e-656564454649"

// TPM2PCRs is the PCR set the root seals to. PCR 7 covers the Secure Boot
// policy: the same shim/GRUB chain boots the medium and the installed copy.
const TPM2PCRs = "7"

// Partition is one GPT entry of the medium.
type Partition struct {
	// Index is the 1-based position in the medium table. Definition file
	// names derive from it, so a skipped BIOS entry leaves a gap, exactly
	// like the Utah script.
	Index int
	// Type is the partition type UUID.
	Type string
	// PartUUID is the partition UUID (PARTUUID).
	PartUUID string
	// Name is the GPT label, empty when unset.
	Name string
	// SizeBytes is the partition size in bytes.
	SizeBytes int64
	// Node is the medium device node, for example /dev/vda2.
	Node string
}

// Table is the medium's GPT.
type Table struct {
	SectorSize int64
	Partitions []Partition
}

// Definition is one repart definition file: its name and its text.
type Definition struct {
	FileName string
	Text     string
}

// Generate mirrors the medium table into repart definitions. rootNode is
// the medium partition holding the booted root; it becomes the LUKS2 root.
// tpm2 selects Encrypt=key-file+tpm2 (PCR 7) or plain Encrypt=key-file.
// A nil key file is intended: repart enrolls an empty passphrase, which
// utah-sysinstall-post unlocks and gnome-setup's recovery key page replaces.
func Generate(t Table, rootNode string, tpm2 bool) ([]Definition, error) {
	if len(t.Partitions) == 0 {
		return nil, fmt.Errorf("repart: empty partition table")
	}
	root := -1
	for i, p := range t.Partitions {
		if p.Node == rootNode {
			root = i
			break
		}
	}
	if root < 0 {
		return nil, fmt.Errorf("repart: root node %q is not in the partition table", rootNode)
	}
	var out []Definition
	for _, p := range t.Partitions {
		if strings.EqualFold(p.Type, BIOSType) {
			continue
		}
		lines := []string{
			"[Partition]",
			"Type=" + strings.ToLower(p.Type),
			"CopyBlocks=/dev/disk/by-partuuid/" + strings.ToLower(p.PartUUID),
		}
		if p.Name != "" {
			lines = append(lines, "Label="+p.Name)
		}
		if p.Node == rootNode {
			if tpm2 {
				lines = append(lines, "Encrypt=key-file+tpm2", "TPM2PCRs="+TPM2PCRs)
			} else {
				lines = append(lines, "Encrypt=key-file")
			}
		} else {
			// ESP and /boot keep their size; the root takes the rest of
			// the disk (repart adds room for the LUKS2 header itself).
			lines = append(lines,
				fmt.Sprintf("SizeMinBytes=%d", p.SizeBytes),
				fmt.Sprintf("SizeMaxBytes=%d", p.SizeBytes))
		}
		out = append(out, Definition{
			FileName: fmt.Sprintf("%02d-copy.conf", p.Index),
			Text:     strings.Join(lines, "\n") + "\n",
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("repart: no partitions left after skipping BIOS boot")
	}
	return out, nil
}

// Write stores definitions in dir, creating it. It writes nothing else.
func Write(dir string, defs []Definition) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, d := range defs {
		if err := os.WriteFile(filepath.Join(dir, d.FileName), []byte(d.Text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// sfdiskJSON is the subset of sfdisk --json the generator reads. Sizes are
// in sectors; the table carries the sector size.
type sfdiskJSON struct {
	PartitionTable struct {
		SectorSize int64 `json:"sectorsize"`
		Partitions []struct {
			Node string `json:"node"`
			Type string `json:"type"`
			UUID string `json:"uuid"`
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"partitions"`
	} `json:"partitiontable"`
}

// ParseSfdisk parses sfdisk --json output for disk into a Table.
func ParseSfdisk(data []byte, disk string) (Table, error) {
	var raw sfdiskJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return Table{}, fmt.Errorf("repart: parsing sfdisk output for %s: %w", disk, err)
	}
	t := Table{SectorSize: raw.PartitionTable.SectorSize}
	if t.SectorSize == 0 {
		t.SectorSize = 512
	}
	for i, p := range raw.PartitionTable.Partitions {
		if p.Node == "" || p.Type == "" || p.UUID == "" {
			return Table{}, fmt.Errorf("repart: partition %d on %s misses node, type or uuid", i+1, disk)
		}
		t.Partitions = append(t.Partitions, Partition{
			Index:     i + 1,
			Type:      p.Type,
			PartUUID:  p.UUID,
			Name:      p.Name,
			SizeBytes: p.Size * t.SectorSize,
			Node:      p.Node,
		})
	}
	if len(t.Partitions) == 0 {
		return Table{}, fmt.Errorf("repart: no partitions on %s", disk)
	}
	return t, nil
}

// HasTPM2 reports whether sysRoot holds a TPM 2.0, the same check the Utah
// script makes: /sys/class/tpm/tpm0/tpm_version_major reads "2".
func HasTPM2(sysRoot string) bool {
	b, err := os.ReadFile(filepath.Join(sysRoot, "class/tpm/tpm0/tpm_version_major"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "2"
}

// RootNodeByFSUUID resolves the booted root the way the Utah script does:
// /dev/disk/by-uuid/<fsUUID> is a symlink to the medium partition node.
// devDir is /dev in production and a fixture directory in tests.
func RootNodeByFSUUID(devDir, fsUUID string) (string, error) {
	if fsUUID == "" {
		return "", fmt.Errorf("repart: empty filesystem UUID")
	}
	link := filepath.Join(devDir, "disk/by-uuid", fsUUID)
	node, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", fmt.Errorf("repart: resolving %s: %w", link, err)
	}
	return node, nil
}

// RootFSUUID scans kernelCmdline (one /proc/cmdline line) for root=UUID=
// and returns the value. bootc images always carry it; without it the
// medium cannot be identified.
func RootFSUUID(kernelCmdline string) (string, error) {
	for _, arg := range strings.Fields(kernelCmdline) {
		if v, ok := strings.CutPrefix(arg, "root=UUID="); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("repart: no root=UUID= on the kernel command line")
}
