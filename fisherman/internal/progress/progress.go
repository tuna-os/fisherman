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

type errorEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	// Code and Field are set when the failure is a recipe or preflight
	// problem with a stable code (docs/VALIDATE.md): the same code and
	// field `fisherman validate --json` reports for it.
	Code  string `json:"code,omitempty"`
	Field string `json:"field,omitempty"`
}

// Step emits a JSON step-progress line to stdout.
// cumulativePct is the bar position (0–100) at the start of this step.
// weightPct is the estimated share of total install time this step occupies.
// The event also carries step_id and overall_pct, derived here.
func Step(step, total int, name string, cumulativePct, weightPct int) {
	mu.Lock()
	defer mu.Unlock()
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
