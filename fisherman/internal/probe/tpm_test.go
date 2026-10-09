package probe

import (
	"path/filepath"
	"testing"
)

// The four cases of bootc-installer's shared/tpm/README.md, against copies of
// its fixtures. Usable must match the contract's result column exactly.
func TestTPMSharedFixtures(t *testing.T) {
	tests := []struct {
		fixture string
		want    TPM
	}{
		{"tpm2", TPM{Present: true, Version: "2.0", Usable: true}},
		{"tpm12", TPM{Present: true, Version: "1.2", Usable: false}},
		{"legacy-tpm2", TPM{Present: true, Version: "2.0", Usable: true}},
		{"legacy-none", TPM{Present: true, Version: "", Usable: false}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			p := &Prober{Root: filepath.Join("testdata", "tpm", tt.fixture), Getenv: func(string) string { return "" }}
			if got := p.TPM(); got != tt.want {
				t.Errorf("TPM() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTPMAbsent(t *testing.T) {
	p := &Prober{Root: t.TempDir(), Getenv: func(string) string { return "" }}
	if got := p.TPM(); got != (TPM{}) {
		t.Errorf("TPM() on an empty root = %+v", got)
	}
}

// BOOTC_INSTALLER_FAKE_TPM forces usable ON, never OFF, and blank or "0"
// does not count.
func TestTPMFakeEnv(t *testing.T) {
	tests := []struct {
		fixture, env string
		usable       bool
	}{
		{"tpm12", "1", true},
		{"tpm12", "", false},
		{"tpm12", "0", false},
		{"tpm2", "0", true},
	}
	for _, tt := range tests {
		p := &Prober{Root: filepath.Join("testdata", "tpm", tt.fixture), Getenv: func(k string) string {
			if k == FakeTPMEnv {
				return tt.env
			}
			return ""
		}}
		if got := p.TPM().Usable; got != tt.usable {
			t.Errorf("%s with %s=%q: usable=%v, want %v", tt.fixture, FakeTPMEnv, tt.env, got, tt.usable)
		}
	}
}
