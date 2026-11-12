package progress

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

var startTime = time.Now()

// mu serialises event emission. Substeps arrive from more than one goroutine
// (the bootc output relay, the Flatpak copy reporter), and overall_pct is
// computed and written under the same lock so it is monotonic on the wire,
// not just in the tracker.
var (
	mu  sync.Mutex
	bar = NewTracker()
	// lastStepID and stepStarted record the last step event (CurrentStep).
	lastStepID  string
	stepStarted bool
)

type stepEvent struct {
	Type          string `json:"type"`
	Step          int    `json:"step"`
	TotalSteps    int    `json:"total_steps"`
	StepName      string `json:"step_name"`
	WeightPct     int    `json:"weight_pct"`
	CumulativePct int    `json:"cumulative_pct"`
	// StepID is the stable id for StepName (see stepIDs); omitted for a name
	// that is not in the table.
	StepID string `json:"step_id,omitempty"`
	// OverallPct is the bar position, 0-100 (see Tracker).
	OverallPct float64 `json:"overall_pct"`
}

type infoEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type completeEvent struct {
	Type       string  `json:"type"`
	Message    string  `json:"message"`
	BootID     string  `json:"boot_id,omitempty"`
	OverallPct float64 `json:"overall_pct"`
}

type substepEvent struct {
	Type       string  `json:"type"`
	Message    string  `json:"message"`
	OverallPct float64 `json:"overall_pct"`
}

type recoveryKeyEvent struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

// ReceiptEventVersion is the version of the `receipt` event. Fields and
// reason codes are only ever added; a new version means a field changed
// meaning.
const ReceiptEventVersion = 1

// Reason codes of the `receipt` event (docs/RECEIPT.md). A code is never
// renamed or reused.
const (
	// ReceiptResolveFailed: the installed deployment was not found, so
	// nothing was written.
	ReceiptResolveFailed = "resolve_failed"
	// ReceiptBuildFailed: the receipt could not be built (no install id),
	// so nothing was written.
	ReceiptBuildFailed = "build_failed"
	// ReceiptWriteFailed: the receipt file could not be written.
	ReceiptWriteFailed = "write_failed"
	// ReceiptTransientEtc: the receipt is written, but the image's /etc is
	// transient, so the first-boot collector is not staged.
	ReceiptTransientEtc = "transient_etc"
	// ReceiptCollectorFailed: the receipt is written, but staging the
	// first-boot collector in /etc failed.
	ReceiptCollectorFailed = "collector_failed"
)

// ReceiptStatus is what the install did with the installation receipt
// (docs/RECEIPT.md). It holds nothing the receipt does not hold.
type ReceiptStatus struct {
	// Written is true when the receipt file is on the installed system.
	Written bool `json:"written"`
	// InstallID and Path are set only when Written.
	InstallID string `json:"install_id,omitempty"`
	Path      string `json:"path,omitempty"`
	// FirstBootCheck is true when the first-boot collector is staged.
	FirstBootCheck bool `json:"first_boot_check"`
	// Reason is one of the Receipt* codes; empty when the receipt is
	// written and the collector staged.
	Reason string `json:"reason,omitempty"`
}

type receiptEvent struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	ReceiptStatus
}

type errorEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	// Code is the `fisherman validate --json` code of a recipe or
	// preflight problem (docs/VALIDATE.md), else a runtime Failure* code.
	// Field is the recipe field of a validate code.
	Code  string `json:"code,omitempty"`
	Field string `json:"field,omitempty"`
	// StepID is the step that was running; DiskChanged whether any disk
	// step had started; Cleanup what the teardown before the event did.
	// Set by Fail.
	StepID      string   `json:"step_id,omitempty"`
	DiskChanged *bool    `json:"disk_changed,omitempty"`
	Cleanup     *Cleanup `json:"cleanup,omitempty"`
}

// Step emits a JSON step-progress line to stdout.
// cumulativePct is the bar position (0–100) at the start of this step.
// weightPct is the estimated share of total install time this step occupies.
// The event also carries step_id and overall_pct, derived here.
func Step(step, total int, name string, cumulativePct, weightPct int) {
	mu.Lock()
	defer mu.Unlock()
	lastStepID, stepStarted = StepID(name), true
	writeLocked(stepEvent{
		Type:          "step",
		Step:          step,
		TotalSteps:    total,
		StepName:      name,
		WeightPct:     weightPct,
		CumulativePct: cumulativePct,
		StepID:        StepID(name),
		OverallPct:    roundPct(bar.Step(step, cumulativePct, weightPct)),
	})
}

// SubstepFn is called by Substep in addition to writing JSON. Tests can
// replace it to capture substep messages without parsing stdout.
var SubstepFn func(string)

// Substep emits a JSON sub-step message within the current step (e.g. bootc internal progress).
func Substep(message string) {
	if SubstepFn != nil {
		SubstepFn(message)
	}
	mu.Lock()
	defer mu.Unlock()
	writeLocked(substepEvent{
		Type:       "substep",
		Message:    message,
		OverallPct: roundPct(bar.Substep(message)),
	})
}

// Info emits a JSON informational message to stdout.
func Info(message string) {
	write(infoEvent{Type: "info", Message: message})
}

// Complete emits the final JSON completion message to stdout.
// bootID is the 4-digit EFI boot entry number (e.g. "0001"); pass an empty
// string when it is unknown so the field is omitted from the JSON.
// overall_pct is always 100.
func Complete(message, bootID string) {
	mu.Lock()
	defer mu.Unlock()
	writeLocked(completeEvent{
		Type:       "complete",
		Message:    message,
		BootID:     bootID,
		OverallPct: roundPct(bar.Complete()),
	})
}

// RecoveryKey emits the LUKS recovery passphrase for `tpm2-luks` installs.
// The GUI displays this key so the user can write it down before rebooting.
// Only emit when a random (non-user-chosen) passphrase is the sole fallback.
func RecoveryKey(key string) {
	write(recoveryKeyEvent{Type: "recovery_key", Key: key})
}

// Receipt emits the `receipt` event: whether the installation receipt was
// written, and whether the first-boot check is staged. It is informational;
// the install goes on whatever it says.
func Receipt(st ReceiptStatus) {
	write(receiptEvent{Type: "receipt", Version: ReceiptEventVersion, ReceiptStatus: st})
}

// Error emits a JSON terminal-failure event to stdout. Callers still write a
// plain-text summary to stderr for terminal/log visibility; this event lets
// consumers that parse the stdout protocol (GUIs, install logs) capture the
// same failure programmatically instead of only seeing an unstructured
// stderr line.
func Error(message string) {
	write(errorEvent{Type: "error", Message: message})
}

// ErrorWithCode is Error for a failure that has a stable problem code: a
// recipe rule or a preflight check. code and field are the ones
// `fisherman validate --json` gives the same problem; an empty value is
// left out of the event.
func ErrorWithCode(message, code, field string) {
	write(errorEvent{Type: "error", Message: message, Code: code, Field: field})
}

func write(v any) {
	mu.Lock()
	defer mu.Unlock()
	writeLocked(v)
}

// writeLocked writes one event line; the caller holds mu.
func writeLocked(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "progress: marshal error: %v\n", err)
		return
	}
	// Inject timestamp and elapsed_ms into every event without touching each
	// struct individually. Key order is alphabetical (Go map marshaling).
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		fmt.Fprintf(os.Stderr, "progress: unmarshal error: %v\n", err)
		return
	}
	m["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	m["elapsed_ms"] = time.Since(startTime).Milliseconds()
	data, err = json.Marshal(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "progress: marshal error: %v\n", err)
		return
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s\n", data); err != nil && errors.Is(err, syscall.EPIPE) {
		reportBrokenPipe()
	}
}

// Broken-pipe handling. The reader of the event stream can go away while
// fisherman is still running: the frontend crashes, or `fisherman … | tee
// log` loses its tee. Every later event write then fails with EPIPE. The
// installer cannot report anything any more, so going on would be an
// unattended install; the handler set here cancels it instead.
var (
	brokenPipeMu      sync.Mutex
	brokenPipeHandler func()
	brokenPipeSeen    bool
)

// OnBrokenPipe sets the function called, once and on its own goroutine, the
// first time an event cannot be written because the reader of stdout has
// gone (EPIPE). The process must have SIGPIPE handled (signal.Notify), or Go
// ends it on that write before any handler can run.
func OnBrokenPipe(handler func()) {
	brokenPipeMu.Lock()
	defer brokenPipeMu.Unlock()
	brokenPipeHandler = handler
	brokenPipeSeen = false
}

func reportBrokenPipe() {
	brokenPipeMu.Lock()
	handler := brokenPipeHandler
	first := !brokenPipeSeen
	brokenPipeSeen = true
	brokenPipeMu.Unlock()
	if handler != nil && first {
		// Not on this goroutine: the caller holds mu, and the handler
		// emits an event of its own.
		go handler()
	}
}
