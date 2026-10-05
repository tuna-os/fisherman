package workflows

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/post"
)

// Fatal logs an error and exits the program after running cleanup.
// This is the centralized error path for all workflows.
func Fatal(cleanup *post.Cleanup, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	progress.Error(msg)
	cleanup.Run()
	fmt.Fprintf(os.Stderr, "fisherman: fatal: %s\n", msg)
	os.Exit(1)
}

// LookPath is exec.LookPath by default; replaced in tests.
// This is used by subcommands that need tool discovery.
var LookPath = exec.LookPath

// ExpandPath adds standard sbin locations to PATH for tool discovery.
// This covers all sbin paths and any tools staged alongside this binary.
// pkexec strips the calling user's PATH to a minimal safe set which often
// omits /usr/sbin and /sbin on some immutable distros.
func ExpandPath() {
	currentPath := os.Getenv("PATH")
	expandedPath := currentPath + ":/usr/local/sbin:/usr/sbin:/sbin"
	os.Setenv("PATH", expandedPath)
}
