package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tuna-os/fisherman/internal/recipe"
)

func runValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: fisherman validate <recipe.json>\n\n")
		fmt.Fprintf(os.Stderr, "Validate a recipe file without running an installation.\n")
		fmt.Fprintf(os.Stderr, "Exits 0 if valid, 1 if invalid.\n")
	}
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	path := fs.Arg(0)
	r, err := recipe.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%serror:%s loading %s: %v\n", "\033[31m", "\033[0m", path, err)
		os.Exit(1)
	}
	if err := r.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%serror:%s invalid recipe: %v\n", "\033[31m", "\033[0m", err)
		os.Exit(1)
	}

	fmt.Printf("%s✓%s %s is valid\n", "\033[32m", "\033[0m", path)
	fmt.Printf("  disk:       %s\n", r.Disk)
	fmt.Printf("  image:      %s\n", r.Image)
	fmt.Printf("  filesystem: %s\n", r.Filesystem)
	fmt.Printf("  encryption: %s\n", r.Encryption.Type)
	fmt.Printf("  hostname:   %s\n", r.Hostname)
	if r.User.Username != "" {
		fmt.Printf("  user:       %s\n", r.User.Username)
	}
	if r.Bootloader != "" {
		fmt.Printf("  bootloader: %s\n", r.Bootloader)
	}
}
