// Package backend is the TUI's whole contact surface with fisherman: where
// the binary is, the disks it reports, its verdict on recipe fields, and the
// install command. The TUI holds no copy of fisherman's rules.
package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// candidates are the absolute paths tried when fisherman is not on PATH.
// There is no relative candidate: the TUI runs as root, and "./fisherman"
// would run whatever file the current directory holds (#417).
var candidates = []string{"/usr/bin/fisherman", "/usr/local/bin/fisherman"}

// ErrNotFound is returned when no fisherman binary is installed.
var ErrNotFound = errors.New("fisherman is not installed (looked on PATH, in /usr/bin and in /usr/local/bin)")

// Find returns the absolute path of the fisherman binary.
func Find() (string, error) {
	if path, err := exec.LookPath("fisherman"); err == nil {
		// LookPath refuses a match in the current directory (ErrDot), and
		// a relative PATH entry is made absolute here, so the path that is
		// run is the path that was found.
		if abs, err := filepath.Abs(path); err == nil {
			return abs, nil
		}
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return c, nil
		}
	}
	return "", ErrNotFound
}

// Disk is one entry of `fisherman probe --json`'s `disks` (docs/PROBE.md).
type Disk struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	SizeLabel      string `json:"size_label"`
	Model          string `json:"model"`
	Vendor         string `json:"vendor"`
	TransportLabel string `json:"transport_label"`
	Removable      bool   `json:"removable"`
	Eligible       bool   `json:"eligible"`
	ExcludedReason string `json:"excluded_reason"`
}

// Label is the one-line description of a disk for a list.
func (d Disk) Label() string {
	parts := []string{d.Path, d.SizeLabel}
	if name := strings.TrimSpace(d.Vendor + " " + d.Model); name != "" {
		parts = append(parts, name)
	}
	if d.TransportLabel != "" {
		parts = append(parts, d.TransportLabel)
	}
	return strings.Join(parts, "  ")
}

// Probe runs `fisherman probe --json` and returns every disk it reports. The
// caller offers the eligible ones (Eligible) and may show the others with
// their ExcludedReason. An error means there is no disk list; the caller
// must not invent one.
func Probe() ([]Disk, error) {
	bin, err := Find()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd := exec.Command(bin, "probe", "--json")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("fisherman probe --json: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseProbe(out)
}

// ParseProbe reads the output of `fisherman probe --json`.
func ParseProbe(out []byte) ([]Disk, error) {
	var res struct {
		Disks []Disk `json:"disks"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("reading fisherman probe output: %w", err)
	}
	return res.Disks, nil
}

// Eligible returns the disks fisherman accepts as an install target.
func Eligible(all []Disk) []Disk {
	var out []Disk
	for _, d := range all {
		if d.Eligible {
			out = append(out, d)
		}
	}
	return out
}

// Problem is one entry of `fisherman validate --json`'s `errors`
// (docs/VALIDATE.md).
type Problem struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidatePartial sends recipe fields (JSON, in the recipe's own shape) to
// `fisherman validate --json --partial -` on stdin and returns the problems.
// Stdin keeps a passphrase off the command line and out of any file. An
// error means there is no verdict (fisherman is missing, or its answer
// cannot be read); it does not mean the fields are valid.
func ValidatePartial(fields []byte) ([]Problem, error) {
	bin, err := Find()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "validate", "--json", "--partial", "-")
	cmd.Stdin = bytes.NewReader(fields)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	// Exit 0 is valid and exit 1 is invalid; both print the JSON result.
	var res struct {
		Valid  *bool     `json:"valid"`
		Errors []Problem `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil || res.Valid == nil {
		return nil, fmt.Errorf("fisherman validate gave no verdict (%v): %s", runErr, strings.TrimSpace(stderr.String()))
	}
	if !*res.Valid && len(res.Errors) == 0 {
		return nil, errors.New("fisherman validate reported an invalid recipe with no problem")
	}
	return res.Errors, nil
}

// InstallCommand returns the command that installs recipe: `fisherman -`
// with the recipe on stdin. The recipe holds the LUKS passphrase and the
// user's password, so it is never written to a file and never put in argv.
// Stdin ends after the recipe, which is how fisherman knows it is complete.
func InstallCommand(recipe []byte) (*exec.Cmd, error) {
	bin, err := Find()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, InstallArgs()...)
	cmd.Stdin = bytes.NewReader(recipe)
	return cmd, nil
}

// InstallArgs is the backend's install contract: one argument, "-", for a
// recipe on stdin. There is no `install` subcommand and no `--recipe` flag
// (#178).
func InstallArgs() []string { return []string{"-"} }
