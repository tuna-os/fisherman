package diagnostics

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/tuna-os/fisherman/internal/progress"
)

// Progress log limits.
const (
	// MaxProgressBytes is how much of a progress log the export reads: the
	// end of the log, where the failure is.
	MaxProgressBytes = 32 << 20
	// maxLine is the longest line the export decodes. A longer line is
	// counted as oversized and skipped.
	maxLine = 64 << 10
	// MaxTimeline is how many events the timeline keeps: the last ones.
	MaxTimeline = 500
	// maxMessage bounds an error or complete message, as the event does.
	maxMessage = 2048
)

// ProgressSummary is the progress log as the bundle carries it.
type ProgressSummary struct {
	// Timeline is the last MaxTimeline events, each with its allowlisted
	// fields only.
	Timeline []Event `json:"timeline"`
	// Counts is how many events of each known type the log has.
	Counts map[string]int `json:"counts"`
	// Unknown is how many events have a type this export does not know.
	// Their content is not in the bundle.
	Unknown int `json:"unknownEvents"`
	// Malformed is how many lines are not a JSON object (other output
	// mixed into the log, a cut line); Oversized how many are longer than
	// the export decodes. Neither is in the bundle.
	Malformed int `json:"malformedLines"`
	Oversized int `json:"oversizedLines"`
	// Dropped is how many events the timeline left out at its start;
	// Truncated is true when the export read only the end of the log.
	Dropped   int  `json:"droppedEvents"`
	Truncated bool `json:"truncated"`
	// Outcome is the end of the install the log shows.
	Outcome Outcome `json:"outcome"`
}

// Event is one timeline entry. Which fields are set depends on Type.
type Event struct {
	Type string `json:"type"`
	// step
	Step       int      `json:"step,omitempty"`
	TotalSteps int      `json:"total_steps,omitempty"`
	StepID     string   `json:"step_id,omitempty"`
	OverallPct *float64 `json:"overall_pct,omitempty"`
	// Messages is how many info and substep events followed the step
	// before the next step. Their text is not in the bundle.
	Messages int `json:"messages,omitempty"`
	// error and complete
	Message     string        `json:"message,omitempty"`
	Code        string        `json:"code,omitempty"`
	Field       string        `json:"field,omitempty"`
	DiskChanged *bool         `json:"disk_changed,omitempty"`
	Cleanup     *CleanupShape `json:"cleanup,omitempty"`
	// receipt
	Version        int    `json:"version,omitempty"`
	Written        *bool  `json:"written,omitempty"`
	FirstBootCheck *bool  `json:"first_boot_check,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// CleanupShape is the error event's cleanup object.
type CleanupShape struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// Outcome is how the install the log shows ended.
type Outcome struct {
	// State is "complete", "error", or "unfinished" (no terminal event:
	// the log is cut, or the install is still running or was killed).
	State       string `json:"state"`
	Code        string `json:"code,omitempty"`
	StepID      string `json:"step_id,omitempty"`
	DiskChanged *bool  `json:"disk_changed,omitempty"`
	Cleanup     string `json:"cleanup,omitempty"`
}

// knownEvents are the event types the export reads.
var knownEvents = map[string]bool{
	"step": true, "info": true, "substep": true, "complete": true,
	"error": true, "recovery_key": true, "receipt": true,
}

// stepIDRe is the shape of a step_id.
var stepIDRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// rawEvent is every field the export reads from an event line. A field
// that is not here is never decoded.
type rawEvent struct {
	Type       string   `json:"type"`
	Step       int      `json:"step"`
	TotalSteps int      `json:"total_steps"`
	StepID     string   `json:"step_id"`
	OverallPct *float64 `json:"overall_pct"`
	Message    string   `json:"message"`
	Code       string   `json:"code"`
	Field      string   `json:"field"`
	Disk       *bool    `json:"disk_changed"`
	Cleanup    *struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"cleanup"`
	Version        int    `json:"version"`
	Written        *bool  `json:"written"`
	FirstBootCheck *bool  `json:"first_boot_check"`
	Reason         string `json:"reason"`
}

// splitLog returns the lines of a log.
func splitLog(data []byte) [][]byte {
	return bytes.Split(data, []byte("\n"))
}

// decodeEvents decodes each line that is a JSON object and counts the
// lines it does not read.
func decodeEvents(lines [][]byte, sum *ProgressSummary) []*rawEvent {
	out := make([]*rawEvent, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if len(line) > maxLine {
			sum.Oversized++
			continue
		}
		var ev rawEvent
		if line[0] != '{' || json.Unmarshal(line, &ev) != nil {
			sum.Malformed++
			continue
		}
		out = append(out, &ev)
	}
	return out
}

// progressSecrets returns the recovery keys of a log, for the Redactor,
// so a key the log repeats in a message goes too.
func progressSecrets(data []byte) []string {
	var keys []string
	for _, line := range splitLog(data) {
		if len(line) > maxLine || !bytes.Contains(line, []byte(`"recovery_key"`)) {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		}
		if json.Unmarshal(bytes.TrimSpace(line), &ev) == nil && ev.Key != "" {
			keys = append(keys, ev.Key)
		}
	}
	return keys
}

// summarizeProgress returns the summary of a progress log. truncated says
// the caller read only its end; the first line is then a partial one and
// is dropped.
func summarizeProgress(data []byte, truncated bool, rd *Redactor) ProgressSummary {
	sum := ProgressSummary{Counts: map[string]int{}, Truncated: truncated, Timeline: []Event{}}
	lines := splitLog(data)
	if truncated && len(lines) > 0 {
		lines = lines[1:]
	}
	var timeline []Event
	lastStep := -1 // index of the last step event in timeline
	sum.Outcome.State = "unfinished"
	for _, ev := range decodeEvents(lines, &sum) {
		if !knownEvents[ev.Type] {
			sum.Unknown++
			continue
		}
		sum.Counts[ev.Type]++
		switch ev.Type {
		case "info", "substep":
			if lastStep >= 0 {
				timeline[lastStep].Messages++
			}
			continue
		case "step":
			lastStep = len(timeline)
			timeline = append(timeline, Event{Type: "step", Step: ev.Step, TotalSteps: ev.TotalSteps,
				StepID: token(ev.StepID, stepIDRe), OverallPct: ev.OverallPct})
		case "recovery_key":
			// The key itself is a secret: only that one was shown.
			timeline = append(timeline, Event{Type: "recovery_key"})
		case "receipt":
			timeline = append(timeline, Event{Type: "receipt", Version: ev.Version, Written: ev.Written,
				FirstBootCheck: ev.FirstBootCheck, Reason: token(ev.Reason, codeRe)})
		case "complete":
			timeline = append(timeline, Event{Type: "complete", Message: rd.Message(ev.Message, maxMessage), OverallPct: ev.OverallPct})
			sum.Outcome = Outcome{State: "complete"}
		case "error":
			e := Event{Type: "error", Message: rd.Message(ev.Message, maxMessage),
				Code: token(ev.Code, codeRe), Field: token(ev.Field, fieldRe),
				StepID: token(ev.StepID, stepIDRe), DiskChanged: ev.Disk}
			if ev.Cleanup != nil {
				e.Cleanup = &CleanupShape{
					Status:  enum(ev.Cleanup.Status, progress.CleanupOK, progress.CleanupFailed, progress.CleanupTimedOut),
					Message: rd.Message(ev.Cleanup.Message, maxMessage),
				}
			}
			timeline = append(timeline, e)
			sum.Outcome = Outcome{State: "error", Code: e.Code, StepID: e.StepID, DiskChanged: e.DiskChanged}
			if e.Code == "" {
				sum.Outcome.Code = progress.FailureUnknown
			}
			if e.Cleanup != nil {
				sum.Outcome.Cleanup = e.Cleanup.Status
			}
		}
	}
	if len(timeline) > MaxTimeline {
		sum.Dropped = len(timeline) - MaxTimeline
		timeline = timeline[len(timeline)-MaxTimeline:]
	}
	if timeline != nil {
		sum.Timeline = timeline
	}
	return sum
}
