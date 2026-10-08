// Package workflow provides the orchestration abstraction for fisherman installation.
//
// It decouples the workflow sequence (which steps, in what order, with what
// dependencies) from individual step implementations. This enables:
//   - Testing steps independently without full recipe execution
//   - Reordering or conditionally skipping steps
//   - Distinguishing transient vs permanent errors
//   - Clear separation between orchestration and domain logic
package workflow

import (
	"fmt"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// ErrorKind distinguishes transient (retryable) from permanent (fatal) errors.
type ErrorKind int

const (
	// ErrorTransient: network timeout, temporary lock, retry-able condition.
	ErrorTransient ErrorKind = iota
	// ErrorPermanent: validation failure, incompatible config, unrecoverable state.
	ErrorPermanent
)

// StepError wraps a step execution error with kind and context.
type StepError struct {
	Step   string
	Err    error
	Kind   ErrorKind
	Action string // suggested recovery (e.g., "retry", "abort", "skip")
}

// Error implements the error interface.
func (e *StepError) Error() string {
	return fmt.Sprintf("[%s] %v (action: %s)", e.Step, e.Err, e.Action)
}

// StepFunc defines the contract for a workflow step.
// It receives recipe config, execution context, and returns error (if any).
type StepFunc func(ctx *ExecutionContext) error

// Step defines a workflow step with its metadata.
type Step struct {
	Name        string    // human-readable step name (e.g., "Partition disk")
	Description string    // longer description for logging
	Func        StepFunc  // the step's implementation
	Weight      int       // progress bar weight (0-100, sum to 100)
	Required    bool      // if false, failures are non-fatal
	SkipIf      func() bool // optional predicate to skip this step
}

// ExecutionContext holds recipe config, runtime state, and cleanup management.
type ExecutionContext struct {
	Recipe *recipe.Recipe

	// Global paths (may be overridden by recipe)
	TargetMount   string
	LuksMapper    string
	ScratchDir    string
	ActiveEFIPart string
	ActiveRootPart string

	// Runtime state computed during execution
	StateMap map[string]interface{} // step-computed state for later steps

	// Cleanup manager (shared across all steps)
	Cleanup *post.Cleanup

	// Reporter for progress updates
	Reporter Reporter
}

// Reporter abstracts progress reporting so steps can be tested independently.
// In production, this delegates to progress.Info/Step/Error; in tests, it can be mocked.
type Reporter interface {
	Info(msg string)
	Step(current, total int, description string, cumulativePct, weightPct int)
	Error(msg string)
}

// Executor orchestrates workflow execution.
type Executor struct {
	steps    []Step
	context  *ExecutionContext
	reporter Reporter
}

// NewExecutor creates a new workflow executor with the given recipe and context.
func NewExecutor(r *recipe.Recipe, reporter Reporter) *Executor {
	return &Executor{
		context: &ExecutionContext{
			Recipe:   r,
			StateMap: make(map[string]interface{}),
			Cleanup:  &post.Cleanup{},
			Reporter: reporter,
		},
		reporter: reporter,
	}
}

// AddStep appends a step to the workflow.
func (e *Executor) AddStep(s Step) {
	e.steps = append(e.steps, s)
}

// AddSteps appends multiple steps to the workflow.
func (e *Executor) AddSteps(steps ...Step) {
	e.steps = append(e.steps, steps...)
}

// Execute runs the workflow steps in order.
// On permanent error, cleanup is run and the error is returned.
// On transient error (if non-fatal), the step is skipped and execution continues.
func (e *Executor) Execute() error {
	defer func() {
		// Always run cleanup, even on error
		e.context.Cleanup.Run()
	}()

	totalSteps := e.countSteps()
	currentStep := 1

	for _, step := range e.steps {
		// Skip if predicate returns true
		if step.SkipIf != nil && step.SkipIf() {
			continue
		}

		// Report progress
		if e.reporter != nil {
			e.reporter.Step(currentStep, totalSteps, step.Description,
				int(float64(currentStep-1)*100.0/float64(totalSteps)),
				int(float64(step.Weight)*100.0/float64(totalSteps)))
		}

		// Execute step
		err := step.Func(e.context)
		if err != nil {
			// Determine error kind (default to permanent for safety)
			kind := ErrorPermanent
			action := "abort"

			// If non-fatal and transient, log and continue
			if !step.Required && kind == ErrorTransient {
				if e.reporter != nil {
					e.reporter.Error(fmt.Sprintf("[%s] transient error (skipping): %v", step.Name, err))
				}
				currentStep++
				continue
			}

			// Fatal error: log, cleanup, return
			if e.reporter != nil {
				e.reporter.Error(fmt.Sprintf("[%s] fatal error (%s): %v", step.Name, action, err))
			}

			return &StepError{
				Step:   step.Name,
				Err:    err,
				Kind:   kind,
				Action: action,
			}
		}

		currentStep++
	}

	return nil
}

// countSteps counts steps that are not skipped.
func (e *Executor) countSteps() int {
	count := 0
	for _, step := range e.steps {
		if step.SkipIf == nil || !step.SkipIf() {
			count++
		}
	}
	return count
}

// Context returns the execution context for inspection (tests, debugging).
func (e *Executor) Context() *ExecutionContext {
	return e.context
}
