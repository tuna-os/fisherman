package progress

import (
	"encoding/json"
	"fmt"
	"os"
)

type stepEvent struct {
	Type       string `json:"type"`
	Step       int    `json:"step"`
	TotalSteps int    `json:"total_steps"`
	StepName   string `json:"step_name"`
}

type infoEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type completeEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Step emits a JSON step-progress line to stdout.
func Step(step, total int, name string) {
	write(stepEvent{Type: "step", Step: step, TotalSteps: total, StepName: name})
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
	fmt.Fprintf(os.Stdout, "%s\n", data)
}
