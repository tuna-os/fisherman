package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// newProber is the production Prober; tests replace it with one pointed at a
// fixture root and canned command output.
var newProber = func() *probe.Prober {
	return &probe.Prober{Root: "/", Run: probeRun, Getenv: os.Getenv, Sandboxed: runner.InFlatpak, Euid: os.Geteuid}
}

// Limits on every command the probe runs. A probe answers in seconds or
// not at all; a hung or chatty host tool must not hang or flood a frontend.
const (
	probeCommandTimeout = 20 * time.Second
	probeMaxOutput      = 8 << 20
)

func probeRun(name string, args ...string) ([]byte, error) {
	return runner.BoundedOutput(probeCommandTimeout, probeMaxOutput, name, args...)
}

const probeUsage = `usage: fisherman probe --json [--source <ref> | --live-root] [--target-imgref <ref>] [--bootc-image-list]

Print the disks, TPM, RAM/CPU/UEFI, live-media, offline-store, host bootc,
GPU and machine-model facts an installer frontend needs, as one JSON object
on stdout. Read-only; needs no root. Schema: docs/PROBE.md.

  --source <ref>         the recipe's image; scopes "capabilities" to it
  --live-root            the recipe omits image (the live root is the source)
  --target-imgref <ref>  the recipe's targetImgref, the update reference
  --bootc-image-list     also run 'bootc image list' for "bootc_store"; only
                         as root, on an installed ostree host. Not read-only:
                         bootc opens its own store to answer (docs/PROBE.md)

The probe never pulls, mounts, starts a container or contacts a registry.
Without --bootc-image-list it writes nothing.
`

// runProbe implements `fisherman probe --json` and returns the exit code.
func runProbe(args []string, stdout, stderr io.Writer) int {
	jsonOut := false
	var sel probe.Selection
	listImages := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, value, hasValue := strings.Cut(a, "=")
		switch flag {
		case "--source", "--target-imgref":
			if !hasValue {
				if i+1 >= len(args) {
					fmt.Fprintf(stderr, "probe: %s needs a value\n\n%s", flag, probeUsage)
					return 2
				}
				i++
				value = args[i]
			}
			// The value is not echoed: it came from the caller.
			if err := probe.ValidateReference(value); err != nil {
				fmt.Fprintf(stderr, "probe: %s: %v\n", flag, err)
				return 2
			}
			if flag == "--source" {
				sel.Source = value
			} else {
				sel.TargetImgref = value
			}
			continue
		}
		switch a {
		case "--json":
			jsonOut = true
		case "--live-root":
			sel.LiveRoot = true
		case "--bootc-image-list":
			listImages = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, probeUsage)
			return 0
		default:
			fmt.Fprintf(stderr, "probe: unknown argument %q\n\n%s", a, probeUsage)
			return 2
		}
	}
	if !jsonOut {
		fmt.Fprintf(stderr, "probe: --json is required (it is the only output format)\n\n%s", probeUsage)
		return 2
	}
	if sel.LiveRoot && sel.Source != "" {
		fmt.Fprintf(stderr, "probe: --source and --live-root exclude each other\n\n%s", probeUsage)
		return 2
	}

	p := newProber()
	p.Selection = sel
	p.ListBootcImages = listImages
	res, err := p.Probe()
	if err != nil {
		fmt.Fprintf(stderr, "probe: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		fmt.Fprintf(stderr, "probe: encoding output: %v\n", err)
		return 1
	}
	return 0
}

// establishSourceRoot asks the probe whether the host's root is the
// execution root of opts.SourceImgref (tuna-os/fisherman#363). It runs only
// when the install has that choice to make (install.NeedsSourceRoot), and
// only the read-only calls of probe.SourceExecutionRoot. stores are the
// recipe's or the discovered image stores.
func establishSourceRoot(opts install.Options, stores []string) install.SourceRoot {
	if !install.NeedsSourceRoot(opts) {
		return install.SourceRoot{}
	}
	p := newProber()
	p.Selection = probe.Selection{Source: opts.SourceImgref, TargetImgref: opts.TargetImgref}
	r := p.SourceExecutionRoot(stores)
	return install.SourceRoot{Established: r.Established, Reason: r.Reason, Backend: r.Backend}
}
