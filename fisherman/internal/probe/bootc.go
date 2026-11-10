package probe

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// Where a set of bootc facts was established. Only the live host is probed
// today. The selected source image and the installed target are different
// bootc builds and get their own context values when they are probed
// (tuna-os/fisherman#383); a frontend must not read live-host facts as facts
// about the image it is about to install.
const (
	BootcContextLiveHost = "live-host"
)

// Capability status values.
const (
	CapSupported   = "supported"
	CapUnsupported = "unsupported"
	CapUnknown     = "unknown"
)

// Capability reason codes. They are part of the contract: a frontend may
// branch on them, so a code is never renamed or reused.
const (
	// ReasonCommandPresent: `bootc <subcommand> --help` exited 0 in this
	// context. It proves the command exists, not that an install profile
	// that uses it is qualified.
	ReasonCommandPresent = "command_present"
	// ReasonCommandAbsent: bootc ran and rejected the subcommand.
	ReasonCommandAbsent = "command_absent"
	// ReasonBootcMissing: there is no bootc command in this context.
	ReasonBootcMissing = "bootc_missing"
	// ReasonBootcFailed: bootc exists but `bootc --version` failed.
	ReasonBootcFailed = "bootc_failed"
	// ReasonCheckFailed: the check for this one capability failed in a way
	// that says nothing about the capability.
	ReasonCheckFailed = "check_failed"
	// ReasonNotEstablished: no read-only check in this context can
	// establish the capability; it depends on the selected image.
	ReasonNotEstablished = "not_established"
)

// Version reason codes, set only when Version is empty.
const (
	VersionMalformed = "malformed_output"
)

// Capability names.
const (
	CapContainerInspect   = "container_inspect"
	CapPrintConfiguration = "install_print_configuration"
	CapInstallFinalize    = "install_finalize"
	CapDeploymentMount    = "deployment_mount"
	CapUnifiedStorage     = "unified_storage"
	CapBootcImageStorage  = "bootc_image_storage"
)

// Capability is one answer with the reason for it.
type Capability struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Bootc is what the probe could establish about one bootc build.
type Bootc struct {
	// Context says which bootc this is. See BootcContextLiveHost.
	Context string `json:"context"`
	// Found reports whether a bootc command exists in that context.
	Found bool `json:"found"`
	// Version is the version bootc reports ("1.17.1"), or "" when bootc is
	// missing, failed, or printed something else.
	Version string `json:"version"`
	// VersionReason says why Version is empty.
	VersionReason string `json:"version_reason,omitempty"`
	// Capabilities maps a capability name to its answer. Every name this
	// version of the probe knows is always present.
	Capabilities map[string]Capability `json:"capabilities"`
}

// helpProbes are the capabilities a subcommand's own --help can establish.
var helpProbes = []struct {
	name string
	argv []string
}{
	{CapContainerInspect, []string{"container", "inspect", "--help"}},
	{CapPrintConfiguration, []string{"install", "print-configuration", "--help"}},
	{CapInstallFinalize, []string{"install", "finalize", "--help"}},
}

// notEstablished are capabilities of the selected image, not of a command
// line: nothing the live host can run read-only answers them.
var notEstablished = []string{CapDeploymentMount, CapUnifiedStorage, CapBootcImageStorage}

// bootcVersionRe matches the one line `bootc --version` prints. Only what it
// captures is ever copied into the output, so a broken or hostile bootc
// cannot put arbitrary text there.
var bootcVersionRe = regexp.MustCompile(`^bootc\s+v?([0-9]+(?:\.[0-9]+){1,3}(?:[-+~][0-9A-Za-z.+~-]{0,40})?)$`)

// Bootc reports the host's bootc. It runs `bootc --version` and three
// `--help` calls; none of them reads a disk, an image store or the network.
func (p *Prober) Bootc() Bootc {
	b := Bootc{Context: BootcContextLiveHost, Capabilities: map[string]Capability{}}
	// What only the selected image can answer is unknown whatever the host's
	// bootc is: the install runs the image's own bootc, not the host's.
	for _, n := range notEstablished {
		b.Capabilities[n] = Capability{CapUnknown, ReasonNotEstablished}
	}
	all := func(status, reason string) Bootc {
		for _, h := range helpProbes {
			b.Capabilities[h.name] = Capability{status, reason}
		}
		return b
	}

	out, err := p.run("bootc", "--version")
	if err != nil {
		if commandMissing(err) {
			b.VersionReason = ReasonBootcMissing
			return all(CapUnsupported, ReasonBootcMissing)
		}
		b.Found = true
		b.VersionReason = ReasonBootcFailed
		return all(CapUnknown, ReasonBootcFailed)
	}
	b.Found = true
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if m := bootcVersionRe.FindStringSubmatch(strings.TrimSpace(first)); m != nil {
		b.Version = m[1]
	} else {
		b.VersionReason = VersionMalformed
	}

	for _, h := range helpProbes {
		_, err := p.run("bootc", h.argv...)
		switch {
		case err == nil:
			b.Capabilities[h.name] = Capability{CapSupported, ReasonCommandPresent}
		case exitCode(err) == 2:
			// clap's exit status for an unrecognised subcommand.
			b.Capabilities[h.name] = Capability{CapUnsupported, ReasonCommandAbsent}
		default:
			b.Capabilities[h.name] = Capability{CapUnknown, ReasonCheckFailed}
		}
	}
	return b
}

// commandMissing reports whether err means the program does not exist:
// exec's own lookup failure, or the shell convention (127) that
// `flatpak-spawn --host` passes through from the host.
func commandMissing(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || exitCode(err) == 127
}

// exitCode is the exit status inside err, or -1.
func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
