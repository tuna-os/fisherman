// Package probe reports the read-only hardware and environment facts every
// installer frontend needs before it can show its first page: which disks
// may be installed to, whether a TPM 2.0 is usable, whether the machine meets
// the minimum requirements, whether it booted from live media, and which
// offline image stores ship on that media.
//
// Each frontend used to compute these itself, and the copies drifted. The
// output of Probe is the single answer; `fisherman probe --json` prints it.
// See docs/PROBE.md for the schema.
//
// Probe never needs root and never writes. Every read goes through a Prober
// field (a filesystem root and a command runner) so the tests can run it
// against fixture trees and canned command output.
package probe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProtocolVersion is the schema version of Result. Bump it on any change a
// frontend could notice: a removed or renamed field, or a changed meaning.
// Adding a field does not bump it.
const ProtocolVersion = 1

// Result is the whole probe answer, serialised as one JSON object.
type Result struct {
	ProtocolVersion int     `json:"protocol_version"`
	Disks           []Disk  `json:"disks"`
	TPM             TPM     `json:"tpm"`
	System          System  `json:"system"`
	Live            Live    `json:"live"`
	Offline         Offline `json:"offline"`
}

// Runner runs a command on the host and returns its stdout. In production it
// is runner.Output, which wraps the command in `flatpak-spawn --host` inside a
// Flatpak sandbox.
type Runner func(name string, args ...string) ([]byte, error)

// Prober holds the seams through which every OS read goes.
type Prober struct {
	// Root is the filesystem root the probe reads /proc, /sys, /dev, /run
	// and /etc from. "/" in production; a fixture directory in tests.
	Root string
	// Run runs a host command (lsblk, bootc, test, cat).
	Run Runner
	// Getenv reads the probe's own environment.
	Getenv func(string) string
	// Sandboxed reports whether the probe runs inside a Flatpak sandbox.
	// runner.InFlatpak in production: internal/runner owns the one sandbox
	// detector, and its Output already forwards Run to the host there.
	Sandboxed func() bool
}

// Probe gathers every section. Only a failure that leaves a section
// meaningless (lsblk failing, /proc/meminfo unreadable) is an error; absent
// optional facts (no TPM, no offline store) are ordinary answers.
func (p *Prober) Probe() (*Result, error) {
	disks, err := p.Disks()
	if err != nil {
		return nil, fmt.Errorf("disks: %w", err)
	}
	sys, err := p.System()
	if err != nil {
		return nil, fmt.Errorf("system: %w", err)
	}
	return &Result{
		ProtocolVersion: ProtocolVersion,
		Disks:           disks,
		TPM:             p.TPM(),
		System:          sys,
		Live:            p.Live(),
		Offline:         p.Offline(),
	}, nil
}

// local maps an absolute path to the probe's root.
func (p *Prober) local(path string) string {
	return filepath.Join(p.root(), path)
}

func (p *Prober) root() string {
	if p.Root == "" {
		return "/"
	}
	return p.Root
}

func (p *Prober) getenv(k string) string {
	if p.Getenv == nil {
		return os.Getenv(k)
	}
	return p.Getenv(k)
}

func (p *Prober) run(name string, args ...string) ([]byte, error) {
	if p.Run == nil {
		return nil, errors.New("no command runner")
	}
	return p.Run(name, args...)
}

func (p *Prober) inFlatpak() bool {
	return p.Sandboxed != nil && p.Sandboxed()
}

// hostVisible maps a host path to where the sandbox can see it. Inside a
// Flatpak, /etc and /usr are the runtime's; the host's are under /run/host
// (the same prefix bootc-installer's recipe.py uses). Everything else is
// read at its own path.
func (p *Prober) hostVisible(path string) string {
	if p.inFlatpak() && (hasPathPrefix(path, "/etc") || hasPathPrefix(path, "/usr")) {
		return p.local("/run/host" + path)
	}
	return p.local(path)
}

func hasPathPrefix(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// hostTest answers `test <flag> path` about a HOST path. Outside a sandbox it
// is a stat; inside one, a path the sandbox cannot see (the host's /run, /var)
// is asked of the host through the runner.
func (p *Prober) hostTest(flag, path string) bool {
	if fi, err := os.Stat(p.hostVisible(path)); err == nil {
		return flag != "-d" || fi.IsDir()
	}
	if !p.inFlatpak() {
		return false
	}
	_, err := p.run("test", flag, path)
	return err == nil
}

// hostReadFile reads a HOST file, through the runner when the sandbox cannot
// see it.
func (p *Prober) hostReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(p.hostVisible(path))
	if err == nil || !p.inFlatpak() {
		return b, err
	}
	return p.run("cat", path)
}

// readKernelFile reads a /proc or /sys file. The kernel's view is the same
// inside and outside a sandbox, so it is read directly.
func (p *Prober) readKernelFile(path string) (string, error) {
	b, err := os.ReadFile(p.local(path))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
