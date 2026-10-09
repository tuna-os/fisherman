package progress

// Golden tests for overall_pct.
//
// testdata/fraction-cases.json and testdata/dry-run-transcript.ndjson are
// copied verbatim from tuna-os/bootc-installer, shared/progress/ on dev
// (bootc-installer commit 7cb536cf), where generate-fraction-cases.py writes
// the cases from the canonical parser, progress_parser.py. Keep them
// byte-identical with that repository: refresh with
//
//	git -C <bootc-installer> show origin/dev:shared/progress/fraction-cases.json \
//	    > internal/progress/testdata/fraction-cases.json
//
// testdata/fraction-cases-fisherman.json holds fisherman's own additions (the
// Flatpak copy interpolation and the cap below 100), written by hand.

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"regexp"
	"testing"
)

type goldenEvent struct {
	Type          string `json:"type"`
	Step          int    `json:"step"`
	TotalSteps    int    `json:"total_steps"`
	StepName      string `json:"step_name"`
	CumulativePct int    `json:"cumulative_pct"`
	WeightPct     int    `json:"weight_pct"`
	Message       string `json:"message"`
	BootID        string `json:"boot_id"`
	Key           string `json:"key"`
}

type goldenCase struct {
	Name   string        `json:"name"`
	Events []goldenEvent `json:"events"`
	Bar    []float64     `json:"bar"`
}

func loadCases(t *testing.T, path string) []goldenCase {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Cases []goldenCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(doc.Cases) == 0 {
		t.Fatalf("%s has no cases", path)
	}
	for _, c := range doc.Cases {
		if len(c.Events) != len(c.Bar) {
			t.Fatalf("%s: case %s has %d events and %d bar values", path, c.Name, len(c.Events), len(c.Bar))
		}
	}
	return doc.Cases
}

func allCases(t *testing.T) []goldenCase {
	return append(loadCases(t, "testdata/fraction-cases.json"),
		loadCases(t, "testdata/fraction-cases-fisherman.json")...)
}

// apply feeds one event to a Tracker the way the emitters do.
func apply(tr *Tracker, e goldenEvent) {
	switch e.Type {
	case "step":
		tr.Step(e.Step, e.CumulativePct, e.WeightPct)
	case "substep":
		tr.Substep(e.Message)
	case "complete":
		tr.Complete()
	}
}

// The fraction-cases tolerance the frontends use, in bar fraction (0-1).
const fractionTol = 1e-6

// TestTracker_Golden: the Tracker reproduces the reference parser's bar after
// every event of every case, plus fisherman's own cases.
func TestTracker_Golden(t *testing.T) {
	for _, c := range allCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			tr := NewTracker()
			for i, e := range c.Events {
				apply(tr, e)
				if got := tr.Percent() / 100; math.Abs(got-c.Bar[i]) > fractionTol {
					t.Errorf("event %d (%s %q%q): bar = %.6f, want %.6f",
						i, e.Type, e.StepName, e.Message, got, c.Bar[i])
				}
			}
		})
	}
}

// emit sends one event through the package-level emitters and returns the
// decoded line.
func emit(t *testing.T, e goldenEvent) map[string]any {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	switch e.Type {
	case "step":
		Step(e.Step, e.TotalSteps, e.StepName, e.CumulativePct, e.WeightPct)
	case "substep":
		Substep(e.Message)
	case "complete":
		Complete(e.Message, e.BootID)
	case "recovery_key":
		RecoveryKey(e.Key)
	case "info":
		Info(e.Message)
	}
	w.Close()
	os.Stdout = old
	var m map[string]any
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		t.Fatalf("no output for %+v", e)
	}
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", sc.Text(), err)
	}
	if sc.Scan() {
		t.Fatalf("more than one line for %+v", e)
	}
	r.Close()
	return m
}

// TestEmitted_Golden: the overall_pct that goes on the wire matches the golden
// bar to the emitted precision (two decimals), on step, substep and complete.
func TestEmitted_Golden(t *testing.T) {
	for _, c := range allCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			resetBar()
			for i, e := range c.Events {
				m := emit(t, e)
				got, ok := m["overall_pct"].(float64)
				if !ok {
					t.Fatalf("event %d (%s): no numeric overall_pct in %v", i, e.Type, m)
				}
				if want := c.Bar[i] * 100; math.Abs(got-want) > 0.005+1e-9 {
					t.Errorf("event %d (%s %q%q): overall_pct = %v, want %.4f", i, e.Type, e.StepName, e.Message, got, want)
				}
				if got != math.Round(got*100)/100 {
					t.Errorf("event %d: overall_pct %v has more than two decimals", i, got)
				}
			}
		})
	}
}

// TestEmitted_Transcript replays bootc-installer's dry-run transcript (which
// is fisherman's own output) through the emitters: every step carries a
// step_id and starts the bar at its cumulative_pct, the bar never moves back,
// and complete is 100.
func TestEmitted_Transcript(t *testing.T) {
	resetBar()
	f, err := os.Open("testdata/dry-run-transcript.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	last, n := -1.0, 0
	for sc.Scan() {
		var e goldenEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("transcript line %d: %v", n+1, err)
		}
		n++
		m := emit(t, e)
		pct, _ := m["overall_pct"].(float64)
		if pct < last {
			t.Errorf("line %d: overall_pct went back from %v to %v", n, last, pct)
		}
		last = pct
		switch e.Type {
		case "step":
			if m["step_id"] == nil || m["step_id"] == "" {
				t.Errorf("line %d: step %q has no step_id", n, e.StepName)
			}
			if pct != float64(e.CumulativePct) {
				t.Errorf("line %d: step %q overall_pct = %v, want cumulative_pct %d", n, e.StepName, pct, e.CumulativePct)
			}
			// Every pre-existing field is still there, unchanged.
			for k, want := range map[string]any{
				"step": float64(e.Step), "total_steps": float64(e.TotalSteps), "step_name": e.StepName,
				"cumulative_pct": float64(e.CumulativePct), "weight_pct": float64(e.WeightPct),
			} {
				if m[k] != want {
					t.Errorf("line %d: %s = %v, want %v", n, k, m[k], want)
				}
			}
		case "complete":
			if pct != 100 {
				t.Errorf("complete overall_pct = %v, want 100", pct)
			}
			if m["boot_id"] != e.BootID {
				t.Errorf("complete boot_id = %v, want %q", m["boot_id"], e.BootID)
			}
		default:
			if pct >= 100 {
				t.Errorf("line %d: overall_pct %v reached 100 before complete", n, pct)
			}
		}
	}
	if n == 0 {
		t.Fatal("empty transcript")
	}
}

// TestStepIDs pins the id table. The ids are a public contract that
// frontends key their labels on: changing one here is a breaking change.
func TestStepIDs(t *testing.T) {
	want := map[string]string{
		"Preparing disk":               "prepare_disk",
		"Partitioning disk":            "partition",
		"Formatting EFI partition":     "format_efi",
		"Setting up disk encryption":   "luks",
		"Formatting root filesystem":   "format_root",
		"Mounting filesystem":          "mount",
		"Formatting data disk (/var)":  "format_var",
		"Installing OS":                "install_os",
		"Enrolling TPM2 auto-unlock":   "tpm2_enroll",
		"Copying system Flatpaks":      "flatpaks",
		"Configuring installed system": "configure",
		"Finalizing installation":      "finalize",
	}
	got := StepIDs()
	if len(got) != len(want) {
		t.Errorf("table has %d ids, want %d", len(got), len(want))
	}
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	seen := map[string]string{}
	for name, id := range got {
		if want[name] != id {
			t.Errorf("StepID(%q) = %q, want %q", name, id, want[name])
		}
		if !snake.MatchString(id) {
			t.Errorf("step_id %q is not snake_case", id)
		}
		if other, dup := seen[id]; dup {
			t.Errorf("step_id %q used by both %q and %q", id, name, other)
		}
		seen[id] = name
	}
	if StepID("Some future step") != "" {
		t.Error("unknown step names must map to no id")
	}
}

// TestEmitted_UnknownStepOmitsID: a step not in the table still emits, with
// overall_pct, but without a made-up step_id.
func TestEmitted_UnknownStepOmitsID(t *testing.T) {
	resetBar()
	m := emit(t, goldenEvent{Type: "step", Step: 1, TotalSteps: 1, StepName: "Some future step", CumulativePct: 5})
	if _, ok := m["step_id"]; ok {
		t.Errorf("step_id = %v, want it omitted", m["step_id"])
	}
	if m["overall_pct"] != float64(5) {
		t.Errorf("overall_pct = %v, want 5", m["overall_pct"])
	}
}

// TestTracker_NeverDecreases: a step whose cumulative_pct is below the bar
// (which a correct profile never emits) does not drag it back.
func TestTracker_NeverDecreases(t *testing.T) {
	tr := NewTracker()
	tr.Step(1, 0, 50)
	tr.Substep("Copying Flatpak data: 80%")
	if got := tr.Step(2, 10, 10); got != 40 {
		t.Errorf("bar after a lower cumulative = %v, want 40", got)
	}
	tr.Complete()
	if got := tr.Step(3, 0, 0); got != 100 {
		t.Errorf("bar after complete = %v, want 100", got)
	}
}

// TestEmitted_ConcurrentSubsteps: substeps come from several goroutines in a
// real install. Run with -race.
func TestEmitted_ConcurrentSubsteps(t *testing.T) {
	resetBar()
	old := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = devnull
	defer func() { os.Stdout = old; devnull.Close() }()
	Step(1, 1, "Copying system Flatpaks", 0, 50)
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func() {
			for i := 0; i <= 100; i++ {
				Substep("Copying Flatpak data: 50%")
				Info("x")
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	mu.Lock()
	got := bar.Percent()
	mu.Unlock()
	if got != 25 {
		t.Errorf("bar = %v, want 25", got)
	}
}

// resetBar puts the package-level tracker back at 0% so emission tests do not
// depend on the order the tests run in.
func resetBar() {
	mu.Lock()
	defer mu.Unlock()
	bar = NewTracker()
}
