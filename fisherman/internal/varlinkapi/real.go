package varlinkapi

import "context"

// Real is the placeholder for the executor that changes real disks. It is
// NOT IMPLEMENTED in this slice: every method returns ErrNotImplemented, so
// fishermand refuses to start without --mock.
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
type Real struct{}

var _ Executor = Real{}

func (Real) Requirements(context.Context) (RequirementsResult, error) {
	return RequirementsResult{}, ErrNotImplemented
}

func (Real) Probe(context.Context) (ProbeResult, error) { return ProbeResult{}, ErrNotImplemented }

func (Real) WatchDevices(context.Context) (<-chan DeviceEvent, error) {
	return nil, ErrNotImplemented
}

func (Real) Install(context.Context, InstallPlan, Reporter) error { return ErrNotImplemented }

func (Real) MakeRecoveryKey(context.Context) (Secret, error) { return "", ErrNotImplemented }

func (Real) Enroll(context.Context, EnrollParams) (EnrollResult, error) {
	return EnrollResult{}, ErrNotImplemented
}

func (Real) ListSlots(context.Context, string) ([]Slot, error) { return nil, ErrNotImplemented }

func (Real) Shutdown(context.Context, string, ShutdownParams) error { return ErrNotImplemented }

func (Real) Handoff(context.Context, string) (bool, error) { return false, ErrNotImplemented }
