package luks

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// Format creates a LUKS2 container on partition, reading the passphrase from stdin
// to avoid it appearing in the process table.
func Format(partition, passphrase string) error {
	cmd := exec.Command(
		"cryptsetup", "luksFormat",
		"--batch-mode",
		"--type=luks2",
		"--key-file=-",
		partition,
	)
	cmd.Stdin = strings.NewReader(passphrase)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	fmt.Fprintf(os.Stdout, "+ cryptsetup luksFormat --batch-mode --type=luks2 --key-file=- %s\n", partition)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cryptsetup luksFormat %s: %w", partition, err)
	}
	return nil
}

// Open opens an existing LUKS container, making it available at
// /dev/mapper/<mapperName>. The passphrase is passed via stdin.
func Open(partition, passphrase, mapperName string) error {
	cmd := exec.Command(
		"cryptsetup", "luksOpen",
		"--key-file=-",
		partition,
		mapperName,
	)
	cmd.Stdin = strings.NewReader(passphrase)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	fmt.Fprintf(os.Stdout, "+ cryptsetup luksOpen --key-file=- %s %s\n", partition, mapperName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cryptsetup luksOpen %s: %w", partition, err)
	}
	return nil
}

// Close closes the LUKS device identified by mapperName.
func Close(mapperName string) error {
	cmd := exec.Command("cryptsetup", "luksClose", mapperName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	fmt.Fprintf(os.Stdout, "+ cryptsetup luksClose %s\n", mapperName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cryptsetup luksClose %s: %w", mapperName, err)
	}
	return nil
}

// EnrollTPM2 adds a TPM2 auto-unlock token to an existing LUKS2 container,
// authenticating with the supplied passphrase. The passphrase remains as a
// fallback unlock method. PCR 7 (Secure Boot state) is used by default.
func EnrollTPM2(partition, passphrase string) error {
	cmd := exec.Command(
		"systemd-cryptenroll",
		"--tpm2-device=auto",
		"--tpm2-pcrs=7",
		"--unlock-key-file=-",
		partition,
	)
	cmd.Stdin = strings.NewReader(passphrase)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	fmt.Fprintf(os.Stdout, "+ systemd-cryptenroll --tpm2-device=auto --tpm2-pcrs=7 %s\n", partition)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemd-cryptenroll %s: %w", partition, err)
	}
	return nil
}
