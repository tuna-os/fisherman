package workflows

import (
	"fmt"
	"os"

	"github.com/tuna-os/fisherman/internal/slurp"
)

// ScanWorkflow handles the "fisherman scan <disk>" subcommand.
// This is a thin wrapper that encapsulates the scan operation.
func ScanWorkflow(disk string) error {
	output, err := slurp.ScanJSON(disk)
	if err != nil {
		return fmt.Errorf("scan: %v", err)
	}
	fmt.Println(output)
	return nil
}

// PrintHelp outputs the command help text.
// This is extracted to its own function to keep main() clean.
func PrintHelp() {
	help := `fisherman — bootable image installer for immutable Linux

Usage:
  fisherman [GLOBAL_OPTIONS] <recipe-file>    # Install using recipe
  fisherman help                               # Show this help
  fisherman version                            # Show version
  fisherman images                             # List available images
  fisherman validate <recipe-file>             # Validate recipe without installing
  fisherman scan <disk>                        # Scan disk layout

Global options:
  None currently defined; recipe files control all installation parameters.

Examples:
  fisherman /etc/fisherman/recipes/fedora-workstation.yml
  fisherman validate /tmp/custom-recipe.yml
  fisherman scan /dev/sda
`
	fmt.Print(help)
}

// CheckIsSubcommand returns true if the argument looks like a subcommand
// but doesn't match any known command. Used to provide better error messages.
func CheckIsSubcommand(arg string) bool {
	// If it starts with a dash or contains a slash, it's probably a flag or path
	if arg[0] == '-' || arg[0] == '/' {
		return false
	}
	// If it contains a dot or extension, it's probably a file path
	if arg[len(arg)-1] == 'l' && len(arg) > 4 && arg[len(arg)-4:] == ".yml" {
		return false
	}
	if arg[len(arg)-1] == 'l' && len(arg) > 5 && arg[len(arg)-5:] == ".yaml" {
		return false
	}
	return true
}
