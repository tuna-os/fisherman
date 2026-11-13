package install

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// ImageFacts is what `fisherman plan --read-image` learns about an install
// source without pulling it: the identity and the sizes its manifest and,
// for a local copy, its store report. It reads manifests, configurations
// and store metadata only, never a layer, and writes nothing.
type ImageFacts struct {
	// ReadFrom is where the facts come from: "containers-storage" (a local
	// copy, which the install uses as it is), "oci" (an OCI layout on
	// disk), "registry" (the registry's manifest; the install pulls it), or
	// "" when nothing could be read (Reason says why).
	ReadFrom string
	// ManifestDigest is the digest of the manifest the reference names: an
	// image index for a multi-platform image, else the image manifest.
	ManifestDigest string
	// PlatformDigest is the image manifest for this host's platform (the
	// same as ManifestDigest for a single-platform image). Registry only;
	// empty when it could not be read.
	PlatformDigest string
	// Platform is the os/architecture of the image's configuration.
	Platform string
	// Layers is the number of layers; 0 when unknown.
	Layers int
	// CompressedBytes is the sum of the layer sizes the host platform's
	// manifest lists: what a pull downloads. 0 when a size is missing.
	CompressedBytes int64
	// UnpackedBytes is the unpacked size of the layers: what local
	// containers-storage reports (podman's image Size), the sum of the
	// layers' uncompressed-size annotations a registry manifest or an OCI
	// layout's manifest lists, or, for an image without them, an estimate
	// from the compressed size (UnpackedEstimateRatio). UnpackedFrom says
	// which. 0 when unknown (no compressed size to estimate from).
	UnpackedBytes int64
	// UnpackedFrom is where UnpackedBytes comes from: "containers-storage",
	// "annotations" or "estimate"; "" when it is unknown.
	UnpackedFrom string
	// Reason says why ReadFrom is "": the store has no copy and the
	// registry was not asked, or did not answer.
	Reason string
}

// UncompressedSizeAnnotation is the layer annotation that records a
// layer's uncompressed size (containers/storage's estargz support writes
// it). A registry manifest whose every layer has it gives the exact
// unpacked size without a pull.
const UncompressedSizeAnnotation = "io.containers.estargz.uncompressed-size"

// UnpackedEstimateRatio is the factor plan applies to the compressed
// layer sizes of a registry image whose manifest records no uncompressed
// size: OS images (gzip or zstd layers) unpack to two to three times what
// they download, so 3 errs on the side of needing more room. The result
// is flagged "estimate" (UnpackedFrom).
const UnpackedEstimateRatio = 3

// PlanInspectFn runs a read-only image query (`skopeo inspect ...`,
// `podman image inspect ...`) for `plan --read-image` and returns its
// stdout. Unlike SkopeoInspectFn it echoes nothing: the plan's stdout is
// one JSON object. Tests replace it so they never touch a store or the
// network.
var PlanInspectFn = func(name string, args ...string) ([]byte, error) {
	return Exec.Command(name, args...).Output()
}

// planImageInfo is the part of `skopeo inspect` output the plan reads:
// imageInfo plus the layer sizes (skopeo 1.6 and later print LayersData).
type planImageInfo struct {
	imageInfo
	LayersData []struct {
		Size        int64             `json:"Size"`
		Annotations map[string]string `json:"Annotations"`
	} `json:"LayersData"`
}

// annotatedUnpackedBytes is the sum of the layers' uncompressed-size
// annotations, or 0 unless every layer has a valid one.
func (m planImageInfo) annotatedUnpackedBytes() int64 {
	if len(m.LayersData) == 0 || (len(m.Layers) != 0 && len(m.LayersData) != len(m.Layers)) {
		return 0
	}
	anns := make([]map[string]string, 0, len(m.LayersData))
	for _, l := range m.LayersData {
		anns = append(anns, l.Annotations)
	}
	return sumUncompressedSizes(anns)
}

// sumUncompressedSizes is the sum of each layer's uncompressed-size
// annotation, one map per layer, or 0 unless every layer has a valid one.
func sumUncompressedSizes(layers []map[string]string) int64 {
	if len(layers) == 0 {
		return 0
	}
	var n int64
	for _, a := range layers {
		v, err := strconv.ParseInt(a[UncompressedSizeAnnotation], 10, 64)
		if err != nil || v <= 0 || n > math.MaxInt64-v {
			return 0
		}
		n += v
	}
	return n
}

// unpackedFromManifest is the unpacked size of an image from its manifest
// alone: annotated (the sum of the layers' uncompressed-size annotations)
// when it is not 0, else the compressed size times UnpackedEstimateRatio.
func unpackedFromManifest(annotated, compressed int64) (int64, string) {
	if annotated > 0 {
		return annotated, "annotations"
	}
	if compressed > 0 && compressed <= math.MaxInt64/UnpackedEstimateRatio {
		return compressed * UnpackedEstimateRatio, "estimate"
	}
	return 0, ""
}

// registryUnpacked is the unpacked size of a registry image from its
// manifest alone (unpackedFromManifest).
func (m planImageInfo) registryUnpacked(compressed int64) (int64, string) {
	return unpackedFromManifest(m.annotatedUnpackedBytes(), compressed)
}

// inspectOCILayoutForPlan reads what the plan reports about the OCI layout
// at layoutPath from disk: its oci-layout and index.json, the manifest the
// install selects (selectOCIManifest) and its configuration. It never
// opens a layer blob and writes nothing. The unpacked size is the sum of
// the layers' uncompressed-size annotations when every layer has one, else
// the compressed size times UnpackedEstimateRatio, as for a registry
// image.
func inspectOCILayoutForPlan(layoutPath string) (ImageFacts, error) {
	if strings.TrimSpace(layoutPath) == "" {
		return ImageFacts{}, fmt.Errorf("empty OCI layout path")
	}
	layoutPath = filepath.Clean(layoutPath)
	layoutRaw, err := os.ReadFile(filepath.Join(layoutPath, "oci-layout"))
	if err != nil {
		return ImageFacts{}, fmt.Errorf("reading oci-layout in %s: %w", layoutPath, err)
	}
	var layout struct {
		ImageLayoutVersion string `json:"imageLayoutVersion"`
	}
	if err := json.Unmarshal(layoutRaw, &layout); err != nil || layout.ImageLayoutVersion != "1.0.0" {
		return ImageFacts{}, fmt.Errorf("%s is not an OCI layout of version 1.0.0", layoutPath)
	}
	indexRaw, err := os.ReadFile(filepath.Join(layoutPath, "index.json"))
	if err != nil {
		return ImageFacts{}, fmt.Errorf("reading index.json in %s: %w", layoutPath, err)
	}
	var index ociIndex
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		return ImageFacts{}, fmt.Errorf("parsing index.json in %s: %w", layoutPath, err)
	}
	if len(index.Manifests) == 0 {
		return ImageFacts{}, fmt.Errorf("index.json in %s lists no manifests", layoutPath)
	}
	selected, err := selectOCIManifest(index.Manifests, "")
	if err != nil {
		return ImageFacts{}, fmt.Errorf("index.json in %s: %w", layoutPath, err)
	}
	if !validSHA256Digest(selected.Digest) {
		return ImageFacts{}, fmt.Errorf("index.json in %s names the manifest %q, not a sha256 digest", layoutPath, selected.Digest)
	}
	manifestRaw, err := readOCIBlob(layoutPath, selected, "manifest "+selected.Digest)
	if err != nil {
		return ImageFacts{}, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return ImageFacts{}, fmt.Errorf("parsing manifest %s in %s: %w", selected.Digest, layoutPath, err)
	}
	f := ImageFacts{ReadFrom: "oci", ManifestDigest: selected.Digest, Layers: len(manifest.Layers)}
	if manifest.Config.Digest != "" {
		if raw, err := readOCIBlob(layoutPath, manifest.Config, "config "+manifest.Config.Digest); err == nil {
			var cfg struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			}
			if json.Unmarshal(raw, &cfg) == nil && (cfg.OS != "" || cfg.Architecture != "") {
				f.Platform = cfg.OS + "/" + cfg.Architecture
			}
		}
	}
	anns := make([]map[string]string, 0, len(manifest.Layers))
	sized := true
	for _, l := range manifest.Layers {
		anns = append(anns, l.Annotations)
		if l.Size <= 0 || f.CompressedBytes > math.MaxInt64-l.Size {
			sized = false
			continue
		}
		f.CompressedBytes += l.Size
	}
	if !sized {
		f.CompressedBytes = 0 // a descriptor without a size: the sum is not the image's
	}
	f.UnpackedBytes, f.UnpackedFrom = unpackedFromManifest(sumUncompressedSizes(anns), f.CompressedBytes)
	return f, nil
}

func (m planImageInfo) compressedBytes() int64 {
	if len(m.LayersData) == 0 || (len(m.Layers) != 0 && len(m.LayersData) != len(m.Layers)) {
		return 0
	}
	var n int64
	for _, l := range m.LayersData {
		if l.Size <= 0 {
			return 0 // the store or the registry did not say
		}
		n += l.Size
	}
	return n
}

func (m planImageInfo) layers() int {
	if len(m.Layers) > 0 {
		return len(m.Layers)
	}
	return len(m.LayersData)
}

// skopeoPlanInspect runs `skopeo inspect <args>` through PlanInspectFn.
func skopeoPlanInspect(args ...string) ([]byte, error) {
	return PlanInspectFn("skopeo", append([]string{"inspect"}, args...)...)
}

// inspectLocalForPlan reads a local copy in containers-storage. ok is false
// when there is none.
func inspectLocalForPlan(ref string) (ImageFacts, bool) {
	out, err := skopeoPlanInspect(ref)
	var m planImageInfo
	if err != nil || json.Unmarshal(out, &m) != nil || !validSHA256Digest(m.Digest) {
		return ImageFacts{}, false
	}
	f := ImageFacts{ReadFrom: "containers-storage", ManifestDigest: m.Digest, Platform: m.platform(),
		Layers: m.layers(), CompressedBytes: m.compressedBytes()}
	f.UnpackedBytes = localUnpackedBytes(strings.TrimPrefix(ref, "containers-storage:"))
	if f.UnpackedBytes > 0 {
		f.UnpackedFrom = "containers-storage"
	}
	return f, true
}

// localUnpackedBytes is the size containers-storage reports for the
// unpacked image (`podman image inspect`), or 0.
func localUnpackedBytes(ref string) int64 {
	out, err := PlanInspectFn("podman", "image", "inspect", "--format", "{{.Size}}", ref)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// InspectImageForPlan reads what the plan reports about image without a
// pull: an OCI layout from its files on disk (inspectOCILayoutForPlan), a
// containers-storage: source from its store, neither from a registry; a registry reference from local containers-storage
// first (the copy the install would use) and, when registry is true and
// there is none, from the registry's manifest, once and with no retry. It
// never reads a layer and never writes.
func InspectImageForPlan(image string, registry bool) ImageFacts {
	switch {
	case IsOCISource(image):
		// The install takes everything after oci: as the layout's path.
		f, err := inspectOCILayoutForPlan(strings.TrimPrefix(image, "oci:"))
		if err != nil {
			return ImageFacts{Reason: "the OCI layout cannot be read: " + err.Error()}
		}
		return f
	case strings.HasPrefix(image, "containers-storage:"):
		if f, ok := inspectLocalForPlan(image); ok {
			return f
		}
		return ImageFacts{Reason: "local containers-storage has no copy of the image (as the user plan runs as)"}
	}
	ref := bareImageRef(image)
	if f, ok := inspectLocalForPlan("containers-storage:" + ref); ok {
		return f
	}
	if !registry {
		return ImageFacts{Reason: "local containers-storage has no copy of the image, and plan reads no registry"}
	}
	out, err := skopeoPlanInspect("--no-tags", "docker://"+ref)
	if err != nil {
		return ImageFacts{Reason: planRemoteReason(ref, err)}
	}
	var m planImageInfo
	if json.Unmarshal(out, &m) != nil || !validSHA256Digest(m.Digest) {
		return ImageFacts{Reason: fmt.Sprintf("the registry's answer for %s has no usable manifest digest", ref)}
	}
	f := ImageFacts{ReadFrom: "registry", ManifestDigest: m.Digest, Platform: m.platform(),
		Layers: m.layers(), CompressedBytes: m.compressedBytes()}
	f.UnpackedBytes, f.UnpackedFrom = m.registryUnpacked(f.CompressedBytes)
	f.PlatformDigest, _ = platformDigest(skopeoPlanInspect, ref, m.Digest)
	return f
}

// planRemoteReason says why the registry gave no manifest, in the words of
// the install's own image check (classifyInspectError).
func planRemoteReason(ref string, err error) string {
	code, _ := classifyInspectError(err)
	detail := inspectDetail(err)
	switch code {
	case recipe.CodeImageNotFound:
		return fmt.Sprintf("the registry does not have %s (%s)", ref, detail)
	case recipe.CodeImageSourceUnauthorized:
		return fmt.Sprintf("the registry refused access to %s (%s)", ref, detail)
	case recipe.CodeImagePlatformMismatch:
		return fmt.Sprintf("%s has no build for this host, linux/%s (%s)", ref, HostArchitecture(), detail)
	case recipe.CodeImageSourceUnreachable:
		return fmt.Sprintf("the registry of %s cannot be reached (%s)", ref, detail)
	}
	return fmt.Sprintf("cannot inspect %s (%s)", ref, detail)
}
