package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
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

// planOCILayer is one layer descriptor of a planOCILayout: its compressed
// size and, when not 0, its uncompressed-size annotation.
type planOCILayer struct{ size, unpacked int64 }

// writePlanOCILayout builds an OCI layout whose manifest lists layers, and
// returns its path and the manifest digest. It writes the index, the
// manifest and the configuration, never a layer blob: plan must not open
// one. With platforms, index.json lists an arm64 manifest first, then the
// host's.
func writePlanOCILayout(t *testing.T, platforms bool, layers ...planOCILayer) (string, string) {
	t.Helper()
	layout := t.TempDir()
	if err := os.WriteFile(filepath.Join(layout, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"architecture":"` + runtime.GOARCH + `","os":"linux","config":{"Labels":{"containers.bootc":"1"}}}`)
	configDigest := writeBlob(t, layout, config)
	var descs []any
	for i, l := range layers {
		d := map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+zstd",
			"digest": fmt.Sprintf("sha256:%064x", i+1), "size": l.size}
		if l.unpacked != 0 {
			d["annotations"] = map[string]string{UncompressedSizeAnnotation: strconv.FormatInt(l.unpacked, 10)}
		}
		descs = append(descs, d)
	}
	manifest, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": configDigest, "size": len(config)},
		"layers": descs})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := writeBlob(t, layout, manifest)
	entries := []any{map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": manifestDigest, "size": len(manifest)}}
	if platforms {
		entries = []any{
			map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": planTestArm, "size": 1,
				"platform": map[string]string{"os": "linux", "architecture": "not-" + runtime.GOARCH}},
			map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": manifestDigest, "size": len(manifest),
				"platform": map[string]string{"os": runtime.GOOS, "architecture": runtime.GOARCH}},
		}
	}
	index, err := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	return layout, manifestDigest
}

// An OCI layout is read from its files: the manifest the install selects,
// its configuration and the layer descriptors. The unpacked size is the
// layers' annotations when every layer has one, else the ×3 estimate, as
// for a registry image. No command runs and no layer is opened.
func TestInspectImageForPlanOCILayout(t *testing.T) {
	calls := stubPlanInspect(t, nil)
	const gib = int64(1) << 30
	for _, c := range []struct {
		name          string
		platforms     bool
		layers        []planOCILayer
		compressed    int64
		unpacked      int64
		unpackedFrom  string
		wantLayers    int
		wantPlatform  string
		wantDigestSet bool
	}{
		{"annotated", false, []planOCILayer{{2 * gib, 5 * gib}, {1 * gib, 2 * gib}}, 3 * gib, 7 * gib, "annotations", 2, "linux/" + runtime.GOARCH, true},
		{"not annotated", false, []planOCILayer{{2 * gib, 0}, {1 * gib, 0}}, 3 * gib, 9 * gib, "estimate", 2, "linux/" + runtime.GOARCH, true},
		{"partly annotated", false, []planOCILayer{{2 * gib, 5 * gib}, {1 * gib, 0}}, 3 * gib, 9 * gib, "estimate", 2, "linux/" + runtime.GOARCH, true},
		{"multi-platform index", true, []planOCILayer{{4 * gib, 0}}, 4 * gib, 12 * gib, "estimate", 1, "linux/" + runtime.GOARCH, true},
		{"a layer without a size", false, []planOCILayer{{2 * gib, 0}, {0, 0}}, 0, 0, "", 2, "linux/" + runtime.GOARCH, true},
		{"annotated, a layer without a size", false, []planOCILayer{{2 * gib, 5 * gib}, {0, 1 * gib}}, 0, 6 * gib, "annotations", 2, "linux/" + runtime.GOARCH, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			layout, digest := writePlanOCILayout(t, c.platforms, c.layers...)
			f := InspectImageForPlan("oci:"+layout, true)
			if f.ReadFrom != "oci" || f.ManifestDigest != digest || f.Layers != c.wantLayers || f.Platform != c.wantPlatform ||
				f.CompressedBytes != c.compressed || f.UnpackedBytes != c.unpacked || f.UnpackedFrom != c.unpackedFrom ||
				f.PlatformDigest != "" {
				t.Errorf("%+v", f)
			}
		})
	}
	if len(*calls) != 0 {
		t.Errorf("reading an OCI layout ran %v", *calls)
	}
}

// A layout plan cannot read leaves the facts unknown, with the reason.
func TestInspectImageForPlanBrokenOCILayout(t *testing.T) {
	stubPlanInspect(t, nil)
	good, digest := writePlanOCILayout(t, false, planOCILayer{1 << 30, 0})
	alg, hexpart, _ := strings.Cut(digest, ":")
	for name, breakIt := range map[string]func(string) error{
		"no oci-layout": func(l string) error { return os.Remove(filepath.Join(l, "oci-layout")) },
		"wrong version": func(l string) error {
			return os.WriteFile(filepath.Join(l, "oci-layout"), []byte(`{"imageLayoutVersion":"2"}`), 0o644)
		},
		"no index": func(l string) error { return os.Remove(filepath.Join(l, "index.json")) },
		"empty index": func(l string) error {
			return os.WriteFile(filepath.Join(l, "index.json"), []byte(`{"manifests":[]}`), 0o644)
		},
		"no manifest blob": func(l string) error { return os.Remove(filepath.Join(l, "blobs", alg, hexpart)) },
		"bad digest": func(l string) error {
			return os.WriteFile(filepath.Join(l, "index.json"), []byte(`{"manifests":[{"digest":"md5:abc"}]}`), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			layout := copyDir(t, good)
			if err := breakIt(layout); err != nil {
				t.Fatal(err)
			}
			f := InspectImageForPlan("oci:"+layout, true)
			if f.ReadFrom != "" || !strings.HasPrefix(f.Reason, "the OCI layout cannot be read: ") {
				t.Errorf("%+v", f)
			}
		})
	}
}

// copyDir copies the regular files and directories under src to a new
// temporary directory and returns it.
func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}
