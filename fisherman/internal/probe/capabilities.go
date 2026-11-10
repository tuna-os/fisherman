package probe

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// CapabilitiesVersion is the version of the Capabilities object. It has its
// own number, separate from ProtocolVersion, because frontends gate their
// capability UI on it (tuna-os/bootc-installer#266). Bump it when a field is
// removed or renamed or its meaning changes. New features, reason codes and
// fields are additions and do not bump it.
const CapabilitiesVersion = 1

// Selection is what the frontend selected. It carries image references
// only, never a recipe: a recipe holds the LUKS passphrase and user
// password, and the probe has no use for them.
type Selection struct {
	// Source is the installation source as the recipe's `image` will name
	// it: "containers-storage:<name>", "oci:<path>[:<tag>]",
	// "docker://<name>" or a bare registry name.
	Source string
	// TargetImgref is the recipe's `targetImgref`: the reference the
	// installed system follows for updates. Empty means the install derives
	// it from the source.
	TargetImgref string
	// LiveRoot says the recipe will omit `image`: the install then runs the
	// live host's own bootc, which installs the image the live host booted.
	// Mutually exclusive with Source.
	LiveRoot bool
}

// Selected reports whether anything was selected.
func (s Selection) Selected() bool { return s.Source != "" || s.LiveRoot }

// ErrInvalidReference is the error for a reference the probe will not use.
// It never contains the reference: the caller prints it to stderr.
var ErrInvalidReference = errors.New("invalid image reference: use a containers-storage:, oci:, docker:// or bare registry reference of at most 512 characters, without spaces or control characters")

// refRe is the character set of the references fisherman accepts from a
// frontend. It excludes whitespace, quotes, brackets and shell syntax, so a
// reference copied into the JSON cannot carry anything but a reference.
var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+=-]{0,511}$`)

// ValidateReference checks one reference for Selection.
func ValidateReference(ref string) error {
	if !refRe.MatchString(ref) || strings.Contains(ref, "..") {
		return ErrInvalidReference
	}
	return nil
}

// Contexts: where a feature answer was established.
const (
	// ContextSourceRoot: observed by running bootc in an execution root
	// whose identity matches the selected source (ExecutionRoot).
	ContextSourceRoot = "source-root"
	// ContextFisherman: fisherman's own policy. The same for every source.
	ContextFisherman = "fisherman"
	// ContextNone: not established in any context.
	ContextNone = "none"
)

// Execution-root contexts.
const (
	// ExecRootLiveHost: the live host's root is the source's root, so the
	// host's bootc is the source's bootc.
	ExecRootLiveHost = "live-host"
	// ExecRootNone: no read-only root the probe may run matches the source.
	ExecRootNone = "none"
)

// Backends a booted deployment can report.
const (
	BackendOstree    = "ostree"
	BackendComposefs = "composefs"
)

// Source transports.
const (
	TransportContainersStorage = "containers-storage"
	TransportOCI               = "oci"
	TransportRegistry          = "registry"
	TransportLiveRoot          = "live-root"
)

// Source localities.
const (
	LocalityLocal   = "local"
	LocalityRemote  = "remote"
	LocalityUnknown = "unknown"
)

// Feature names. A frontend ignores names it does not know.
const (
	FeatNativeInspection     = "native_inspection"
	FeatInstallConfiguration = "install_configuration"
	FeatDeploymentMount      = "deployment_mount"
	FeatFinalization         = "finalization"
	FeatUnifiedStorage       = "unified_storage"
	FeatBootcImageStorage    = "bootc_image_storage"
)

// Feature reason codes, in addition to the live-host codes in bootc.go
// (command_absent, check_failed, malformed_output). A code is never renamed
// or reused.
const (
	// ReasonObserved: the command ran in the source root and its output
	// parsed. Only a command that does the thing itself, read-only, can
	// give this; a --help call never does.
	ReasonObserved = "observed"
	// ReasonMalformedOutput: the command ran, but its output is not the
	// JSON the adapter reads.
	ReasonMalformedOutput = "malformed_output"
	// ReasonNoSourceSelected: the probe was not told which source.
	ReasonNoSourceSelected = "no_source_selected"
	// ReasonSourceRootUnavailable: no execution root the probe may run
	// read-only is the source's; ExecutionRoot.Reason says why. Running the
	// source's own bootc would need a pull, a mount or a container, which
	// the probe never does.
	ReasonSourceRootUnavailable = "source_root_unavailable"
	// ReasonNotQualified: the source root has the command, but only an
	// install of this source on a test machine can show the feature works
	// with fisherman's install profile. Help text and versions are not that.
	ReasonNotQualified = "not_qualified"
	// ReasonFishermanRefuses: fisherman does not implement the feature;
	// `fisherman validate` refuses a recipe that asks for it.
	ReasonFishermanRefuses = "fisherman_refuses"
)

// Execution-root reason codes.
const (
	// RootLiveRoot: Selection.LiveRoot, and the host booted a bootc image:
	// the install runs that root's bootc on that root's image.
	RootLiveRoot = "live_root"
	// RootDigestMatch: the source's manifest digest equals the digest the
	// host booted.
	RootDigestMatch = "digest_match"
	// RootNoSourceSelected: nothing selected.
	RootNoSourceSelected = "no_source_selected"
	// RootHostNotBootc: the host has no bootc, bootc status failed, or the
	// host did not boot a bootc image.
	RootHostNotBootc = "host_not_bootc"
	// RootHostDigestUnknown: the host booted an image without a readable
	// digest.
	RootHostDigestUnknown = "host_digest_unknown"
	// RootSourceDigestUnknown: the source's digest is not known offline.
	RootSourceDigestUnknown = "source_digest_unknown"
	// RootDigestMismatch: both digests are known and differ: the host is an
	// unrelated image, whatever its name or version.
	RootDigestMismatch = "digest_mismatch"
)

// Source-identity reason codes (Source.DigestReason, LiveHost.IdentityReason).
const (
	// DigestFromStoreIndex: read from a containers-storage image index.
	DigestFromStoreIndex = "store_index"
	// DigestFromOCIIndex: read from an OCI layout's index.json.
	DigestFromOCIIndex = "oci_index"
	// DigestFromReference: the reference pins a digest (name@sha256:...).
	DigestFromReference = "reference"
	// DigestFromLiveRoot: the live host's booted digest (Selection.LiveRoot).
	DigestFromLiveRoot = "live_root"

	// IdentityRemoteUnavailable: a registry source; the probe does not
	// contact registries, so the digest is unknown until an explicit
	// acquisition step (pull or inspect) during the install.
	IdentityRemoteUnavailable = "remote_metadata_unavailable"
	// IdentityNotFoundLocally: no readable local store has the image. The
	// store of root (/var/lib/containers/storage) is usually not readable
	// by the desktop user, so this is not proof the image is absent.
	IdentityNotFoundLocally = "not_found_locally"
	// IdentityAmbiguous: readable stores hold different images under the
	// name.
	IdentityAmbiguous = "ambiguous_local_copies"
	// IdentityLayoutUnreadable: the OCI layout's index.json is missing,
	// unreadable, malformed, or does not single out one manifest.
	IdentityLayoutUnreadable = "oci_layout_unreadable"
	// IdentityNotBooted: the host did not boot a bootc image, or bootc
	// status failed.
	IdentityNotBooted = "not_bootc_booted"
	// IdentityDigestMissing: bootc status named an image without a digest.
	IdentityDigestMissing = "digest_missing"
)

// Capabilities is the source-scoped bootc report. The live host, the
// selected source, the execution root and the installed target are
// separate objects: each answers for itself, and nothing is copied from
// one to another without a matching identity.
type Capabilities struct {
	Version       int                `json:"version"`
	LiveHost      LiveHost           `json:"live_host"`
	Source        Source             `json:"source"`
	ExecutionRoot ExecutionRoot      `json:"execution_root"`
	Target        TargetIdentity     `json:"target"`
	Features      map[string]Feature `json:"features"`
	// InstallDefaults are the documented merged defaults the source root's
	// `bootc install print-configuration` reports. They are not a disk
	// plan: fisherman's own layout rules still apply.
	InstallDefaults InstallDefaults `json:"install_defaults"`
	// Image is what the source root's `bootc container inspect --json`
	// reports.
	Image ImageFacts `json:"image"`
}

// LiveHost is the machine that runs the installer.
type LiveHost struct {
	Context string `json:"context"`
	IsLive  bool   `json:"is_live"`
	// Image is the ref the host booted ("" when it booted none).
	Image string `json:"image"`
	// Digest is the manifest digest the host booted.
	Digest string `json:"digest"`
	// IdentityReason is set when Digest is "".
	IdentityReason string `json:"identity_reason,omitempty"`
	// Platform is the architecture bootc status reports ("x86_64").
	Platform string `json:"platform"`
	// Backend is "ostree", "composefs" or "" (bootc did not say).
	Backend      string `json:"backend"`
	BootcVersion string `json:"bootc_version"`
}

// Source is the selected installation source.
type Source struct {
	Selected bool `json:"selected"`
	// Ref is the reference as selected. Update references are in Target.
	Ref       string `json:"ref"`
	Transport string `json:"transport"`
	Locality  string `json:"locality"`
	// Store is the containers-storage root the digest was read from.
	Store  string `json:"store"`
	Digest string `json:"digest"`
	// DigestFrom names where Digest came from; DigestReason says why it is
	// "".
	DigestFrom   string `json:"digest_from,omitempty"`
	DigestReason string `json:"digest_reason,omitempty"`
	// Platform is "os/arch[/variant]" when an OCI index says so.
	Platform string `json:"platform"`
}

// ExecutionRoot is the root whose bootc the probe ran to answer for the
// source.
type ExecutionRoot struct {
	Context      string `json:"context"`
	Established  bool   `json:"established"`
	Reason       string `json:"reason"`
	Backend      string `json:"backend"`
	BootcVersion string `json:"bootc_version"`
}

// TargetIdentity is the system the install will create. The probe never
// sees it: it does not exist yet.
type TargetIdentity struct {
	// UpdateRef is the reference the installed system will follow.
	UpdateRef string `json:"update_ref"`
	// UpdateRefFrom: "target_imgref" (given), "source" (derived from the
	// source ref), "live_image" (the live host's booted ref) or "".
	UpdateRefFrom string `json:"update_ref_from"`
	State         string `json:"state"`
}

// Feature is one answer with its provenance.
type Feature struct {
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Context string `json:"context"`
}

// InstallDefaults is the adapter's view of print-configuration.
type InstallDefaults struct {
	Observed       bool     `json:"observed"`
	RootFilesystem string   `json:"root_filesystem"`
	BlockSetups    []string `json:"block_setups"`
	Bootloader     string   `json:"bootloader"`
}

// ImageFacts is the adapter's view of container inspect.
type ImageFacts struct {
	Observed      bool   `json:"observed"`
	KernelVersion string `json:"kernel_version"`
	KernelUnified bool   `json:"kernel_unified"`
}

// tokenRe is a short identifier such as a filesystem or bootloader name.
var tokenRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// kernelVersionRe is a kernel release string.
var kernelVersionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z._+~-]{0,95}$`)

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validDigest(d string) bool { return digestRe.MatchString(d) }

// maxBlockSetups caps the list copied from print-configuration.
const maxBlockSetups = 16

// maxIndexBytes caps an image index or OCI index the probe reads.
const maxIndexBytes = 8 << 20

// Capabilities answers for the selected source. It runs bootc only in a
// root whose identity matches the source (ExecutionRoot.Established), and
// there only `bootc container inspect --json`, `bootc install
// print-configuration` and `bootc install finalize --help`. It never pulls,
// mounts, starts a container or contacts a registry. live, offline and host
// are this probe's own answers, passed in so nothing runs twice.
func (p *Prober) Capabilities(live Live, offline Offline, host Bootc) Capabilities {
	st := p.hostStatus()
	c := Capabilities{
		Version: CapabilitiesVersion,
		LiveHost: LiveHost{
			Context:      BootcContextLiveHost,
			IsLive:       live.IsLive,
			Image:        st.image,
			Digest:       st.digest,
			Platform:     st.arch,
			Backend:      st.backend,
			BootcVersion: host.Version,
		},
		ExecutionRoot:   ExecutionRoot{Context: ExecRootNone},
		Target:          TargetIdentity{State: "not_installed"},
		Features:        map[string]Feature{},
		InstallDefaults: InstallDefaults{BlockSetups: []string{}},
	}
	switch {
	case !st.booted:
		c.LiveHost.IdentityReason = IdentityNotBooted
	case st.digest == "":
		c.LiveHost.IdentityReason = IdentityDigestMissing
	}

	sel := p.Selection
	c.Source = p.source(sel, offline, st)
	c.Target = target(sel, st)
	sourceDigests := p.sourceDigests(sel, c.Source, offline)
	c.ExecutionRoot = executionRoot(sel, st, c.Source, sourceDigests)
	if c.ExecutionRoot.Established {
		c.ExecutionRoot.BootcVersion = host.Version
	}

	// fisherman's own policy answers the same for every source.
	c.Features[FeatUnifiedStorage] = Feature{CapUnsupported, ReasonFishermanRefuses, ContextFisherman}

	sourceFeatures := []string{FeatNativeInspection, FeatInstallConfiguration, FeatFinalization,
		FeatDeploymentMount, FeatBootcImageStorage}
	if !c.ExecutionRoot.Established {
		reason := ReasonSourceRootUnavailable
		if !sel.Selected() {
			reason = ReasonNoSourceSelected
		}
		for _, f := range sourceFeatures {
			c.Features[f] = Feature{CapUnknown, reason, ContextNone}
		}
		return c
	}

	c.Features[FeatNativeInspection], c.Image = p.inspect()
	c.Features[FeatInstallConfiguration], c.InstallDefaults = p.printConfiguration()
	c.Features[FeatFinalization] = p.finalizeHelp()
	// Nothing read-only shows these work; only a qualified install does.
	c.Features[FeatDeploymentMount] = Feature{CapUnknown, ReasonNotQualified, ContextSourceRoot}
	c.Features[FeatBootcImageStorage] = Feature{CapUnknown, ReasonNotQualified, ContextSourceRoot}
	return c
}

// runFeature classifies one bootc call in the source root. parse is nil
// for a call whose success says nothing beyond "the command exists".
func (p *Prober) runFeature(args []string, parse func([]byte) bool) Feature {
	out, err := p.run("bootc", args...)
	switch {
	case err == nil && parse == nil:
		return Feature{CapUnknown, ReasonNotQualified, ContextSourceRoot}
	case err == nil && parse(out):
		return Feature{CapSupported, ReasonObserved, ContextSourceRoot}
	case err == nil:
		return Feature{CapUnknown, ReasonMalformedOutput, ContextSourceRoot}
	case exitCode(err) == 2:
		// clap's exit status for an unrecognised subcommand or flag.
		return Feature{CapUnsupported, ReasonCommandAbsent, ContextSourceRoot}
	default:
		return Feature{CapUnknown, ReasonCheckFailed, ContextSourceRoot}
	}
}

func (p *Prober) inspect() (Feature, ImageFacts) {
	var facts ImageFacts
	f := p.runFeature([]string{"container", "inspect", "--json"}, func(out []byte) bool {
		var ok bool
		facts, ok = parseInspect(out)
		return ok
	})
	if f.Status != CapSupported {
		facts = ImageFacts{}
	}
	return f, facts
}

func (p *Prober) printConfiguration() (Feature, InstallDefaults) {
	var d InstallDefaults
	f := p.runFeature([]string{"install", "print-configuration"}, func(out []byte) bool {
		var ok bool
		d, ok = parsePrintConfiguration(out)
		return ok
	})
	if f.Status != CapSupported {
		d = InstallDefaults{BlockSetups: []string{}}
	}
	return f, d
}

// finalizeHelp: a missing subcommand is proof the feature is absent; a
// present one is not proof it works, so it stays unknown.
func (p *Prober) finalizeHelp() Feature {
	return p.runFeature([]string{"install", "finalize", "--help"}, nil)
}

// parsePrintConfiguration is the adapter for `bootc install
// print-configuration`. It reads the documented keys and ignores any other:
// bootc adds keys between releases, and they are not part of fisherman's
// contract. A known key with the wrong type, or a value that is not a plain
// token, makes the whole output malformed.
func parsePrintConfiguration(out []byte) (InstallDefaults, bool) {
	d := InstallDefaults{Observed: true, BlockSetups: []string{}}
	var raw map[string]json.RawMessage
	if json.Unmarshal(out, &raw) != nil || raw == nil {
		return InstallDefaults{BlockSetups: []string{}}, false
	}
	bad := func() (InstallDefaults, bool) { return InstallDefaults{BlockSetups: []string{}}, false }
	if v, ok := raw["root-fs-type"]; ok && present(v) {
		if json.Unmarshal(v, &d.RootFilesystem) != nil || !tokenRe.MatchString(d.RootFilesystem) {
			return bad()
		}
	}
	if v, ok := raw["filesystem"]; ok && present(v) && d.RootFilesystem == "" {
		var fs struct {
			Root *struct {
				Type string `json:"type"`
			} `json:"root"`
		}
		if json.Unmarshal(v, &fs) != nil {
			return bad()
		}
		if fs.Root != nil && fs.Root.Type != "" {
			if !tokenRe.MatchString(fs.Root.Type) {
				return bad()
			}
			d.RootFilesystem = fs.Root.Type
		}
	}
	if v, ok := raw["block"]; ok && present(v) {
		var block []string
		if json.Unmarshal(v, &block) != nil || len(block) > maxBlockSetups {
			return bad()
		}
		for _, b := range block {
			if !tokenRe.MatchString(b) {
				return bad()
			}
		}
		d.BlockSetups = append(d.BlockSetups, block...)
	}
	if v, ok := raw["bootloader"]; ok && present(v) {
		if json.Unmarshal(v, &d.Bootloader) != nil || !tokenRe.MatchString(d.Bootloader) {
			return bad()
		}
	}
	return d, true
}

// parseInspect is the adapter for `bootc container inspect --json`.
func parseInspect(out []byte) (ImageFacts, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(out, &raw) != nil || raw == nil {
		return ImageFacts{}, false
	}
	f := ImageFacts{Observed: true}
	if v, ok := raw["kernel"]; ok && present(v) {
		var k struct {
			Version string `json:"version"`
			Unified bool   `json:"unified"`
		}
		if json.Unmarshal(v, &k) != nil || !kernelVersionRe.MatchString(k.Version) {
			return ImageFacts{}, false
		}
		f.KernelVersion, f.KernelUnified = k.Version, k.Unified
	}
	return f, true
}

// source describes the selected source from local, read-only facts.
func (p *Prober) source(sel Selection, offline Offline, st bootcHostStatus) Source {
	s := Source{Locality: LocalityUnknown}
	switch {
	case sel.LiveRoot:
		s.Selected = true
		s.Transport = TransportLiveRoot
		s.Locality = LocalityLocal
		s.Ref = st.image
		if st.digest != "" {
			s.Digest, s.DigestFrom = st.digest, DigestFromLiveRoot
		} else {
			s.DigestReason = IdentityNotBooted
			if st.booted {
				s.DigestReason = IdentityDigestMissing
			}
		}
		return s
	case sel.Source == "":
		return s
	}
	s.Selected = true
	s.Ref = sel.Source
	transport, name := splitTransport(sel.Source)
	s.Transport = transport
	switch transport {
	case TransportOCI:
		s.Digest, s.Platform = p.ociLayoutDigest(name)
		if s.Digest == "" {
			s.DigestReason = IdentityLayoutUnreadable
		} else {
			s.Locality, s.DigestFrom = LocalityLocal, DigestFromOCIIndex
		}
	case TransportContainersStorage:
		matches := p.storeMatches(name, offline)
		switch {
		case len(matches) == 0:
			s.DigestReason = IdentityNotFoundLocally
		case !sameImage(matches):
			s.Locality, s.DigestReason = LocalityLocal, IdentityAmbiguous
		default:
			s.Locality = LocalityLocal
			s.Store, s.Digest, s.DigestFrom = matches[0].store, matches[0].digest, DigestFromStoreIndex
		}
	default: // registry
		s.Locality = LocalityRemote
		if _, d, ok := strings.Cut(name, "@"); ok && validDigest(d) {
			s.Digest, s.DigestFrom = d, DigestFromReference
		} else {
			s.DigestReason = IdentityRemoteUnavailable
		}
	}
	return s
}

// sourceDigests is every digest that identifies the source: a store entry
// records the manifest digest and the list digest it was pulled by.
func (p *Prober) sourceDigests(sel Selection, s Source, offline Offline) []string {
	if s.Digest == "" {
		return nil
	}
	if s.Transport != TransportContainersStorage {
		return []string{s.Digest}
	}
	_, name := splitTransport(sel.Source)
	var all []string
	for _, m := range p.storeMatches(name, offline) {
		all = append(all, m.digests...)
	}
	return all
}

func executionRoot(sel Selection, st bootcHostStatus, s Source, sourceDigests []string) ExecutionRoot {
	r := ExecutionRoot{Context: ExecRootNone}
	switch {
	case !sel.Selected():
		r.Reason = RootNoSourceSelected
	case !st.booted:
		r.Reason = RootHostNotBootc
	case sel.LiveRoot:
		// bootc with no --source-imgref installs the root it runs in.
		r.Reason = RootLiveRoot
	case st.digest == "":
		r.Reason = RootHostDigestUnknown
	case s.Digest == "":
		r.Reason = RootSourceDigestUnknown
	case !contains(sourceDigests, st.digest):
		r.Reason = RootDigestMismatch
	default:
		r.Reason = RootDigestMatch
	}
	if r.Reason == RootLiveRoot || r.Reason == RootDigestMatch {
		r.Context, r.Established, r.Backend = ExecRootLiveHost, true, st.backend
	}
	return r
}

func target(sel Selection, st bootcHostStatus) TargetIdentity {
	t := TargetIdentity{State: "not_installed"}
	switch {
	case sel.TargetImgref != "":
		t.UpdateRef, t.UpdateRefFrom = sel.TargetImgref, "target_imgref"
	case sel.LiveRoot && st.image != "":
		t.UpdateRef, t.UpdateRefFrom = st.image, "live_image"
	case sel.Source != "":
		// The install strips the transport the same way (bareImageRef).
		if tr, name := splitTransport(sel.Source); tr != TransportOCI {
			t.UpdateRef, t.UpdateRefFrom = name, "source"
		}
	}
	return t
}

// splitTransport returns the transport and the rest of a reference.
func splitTransport(ref string) (string, string) {
	switch {
	case strings.HasPrefix(ref, "containers-storage:"):
		return TransportContainersStorage, strings.TrimPrefix(ref, "containers-storage:")
	case strings.HasPrefix(ref, "oci:"):
		return TransportOCI, strings.TrimPrefix(ref, "oci:")
	case strings.HasPrefix(ref, "docker://"):
		return TransportRegistry, strings.TrimPrefix(ref, "docker://")
	}
	return TransportRegistry, ref
}

type storeMatch struct {
	store   string
	digest  string
	digests []string
}

// rootStore is root's containers-storage. It is read when it is readable
// (the probe runs as the desktop user, so usually it is not).
const rootStore = "/var/lib/containers/storage"

// storeMatches reads every readable store's image index and returns the
// entries named name (or name:latest when name has no tag).
func (p *Prober) storeMatches(name string, offline Offline) []storeMatch {
	names := []string{name}
	if !strings.Contains(name, "@") && !strings.Contains(name[strings.LastIndex(name, "/")+1:], ":") {
		names = append(names, name+":latest")
	}
	var out []storeMatch
	for _, store := range append(append([]string{}, offline.Stores...), rootStore) {
		for _, idx := range storeImageIndexes {
			b, err := p.hostReadFile(filepath.Join(store, idx))
			if err != nil || len(b) > maxIndexBytes {
				continue
			}
			var imgs []struct {
				Names   []string `json:"names"`
				Digest  string   `json:"digest"`
				Digests []string `json:"digests"`
			}
			if json.Unmarshal(b, &imgs) != nil {
				continue
			}
			for _, img := range imgs {
				if !overlaps(img.Names, names) || !validDigest(img.Digest) {
					continue
				}
				m := storeMatch{store: store, digest: img.Digest, digests: []string{img.Digest}}
				for _, d := range img.Digests {
					if validDigest(d) && d != img.Digest {
						m.digests = append(m.digests, d)
					}
				}
				out = append(out, m)
			}
		}
	}
	return out
}

func sameImage(ms []storeMatch) bool {
	for _, m := range ms[1:] {
		if m.digest != ms[0].digest {
			return false
		}
	}
	return true
}

// ociLayoutDigest reads an OCI layout's index.json and returns the digest
// and platform of the manifest the reference selects: the one whose
// org.opencontainers.image.ref.name is the tag, or the only one.
func (p *Prober) ociLayoutDigest(rest string) (digest, platform string) {
	path, tag := rest, ""
	if i := strings.LastIndex(rest, ":"); i > 0 && !strings.Contains(rest[i+1:], "/") {
		path, tag = rest[:i], rest[i+1:]
	}
	if !filepath.IsAbs(path) {
		return "", ""
	}
	b, err := p.hostReadFile(filepath.Join(filepath.Clean(path), "index.json"))
	if err != nil || len(b) > maxIndexBytes {
		return "", ""
	}
	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if json.Unmarshal(b, &idx) != nil {
		return "", ""
	}
	pick := -1
	for i, m := range idx.Manifests {
		if (tag == "" && len(idx.Manifests) == 1) || (tag != "" && m.Annotations["org.opencontainers.image.ref.name"] == tag) {
			if pick != -1 {
				return "", ""
			}
			pick = i
		}
	}
	if pick == -1 || !validDigest(idx.Manifests[pick].Digest) {
		return "", ""
	}
	m := idx.Manifests[pick]
	if pl := m.Platform; pl != nil && tokenRe.MatchString(pl.OS) && tokenRe.MatchString(pl.Architecture) {
		platform = pl.OS + "/" + pl.Architecture
		if tokenRe.MatchString(pl.Variant) {
			platform += "/" + pl.Variant
		}
	}
	return m.Digest, platform
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func overlaps(a, b []string) bool {
	for _, v := range a {
		if contains(b, v) {
			return true
		}
	}
	return false
}
