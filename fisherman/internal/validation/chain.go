package validation

import (
	"fmt"
	"os/exec"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
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
	tools := v.requiredToolsForRecipe(v.recipe)
	var missing []string
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, fmt.Sprintf("%q not found in PATH", tool))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s", missing[0])
	}
	return nil
}

// requiredToolsForRecipe returns the list of tools needed for a specific recipe.
func (v *RecipeDependentToolValidator) requiredToolsForRecipe(r *recipe.Recipe) []string {
	type requirement struct {
		tool string
		when bool
	}
	reqs := []requirement{
		{"sfdisk", true},
		{"mkfs.fat", true},
		{"mkfs.ext4", true},
		{"mkfs.xfs", r.Filesystem == "xfs"},
		{"mkfs.btrfs", r.Filesystem == "btrfs"},
		{"zpool", r.Filesystem == "zfs"},
		{"zfs", r.Filesystem == "zfs"},
		{"cryptsetup", r.Encryption.Type != "" && r.Encryption.Type != "none"},
		{"systemd-cryptenroll", r.Encryption.Type == "tpm2-luks" || r.Encryption.Type == "tpm2-luks-passphrase"},
		{"skopeo", true},
		{"podman", true},
	}

	var tools []string
	for _, req := range reqs {
		if req.when {
			tools = append(tools, req.tool)
		}
	}
	return tools
}
