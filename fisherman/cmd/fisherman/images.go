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
		fmt.Fprintf(os.Stderr, "usage: fisherman images [--file <path>] [--plain]\n\n")
		fmt.Fprintf(os.Stderr, "Print the available image catalog.\n\n")
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
		// Align imgref column at 50 chars from the start of the name
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
