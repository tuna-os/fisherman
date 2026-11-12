package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// planBindingVersion is the version of planBasis. Bump it when the basis
// gains, loses or changes a field: a digest of the old basis then no longer
// matches, and the install refuses it (plan_stale) instead of comparing it
// with something else.
const planBindingVersion = 1

// planBinding is plan.binding: what a frontend copies into the recipe's
// planBinding to bind the install to the plan the user confirmed.
type planBinding struct {
	// Digest is "sha256:" and the hex SHA-256 of planBasis as JSON.
	Digest string `json:"digest"`
	// ReadImage: the plan was made with --read-image. The install's check
	// reads the image only when the recipe says so (planBinding.readImage).
	ReadImage bool `json:"read_image"`
}

// planBasis is the part of a plan the user confirms and the install holds
// the machine to: the target disk and what it reports about itself, the
// partition table and the action on each partition, what is destroyed, and
// the capacity verdicts. It leaves out what a frontend chose and the recipe
// already fixes (the source, the encryption, the steps), and what drifts
// without changing the verdict (the scratch free space in bytes, the
// /dev/disk/by-id links, the reasons in plan.unknown).
type planBasis struct {
	Version   int    `json:"v"`
	ReadImage bool   `json:"read_image"`
	Layout    string `json:"layout"`
	Device    string `json:"device,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Loop      bool   `json:"loop,omitempty"`
	Serial    string `json:"serial,omitempty"`
	WWN       string `json:"wwn,omitempty"`
	Model     string `json:"model,omitempty"`

	Boot       planBoot             `json:"boot"`
	Partitions []planBasisPartition `json:"partitions"`
	VarDisk    *planBasisVarDisk    `json:"var_disk,omitempty"`
	Destroys   []planDestroy        `json:"destroys"`

	DiskMinimum   string `json:"disk_minimum"`
	RootBytes     int64  `json:"root_bytes,omitempty"`
	RootNeedBytes int64  `json:"root_need_bytes,omitempty"`
	ImageFit      string `json:"image_fit"`
	ScratchPath   string `json:"scratch_path"`
	ScratchTarget bool   `json:"scratch_on_target"`
	ScratchFit    string `json:"scratch_fit"`
}

type planBasisPartition struct {
	Number     int    `json:"number,omitempty"`
	Device     string `json:"device"`
	Role       string `json:"role"`
	GPTType    string `json:"gpt_type,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Action     string `json:"action"`
	Filesystem string `json:"filesystem,omitempty"`
	Encryption string `json:"encryption,omitempty"`
	Mount      string `json:"mount,omitempty"`
}

type planBasisVarDisk struct {
	Device string `json:"device"`
	Action string `json:"action"`
}

// bindingOf is plan.binding for p, a plan made with or without
// --read-image.
func bindingOf(p *installPlan, readImage bool) planBinding {
	b := planBasis{Version: planBindingVersion, ReadImage: readImage, Layout: p.Target.Layout,
		Device: p.Target.Device, SizeBytes: p.Target.SizeBytes, Loop: p.Target.Loop,
		Boot: p.Boot, Destroys: p.Destroys, Partitions: []planBasisPartition{},
		ImageFit: planUnknownValue, DiskMinimum: planUnknownValue,
		ScratchPath: p.Scratch.Path, ScratchTarget: p.Scratch.OnTarget, ScratchFit: p.Scratch.Fit}
	if id := p.Target.Identity; id != nil {
		b.Serial, b.WWN, b.Model = id.Serial, id.WWN, id.Model
	}
	for _, pp := range p.Partitions {
		b.Partitions = append(b.Partitions, planBasisPartition{Number: pp.Number, Device: pp.Device, Role: pp.Role,
			GPTType: pp.GPTType, SizeBytes: pp.SizeBytes, Action: pp.Action, Filesystem: pp.Filesystem,
			Encryption: pp.Encryption, Mount: pp.Mount})
	}
	if v := p.VarDisk; v != nil {
		b.VarDisk = &planBasisVarDisk{Device: v.Device, Action: v.Action}
	}
	if c := p.Capacity; c != nil {
		b.DiskMinimum, b.RootBytes, b.RootNeedBytes, b.ImageFit = c.DiskMinimum, c.RootBytes, c.RootNeedBytes, c.ImageFit
	}
	raw, err := json.Marshal(b)
	if err != nil {
		panic(fmt.Sprintf("plan binding: %v", err)) // plain strings and numbers: cannot fail
	}
	return planBinding{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), ReadImage: readImage}
}

// planBindingProblems plans r again, as `fisherman plan --json` would on
// this machine now (with --read-image when planBinding.readImage), and
// reports plan_stale when the result is not the plan the recipe's
// planBinding names. installProblemsWith runs it last, once every other
// rule has passed: only a valid recipe has a plan. It reads what plan reads
// and writes nothing.
func planBindingProblems(r *recipe.Recipe) []recipe.Problem {
	want := r.PlanBinding
	if want == nil || len(recipe.CheckPlanBinding(r)) > 0 {
		return nil
	}
	c := *r // buildPlan resolves disk and varDisk.disk in place
	if r.VarDisk != nil {
		v := *r.VarDisk
		c.VarDisk = &v
	}
	if got := buildPlan(&c, want.ReadImage); got.Binding.Digest == want.Digest {
		return nil
	}
	return []recipe.Problem{{Field: "planBinding.digest", Code: recipe.CodePlanStale,
		Message: "this machine no longer matches the plan the user confirmed: the target disk, its partitions, " +
			"what the install destroys or a capacity verdict changed since `fisherman plan --json` printed planBinding.digest; " +
			"plan again and let the user confirm the new plan"}}
}
