package receipt

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Outcomes. Only Verified is success; every other outcome, including a
// missing or unreadable status, is not.
const (
	OutcomeVerified    = "verified"
	OutcomeMismatch    = "mismatch"
	OutcomeUnsupported = "unsupported"
	OutcomeUnavailable = "unavailable"
	OutcomeNotYetRun   = "not-yet-run"
)

// Reason codes. A code is never renamed or reused; new codes are additions.
const (
	// verified
	ReasonBootedMatchesReceipt = "booted_matches_receipt"

	// mismatch
	ReasonBackendMismatch      = "backend_mismatch"
	ReasonDeploymentMismatch   = "deployment_mismatch"
	ReasonBootedDigestMismatch = "booted_digest_mismatch"
	ReasonExpectedStaged       = "expected_image_staged_not_booted"
	ReasonExpectedCached       = "expected_image_cached_not_booted"
	ReasonExpectedRollback     = "expected_image_is_rollback"
	ReasonPlatformMismatch     = "platform_mismatch"
	ReasonUpdateRefMismatch    = "update_ref_mismatch"

	// unsupported
	ReasonStatusFormatUnsupported      = "status_format_unsupported"
	ReasonStatusAPIUnsupported         = "status_api_unsupported"
	ReasonReceiptVersionUnsupported    = "receipt_version_unsupported"
	ReasonCollectionVersionUnsupported = "collection_version_unsupported"

	// unavailable
	ReasonReceiptMissing           = "receipt_missing"
	ReasonReceiptMalformed         = "receipt_malformed"
	ReasonCollectionMalformed      = "collection_malformed"
	ReasonWrongExecutionContext    = "wrong_execution_context"
	ReasonBootcAbsent              = "bootc_absent"
	ReasonStatusFailed             = "status_failed"
	ReasonStatusMalformed          = "status_malformed"
	ReasonNotBootedBootc           = "not_booted_bootc"
	ReasonBootedIdentityUnreported = "booted_identity_unreported"
	ReasonExpectedIdentityUnknown  = "expected_identity_unknown"

	// not-yet-run
	ReasonCollectionMissing = "collection_missing"
)

// Check results.
const (
	CheckMatch       = "match"
	CheckMismatch    = "mismatch"
	CheckNotRecorded = "not_recorded" // the receipt has no value
	CheckNotReported = "not_reported" // bootc status has no value
	CheckNotChecked  = "not_checked"  // the evaluation stopped before it
)

// Check names.
const (
	CheckBackend    = "backend"
	CheckDeployment = "deployment"
	CheckDigest     = "platform_manifest_digest"
	CheckPlatform   = "platform"
	CheckUpdateRef  = "update_ref"
)

// Evidence: where the bootc status came from.
const (
	// EvidenceLive: `bootc status` ran now, on the system whose root is /.
	EvidenceLive = "live_status"
	// EvidenceCollected: the first-boot unit of this install collected it.
	EvidenceCollected = "collected_status"
)

// Result is the verification result.
type Result struct {
	Schema   string `json:"schema"`
	Version  int    `json:"version"`
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason"`
	Evidence string `json:"evidence"`
	// VerifiedAt is when this result was computed.
	VerifiedAt string            `json:"verified_at"`
	Tools      Tools             `json:"tools"`
	InstallID  string            `json:"install_id"`
	Expected   ExpectedIdentity  `json:"expected"`
	Booted     BootedIdentity    `json:"booted"`
	Checks     map[string]string `json:"checks"`
	// Other entries bootc status reports. They are never the booted
	// deployment, whatever they hold.
	Staged   OtherEntry `json:"staged"`
	Rollback OtherEntry `json:"rollback"`
	// CachedUpdateDigest is an update bootc downloaded but did not deploy.
	CachedUpdateDigest string `json:"cached_update_digest"`
}

// Tools are the versions that produced the evidence and the result.
type Tools struct {
	Fisherman string `json:"fisherman"`
	Bootc     string `json:"bootc"`
}

// ExpectedIdentity is the receipt's side.
type ExpectedIdentity struct {
	Backend                string `json:"backend"`
	DeploymentID           string `json:"deployment_id"`
	PlatformManifestDigest string `json:"platform_manifest_digest"`
	DigestFrom             string `json:"digest_from"`
	IndexDigest            string `json:"index_digest"`
	Platform               string `json:"platform"`
	UpdateRef              string `json:"update_ref"`
	InstalledAt            string `json:"installed_at"`
}

// BootedIdentity is what bootc status reports for the booted entry.
type BootedIdentity struct {
	Backend        string `json:"backend"`
	DeploymentID   string `json:"deployment_id"`
	Image          string `json:"image"`
	Transport      string `json:"transport"`
	ManifestDigest string `json:"manifest_digest"`
	Architecture   string `json:"architecture"`
	ImageVersion   string `json:"image_version"`
	ImageTimestamp string `json:"image_timestamp"`
	// BootID and CollectedAt are set for collected evidence.
	BootID      string `json:"boot_id"`
	CollectedAt string `json:"collected_at"`
}

// OtherEntry is a staged or rollback entry.
type OtherEntry struct {
	Present        bool   `json:"present"`
	DeploymentID   string `json:"deployment_id"`
	ManifestDigest string `json:"manifest_digest"`
}

// Input is everything Evaluate reads. It runs nothing itself.
type Input struct {
	// Receipt is the receipt file's content; ReceiptErr its read error.
	Receipt    []byte
	ReceiptErr error
	// Evidence is EvidenceLive or EvidenceCollected.
	Evidence string
	// Collection is collection.json (EvidenceCollected only).
	Collection    []byte
	CollectionErr error
	// Status is the bootc status output. For EvidenceLive, StatusErr is
	// the command's error; for EvidenceCollected, the status file's read
	// error.
	Status    []byte
	StatusErr error
	// BootcVersion is the first line of `bootc --version`, as reported.
	BootcVersion     string
	FishermanVersion string
	Now              time.Time
}

// collection is collection.json.
type collection struct {
	Schema      string `json:"schema"`
	Version     *int   `json:"version"`
	InstallID   string `json:"install_id"`
	CollectedAt string `json:"collected_at"`
	BootID      string `json:"boot_id"`
	BootcExit   *int   `json:"bootc_exit"`
}

// bootc status --format json --format-version 1. Only the fields the
// verifier reads; bootc adds fields between releases, and they are ignored.
type hostStatus struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Status     *struct {
		Staged   *bootEntry `json:"staged"`
		Booted   *bootEntry `json:"booted"`
		Rollback *bootEntry `json:"rollback"`
	} `json:"status"`
}

type bootEntry struct {
	Image        *imageStatus `json:"image"`
	CachedUpdate *imageStatus `json:"cachedUpdate"`
	Ostree       *struct {
		Checksum  string `json:"checksum"`
		Stateroot string `json:"stateroot"`
	} `json:"ostree"`
	Composefs *struct {
		Verity string `json:"verity"`
	} `json:"composefs"`
}

type imageStatus struct {
	Image struct {
		Image     string `json:"image"`
		Transport string `json:"transport"`
	} `json:"image"`
	Version      *string `json:"version"`
	Timestamp    *string `json:"timestamp"`
	ImageDigest  string  `json:"imageDigest"`
	Architecture string  `json:"architecture"`
}

// StatusAPIVersion is the apiVersion of `--format-version 1`.
const StatusAPIVersion = "org.containers.bootc/v1"

var (
	bootIDRe    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	timestampRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})$`)
)

// Evaluate compares a receipt with bootc status. It never returns Verified
// unless every identity the receipt records was reported by bootc for the
// booted entry and matched, and at least one of the deployment id and the
// platform manifest digest was compared.
func Evaluate(in Input) Result {
	res := Result{
		Schema:   ResultSchema,
		Version:  ResultVersion,
		Evidence: in.Evidence,
		Tools: Tools{
			Fisherman: sanitizeVersion(in.FishermanVersion),
			Bootc:     sanitizeVersion(in.BootcVersion),
		},
		Checks: map[string]string{},
	}
	if !in.Now.IsZero() {
		res.VerifiedAt = in.Now.UTC().Format(time.RFC3339)
	}
	for _, c := range []string{CheckBackend, CheckDeployment, CheckDigest, CheckPlatform, CheckUpdateRef} {
		res.Checks[c] = CheckNotChecked
	}
	done := func(outcome, reason string) Result {
		res.Outcome, res.Reason = outcome, reason
		return res
	}

	// 1. The receipt: what this install expects.
	if in.ReceiptErr != nil {
		if errors.Is(in.ReceiptErr, fs.ErrNotExist) {
			return done(OutcomeUnavailable, ReasonReceiptMissing)
		}
		return done(OutcomeUnavailable, ReasonReceiptMalformed)
	}
	var rc Receipt
	if json.Unmarshal(in.Receipt, &rc) != nil || rc.Schema != ReceiptSchema {
		return done(OutcomeUnavailable, ReasonReceiptMalformed)
	}
	if rc.Version != ReceiptVersion {
		return done(OutcomeUnsupported, ReasonReceiptVersionUnsupported)
	}
	if !hexIDRe.MatchString(rc.InstallID) {
		return done(OutcomeUnavailable, ReasonReceiptMalformed)
	}
	res.InstallID = rc.InstallID
	res.Expected = expectedFrom(rc)

	// 2. Where the status came from.
	switch in.Evidence {
	case EvidenceCollected:
		if in.CollectionErr != nil {
			if errors.Is(in.CollectionErr, fs.ErrNotExist) {
				return done(OutcomeNotYetRun, ReasonCollectionMissing)
			}
			return done(OutcomeUnavailable, ReasonCollectionMalformed)
		}
		var c collection
		if json.Unmarshal(in.Collection, &c) != nil || c.Schema != CollectionSchema || c.Version == nil || c.BootcExit == nil {
			return done(OutcomeUnavailable, ReasonCollectionMalformed)
		}
		if *c.Version != CollectionVersion {
			return done(OutcomeUnsupported, ReasonCollectionVersionUnsupported)
		}
		// The collector of this install wrote the receipt's id into it.
		// Anything else was collected somewhere else.
		if c.InstallID != rc.InstallID {
			return done(OutcomeUnavailable, ReasonWrongExecutionContext)
		}
		if bootIDRe.MatchString(c.BootID) {
			res.Booted.BootID = c.BootID
		}
		if timestampRe.MatchString(c.CollectedAt) {
			res.Booted.CollectedAt = c.CollectedAt
		}
		switch code := *c.BootcExit; {
		case code == 127:
			return done(OutcomeUnavailable, ReasonBootcAbsent)
		case code == 2:
			return done(OutcomeUnsupported, ReasonStatusFormatUnsupported)
		case code != 0:
			return done(OutcomeUnavailable, ReasonStatusFailed)
		}
		if in.StatusErr != nil {
			return done(OutcomeUnavailable, ReasonCollectionMalformed)
		}
	case EvidenceLive:
		if in.StatusErr != nil {
			switch code := exitCode(in.StatusErr); {
			case errors.Is(in.StatusErr, exec.ErrNotFound), errors.Is(in.StatusErr, fs.ErrNotExist), code == 127:
				return done(OutcomeUnavailable, ReasonBootcAbsent)
			case code == 2:
				// clap's status for an unknown flag: a bootc without
				// --format-version.
				return done(OutcomeUnsupported, ReasonStatusFormatUnsupported)
			default:
				return done(OutcomeUnavailable, ReasonStatusFailed)
			}
		}
	default:
		return done(OutcomeUnavailable, ReasonWrongExecutionContext)
	}

	// 3. The status itself.
	var st hostStatus
	if json.Unmarshal(in.Status, &st) != nil {
		return done(OutcomeUnavailable, ReasonStatusMalformed)
	}
	if st.APIVersion != StatusAPIVersion || st.Kind != "BootcHost" {
		return done(OutcomeUnsupported, ReasonStatusAPIUnsupported)
	}
	if st.Status == nil || st.Status.Booted == nil || st.Status.Booted.Image == nil {
		return done(OutcomeUnavailable, ReasonNotBootedBootc)
	}
	b := st.Status.Booted
	res.Booted.Backend, res.Booted.DeploymentID = entryIdentity(b)
	res.Booted.Image = sanitizeRef(b.Image.Image.Image)
	if tokenRe.MatchString(b.Image.Image.Transport) {
		res.Booted.Transport = b.Image.Image.Transport
	}
	if validDigest(b.Image.ImageDigest) {
		res.Booted.ManifestDigest = b.Image.ImageDigest
	}
	if tokenRe.MatchString(b.Image.Architecture) {
		res.Booted.Architecture = b.Image.Architecture
	}
	if b.Image.Version != nil {
		res.Booted.ImageVersion = sanitizeVersion(*b.Image.Version)
	}
	if b.Image.Timestamp != nil && timestampRe.MatchString(*b.Image.Timestamp) {
		res.Booted.ImageTimestamp = *b.Image.Timestamp
	}
	res.Staged = other(st.Status.Staged)
	res.Rollback = other(st.Status.Rollback)
	for _, e := range []*bootEntry{b, st.Status.Staged} {
		if e != nil && e.CachedUpdate != nil && validDigest(e.CachedUpdate.ImageDigest) {
			res.CachedUpdateDigest = e.CachedUpdate.ImageDigest
			break
		}
	}

	// 4. Compare. Every check runs, so the result shows all of them; the
	// reason is the first mismatch in this order.
	exp := res.Expected
	var mismatch string
	note := func(reason string) {
		if mismatch == "" {
			mismatch = reason
		}
	}
	unreported := false
	compare := func(name, want, got string) string {
		switch {
		case want == "":
			res.Checks[name] = CheckNotRecorded
		case got == "":
			res.Checks[name] = CheckNotReported
		case want == got:
			res.Checks[name] = CheckMatch
		default:
			res.Checks[name] = CheckMismatch
		}
		return res.Checks[name]
	}

	if compare(CheckBackend, exp.Backend, res.Booted.Backend) == CheckMismatch {
		note(ReasonBackendMismatch)
	}

	switch compare(CheckDeployment, exp.DeploymentID, res.Booted.DeploymentID) {
	case CheckMismatch:
		switch {
		case res.Staged.DeploymentID == exp.DeploymentID:
			note(ReasonExpectedStaged)
		case res.Rollback.DeploymentID == exp.DeploymentID:
			note(ReasonExpectedRollback)
		default:
			note(ReasonDeploymentMismatch)
		}
	case CheckNotReported:
		unreported = true
	}

	// bootc reports the platform manifest digest. An index digest is a
	// different object and is never compared with it.
	switch compare(CheckDigest, exp.PlatformManifestDigest, res.Booted.ManifestDigest) {
	case CheckMismatch:
		switch {
		case res.Staged.ManifestDigest == exp.PlatformManifestDigest:
			note(ReasonExpectedStaged)
		case res.CachedUpdateDigest == exp.PlatformManifestDigest:
			note(ReasonExpectedCached)
		case res.Rollback.ManifestDigest == exp.PlatformManifestDigest:
			note(ReasonExpectedRollback)
		default:
			note(ReasonBootedDigestMismatch)
		}
	case CheckNotReported:
		unreported = true
	}

	// Older bootc does not report the architecture; the digest is
	// platform-specific already, so a missing one is not a failure.
	if compare(CheckPlatform, normArch(platformArch(exp.Platform)), normArch(res.Booted.Architecture)) == CheckMismatch {
		note(ReasonPlatformMismatch)
	}

	gotRef := ""
	if res.Booted.Image != "" {
		gotRef = normRef(res.Booted.Transport, res.Booted.Image)
	}
	wantRef := ""
	if exp.UpdateRef != "" {
		t, n := splitTransport(exp.UpdateRef)
		wantRef = normRef(t, n)
	}
	switch compare(CheckUpdateRef, wantRef, gotRef) {
	case CheckMismatch:
		note(ReasonUpdateRefMismatch)
	case CheckNotReported:
		unreported = true
	}

	switch {
	case mismatch != "":
		return done(OutcomeMismatch, mismatch)
	case unreported:
		return done(OutcomeUnavailable, ReasonBootedIdentityUnreported)
	case res.Checks[CheckDeployment] != CheckMatch && res.Checks[CheckDigest] != CheckMatch:
		return done(OutcomeUnavailable, ReasonExpectedIdentityUnknown)
	}
	return done(OutcomeVerified, ReasonBootedMatchesReceipt)
}

func expectedFrom(rc Receipt) ExpectedIdentity {
	e := ExpectedIdentity{
		InstalledAt: rc.InstalledAt,
		UpdateRef:   sanitizeRef(rc.UpdateRef),
	}
	if !timestampRe.MatchString(e.InstalledAt) {
		e.InstalledAt = ""
	}
	if rc.Backend == BackendOstree || rc.Backend == BackendComposefs {
		e.Backend = rc.Backend
	}
	switch {
	case e.Backend == BackendOstree && ostreeCsRe.MatchString(rc.Deployment.ID),
		e.Backend == BackendComposefs && verityRe.MatchString(rc.Deployment.ID):
		e.DeploymentID = rc.Deployment.ID
	}
	if validDigest(rc.Expected.PlatformManifestDigest) {
		e.PlatformManifestDigest = rc.Expected.PlatformManifestDigest
		if tokenRe.MatchString(rc.Expected.DigestFrom) {
			e.DigestFrom = rc.Expected.DigestFrom
		}
	}
	if validDigest(rc.Expected.IndexDigest) {
		e.IndexDigest = rc.Expected.IndexDigest
	}
	if platformRe.MatchString(rc.Expected.Platform) {
		e.Platform = rc.Expected.Platform
	}
	return e
}

func entryIdentity(e *bootEntry) (backend, id string) {
	switch {
	case e.Composefs != nil:
		backend = BackendComposefs
		if verityRe.MatchString(e.Composefs.Verity) {
			id = e.Composefs.Verity
		}
	case e.Ostree != nil:
		backend = BackendOstree
		if ostreeCsRe.MatchString(e.Ostree.Checksum) {
			id = e.Ostree.Checksum
		}
	}
	return backend, id
}

func other(e *bootEntry) OtherEntry {
	if e == nil {
		return OtherEntry{}
	}
	o := OtherEntry{Present: true}
	_, o.DeploymentID = entryIdentity(e)
	if e.Image != nil && validDigest(e.Image.ImageDigest) {
		o.ManifestDigest = e.Image.ImageDigest
	}
	return o
}

// platformArch returns the architecture of "os/arch[/variant]".
func platformArch(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// normArch maps kernel and OCI architecture names to one spelling.
func normArch(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "ppc64le", "powerpc64le":
		return "ppc64le"
	}
	return a
}

// normRef is a reference with its transport, registry being the default.
func normRef(transport, name string) string {
	if transport == "" || transport == TransportRegistry || transport == "docker" {
		return TransportRegistry + ":" + strings.TrimPrefix(name, "docker://")
	}
	return transport + ":" + name
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
