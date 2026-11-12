package progress_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/progress"
)

type errorCases struct {
	Secrets []string         `json:"secrets"`
	Legacy  []map[string]any `json:"legacy"`
	Cases   []struct {
		Name    string           `json:"name"`
		Failure progress.Failure `json:"failure"`
		Event   map[string]any   `json:"event"`
	} `json:"cases"`
}

func loadErrorCases(t *testing.T) errorCases {
	t.Helper()
	raw, err := os.ReadFile("testdata/error-events.json")
	if err != nil {
		t.Fatal(err)
	}
	var c errorCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// testdata/error-events.json pins what progress.Fail writes (#369): the
// code, the step, whether a disk step had started, the cleanup outcome,
// and no registered secret anywhere.
func TestFailEvents(t *testing.T) {
	c := loadErrorCases(t)
	for _, s := range c.Secrets {
		progress.RegisterSecret(s)
	}
	allowed := map[string]bool{"type": true, "message": true, "code": true, "field": true, "step_id": true,
		"disk_changed": true, "cleanup": true, "timestamp": true, "elapsed_ms": true}
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			out := captureStdout(t, func() { progress.Fail(tc.Failure) })
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("invalid JSON %q: %v", out, err)
			}
			for k := range got {
				if !allowed[k] {
					t.Errorf("field %q is not in the documented event", k)
				}
			}
			delete(got, "timestamp")
			delete(got, "elapsed_ms")
			if g, w := mustJSON(t, got), mustJSON(t, tc.Event); g != w {
				t.Errorf("\n got %s\nwant %s", g, w)
			}
			for _, s := range c.Secrets {
				if strings.Contains(out, s) {
					t.Errorf("the event carries a secret: %s", out)
				}
			}
		})
	}
}

// A consumer reads every form: the legacy lines have a message and at most
// the validate code; the new ones add fields an old consumer ignores.
func TestErrorEventFormsShareTheOldFields(t *testing.T) {
	c := loadErrorCases(t)
	forms := c.Legacy
	for _, tc := range c.Cases {
		forms = append(forms, tc.Event)
	}
	for i, ev := range forms {
		if ev["type"] != "error" {
			t.Errorf("form %d: type %v", i, ev["type"])
		}
		if m, ok := ev["message"].(string); !ok || m == "" {
			t.Errorf("form %d: no message", i)
		}
	}
}

func TestFailBoundsTheMessage(t *testing.T) {
	long := strings.Repeat("é", 5000)
	out := captureStdout(t, func() {
		progress.Fail(progress.Failure{Message: long, Code: progress.FailureUnknown,
			Cleanup: &progress.Cleanup{Status: progress.CleanupFailed, Message: long}})
	})
	var got struct {
		Message string            `json:"message"`
		Cleanup *progress.Cleanup `json:"cleanup"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{got.Message, got.Cleanup.Message} {
		if len(m) > 2048 || !strings.HasSuffix(m, "…") || !strings.HasPrefix(m, "éé") {
			t.Errorf("message not bounded on a rune boundary: %d bytes, %q…", len(m), m[:10])
		}
	}
}

func TestShortSecretIsNotRegistered(t *testing.T) {
	progress.RegisterSecret("abc")
	if got := progress.Sanitize("abcdef"); got != "abcdef" {
		t.Errorf("Sanitize = %q; a 3-byte secret must not be replaced", got)
	}
}

func TestCurrentStep(t *testing.T) {
	_ = captureStdout(t, func() { progress.Step(1, 8, "Partitioning disk", 0, 1) })
	if id, started := progress.CurrentStep(); id != "partition" || !started {
		t.Errorf("CurrentStep = %q %v, want partition true", id, started)
	}
}
