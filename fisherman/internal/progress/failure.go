package progress

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Runtime failure codes of the `error` event (docs/JSON-PROGRESS-FORMAT.md,
// "Failure codes"). A failure that is a recipe rule or a preflight check
// carries the `fisherman validate` code instead (docs/VALIDATE.md). These
// name the operation that failed, not a guess at its cause: a pull failure
// is not a timeout, a cryptsetup failure is not a bad key. A code is never
// renamed or reused; a consumer that does not know one shows `message`.
const (
	// FailureUnknown: a failure with no more precise code.
	FailureUnknown = "unknown"
	// FailureCancelled: a signal, the death of the parent or a closed
	// stdout cancelled the install (exit 130).
	FailureCancelled = "cancelled"

	FailureLockFailed             = "lock_failed"
	FailureLocalSourceUnavailable = "local_source_unavailable"
	FailureManualLayoutFailed     = "manual_layout_failed"
	FailurePartitionFailed        = "partition_failed"
	FailureFormatFailed           = "format_failed"
	FailureLUKSFailed             = "luks_failed"
	FailureZFSFailed              = "zfs_failed"
	FailureMountFailed            = "mount_failed"
	FailureScratchFailed          = "scratch_failed"
	FailurePullFailed             = "pull_failed"
	FailureBootcInstallFailed     = "bootc_install_failed"
	FailureRetagFailed            = "retag_failed"
	FailurePersistentMountsFailed = "persistent_mounts_failed"
	FailureHostnameFailed         = "hostname_failed"
	FailureUserFailed             = "user_failed"
	FailureLUKSBootArgsFailed     = "luks_boot_args_failed"
	FailureFinalizeFailed         = "finalize_failed"
)

// Cleanup statuses of the `error` event's `cleanup` object.
const (
	// CleanupOK: the teardown released everything the install held.
	CleanupOK = "ok"
	// CleanupFailed: a part of the teardown failed; the target may still
	// be mounted or unlocked.
	CleanupFailed = "failed"
	// CleanupTimedOut: the teardown did not finish in time.
	CleanupTimedOut = "timed_out"
)

// Cleanup is the outcome of the teardown that ran before the `error`
// event.
type Cleanup struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// Failure is the content of the terminal `error` event.
type Failure struct {
	// Message is what failed, for people. It keeps the cleanup failure at
	// its end, as before, for consumers that read only the message.
	Message string
	// Code is the validate code of a recipe or preflight problem, else a
	// Failure* code. Empty is FailureUnknown.
	Code  string
	Field string
	// StepID is the step_id of the last step event, empty before the
	// first.
	StepID string
	// DiskChanged is true once a disk step has started: the target may be
	// partly changed, and the install is not safe to retry as if nothing
	// happened.
	DiskChanged bool
	// Cleanup is the teardown outcome; nil leaves it out.
	Cleanup *Cleanup
}

// Field bounds. A message is long enough for a command's last error line;
// nothing in the event is a whole recipe or a full command output.
const (
	maxMessage = 2048
	maxField   = 128
	// minSecret is the shortest registered secret Sanitize replaces. A
	// shorter one would match ordinary words of the message; no passphrase
	// that short protects anything.
	minSecret = 4
)

var codeRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

var (
	secretsMu sync.Mutex
	secrets   []string
)

// RegisterSecret adds s (a LUKS passphrase, a recovery key, a password) to
// what Sanitize removes from every `error` event and failure line.
func RegisterSecret(s string) {
	if len(s) < minSecret {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, have := range secrets {
		if have == s {
			return
		}
	}
	secrets = append(secrets, s)
}

// Sanitize replaces every registered secret in s with "[redacted]".
func Sanitize(s string) string {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}

// bound cuts s to at most n bytes on a rune boundary, marking the cut.
func bound(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// clean is f as the event carries it: secrets removed, fields bounded, and
// a code that is not one replaced with FailureUnknown.
func (f Failure) clean() Failure {
	f.Message = bound(Sanitize(f.Message), maxMessage)
	f.Field = bound(Sanitize(f.Field), maxField)
	f.StepID = bound(f.StepID, maxField)
	if !codeRe.MatchString(f.Code) {
		f.Code = FailureUnknown
	}
	if f.Cleanup != nil {
		c := *f.Cleanup
		c.Message = bound(Sanitize(c.Message), maxMessage)
		f.Cleanup = &c
	}
	return f
}

// Fail emits the terminal `error` event for f.
func Fail(f Failure) {
	f = f.clean()
	disk := f.DiskChanged
	write(errorEvent{Type: "error", Message: f.Message, Code: f.Code, Field: f.Field,
		StepID: f.StepID, DiskChanged: &disk, Cleanup: f.Cleanup})
}

// CurrentStep returns the step_id of the last step event, and whether a
// step event has been emitted at all.
func CurrentStep() (stepID string, started bool) {
	mu.Lock()
	defer mu.Unlock()
	return lastStepID, stepStarted
}
