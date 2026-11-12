package install

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
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

// A registry image's unpacked size comes from its manifest alone: the sum
// of the layers' uncompressed-size annotations when every layer has a
// valid one, else the compressed size times UnpackedEstimateRatio, flagged
// "estimate". No layer is read; a local copy keeps the store's size.
func TestInspectImageForPlanRegistryUnpacked(t *testing.T) {
	layer := func(size int64, ann string) string {
		a := "null"
		if ann != "" {
			a = `{"` + UncompressedSizeAnnotation + `":"` + ann + `"}`
		}
		return `{"Size":` + strconv.FormatInt(size, 10) + `,"Annotations":` + a + `}`
	}
	inspect := func(layers ...string) string {
		names := make([]string, len(layers))
		for i := range names {
			names[i] = `"l` + strconv.Itoa(i) + `"`
		}
		return `{"Digest":"` + planTestDigest + `","Os":"linux","Architecture":"amd64","Layers":[` + strings.Join(names, ",") +
			`],"LayersData":[` + strings.Join(layers, ",") + `]}`
	}
	calls := stubPlanInspect(t, map[string]string{
		"skopeo inspect --no-tags docker://example.invalid/annotated:1":   inspect(layer(10, "40"), layer(20, "50")),
		"skopeo inspect --no-tags docker://example.invalid/partial:1":     inspect(layer(10, "40"), layer(20, "")),
		"skopeo inspect --no-tags docker://example.invalid/bad:1":         inspect(layer(10, "x"), layer(20, "50")),
		"skopeo inspect --no-tags docker://example.invalid/plain:1":       inspect(layer(10, ""), layer(20, "")),
		"skopeo inspect --no-tags docker://example.invalid/nosizes:1":     `{"Digest":"` + planTestDigest + `","Layers":["a"]}`,
		"skopeo inspect containers-storage:example.invalid/local:1":       inspect(layer(10, "40")),
		"podman image inspect --format {{.Size}} example.invalid/local:1": "70\n",
	})
	for _, c := range []struct {
		image    string
		unpacked int64
		from     string
	}{
		{"example.invalid/annotated:1", 90, "annotations"},
		{"example.invalid/partial:1", 30 * UnpackedEstimateRatio, "estimate"},
		{"example.invalid/bad:1", 30 * UnpackedEstimateRatio, "estimate"},
		{"example.invalid/plain:1", 30 * UnpackedEstimateRatio, "estimate"},
		{"example.invalid/nosizes:1", 0, ""},
		{"containers-storage:example.invalid/local:1", 70, "containers-storage"},
	} {
		f := InspectImageForPlan(c.image, true)
		if f.UnpackedBytes != c.unpacked || f.UnpackedFrom != c.from {
			t.Errorf("%s: unpacked %d from %q, want %d from %q", c.image, f.UnpackedBytes, f.UnpackedFrom, c.unpacked, c.from)
		}
	}
	for _, line := range *calls {
		if strings.Contains(line, "copy") || strings.Contains(line, "pull") || strings.Contains(line, "blob") {
			t.Errorf("plan read more than metadata: %q", line)
		}
	}
}

func TestAnnotatedUnpackedBytesOverflow(t *testing.T) {
	var m planImageInfo
	if err := json.Unmarshal([]byte(`{"LayersData":[{"Size":1,"Annotations":{"`+UncompressedSizeAnnotation+
		`":"9223372036854775807"}},{"Size":1,"Annotations":{"`+UncompressedSizeAnnotation+`":"1"}}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if n := m.annotatedUnpackedBytes(); n != 0 {
		t.Errorf("overflowing annotations sum to %d, want 0 (unknown)", n)
	}
	if n, from := m.registryUnpacked(math.MaxInt64); n != 0 || from != "" {
		t.Errorf("an estimate that overflows: %d %q", n, from)
	}
}
