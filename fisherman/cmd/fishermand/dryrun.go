//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/repart"
	"github.com/tuna-os/fisherman/internal/runner"
)

// dryRunFlags are the seams --dry-run reads through. Production defaults
// point at the live system; tests override them with fixture trees.
type dryRunFlags struct {
	mediumDisk string // override the auto-detected medium disk
	rootNode   string // override the auto-detected medium root partition
	cmdline    string // kernel cmdline file; default /proc/cmdline
	devDir     string // device directory; default /dev
	sysRoot    string // sysfs root; default /sys
	sfdiskJSON string // read sfdisk output from this file instead of running sfdisk
}

// assembleDryRun builds the definition text from already-gathered inputs.
// cmdline is one /proc/cmdline line, devDir holds disk/by-uuid, sysRoot
// holds class/tpm, and sfdiskOut is sfdisk --json output for disk.
func assembleDryRun(cmdline, devDir, sysRoot string, sfdiskOut []byte, disk, rootNode string) (string, error) {
	if rootNode == "" {
		uuid, err := repart.RootFSUUID(cmdline)
		if err != nil {
			return "", err
		}
		node, err := repart.RootNodeByFSUUID(devDir, uuid)
		if err != nil {
			return "", err
		}
		rootNode = node
	}
	if disk == "" {
		disk = diskOf(rootNode)
	}
	table, err := repart.ParseSfdisk(sfdiskOut, disk)
	if err != nil {
		return "", err
	}
	defs, err := repart.Generate(table, rootNode, repart.HasTPM2(sysRoot))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, d := range defs {
		fmt.Fprintf(&b, "### %s\n%s", d.FileName, d.Text)
	}
	return b.String(), nil
}

// diskOf maps a partition node to its whole disk (/dev/vda3 -> /dev/vda,
// /dev/nvme0n1p2 -> /dev/nvme0n1). Callers prefer lsblk; this is the
// fallback when only the node string is known.
func diskOf(node string) string {
	base := filepath.Base(node)
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] < '0' || base[i] > '9' {
			// NVMe-style pN suffix: strip the p too (nvme0n1p2 -> nvme0n1).
			if base[i] == 'p' && i > 0 && base[i-1] >= '0' && base[i-1] <= '9' {
				return filepath.Join(filepath.Dir(node), base[:i])
			}
			return filepath.Join(filepath.Dir(node), base[:i+1])
		}
	}
	return node
}

// runDryRun gathers the live inputs (or the fixture overrides) and prints
// the definitions to stdout. It changes nothing.
func runDryRun(stdout io.Writer, f dryRunFlags) error {
	cmdline := ""
	if f.cmdline != "" {
		b, err := os.ReadFile(f.cmdline)
		if err != nil {
			return err
		}
		cmdline = string(b)
	} else if b, err := os.ReadFile("/proc/cmdline"); err == nil {
		cmdline = string(b)
	}
	devDir, sysRoot := f.devDir, f.sysRoot
	if devDir == "" {
		devDir = "/dev"
	}
	if sysRoot == "" {
		sysRoot = "/sys"
	}
	disk, rootNode := f.mediumDisk, f.rootNode
	if rootNode == "" && disk == "" && cmdline != "" {
		// Auto-detect the medium disk from the booted root, like the
		// Utah script: root=UUID= -> node -> parent disk.
		if uuid, err := repart.RootFSUUID(cmdline); err == nil {
			if node, err := repart.RootNodeByFSUUID(devDir, uuid); err == nil {
				rootNode = node
				if out, err := runner.Output("lsblk", "-rno", "PKNAME", node); err == nil {
					if parent := strings.TrimSpace(string(out)); parent != "" {
						disk = "/dev/" + parent
					}
				}
			}
		}
	}
	if rootNode == "" && disk == "" {
		return fmt.Errorf("fishermand: cannot detect the medium disk; pass --medium-disk and --root-node")
	}
	var sfdiskOut []byte
	var err error
	if f.sfdiskJSON != "" {
		sfdiskOut, err = os.ReadFile(f.sfdiskJSON)
	} else {
		if disk == "" {
			disk = diskOf(rootNode)
		}
		sfdiskOut, err = runner.Output("sfdisk", "--json", disk)
	}
	if err != nil {
		return fmt.Errorf("fishermand: sfdisk for %s: %w", disk, err)
	}
	text, err := assembleDryRun(cmdline, devDir, sysRoot, sfdiskOut, disk, rootNode)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, text)
	return err
}
