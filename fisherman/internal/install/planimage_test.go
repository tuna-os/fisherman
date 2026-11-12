package install

import (
	"errors"
	"strings"
	"testing"
)

const (
	planTestDigest = "sha256:abababababababababababababababababababababababababababababababab"
	planTestArm    = "sha256:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
)

// stubPlanInspect answers PlanInspectFn from answers (keyed by the command
// line) and records every call.
func stubPlanInspect(t *testing.T, answers map[string]string) *[]string {
	t.Helper()
	var calls []string
	old, oldArch := PlanInspectFn, HostArchitecture
	PlanInspectFn = func(name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, line)
		if out, ok := answers[line]; ok {
			return []byte(out), nil
		}
		return nil, errors.New("pinging container registry example.invalid: dial tcp: no such host")
	}
	HostArchitecture = func() string { return "amd64" }
	t.Cleanup(func() { PlanInspectFn, HostArchitecture = old, oldArch })
	return &calls
}

func TestInspectImageForPlanNeverReadsARegistryUnasked(t *testing.T) {
	calls := stubPlanInspect(t, nil)
	for _, image := range []string{"example.invalid/os:1", "docker://example.invalid/os:1", "containers-storage:example.invalid/os:1", "oci:/nonexistent/layout"} {
		if f := InspectImageForPlan(image, false); f.ReadFrom != "" || f.Reason == "" {
			t.Errorf("%s: %+v", image, f)
		}
	}
	// A local source never reads a registry, even when allowed.
	InspectImageForPlan("containers-storage:example.invalid/os:1", true)
	InspectImageForPlan("oci:/nonexistent/layout", true)
	for _, c := range *calls {
		if strings.Contains(c, "docker://") || (!strings.HasPrefix(c, "skopeo inspect ") && !strings.HasPrefix(c, "podman image inspect ")) {
			t.Errorf("unexpected command %q", c)
		}
	}
}

func TestInspectImageForPlanSizes(t *testing.T) {
	stubPlanInspect(t, map[string]string{
		// Missing and unknown (-1) layer sizes leave the compressed size unknown.
		"skopeo inspect containers-storage:example.invalid/nosizes:1":     `{"Digest":"` + planTestDigest + `","Os":"linux","Architecture":"amd64","Layers":["a","b"]}`,
		"skopeo inspect containers-storage:example.invalid/minus:1":       `{"Digest":"` + planTestDigest + `","Layers":["a"],"LayersData":[{"Size":-1}]}`,
		"podman image inspect --format {{.Size}} example.invalid/minus:1": "not a number\n",
		"skopeo inspect --no-tags docker://example.invalid/multi:1":       `{"Digest":"` + planTestDigest + `","Os":"linux","Architecture":"amd64","Layers":["a","b"],"LayersData":[{"Size":10},{"Size":20}]}`,
		"skopeo inspect --raw docker://example.invalid/multi:1":           `{"manifests":[{"digest":"` + planTestArm + `","platform":{"os":"linux","architecture":"arm64"}}]}`,
	})
	f := InspectImageForPlan("containers-storage:example.invalid/nosizes:1", false)
	if f.ReadFrom != "containers-storage" || f.Layers != 2 || f.CompressedBytes != 0 || f.UnpackedBytes != 0 || f.Platform != "linux/amd64" {
		t.Errorf("nosizes: %+v", f)
	}
	f = InspectImageForPlan("containers-storage:example.invalid/minus:1", false)
	if f.CompressedBytes != 0 || f.UnpackedBytes != 0 || f.Platform != "" {
		t.Errorf("minus: %+v", f)
	}
	// The index lists no build for this host: no platform digest, and the
	// install's own check refuses the image.
	f = InspectImageForPlan("example.invalid/multi:1", true)
	if f.ReadFrom != "registry" || f.CompressedBytes != 30 || f.PlatformDigest != "" || f.ManifestDigest != planTestDigest {
		t.Errorf("multi: %+v", f)
	}
	f = InspectImageForPlan("example.invalid/gone:1", true)
	if f.ReadFrom != "" || !strings.Contains(f.Reason, "cannot be reached") {
		t.Errorf("gone: %+v", f)
	}
}
