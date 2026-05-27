package luks

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
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

// UUID returns the UUID of the LUKS container on partition by running
// cryptsetup luksUUID. Returns an empty string on any error.
func UUID(partition string) string {
	out, err := runner.Output("cryptsetup", "luksUUID", partition)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// EnrollTPM2 adds a TPM2 auto-unlock token to an existing LUKS2 container,
// authenticating with the supplied passphrase. The passphrase remains as a
// fallback unlock method. PCR 7 (Secure Boot state) is used by default.
//
// The passphrase is written to a temp file rather than passed via stdin because
// systemd-cryptenroll resolves "--unlock-key-file=-" against the process home
// directory (/var/roothome when running under pkexec), which fails when that
// path does not exist. A temp file avoids this root home lookup entirely.
func EnrollTPM2(partition, passphrase string) error {
	f, err := os.CreateTemp("", "fisherman-luks-key-*")
	if err != nil {
		return fmt.Errorf("creating temp key file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(passphrase); err != nil {
		f.Close()
		return fmt.Errorf("writing temp key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing temp key file: %w", err)
	}
	return runner.Run(
		"systemd-cryptenroll",
		"--tpm2-device=auto",
		"--tpm2-pcrs=7",
		fmt.Sprintf("--unlock-key-file=%s", f.Name()),
		partition,
	)
}
