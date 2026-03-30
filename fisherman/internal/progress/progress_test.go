package progress_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/tuna-os/fisherman/internal/progress"
)

// captureStdout redirects os.Stdout for the duration of fn, returning the
// output as a string. Restores os.Stdout before returning.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

func TestStep(t *testing.T) {
	out := captureStdout(t, func() {
		progress.Step(2, 9, "Formatting EFI partition")
	})

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(out[:len(out)-1]), &event); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %q", err, out)
	}

	if event["type"] != "step" {
		t.Errorf("type = %v, want step", event["type"])
	}
	if event["step"] != float64(2) {
		t.Errorf("step = %v, want 2", event["step"])
	}
	if event["total_steps"] != float64(9) {
		t.Errorf("total_steps = %v, want 9", event["total_steps"])
	}
	if event["step_name"] != "Formatting EFI partition" {
		t.Errorf("step_name = %v, want 'Formatting EFI partition'", event["step_name"])
	}
}

func TestSubstep(t *testing.T) {
	out := captureStdout(t, func() {
		progress.Substep("Pulling container image")
	})

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(out[:len(out)-1]), &event); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %q", err, out)
	}

	if event["type"] != "substep" {
		t.Errorf("type = %v, want substep", event["type"])
	}
	if event["message"] != "Pulling container image" {
		t.Errorf("message = %v, want 'Pulling container image'", event["message"])
	}
}

func TestInfo(t *testing.T) {
	out := captureStdout(t, func() {
		progress.Info("Writing hostname: tunaos")
	})

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(out[:len(out)-1]), &event); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %q", err, out)
	}

	if event["type"] != "info" {
		t.Errorf("type = %v, want info", event["type"])
	}
	if event["message"] != "Writing hostname: tunaos" {
		t.Errorf("message = %v, want 'Writing hostname: tunaos'", event["message"])
	}
}

func TestComplete(t *testing.T) {
	out := captureStdout(t, func() {
		progress.Complete("Installation complete!")
	})

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(out[:len(out)-1]), &event); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %q", err, out)
	}

	if event["type"] != "complete" {
		t.Errorf("type = %v, want complete", event["type"])
	}
	if event["message"] != "Installation complete!" {
		t.Errorf("message = %v, want 'Installation complete!'", event["message"])
	}
}

// TestOutputIsNewlineTerminated ensures every emitter appends a newline,
// which the GUI relies on for line-by-line JSON parsing.
func TestOutputIsNewlineTerminated(t *testing.T) {
	fns := []struct {
		name string
		fn   func()
	}{
		{"Step", func() { progress.Step(1, 9, "x") }},
		{"Substep", func() { progress.Substep("x") }},
		{"Info", func() { progress.Info("x") }},
		{"Complete", func() { progress.Complete("x") }},
	}
	for _, f := range fns {
		t.Run(f.name, func(t *testing.T) {
			out := captureStdout(t, f.fn)
			if len(out) == 0 || out[len(out)-1] != '\n' {
				t.Errorf("%s output does not end with newline: %q", f.name, out)
			}
		})
	}
}
