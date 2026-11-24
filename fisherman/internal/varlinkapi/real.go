package varlinkapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/tuna-os/fisherman/internal/crypt"
	"github.com/tuna-os/fisherman/internal/handoff"
	"github.com/tuna-os/fisherman/internal/repart"
	"os"
	"os/exec"
	"strings"
)

// Real is the executor that changes real disks. The repart definitions, the
// cryptenroll wrapper (passphrase, recovery key, wipe) and the reboot
// handoff are implemented; Probe, Install, Power and the deferred migrate /
// soft-reboot handoff return ErrNotImplemented or ErrDeferred until their
// slices land (postfix, socket auth), so fishermand still refuses to start
// without --mock.
//
// The work that slots in here (tuna-os/fisherman#512):
//
//   - Probe: reuse internal/probe (lsblk, TPM, system gates) plus Intel
//     RST/VMD detection and existing-data inspection.
//   - Install: generate and run systemd-repart definitions; LUKS2 and TPM2
//     PCR 7 enrolment via systemd-cryptenroll; copy the medium image or the
//     btrfs seed/migrate (tunaOS#4908); write the target with
//     systemd-firstboot --root; the Utah post-install fixups (UUIDs,
//     rd.luks.uuid, crypttab, live entry removal, firmware boot entry);
//     cleanup on failure and cancel.
//   - CryptEnroller: call systemd-cryptenroll, or the real
//     io.systemd.CryptEnroll socket.
//   - Power: call io.systemd.Shutdown on the real system bus socket.
//   - Handoff: btrfs migrate, EL10 soft reboot (tunaOS#4909) or reboot.
type Real struct {
	// SysRoot is the sysfs root ("/sys" in production). Tests point it at
	// a fixture tree for the TPM2 check.
	SysRoot string
	// DevDir holds disk/by-uuid ("/dev" in production).
	DevDir string
	// CmdlinePath holds the kernel command line ("/proc/cmdline").
	CmdlinePath string
	// Sfdisk runs sfdisk --json for a disk. The default shells out;
	// tests substitute canned output. It must not log its output.
	Sfdisk func(ctx context.Context, disk string) ([]byte, error)
	// Enroller wraps systemd-cryptenroll. Nil means the system tools.
	Enroller *crypt.Enroller
	// HandoffRunner runs the handoff helpers. Nil means the system tools.
	HandoffRunner handoff.Runner
}

var _ Executor = Real{}

func (Real) Requirements(context.Context) (RequirementsResult, error) {
	return RequirementsResult{}, ErrNotImplemented
}

func (Real) Probe(context.Context) (ProbeResult, error) { return ProbeResult{}, ErrNotImplemented }

func (Real) WatchDevices(context.Context) (<-chan DeviceEvent, error) {
	return nil, ErrNotImplemented
}

func (Real) Install(context.Context, InstallPlan, Reporter) error { return ErrNotImplemented }

func (r Real) MakeRecoveryKey(ctx context.Context) (Secret, error) {
	key, err := crypt.GenerateRecoveryKey()
	if err != nil {
		return "", err
	}
	return Secret(key), nil
}

func (r Real) Enroll(ctx context.Context, p EnrollParams) (EnrollResult, error) {
	enroller := r.Enroller
	if enroller == nil {
		enroller = crypt.SystemEnroller()
	}
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
	enroller := r.Enroller
	if enroller == nil {
		enroller = crypt.SystemEnroller()
	}
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

func (Real) Shutdown(context.Context, string, ShutdownParams) error { return ErrNotImplemented }

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
		run = handoff.RunnerFunc(handoff.SystemRunner)
	}
	return handoff.Handoff(ctx, run, mode)
}

// DecideHandoff implements Handoffer. The fact-gathering Probe slice is
// not landed, so the decision sees zero facts and picks reboot; the
// matrix itself (btrfs seed, EL10, default) is tested in handoff.Decide.
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
	out, err := exec.CommandContext(ctx, "lsblk", "-rno", "PKNAME", rootNode).Output()
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
			return exec.CommandContext(ctx, "sfdisk", "--json", disk).Output()
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
