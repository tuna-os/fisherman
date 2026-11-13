// Package mediainput is the v1 offline-media contract between the live-media
// builder (tacklebox/SuperISO) and fisherman: the shape of media-input.json,
// how it is parsed, and the checks that need no I/O.
//
// It imports only the standard library so that read-only callers (probe)
// and the installer (install) can share it without either depending on the
// other.
package mediainput

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	// Exactly one of OCILayoutPath and ContainersStoragePath is set.
	OCILayoutPath string `json:"ociLayoutPath"`
	// ContainersStoragePath is the host-absolute path of a verified
	// read-only containers-storage graphroot holding the embedded image.
	// The install reads it as --source-imgref containers-storage:<Image>
	// (direct for ostree images, exported to an OCI cache for composefs).
	// v1 binds only the default graphroot: unpacked store files then share
	// squashfs blocks with the live root instead of duplicating it as an
	// opaque layer blob.
	ContainersStoragePath string `json:"containersStoragePath"`
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

// DefaultContainersStoragePath is the only offline store location the v1
// store-source contract binds: the default containers-storage graphroot.
// A builder embedding the unpacked store anywhere else fails closed.
const DefaultContainersStoragePath = "/var/lib/containers/storage"

// LiveMediaImageStore is the SuperISO offline image store. When it is
// mounted on the host, every install exposes it to bootc, and `fisherman
// probe` lists it among the offline stores.
const LiveMediaImageStore = "/var/lib/superiso-store"

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
// when set, a containers-storage path must be the default graphroot, and an
// embedded image must come with a day-2 update ref.
func (m *MediaInput) Validate() error {
	return m.ValidateBound(DefaultContainersStoragePath)
}

// ValidateBound is Validate with the accepted containers-storage root given
// explicitly. Production callers pass DefaultContainersStoragePath (or call
// Validate); the install gate passes its own root so its tests can bind a
// fixture store without a mutable root in this package.
func (m *MediaInput) ValidateBound(storeRoot string) error {
	if m == nil {
		return fmt.Errorf("media input is nil")
	}
	for _, d := range []struct{ name, value string }{
		{"platformManifestDigest", m.PlatformManifestDigest},
		{"indexDigest", m.IndexDigest},
		{"configDigest", m.ConfigDigest},
	} {
		if d.value != "" && !ValidSHA256Digest(d.value) {
			return fmt.Errorf("media input %s is not a sha256 digest: %q", d.name, d.value)
		}
	}
	if m.OCILayoutPath != "" && !filepath.IsAbs(m.OCILayoutPath) {
		return fmt.Errorf("media input ociLayoutPath is not absolute: %q", m.OCILayoutPath)
	}
	if m.OCILayoutPath != "" && m.ContainersStoragePath != "" {
		return fmt.Errorf("media input sets both an OCI layout and a containers-storage path")
	}
	if m.ContainersStoragePath != "" {
		if !filepath.IsAbs(m.ContainersStoragePath) || filepath.Clean(m.ContainersStoragePath) != m.ContainersStoragePath {
			return fmt.Errorf("media input containersStoragePath is not canonical absolute: %q", m.ContainersStoragePath)
		}
		if m.ContainersStoragePath != storeRoot {
			return fmt.Errorf("media input containersStoragePath %q is not the bound offline store", m.ContainersStoragePath)
		}
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
	m.ContainersStoragePath = get("containersstoragepath", "storepath", "containersstore", "store")
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

// ValidSHA256Digest reports whether digest is "sha256:" followed by 64
// lowercase hex digits.
func ValidSHA256Digest(digest string) bool {
	if len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	for _, ch := range digest[7:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
