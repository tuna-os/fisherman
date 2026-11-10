package main

import "path/filepath"

// resolveDevice follows symlinks (/dev/disk/by-id/…, /dev/mapper/…) to the
// kernel's device node; a path that cannot be resolved is returned as given.
func resolveDevice(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
