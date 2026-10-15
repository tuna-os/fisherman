package main

// Layout-aware tests for buildProfile. main() walks the profile by index
// (profile[pi], pi++ after every progress.Step), so the profile is only
// correct if its slots line up one-to-one with the steps main() actually
// emits for the recipe. These tests pair the two and check the result.

import (
	"os"
	"reflect"
	"regexp"
	"testing"
)

// layout describes the recipe shape that decides which steps main() emits.
type layout struct {
	name                         string
	pull, manual, luks, tpm2, vd bool
}

// emittedSteps mirrors the control flow of main(): the names of the
// progress.Step calls it makes, in order, for a given recipe shape.
//   - manual layouts emit one "Preparing disk" step in place of
//     partition/EFI/LUKS/root/mount;
//   - LUKS setup runs only on the auto path;
//   - TPM2 enrolment needs activeRootPart, which manual mode leaves empty;
//   - a /var disk that is not kept gets its own format step.
func emittedSteps(l layout) []string {
	var s []string
	if l.manual {
		s = append(s, "Preparing disk")
	} else {
		s = append(s, "Partitioning disk", "Formatting EFI partition")
		if l.luks {
			s = append(s, "Setting up disk encryption")
		}
		s = append(s, "Formatting root filesystem", "Mounting filesystem")
	}
	if l.vd {
		s = append(s, "Formatting data disk (/var)")
	}
	s = append(s, "Installing OS")
	if l.tpm2 && !l.manual {
		s = append(s, "Enrolling TPM2 auto-unlock")
	}
	return append(s, "Copying system Flatpaks", "Configuring installed system", "Finalizing installation")
}

// profileUnderTest builds the profile main() would build for the layout.
func profileUnderTest(l layout) []stepProfile {
	return buildProfile(l.pull, l.manual, l.luks, l.tpm2, l.vd)
}

func allLayouts() []layout {
	var out []layout
	for _, pull := range []bool{true, false} {
		for _, manual := range []bool{false, true} {
			for _, luks := range []bool{false, true} {
				for _, tpm2 := range []bool{false, true} {
					for _, vd := range []bool{false, true} {
						out = append(out, layout{"", pull, manual, luks, tpm2, vd})
					}
				}
			}
		}
	}
	return out
}

// TestBuildProfile_MatchesEmittedSequence pairs every emitted step with the
// profile slot main() would read for it and checks the bar is sane: one slot
// per step, "Installing OS" carries the dominant weight, cumulative positions
// are contiguous and non-decreasing, the last step starts at <= 99 and the
// weights reach exactly 100.
func TestBuildProfile_MatchesEmittedSequence(t *testing.T) {
	for _, l := range allLayouts() {
		steps := emittedSteps(l)
		p := profileUnderTest(l)
		if len(p) != len(steps) {
			t.Errorf("%+v: profile has %d slots, main() emits %d steps %v", l, len(p), len(steps), steps)
			if len(p) < len(steps) {
				continue
			}
			// main() reads slots by index, so judge what it would emit.
			p = p[:len(steps)]
		}
		osIdx := -1
		for i, name := range steps {
			if name == "Installing OS" {
				osIdx = i
			}
		}
		wantOS := 87
		if !l.pull {
			wantOS = 68
		}
		if !l.manual && l.luks {
			wantOS--
		}
		if !l.manual && l.tpm2 {
			wantOS--
		}
		if got := p[osIdx].weightPct; got != wantOS {
			t.Errorf("%+v: \"Installing OS\" weight = %d, want %d (profile %+v)", l, got, wantOS, p)
		}
		if p[0].cumulativePct != 0 {
			t.Errorf("%+v: first step starts at %d, want 0", l, p[0].cumulativePct)
		}
		for i := 1; i < len(p); i++ {
			if want := p[i-1].cumulativePct + p[i-1].weightPct; p[i].cumulativePct != want {
				t.Errorf("%+v: step %d (%s) starts at %d, want %d", l, i, steps[i], p[i].cumulativePct, want)
			}
		}
		last := p[len(p)-1]
		if last.cumulativePct > 99 {
			t.Errorf("%+v: last step starts at %d, want <= 99", l, last.cumulativePct)
		}
		if osIdx >= 0 && last.cumulativePct < p[osIdx].cumulativePct+p[osIdx].weightPct {
			t.Errorf("%+v: last step starts at %d, before \"Installing OS\" ends", l, last.cumulativePct)
		}
		if last.cumulativePct+last.weightPct != 100 {
			t.Errorf("%+v: weights reach %d, want 100", l, last.cumulativePct+last.weightPct)
		}
	}
}

// TestBuildProfile_ManualLayout pins the exact manual-layout profiles. The
// "Preparing disk" step stands in for partition+EFI+root+mount and so takes
// their combined weight (0+1+0+0); everything after it keeps the auto weights.
func TestBuildProfile_ManualLayout(t *testing.T) {
	cases := []struct {
		l    layout
		want []int
	}{
		{layout{name: "manual", pull: true, manual: true}, []int{1, 87, 11, 0, 1}},
		{layout{name: "manual+var", pull: true, manual: true, vd: true}, []int{1, 0, 87, 11, 0, 1}},
		{layout{name: "manual cached", manual: true}, []int{1, 68, 29, 0, 2}},
		{layout{name: "manual cached+var", manual: true, vd: true}, []int{1, 0, 68, 29, 0, 2}},
	}
	for _, tc := range cases {
		var got []int
		for _, s := range profileUnderTest(tc.l) {
			got = append(got, s.weightPct)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: weights = %v, want %v", tc.l.name, got, tc.want)
		}
	}
}

// TestBuildProfile_AutoLayoutUnchanged pins every auto-layout profile to the
// exact weights it had before manual layouts were modelled.
func TestBuildProfile_AutoLayoutUnchanged(t *testing.T) {
	cases := []struct {
		pull, luks, tpm2, vd bool
		want                 []int
	}{
		{true, false, false, false, []int{0, 1, 0, 0, 87, 11, 0, 1}},
		{true, false, false, true, []int{0, 1, 0, 0, 0, 87, 11, 0, 1}},
		{true, false, true, false, []int{0, 1, 0, 0, 86, 1, 11, 0, 1}},
		{true, false, true, true, []int{0, 1, 0, 0, 0, 86, 1, 11, 0, 1}},
		{true, true, false, false, []int{0, 1, 1, 0, 0, 86, 11, 0, 1}},
		{true, true, false, true, []int{0, 1, 1, 0, 0, 0, 86, 11, 0, 1}},
		{true, true, true, false, []int{0, 1, 1, 0, 0, 85, 1, 11, 0, 1}},
		{true, true, true, true, []int{0, 1, 1, 0, 0, 0, 85, 1, 11, 0, 1}},
		{false, false, false, false, []int{0, 1, 0, 0, 68, 29, 0, 2}},
		{false, false, false, true, []int{0, 1, 0, 0, 0, 68, 29, 0, 2}},
		{false, false, true, false, []int{0, 1, 0, 0, 67, 1, 29, 0, 2}},
		{false, false, true, true, []int{0, 1, 0, 0, 0, 67, 1, 29, 0, 2}},
		{false, true, false, false, []int{0, 1, 1, 0, 0, 67, 29, 0, 2}},
		{false, true, false, true, []int{0, 1, 1, 0, 0, 0, 67, 29, 0, 2}},
		{false, true, true, false, []int{0, 1, 1, 0, 0, 66, 1, 29, 0, 2}},
		{false, true, true, true, []int{0, 1, 1, 0, 0, 0, 66, 1, 29, 0, 2}},
	}
	for _, tc := range cases {
		var got []int
		for _, s := range profileUnderTest(layout{"", tc.pull, false, tc.luks, tc.tpm2, tc.vd}) {
			got = append(got, s.weightPct)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("auto pull=%v luks=%v tpm2=%v var=%v: weights = %v, want %v",
				tc.pull, tc.luks, tc.tpm2, tc.vd, got, tc.want)
		}
	}
}

// TestEmittedSteps_MatchesMainSource keeps emittedSteps honest: the step
// names main.go passes to progress.Step, in source order, must be exactly
// the union of what emittedSteps produces, in the same order.
func TestEmittedSteps_MatchesMainSource(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`progress\.Step\(step, totalSteps, "([^"]+)"`)
	var inSource []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		inSource = append(inSource, m[1])
	}
	manual := emittedSteps(layout{manual: true, vd: true})
	auto := emittedSteps(layout{luks: true, tpm2: true, vd: true})
	want := append([]string{manual[0]}, auto...)
	if !reflect.DeepEqual(inSource, want) {
		t.Errorf("progress.Step names in main.go = %v\nwant %v (update emittedSteps and buildProfile together)", inSource, want)
	}
}
