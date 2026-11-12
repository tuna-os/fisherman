package validation

import (
	"fmt"
	"os/exec"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Validator performs a single validation check and returns an error if validation fails.
// Validators are independent and can be tested in isolation.
type Validator interface {
	// Name returns the human-readable name of this validator (e.g., "Recipe Syntax").
	Name() string
	// Validate performs the check. Returns nil if validation passes, error if it fails.
	Validate() error
}

// Chain orchestrates multiple validators in sequence, stopping at first failure.
// Each validator is independent and can be tested separately.
type Chain struct {
	validators []Validator
}

// NewChain creates an empty validation chain.
func NewChain() *Chain {
	return &Chain{
		validators: []Validator{},
	}
}

// Add appends a validator to the chain.
func (c *Chain) Add(v Validator) *Chain {
	c.validators = append(c.validators, v)
	return c
}

// Run executes all validators in sequence, stopping at first failure.
// Returns the first error encountered, or nil if all pass.
func (c *Chain) Run() error {
	for _, v := range c.validators {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("%s: %w", v.Name(), err)
		}
	}
	return nil
}

// RecipeValidator validates a Recipe object. Implements Validator.
type RecipeValidator struct {
	recipe *recipe.Recipe
}

// NewRecipeValidator creates a validator for recipe syntax and contents.
func NewRecipeValidator(r *recipe.Recipe) *RecipeValidator {
	return &RecipeValidator{recipe: r}
}

// Name implements Validator.
func (v *RecipeValidator) Name() string {
	return "Recipe"
}

// Validate implements Validator.
func (v *RecipeValidator) Validate() error {
	if v.recipe == nil {
		return fmt.Errorf("recipe is nil")
	}
	return v.recipe.Validate()
}

// ToolValidator checks that required tools are available on the system.
type ToolValidator struct {
	tools []string
}

// NewToolValidator creates a validator for required CLI tools.
func NewToolValidator(requiredTools []string) *ToolValidator {
	return &ToolValidator{tools: requiredTools}
}

// Name implements Validator.
func (v *ToolValidator) Name() string {
	return "Required Tools"
}

// Validate implements Validator.
func (v *ToolValidator) Validate() error {
	var missing []string
	for _, tool := range v.tools {
		if err := checkToolAvailable(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing: %v", missing)
	}
	return nil
}

// ImageCacheValidator checks that the required OS image is cached locally.
type ImageCacheValidator struct {
	imagePath string
}

// NewImageCacheValidator creates a validator for image cache availability.
func NewImageCacheValidator(path string) *ImageCacheValidator {
	return &ImageCacheValidator{imagePath: path}
}

// Name implements Validator.
func (v *ImageCacheValidator) Name() string {
	return "Image Cache"
}

// Validate implements Validator.
func (v *ImageCacheValidator) Validate() error {
	return checkImageCached(v.imagePath)
}

// ImageSourceValidator checks that image source is valid (set or on live media).
type ImageSourceValidator struct {
	recipe *recipe.Recipe
	live   probe.Live
}

// NewImageSourceValidator creates a validator for image source availability.
func NewImageSourceValidator(r *recipe.Recipe, live probe.Live) *ImageSourceValidator {
	return &ImageSourceValidator{recipe: r, live: live}
}

// Name implements Validator.
func (v *ImageSourceValidator) Name() string {
	return "Image Source"
}

// Validate implements Validator.
func (v *ImageSourceValidator) Validate() error {
	if v.recipe.Image != "" {
		return nil
	}
	return v.recipe.ValidateImage(v.live.IsLive)
}

// RecipeProblemsValidator checks all recipe validation rules including TPM checks.
type RecipeProblemsValidator struct {
	recipe     *recipe.Recipe
	tpm2Usable bool
}

// NewRecipeProblemsValidator creates a validator that checks all recipe rules.
func NewRecipeProblemsValidator(r *recipe.Recipe, tpm2Usable bool) *RecipeProblemsValidator {
	return &RecipeProblemsValidator{recipe: r, tpm2Usable: tpm2Usable}
}

// Name implements Validator.
func (v *RecipeProblemsValidator) Name() string {
	return "Recipe Validation"
}

// Validate implements Validator.
func (v *RecipeProblemsValidator) Validate() error {
	if v.recipe == nil {
		return fmt.Errorf("recipe is nil")
	}
	problems := v.recipe.Problems()
	if recipe.NeedsTPM2(v.recipe.Encryption.Type) {
		problems = append(problems, v.recipe.CheckTPM(v.tpm2Usable)...)
	}
	if len(problems) > 0 {
		return fmt.Errorf(problems[0].Message)
	}
	return nil
}

// RecipeDependentToolValidator checks for tools required by a specific recipe.
type RecipeDependentToolValidator struct {
	recipe *recipe.Recipe
	// Look finds a tool on the host; nil means HostLookPath.
	Look func(tool string) error
}

// NewRecipeDependentToolValidator creates a validator for recipe-specific tools.
func NewRecipeDependentToolValidator(r *recipe.Recipe) *RecipeDependentToolValidator {
	return &RecipeDependentToolValidator{recipe: r}
}

// Name implements Validator.
func (v *RecipeDependentToolValidator) Name() string {
	return "Required Tools"
}

// Validate implements Validator.
func (v *RecipeDependentToolValidator) Validate() error {
	if v.recipe == nil {
		return fmt.Errorf("recipe is nil")
	}
	look := v.Look
	if look == nil {
		look = HostLookPath
	}
	if missing := MissingTools(v.recipe, look); len(missing) > 0 {
		return missing[0]
	}
	return nil
}

// ToolRequirement is one host program an install of a given recipe runs,
// and the package that usually ships it.
type ToolRequirement struct {
	Tool    string
	Package string
}

// Error makes a missing ToolRequirement usable as an error.
func (t ToolRequirement) Error() string {
	return fmt.Sprintf("%q not found on the host; install the %q package", t.Tool, t.Package)
}

// RequiredTools lists the host programs the install of r runs before bootc
// takes over, in the order the install reaches them. It is the one list:
// a tool missing from it is found only when its step runs, after earlier
// steps have already partitioned or formatted.
//
// The auto layout partitions the disk (sfdisk) and formats an ESP, an ext4
// /boot (GRUB layouts) and the root. A manual layout (customMounts) does
// not partition, and formats exactly what its entries say: the list comes
// from customMounts[].fstype, not from `filesystem`, which is empty there.
func RequiredTools(r *recipe.Recipe) []ToolRequirement {
	var tools []ToolRequirement
	seen := map[string]bool{}
	need := func(tool, pkg string) {
		if !seen[tool] {
			seen[tool] = true
			tools = append(tools, ToolRequirement{tool, pkg})
		}
	}

	if len(r.CustomMounts) > 0 {
		for _, cm := range r.CustomMounts {
			if cm.Target == "swap" {
				// disk.ApplyCustomLayout formats and activates swap only
				// for target "swap" with fstype "swap"; teardown turns it
				// off again.
				if cm.Fstype == "swap" {
					need("mkswap", "util-linux")
					need("swapon", "util-linux")
					need("swapoff", "util-linux")
				}
				continue
			}
			if cm.Target == "" {
				continue // not mounted, not formatted
			}
			switch cm.Fstype {
			case "fat32":
				need("mkfs.fat", "dosfstools")
			case "ext4":
				need("mkfs.ext4", "e2fsprogs")
			case "ext3":
				need("mkfs.ext3", "e2fsprogs")
			case "xfs":
				need("mkfs.xfs", "xfsprogs")
			case "btrfs":
				need("mkfs.btrfs", "btrfs-progs")
			}
		}
	} else {
		need("sfdisk", "util-linux")
		need("mkfs.fat", "dosfstools")
		need("mkfs.ext4", "e2fsprogs")
		switch r.Filesystem {
		case "xfs":
			need("mkfs.xfs", "xfsprogs")
		case "btrfs":
			need("mkfs.btrfs", "btrfs-progs")
		case "zfs":
			need("zpool", "zfsutils-linux")
			need("zfs", "zfsutils-linux")
		}
		if recipe.IsEncrypted(r.Encryption.Type) {
			need("cryptsetup", "cryptsetup")
		}
		// No systemd-cryptenroll here: a tpm2-* type enrols on the first
		// boot of the installed system, with the TARGET image's tool. The
		// install looks for it there once the image is on the disk
		// (luks.StageFirstBootEnrollment); the host needs cryptsetup only.
	}
	// A /var disk that is not kept is wiped and formatted XFS
	// (disk.PrepareVarDisk).
	if r.VarDisk != nil && !r.VarDisk.KeepExisting {
		need("wipefs", "util-linux")
		need("mkfs.xfs", "xfsprogs")
	}
	// blkid gives the UUID and the filesystem type of every fstab line the
	// install writes (a /var disk, a data or swap entry of a manual
	// layout), and checks a device that is kept as it is.
	needBlkid := r.VarDisk != nil
	for _, cm := range r.CustomMounts {
		switch recipe.ClassifyCustomMount(cm.Target) {
		case recipe.MountIgnored:
		case recipe.MountData:
			needBlkid = true
		case recipe.MountSwap:
			needBlkid = needBlkid || cm.Fstype == "swap"
		case recipe.MountBootStack:
			needBlkid = needBlkid || cm.Fstype == "" || cm.Fstype == "unformatted"
		}
	}
	if needBlkid {
		need("blkid", "util-linux")
	}
	need("skopeo", "skopeo")
	need("podman", "podman")
	// The source reuse (#384) runs the host's bootc for the copy and
	// ostree to check the image's ref. The install from the copy runs the
	// copy's own bootc, in its container (podman, above).
	if r.SourceReuse != nil {
		need("bootc", "bootc")
		need("ostree", "ostree")
	}
	return tools
}

// MissingTools returns the RequiredTools of r that look does not find.
func MissingTools(r *recipe.Recipe, look func(tool string) error) []ToolRequirement {
	var missing []ToolRequirement
	for _, t := range RequiredTools(r) {
		if err := look(t.Tool); err != nil {
			missing = append(missing, t)
		}
	}
	return missing
}

// HostLookPath finds tool where the install will run it: on the host.
// Every install command goes through internal/runner, which wraps it in
// `flatpak-spawn --host` inside a Flatpak sandbox, so a lookup in the
// sandbox's own PATH would test the wrong filesystem: it passes for a tool
// the runtime ships and the host lacks, and fails for the reverse.
func HostLookPath(tool string) error {
	if !inFlatpak() {
		_, err := exec.LookPath(tool)
		return err
	}
	_, err := hostOutput("sh", "-c", `command -v -- "$1"`, "sh", tool)
	return err
}

// Seams for tests.
var (
	inFlatpak  = runner.InFlatpak
	hostOutput = runner.Output
)
