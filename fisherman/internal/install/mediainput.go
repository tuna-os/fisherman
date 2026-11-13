package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tuna-os/fisherman/internal/mediainput"
)

// MediaInput is the offline-media contract; it is defined in
// internal/mediainput so read-only callers need not import install.
type MediaInput = mediainput.MediaInput

// IsOCISource reports whether ref names a read-only on-disk OCI layout
// ("oci:<absolute path>"). See mediainput.IsOCISource.
func IsOCISource(ref string) bool {
	return mediainput.IsOCISource(ref)
}

// ParseMediaInput decodes media-input.json. See mediainput.ParseMediaInput.
func ParseMediaInput(data []byte) (*MediaInput, error) {
	return mediainput.ParseMediaInput(data)
}

// defaultContainersStoragePath is the only offline store location the v1
// store-source contract binds.
const defaultContainersStoragePath = mediainput.DefaultContainersStoragePath

// boundOfflineStoreRoot is the store root the bound-store gate accepts.
// The production value is defaultContainersStoragePath; tests substitute a
// fixture directory (the read-only check is separately stubbed).
var boundOfflineStoreRoot = defaultContainersStoragePath

// ociDescriptor is one entry of an OCI image index or manifest.
type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	// Annotations: a layer's io.containers.estargz.uncompressed-size,
	// which `plan --read-image` reads (UncompressedSizeAnnotation).
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform,omitempty"`
}

// ociIndex is the top-level index.json of an OCI layout.
type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Manifests     []ociDescriptor `json:"manifests"`
}

// ociManifest is one image manifest inside an OCI layout.
type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

// PreflightOCILayout verifies that layoutPath holds a complete, readable OCI
// layout for this host before any disk is touched:
//
//   - an oci-layout file declaring imageLayoutVersion "1.0.0",
//   - a parseable index.json whose selected manifest digest equals
//     expectedDigest when one is given,
//   - a selected manifest whose declared platform matches this host,
//   - every blob the manifest references present under
//     blobs/<alg>/<hex> with the descriptor's size.
//
// Any missing, corrupt or mismatched input is an error; the caller refuses
// the installation (never a registry pull).
func PreflightOCILayout(layoutPath, expectedDigest string) error {
	_, err := inspectOCILayout(layoutPath, expectedDigest)
	return err
}

// inspectOCILayout is PreflightOCILayout, also reporting the manifest's layer
// count for progress display.
func inspectOCILayout(layoutPath, expectedDigest string) (int, error) {
	if strings.TrimSpace(layoutPath) == "" {
		return 0, fmt.Errorf("empty OCI layout path")
	}
	layoutPath = filepath.Clean(layoutPath)

	layoutRaw, err := os.ReadFile(filepath.Join(layoutPath, "oci-layout"))
	if err != nil {
		return 0, fmt.Errorf("reading oci-layout in %s: %w", layoutPath, err)
	}
	var layout struct {
		ImageLayoutVersion string `json:"imageLayoutVersion"`
	}
	if err := json.Unmarshal(layoutRaw, &layout); err != nil {
		return 0, fmt.Errorf("parsing oci-layout in %s: %w", layoutPath, err)
	}
	if layout.ImageLayoutVersion != "1.0.0" {
		return 0, fmt.Errorf("unsupported oci-layout version %q in %s (want \"1.0.0\")",
			layout.ImageLayoutVersion, layoutPath)
	}

	indexRaw, err := os.ReadFile(filepath.Join(layoutPath, "index.json"))
	if err != nil {
		return 0, fmt.Errorf("reading index.json in %s: %w", layoutPath, err)
	}
	var index ociIndex
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		return 0, fmt.Errorf("parsing index.json in %s: %w", layoutPath, err)
	}
	if len(index.Manifests) == 0 {
		return 0, fmt.Errorf("index.json in %s lists no manifests", layoutPath)
	}
	selected, err := selectOCIManifest(index.Manifests, expectedDigest)
	if err != nil {
		return 0, fmt.Errorf("index.json in %s: %w", layoutPath, err)
	}
	if p := selected.Platform; p != nil && (p.OS != "" || p.Architecture != "") {
		if p.OS != runtime.GOOS || p.Architecture != runtime.GOARCH {
			return 0, fmt.Errorf("selected manifest %s targets platform %s/%s, this host is %s/%s",
				selected.Digest, p.OS, p.Architecture, runtime.GOOS, runtime.GOARCH)
		}
	}

	manifestRaw, err := readOCIBlob(layoutPath, selected, "manifest "+selected.Digest)
	if err != nil {
		return 0, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return 0, fmt.Errorf("parsing manifest %s in %s: %w", selected.Digest, layoutPath, err)
	}
	if manifest.Config.Digest == "" {
		return 0, fmt.Errorf("manifest %s in %s has no config", selected.Digest, layoutPath)
	}
	if _, err := readOCIBlob(layoutPath, manifest.Config, "config "+manifest.Config.Digest); err != nil {
		return 0, err
	}
	if len(manifest.Layers) == 0 {
		return 0, fmt.Errorf("manifest %s in %s has no layers", selected.Digest, layoutPath)
	}
	for i, layer := range manifest.Layers {
		if _, err := readOCIBlob(layoutPath, layer, fmt.Sprintf("layer %d (%s)", i, layer.Digest)); err != nil {
			return 0, err
		}
	}
	return len(manifest.Layers), nil
}

// selectOCIManifest picks the manifest to install: the one matching
// expectedDigest when given, else the single manifest, else the one for this
// host's platform.
func selectOCIManifest(manifests []ociDescriptor, expected string) (ociDescriptor, error) {
	if expected != "" {
		for _, m := range manifests {
			if m.Digest == expected {
				return m, nil
			}
		}
		return ociDescriptor{}, fmt.Errorf("expected manifest %s is not present in the layout", expected)
	}
	if len(manifests) == 1 {
		return manifests[0], nil
	}
	for _, m := range manifests {
		if p := m.Platform; p != nil && p.OS == runtime.GOOS && p.Architecture == runtime.GOARCH {
			return m, nil
		}
	}
	for _, m := range manifests {
		if p := m.Platform; p == nil || (p.OS == "" && p.Architecture == "") {
			return m, nil
		}
	}
	return ociDescriptor{}, fmt.Errorf("layout has %d manifests and none matches this host (%s/%s)",
		len(manifests), runtime.GOOS, runtime.GOARCH)
}

// readOCIBlob reads the blob a descriptor points at and checks its size
// against the descriptor when the descriptor carries one.
func readOCIBlob(layoutPath string, desc ociDescriptor, what string) ([]byte, error) {
	alg, hex, ok := strings.Cut(desc.Digest, ":")
	if !ok || alg == "" || hex == "" || strings.Contains(hex, "/") {
		return nil, fmt.Errorf("malformed blob digest %q for %s in %s", desc.Digest, what, layoutPath)
	}
	blobPath := filepath.Join(layoutPath, "blobs", alg, hex)
	raw, err := os.ReadFile(blobPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s from %s: %w", what, blobPath, err)
	}
	if desc.Size > 0 && int64(len(raw)) != desc.Size {
		return nil, fmt.Errorf("%s in %s has size %d, descriptor says %d",
			what, blobPath, len(raw), desc.Size)
	}
	return raw, nil
}

// PreflightMediaInput validates m and verifies its OCI layout on disk,
// including the platform match. It is the one-call gate for a bound media
// input; main.go spells out the same steps via Validate, a platform check
// and PreflightOCILayout.
func PreflightMediaInput(m *MediaInput) error {
	if m == nil {
		return fmt.Errorf("no media input bound")
	}
	if err := m.ValidateBound(boundOfflineStoreRoot); err != nil {
		return err
	}
	if m.Platform != "" {
		os_, arch, _ := strings.Cut(m.Platform, "/")
		if arch == "" {
			arch = os_
			os_ = runtime.GOOS
		}
		if os_ != runtime.GOOS || arch != runtime.GOARCH {
			return fmt.Errorf("media platform %q does not match this host (%s/%s)",
				m.Platform, runtime.GOOS, runtime.GOARCH)
		}
	}
	if m.ContainersStoragePath != "" {
		return PreflightStoreMediaInput(m)
	}
	return PreflightOCILayout(m.OCILayoutPath, m.ExpectedDigest())
}

// PreflightStoreMediaInput validates m and verifies the embedded image
// resolves in the bound default containers-storage with the receipt digest.
// Fail-closed like the OCI gate: a missing store, an unresolvable image,
// or a digest mismatch refuses the installation (never a registry pull).
func PreflightStoreMediaInput(m *MediaInput) error {
	if m == nil {
		return fmt.Errorf("no media input bound")
	}
	if err := m.ValidateBound(boundOfflineStoreRoot); err != nil {
		return err
	}
	if m.ContainersStoragePath == "" {
		return fmt.Errorf("no containers-storage path bound")
	}
	return inspectStoreDigest(m.Image, m.ExpectedDigest())
}

// inspectStoreDigest resolves image in the default containers-storage and
// requires its manifest digest to equal expected when expected is given.
func inspectStoreDigest(image, expected string) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("no embedded image bound")
	}
	out, err := SkopeoInspectFn("containers-storage:" + image)
	if err != nil {
		return fmt.Errorf("inspecting embedded image %q: %w", image, err)
	}
	var v struct {
		Digest string `json:"Digest"`
	}
	if err := json.Unmarshal(out, &v); err != nil || !validSHA256Digest(v.Digest) {
		return fmt.Errorf("embedded image %q has no valid digest", image)
	}
	if expected != "" && v.Digest != expected {
		return fmt.Errorf("embedded image digest %s does not match media input %s", v.Digest, expected)
	}
	return nil
}
