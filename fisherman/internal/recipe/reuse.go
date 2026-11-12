package recipe

import (
	"fmt"
	"regexp"
	"strings"
)

// SourceReuse asks the install to take its source from an image the live
// host's own bootc already keeps, instead of pulling it or reading a store
// the recipe names (tuna-os/fisherman#384, phase 2). It is an explicit,
// opt-in acquisition step: fisherman runs `bootc image copy-to-storage`
// with an explicit source and destination, verifies the copy, installs
// from it, and removes it again. See docs/SOURCE-REUSE.md.
//
// The item is named by its immutable identity, the manifest digest that
// `fisherman probe --json` reports in bootc_store.items[].digest, never by
// a name: a tag on the live host can move, and two images can share one.
// The recipe's `image` stays empty, and `targetImgref` is required: the
// installation source (the copy) and the reference the installed system
// follows for updates are never derived from one another.
type SourceReuse struct {
	// Store is the storage the item lives in. Only "bootc" (the live
	// host's bootc-owned ostree storage) is accepted.
	Store string `json:"store"`
	// Digest is the item's manifest digest, "sha256:<64 hex>".
	Digest string `json:"digest"`
	// Platform is the item's architecture in bootc's spelling ("x86_64",
	// "aarch64"), as bootc_store.items[].platform reports it. The install
	// refuses an item, or a copy, of another platform.
	Platform string `json:"platform"`
}

// SourceReuseStoreBootc is the one SourceReuse.Store value.
const SourceReuseStoreBootc = "bootc"

// ExperimentalBootcStoreReuse: `sourceReuse`. Only an advisory VM profile
// covers it; no required canary installs from a reused store
// (tuna-os/fisherman#384).
const ExperimentalBootcStoreReuse = "bootc_store_reuse"

// sourceReuse problem codes. The static ones are recipe rules; the host
// ones need the machine (full validate and the install); the install-only
// ones need root and a mutating step. docs/VALIDATE.md lists them all.
const (
	CodeSourceReuseExperimental        = "source_reuse_experimental"
	CodeSourceReuseStoreUnsupported    = "source_reuse_store_unsupported"
	CodeSourceReuseDigestInvalid       = "source_reuse_digest_invalid"
	CodeSourceReusePlatformInvalid     = "source_reuse_platform_invalid"
	CodeSourceReuseImageConflict       = "source_reuse_image_conflict"
	CodeSourceReuseTargetImgrefMissing = "source_reuse_target_imgref_required"
	CodeSourceReuseTargetImgrefInvalid = "source_reuse_target_imgref_invalid"
	CodeSourceReuseBackendUnsupported  = "source_reuse_backend_unsupported"

	// Host checks (full validate and the install).
	CodeSourceReuseHostNotBootc         = "source_reuse_host_not_bootc"
	CodeSourceReuseLiveMedia            = "source_reuse_live_media"
	CodeSourceReuseBackendNotQualified  = "source_reuse_backend_not_qualified"
	CodeSourceReuseItemNotFound         = "source_reuse_item_not_found"
	CodeSourceReuseItemUnverified       = "source_reuse_item_unverified"
	CodeSourceReusePlatformMismatch     = "source_reuse_platform_mismatch"
	CodeSourceReuseSandboxed            = "source_reuse_sandboxed"
	CodeSourceReuseCommandUnsupported   = "source_reuse_command_unsupported"
	CodeSourceReuseSourceUnavailable    = "source_reuse_source_unavailable"
	CodeSourceReuseSourceMoved          = "source_reuse_source_moved"
	CodeSourceReuseEstimateFailed       = "source_reuse_estimate_failed"
	CodeSourceReuseInsufficientSpace    = "source_reuse_insufficient_space"
	CodeSourceReuseDestinationExists    = "source_reuse_destination_exists"
	CodeSourceReuseTransferFailed       = "source_reuse_transfer_failed"
	CodeSourceReuseDiskExhausted        = "source_reuse_disk_exhausted"
	CodeSourceReuseVerifyFailed         = "source_reuse_verify_failed"
	CodeSourceReuseIdentityMismatch     = "source_reuse_identity_mismatch"
	CodeSourceReuseInstalledDigestDiffs = "source_reuse_installed_digest_differs"
)

var (
	reuseDigestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reusePlatformPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
	// updateRefPattern is the character set of a registry reference, the
	// same one `fisherman probe --target-imgref` accepts.
	updateRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+=-]{0,511}$`)
)

// CheckSourceReuse applies the static sourceReuse rules (no machine
// needed). Nothing is checked when the field is absent.
func CheckSourceReuse(r *Recipe) []Problem {
	return checkSourceReuse(r, true, true)
}

// CheckSourceReusePartial is CheckSourceReuse for `validate --partial`:
// `image` and `targetImgref` are judged only when the fragment has them,
// so a page that holds only the reuse choice is not told to fill in a
// field another page owns.
func CheckSourceReusePartial(r *Recipe, hasImage, hasTargetImgref bool) []Problem {
	return checkSourceReuse(r, hasImage, hasTargetImgref)
}

func checkSourceReuse(r *Recipe, checkImage, checkTarget bool) []Problem {
	s := r.SourceReuse
	if s == nil {
		return nil
	}
	var ps []Problem
	if !r.AcceptsExperimental(ExperimentalBootcStoreReuse) {
		ps = append(ps, Problem{"sourceReuse", CodeSourceReuseExperimental,
			"sourceReuse is experimental: only an advisory VM profile installs from a reused bootc store. " +
				"Add \"experimental\": [\"" + ExperimentalBootcStoreReuse + "\"] to the recipe to accept that, or remove sourceReuse"})
	}
	if s.Store != SourceReuseStoreBootc {
		ps = append(ps, Problem{"sourceReuse.store", CodeSourceReuseStoreUnsupported,
			fmt.Sprintf("sourceReuse.store must be %q (the live host's bootc storage), got %q", SourceReuseStoreBootc, s.Store)})
	}
	if !reuseDigestPattern.MatchString(s.Digest) {
		ps = append(ps, Problem{"sourceReuse.digest", CodeSourceReuseDigestInvalid,
			"sourceReuse.digest must be a manifest digest, sha256: followed by 64 lowercase hex digits " +
				"(bootc_store.items[].digest in `fisherman probe --json`)"})
	}
	if !reusePlatformPattern.MatchString(s.Platform) {
		ps = append(ps, Problem{"sourceReuse.platform", CodeSourceReusePlatformInvalid,
			"sourceReuse.platform must be the item's architecture as bootc spells it, such as x86_64 or aarch64"})
	}
	if checkImage && r.Image != "" {
		ps = append(ps, Problem{"image", CodeSourceReuseImageConflict,
			"image must be empty with sourceReuse: the installation source is the reused store item"})
	}
	if checkTarget {
		ps = append(ps, checkReuseUpdateRef(r.TargetImgref)...)
	}
	if r.ComposeFsBackend || r.Filesystem == "zfs" {
		ps = append(ps, Problem{"sourceReuse", CodeSourceReuseBackendUnsupported,
			"sourceReuse installs ostree-backend images only: composeFsBackend (or filesystem zfs, which forces it) is not qualified"})
	}
	return ps
}

// checkReuseUpdateRef: with sourceReuse the source is a local copy whose
// name means nothing after the install, so the update reference must be
// given, and must be a registry reference: not a local transport, not a
// localhost/ name, and without credentials.
func checkReuseUpdateRef(ref string) []Problem {
	if ref == "" {
		return []Problem{{"targetImgref", CodeSourceReuseTargetImgrefMissing,
			"targetImgref is required with sourceReuse: it is the reference the installed system follows for updates, " +
				"and fisherman does not derive it from the reused copy"}}
	}
	bare := strings.TrimPrefix(ref, "docker://")
	host, _, _ := strings.Cut(bare, "/")
	bad := !updateRefPattern.MatchString(ref) || strings.Contains(ref, "..") ||
		strings.HasPrefix(ref, "containers-storage:") || strings.HasPrefix(ref, "oci:") ||
		strings.HasPrefix(ref, "oci-archive:") || strings.HasPrefix(ref, "dir:") ||
		host == "localhost" || strings.HasPrefix(host, "localhost:") || strings.Contains(host, "@") ||
		!strings.Contains(bare, "/")
	if bad {
		return []Problem{{"targetImgref", CodeSourceReuseTargetImgrefInvalid,
			"targetImgref must be a registry reference (registry/name[:tag][@digest]) with sourceReuse: " +
				"not a containers-storage:, oci: or localhost reference, and without credentials"}}
	}
	return nil
}
