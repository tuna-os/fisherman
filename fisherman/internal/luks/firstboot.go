package luks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tuna-os/fisherman/internal/deploy"
)

// First-boot TPM2 enrolment.
//
// systemd-cryptenroll --tpm2-pcrs=7 seals against PCR 7 (Secure Boot state)
// as measured when it runs. Inside the installer that is the live medium's
// boot chain; the installed system measures another, so a token enrolled at
// install time never unseals. The enrolment therefore runs on the first boot
// of the installed system, from a oneshot unit staged here.
//
// The unit needs a key to authorise the enrolment. That key is NOT the
// user's passphrase and NOT the recovery key: it is a random key in its own
// keyslot (EnrollKeySlot), added at install time for this one use. The unit
// removes the slot and the file when it stops, whether the enrolment
// succeeded or not. A copy of the file that survives on the disk (a
// snapshot, an SSD's old blocks) then unlocks nothing (#409).
//
// Everything is staged in the deployment's /etc (deploy.Deployment.Etc), the
// one the installed system boots with. <target>/etc and <target>/usr are the
// physical sysroot: nothing staged there ever runs (#360).
const (
	// EnrollUnitName is the first-boot oneshot.
	EnrollUnitName = "fisherman-tpm2-enroll.service"
	// EnrollKeyPath is the single-use key, as the installed system sees it.
	EnrollKeyPath = "/etc/fisherman/tpm2-enroll.key"
	// EnrollKeySlot is the LUKS2 keyslot of the single-use key: the last
	// one, far from the slots cryptsetup and systemd-cryptenroll allocate
	// from 0 upward.
	EnrollKeySlot = 31
	// EnrollAttempts bounds the enrolment retries within the one boot the
	// unit runs in. There is no retry on a later boot: the key is gone.
	EnrollAttempts = 3
)

// ErrNoCryptenroll means the installed image has no systemd-cryptenroll, so
// a first-boot enrolment could only fail. Nothing was staged.
var ErrNoCryptenroll = errors.New("the installed image has no systemd-cryptenroll")

// ErrTransientEtc means the installed image boots with a transient /etc: a
// unit staged there is gone before it can run. Nothing was staged.
var ErrTransientEtc = errors.New("the installed image uses a transient /etc")

// cryptenrollPaths are where an image ships systemd-cryptenroll, relative
// to /usr.
var cryptenrollPaths = []string{"bin/systemd-cryptenroll", "sbin/systemd-cryptenroll", "lib/systemd/systemd-cryptenroll"}

// EnrollUnit returns the first-boot unit for the LUKS container luksUUID.
//
// ExecStart tries the enrolment up to EnrollAttempts times (the TPM device
// can appear late). ExecStopPost runs when the service stops for any
// reason: after success, after the last failed attempt, after a timeout and
// when ExecStart could not be executed at all. It removes the single-use
// keyslot (by its key with cryptsetup, else by its number with
// systemd-cryptenroll), then the key file, then the unit's own enablement.
// The file is removed even when no tool could remove the slot.
func EnrollUnit(luksUUID string) string {
	dev := "/dev/disk/by-uuid/" + luksUUID
	slot := strconv.Itoa(EnrollKeySlot)
	enroll := "systemd-cryptenroll --tpm2-device=auto --tpm2-pcrs=7 --unlock-key-file=" + EnrollKeyPath + " " + dev
	attempts := make([]string, EnrollAttempts)
	for i := range attempts {
		attempts[i] = strconv.Itoa(i + 1)
	}
	return `[Unit]
Description=Fisherman first-boot TPM2 LUKS enrollment
Documentation=https://github.com/tuna-os/fisherman
ConditionPathExists=` + EnrollKeyPath + `
After=basic.target
DefaultDependencies=no

[Service]
Type=oneshot
RemainAfterExit=no
TimeoutStartSec=300
Environment=PATH=/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/bin/sh -c 'for attempt in ` + strings.Join(attempts, " ") + `; do ` + enroll + ` && exit 0; sleep 10; done; exit 1'
ExecStopPost=-/bin/sh -c 'cryptsetup luksRemoveKey ` + dev + ` ` + EnrollKeyPath + ` || systemd-cryptenroll --wipe-slot=` + slot + ` ` + dev + `'
ExecStopPost=-/bin/sh -c 'rm -f ` + EnrollKeyPath + `; rmdir ` + filepath.Dir(EnrollKeyPath) + `; rm -f /etc/systemd/system/multi-user.target.wants/` + EnrollUnitName + `'

[Install]
WantedBy=multi-user.target
`
}

// StageFirstBootEnrollment prepares the installed system d to enrol a TPM2
// token for the LUKS container on partition (UUID luksUUID) at its first
// boot. passphrase unlocks an existing keyslot; it is used once, here, to
// add the single-use keyslot, and is never written to the target.
//
// It returns ErrNoCryptenroll or ErrTransientEtc, having changed nothing,
// when the installed image cannot run the enrolment. On any later failure it
// removes what it added: no keyslot and no key file are left behind.
func StageFirstBootEnrollment(d *deploy.Deployment, partition, luksUUID, passphrase string) error {
	if luksUUID == "" {
		return fmt.Errorf("first-boot TPM2 enrollment needs the LUKS UUID")
	}
	if d == nil {
		return fmt.Errorf("first-boot TPM2 enrollment needs the installed deployment")
	}
	if d.EtcIsTransient() {
		return ErrTransientEtc
	}
	// The unit runs the TARGET's systemd-cryptenroll, so the target is where
	// it must exist. A composefs image keeps /usr in its object store, where
	// it cannot be inspected here: the unit is staged, and cleans up after
	// itself if the tool turns out to be missing.
	if present, known := d.HasUsrFile(cryptenrollPaths...); known && !present {
		return ErrNoCryptenroll
	}

	keyDir := filepath.Join(d.Etc, "fisherman")
	keyPath := filepath.Join(keyDir, filepath.Base(EnrollKeyPath))
	unitDir := filepath.Join(d.Etc, "systemd", "system")
	unitPath := filepath.Join(unitDir, EnrollUnitName)
	wantsDir := filepath.Join(unitDir, "multi-user.target.wants")
	link := filepath.Join(wantsDir, EnrollUnitName)

	slotAdded := false
	undo := func() {
		if slotAdded {
			_ = RemoveKeyFile(partition, keyPath)
		}
		_ = os.Remove(link)
		_ = os.Remove(unitPath)
		_ = os.Remove(keyPath)
		_ = os.Remove(keyDir) // only when empty
	}

	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", keyDir, err)
	}
	deploy.InheritLabel(keyDir)
	// O_EXCL after a remove: never write the key through a file (or a
	// symlink) that was already there with other permissions (#221).
	_ = os.Remove(keyPath)
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		undo()
		return fmt.Errorf("create single-use key: %w", err)
	}
	_, werr := f.WriteString(RandomPassphrase())
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(keyPath, 0o600)); err != nil {
		undo()
		return fmt.Errorf("write single-use key: %w", err)
	}
	deploy.InheritLabel(keyPath)

	if err := AddKeyFile(partition, passphrase, keyPath, EnrollKeySlot); err != nil {
		undo()
		return fmt.Errorf("adding the single-use enrolment keyslot: %w", err)
	}
	slotAdded = true

	// The unit goes in /etc/systemd/system: on a bootc system /usr is the
	// image's, read-only and (ostree) hard-linked into the repository.
	_, statErr := os.Stat(wantsDir)
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		undo()
		return fmt.Errorf("mkdir %s: %w", wantsDir, err)
	}
	if statErr != nil {
		deploy.InheritLabel(wantsDir)
	}
	if err := os.WriteFile(unitPath, []byte(EnrollUnit(luksUUID)), 0o644); err != nil {
		undo()
		return fmt.Errorf("write unit: %w", err)
	}
	deploy.InheritLabel(unitPath)
	// Enable via wants symlink (systemctl enable isn't available offline).
	_ = os.Remove(link)
	if err := os.Symlink("/etc/systemd/system/"+EnrollUnitName, link); err != nil {
		undo()
		return fmt.Errorf("enable unit: %w", err)
	}
	deploy.InheritLabel(link)
	return nil
}
