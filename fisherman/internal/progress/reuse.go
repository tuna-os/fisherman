package progress

// SourceReuseVersion is the version of the source_reuse event
// (tuna-os/fisherman#384). Bump it when a field is removed or renamed or its
// meaning changes; new phases, reasons and fields are additions.
const SourceReuseVersion = 1

// source_reuse phases, in the order an install emits them.
const (
	ReusePlanned  = "planned"
	ReuseCopying  = "copying"
	ReuseVerified = "verified"
	ReuseFailed   = "failed"
	ReuseReleased = "released"
)

// ReuseEstimate is the space the copy needs, and what is free, in bytes.
type ReuseEstimate struct {
	// ScratchBytes: the uncompressed OCI layout bootc writes under /var/tmp
	// before it copies it into containers-storage.
	ScratchBytes uint64 `json:"scratch_bytes"`
	// StoreBytes: the unpacked image in containers-storage.
	StoreBytes uint64 `json:"store_bytes"`
	// RequiredBytes: what must be free, margin included.
	RequiredBytes uint64 `json:"required_bytes"`
	// ScratchFreeBytes / StoreFreeBytes: free space on the scratch and
	// store filesystems before the copy (the same number when they are
	// one filesystem).
	ScratchFreeBytes uint64 `json:"scratch_free_bytes"`
	StoreFreeBytes   uint64 `json:"store_free_bytes"`
	SameFilesystem   bool   `json:"same_filesystem"`
	Files            uint64 `json:"files"`
	EstimateMS       int64  `json:"estimate_ms"`
}

// ReuseMeasured is what the copy cost, measured on the live host.
type ReuseMeasured struct {
	CopyMS int64 `json:"copy_ms"`
	// ScratchPeakBytes / StorePeakBytes: the largest drop in free space
	// seen on each filesystem during the copy (sampled; one number when
	// they are one filesystem).
	ScratchPeakBytes uint64 `json:"scratch_peak_bytes"`
	StorePeakBytes   uint64 `json:"store_peak_bytes"`
	// StoreBytes: free space the copy still holds after bootc removed its
	// temporary layout.
	StoreBytes uint64 `json:"store_bytes"`
	VerifyMS   int64  `json:"verify_ms"`
}

// SourceReuseEvent is one {"type":"source_reuse"} line. docs/JSON-PROGRESS-FORMAT.md.
type SourceReuseEvent struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Phase   string `json:"phase"`
	// Digest and Platform: the item the recipe names.
	Digest   string `json:"digest"`
	Platform string `json:"platform"`
	// Source: the reference given to `bootc image copy-to-storage
	// --source` (credential-free).
	Source string `json:"source,omitempty"`
	// Destination: the containers-storage reference fisherman created.
	Destination string `json:"destination,omitempty"`
	// DestinationDigest: the copy's manifest digest. bootc re-serialises
	// the layers, so it differs from Digest; the installed system boots
	// this one.
	DestinationDigest string         `json:"destination_digest,omitempty"`
	Estimate          *ReuseEstimate `json:"estimate,omitempty"`
	Measured          *ReuseMeasured `json:"measured,omitempty"`
	// Code and Reason say why, for "failed" (Code is the error event's
	// code) and "released" (removed, remove_failed).
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// SourceReuse emits a source_reuse event.
func SourceReuse(e SourceReuseEvent) {
	e.Type, e.Version = "source_reuse", SourceReuseVersion
	write(e)
}
