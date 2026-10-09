package probe

import (
	"os"
)

// The TPM probe follows bootc-installer's shared/tpm/README.md contract,
// which all five frontends implement and test against the same fixtures.
const (
	// tpmVersionFile holds the TCG spec major version: "2" for TPM 2.0, "1"
	// for TPM 1.2. Added in Linux 5.5.
	tpmVersionFile = "/sys/class/tpm/tpm0/tpm_version_major"
	// tpmResourceManager exists only for a TPM 2.0 device (the in-kernel
	// resource manager is a 2.0 feature). The fallback for kernels < 5.5.
	tpmResourceManager = "/dev/tpmrm0"
	// tpmClassDir exists for TPM 1.2 as well, so it only answers "present",
	// never "usable".
	tpmClassDir = "/sys/class/tpm/tpm0"
	// FakeTPMEnv forces Usable to true (never to false), for screenshots.
	FakeTPMEnv = "BOOTC_INSTALLER_FAKE_TPM"
)

// TPM describes the first TPM device.
type TPM struct {
	// Present: the kernel exposes a TPM of any version.
	Present bool `json:"present"`
	// Version is "2.0", "1.2", or "" when no TPM or the version is unknown.
	Version string `json:"version"`
	// Usable: a TPM 2.0 is present, so the tpm2-luks encryption modes can
	// enrol. This is the shared/tpm contract's answer, field for field.
	Usable bool `json:"usable"`
}

// TPM probes the TPM. /sys and /dev are the kernel's, so they are read the
// same way inside and outside a sandbox.
func (p *Prober) TPM() TPM {
	var t TPM
	if v, err := p.readKernelFile(tpmVersionFile); err == nil {
		t.Present = true
		switch v {
		case "2":
			t.Version, t.Usable = "2.0", true
		case "1":
			t.Version = "1.2"
		default:
			t.Version = v
		}
	} else if _, err := os.Stat(p.local(tpmResourceManager)); err == nil {
		t.Present, t.Version, t.Usable = true, "2.0", true
	} else if _, err := os.Stat(p.local(tpmClassDir)); err == nil {
		t.Present = true
	}
	if v := p.getenv(FakeTPMEnv); v != "" && v != "0" {
		t.Usable = true
	}
	return t
}
