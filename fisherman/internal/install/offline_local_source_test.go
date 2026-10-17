package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Actual installer argument/store mapping with controlled process boundaries;
// this does not prove an OS installation.
func TestVerifiedLocalSourceKeepsOriginalStoreAndTargetPin(t *testing.T) {
	checkVerifiedLocalRoute(t, true)
}
func TestVerifiedLocalSourceRuntimeIdWithoutRedirect(t *testing.T) {
	checkVerifiedLocalRoute(t, false)
}
func checkVerifiedLocalRoute(t *testing.T, redirect bool) {
	dir, err := os.MkdirTemp(".", ".offline-route-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	log := filepath.Join(dir, "podman.argv")
	if err = os.WriteFile(filepath.Join(dir, "podman"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("CAPTURE", log)
	oldSpace, oldDriver, oldExport := storageSpaceConstrainedFn, selectStorageDriverFn, SkopeoExportOCIFn
	defer func() {
		storageSpaceConstrainedFn = oldSpace
		selectStorageDriverFn = oldDriver
		SkopeoExportOCIFn = oldExport
	}()
	storageSpaceConstrainedFn = func() bool { return redirect }
	selectStorageDriverFn = func(string) (string, string) { return "overlay", "controlled route" }
	const source = "containers-storage:09dae9ca006837e4962e8f9362c7985758bab0c1c329e3f484e1365babc1ee20"
	const target = "ghcr.io/tuna-os/yellowfin@sha256:3a086506b128b0afa5e8a672d554c587381a99b8ed1a27784eaa18945ccaba4b"
	exported := ""
	SkopeoExportOCIFn = func(image, dest, tmp string) error { exported = image; return nil }
	err = BootcInstall(Options{SourceImgref: source, TargetImgref: target, Target: filepath.Join(dir, "target"), ScratchDir: filepath.Join(dir, "scratch")})
	if err != nil {
		t.Fatal(err)
	}
	if redirect && exported != source {
		t.Fatalf("export source changed to unpopulated store: %q", exported)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal("actual owned command was not executed:", err)
	}
	args := string(raw)
	if strings.Contains(args, "\npull\n") {
		t.Fatal("local source triggered pull")
	}
	if !strings.Contains(args, "--target-imgref\n"+target+"\n") {
		t.Fatalf("original root pin lost: %s", args)
	}
	if redirect && !strings.Contains(args, "oci:"+filepath.Join(dir, "scratch", "oci-cache")+"\n") {
		t.Fatal("local export not used as container source")
	}
	if !redirect && (exported != "" || !strings.Contains(args, strings.TrimPrefix(source, "containers-storage:")+"\nbootc\n")) {
		t.Fatal("unredirected container did not consume bare immutable ID")
	}
}

func TestExplicitLocalCheckNeverQueriesRegistry(t *testing.T) {
	old := SkopeoInspectFn
	defer func() { SkopeoInspectFn = old }()
	const source = "containers-storage:09dae9ca006837e4962e8f9362c7985758bab0c1c329e3f484e1365babc1ee20"
	for _, mode := range []string{"valid", "missing", "malformed", "empty", "bad-digest"} {
		calls := 0
		SkopeoInspectFn = func(args ...string) ([]byte, error) {
			calls++
			if len(args) != 1 || args[0] != source {
				t.Fatalf("unexpected registry/source lookup: %v", args)
			}
			switch mode {
			case "missing":
				return nil, errors.New("controlled missing source")
			case "malformed":
				return []byte("{bad"), nil
			case "empty":
				return []byte("{}"), nil
			case "bad-digest":
				return []byte(`{"Digest":"sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`), nil
			}
			return []byte(`{"Digest":"sha256:2445c8493c47dd0b1406a30066a44ae2738e5b18b9d80bf59190232ae7c56e4d","Layers":["layer"]}`), nil
		}
		result := CheckImage(source)
		if calls != 1 || result.NeedsPull != (mode != "valid") {
			t.Fatalf("mode %s result %v calls %d", mode, result, calls)
		}
	}
}
func TestMissingExplicitLocalSourceCannotInstall(t *testing.T) {
	old := SkopeoExportOCIFn
	defer func() { SkopeoExportOCIFn = old }()
	SkopeoExportOCIFn = func(string, string, string) error { t.Fatal("missing source reached export"); return nil }
	if err := BootcInstall(Options{SourceImgref: "containers-storage:missing", NeedsPull: true}); err == nil {
		t.Fatal("missing source accepted")
	}
}

// --- Offline OCI media (tacklebox/SuperISO) sources ---
//
// Helpers and behavioral tests for the MediaInput v1 contract: the ISO
// builder's media-input.json names an embedded image, its verified
// read-only OCI layout on the live system, and the day-2 update ref.

// sha256Of returns the "sha256:<hex>" digest of content.
func sha256Of(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeBlob stores content under blobs/<alg>/<hex> and returns its digest.
func writeBlob(t *testing.T, layout string, content []byte) string {
	t.Helper()
	digest := sha256Of(content)
	alg, hexpart, ok := strings.Cut(digest, ":")
	if !ok {
		t.Fatalf("bad digest %q", digest)
	}
	path := filepath.Join(layout, "blobs", alg, hexpart)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return digest
}

// writeOCILayoutFixture builds a minimal but complete OCI layout (oci-layout,
// one config blob, one layer blob, one manifest, index.json) and returns the
// layout dir and the manifest digest. The manifest carries no platform, so
// no host-platform match is required.
func writeOCILayoutFixture(t *testing.T) (layout, manifestDigest string) {
	t.Helper()
	layout, err := os.MkdirTemp("", ".oci-layout-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(layout) })
	if err := os.WriteFile(filepath.Join(layout, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("controlled layer content")
	configDigest := writeBlob(t, layout, config)
	layerDigest := writeBlob(t, layout, layer)
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": configDigest, "size": len(config)},
		"layers":        []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": layerDigest, "size": len(layer)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest = writeBlob(t, layout, manifest)
	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests":     []any{map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": manifestDigest, "size": len(manifest)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	return layout, manifestDigest
}

func TestPreflightOCILayoutAcceptsCompleteLayout(t *testing.T) {
	layout, digest := writeOCILayoutFixture(t)
	if err := PreflightOCILayout(layout, digest); err != nil {
		t.Fatalf("complete layout refused: %v", err)
	}
	if err := PreflightOCILayout(layout, ""); err != nil {
		t.Fatalf("complete layout refused without expected digest: %v", err)
	}
	mi := &MediaInput{
		SchemaVersion:          1,
		Kind:                   "tacklebox-live",
		Image:                  "ghcr.io/example/os:stable",
		PlatformManifestDigest: digest,
		OCILayoutPath:          layout,
		TargetImgref:           "ghcr.io/example/os:stable",
		Platform:               runtime.GOOS + "/" + runtime.GOARCH,
	}
	if got := mi.ExpectedDigest(); got != digest {
		t.Fatalf("ExpectedDigest = %q, want platform digest %q", got, digest)
	}
	if err := PreflightMediaInput(mi); err != nil {
		t.Fatalf("complete media input refused: %v", err)
	}
}

func TestPreflightOCILayoutRefusals(t *testing.T) {
	layout, digest := writeOCILayoutFixture(t)
	wrongDigest := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if wrongDigest == digest {
		t.Fatal("fixture digest collides with refusal digest")
	}
	// A layout whose manifest targets a platform that cannot be this host.
	foreignLayout, foreignDigest := writeOCILayoutFixture(t)
	foreignIndex, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests": []any{map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    foreignDigest,
			"size":      1,
			"platform":  map[string]any{"os": "other-os", "architecture": "other-arch"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignLayout, "index.json"), foreignIndex, 0o644); err != nil {
		t.Fatal(err)
	}
	// A layout with a corrupt index.json.
	corruptLayout, _ := writeOCILayoutFixture(t)
	if err := os.WriteFile(filepath.Join(corruptLayout, "index.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A truncated layout: the layer blob the manifest references is gone.
	truncatedLayout, truncatedDigest := writeOCILayoutFixture(t)
	manifestHex := strings.TrimPrefix(truncatedDigest, "sha256:")
	manifestRaw, err := os.ReadFile(filepath.Join(truncatedLayout, "blobs", "sha256", manifestHex))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Layers) != 1 {
		t.Fatalf("fixture has %d layers, want 1", len(manifest.Layers))
	}
	if err := os.Remove(filepath.Join(truncatedLayout, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:"))); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		layout string
		digest string
	}{
		{"missing layout dir", filepath.Join(layout, "no-such-dir"), ""},
		{"missing oci-layout file", t.TempDir(), ""},
		{"corrupt index.json", corruptLayout, ""},
		{"digest mismatch", layout, wrongDigest},
		{"truncated layer blob", truncatedLayout, truncatedDigest},
		{"foreign platform manifest", foreignLayout, foreignDigest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := PreflightOCILayout(tt.layout, tt.digest); err == nil {
				t.Fatal("broken layout accepted")
			}
		})
	}
}

func TestParseMediaInputV1Spellings(t *testing.T) {
	camel := `{"schemaVersion":1,"kind":"tacklebox-live","image":"ghcr.io/example/os:stable",` +
		`"platformManifestDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
		`"ociLayoutPath":"/run/media/layout","targetImgref":"ghcr.io/example/os:stable","platform":"linux/amd64"}`
	snake := `{"schema_version":1,"kind":"tacklebox-live","image":"ghcr.io/example/os:stable",` +
		`"platform_manifest_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
		`"oci_layout_path":"/run/media/layout","target_imgref":"ghcr.io/example/os:stable","platform":"linux/amd64"}`
	c, err := ParseMediaInput([]byte(camel))
	if err != nil {
		t.Fatalf("camelCase rejected: %v", err)
	}
	s, err := ParseMediaInput([]byte(snake))
	if err != nil {
		t.Fatalf("snake_case rejected: %v", err)
	}
	if *c != *s {
		t.Fatalf("spelling variants decode differently:\n%+v\n%+v", c, s)
	}
	if c.SchemaVersion != 1 || c.Image != "ghcr.io/example/os:stable" || c.OCILayoutPath != "/run/media/layout" ||
		c.TargetImgref != "ghcr.io/example/os:stable" ||
		c.PlatformManifestDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("fields lost in decode: %+v", c)
	}
	if got := c.ExpectedDigest(); got != c.PlatformManifestDigest {
		t.Fatalf("ExpectedDigest = %q, want platform digest", got)
	}
	indexOnly := &MediaInput{IndexDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	if got := indexOnly.ExpectedDigest(); got != indexOnly.IndexDigest {
		t.Fatalf("ExpectedDigest without platform digest = %q, want index digest", got)
	}
	for _, bad := range []string{"", "   ", "{broken"} {
		if _, err := ParseMediaInput([]byte(bad)); err == nil {
			t.Fatalf("input %q accepted", bad)
		}
	}
}

func TestMediaInputValidate(t *testing.T) {
	valid := &MediaInput{
		Image:         "ghcr.io/example/os:stable",
		TargetImgref:  "ghcr.io/example/os:stable",
		OCILayoutPath: "/run/media/layout",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	for _, tt := range []struct {
		name string
		mi   *MediaInput
	}{
		{"nil", nil},
		{"bad platform digest", &MediaInput{PlatformManifestDigest: "sha256:zzz"}},
		{"bad index digest", &MediaInput{IndexDigest: "not-a-digest"}},
		{"relative layout path", &MediaInput{OCILayoutPath: "relative/path"}},
		{"image without day-2 ref", &MediaInput{Image: "ghcr.io/example/os:stable"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.mi.Validate(); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestCheckImageOCISourceNeverTouchesRegistry(t *testing.T) {
	old := SkopeoInspectFn
	defer func() { SkopeoInspectFn = old }()
	SkopeoInspectFn = func(args ...string) ([]byte, error) {
		t.Fatalf("OCI source queried the registry: %v", args)
		return nil, fmt.Errorf("must not be called")
	}
	layout, _ := writeOCILayoutFixture(t)
	good := CheckImage("oci:" + layout)
	if good.NeedsPull || !good.Offline || good.LayerCount != 1 {
		t.Fatalf("complete layout not accepted offline: %+v", good)
	}
	for _, bad := range []string{"oci:" + filepath.Join(layout, "no-such-dir"), "oci:"} {
		if got := CheckImage(bad); !got.NeedsPull {
			t.Fatalf("broken source %q accepted: %+v", bad, got)
		}
	}
}

func TestOCISourceBuildsBootcArgsStraightThrough(t *testing.T) {
	const layoutPath = "/run/media/layout"
	for _, composefs := range []bool{false, true} {
		args := BuildBootcArgs(Options{SourceImgref: "oci:" + layoutPath, ComposeFsBackend: composefs}, "ghcr.io/example/os:stable", "/target")
		joined := strings.Join(args, "\n")
		if !strings.Contains(joined, "--source-imgref\noci:"+layoutPath+"\n") {
			t.Fatalf("composefs=%v lost OCI layout source: %q", composefs, joined)
		}
		if !strings.Contains(joined, "--target-imgref\nghcr.io/example/os:stable\n") {
			t.Fatalf("composefs=%v lost day-2 target: %q", composefs, joined)
		}
	}
}

func TestOCISourceInstallKeepsDay2TargetWithoutPullOrExport(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".oci-install-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	log := filepath.Join(dir, "podman.argv")
	if err = os.WriteFile(filepath.Join(dir, "podman"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$CAPTURE\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("CAPTURE", log)
	oldExport := SkopeoExportOCIFn
	defer func() { SkopeoExportOCIFn = oldExport }()
	SkopeoExportOCIFn = func(string, string, string) error { t.Fatal("verified layout reached export"); return nil }
	layout, _ := writeOCILayoutFixture(t)
	const target = "ghcr.io/example/os:stable"
	err = BootcInstall(Options{SourceImgref: "oci:" + layout, TargetImgref: target, Target: filepath.Join(dir, "target"), ScratchDir: filepath.Join(dir, "scratch")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal("actual owned command was not executed:", err)
	}
	args := string(raw)
	if strings.Contains(args, "\npull\n") {
		t.Fatal("verified layout triggered pull")
	}
	if !strings.Contains(args, "oci:"+layout+"\nbootc\n") {
		t.Fatalf("container did not run bootc from the verified layout: %s", args)
	}
	if !strings.Contains(args, "--source-imgref\noci:"+layout+"\n") {
		t.Fatalf("bootc did not receive the layout source: %s", args)
	}
	if !strings.Contains(args, "--target-imgref\n"+target+"\n") {
		t.Fatalf("day-2 target lost: %s", args)
	}
}

func TestOCISourceWithNeedsPullRefusesRegistryFallback(t *testing.T) {
	old := SkopeoExportOCIFn
	defer func() { SkopeoExportOCIFn = old }()
	SkopeoExportOCIFn = func(string, string, string) error { t.Fatal("refused source reached export"); return nil }
	err := BootcInstall(Options{SourceImgref: "oci:/no/such/layout", NeedsPull: true, Target: t.TempDir(), ScratchDir: t.TempDir()})
	if err == nil {
		t.Fatal("missing OCI source accepted")
	}
}

func TestIsOCISourcePrefixGuard(t *testing.T) {
	if !IsOCISource("oci:/run/media/layout") {
		t.Fatal("oci: layout not recognised")
	}
	for _, other := range []string{"", "containers-storage:abc", "docker://ghcr.io/example/os:stable", "ghcr.io/example/os:stable", "OCI:/x"} {
		if IsOCISource(other) {
			t.Fatalf("%q misclassified as an OCI layout source", other)
		}
	}
}
