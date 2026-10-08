package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// newProber is the production Prober; tests replace it with one pointed at a
// fixture root and canned command output.
var newProber = func() *probe.Prober {
	return &probe.Prober{Root: "/", Run: runner.Output, Getenv: os.Getenv, Sandboxed: runner.InFlatpak}
}

const probeUsage = `usage: fisherman probe --json

Print the disks, TPM, RAM/CPU/UEFI, live-media and offline-store facts an
installer frontend needs, as one JSON object on stdout. Read-only; needs no
root. Schema: docs/PROBE.md.
`

// runProbe implements `fisherman probe --json` and returns the exit code.
func runProbe(args []string, stdout, stderr io.Writer) int {
	jsonOut := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
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

	res, err := newProber().Probe()
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
