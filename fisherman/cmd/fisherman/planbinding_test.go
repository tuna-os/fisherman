package main

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// planFor runs `plan --json [--read-image] -` on in and returns the result.
func planFor(t *testing.T, in string, readImage bool) (planResult, int) {
	t.Helper()
	args := []string{"--json", "-"}
	if readImage {
		args = []string{"--json", "--read-image", "-"}
	}
	var out bytes.Buffer
	code := runPlan(args, strings.NewReader(in), &out, io.Discard)
	var res planResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("plan output is not JSON: %v\n%s", err, out.String())
	}
	for _, s := range planSecrets {
		if strings.Contains(out.String(), s) {
			t.Errorf("the plan holds the secret %q", s)
		}
	}
	return res, code
}

// bindRecipe adds the plan's binding to the recipe object in (which ends
// in "}"), as a frontend does after the user confirmed the plan.
func bindRecipe(t *testing.T, in string, b planBinding) string {
	t.Helper()
	raw, err := json.Marshal(recipe.PlanBinding{Digest: b.Digest, ReadImage: b.ReadImage})
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(in, "}") + `,"planBinding":` + string(raw) + "}"
}

// agreeOnStale checks that plan, the full validate and the install's
// pre-flight (installProblems, which main() runs before any disk step)
// give the same verdict on the bound recipe: valid, or plan_stale alone.
func agreeOnStale(t *testing.T, bound string, readImage, wantStale bool) {
	t.Helper()
	res, code := planFor(t, bound, readImage)
	if wantStale {
		if code != exitFailure || res.Valid || len(res.Errors) != 1 || res.Errors[0].Code != recipe.CodePlanStale ||
			res.Errors[0].Field != "planBinding.digest" {
			t.Fatalf("plan: exit %d, errors %+v; want plan_stale", code, res.Errors)
		}
	} else if code != 0 || !res.Valid {
		t.Fatalf("plan: exit %d, errors %+v; want valid", code, res.Errors)
	}

	var vout bytes.Buffer
	if vcode := runValidateJSON([]string{"--json", "-"}, strings.NewReader(bound), &vout, io.Discard); vcode != code {
		t.Errorf("validate exits %d, plan %d", vcode, code)
	}
	var v validateResult
	if err := json.Unmarshal(vout.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Errors, res.Errors) {
		t.Errorf("validate %v, plan %v", v.Errors, res.Errors)
	}
	r, err := recipe.Parse([]byte(bound))
	if err != nil {
		t.Fatal(err)
	}
	ps := installProblems(r)
	if ps == nil {
		ps = []recipe.Problem{}
	}
	if !reflect.DeepEqual(ps, res.Errors) {
		t.Errorf("the install's pre-flight reports %v, plan %v", ps, res.Errors)
	}
}

// A frontend plans, shows the plan, and copies plan.binding into the recipe
// it sends. The install accepts the recipe while the machine still gives
// that plan, and refuses it with plan_stale once the target, its partitions
// or a capacity verdict changed. Plan, validate and the install's
// pre-flight agree; none of them reads the image unless the binding says
// the confirmed plan did.
func TestPlanBindingRoundTrip(t *testing.T) {
	dir := planDisks(t)
	auto := `{"disk":"` + dir + `/sda","filesystem":"xfs","image":"ghcr.io/example/os:stable","hostname":"fixture",` +
		`"encryption":{"type":"luks-passphrase","passphrase":"SENTINEL-LUKS-PASSPHRASE"}}`

	g := guardCommands(t)
	first, code := planFor(t, auto, false)
	if code != 0 || first.Plan.Binding.ReadImage || !planDigestPattern(first.Plan.Binding.Digest) {
		t.Fatalf("first plan: exit %d, binding %+v", code, first.Plan.Binding)
	}
	bound := bindRecipe(t, auto, first.Plan.Binding)

	t.Run("unchanged", func(t *testing.T) {
		agreeOnStale(t, bound, false, false)
		// The binding is not part of what it binds: the bound recipe plans
		// to the same digest.
		again, _ := planFor(t, bound, false)
		if again.Plan.Binding != first.Plan.Binding {
			t.Errorf("binding %+v, then %+v", first.Plan.Binding, again.Plan.Binding)
		}
		for _, line := range g.seen {
			if strings.HasPrefix(line, "skopeo") || strings.HasPrefix(line, "podman") {
				t.Errorf("a binding without readImage read the image: %q", line)
			}
		}
	})
	t.Run("drift that changes no verdict", func(t *testing.T) {
		// Free space and by-id links are not in the basis.
		hostScratchFree = planScratchFree(planFixtureFreeBytes / 2)
		hostTarget = planHost(map[string]probe.Target{"sda": {SizeBytes: 500107862016, Serial: "FIXTURE-SDA-SERIAL",
			WWN: "0x5000c500a1b2c3d4", Model: "Fixture Disk"}})
		t.Cleanup(func() { hostScratchFree, hostTarget = planScratchFree(planFixtureFreeBytes), planHost(nil) })
		agreeOnStale(t, bound, false, false)
	})
	for _, c := range []struct {
		name string
		now  probe.Target
	}{
		{"another disk on the same node", probe.Target{SizeBytes: 500107862016, Serial: "OTHER-SERIAL", WWN: "0x5000c500deadbeef", Model: "Fixture Disk"}},
		{"the size changed", probe.Target{SizeBytes: 1 << 40, Serial: "FIXTURE-SDA-SERIAL", WWN: "0x5000c500a1b2c3d4", Model: "Fixture Disk"}},
		{"the disk lost its identity", probe.Target{SizeBytes: 500107862016}},
	} {
		t.Run(c.name, func(t *testing.T) {
			hostTarget = planHost(map[string]probe.Target{"sda": c.now})
			t.Cleanup(func() { hostTarget = planHost(nil) })
			agreeOnStale(t, bound, false, true)
		})
	}
	t.Run("scratch moved to the target", func(t *testing.T) {
		varIsSpaceConstrained = func() bool { return true }
		t.Cleanup(func() { varIsSpaceConstrained = func() bool { return false } })
		agreeOnStale(t, bound, false, true)
	})
	t.Run("another recipe", func(t *testing.T) {
		// A plan for btrfs is not the plan for xfs.
		agreeOnStale(t, strings.Replace(bound, `"filesystem":"xfs"`, `"filesystem":"btrfs"`, 1), false, true)
	})
}

// A binding made with --read-image makes the check read the image too, and
// a capacity verdict that changed since (the image grew past the root) is
// plan_stale.
func TestPlanBindingReadsTheImageOnlyWhenTheConfirmedPlanDid(t *testing.T) {
	dir := planDisks(t)
	in := `{"disk":"` + dir + `/sda","filesystem":"xfs","image":"ghcr.io/example/cached:stable","hostname":"fixture",` +
		`"encryption":{"type":"none"}}`
	images := stubImages(t, guardCommands(t))
	first, code := planFor(t, in, true)
	if code != 0 || !first.Plan.Binding.ReadImage || first.Plan.Capacity.ImageFit != "enough" {
		t.Fatalf("first plan: exit %d, binding %+v, capacity %+v", code, first.Plan.Binding, first.Plan.Capacity)
	}
	withoutImage, _ := planFor(t, in, false)
	if withoutImage.Plan.Binding.Digest == first.Plan.Binding.Digest {
		t.Error("a plan without the image has the digest of one with it")
	}
	bound := bindRecipe(t, in, first.Plan.Binding)
	agreeOnStale(t, bound, true, false)

	// The image in the store is now bigger than the disk's root.
	images.answers["podman image inspect --format {{.Size}} ghcr.io/example/cached:stable"] = fakeImageAnswer{out: "999999999999999\n"}
	agreeOnStale(t, bound, true, true)

	// Without readImage the check reads no image, so the same recipe bound
	// to the plan without one still matches.
	before := len(images.g.seen)
	agreeOnStale(t, bindRecipe(t, in, withoutImage.Plan.Binding), false, false)
	for _, line := range images.g.seen[before:] {
		if strings.HasPrefix(line, "skopeo") || strings.HasPrefix(line, "podman") {
			t.Errorf("a binding without readImage read the image: %q", line)
		}
	}
}

// A manual layout binds its partitions: one that changed size is
// plan_stale.
func TestPlanBindingManualLayout(t *testing.T) {
	dir := planDisks(t)
	guardCommands(t)
	in := `{"customMounts":[{"partition":"` + dir + `/sda1","target":"/boot/efi","fstype":"fat32"},` +
		`{"partition":"` + dir + `/sda2","target":"/boot","fstype":"ext4"},` +
		`{"partition":"` + dir + `/sda3","target":"/","fstype":"xfs"}],` +
		`"image":"ghcr.io/example/os:stable","hostname":"fixture","encryption":{"type":"none"}}`
	first, code := planFor(t, in, false)
	if code != 0 {
		t.Fatalf("exit %d: %+v", code, first.Errors)
	}
	bound := bindRecipe(t, in, first.Plan.Binding)
	agreeOnStale(t, bound, false, false)

	old := planFixturePartitions["sda3"]
	planFixturePartitions["sda3"] = 50 << 30
	t.Cleanup(func() { planFixturePartitions["sda3"] = old })
	agreeOnStale(t, bound, false, true)
}

// planDigestPattern reports whether s has the form of plan.binding.digest,
// which recipe.CheckPlanBinding accepts.
func planDigestPattern(s string) bool {
	return len(recipe.CheckPlanBinding(&recipe.Recipe{PlanBinding: &recipe.PlanBinding{Digest: s}})) == 0
}

// The /var disk's size is in the basis: a /var disk that is now another
// size (another disk on the same node) is plan_stale.
func TestPlanBindingVarDiskSize(t *testing.T) {
	dir := planDisks(t)
	guardCommands(t)
	in := `{"disk":"` + dir + `/sda","filesystem":"xfs","varDisk":{"disk":"` + dir + `/sdb"},` +
		`"image":"ghcr.io/example/os:stable","hostname":"fixture","encryption":{"type":"none"}}`
	first, code := planFor(t, in, false)
	if code != 0 || first.Plan.VarDisk == nil || first.Plan.VarDisk.SizeBytes != planFixtureTargets["sdb"].SizeBytes {
		t.Fatalf("exit %d, var_disk %+v", code, first.Plan.VarDisk)
	}
	bound := bindRecipe(t, in, first.Plan.Binding)
	agreeOnStale(t, bound, false, false)

	hostTarget = planHost(map[string]probe.Target{"sdb": {SizeBytes: 1 << 40, Serial: "FIXTURE-SDB-SERIAL", Model: "Fixture USB"}})
	t.Cleanup(func() { hostTarget = planHost(nil) })
	agreeOnStale(t, bound, false, true)
}
