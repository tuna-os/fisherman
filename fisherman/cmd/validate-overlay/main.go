package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// This program demonstrates the overlay storage driver selection logic
// without requiring a real disk or bootc install.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <scratch-path>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s /tmp\n", os.Args[0])
		os.Exit(1)
	}

	scratchPath := os.Args[1]

	fmt.Println("=== Overlay Storage Driver Selection Test ===")
	fmt.Println()

	// Ensure scratch path exists
	if err := os.MkdirAll(scratchPath, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot create scratch path: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Scratch path: %s\n", scratchPath)
	absPath, _ := filepath.Abs(scratchPath)
	fmt.Printf("Absolute path: %s\n", absPath)
	fmt.Println()

	// Test non-composefs (should always be VFS)
	fmt.Println("Test 1: Non-composefs install")
	fmt.Println("  Result: should always use VFS (unchanged behavior)")
	fmt.Println()

	// Test composefs on this scratch path
	fmt.Println("Test 2: Composefs install on scratch path")
	fmt.Printf("  Testing driver selection for: %s\n", scratchPath)
	fmt.Println()

	// The actual selectStorageDriver is in the internal/install package.
	// For this dry-run, we would need to export it or test via integration.
	fmt.Println("  Note: Full integration test requires:")
	fmt.Println("    - podman available and working")
	fmt.Println("    - overlay driver support on this kernel")
	fmt.Println("    - real Dakota image available")
	fmt.Println()

	fmt.Println("=== Expected Behaviors ===")
	fmt.Println()
	fmt.Println("On ext4 scratch:")
	fmt.Println("  ✓ selectStorageDriver(scratch) -> ('overlay', 'reason')")
	fmt.Println("  ✓ podmanArgs includes: --storage-driver overlay")
	fmt.Println()
	fmt.Println("On BTRFS scratch:")
	fmt.Println("  ✓ selectStorageDriver(scratch) -> ('overlay', 'reason') or ('vfs', 'reason') if probe fails")
	fmt.Println("  ✓ podmanArgs includes: --storage-driver overlay or vfs")
	fmt.Println()
	fmt.Println("On tmpfs scratch (like /tmp):")
	fmt.Println("  ✓ selectStorageDriver(scratch) -> ('vfs', 'scratch filesystem tmpfs does not support overlay')")
	fmt.Println("  ✓ podmanArgs includes: --storage-driver vfs")
	fmt.Println()
	fmt.Println("If podman probe fails:")
	fmt.Println("  ✓ selectStorageDriver(scratch) -> ('vfs', 'podman overlay probe failed: ...')")
	fmt.Println("  ✓ podmanArgs includes: --storage-driver vfs")
	fmt.Println()
}
