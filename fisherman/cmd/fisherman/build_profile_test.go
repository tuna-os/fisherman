package main

// Tests for buildProfile (the GUI progress-weight scheduler) and
// isSpaceConstrained. buildProfile is pure — the frontend progress bar
// depends on its weights summing to exactly 100 with monotonic cumulative
// positions, so the shape of every recipe variant is asserted table-driven.

import (
	"runtime"
	"testing"
)

// sumWeights returns the total weight of a profile.
func sumWeights(p []stepProfile) int {
	total := 0
	for _, s := range p {
		total += s.weightPct
	}
	return total
}

// TestBuildProfile_WeightsSumTo100 checks every combination of the four
// feature flags: the progress bar must always reach exactly 100%.
func TestBuildProfile_WeightsSumTo100(t *testing.T) {
	for _, pull := range []bool{false, true} {
		for _, luks := range []bool{false, true} {
			for _, tpm2 := range []bool{false, true} {
				for _, varDisk := range []bool{false, true} {
					p := buildProfile(pull, luks, tpm2, varDisk)
					if got := sumWeights(p); got != 100 {
						t.Errorf("buildProfile(pull=%v,luks=%v,tpm2=%v,var=%v) weights sum = %d, want 100",
							pull, luks, tpm2, varDisk, got)
					}
				}
			}
		}
	}
}

// TestBuildProfile_StepCounts verifies the number of progress steps each
// recipe variant produces.
func TestBuildProfile_StepCounts(t *testing.T) {
	cases := []struct {
		name        string
		pull, luks  bool
		tpm2, vdisk bool
		want        int
	}{
		{"base", true, false, false, false, 8},
		{"no-pull", false, false, false, false, 8},
		{"luks", true, true, false, false, 9},
		{"tpm2", true, false, true, false, 9},
		{"var-disk", true, false, false, true, 9},
		{"luks+tpm2", true, true, true, false, 10},
		{"everything", false, true, true, true, 11},
	}
	for _, tc := range cases {
		p := buildProfile(tc.pull, tc.luks, tc.tpm2, tc.vdisk)
		if len(p) != tc.want {
			t.Errorf("%s: steps = %d, want %d (profile %+v)", tc.name, len(p), tc.want, p)
		}
	}
}

// TestBuildProfile_CumulativePositions verifies the bar-position invariant:
// step 0 starts at 0, each step starts where the previous one ended, and
// the final step's start + its own weight reaches 100.
func TestBuildProfile_CumulativePositions(t *testing.T) {
	for _, pull := range []bool{false, true} {
		for _, luks := range []bool{false, true} {
			p := buildProfile(pull, luks, false, false)
			if p[0].cumulativePct != 0 {
				t.Errorf("first step cumulative = %d, want 0", p[0].cumulativePct)
			}
			for i := 1; i < len(p); i++ {
				want := p[i-1].cumulativePct + p[i-1].weightPct
				if p[i].cumulativePct != want {
					t.Errorf("step %d cumulative = %d, want %d (prev %+v)", i, p[i].cumulativePct, want, p[i-1])
				}
			}
			last := p[len(p)-1]
			if last.cumulativePct+last.weightPct != 100 {
				t.Errorf("last step (%+v) does not reach 100", last)
			}
		}
	}
}

// TestBuildProfile_Order verifies the step sequence for a LUKS + TPM2 +
// separate-/var recipe: the LUKS step comes right after formatting the EFI
// partition, the TPM2 step after the OS install, and the /var format right
// after mounting.
func TestBuildProfile_Order(t *testing.T) {
	p := buildProfile(true, true, true, true)
	// Expect: 0(part) 1(efi) 1(luks) 0(root) 0(mount) 0(var) 85(os) 1(tpm2) 11(flatpak) 0(conf) 1(finalize)
	var weights []int
	for _, s := range p {
		weights = append(weights, s.weightPct)
	}
	want := []int{0, 1, 1, 0, 0, 0, 85, 1, 11, 0, 1}
	if len(weights) != len(want) {
		t.Fatalf("weights = %v, want %v", weights, want)
	}
	for i := range want {
		if weights[i] != want[i] {
			t.Errorf("weights[%d] = %d, want %d (full: %v)", i, weights[i], want[i], weights)
		}
	}
}

// TestBuildProfile_NoPullAdjustsWeights verifies that a cached image (no
// pull) shifts weight from the OS install to the flatpak step.
func TestBuildProfile_NoPullAdjustsWeights(t *testing.T) {
	pull := buildProfile(true, false, false, false)
	noPull := buildProfile(false, false, false, false)

	find := func(p []stepProfile, w int) int {
		for _, s := range p {
			if s.weightPct == w {
				return w
			}
		}
		return -1
	}
	if find(pull, 87) < 0 || find(pull, 11) < 0 {
		t.Errorf("pull profile should have 87 (OS) and 11 (flatpak) weights: %+v", pull)
	}
	if find(noPull, 68) < 0 || find(noPull, 29) < 0 {
		t.Errorf("no-pull profile should have 68 (OS) and 29 (flatpak) weights: %+v", noPull)
	}
}

// TestBuildProfile_LuksAndTpm2ShaveOsWeight verifies each encryption feature
// deducts one weight point from the OS install step.
func TestBuildProfile_LuksAndTpm2ShaveOsWeight(t *testing.T) {
	base := buildProfile(true, false, false, false)
	luks := buildProfile(true, true, false, false)
	tpm2 := buildProfile(true, false, true, false)
	both := buildProfile(true, true, true, false)

	maxWeight := func(p []stepProfile) int {
		m := 0
		for _, s := range p {
			if s.weightPct > m {
				m = s.weightPct
			}
		}
		return m
	}
	if maxWeight(base) != 87 {
		t.Errorf("base OS weight = %d, want 87", maxWeight(base))
	}
	if maxWeight(luks) != 86 || maxWeight(tpm2) != 86 || maxWeight(both) != 85 {
		t.Errorf("OS weights luks=%d tpm2=%d both=%d, want 86/86/85",
			maxWeight(luks), maxWeight(tpm2), maxWeight(both))
	}
}

// TestIsSpaceConstrained_Tmpfs verifies tmpfs (used by live ISOs for /var)
// is detected as space-constrained. /dev/shm is tmpfs on any Linux host.
func TestIsSpaceConstrained_Tmpfs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("statfs magic numbers are Linux-specific")
	}
	if !isSpaceConstrained("/dev/shm") {
		t.Error("/dev/shm is tmpfs; isSpaceConstrained should report true")
	}
}

func TestIsSpaceConstrained_MissingPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("statfs magic numbers are Linux-specific")
	}
	if isSpaceConstrained("/definitely/not/a/real/path-xyz") {
		t.Error("a nonexistent path must not report constrained")
	}
}
