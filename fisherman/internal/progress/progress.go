package progress

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

var startTime = time.Now()

type stepEvent struct {
	Type          string `json:"type"`
	Step          int    `json:"step"`
	TotalSteps    int    `json:"total_steps"`
	StepName      string `json:"step_name"`
	WeightPct     int    `json:"weight_pct"`
	CumulativePct int    `json:"cumulative_pct"`
}

type infoEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type completeEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type substepEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Step emits a JSON step-progress line to stdout.
// cumulativePct is the bar position (0–100) at the start of this step.
// weightPct is the estimated share of total install time this step occupies.
func Step(step, total int, name string, cumulativePct, weightPct int) {
	write(stepEvent{
		Type:          "step",
		Step:          step,
		TotalSteps:    total,
		StepName:      name,
		WeightPct:     weightPct,
		CumulativePct: cumulativePct,
	})
}

// Substep emits a JSON sub-step message within the current step (e.g. bootc internal progress).
func Substep(message string) {
	write(substepEvent{Type: "substep", Message: message})
}

// Info emits a JSON informational message to stdout.
func Info(message string) {
	write(infoEvent{Type: "info", Message: message})
}

// Complete emits the final JSON completion message to stdout.
func Complete(message string) {
	write(completeEvent{Type: "complete", Message: message})
}

func write(v any) {
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
	fmt.Fprintf(os.Stdout, "%s\n", data)
}
