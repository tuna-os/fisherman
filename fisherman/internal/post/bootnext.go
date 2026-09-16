package post

import (
	"fmt"
	"strings"
)

// FindBootNextID finds the EFI boot entry number (e.g. "0001") for the
// given EFI partition by matching its PARTUUID against the verbose
// efibootmgr output. Returns an empty string (and no error) when the
// entry cannot be found, so the caller can degrade gracefully.
func FindBootNextID(efiPart string) (string, error) {
	// Get the PARTUUID of the EFI partition we just formatted.
	partuuidRaw, err := Exec.Command("blkid", "-s", "PARTUUID", "-o", "value", efiPart).Output()
	if err != nil {
		return "", fmt.Errorf("blkid %s: %w", efiPart, err)
	}
	if strings.TrimSpace(string(partuuidRaw)) == "" {
		return "", fmt.Errorf("blkid %s: no PARTUUID found (partition may not be formatted yet)", efiPart)
	}
	// Normalise to lowercase without dashes so we can match regardless of
	// how efibootmgr formats the UUID in the HD() descriptor.
	partuuid := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(string(partuuidRaw)), "-", ""))

	efivarsRaw, err := Exec.Command("efibootmgr", "-v").Output()
	if err != nil {
		return "", fmt.Errorf("efibootmgr -v: %w", err)
	}

	for _, line := range strings.Split(string(efivarsRaw), "\n") {
		// Only inspect "Boot####*" active-entry lines.
		if !strings.HasPrefix(line, "Boot") || len(line) < 9 || line[8] != '*' {
			continue
		}
		lineNorm := strings.ToLower(strings.ReplaceAll(line, "-", ""))
		if strings.Contains(lineNorm, partuuid) {
			// Characters 4–8 are the 4-digit hex boot number.
			return line[4:8], nil
		}
	}
	return "", nil
}
