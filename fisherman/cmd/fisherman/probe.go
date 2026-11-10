package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// newProber is the production Prober; tests replace it with one pointed at a
// fixture root and canned command output.
var newProber = func() *probe.Prober {
	return &probe.Prober{Root: "/", Run: probeRun, Getenv: os.Getenv, Sandboxed: runner.InFlatpak}
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

const probeUsage = `usage: fisherman probe --json [--source <ref> | --live-root] [--target-imgref <ref>]

Print the disks, TPM, RAM/CPU/UEFI, live-media, offline-store, host bootc,
GPU and machine-model facts an installer frontend needs, as one JSON object
on stdout. Read-only; needs no root. Schema: docs/PROBE.md.

  --source <ref>         the recipe's image; scopes "capabilities" to it
  --live-root            the recipe omits image (the live root is the source)
  --target-imgref <ref>  the recipe's targetImgref, the update reference

The probe never pulls, mounts, starts a container or contacts a registry.
`

// runProbe implements `fisherman probe --json` and returns the exit code.
func runProbe(args []string, stdout, stderr io.Writer) int {
	jsonOut := false
	var sel probe.Selection
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
