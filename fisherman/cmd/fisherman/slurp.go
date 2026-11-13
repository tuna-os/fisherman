package main

import (
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"

	"github.com/tuna-os/wootc/slurp"
)

// The Windows data migration (`fisherman scan`, the pre-partition extract,
// the post-install inject) is the shared slurp module from wootc. It runs
// no command itself: these adapters hand it fisherman's runner, so its
// mount/cp/chown calls go through the same host-command and halt rules as
// every other step, and fisherman's progress stream.

// slurpRunner is slurp.Runner over internal/runner.
type slurpRunner struct{}

func (slurpRunner) Run(name string, args ...string) error { return runner.Run(name, args...) }

func (slurpRunner) Output(name string, args ...string) ([]byte, error) {
	return runner.Output(name, args...)
}

// slurpProgress is slurp.Progress over internal/progress.
type slurpProgress struct{}

func (slurpProgress) Substep(msg string) { progress.Substep(msg) }
func (slurpProgress) Info(msg string)    { progress.Info(msg) }

// slurper is the migration with fisherman's runner, progress and the
// module's default /run scratch and mount points.
var slurper = slurp.NewSlurper(slurpRunner{}, slurpProgress{})
