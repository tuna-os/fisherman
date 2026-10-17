package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// MediaInput describes the immutable embedded image carried on offline
// (tacklebox/SuperISO) live media, as written by the ISO builder to
// /etc/bootc-installer/media-input.json (see probe.MediaInputPath).
//
// It is the v1 contract between the media builder and fisherman: which image
// is embedded, where its verified read-only OCI layout lives on the live
// system, which manifest to install, and which ref the installed system
// should track for day-2 updates.
type MediaInput struct {
	// SchemaVersion is the contract version. Builders write 1.
	SchemaVersion int `json:"schemaVersion"`
	// Kind names the media flavour (e.g. "tacklebox-live"). Opaque to fisherman.
	Kind string `json:"kind"`
	// Image is the embedded image ref (e.g. "ghcr.io/tuna-os/yellowfin:stable").
	Image string `json:"image"`
	// PlatformManifestDigest is the digest of the selected platform-specific
	// manifest in the on-disk OCI layout. Preferred over IndexDigest.
	PlatformManifestDigest string `json:"platformManifestDigest"`
	// IndexDigest is the digest of the top-level index in the OCI layout.
	IndexDigest string `json:"indexDigest"`
	// ConfigDigest is the digest of the image config blob. Informational.
	ConfigDigest string `json:"configDigest"`
	// OCILayoutPath is the host-absolute path of the verified read-only OCI
	// layout directory. Passed straight through to bootc as
	// --source-imgref oci:<OCILayoutPath>; never an additionalImageStore.
	OCILayoutPath string `json:"ociLayoutPath"`
	// TargetImgref is the update-tracking ref written into the installed
	// system for day-2 updates (--target-imgref).
	TargetImgref string `json:"targetImgref"`
	// Platform is the image platform as "os/arch" (e.g. "linux/amd64").
	Platform string `json:"platform"`
	// CatalogVersion is the builder's image-catalog version. Opaque.
	CatalogVersion string `json:"catalogVersion"`
	// QualificationID identifies the builder's qualification run. Opaque.
	QualificationID string `json:"qualificationID"`
}

// IsOCISource reports whether ref names a read-only on-disk OCI layout
// ("oci:<absolute path>").
func IsOCISource(ref string) bool {
	return strings.HasPrefix(ref, "oci:")
}

// ExpectedDigest returns the manifest digest the on-disk layout must
// contain: the platform-specific manifest when known, else the top-level
// index digest, else "" (accept whatever single/host-matching manifest the
// layout carries).
func (m *MediaInput) ExpectedDigest() string {
	if m == nil {
		return ""
	}
	if m.PlatformManifestDigest != "" {
		return m.PlatformManifestDigest
	}
	return m.IndexDigest
}

// Validate checks the machine-readable shapes of a media input: digest
// fields must be sha256 digests when set, the layout path must be absolute
// when set, and an embedded image must come with a day-2 update ref.
func (m *MediaInput) Validate() error {
	if m == nil {
		return fmt.Errorf("media input is nil")
	}
	for _, d := range []struct{ name, value string }{
		{"platformManifestDigest", m.PlatformManifestDigest},
		{"indexDigest", m.IndexDigest},
		{"configDigest", m.ConfigDigest},
	} {
		if d.value != "" && !validSHA256Digest(d.value) {
			return fmt.Errorf("media input %s is not a sha256 digest: %q", d.name, d.value)
		}
	}
	if m.OCILayoutPath != "" && !filepath.IsAbs(m.OCILayoutPath) {
		return fmt.Errorf("media input ociLayoutPath is not absolute: %q", m.OCILayoutPath)
	}
	if m.Image != "" && m.TargetImgref == "" {
		return fmt.Errorf("media input names image %q but no targetImgref for day-2 updates", m.Image)
	}
	return nil
}

// normalizeMediaKey folds the key spellings builders use (camelCase,
// snake_case, kebab-case, any case) to one canonical form.
func normalizeMediaKey(k string) string {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "_", "")
	k = strings.ReplaceAll(k, "-", "")
	return k
}

// mediaString decodes a JSON string (or, leniently, a number) value.
func mediaString(rm json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(rm, &s) == nil {
		return strings.TrimSpace(s), true
	}
	var f float64
	if json.Unmarshal(rm, &f) == nil {
		if f == float64(int64(f)) {
			return strconv.FormatInt(int64(f), 10), true
		}
		return strconv.FormatFloat(f, 'f', -1, 64), true
	}
	return "", false
}

// ParseMediaInput decodes media-input.json. It accepts the documented
// camelCase keys as well as snake_case/kebab-case spellings, so builders and
// tests are not coupled to one exact casing. Syntactic checks belong to
// Validate; Parse only reports malformed JSON.
func ParseMediaInput(data []byte) (*MediaInput, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("empty media input")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing media input: %w", err)
	}
	folded := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		folded[normalizeMediaKey(k)] = v
	}
	m := &MediaInput{}
	get := func(keys ...string) string {
		for _, k := range keys {
			if rm, ok := folded[k]; ok {
				if s, ok := mediaString(rm); ok {
					return s
				}
			}
		}
		return ""
	}
	m.Kind = get("kind")
	m.Image = get("image")
	m.PlatformManifestDigest = get("platformmanifestdigest", "manifestdigest")
	m.IndexDigest = get("indexdigest")
	m.ConfigDigest = get("configdigest")
	m.OCILayoutPath = get("ocilayoutpath", "layoutpath", "ocilayout", "layout")
	m.TargetImgref = get("targetimgref", "target")
	m.Platform = get("platform", "osarch")
	m.CatalogVersion = get("catalogversion")
	m.QualificationID = get("qualificationid", "qualification")
	if v := get("schemaversion", "version"); v != "" {
		v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
		if n, err := strconv.Atoi(v); err == nil {
			m.SchemaVersion = n
		} else {
			return nil, fmt.Errorf("media input schemaVersion is not a number: %q", v)
		}
	}
	return m, nil
}

// ociDescriptor is one entry of an OCI image index or manifest.
type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
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
	if err := m.Validate(); err != nil {
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
	return PreflightOCILayout(m.OCILayoutPath, m.ExpectedDigest())
}
