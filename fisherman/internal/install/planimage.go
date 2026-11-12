package install

import (
	"encoding/json"
	"fmt"
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
	// UnpackedBytes is the size local containers-storage reports for the
	// unpacked layers (podman's image Size). 0 when unknown: a registry
	// manifest and an OCI layout list compressed sizes only.
	UnpackedBytes int64
	// Reason says why ReadFrom is "": the store has no copy and the
	// registry was not asked, or did not answer.
	Reason string
}

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
		Size int64 `json:"Size"`
	} `json:"LayersData"`
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

// inspectLocalForPlan reads a local copy: an OCI layout or a
// containers-storage image. ok is false when there is none.
func inspectLocalForPlan(ref, readFrom string) (ImageFacts, bool) {
	out, err := skopeoPlanInspect(ref)
	var m planImageInfo
	if err != nil || json.Unmarshal(out, &m) != nil || !validSHA256Digest(m.Digest) {
		return ImageFacts{}, false
	}
	f := ImageFacts{ReadFrom: readFrom, ManifestDigest: m.Digest, Platform: m.platform(),
		Layers: m.layers(), CompressedBytes: m.compressedBytes()}
	if readFrom == "containers-storage" {
		f.UnpackedBytes = localUnpackedBytes(strings.TrimPrefix(ref, "containers-storage:"))
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
// pull: a local source (oci:, containers-storage:) from its store only,
// never a registry; a registry reference from local containers-storage
// first (the copy the install would use) and, when registry is true and
// there is none, from the registry's manifest, once and with no retry. It
// never reads a layer and never writes.
func InspectImageForPlan(image string, registry bool) ImageFacts {
	switch {
	case IsOCISource(image):
		if f, ok := inspectLocalForPlan(image, "oci"); ok {
			return f
		}
		return ImageFacts{Reason: "the OCI layout cannot be read"}
	case strings.HasPrefix(image, "containers-storage:"):
		if f, ok := inspectLocalForPlan(image, "containers-storage"); ok {
			return f
		}
		return ImageFacts{Reason: "local containers-storage has no copy of the image (as the user plan runs as)"}
	}
	ref := bareImageRef(image)
	if f, ok := inspectLocalForPlan("containers-storage:"+ref, "containers-storage"); ok {
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
