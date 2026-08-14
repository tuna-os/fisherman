package install

import "github.com/tuna-os/fisherman/internal/runner"

// Exec is the command executor used by the pure command-construction
// subprocess wrappers in this package (SELinux bypass shim compilation,
// loop-device management, skopeo inspect). Replaced in tests to avoid
// touching real devices/binaries.
var Exec runner.Executor = runner.DefaultExecutor
