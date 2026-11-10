package luks

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
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

// Deprecated: enroll on first boot with StageFirstBootEnrollment instead —
// install-time PCR 7 (measured in the live installer) never matches the
// installed system's PCR 7, so this can't unseal on first boot. Retained
// for API compatibility.
//
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
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		f.Close()
		return fmt.Errorf("setting temp key file permissions: %w", err)
	}
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

// AddKeyFile adds a keyslot to the LUKS2 container on partition. The new
// slot's key is the content of newKeyFile; passphrase unlocks an existing
// slot and goes on stdin, so neither secret is in the process table. slot
// pins the new keyslot's number, so it can be removed later by number.
func AddKeyFile(partition, passphrase, newKeyFile string, slot int) error {
	return runner.RunWithStdin(
		strings.NewReader(passphrase),
		"cryptsetup", "luksAddKey",
		"--batch-mode",
		"--key-file=-",
		"--key-slot", strconv.Itoa(slot),
		partition,
		newKeyFile,
	)
}

// RemoveKeyFile removes the keyslot that the content of keyFile unlocks.
// It never removes another slot: a key that matches none is an error.
func RemoveKeyFile(partition, keyFile string) error {
	return runner.Run("cryptsetup", "luksRemoveKey", partition, keyFile)
}
