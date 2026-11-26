// Package varlinkapi implements the fishermand Varlink services:
// io.systemd.SysInstall, io.systemd.CryptEnroll and io.systemd.Shutdown
// (wire-compatible with what hanthor/gnome-setup calls) and
// io.tunaos.Fisherman.
//
// Everything that touches a disk, a TPM or the power state sits behind the
// Executor interface. This slice ships a mock Executor and a placeholder for
// the real one.
package varlinkapi

import (
	"encoding/json"
	"log/slog"
)

// Secret is a string that never prints itself. fmt, slog and %v all show
// "[redacted]"; only JSON marshalling emits the value, because the wire is
// the one place the value must go.
type Secret string

func (Secret) String() string   { return "[redacted]" }
func (Secret) GoString() string { return `"[redacted]"` }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// UnmarshalJSON keeps Secret decodable from a plain JSON string.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = Secret(v)
	return nil
}

// Reveal returns the secret value. Call it only where the value is used.
func (s Secret) Reveal() string { return string(s) }

// ---- io.tunaos.Fisherman data model -------------------------------------

// Step ids. They are a public contract: frontends key translated labels on
// them. Never rename or reuse one. Ids that the legacy recipe pipeline already
// uses (internal/progress) keep the same meaning here.
const (
	StepValidateDevice = "validate_device"
	StepPartition      = "partition"
	StepLUKS           = "luks"
	StepTPM2Enroll     = "tpm2_enroll"
	StepMount          = "mount"
	StepInstallOS      = "install_os"
	StepConfigure      = "configure"
	StepPostFixups     = "post_fixups"
	StepBootloader     = "bootloader"
	StepFinalize       = "finalize"
	// StepCleanup runs after a failure or a cancel, never in a clean install.
	StepCleanup = "cleanup"
)

// StepIDs lists the steps of a clean install, in order.
var StepIDs = []string{
	StepValidateDevice, StepPartition, StepLUKS, StepTPM2Enroll, StepMount,
	StepInstallOS, StepConfigure, StepPostFixups, StepBootloader, StepFinalize,
}

type Gate struct {
	ID       string  `json:"id"`
	OK       bool    `json:"ok"`
	Severity string  `json:"severity"` // block | warn
	Required *string `json:"required,omitempty"`
	Actual   *string `json:"actual,omitempty"`
	Message  string  `json:"message"`
	Action   *string `json:"action,omitempty"` // power_off | firmware_setup
}

type Partition struct {
	Path      string  `json:"path"`
	FSType    *string `json:"fstype,omitempty"`
	Label     *string `json:"label,omitempty"`
	SizeBytes int64   `json:"sizeBytes"`
}

type ExistingData struct {
	HasPartitionTable  bool        `json:"hasPartitionTable"`
	PartitionTableType *string     `json:"partitionTableType,omitempty"`
	Partitions         []Partition `json:"partitions"`
	OperatingSystems   []string    `json:"operatingSystems"`
	HasLUKS            bool        `json:"hasLuks"`
}

type Disk struct {
	Path             string       `json:"path"`
	SizeBytes        int64        `json:"sizeBytes"`
	SizeLabel        string       `json:"sizeLabel"`
	Model            string       `json:"model"`
	Vendor           string       `json:"vendor"`
	Transport        string       `json:"transport"`
	Removable        bool         `json:"removable"`
	Eligible         bool         `json:"eligible"`
	ExcludedReason   *string      `json:"excludedReason,omitempty"`
	FreeBytes        int64        `json:"freeBytes"`
	MinimalSizeBytes int64        `json:"minimalSizeBytes"`
	IntelRST         bool         `json:"intelRst"`
	ExistingData     ExistingData `json:"existingData"`
	// ForeignLabel marks a partition table systemd-repart cannot extend. It
	// feeds io.systemd.SysInstall's conflicting_disk_label_present and is
	// not part of io.tunaos.Fisherman.
	ForeignLabel bool `json:"-"`
	// Symlinks and DiskSeq feed the SysInstall candidate list only.
	Symlinks []string `json:"-"`
	DiskSeq  int64    `json:"-"`
}

type Tpm struct {
	Present bool   `json:"present"`
	Version string `json:"version"`
	Usable  bool   `json:"usable"`
}

type IntelRST struct {
	Detected      bool     `json:"detected"`
	Mode          *string  `json:"mode,omitempty"`
	AffectedDisks []string `json:"affectedDisks"`
}

type Live struct {
	IsLive    bool    `json:"isLive"`
	LiveImage *string `json:"liveImage,omitempty"`
}

// ProbeResult is the Probe reply.
type ProbeResult struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Disks           []Disk   `json:"disks"`
	TPM             Tpm      `json:"tpm"`
	IntelRST        IntelRST `json:"intelRst"`
	Live            Live     `json:"live"`
	// Handoff is the daemon's takeover decision. Frontends show the
	// matching page and call Handoff with no mode.
	Handoff HandoffDecision `json:"handoff"`
	// Eula names the shared license file. No file means the frontend
	// skips the page.
	Eula Eula `json:"eula"`
}

// HandoffDecision is how the installed system takes over: the mode and
// why the daemon picked it.
type HandoffDecision struct {
	Mode string `json:"mode"` // migrate | soft_reboot | reboot
	// Reason is informational (btrfs-seed, el10, default).
	Reason string `json:"reason,omitempty"`
}

// Eula names the shared end-user license. TUNA_EULA_FILE overrides the
// default path; Available is false when no file is there.
type Eula struct {
	Available bool    `json:"available"`
	Path      *string `json:"path,omitempty"`
}

// Privacy holds the privacy choices. LocationServices defaults to false.
type Privacy struct {
	LocationServices bool `json:"locationServices,omitempty"`
}

// Accessibility holds the choices that a person made in the live session for
// their own use: they carry to each account of the installed system.
// ScalePercent is the display scale in percent (100 to 300); 0 leaves it.
type Accessibility struct {
	ScreenReader bool `json:"screenReader,omitempty"`
	ScalePercent int  `json:"scalePercent,omitempty"`
}

// RequirementsResult is the Requirements reply.
type RequirementsResult struct {
	MeetsRequirements bool   `json:"meetsRequirements"`
	Gates             []Gate `json:"gates"`
}

type ValidationError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type User struct {
	Username string  `json:"username"`
	FullName *string `json:"fullName,omitempty"`
	Password *Secret `json:"password,omitempty"`
	Admin    *bool   `json:"admin,omitempty"`
}

type Encryption struct {
	Type       string  `json:"type"` // none | luks_passphrase | tpm2_luks | tpm2_luks_passphrase
	Passphrase *Secret `json:"passphrase,omitempty"`
}

// Config is the install choice set shared by Validate and Install.
type Config struct {
	Disk        *string     `json:"disk,omitempty"`
	InstallMode *string     `json:"installMode,omitempty"` // erase | free_space
	Encryption  *Encryption `json:"encryption,omitempty"`
	User        *User       `json:"user,omitempty"`
	Hostname    *string     `json:"hostname,omitempty"`
}

// Event kinds of the Install stream.
const (
	EvStepStarted  = "step_started"
	EvProgress     = "progress"
	EvWarning      = "warning"
	EvStepFinished = "step_finished"
	EvFailed       = "failed"
	EvCancelled    = "cancelled"
	EvComplete     = "complete"
)

// Event is one reply of the Install stream.
type Event struct {
	Event      string  `json:"event"`
	StepID     *string `json:"stepId,omitempty"`
	StepIndex  *int    `json:"stepIndex,omitempty"`
	TotalSteps *int    `json:"totalSteps,omitempty"`
	OverallPct int     `json:"overallPct"`
	Message    *string `json:"message,omitempty"`
	Reason     *string `json:"reason,omitempty"`
	NextAction *string `json:"nextAction,omitempty"`

	// stepPct is the position inside the step; the SysInstall adapter uses it.
	stepPct int
}

// ---- io.systemd.* data model --------------------------------------------

// Device fit values of io.systemd.SysInstall.
const (
	FitEnough       = "enough_free_space"
	FitConflict     = "conflicting_disk_label_present"
	FitInsufficient = "insufficient_free_space"
	FitTooSmall     = "disk_too_small"
)

// CandidateDevice is one ListCandidateDevices reply. Pointer fields are
// omitted when nil, as systemd omits them.
type CandidateDevice struct {
	Node             *string  `json:"node,omitempty"`
	Symlinks         []string `json:"symlinks,omitempty"`
	DiskSeq          *int64   `json:"diskseq,omitempty"`
	SizeBytes        *int64   `json:"sizeBytes,omitempty"`
	Vendor           *string  `json:"vendor,omitempty"`
	Model            *string  `json:"model,omitempty"`
	Subsystem        *string  `json:"subsystem,omitempty"`
	Fit              *string  `json:"fit,omitempty"`
	MinimalSizeBytes *int64   `json:"minimalSizeBytes,omitempty"`
	CurrentSizeBytes *int64   `json:"currentSizeBytes,omitempty"`
	Action           *string  `json:"action,omitempty"`
}

// RunOutput is one Run reply.
type RunOutput struct {
	Phase    *string `json:"phase,omitempty"`
	Object   *string `json:"object,omitempty"`
	Progress *int64  `json:"progress,omitempty"`
}

// RunParams are the Run call parameters, as gnome-setup (zlink) names them.
type RunParams struct {
	Node            string   `json:"node"`
	Definitions     []string `json:"definitions"`
	Erase           bool     `json:"erase"`
	KernelImagePath *string  `json:"kernelImagePath"`
	Variables       bool     `json:"variables"`
	CopyLocale      *bool    `json:"copyLocale"`
	CopyKeymap      *bool    `json:"copyKeymap"`
	CopyTimezone    *bool    `json:"copyTimezone"`
}

// SysInstall progress phases.
const (
	PhaseValidateBlockDevice = "validate_block_device"
	PhaseLoadCredentials     = "load_credentials"
	PhaseEncryptCredentials  = "encrypt_credentials"
	PhaseInstallPartitions   = "install_partitions"
	PhaseMountPartitions     = "mount_partitions"
	PhaseInstallKernel       = "install_kernel"
	PhaseInstallBootloader   = "install_bootloader"
	PhaseUnmountPartitions   = "unmount_partitions"
)

// Crypt enroll mechanisms.
const (
	MechPassword = "password"
	MechRecovery = "recovery"
	MechPKCS11   = "pkcs11"
	MechFIDO2    = "fido2"
	MechTPM2     = "tpm2"
)

// EnrollParams are the Enroll call parameters.
type EnrollParams struct {
	Node           string  `json:"node"`
	Mechanism      string  `json:"mechanism"`
	RecoveryKey    *Secret `json:"recoveryKey"`
	UnlockPassword *Secret `json:"unlockPassword"`
	Password       *Secret `json:"password"`
	WipeSlots      []int64 `json:"wipeSlots"`
}

// EnrollResult is the Enroll reply.
type EnrollResult struct {
	Slot        *int64  `json:"slot,omitempty"`
	WipedSlots  []int64 `json:"wipedSlots,omitempty"`
	RecoveryKey *Secret `json:"recoveryKey,omitempty"`
	State       *string `json:"state,omitempty"`
}

// Slot is one keyslot of a LUKS2 volume.
type Slot struct {
	Slot int64  `json:"slot"`
	Type string `json:"type"`
}

// ShutdownParams are the parameters of every io.systemd.Shutdown method.
type ShutdownParams struct {
	AllowInteractiveAuthentication *bool `json:"allowInteractiveAuthentication"`
	SkipInhibitors                 *bool `json:"skipInhibitors"`
}

func ptr[T any](v T) *T { return &v }

// The types below carry secrets. slog's JSON handler marshals a struct it
// does not recognise with encoding/json, which would emit the Secret values
// (Secret redacts only when logged directly). A LogValue method makes the
// whole value safe to log by accident.

func (e Encryption) LogValue() slog.Value {
	return slog.GroupValue(slog.String("type", e.Type), slog.Bool("passphraseSet", e.Passphrase != nil))
}

func (u User) LogValue() slog.Value {
	return slog.GroupValue(slog.String("username", u.Username), slog.Bool("passwordSet", u.Password != nil))
}

func (c Config) LogValue() slog.Value {
	attrs := []slog.Attr{}
	if c.Disk != nil {
		attrs = append(attrs, slog.String("disk", *c.Disk))
	}
	if c.InstallMode != nil {
		attrs = append(attrs, slog.String("installMode", *c.InstallMode))
	}
	if c.Encryption != nil {
		attrs = append(attrs, slog.Any("encryption", *c.Encryption))
	}
	if c.User != nil {
		attrs = append(attrs, slog.Any("user", *c.User))
	}
	return slog.GroupValue(attrs...)
}

func (p EnrollParams) LogValue() slog.Value {
	return slog.GroupValue(slog.String("node", p.Node), slog.String("mechanism", p.Mechanism))
}

func (r EnrollResult) LogValue() slog.Value {
	return slog.GroupValue(slog.Bool("recoveryKeySet", r.RecoveryKey != nil))
}
