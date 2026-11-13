package diagnostics

import (
	"regexp"
	"sort"

	"github.com/tuna-os/fisherman/internal/probe"
)

// ProbeSummary is the probe report (docs/PROBE.md) as the bundle carries
// it. It names each kept field; anything else in the report is left out.
type ProbeSummary struct {
	ProtocolVersion int            `json:"protocol_version"`
	Disks           []DiskShape    `json:"disks"`
	TPM             probe.TPM      `json:"tpm"`
	System          SystemShape    `json:"system"`
	Live            LiveShape      `json:"live"`
	Offline         OfflineShape   `json:"offline"`
	Bootc           BootcShape     `json:"bootc"`
	GPUs            []GPUShape     `json:"gpus"`
	DMI             DMIShape       `json:"dmi"`
	Capabilities    CapsShape      `json:"capabilities"`
	BootcStore      BootcStoreInfo `json:"bootc_store"`
}

// DiskShape is one probe disk. Path, model and vendor are identifiers.
type DiskShape struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	Model          string `json:"model,omitempty"`
	Vendor         string `json:"vendor,omitempty"`
	Transport      string `json:"transport"`
	Removable      bool   `json:"removable"`
	ReadOnly       bool   `json:"read_only"`
	Eligible       bool   `json:"eligible"`
	ExcludedReason string `json:"excluded_reason,omitempty"`
}

// SystemShape is probe system.
type SystemShape struct {
	RAMBytes          int64    `json:"ram_bytes"`
	CPUThreads        int      `json:"cpu_threads"`
	CPUCores          int      `json:"cpu_cores"`
	UEFI              bool     `json:"uefi"`
	MeetsRequirements bool     `json:"meets_requirements"`
	Unmet             []string `json:"unmet"`
}

// LiveShape is probe live.
type LiveShape struct {
	IsLive     bool   `json:"is_live"`
	LiveImage  string `json:"live_image,omitempty"`
	DetectedBy string `json:"detected_by,omitempty"`
}

// OfflineShape counts the offline stores and images; their host paths and
// names are not in the bundle.
type OfflineShape struct {
	Stores int `json:"stores"`
	Images int `json:"images"`
}

// BootcShape is probe bootc.
type BootcShape struct {
	Context       string                      `json:"context"`
	Found         bool                        `json:"found"`
	Version       string                      `json:"version,omitempty"`
	VersionReason string                      `json:"version_reason,omitempty"`
	Capabilities  map[string]probe.Capability `json:"capabilities,omitempty"`
}

// GPUShape is one probe GPU.
type GPUShape struct {
	VendorID string `json:"vendor_id"`
	DeviceID string `json:"device_id"`
	Class    string `json:"class"`
}

// DMIShape is the machine model (not a serial number).
type DMIShape struct {
	Vendor  string `json:"vendor,omitempty"`
	Product string `json:"product,omitempty"`
}

// CapsShape is the versioned capability report, without the install
// defaults and image facts it also carries.
type CapsShape struct {
	Version       int                      `json:"version"`
	LiveHost      LiveHostShape            `json:"live_host"`
	Source        SourceShape              `json:"source"`
	ExecutionRoot probe.ExecutionRoot      `json:"execution_root"`
	Features      map[string]probe.Feature `json:"features,omitempty"`
}

// LiveHostShape is capabilities.live_host.
type LiveHostShape struct {
	Context        string `json:"context"`
	IsLive         bool   `json:"is_live"`
	Image          string `json:"image,omitempty"`
	Digest         string `json:"digest,omitempty"`
	IdentityReason string `json:"identity_reason,omitempty"`
	Platform       string `json:"platform,omitempty"`
	Backend        string `json:"backend,omitempty"`
	BootcVersion   string `json:"bootc_version,omitempty"`
}

// SourceShape is capabilities.source, without the store path.
type SourceShape struct {
	Selected     bool   `json:"selected"`
	Ref          string `json:"ref,omitempty"`
	Transport    string `json:"transport,omitempty"`
	Locality     string `json:"locality,omitempty"`
	Digest       string `json:"digest,omitempty"`
	DigestFrom   string `json:"digest_from,omitempty"`
	DigestReason string `json:"digest_reason,omitempty"`
	Platform     string `json:"platform,omitempty"`
}

// BootcStoreInfo is bootc_store without its item list.
type BootcStoreInfo struct {
	Version   int    `json:"version"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

var (
	digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hexIDRe  = regexp.MustCompile(`^0x[0-9a-fA-F]{1,8}$`)
	// modelRe is a hardware model or vendor name.
	modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()/,+-]{0,63}$`)
)

func summarizeProbe(p *probe.Result, rd *Redactor) ProbeSummary {
	s := ProbeSummary{
		ProtocolVersion: p.ProtocolVersion,
		Disks:           []DiskShape{},
		TPM:             probe.TPM{Present: p.TPM.Present, Version: token(p.TPM.Version, wordRe), Usable: p.TPM.Usable},
		System: SystemShape{RAMBytes: p.System.RAMBytes, CPUThreads: p.System.CPUThreads, CPUCores: p.System.CPUCores,
			UEFI: p.System.UEFI, MeetsRequirements: p.System.MeetsRequirements, Unmet: []string{}},
		Live:    LiveShape{IsLive: p.Live.IsLive, LiveImage: imageRef(p.Live.LiveImage), DetectedBy: token(p.Live.DetectedBy, wordRe)},
		Offline: OfflineShape{Stores: len(p.Offline.Stores), Images: len(p.Offline.Images)},
		Bootc: BootcShape{Context: token(p.Bootc.Context, codeRe), Found: p.Bootc.Found,
			Version: token(p.Bootc.Version, wordRe), VersionReason: token(p.Bootc.VersionReason, codeRe)},
		GPUs: []GPUShape{},
		Capabilities: CapsShape{
			Version: p.Capabilities.Version,
			LiveHost: LiveHostShape{
				Context: token(p.Capabilities.LiveHost.Context, codeRe), IsLive: p.Capabilities.LiveHost.IsLive,
				Image: imageRef(p.Capabilities.LiveHost.Image), Digest: token(p.Capabilities.LiveHost.Digest, digestRe),
				IdentityReason: token(p.Capabilities.LiveHost.IdentityReason, codeRe),
				Platform:       token(p.Capabilities.LiveHost.Platform, wordRe), Backend: token(p.Capabilities.LiveHost.Backend, codeRe),
				BootcVersion: token(p.Capabilities.LiveHost.BootcVersion, wordRe),
			},
			Source: SourceShape{
				Selected: p.Capabilities.Source.Selected, Ref: imageRef(p.Capabilities.Source.Ref),
				Transport: token(p.Capabilities.Source.Transport, wordRe), Locality: token(p.Capabilities.Source.Locality, codeRe),
				Digest: token(p.Capabilities.Source.Digest, digestRe), DigestFrom: token(p.Capabilities.Source.DigestFrom, codeRe),
				DigestReason: token(p.Capabilities.Source.DigestReason, codeRe), Platform: token(p.Capabilities.Source.Platform, wordRe),
			},
			ExecutionRoot: probe.ExecutionRoot{
				Context: token(p.Capabilities.ExecutionRoot.Context, codeRe), Established: p.Capabilities.ExecutionRoot.Established,
				Reason: token(p.Capabilities.ExecutionRoot.Reason, codeRe), Backend: token(p.Capabilities.ExecutionRoot.Backend, codeRe),
				BootcVersion: token(p.Capabilities.ExecutionRoot.BootcVersion, wordRe),
			},
		},
		BootcStore: BootcStoreInfo{Version: p.BootcStore.Version, Available: p.BootcStore.Available, Reason: token(p.BootcStore.Reason, codeRe)},
	}
	for _, d := range p.Disks {
		ds := DiskShape{Path: rd.Ident("device", d.Path), SizeBytes: d.SizeBytes, Transport: token(d.Transport, codeRe),
			Removable: d.Removable, ReadOnly: d.ReadOnly, Eligible: d.Eligible, ExcludedReason: token(d.ExcludedReason, codeRe)}
		if rd.includeIdentifiers {
			ds.Model, ds.Vendor = token(d.Model, modelRe), token(d.Vendor, modelRe)
		}
		s.Disks = append(s.Disks, ds)
	}
	for _, u := range p.System.Unmet {
		s.System.Unmet = append(s.System.Unmet, token(u, codeRe))
	}
	if len(p.Bootc.Capabilities) > 0 {
		s.Bootc.Capabilities = map[string]probe.Capability{}
		for _, k := range sortedKeys(p.Bootc.Capabilities) {
			if codeRe.MatchString(k) {
				c := p.Bootc.Capabilities[k]
				s.Bootc.Capabilities[k] = probe.Capability{Status: token(c.Status, codeRe), Reason: token(c.Reason, codeRe)}
			}
		}
	}
	if len(p.Capabilities.Features) > 0 {
		s.Capabilities.Features = map[string]probe.Feature{}
		for _, k := range sortedKeys(p.Capabilities.Features) {
			if codeRe.MatchString(k) {
				f := p.Capabilities.Features[k]
				s.Capabilities.Features[k] = probe.Feature{Status: token(f.Status, codeRe), Reason: token(f.Reason, codeRe),
					Context: token(f.Context, codeRe)}
			}
		}
	}
	for _, g := range p.GPUs {
		s.GPUs = append(s.GPUs, GPUShape{VendorID: token(g.VendorID, hexIDRe), DeviceID: token(g.DeviceID, hexIDRe), Class: token(g.Class, hexIDRe)})
	}
	s.DMI = DMIShape{Vendor: token(p.DMI.Vendor, modelRe), Product: token(p.DMI.Product, modelRe)}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
