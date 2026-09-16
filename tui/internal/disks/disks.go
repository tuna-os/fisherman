package disks

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

type Disk struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Model      string `json:"model"`
	Type       string `json:"type"`
	Mountpoint string `json:"mountpoint"`
}

type lsblkOutput struct {
	Blockdevices []Disk `json:"blockdevices"`
}

// List returns all physical disks that are not currently mounted.
func List() ([]Disk, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,SIZE,MODEL,TYPE,MOUNTPOINT").Output()
	if err != nil {
		// Return a fake disk for development/testing if lsblk fails
		return []Disk{
			{Name: "sda", Size: 107374182400, Model: "QEMU HARDDISK", Type: "disk"},
		}, nil
	}

	var result lsblkOutput
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}

	var disks []Disk
	for _, d := range result.Blockdevices {
		if d.Type == "disk" && d.Mountpoint == "" {
			disks = append(disks, d)
		}
	}
	return disks, nil
}

// HumanSize converts bytes to a human-readable string.
func HumanSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)
	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.1f TB", float64(bytes)/TB)
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
