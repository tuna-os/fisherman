package varlinkapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tuna-os/fisherman/internal/handoff"
)

// Scenario scripts the mock. Build one with ParseScenario; names combine
// with commas ("no-tpm,existing-data").
type Scenario struct {
	// Spec is the normalised name list the scenario came from.
	Spec string

	NoTPM        bool // no usable TPM 2.0: the frontend asks for a passphrase
	RST          bool // Intel RST/VMD hides the NVMe disk
	ExistingData bool // the NVMe disk holds Windows; adds a foreign-label disk
	TightSpace   bool // like existing-data, but the free space is below the minimum
	LowRAM       bool // a requirement gate blocks
	NoUEFI       bool // legacy BIOS: a requirement gate blocks
	Inhibitor    bool // Shutdown calls without skipInhibitors are blocked
	// HandoffMode overrides the daemon's takeover decision (Probe handoff
	// and the mode Handoff uses with no mode). Empty means reboot.
	HandoffMode string

	// Failure injection. FailStep names the step; FailReason is the reason.
	FailStep   string
	FailReason string
	// HoldStep makes the install wait at 50% of that step until cancelled.
	HoldStep string
}

// scenarioDefs documents and builds every name.
var scenarioDefs = map[string]struct {
	doc   string
	apply func(*Scenario)
}{
	"happy":         {"clean install, TPM present, empty disk", func(*Scenario) {}},
	"no-tpm":        {"no usable TPM 2.0", func(s *Scenario) { s.NoTPM = true }},
	"rst":           {"Intel RST/VMD hides the NVMe disk", func(s *Scenario) { s.RST = true }},
	"existing-data": {"Windows on the NVMe disk, plus a disk with a foreign label", func(s *Scenario) { s.ExistingData = true }},
	"tight-space":   {"Windows fills the NVMe disk: free space is below the minimum", func(s *Scenario) { s.ExistingData, s.TightSpace = true, true }},
	"low-ram":       {"a RAM requirement gate blocks", func(s *Scenario) { s.LowRAM = true }},
	"no-uefi":       {"legacy BIOS: the firmware gate blocks", func(s *Scenario) { s.NoUEFI = true }},
	"inhibitor":     {"a block inhibitor stops Shutdown unless skipInhibitors is set", func(s *Scenario) { s.Inhibitor = true }},
	"device-lost": {"the target disk disappears during install_os", func(s *Scenario) {
		s.FailStep, s.FailReason = StepInstallOS, ReasonDeviceLost
	}},
	"repart-error": {"systemd-repart fails during partition", func(s *Scenario) {
		s.FailStep, s.FailReason = StepPartition, ReasonRepartError
	}},
	"cancel": {"the install waits during install_os until Cancel is called", func(s *Scenario) {
		s.HoldStep = StepInstallOS
	}},
	"handoff-migrate":     {"the daemon's takeover decision is migrate (btrfs seed)", func(s *Scenario) { s.HandoffMode = handoff.ModeMigrate }},
	"handoff-soft-reboot": {"the daemon's takeover decision is soft_reboot (EL10 block copy)", func(s *Scenario) { s.HandoffMode = handoff.ModeSoftReboot }},
}

// ScenarioNames lists the valid scenario names, sorted.
func ScenarioNames() []string {
	out := make([]string, 0, len(scenarioDefs))
	for n := range scenarioDefs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ScenarioDoc describes a scenario name.
func ScenarioDoc(name string) string { return scenarioDefs[name].doc }

// ParseScenario builds a scenario from a comma-separated name list. The empty
// string means "happy".
func ParseScenario(spec string) (Scenario, error) {
	var s Scenario
	var names []string
	for _, n := range strings.Split(spec, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		d, ok := scenarioDefs[n]
		if !ok {
			return Scenario{}, fmt.Errorf("unknown scenario %q (valid: %s)", n, strings.Join(ScenarioNames(), ", "))
		}
		d.apply(&s)
		names = append(names, n)
	}
	if len(names) == 0 {
		names = []string{"happy"}
	}
	s.Spec = strings.Join(names, ",")
	return s, nil
}
