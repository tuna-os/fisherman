package probe

import (
	"fmt"
	"regexp"
	"strings"
)

// Source reuse (tuna-os/fisherman#384, phase 2): which bootc-owned image of
// the live host a recipe's sourceReuse names, and whether the host is one
// the reuse is qualified on. ReuseSource only reads, exactly what the
// default probe reads (`bootc status --json`, the live-media signals and
// bootc's store index as a file); the copy itself is internal/reuse, which
// only the install runs.

// Reuse reason codes (ReuseSource.Reason). The CLI maps each to a
// source_reuse_* validate code.
const (
	ReuseEligible = "eligible"
	// ReuseHostNotBootc: bootc_store.available is false: no bootc, bootc
	// status failed or was malformed, or the host did not boot a bootc
	// image (a container, a package-based host). HostReason says which.
	ReuseHostNotBootc = "host_not_bootc"
	// ReuseLiveMedia: the host booted from live media. bootc refuses
	// copy-to-storage on a read-only sysroot, and fisherman does not
	// qualify a write to the live medium's storage.
	ReuseLiveMedia = "live_media"
	// ReuseBackendNotQualified: the booted entry is not ostree. On
	// composefs bootc 1.17.1 exports through another implementation and
	// mounts the ESP read-write; no profile qualifies it.
	ReuseBackendNotQualified = "backend_not_qualified"
	// ReuseItemNotFound: no ostree deployment of the host has the digest.
	// An image only in bootc's containers-storage (logically bound images)
	// or only named by `bootc image list` is not a source copy-to-storage
	// can export.
	ReuseItemNotFound = "item_not_found"
	// ReuseItemUnverified: the deployment has the digest, but its
	// reference is redacted or invalid, its platform is unknown, its
	// transport is one fisherman does not name, or its ostree commit is
	// not reported.
	ReuseItemUnverified = "item_unverified"
	// ReusePlatformMismatch: the item's platform is not the recipe's, or
	// not this machine's.
	ReusePlatformMismatch = "platform_mismatch"
)

// OstreeRepo is the live host's ostree repository.
const OstreeRepo = "/sysroot/ostree/repo"

// ReuseSource is the read-only answer for one sourceReuse.
type ReuseSource struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
	// HostReason is bootc_store.reason when the host is not bootc-booted.
	HostReason string `json:"host_reason,omitempty"`
	// Candidates are the deployments that carry the digest, in the order
	// booted, staged, rollback, others. The install uses the first whose
	// ostree image ref still points at its commit.
	Candidates []ReuseCandidate `json:"candidates"`
}

// ReuseCandidate is one ostree deployment of the item.
type ReuseCandidate struct {
	Role string `json:"role"`
	// Ref is the image reference without a transport, as the inventory
	// reports it (credential-free).
	Ref string `json:"ref"`
	// Transport is bootc's transport token ("registry", ...).
	Transport string `json:"transport"`
	// SourceRef is the reference passed to `bootc image copy-to-storage
	// --source`: transport and name, in ostree-ext's own spelling.
	SourceRef string `json:"source_ref"`
	// OstreeRef is the ref ostree-ext keeps the image under; the copy
	// reads the commit it points to.
	OstreeRef string `json:"ostree_ref"`
	// Checksum, Stateroot and Serial name the deployment's commit and
	// checkout (/sysroot/ostree/deploy/<stateroot>/deploy/<checksum>.<serial>).
	Checksum  string `json:"checksum"`
	Stateroot string `json:"stateroot"`
	Serial    int    `json:"serial"`
	Digest    string `json:"digest"`
	Platform  string `json:"platform"`
}

// DeployPath is the deployment's checkout on the host.
func (c ReuseCandidate) DeployPath() string {
	return fmt.Sprintf("/sysroot/ostree/deploy/%s/deploy/%s.%d", c.Stateroot, c.Checksum, c.Serial)
}

var (
	ostreeChecksumRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	stateRootRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// reuseTransports maps bootc's transport token to ostree-ext's
// ImageReference spelling (its Display), which both `--source` and the
// image's ostree ref use. A registry image prints as docker://.
var reuseTransports = map[string]string{
	"registry":           "docker://",
	"containers-storage": "containers-storage:",
	"oci":                "oci:",
	"oci-archive":        "oci-archive:",
}

// ReuseSource resolves a sourceReuse against the live host. It needs the
// host to be bootc-booted (bootc_store.available), installed rather than
// live media, on the ostree backend, with an ostree deployment of the
// digest whose platform is platform and this machine's.
func (p *Prober) ReuseSource(digest, platform string) ReuseSource {
	r := ReuseSource{Candidates: []ReuseCandidate{}}
	live := p.Live()
	store := p.BootcStore(Capabilities{}, live)
	st := p.hostStatus()
	switch {
	case !store.Available:
		r.Reason, r.HostReason = ReuseHostNotBootc, store.Reason
		return r
	case live.IsLive:
		r.Reason = ReuseLiveMedia
		return r
	case st.backend != BackendOstree:
		r.Reason = ReuseBackendNotQualified
		return r
	}
	var item *BootcStoreItem
	for i := range store.Items {
		it := &store.Items[i]
		if it.Storage == BackendOstree && it.Availability == AvailDeployed && it.Digest == digest {
			if item == nil || it.MatchesMachine {
				item = it
			}
		}
	}
	switch {
	case item == nil:
		r.Reason = ReuseItemNotFound
		return r
	case item.MismatchReason == MismatchPlatformDiffers || (item.Platform != "" && item.Platform != platform):
		r.Reason = ReusePlatformMismatch
		return r
	case !item.MatchesMachine:
		r.Reason = ReuseItemUnverified
		return r
	}
	for _, d := range st.deployments {
		if d.backend != BackendOstree || d.digest != digest || d.arch != platform {
			continue
		}
		ref, reason := inventoryRef(d.image)
		prefix, known := reuseTransports[d.transport]
		if reason != "" || !known || !ostreeChecksumRe.MatchString(d.checksum) ||
			!stateRootRe.MatchString(d.stateroot) || d.serial < 0 {
			continue
		}
		src := prefix + ref
		c := ReuseCandidate{Role: d.role, Ref: ref, Transport: d.transport, SourceRef: src,
			OstreeRef: OstreeImageRef(src), Checksum: d.checksum, Stateroot: d.stateroot,
			Serial: d.serial, Digest: d.digest, Platform: d.arch}
		r.Candidates = append(r.Candidates, c)
	}
	if len(r.Candidates) == 0 {
		r.Reason = ReuseItemUnverified
		return r
	}
	r.Eligible, r.Reason = true, ReuseEligible
	return r
}

// OstreeImageRef is the ostree ref ostree-ext keeps an image under:
// "ostree/container/image/" and the image reference escaped with
// ostree-ext's refescape (bootc v1.17.1 crates/ostree-ext/src/refescape.rs):
// ASCII letters and digits pass, "-" passes, "/" passes after a letter or
// digit when more follows, "_" doubles, and every other character becomes
// _XX_ with its code point in upper-case hex.
func OstreeImageRef(imageRef string) string {
	var b strings.Builder
	runes := []rune(imageRef)
	prevAlnum := false
	for i, c := range runes {
		alnum := c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
		switch {
		case alnum:
			b.WriteRune(c)
		case c == '/' && prevAlnum && i+1 < len(runes):
			b.WriteRune(c)
		case c == '-':
			b.WriteRune(c)
		case c == '_':
			b.WriteString("__")
		default:
			fmt.Fprintf(&b, "_%02X_", c)
		}
		prevAlnum = alnum
	}
	return "ostree/container/image/" + b.String()
}
