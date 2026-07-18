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

// StageFirstBootEnrollment installs a oneshot into the target that enrolls
// the TPM2 token on the FIRST BOOT of the installed system, then shreds the
// transient key and disables itself.
//
// Why not enroll during install: systemd-cryptenroll --tpm2-pcrs=7 seals
// against PCR 7 (Secure Boot state) as measured RIGHT NOW — inside the live
// installer. The installed system boots a different chain and measures a
// different PCR 7, so an install-time enrollment can never unseal on first
// boot (observed: the LUKS E2E dropped to a passphrase prompt). Enrolling in
// the booted target captures the correct PCR 7.
//
// targetMount is the installed root; luksUUID identifies the LUKS partition
// (by-uuid is stable across the install→installed device renumbering); key
// is the transient unlock passphrase/recovery key.
func StageFirstBootEnrollment(targetMount, luksUUID, key string) error {
	if luksUUID == "" {
		return fmt.Errorf("first-boot TPM2 enrollment needs the LUKS UUID")
	}
	keyDir := targetMount + "/etc/fisherman"
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", keyDir, err)
	}
	keyPath := keyDir + "/tpm2-enroll.key"
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		return fmt.Errorf("write transient key: %w", err)
	}

	// The oneshot: enroll against the running system's PCR 7, shred the key,
	// and disable itself so it never runs again. Idempotent — a second run
	// (key already gone) is a clean no-op.
	unit := `[Unit]
Description=Fisherman first-boot TPM2 LUKS enrollment
Documentation=https://github.com/tuna-os/fisherman
ConditionPathExists=/etc/fisherman/tpm2-enroll.key
After=basic.target
DefaultDependencies=no

[Service]
Type=oneshot
RemainAfterExit=no
ExecStart=/usr/bin/systemd-cryptenroll --tpm2-device=auto --tpm2-pcrs=7 --unlock-key-file=/etc/fisherman/tpm2-enroll.key /dev/disk/by-uuid/` + luksUUID + `
ExecStartPost=-/usr/bin/shred -u /etc/fisherman/tpm2-enroll.key
ExecStartPost=-/usr/bin/systemctl disable fisherman-tpm2-enroll.service

[Install]
WantedBy=multi-user.target
`
	unitDir := targetMount + "/usr/lib/systemd/system"
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", unitDir, err)
	}
	unitPath := unitDir + "/fisherman-tpm2-enroll.service"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	// Enable via wants symlink (systemctl enable isn't available offline).
	wantsDir := targetMount + "/etc/systemd/system/multi-user.target.wants"
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir wants: %w", err)
	}
	link := wantsDir + "/fisherman-tpm2-enroll.service"
	_ = os.Remove(link)
	if err := os.Symlink("/usr/lib/systemd/system/fisherman-tpm2-enroll.service", link); err != nil {
		return fmt.Errorf("enable unit: %w", err)
	}
	return nil
}
