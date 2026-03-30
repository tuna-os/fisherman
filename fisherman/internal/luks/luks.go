package luks

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tuna-os/fisherman/internal/runner"
)

const mapperPrefix = "/dev/mapper/"

// MapperPath returns the /dev/mapper/ device path for a mapper name.
func MapperPath(name string) string {
	return mapperPrefix + name
}

// RandomPassphrase generates a cryptographically random 64-character hex passphrase.
func RandomPassphrase() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("luks: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// Format creates a LUKS2 container on partition, reading the passphrase from
// stdin to avoid it appearing in the process table.
func Format(partition, passphrase string) error {
	return runner.RunWithStdin(
		strings.NewReader(passphrase),
		"cryptsetup", "luksFormat",
		"--batch-mode",
		"--type=luks2",
		"--key-file=-",
		partition,
	)
}

// Open opens an existing LUKS container, making it available at
// /dev/mapper/<mapperName>. The passphrase is passed via stdin.
func Open(partition, passphrase, mapperName string) error {
	return runner.RunWithStdin(
		strings.NewReader(passphrase),
		"cryptsetup", "luksOpen",
		"--key-file=-",
		partition,
		mapperName,
	)
}

// Close closes the LUKS device identified by mapperName.
func Close(mapperName string) error {
	return runner.Run("cryptsetup", "luksClose", mapperName)
}

// EnrollTPM2 adds a TPM2 auto-unlock token to an existing LUKS2 container,
// authenticating with the supplied passphrase. The passphrase remains as a
// fallback unlock method. PCR 7 (Secure Boot state) is used by default.
func EnrollTPM2(partition, passphrase string) error {
	return runner.RunWithStdin(
		strings.NewReader(passphrase),
		"systemd-cryptenroll",
		"--tpm2-device=auto",
		"--tpm2-pcrs=7",
		"--unlock-key-file=-",
		partition,
	)
}
