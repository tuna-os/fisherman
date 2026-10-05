package validation

import (
	"fmt"

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
