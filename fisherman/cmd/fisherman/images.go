package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/tuna-os/fisherman/internal/images"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	cyan   = "\033[36m"
	yellow = "\033[33m"
	green  = "\033[32m"
)

func runImages(args []string) {
	fs := flag.NewFlagSet("images", flag.ExitOnError)
	file := fs.String("file", "", "path to images.json (default: auto-detect)")
	plain := fs.Bool("plain", false, "plain output without ANSI color or tree characters")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: fisherman images [--file <path>] [--plain] [<query>]\n\n")
		fmt.Fprintf(os.Stderr, "Without a query: print the full image catalog as a tree.\n")
		fmt.Fprintf(os.Stderr, "With a query: show details for matching images or families.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  fisherman images\n")
		fmt.Fprintf(os.Stderr, "  fisherman images TunaOS\n")
		fmt.Fprintf(os.Stderr, "  fisherman images yellowfin\n")
		fmt.Fprintf(os.Stderr, "  fisherman images \"GNOME 50\"\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	var catalog *images.Catalog
	var path string
	var err error

	if *file != "" {
		catalog, err = images.Load(*file)
		path = *file
	} else {
		catalog, path, err = images.LoadDefault()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fisherman images: %v\n", err)
		os.Exit(1)
	}

	query := strings.Join(fs.Args(), " ")

	if query != "" {
		results := catalog.Find(query)
		if len(results) == 0 {
			fmt.Fprintf(os.Stderr, "fisherman images: no images found matching %q\n", query)
			os.Exit(1)
		}
		for i, r := range results {
			if i > 0 {
				fmt.Println()
			}
			printNodeDetail(r, catalog.DefaultImage, *plain)
		}
		return
	}

	if !*plain {
		fmt.Printf("%s# %s%s\n\n", dim, path, reset)
	}

	for i, root := range catalog.Images {
		last := i == len(catalog.Images)-1
		printNode(root, "", last, catalog.DefaultImage, *plain)
		if !last {
			fmt.Println()
		}
	}
}

// printNodeDetail shows a rich detail view for a single matched node.
func printNodeDetail(r *images.NodeResult, defaultImg string, plain bool) {
	n := r.Node
	resolved := r.Resolve()

	if plain {
		if r.Breadcrumb() != n.Name {
			fmt.Printf("Path: %s\n", r.Breadcrumb())
		}
		fmt.Printf("Name: %s\n", n.Name)
		if n.Imgref != "" {
			marker := ""
			if n.Imgref == defaultImg {
				marker = " [default]"
			}
			fmt.Printf("Image: %s%s\n", n.Imgref, marker)
		}
		if resolved.Desc != "" {
			fmt.Printf("Description: %s\n", resolved.Desc)
		}
		if resolved.Subtitle != "" {
			fmt.Printf("Subtitle: %s\n", resolved.Subtitle)
		}
		if resolved.Bootloader != "" {
			fmt.Printf("Bootloader: %s\n", resolved.Bootloader)
		}
		if resolved.Filesystem != "" {
			fmt.Printf("Filesystem: %s\n", resolved.Filesystem)
		}
		if resolved.ComposeFsBackend {
			fmt.Printf("ComposFS: enabled\n")
		}
		if resolved.Flatpaks != "" {
			fmt.Printf("Flatpaks: %s\n", resolved.Flatpaks)
		}
		if resolved.NeedsUserCreation {
			fmt.Printf("Needs user creation: yes\n")
		} else {
			fmt.Printf("Needs user creation: no\n")
		}
		if resolved.SearchExtra != "" {
			fmt.Printf("Search keywords: %s\n", resolved.SearchExtra)
		}
		if len(n.Children) > 0 {
			fmt.Printf("Variants:\n")
			for _, child := range n.Children {
				printNode(child, "  ", true, defaultImg, true)
			}
		}
		return
	}

	// Coloured detail view
	if bc := r.Breadcrumb(); bc != n.Name {
		fmt.Printf("%s%s%s\n", dim, bc, reset)
	}
	fmt.Printf("%s%s%s", bold, n.Name, reset)
	if resolved.Subtitle != "" {
		fmt.Printf("  %s%s%s", dim, resolved.Subtitle, reset)
	}
	fmt.Println()

	if n.Imgref != "" {
		isDefault := n.Imgref == defaultImg
		fmt.Printf("  %simage:%s   %s%s%s", dim, reset, cyan, n.Imgref, reset)
		if isDefault {
			fmt.Printf("  %s★ default%s", yellow, reset)
		}
		fmt.Println()
	}
	if resolved.Desc != "" {
		fmt.Printf("  %sdesc:%s    %s\n", dim, reset, resolved.Desc)
	}
	if resolved.Bootloader != "" {
		fmt.Printf("  %sboot:%s    %s\n", dim, reset, resolved.Bootloader)
	} else {
		fmt.Printf("  %sboot:%s    %sgrub2 (default)%s\n", dim, reset, dim, reset)
	}
	if resolved.Filesystem != "" {
		fmt.Printf("  %sfs:%s      %s\n", dim, reset, resolved.Filesystem)
	} else {
		fmt.Printf("  %sfs:%s      %sxfs (default)%s\n", dim, reset, dim, reset)
	}
	if resolved.ComposeFsBackend {
		fmt.Printf("  %scomposefs:%s %senabled%s\n", dim, reset, green, reset)
	}
	if resolved.NeedsUserCreation {
		fmt.Printf("  %sneeds user creation:%s %syes%s\n", dim, reset, yellow, reset)
	} else {
		fmt.Printf("  %sneeds user creation:%s no\n", dim, reset)
	}
	if resolved.Flatpaks != "" {
		fmt.Printf("  %sflatpaks:%s %s\n", dim, reset, resolved.Flatpaks)
	}
	if resolved.SearchExtra != "" {
		fmt.Printf("  %skeywords:%s %s%s%s\n", dim, reset, dim, resolved.SearchExtra, reset)
	}

	if len(n.Children) > 0 {
		fmt.Printf("\n  %sVariants:%s\n", bold, reset)
		for i, child := range n.Children {
			last := i == len(n.Children)-1
			printNode(child, "  ", last, defaultImg, false)
		}
	}
}

func printNode(n *images.Node, prefix string, last bool, defaultImg string, plain bool) {
	if plain {
		if n.IsLeaf() {
			marker := ""
			if n.Imgref == defaultImg {
				marker = " *"
			}
			fmt.Printf("%s- %s  %s%s\n", prefix, n.Name, n.Imgref, marker)
		} else {
			fmt.Printf("%s%s\n", prefix, n.Name)
		}
		for _, child := range n.Children {
			printNode(child, prefix+"  ", false, defaultImg, plain)
		}
		return
	}

	connector := "├─ "
	childPrefix := prefix + "│  "
	if last {
		connector = "└─ "
		childPrefix = prefix + "   "
	}

	if n.IsLeaf() {
		marker := ""
		if n.Imgref == defaultImg {
			marker = yellow + " ★ default" + reset
		}
		namePart := n.Name
		padding := 46 - len(prefix) - len(connector) - len(namePart)
		if padding < 1 {
			padding = 1
		}
		fmt.Printf("%s%s%s%s%s%s%s%s\n",
			prefix, connector,
			cyan, namePart, reset,
			strings.Repeat(" ", padding),
			dim+n.Imgref+reset,
			marker,
		)
	} else {
		fmt.Printf("%s%s%s%s%s\n", prefix, connector, bold, n.Name, reset)
	}

	for i, child := range n.Children {
		childLast := i == len(n.Children)-1
		printNode(child, childPrefix, childLast, defaultImg, plain)
	}
}
