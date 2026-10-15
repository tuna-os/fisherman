package main

// overall_pct over whole installs: every buildProfile combination main() can
// produce, replayed with the substeps fisherman emits in each step, must give
// a bar that never moves backwards, stays below 100 until complete and ends
// at 100. And every step name main() emits must have a step_id.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/tuna-os/fisherman/internal/progress"
)

// substepsFor is a representative substep stream for one step, in the order
// fisherman emits it.
func substepsFor(name string, pull bool) []string {
	switch name {
	case "Installing OS":
		var s []string
		if pull {
			s = append(s, "Pulling container image", "Pulling image: 7 layers to download")
			for i := 1; i <= 4; i++ {
				s = append(s, fmt.Sprintf("Pulling image: layer %d/7", i))
			}
			// A retried pull restarts its count.
			s = append(s, "Pull failed (attempt 1/3): EOF — retrying in 5s", "Pulling image: layer 1/7")
			for i := 2; i <= 7; i++ {
				s = append(s, fmt.Sprintf("Pulling image: layer %d/7", i))
			}
			s = append(s, "Pulling image: writing manifest", "Image pulled successfully")
		} else {
			s = append(s, "Image already up to date, skipping pull")
		}
		return append(s,
			"Exporting image to OCI layout for composefs install",
			"OCI export complete",
			"Using overlay storage driver with OCI layout",
			"Writing 64 (3.7 GB) to disk — this may take several minutes",
			"Initializing ostree layout",
			"Deploying image",
			"OS deployed, installing bootloader",
			"Detected bootloader",
			"Installing bootloader",
			"Configuring EFI boot entry",
			"Generating initramfs",
			"bootc installation complete")
	case "Copying system Flatpaks":
		s := []string{"Installing 3 per-image flatpak apps", "Found 3/3 wanted apps in system install",
			"Copying 3 Flatpak apps (1.2 GB)"}
		for p := 5; p <= 100; p += 5 {
			s = append(s, fmt.Sprintf("Copying Flatpak data: %d%%", p))
		}
		return append(s, "Copied 3 Flatpak apps")
	case "Configuring installed system":
		return []string{"Pre-generating wallpaper thumbnails", "Pre-warming system caches for first boot"}
	}
	return nil
}

func TestOverallPct_MonotonicAcrossProfiles(t *testing.T) {
	for _, l := range allLayouts() {
		name := fmt.Sprintf("pull=%v/manual=%v/luks=%v/tpm2=%v/var=%v", l.pull, l.manual, l.luks, l.tpm2, l.vd)
		t.Run(name, func(t *testing.T) {
			steps := emittedSteps(l)
			p := profileUnderTest(l)
			if len(p) != len(steps) {
				t.Fatalf("profile has %d slots for %d steps", len(p), len(steps))
			}
			tr := progress.NewTracker()
			last := 0.0
			check := func(what string, pct float64) {
				t.Helper()
				if pct < last {
					t.Errorf("%s: overall_pct went back from %v to %v", what, last, pct)
				}
				if pct >= 100 {
					t.Errorf("%s: overall_pct %v reached 100 before complete", what, pct)
				}
				last = pct
			}
			for i, s := range steps {
				if progress.StepID(s) == "" {
					t.Errorf("step %q has no step_id", s)
				}
				pct := tr.Step(i+1, p[i].cumulativePct, p[i].weightPct)
				if pct != float64(p[i].cumulativePct) {
					t.Errorf("step %q: overall_pct %v, want cumulative_pct %d", s, pct, p[i].cumulativePct)
				}
				check(s, pct)
				for _, msg := range substepsFor(s, l.pull) {
					check(s+": "+msg, tr.Substep(msg))
				}
				// The interpolated steps finish exactly where the next starts.
				if s == "Installing OS" || s == "Copying system Flatpaks" {
					if end := float64(p[i].cumulativePct + p[i].weightPct); last != end {
						t.Errorf("step %q ends at %v, want %v", s, last, end)
					}
				}
			}
			if got := tr.Complete(); got != 100 {
				t.Errorf("complete overall_pct = %v, want 100", got)
			}
		})
	}
}

// TestStepNamesHaveIDs parses main.go and checks that every literal step name
// passed to progress.Step is in progress's step_id table, so a new or renamed
// step cannot ship without one.
func TestStepNamesHaveIDs(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Step" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "progress" || len(call.Args) < 3 {
			return true
		}
		lit, ok := call.Args[2].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("progress.Step called with a non-literal step name; keep names literal so this test can check them")
			return true
		}
		name, _ := strconv.Unquote(lit.Value)
		found++
		if progress.StepID(name) == "" {
			t.Errorf("progress.Step(%q) has no step_id in internal/progress/bar.go", name)
		}
		return true
	})
	if found != len(progress.StepIDs()) {
		t.Errorf("main.go emits %d step names, the step_id table has %d", found, len(progress.StepIDs()))
	}
}
