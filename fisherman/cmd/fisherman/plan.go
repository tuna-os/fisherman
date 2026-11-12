package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// planProtocolVersion is the schema version of the `plan --json` output.
// Bump it on any change a frontend could notice (a removed or renamed
// field, a changed meaning); adding a field does not. Schema: docs/PLAN.md.
const planProtocolVersion = 1

// planUnknownValue stands in for a value that only the install can
// resolve: one that needs an image pull, a registry or the target disk's
// contents. Every field that carries it has an entry in plan.unknown.
const planUnknownValue = "unknown"

// planResult is the `plan --json` output. The validation half is the
// `validate --json` result: when valid is false, errors holds the same
// problems validate reports and plan is null.
type planResult struct {
	ProtocolVersion int              `json:"protocol_version"`
	Valid           bool             `json:"valid"`
	Errors          []recipe.Problem `json:"errors"`
	Plan            *installPlan     `json:"plan"`
}

// installPlan is what an install of the recipe would do, as far as it can
// be known without a pull, a registry or a write. It never holds a secret:
// no passphrase, password or recovery key, and not the raw recipe.
type installPlan struct {
	Source     planSource      `json:"source"`
	Image      planImage       `json:"image"`
	Target     planTarget      `json:"target"`
	Boot       planBoot        `json:"boot"`
	Encryption planEncryption  `json:"encryption"`
	Partitions []planPartition `json:"partitions"`
	VarDisk    *planVarDisk    `json:"var_disk"`
	Destroys   []planDestroy   `json:"destroys"`
	Capacity   *planCapacity   `json:"capacity"`
	Scratch    planScratch     `json:"scratch"`
	Steps      []planStep      `json:"steps"`
	Unknown    []planUnknown   `json:"unknown"`
}

type planSource struct {
	// Kind: "registry", "containers-storage", "oci", "live" (the running
	// live image) or "source-reuse" (this host's bootc store).
	Kind         string `json:"kind"`
	Image        string `json:"image"`
	TargetImgref string `json:"target_imgref"`
	Digest       string `json:"digest"`
	// Pull: "none" when no registry is read, else "unknown".
	Pull string `json:"pull"`
}

// planImage is what `plan --read-image` read about the source image
// without a pull (install.InspectImageForPlan): its identity and the sizes
// its manifest and its local store report. Without --read-image, or when
// nothing answers, read_from is "none" and the rest is unknown.
type planImage struct {
	// ReadFrom: "containers-storage", "oci", "registry" or "none".
	ReadFrom       string `json:"read_from"`
	ManifestDigest string `json:"manifest_digest"` // or "unknown"
	PlatformDigest string `json:"platform_digest"` // or "unknown"
	Platform       string `json:"platform"`        // os/architecture, or "unknown"
	Layers         int    `json:"layers,omitempty"`
	// CompressedBytes: the sum of the layer sizes the manifest lists.
	CompressedBytes int64 `json:"compressed_bytes,omitempty"`
	// UnpackedBytes: the unpacked size local containers-storage reports.
	UnpackedBytes int64 `json:"unpacked_bytes,omitempty"`
}

type planTarget struct {
	Layout    string `json:"layout"` // "auto" or "manual"
	Disk      string `json:"disk,omitempty"`
	Device    string `json:"device,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Loop      bool   `json:"loop,omitempty"`
	// StableID is the WWN, else the serial, else "unknown".
	StableID string        `json:"stable_id,omitempty"`
	Identity *planIdentity `json:"identity,omitempty"`
}

// planIdentity is what the target disk reports about itself (probe.Target),
// read without a write: the values a frontend copies into the recipe's
// diskIdentity to bind the install to this disk.
type planIdentity struct {
	Serial    string   `json:"serial"` // or "unknown"
	WWN       string   `json:"wwn"`    // or "unknown"
	Model     string   `json:"model"`  // or "unknown"
	SizeBytes int64    `json:"size_bytes,omitempty"`
	ByID      []string `json:"by_id"`
	// Bound: the recipe has a diskIdentity, and this disk matches it (a
	// recipe it does not match is invalid and has no plan).
	Bound bool `json:"bound"`
}

// planCapacity is what the automatic layout leaves for the root, and the
// minimum fisherman holds the disk to. Manual layouts have none: plan does
// not read the size of the partitions they keep.
type planCapacity struct {
	MinDiskBytes int64 `json:"min_disk_bytes"`
	// DiskMinimum: "met", "overridden" (smaller, and "too_small" is in
	// override), "not_applicable" (a loop device) or "unknown".
	DiskMinimum   string `json:"disk_minimum"`
	ReservedBytes int64  `json:"reserved_bytes"`
	RootBytes     int64  `json:"root_bytes,omitempty"`
	// RootMinimum: always "unknown": fisherman holds the root to no
	// minimum of its own; root_need_bytes and image_fit compare it with
	// the image.
	RootMinimum string `json:"root_minimum"`
	// RootNeedBytes: the root one deployment of the image takes, estimated
	// from its unpacked size. Left out when that is unknown.
	RootNeedBytes int64 `json:"root_need_bytes,omitempty"`
	// ImageFit: "enough", "not_enough" or "unknown" (planImageFit).
	ImageFit string `json:"image_fit"`
}

type planBoot struct {
	Bootloader      string `json:"bootloader"`                 // "grub2" or "systemd"
	PartitionLayout string `json:"partition_layout,omitempty"` // disk.Layout; auto layout only
	ComposeFs       bool   `json:"composefs"`
}

type planEncryption struct {
	Type        string `json:"type"`
	LUKS        bool   `json:"luks"`
	TPM2        bool   `json:"tpm2"`
	Passphrase  bool   `json:"passphrase"`   // the recipe's passphrase unlocks the root
	RecoveryKey string `json:"recovery_key"` // "generated" (tpm2-luks) or "none"
}

type planPartition struct {
	Number       int    `json:"number,omitempty"`
	Device       string `json:"device"`
	Role         string `json:"role"`
	Created      bool   `json:"created"`
	GPTName      string `json:"gpt_name,omitempty"`
	GPTType      string `json:"gpt_type,omitempty"`
	Size         string `json:"size,omitempty"` // sfdisk size, or "rest" of the disk
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Action       string `json:"action"` // disk.MountAction
	Filesystem   string `json:"filesystem,omitempty"`
	Encryption   string `json:"encryption,omitempty"` // "luks2"
	ZFSPool      string `json:"zfs_pool,omitempty"`
	Mount        string `json:"mount,omitempty"`
	MountOptions string `json:"mount_options,omitempty"`
}

type planVarDisk struct {
	Disk       string `json:"disk"`
	Device     string `json:"device"`
	Action     string `json:"action"` // "format" or "preserve"
	Filesystem string `json:"filesystem,omitempty"`
	Mount      string `json:"mount"`
}

type planDestroy struct {
	Device string `json:"device"`
	// Effect: "wipe_disk" (every partition and signature), "format" or
	// "mkswap" (one partition).
	Effect string `json:"effect"`
}

type planScratch struct {
	Path     string `json:"path"`
	OnTarget bool   `json:"on_target"`
	// Bytes: always "unknown" (protocol 1); NeedBytes is the estimate.
	Bytes string `json:"bytes"`
	// NeedBytes: an upper estimate of the scratch space the install takes,
	// image.compressed_bytes + image.unpacked_bytes. Left out when either
	// is unknown.
	NeedBytes int64 `json:"need_bytes,omitempty"`
}

type planStep struct {
	Number int    `json:"number"`
	StepID string `json:"step_id"`
	Name   string `json:"name"`
}

type planUnknown struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

const planUsage = `usage: fisherman plan --json [--read-image] -

Print what an install of the recipe on stdin would do, as one JSON object
on stdout: the source, the target, the partitions it creates or keeps, the
encryption type, the boot stack and the ordered steps. It runs the checks
of 'validate --json' first. It needs no root, writes nothing, pulls
nothing and prints no secret. A value only the install can resolve is
"unknown". The recipe is read from stdin only: it can hold the LUKS
passphrase. Schema: docs/PLAN.md.

--read-image  also read the source image's manifest and size, without a
              pull: from local containers-storage, else from its registry
              (the only network read plan makes; it reads no layer)

Exit 0 with a plan, 1 if the recipe is invalid, 2 on bad arguments.
`

// runPlan implements `fisherman plan --json -` and returns the exit code.
func runPlan(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	jsonOut, fromStdin, readImage := false, false, false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "--read-image":
			readImage = true
		case "-":
			if fromStdin {
				fmt.Fprintf(stderr, "plan: - is given twice\n\n%s", planUsage)
				return exitUsage
			}
			fromStdin = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, planUsage)
			return 0
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(stderr, "plan: unknown argument %q\n\n%s", a, planUsage)
			} else {
				fmt.Fprintf(stderr, "plan: the recipe is read from stdin only (-), not from %q: it can hold the LUKS passphrase\n\n%s", a, planUsage)
			}
			return exitUsage
		}
	}
	if !jsonOut || !fromStdin {
		fmt.Fprintf(stderr, "plan: needs --json and - (the recipe on stdin)\n\n%s", planUsage)
		return exitUsage
	}

	var r *recipe.Recipe
	var ps []recipe.Problem
	raw, err := io.ReadAll(io.LimitReader(stdin, recipe.MaxRecipeBytes+1))
	switch {
	case err != nil:
		ps = []recipe.Problem{{Code: recipe.CodeRecipeUnreadable, Message: fmt.Sprintf("reading recipe: %v", err)}}
	case len(raw) > recipe.MaxRecipeBytes:
		ps = []recipe.Problem{{Code: recipe.CodeRecipeUnreadable, Message: fmt.Sprintf("reading recipe: stdin holds more than %d bytes", recipe.MaxRecipeBytes)}}
	default:
		r, ps = fullProblems(raw)
	}

	res := planResult{ProtocolVersion: planProtocolVersion, Valid: len(ps) == 0, Errors: ps}
	if res.Errors == nil {
		res.Errors = []recipe.Problem{}
	}
	if res.Valid {
		res.Plan = buildPlan(r, readImage)
	}
	if err := writeJSON(stdout, res); err != nil {
		fmt.Fprintf(stderr, "plan: encoding output: %v\n", err)
		return exitFailure
	}
	if !res.Valid {
		return exitFailure
	}
	return 0
}

// autoLayout is the GPT layout the auto-partition path writes for r. The
// install partitions with it and `plan` reports it.
func autoLayout(r *recipe.Recipe) disk.Layout {
	switch {
	case r.IsZFS():
		return disk.LayoutZFS
	case r.UsesSystemdBoot():
		return disk.LayoutSystemdBoot
	}
	return disk.LayoutGRUB2
}

// buildPlan describes the install of r, a recipe installProblems accepts.
// It reads only what validate reads (the target's probe facts, the live
// marker) and what the install would decide from the recipe: the step list
// (planSteps), the partition table (disk.LayoutPartitions), the order and
// the action of each manual mount (disk.OrderMounts, MountSpec.Action).
// With readImage it also reads the source image's manifest and local store
// metadata (install.InspectImageForPlan), never a layer.
func buildPlan(r *recipe.Recipe, readImage bool) *installPlan {
	givenDisk, givenVarDisk := r.Disk, ""
	if r.VarDisk != nil {
		givenVarDisk = r.VarDisk.Disk
	}
	resolveTargets(r) // as the install does after validation
	p := &installPlan{Partitions: []planPartition{}, Destroys: []planDestroy{}, Unknown: []planUnknown{}}
	unknown := func(field, reason string) { p.Unknown = append(p.Unknown, planUnknown{field, reason}) }

	// ── Source ──
	p.Source = planSource{Image: r.Image, TargetImgref: r.TargetImgref, Digest: planUnknownValue, Pull: planUnknownValue}
	switch {
	case r.SourceReuse != nil:
		p.Source.Kind = "source-reuse"
		p.Source.Digest = r.SourceReuse.Digest
		p.Source.Pull = "none"
	case r.Image == "":
		p.Source.Kind = "live"
		live, _ := checkImageSource(r)
		p.Source.Image = live.LiveImage
		if p.Source.Image == "" {
			p.Source.Image = planUnknownValue
			unknown("source.image", "the live host does not name its running image")
		}
		p.Source.Pull = "none"
	case install.IsOCISource(r.Image):
		p.Source.Kind = "oci"
		p.Source.Pull = "none"
	case strings.HasPrefix(r.Image, "containers-storage:"):
		p.Source.Kind = "containers-storage"
		p.Source.Pull = "none"
	default:
		p.Source.Kind = "registry"
	}
	if p.Source.TargetImgref == "" {
		p.Source.TargetImgref = p.Source.Image // the installed system tracks its source
		if p.Source.TargetImgref == planUnknownValue {
			unknown("source.target_imgref", "the installed system tracks the running live image, which the host does not name")
		}
	}
	img, imgWhy := planImageFacts(r, p.Source.Kind, readImage)
	p.Image = img
	if p.Source.Digest == planUnknownValue && img.ManifestDigest != planUnknownValue {
		p.Source.Digest = img.ManifestDigest
	}
	if img.ReadFrom == "none" {
		unknown("image", imgWhy)
	} else {
		if img.PlatformDigest == planUnknownValue {
			unknown("image.platform_digest", "only a registry manifest lists the platform manifests; plan read a local copy, or the registry did not give its raw manifest")
		}
		if img.Platform == planUnknownValue {
			unknown("image.platform", "the image configuration names no platform")
		}
		if img.Layers == 0 {
			unknown("image.layers", "the image metadata lists no layers")
		}
		if img.CompressedBytes == 0 {
			unknown("image.compressed_bytes", "the manifest lists no layer sizes (skopeo before 1.6 prints none)")
		}
		if img.UnpackedBytes == 0 {
			unknown("image.unpacked_bytes", "only local containers-storage reports the unpacked size; a registry manifest and an OCI layout list compressed sizes")
		}
	}
	if p.Source.Digest == planUnknownValue {
		reason := "the install reads the image identity when it pulls or opens the image; plan reads it only with --read-image"
		if readImage {
			reason = "plan could not read the image: " + imgWhy
		}
		unknown("source.digest", reason)
	}
	if p.Source.Pull == planUnknownValue {
		unknown("source.pull", "whether the image is in a local store or must be pulled is known only when the install checks the image cache")
	}
	if r.SourceReuse == nil {
		unknown("source.media_input", "on live media the install first binds the verified media input (probe media_input), which can replace the source; plan does not verify it")
	}

	// ── Boot and encryption ──
	p.Boot = planBoot{Bootloader: "grub2", ComposeFs: r.UsesComposeFs()}
	if r.UsesSystemdBoot() {
		p.Boot.Bootloader = "systemd"
	}
	encType := r.Encryption.Type
	if encType == "" {
		encType = "none"
	}
	p.Encryption = planEncryption{Type: encType, LUKS: r.EncryptsRoot(), TPM2: r.EnrollsTPM2(), RecoveryKey: "none"}
	p.Encryption.Passphrase = p.Encryption.LUKS && r.Encryption.Type != "tpm2-luks"
	if p.Encryption.LUKS && r.Encryption.Type == "tpm2-luks" {
		p.Encryption.RecoveryKey = "generated"
	}

	// ── Target and partitions ──
	if r.IsManualLayout() {
		p.Target = planTarget{Layout: "manual"}
		unknown("capacity", "a manual layout keeps the partitions it names; plan does not read their sizes")
		specs := make([]disk.MountSpec, 0, len(r.CustomMounts))
		for _, cm := range r.CustomMounts {
			specs = append(specs, disk.MountSpec{Partition: cm.Partition, Target: cm.Target, Fstype: cm.Fstype})
		}
		for i, s := range disk.OrderMounts(specs) {
			pp := planPartition{Device: s.Partition, Role: manualRole(s.Target), Action: string(s.Action()), Mount: s.Target}
			switch s.Action() {
			case disk.MountFormat:
				pp.Filesystem = s.Fstype
				p.Destroys = append(p.Destroys, planDestroy{s.Partition, "format"})
			case disk.MountSwap:
				pp.Filesystem = "swap"
				p.Destroys = append(p.Destroys, planDestroy{s.Partition, "mkswap"})
			case disk.MountPreserve:
				pp.Filesystem = planUnknownValue
				unknown(fmt.Sprintf("partitions[%d].filesystem", i), "a kept partition is mounted with the filesystem it holds; plan does not read it")
			case disk.MountIgnore:
				// Not touched: no filesystem, nothing destroyed.
			}
			if s.Target == "/" {
				pp.MountOptions = r.RootMountOptions()
			}
			p.Partitions = append(p.Partitions, pp)
		}
	} else {
		layout := autoLayout(r)
		p.Boot.PartitionLayout = string(layout)
		p.Target = planTarget{Layout: "auto", Disk: givenDisk, Device: r.Disk}
		t, terr := hostTarget(r.Disk)
		if terr == nil {
			p.Target.SizeBytes, p.Target.Loop = t.SizeBytes, t.Loop
		} else {
			unknown("target.size_bytes", "the probe could not read the target disk")
		}
		p.Target.Identity, p.Target.StableID = planTargetIdentity(r, t, terr, unknown)
		p.Destroys = append(p.Destroys, planDestroy{r.Disk, "wipe_disk"})
		parts, _ := disk.LayoutPartitions(layout)
		p.Capacity = &planCapacity{MinDiskBytes: probe.MinDiskBytes, DiskMinimum: planUnknownValue,
			ReservedBytes: planAlignmentBytes, RootMinimum: planUnknownValue, RootNeedBytes: img.UnpackedBytes,
			ImageFit: planUnknownValue}
		if img.UnpackedBytes == 0 {
			unknown("capacity.root_need_bytes", "the estimate is the image's unpacked size, which only local containers-storage reports")
		}
		switch {
		case terr != nil:
			unknown("capacity.disk_minimum", "the probe could not read the target disk")
		case t.Loop:
			p.Capacity.DiskMinimum = "not_applicable" // an image for another machine
		case t.SizeBytes >= probe.MinDiskBytes:
			p.Capacity.DiskMinimum = "met"
		default:
			p.Capacity.DiskMinimum = "overridden" // else validate refused it (disk_too_small)
		}
		for _, part := range parts {
			if n, ok := sfdiskBytes(part.Size); ok {
				p.Capacity.ReservedBytes += n
			}
		}
		if terr == nil && t.SizeBytes > p.Capacity.ReservedBytes {
			p.Capacity.RootBytes = t.SizeBytes - p.Capacity.ReservedBytes
		} else {
			unknown("capacity.root_bytes", "the target disk size is unknown, or no larger than the partitions before the root")
		}
		unknown("capacity.root_minimum", "fisherman sets no root minimum beyond the disk minimum; root_need_bytes and image_fit compare the root with the image")
		for _, part := range parts {
			pp := planPartition{Number: part.Number, Device: disk.PartName(r.Disk, part.Number), Role: part.Role,
				Created: true, GPTName: part.Name, GPTType: part.Type, Size: part.Size, Action: string(disk.MountFormat)}
			if n, ok := sfdiskBytes(part.Size); ok {
				pp.SizeBytes = n
			}
			if pp.Size == "" {
				pp.Size = "rest"
				pp.SizeBytes = p.Capacity.RootBytes
				if pp.SizeBytes == 0 {
					unknown(fmt.Sprintf("partitions[%d].size_bytes", len(p.Partitions)), "the target disk size is unknown")
				}
			}
			switch part.Role {
			case disk.RoleEFI:
				pp.Filesystem, pp.Mount = "fat32", "/boot/efi"
			case disk.RoleBoot:
				pp.Filesystem, pp.Mount = "ext4", "/boot"
			case disk.RoleRoot:
				pp.Filesystem, pp.Mount, pp.MountOptions = r.Filesystem, "/", r.RootMountOptions()
				if r.IsZFS() {
					pp.ZFSPool = disk.PoolName(r.ZFSPoolName)
				}
				if r.EncryptsRoot() {
					pp.Encryption = "luks2"
				}
			}
			p.Partitions = append(p.Partitions, pp)
		}
	}

	if r.HasVarDisk() {
		v := &planVarDisk{Disk: givenVarDisk, Device: r.VarDisk.Disk, Action: string(disk.MountPreserve), Mount: "/var"}
		if r.FormatsVarDisk() {
			v.Action, v.Filesystem = string(disk.MountFormat), "xfs"
			p.Destroys = append(p.Destroys, planDestroy{r.VarDisk.Disk, "wipe_disk"})
		} else {
			v.Filesystem = planUnknownValue
			unknown("var_disk.filesystem", "a kept /var disk is mounted with the filesystem it holds; plan does not read it")
		}
		p.VarDisk = v
	}

	// ── Scratch ──
	mount := defaultTargetMount
	if r.TargetMount != "" {
		mount = r.TargetMount
	}
	p.Scratch = planScratch{Path: "/var/fisherman-tmp", Bytes: planUnknownValue}
	if varIsSpaceConstrained() {
		p.Scratch = planScratch{Path: filepath.Join(mount, ".fisherman-scratch"), OnTarget: true, Bytes: planUnknownValue}
	}
	unknown("scratch.bytes", "the exact scratch use depends on the path the install takes (a pull, an OCI export, a redirected store); scratch.need_bytes is an upper estimate")
	if img.CompressedBytes > 0 && img.UnpackedBytes > 0 {
		p.Scratch.NeedBytes = img.CompressedBytes + img.UnpackedBytes
	} else {
		unknown("scratch.need_bytes", "the estimate needs the image's compressed and unpacked sizes (image.compressed_bytes, image.unpacked_bytes)")
	}
	if p.Capacity != nil {
		p.Capacity.ImageFit = planImageFit(p.Capacity, img, p.Scratch, p.Target.SizeBytes > 0, unknown)
	}

	// ── Steps ── The pull moves weight between steps, never a step.
	for i, s := range planSteps(r, true) {
		p.Steps = append(p.Steps, planStep{Number: i + 1, StepID: progress.StepID(s.name), Name: s.name})
	}
	return p
}

// planAlignmentBytes is what the automatic layout loses to alignment and
// the GPT: sfdisk starts the first partition at 1 MiB, aligns each one to
// 1 MiB, and keeps 33 sectors at the end of the disk for the backup GPT.
// 3 MiB covers all of it, so root_bytes is a lower bound.
const planAlignmentBytes int64 = 3 << 20

// sfdiskBytes is the byte count of an sfdisk size ("2GiB"). ok is false for
// "" (the rest of the disk) and for a size it cannot read.
func sfdiskBytes(size string) (int64, bool) {
	for _, u := range []struct {
		suffix string
		shift  uint
	}{{"KiB", 10}, {"MiB", 20}, {"GiB", 30}, {"TiB", 40}} {
		if num, found := strings.CutSuffix(size, u.suffix); found {
			n, err := strconv.ParseInt(num, 10, 64)
			if err != nil || n <= 0 || n > (1<<62)>>u.shift {
				return 0, false
			}
			return n << u.shift, true
		}
	}
	return 0, false
}

// planTargetIdentity is the identity the target disk reports (t, from the
// same probe.Target call validate makes), and its stable ID: the WWN, else
// the serial, else "unknown". Each value the disk does not report is
// "unknown" with a reason.
func planTargetIdentity(r *recipe.Recipe, t probe.Target, terr error, unknown func(field, reason string)) (*planIdentity, string) {
	id := &planIdentity{Serial: t.Serial, WWN: t.WWN, Model: t.Model, SizeBytes: t.SizeBytes, ByID: t.ByID,
		Bound: r.DiskIdentity != nil}
	if id.ByID == nil {
		id.ByID = []string{}
	}
	why := func(what string) string {
		if terr != nil {
			return "the probe could not read the target disk"
		}
		return "the disk reports no " + what + " (loop devices, most virtual disks and some USB bridges have none)"
	}
	if id.Serial == "" {
		id.Serial = planUnknownValue
		unknown("target.identity.serial", why("serial"))
	}
	if id.WWN == "" {
		id.WWN = planUnknownValue
		unknown("target.identity.wwn", why("WWN"))
	}
	if id.Model == "" {
		id.Model = planUnknownValue
		unknown("target.identity.model", why("model"))
	}
	if terr != nil {
		unknown("target.identity.size_bytes", why("size"))
	}
	if len(id.ByID) == 0 {
		unknown("target.identity.by_id", "udev made no /dev/disk/by-id link for the disk, or plan cannot read that directory")
	}
	switch {
	case t.WWN != "":
		return id, t.WWN
	case t.Serial != "":
		return id, t.Serial
	}
	unknown("target.stable_id", "the disk reports neither a WWN nor a serial; bind the install with diskIdentity.sizeBytes, and the install checks the device again before it writes")
	return id, planUnknownValue
}

// planImageFacts is plan.image for r's source, and why it is not read
// when read_from is "none". Only with readImage does plan run a command
// for it (install.InspectImageForPlan).
func planImageFacts(r *recipe.Recipe, kind string, readImage bool) (planImage, string) {
	img := planImage{ReadFrom: "none", ManifestDigest: planUnknownValue, PlatformDigest: planUnknownValue, Platform: planUnknownValue}
	switch {
	case kind == "source-reuse":
		return img, "a reused source is this host's bootc store item (source.digest); the install reports its size in its source_reuse estimate"
	case kind == "live":
		return img, "the source is the running live image, which plan does not read"
	case !readImage:
		return img, "plan reads the image only with --read-image"
	}
	f := install.InspectImageForPlan(r.Image, kind == "registry")
	if f.ReadFrom == "" {
		return img, f.Reason
	}
	img.ReadFrom, img.Layers, img.CompressedBytes, img.UnpackedBytes = f.ReadFrom, f.Layers, f.CompressedBytes, f.UnpackedBytes
	if f.ManifestDigest != "" {
		img.ManifestDigest = f.ManifestDigest
	}
	if f.PlatformDigest != "" {
		img.PlatformDigest = f.PlatformDigest
	}
	if f.Platform != "" {
		img.Platform = f.Platform
	}
	return img, ""
}

// planImageFit compares the root the automatic layout leaves with what the
// image needs. "enough": the root holds the unpacked image, and the scratch
// estimate too when scratch is on the target. "not_enough": the root is
// smaller than the image's compressed layers, a lower bound of its
// unpacked size, or than the unpacked size itself. Else "unknown". It is a
// report: neither validate nor the install refuses on it.
func planImageFit(c *planCapacity, img planImage, s planScratch, sizeKnown bool, unknown func(field, reason string)) string {
	if !sizeKnown || c.RootBytes == 0 {
		unknown("capacity.image_fit", "the target disk size is unknown")
		return planUnknownValue
	}
	floor := img.CompressedBytes
	if img.UnpackedBytes > 0 {
		floor = img.UnpackedBytes
	}
	if floor > 0 && c.RootBytes < floor {
		return "not_enough"
	}
	need := img.UnpackedBytes
	if s.OnTarget {
		need = s.NeedBytes // includes the unpacked size; 0 when unknown
	}
	if img.UnpackedBytes > 0 && need > 0 {
		if c.RootBytes >= need {
			return "enough"
		}
		unknown("capacity.image_fit", "the root holds the unpacked image but not the scratch estimate as well; the estimate is an upper bound, so it may still be enough")
		return planUnknownValue
	}
	if floor == 0 {
		unknown("capacity.image_fit", "plan has no size of the image (see image)")
		return planUnknownValue
	}
	unknown("capacity.image_fit", "the image's unpacked size is unknown (see image.unpacked_bytes), and the root is not smaller than its compressed layers")
	return planUnknownValue
}

// manualRole names what a customMounts target is.
func manualRole(target string) string {
	switch target {
	case "/":
		return disk.RoleRoot
	case "/boot":
		return disk.RoleBoot
	case "/boot/efi":
		return disk.RoleEFI
	case "swap":
		return "swap"
	case "":
		return "none"
	}
	return "data"
}
