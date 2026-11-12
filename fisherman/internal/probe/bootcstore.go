package probe

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"strings"
)

// BootcStoreVersion is the version of the BootcStore object
// (tuna-os/fisherman#384). Like CapabilitiesVersion it is separate from
// ProtocolVersion: bump it when a field is removed or renamed or its meaning
// changes. New reason codes, roles and fields are additions.
const BootcStoreVersion = 1

// Inventory reason codes (BootcStore.Reason). The bootc_missing,
// check_failed and malformed_output values are the live-host codes of
// bootc.go and capabilities.go, applied to `bootc status --json`.
const (
	// StoreObserved: `bootc status --json` named a booted image, so the
	// host's bootc-owned storage is described.
	StoreObserved = "observed"
	// StoreNotBootcBooted: bootc ran, but the host did not boot a bootc
	// image (a generic live image, a container, a package-based host).
	StoreNotBootcBooted = "not_bootc_booted"
)

// Per-source status codes (BootcStore.Sources). observed, command_absent,
// check_failed and malformed_output keep the meanings they have elsewhere in
// the probe.
const (
	// SourceNotApplicable: the host is not bootc-booted, so the source was
	// not consulted.
	SourceNotApplicable = "not_applicable"
	// SourceAbsent: the bootc-owned store does not exist.
	SourceAbsent = "absent"
	// SourceInaccessible: the store exists, but the probe cannot read its
	// index (it belongs to root; the probe runs as the desktop user).
	SourceInaccessible = "inaccessible"
	// SourceNotRequested: `bootc image list` was not asked for. It is never
	// part of the default probe; see ListBootcImages.
	SourceNotRequested = "not_requested"
	// SourceNeedsRoot: asked for, but the probe is not root. bootc refuses
	// `image list` without root, so fisherman does not run it.
	SourceNeedsRoot = "needs_root"
	// SourceLiveMedia: asked for on live media. bootc refuses `image list`
	// on a read-only sysroot, and the live host's store is not the target's.
	SourceLiveMedia = "live_media"
	// SourceBackendNotQualified: asked for on a host whose backend is not
	// ostree. On composefs, bootc 1.17.1 mounts the ESP read-write to list
	// images, so fisherman does not run it there.
	SourceBackendNotQualified = "backend_not_qualified"
)

// Item roles.
const (
	RoleBooted          = "booted"
	RoleStaged          = "staged"
	RoleRollback        = "rollback"
	RoleOtherDeployment = "other_deployment"
	// RoleListedHost / RoleListedUnified: `bootc image list` named the image
	// with image_type "host" / "unified".
	RoleListedHost    = "listed_host"
	RoleListedUnified = "listed_unified"
	// RoleStored: the bootc-owned containers-storage index has the image.
	RoleStored = "stored"
)

// Storage kinds an item lives in.
const (
	// StorageBootcContainers is bootc's own containers-storage
	// (/sysroot/ostree/bootc/storage): logically bound images and, with
	// unified storage, host images. It is not podman's store.
	StorageBootcContainers = "bootc-containers-storage"
	// StorageUnknown: `bootc image list` named the image without saying
	// where it is kept beyond its type.
	StorageUnknown = "unknown"
)

// Availability of an item.
const (
	// AvailDeployed: a boot entry of the host uses the image; its content is
	// in the host's ostree or composefs repository.
	AvailDeployed = "deployed"
	// AvailStored: the bootc-owned containers-storage index lists it.
	AvailStored = "stored"
	// AvailListed: only `bootc image list` names it. Nothing confirms its
	// identity or content.
	AvailListed = "listed"
)

// Item identity reason codes.
const (
	// IdentityFromBootcStatus / IdentityFromBootcStoreIndex: where Digest
	// came from.
	IdentityFromBootcStatus     = "bootc_status"
	IdentityFromBootcStoreIndex = "bootc_store_index"
	// IdentityNotReported: the source names the image but reports no
	// identity (`bootc image list` prints names only; the store index
	// records no architecture).
	IdentityNotReported = "not_reported"
	// IdentityMalformed: the value is present but is not a sha256 digest or
	// a plain architecture token.
	IdentityMalformed = "malformed"
	// IdentityAmbiguousRef: the store index lists the name under two
	// different digests.
	IdentityAmbiguousRef = "ambiguous"
)

// Item reference reason codes (BootcStoreItem.RefReason).
const (
	// RefCredentialsRedacted: the reference carried user information
	// ("user:token@registry/..."); it is not copied.
	RefCredentialsRedacted = "credentials_redacted"
	// RefInvalid: the reference does not match the pattern the probe
	// accepts for --source.
	RefInvalid = "invalid_reference"
)

// Mismatch codes (BootcStoreItem.MismatchReason), checked in this
// order.
const (
	MismatchRefUnavailable  = "ref_unavailable"
	MismatchIdentityUnknown = "identity_unknown"
	MismatchPlatformUnknown = "platform_unknown"
	MismatchPlatformDiffers = "platform_mismatch"
)

// bootcStoreIndex is the image index of bootc's own containers-storage,
// relative to the physical root /sysroot (bootc's CStorage::subpath).
const bootcStoreIndex = "/sysroot/ostree/bootc/storage/overlay-images/images.json"

// Caps on what the inventory copies.
const (
	maxDeployments    = 16
	maxInventoryItems = 256
	maxListEntries    = 1024
)

// BootcStore is the read-only inventory of the live host's bootc-owned
// image storage. Context is always the live host: the inventory never
// describes the selected source or the installed target.
type BootcStore struct {
	Version int    `json:"version"`
	Context string `json:"context"`
	// Available is true when the host is bootc-booted and the inventory
	// below describes its storage. Reason says why not.
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	// MachinePlatform is the architecture of the machine fisherman runs on,
	// in bootc's spelling ("x86_64"). Items are compared with it.
	MachinePlatform string `json:"machine_platform"`
	Sources         struct {
		BootcStatus string `json:"bootc_status"`
		StoreIndex  string `json:"store_index"`
		ImageList   string `json:"image_list"`
	} `json:"sources"`
	// IgnoredEntries counts `bootc image list` entries that are not host
	// inventory: logically bound images and unknown types.
	IgnoredEntries int              `json:"ignored_entries"`
	Items          []BootcStoreItem `json:"items"`
}

// BootcStoreItem is one image in the host's bootc-owned storage.
type BootcStoreItem struct {
	// Ref is the image reference without a transport ("" when redacted).
	Ref       string `json:"ref"`
	RefReason string `json:"ref_reason,omitempty"`
	// Owner is "bootc": the storage belongs to the host's bootc, not podman.
	Owner string `json:"owner"`
	// Context is "live-host".
	Context string `json:"context"`
	// Storage is "ostree", "composefs", "bootc-containers-storage" or
	// "unknown".
	Storage      string   `json:"storage"`
	Roles        []string `json:"roles"`
	Availability string   `json:"availability"`
	// Digest is the manifest digest, the item's immutable identity.
	Digest       string `json:"digest"`
	DigestFrom   string `json:"digest_from,omitempty"`
	DigestReason string `json:"digest_reason,omitempty"`
	// Platform is the architecture bootc reports ("x86_64").
	Platform       string `json:"platform"`
	PlatformReason string `json:"platform_reason,omitempty"`
	// MatchesMachine: the reference, digest and platform are known and the
	// platform is this machine's. It is a fact about identity, not a promise
	// that fisherman can install from the item: no install path reads
	// bootc-owned storage yet.
	MatchesMachine bool   `json:"matches_machine"`
	MismatchReason string `json:"mismatch_reason,omitempty"`
}

// machineArch is this machine's architecture in bootc's spelling.
// NormalizeArch spells an architecture the way the probe reports it, the
// kernel's spelling ("x86_64", "aarch64"). bootc status reports the image's
// OCI architecture ("amd64", "arm64"), so the two never compared equal on a
// real host and every item read as platform_differs (#384, found by the
// store-reuse VM profile). Unknown tokens pass through unchanged.
func NormalizeArch(a string) string {
	switch a {
	case "amd64", "x86-64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "powerpc64le":
		return "ppc64le"
	}
	return a
}

func (p *Prober) machineArch() string {
	if p.Arch != "" {
		return p.Arch
	}
	return NormalizeArch(runtime.GOARCH)
}

func (p *Prober) euid() int {
	if p.Euid == nil {
		return os.Geteuid()
	}
	return p.Euid()
}

// BootcStore inventories the live host's bootc-owned storage. It reads the
// cached `bootc status --json` and, as a file, bootc's store index. It runs
// `bootc image list --type host --format json` only when ListBootcImages is
// set, on an installed (not live) ostree host, as root; see docs/PROBE.md
// for what bootc itself does then. caps is this probe's capability report:
// the inventory exists only when it found a bootc-booted host.
func (p *Prober) BootcStore(caps Capabilities, live Live) BootcStore {
	s := BootcStore{
		Version:         BootcStoreVersion,
		Context:         BootcContextLiveHost,
		MachinePlatform: p.machineArch(),
		Items:           []BootcStoreItem{},
	}
	s.Sources.StoreIndex, s.Sources.ImageList = SourceNotApplicable, SourceNotApplicable
	st := p.hostStatus()
	switch {
	case st.missing:
		s.Reason = ReasonBootcMissing
	case st.failed:
		s.Reason = ReasonCheckFailed
	case !st.ok:
		s.Reason = ReasonMalformedOutput
	case !st.booted || caps.LiveHost.IdentityReason == IdentityNotBooted:
		s.Reason = StoreNotBootcBooted
	default:
		s.Reason, s.Available = StoreObserved, true
	}
	s.Sources.BootcStatus = s.Reason
	if !s.Available {
		return s
	}

	inv := inventory{machine: s.MachinePlatform}
	for _, d := range st.deployments {
		inv.addDeployment(d, st.backend)
	}
	s.Sources.StoreIndex = p.readBootcStoreIndex(&inv)
	s.Sources.ImageList = p.listBootcImages(&inv, live, st.backend, &s.IgnoredEntries)
	s.Items = inv.finish()
	return s
}

// inventory merges items by (storage, ref, digest).
type inventory struct {
	machine string
	items   []BootcStoreItem
}

func (inv *inventory) find(storage, ref, digest string) *BootcStoreItem {
	if ref == "" {
		return nil
	}
	for i := range inv.items {
		it := &inv.items[i]
		if it.Storage == storage && it.Ref == ref && it.Digest == digest {
			return it
		}
	}
	return nil
}

func (inv *inventory) add(it BootcStoreItem) *BootcStoreItem {
	if len(inv.items) >= maxInventoryItems {
		return nil
	}
	it.Owner, it.Context = "bootc", BootcContextLiveHost
	inv.items = append(inv.items, it)
	return &inv.items[len(inv.items)-1]
}

func (inv *inventory) addDeployment(d bootcDeployment, backend string) {
	storage := backend
	if storage == "" {
		storage = StorageUnknown
	}
	it := BootcStoreItem{Storage: storage, Availability: AvailDeployed}
	it.Ref, it.RefReason = inventoryRef(d.image)
	switch {
	case validDigest(d.digest):
		it.Digest, it.DigestFrom = d.digest, IdentityFromBootcStatus
	case d.digest == "":
		it.DigestReason = IdentityDigestMissing
	default:
		it.DigestReason = IdentityMalformed
	}
	switch {
	case tokenRe.MatchString(d.arch):
		it.Platform = d.arch
	case d.arch == "":
		it.PlatformReason = IdentityNotReported
	default:
		it.PlatformReason = IdentityMalformed
	}
	if existing := inv.find(it.Storage, it.Ref, it.Digest); existing != nil && existing.Platform == it.Platform {
		existing.Roles = append(existing.Roles, d.role)
		return
	}
	it.Roles = []string{d.role}
	inv.add(it)
}

// readBootcStoreIndex adds the entries of bootc's own containers-storage
// index. A name the index lists under two digests is reported once, without
// a digest.
func (p *Prober) readBootcStoreIndex(inv *inventory) string {
	b, err := p.hostReadFile(bootcStoreIndex)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !p.inFlatpak() {
			return SourceAbsent
		}
		if p.hostTest("-e", bootcStoreIndex) {
			return SourceInaccessible
		}
		return SourceAbsent
	}
	if len(b) > maxIndexBytes {
		return ReasonMalformedOutput
	}
	var imgs []struct {
		Names  []string `json:"names"`
		Digest string   `json:"digest"`
	}
	if json.Unmarshal(b, &imgs) != nil {
		return ReasonMalformedOutput
	}
	digests := map[string]string{}
	var order []string
	for _, img := range imgs {
		d := img.Digest
		if !validDigest(d) {
			d = "!" // malformed
		}
		for _, n := range img.Names {
			prev, seen := digests[n]
			switch {
			case !seen:
				order = append(order, n)
				digests[n] = d
			case prev != d:
				digests[n] = "?" // ambiguous
			}
		}
	}
	for _, n := range order {
		it := BootcStoreItem{Storage: StorageBootcContainers, Availability: AvailStored,
			Roles: []string{RoleStored}, PlatformReason: IdentityNotReported}
		it.Ref, it.RefReason = inventoryRef(n)
		switch d := digests[n]; d {
		case "?":
			it.DigestReason = IdentityAmbiguousRef
		case "!":
			it.DigestReason = IdentityMalformed
		default:
			it.Digest, it.DigestFrom = d, IdentityFromBootcStoreIndex
		}
		inv.add(it)
	}
	return StoreObserved
}

// listBootcImages runs `bootc image list --type host --format json` when it
// was asked for and every gate holds, and adds what it names.
func (p *Prober) listBootcImages(inv *inventory, live Live, backend string, ignored *int) string {
	switch {
	case !p.ListBootcImages:
		return SourceNotRequested
	case live.IsLive:
		return SourceLiveMedia
	case backend != BackendOstree:
		return SourceBackendNotQualified
	case p.euid() != 0:
		return SourceNeedsRoot
	}
	out, err := p.run("bootc", "image", "list", "--type", "host", "--format", "json")
	switch {
	case err != nil && exitCode(err) == 2:
		return ReasonCommandAbsent
	case err != nil:
		return ReasonCheckFailed
	}
	entries, ok := parseImageList(out)
	if !ok {
		return ReasonMalformedOutput
	}
	for _, e := range entries {
		role := RoleListedHost
		switch e.ImageType {
		case "host":
		case "unified":
			role = RoleListedUnified
		default: // "logical" or a type this version does not know
			*ignored++
			continue
		}
		ref, reason := inventoryRef(e.Image)
		if merged := inv.mergeListed(ref, role); merged {
			continue
		}
		storage := StorageUnknown
		if role == RoleListedUnified {
			storage = StorageBootcContainers
		}
		inv.add(BootcStoreItem{Ref: ref, RefReason: reason, Storage: storage, Roles: []string{role},
			Availability: AvailListed, DigestReason: IdentityNotReported, PlatformReason: IdentityNotReported})
	}
	return StoreObserved
}

// mergeListed adds role to the item the listing names. A unified entry is
// the store index entry of the same name (same store, so the same image).
// A host entry is merged only into an earlier listed entry: a name shared
// with a boot entry is not proof of the same image, because the ostree ref
// may have moved on since that entry was deployed.
func (inv *inventory) mergeListed(ref, role string) bool {
	if ref == "" {
		return false
	}
	for i := range inv.items {
		it := &inv.items[i]
		if it.Ref != ref {
			continue
		}
		sameStore := role == RoleListedUnified && it.Storage == StorageBootcContainers
		sameListing := it.Availability == AvailListed && contains(it.Roles, role)
		if sameStore || sameListing {
			if !contains(it.Roles, role) {
				it.Roles = append(it.Roles, role)
			}
			return true
		}
	}
	return false
}

type imageListEntry struct {
	ImageType string `json:"image_type"`
	Image     string `json:"image"`
}

// parseImageList is the adapter for `bootc image list --format json`: an
// array of {image_type, image}. Other keys are ignored. A wrong type
// anywhere makes the whole output malformed.
func parseImageList(out []byte) ([]imageListEntry, bool) {
	var raw []map[string]json.RawMessage
	if json.Unmarshal(out, &raw) != nil || raw == nil || len(raw) > maxListEntries {
		return nil, false
	}
	entries := make([]imageListEntry, 0, len(raw))
	for _, r := range raw {
		var e imageListEntry
		if json.Unmarshal(r["image_type"], &e.ImageType) != nil || json.Unmarshal(r["image"], &e.Image) != nil {
			return nil, false
		}
		entries = append(entries, e)
	}
	return entries, true
}

// signaturePrefixes are ostree-ext's image reference schemes. An
// ostree-remote-* scheme is followed by the remote name and a colon.
var signaturePrefixes = []string{"ostree-unverified-registry:", "ostree-unverified-image:",
	"ostree-image-signed:", "ostree-remote-registry:", "ostree-remote-image:"}

// inventoryRef reduces a reference bootc printed to the bare name the probe
// reports, or "" and a reason. It strips ostree-ext's signature scheme and a
// transport, refuses user information in the registry part, and applies the
// --source pattern.
func inventoryRef(ref string) (string, string) {
	for _, pfx := range signaturePrefixes {
		if rest, ok := strings.CutPrefix(ref, pfx); ok {
			ref = rest
			if strings.HasPrefix(pfx, "ostree-remote-") {
				_, ref, _ = strings.Cut(ref, ":")
			}
			break
		}
	}
	for _, t := range []string{"docker://", "registry:", "containers-storage:"} {
		ref = strings.TrimPrefix(ref, t)
	}
	host, _, _ := strings.Cut(ref, "/")
	if strings.Contains(host, "@") {
		return "", RefCredentialsRedacted
	}
	if ValidateReference(ref) != nil {
		return "", RefInvalid
	}
	return ref, ""
}

// finish sets eligibility and returns the items.
func (inv *inventory) finish() []BootcStoreItem {
	for i := range inv.items {
		it := &inv.items[i]
		switch {
		case it.Ref == "":
			it.MismatchReason = MismatchRefUnavailable
		case it.Digest == "":
			it.MismatchReason = MismatchIdentityUnknown
		case it.Platform == "":
			it.MismatchReason = MismatchPlatformUnknown
		case it.Platform != inv.machine:
			it.MismatchReason = MismatchPlatformDiffers
		default:
			it.MatchesMachine = true
		}
	}
	if inv.items == nil {
		return []BootcStoreItem{}
	}
	return inv.items
}
