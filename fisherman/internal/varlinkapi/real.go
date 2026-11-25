package varlinkapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tuna-os/fisherman/internal/crypt"
	"github.com/tuna-os/fisherman/internal/handoff"
	"github.com/tuna-os/fisherman/internal/repart"
)

// Real is the executor that changes real disks. Requirements, Probe,
// Install, the CryptEnroll calls, Shutdown and the reboot handoff are
// implemented. The migrate and soft_reboot handoffs stay deferred to
// tunaOS#4908 and tunaOS#4909 (see DecideHandoff).
//
// Every host program runs through Cmd, so unit tests substitute a fake and
// pin the command lines. The install model is the one of ADR 0018: the
// medium is a GPT image, systemd-repart mirrors it onto the target with
// CopyBlocks=, the user's passphrase replaces the empty keyslot, the
// postfix fixups run on the mounted copy, ConfigureTarget writes the
// account and locale, and everything unmounts and closes.
type Real struct {
	// SysRoot is the sysfs root ("/sys" in production). Tests point it at
	// a fixture tree for the TPM2 and Intel RST checks.
	SysRoot string
	// DevDir holds disk/by-uuid ("/dev" in production).
	DevDir string
	// CmdlinePath holds the kernel command line ("/proc/cmdline"). It names
	// the medium (root=UUID=), so an integration test points it at a file.
	CmdlinePath string
	// Root is the filesystem root the probe reads /proc and /sys from ("/").
	Root string
	// Sfdisk runs sfdisk --json for a disk. Nil runs it through Cmd. It
	// must not log its output.
	Sfdisk func(ctx context.Context, disk string) ([]byte, error)
	// Cmd runs host programs. Nil means the system tools.
	Cmd Commander
	// Enroller wraps systemd-cryptenroll. Nil means the system tools.
	Enroller *crypt.Enroller
	// HandoffRunner runs the handoff helpers. Nil runs them through Cmd.
	HandoffRunner handoff.Runner
	// WorkDir holds the install's scratch tree: definitions, mount points.
	// It is a tmpfs, so a passphrase-derived path never reaches a disk.
	// Default "/run/fishermand".
	WorkDir string
	// EFIVars is the efivars directory. The firmware boot entry is added
	// only when it exists, like the Utah script. Default
	// "/sys/firmware/efi/efivars".
	EFIVars string
	// NoFirmwareEntry never writes a firmware boot entry. The integration
	// test sets it: it runs on a host with real NVRAM.
	NoFirmwareEntry bool
	// SetFATSerial writes a FAT volume ID. Nil writes the boot sector of the
	// block device; tests substitute a recorder.
	SetFATSerial func(dev string, serial uint32) error
	// DiskExists tells whether a disk node still exists, to tell a lost
	// device from a failed command. Nil stats the path.
	DiskExists func(path string) bool
}

var _ Executor = Real{}

func (r Real) cmd() Commander {
	if r.Cmd != nil {
		return r.Cmd
	}
	return SystemCommander()
}

func (r Real) sysRoot() string {
	if r.SysRoot != "" {
		return r.SysRoot
	}
	return "/sys"
}

func (r Real) workDir() string {
	if r.WorkDir != "" {
		return r.WorkDir
	}
	return "/run/fishermand"
}

func (r Real) efiVars() string {
	if r.EFIVars != "" {
		return r.EFIVars
	}
	return "/sys/firmware/efi/efivars"
}

func (r Real) enroller() *crypt.Enroller {
	if r.Enroller != nil {
		return r.Enroller
	}
	return crypt.SystemEnroller()
}

// hasTPM2 is the one TPM decision of the install: the same check the repart
// definitions make.
func (r Real) hasTPM2() bool { return repart.HasTPM2(r.sysRoot()) }

func (r Real) MakeRecoveryKey(ctx context.Context) (Secret, error) {
	key, err := crypt.GenerateRecoveryKey()
	if err != nil {
		return "", err
	}
	return Secret(key), nil
}

func (r Real) Enroll(ctx context.Context, p EnrollParams) (EnrollResult, error) {
	enroller := r.enroller()
	opts := crypt.EnrollOpts{
		Node:      p.Node,
		Mechanism: p.Mechanism,
		WipeSlots: append([]int64(nil), p.WipeSlots...),
	}
	if p.RecoveryKey != nil {
		opts.RecoveryKey = []byte(p.RecoveryKey.Reveal())
	}
	if p.Password != nil {
		opts.NewPassword = []byte(p.Password.Reveal())
	}
	if p.UnlockPassword != nil {
		opts.Unlock = crypt.Unlock{Secret: []byte(p.UnlockPassword.Reveal())}
	}
	res, err := enroller.Enroll(ctx, opts)
	if err != nil {
		return EnrollResult{}, mapCryptError(err)
	}
	out := EnrollResult{WipedSlots: res.WipedSlots, Slot: res.Slot}
	if res.RecoveryKey != nil {
		out.RecoveryKey = ptr(Secret(res.RecoveryKey))
	}
	out.State = ptr("enrolled")
	return out, nil
}

func (r Real) ListSlots(ctx context.Context, node string) ([]Slot, error) {
	enroller := r.enroller()
	slots, err := enroller.ListSlots(ctx, node)
	if err != nil {
		return nil, mapCryptError(err)
	}
	out := make([]Slot, 0, len(slots))
	for _, s := range slots {
		out = append(out, Slot{Slot: s.Number, Type: s.Type})
	}
	return out, nil
}

// mapCryptError turns the crypt wrapper's sentinels into the daemon's wire
// errors. The mock returns the same sentinels, so both backends agree.
func mapCryptError(err error) error {
	switch {
	case errors.Is(err, crypt.ErrNoDevice):
		return ErrNoDevice
	case errors.Is(err, crypt.ErrPasswordRequired):
		return ErrPasswordRequired
	case errors.Is(err, crypt.ErrPasswordIncorrect):
		return ErrPasswordIncorrect
	case errors.Is(err, crypt.ErrForeignVolume):
		return ErrVolumeForeign
	case errors.Is(err, crypt.ErrFidoDeviceNotFound):
		return ErrFidoDeviceNotFound
	case errors.Is(err, crypt.ErrFidoTimeout):
		return ErrFidoTimeout
	}
	return err
}

func (r Real) Handoff(ctx context.Context, mode string) (bool, error) {
	run := r.HandoffRunner
	if run == nil {
		run = handoffRunner(r.cmd())
	}
	return handoff.Handoff(ctx, run, mode)
}

// DecideHandoff implements Handoffer. It decides reboot everywhere for now:
// handoff.Decide would pick migrate for a btrfs root and soft_reboot for
// EL10, but both modes fail with handoff.ErrDeferred until their mechanism
// lands, and a frontend that was told migrate would show a page for a
// handoff that cannot run.
//
// TODO(tunaOS#4908, tunaOS#4909): feed handoff.Facts (root filesystem of
// the medium, EL base) here once the migrate and soft-reboot hooks exist.
// The decision matrix itself is already tested in handoff.Decide.
func (r Real) DecideHandoff(context.Context) (HandoffDecision, error) {
	mode, reason := handoff.Decide(handoff.Facts{})
	return HandoffDecision{Mode: mode, Reason: reason}, nil
}

// mediumRoot finds the live medium: the disk holding the booted root, and
// the medium root partition node. It follows the Utah script: root=UUID= on
// the kernel command line names the medium root, whose parent disk (via
// lsblk) is the medium.
func (r Real) mediumRoot(ctx context.Context) (disk, rootNode string, err error) {
	cmdlinePath, devDir := r.CmdlinePath, r.DevDir
	if cmdlinePath == "" {
		cmdlinePath = "/proc/cmdline"
	}
	if devDir == "" {
		devDir = "/dev"
	}
	cmdline, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return "", "", fmt.Errorf("real executor: reading cmdline: %w", err)
	}
	uuid, err := repart.RootFSUUID(string(cmdline))
	if err != nil {
		return "", "", fmt.Errorf("real executor: %w", err)
	}
	rootNode, err = repart.RootNodeByFSUUID(devDir, uuid)
	if err != nil {
		return "", "", fmt.Errorf("real executor: %w", err)
	}
	out, err := r.cmd().Run(ctx, "lsblk", []string{"-rno", "PKNAME", rootNode}, CmdOpts{})
	if err != nil {
		return "", "", fmt.Errorf("real executor: finding the medium disk: %w", err)
	}
	if parent := strings.TrimSpace(string(out)); parent != "" {
		return "/dev/" + parent, rootNode, nil
	}
	return "", "", fmt.Errorf("real executor: no parent disk for %s", rootNode)
}

// Definitions generates the systemd-repart definitions for the live medium:
// the medium's GPT mirrored partition by partition with CopyBlocks=, the
// root into LUKS2 (key-file+tpm2 on PCR 7 with a TPM 2.0, else key-file).
// Install's partition step will write and apply these; --dry-run prints them.
func (r Real) Definitions(ctx context.Context) ([]repart.Definition, error) {
	disk, rootNode, err := r.mediumRoot(ctx)
	if err != nil {
		return nil, err
	}
	sfdisk := r.Sfdisk
	if sfdisk == nil {
		sfdisk = func(ctx context.Context, disk string) ([]byte, error) {
			return r.cmd().Run(ctx, "sfdisk", []string{"--json", disk}, CmdOpts{})
		}
	}
	raw, err := sfdisk(ctx, disk)
	if err != nil {
		return nil, fmt.Errorf("real executor: sfdisk for %s: %w", disk, err)
	}
	table, err := repart.ParseSfdisk(raw, disk)
	if err != nil {
		return nil, err
	}
	sysRoot := r.SysRoot
	if sysRoot == "" {
		sysRoot = "/sys"
	}
	return repart.Generate(table, rootNode, repart.HasTPM2(sysRoot))
}
