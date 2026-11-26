package varlinkapi

import (
	"context"
	"errors"
	"fmt"
)

// Executor is everything the daemon needs from the machine it runs on. The
// service layer (state machine, wire format, validation, step ordering) is
// shared; an Executor only reads hardware or changes the disk.
//
// Two implementations exist: Mock (this slice, touches nothing) and Real
// (placeholder, see real.go). The real one will wrap systemd-repart,
// systemd-cryptenroll, the copy or btrfs migrate, and the Utah post-install
// fixups.
type Executor interface {
	Prober
	Installer
	CryptEnroller
	Power
	Handoffer
}

// Prober reads facts. It must not change anything.
type Prober interface {
	Requirements(ctx context.Context) (RequirementsResult, error)
	Probe(ctx context.Context) (ProbeResult, error)
	// WatchDevices streams hot-plug events until ctx ends. The first
	// events are not replayed; use Probe for the current set.
	WatchDevices(ctx context.Context) (<-chan DeviceEvent, error)
}

// DeviceEvent is a block device hot-plug event.
type DeviceEvent struct {
	Action string // add | remove
	Disk   Disk
}

// InstallPlan is a validated, confirmed install.
type InstallPlan struct {
	Disk Disk
	// Erase wipes the whole disk. False installs into free space.
	Erase      bool
	Encryption Encryption
	Target     TargetConfig
}

// TargetConfig is what ConfigureTarget stored. Written to the target during
// the configure step.
type TargetConfig struct {
	User      *User
	Hostname  string
	Locale    string
	Keymap    string
	TimeZone  string
	Autologin bool
	// Privacy is set only when ConfigureTarget carried it.
	Privacy *Privacy
	// Accessibility is set only when ConfigureTarget carried it.
	Accessibility *Accessibility
}

// Reporter receives step events from an install. The service turns them
// into the stream frontends see, so an Executor cannot break the step id
// contract or make the bar run backwards.
type Reporter interface {
	// Start begins a step. A step that is not Done before the next Start is
	// finished implicitly.
	Start(step string)
	// Progress reports 0-100 within the current step.
	Progress(step string, pct int, message string)
	// Warn reports a problem that does not stop the install.
	Warn(step, message string)
	// Done finishes a step.
	Done(step string)
}

// Installer changes the disk.
type Installer interface {
	// Install runs the whole pipeline. It must clean up after itself on
	// failure and on cancel, reporting that work as StepCleanup, and return
	// *InstallError on failure or ctx.Err() on cancel.
	Install(ctx context.Context, plan InstallPlan, rep Reporter) error
}

// CryptEnroller implements io.systemd.CryptEnroll. Errors are the sentinel
// errors below. Implementations must never log secrets.
type CryptEnroller interface {
	MakeRecoveryKey(ctx context.Context) (Secret, error)
	Enroll(ctx context.Context, p EnrollParams) (EnrollResult, error)
	ListSlots(ctx context.Context, node string) ([]Slot, error)
}

// Power implements io.systemd.Shutdown. kind is one of PowerOff, Reboot,
// Halt, KExec, SoftReboot.
type Power interface {
	Shutdown(ctx context.Context, kind string, p ShutdownParams) error
}

// Handoffer implements io.tunaos.Fisherman.Handoff and decides its mode.
type Handoffer interface {
	// Handoff returns whether the live session keeps running afterwards.
	Handoff(ctx context.Context, mode string) (sessionKeepsRunning bool, err error)
	// DecideHandoff picks the takeover mode the daemon reports in Probe
	// and uses when Handoff arrives with no mode.
	DecideHandoff(ctx context.Context) (HandoffDecision, error)
}

// Failure reasons carried in the failed event. Stable: frontends key copy on
// them.
const (
	ReasonDeviceLost  = "device_lost"
	ReasonRepartError = "repart_failed"
	ReasonLUKSError   = "luks_failed"
	ReasonInternal    = "internal_error"
	// ReasonDiskTooSmall: the medium's partitions do not fit the disk.
	ReasonDiskTooSmall = "disk_too_small"
	// ReasonUnsupported: the config asks for something this executor does
	// not do (an unencrypted root, a TPM that is not there).
	ReasonUnsupported = "unsupported_config"
)

// Next actions carried in the failed event. Stable.
const (
	ActionChooseAnotherDisk = "choose_another_disk"
	ActionRetry             = "retry"
	ActionReportBug         = "report_bug"
	// ActionChangeSettings: go back and change the encryption or install mode.
	ActionChangeSettings = "change_settings"
)

// InstallError is a failed install: a reason and a next action for the user.
type InstallError struct {
	Step       string
	Reason     string
	Message    string
	NextAction string
}

func (e *InstallError) Error() string {
	return fmt.Sprintf("install failed at %s: %s: %s", e.Step, e.Reason, e.Message)
}

// Errors an Executor returns for the systemd interfaces.
var (
	ErrPasswordRequired   = errors.New("password required")
	ErrPasswordIncorrect  = errors.New("password incorrect")
	ErrVolumeForeign      = errors.New("volume under foreign management")
	ErrFidoDeviceNotFound = errors.New("fido device not found")
	ErrFidoTimeout        = errors.New("fido action timeout")
	// ErrNoDevice is ENODEV: the node is not a known device.
	ErrNoDevice = errors.New("no such device")
	// ErrAlreadyInProgress is io.systemd.Shutdown.AlreadyInProgress.
	ErrAlreadyInProgress = errors.New("shutdown already in progress")
	// ErrNotImplemented marks the placeholder Real executor.
	ErrNotImplemented = errors.New("real executor: not implemented")
)

// BlockedByInhibitor is io.systemd.Shutdown.BlockedByInhibitor.
type BlockedByInhibitor struct{ Who, Why string }

func (e *BlockedByInhibitor) Error() string { return "blocked by inhibitor: " + e.Who }
