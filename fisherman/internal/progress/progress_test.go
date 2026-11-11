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
		progress.Step(2, 8, "Formatting EFI partition", 0, 1)
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
	if event["total_steps"] != float64(8) {
		t.Errorf("total_steps = %v, want 8", event["total_steps"])
	}
	if event["step_name"] != "Formatting EFI partition" {
		t.Errorf("step_name = %v, want 'Formatting EFI partition'", event["step_name"])
	}
	if event["weight_pct"] != float64(1) {
		t.Errorf("weight_pct = %v, want 1", event["weight_pct"])
	}
	if event["cumulative_pct"] != float64(0) {
		t.Errorf("cumulative_pct = %v, want 0", event["cumulative_pct"])
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
		progress.Complete("Installation complete!", "")
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

func TestError(t *testing.T) {
	out := captureStdout(t, func() {
		progress.Error("partitioning disk: device busy")
	})

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(out[:len(out)-1]), &event); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %q", err, out)
	}

	if event["type"] != "error" {
		t.Errorf("type = %v, want error", event["type"])
	}
	if event["message"] != "partitioning disk: device busy" {
		t.Errorf("message = %v, want 'partitioning disk: device busy'", event["message"])
	}
}

// TestOutputIsNewlineTerminated ensures every emitter appends a newline,
// which the GUI relies on for line-by-line JSON parsing.
func TestOutputIsNewlineTerminated(t *testing.T) {
	fns := []struct {
		name string
		fn   func()
	}{
		{"Step", func() { progress.Step(1, 8, "x", 0, 0) }},
		{"Substep", func() { progress.Substep("x") }},
		{"Info", func() { progress.Info("x") }},
		{"Complete", func() { progress.Complete("x", "") }},
		{"Error", func() { progress.Error("x") }},
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

// testdata/receipt-events.ndjson holds one `receipt` event for each outcome
// (docs/RECEIPT.md). Each line is what Receipt writes for it, apart from
// timestamp and elapsed_ms, so a frontend's parser can be tested against
// the file.
func TestReceiptEvents(t *testing.T) {
	data, err := os.ReadFile("testdata/receipt-events.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{
		progress.ReceiptResolveFailed: false, progress.ReceiptBuildFailed: false,
		progress.ReceiptWriteFailed: false, progress.ReceiptTransientEtc: false,
		progress.ReceiptCollectorFailed: false,
	}
	allowed := map[string]bool{
		"type": true, "version": true, "written": true, "install_id": true, "path": true,
		"first_boot_check": true, "reason": true, "timestamp": true, "elapsed_ms": true,
	}
	sawOK := false
	for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var want map[string]any
		if err := json.Unmarshal(line, &want); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		var st progress.ReceiptStatus
		if err := json.Unmarshal(line, &st); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		out := captureStdout(t, func() { progress.Receipt(st) })
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("line %d: invalid JSON %q: %v", i+1, out, err)
		}
		for _, m := range []map[string]any{want, got} {
			delete(m, "timestamp")
			delete(m, "elapsed_ms")
		}
		if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
			t.Errorf("line %d:\n got %s\nwant %s", i+1, g, w)
		}
		for k := range got {
			if !allowed[k] {
				t.Errorf("line %d: field %q is not in the documented event", i+1, k)
			}
		}
		if got["type"] != "receipt" || got["version"] != float64(progress.ReceiptEventVersion) {
			t.Errorf("line %d: type/version %v/%v", i+1, got["type"], got["version"])
		}
		switch st.Reason {
		case "":
			if !st.Written || !st.FirstBootCheck {
				t.Errorf("line %d: no reason, but %+v", i+1, st)
			}
			sawOK = true
		default:
			if _, ok := codes[st.Reason]; !ok {
				t.Errorf("line %d: unknown reason %q", i+1, st.Reason)
			}
			codes[st.Reason] = true
			if st.FirstBootCheck {
				t.Errorf("line %d: first_boot_check with reason %q", i+1, st.Reason)
			}
		}
		if st.Written != (st.InstallID != "" && st.Path != "") {
			t.Errorf("line %d: install_id and path go with written: %+v", i+1, st)
		}
	}
	if !sawOK {
		t.Error("no line for a written receipt")
	}
	for code, seen := range codes {
		if !seen {
			t.Errorf("no line for reason %q", code)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
