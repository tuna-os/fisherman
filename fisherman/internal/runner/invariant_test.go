package runner_test

// Enforces the Flatpak sandbox invariant at the source level, across every
// package in this module: host subprocess calls must be wrapped by
// runner.HostArgs (or HostArgsWithEnv), and this package must be the only
// place that decides whether we are inside a sandbox.
//
// The behavioural tests in flatpak_test.go can only reach the wrappers
// themselves. A caller that skips them is invisible to those tests, and
// invisible to the disk tests too: they install fake binaries on PATH, so a
// command missing its flatpak-spawn prefix still resolves and still passes.
// The failure surfaces only inside the Flatpak frontends, on real hardware.
// These checks read the code instead, so the invariant does not depend on
// review to hold.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// flatpakInfoPath is the canonical sandbox indicator. Only this package may
// consult it; every other package must ask via runner.InFlatpak.
const flatpakInfoPath = "/.flatpak-info"

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod, so the checks cover the whole module rather than one package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// eachGoFile calls fn for every non-test .go file in the module.
func eachGoFile(t *testing.T, fn func(path string, fset *token.FileSet, file *ast.File)) {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		fn(path, fset, parsed)
		return nil
	})
	if err != nil {
		t.Fatalf("walking module: %v", err)
	}
}

// TestExecCommandLivesInRunnerOnly asserts that no production code outside
// this package constructs its own exec.Command.
//
// This supersedes the weaker check it replaces, which only rejected a literal
// program name. A literal name caught the Flatpak half of the problem: the
// wrapped form necessarily passes the variable HostArgs returned, because in
// a sandbox the program actually executed is "flatpak-spawn". It could not
// catch the other half. A hand-built command also answers to no halt gate, so
// after runner.Halt it still forks — and for the mount diagnostics in
// internal/disk/format.go that meant Halt itself provoked the mount it exists
// to prevent, because ErrHalted is the error that triggers the re-run.
//
// Callers that need exec.Cmd itself (raw Output or CombinedOutput, a detached
// Start, a custom cmd.Env) use runner.HostCommand, or runner.WrappedCommand
// when they need the effective argv first. Both apply the halt gate, and
// HostCommand applies the Flatpak wrapping as well.
func TestExecCommandLivesInRunnerOnly(t *testing.T) {
	root := moduleRoot(t)
	runnerDir := filepath.Join(root, "internal", "runner")

	eachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		if filepath.Dir(path) == runnerDir {
			return
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Command" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "exec" {
				return true
			}
			t.Errorf("%s: exec.Command is built outside internal/runner; "+
				"use runner.HostCommand (or runner.WrappedCommand for an argv "+
				"already passed through HostArgs) so the call is forwarded to "+
				"the host inside a Flatpak sandbox and refuses to start after "+
				"runner.Halt",
				fset.Position(call.Pos()))
			return true
		})
	})
}

// TestSandboxDetectionLivesInRunnerOnly asserts that /.flatpak-info is read in
// exactly one package. A second detector is a second answer to "are we
// sandboxed", free to drift from this one — and free to skip the localCommands
// allowlist that decides which tools are bundled in the sandbox and must not
// be forwarded to the host.
func TestSandboxDetectionLivesInRunnerOnly(t *testing.T) {
	root := moduleRoot(t)
	runnerDir := filepath.Join(root, "internal", "runner")

	eachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		if filepath.Dir(path) == runnerDir {
			return
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if !strings.Contains(lit.Value, flatpakInfoPath) {
				return true
			}
			t.Errorf("%s: %s is read outside internal/runner; "+
				"ask runner.InFlatpak instead of adding a second sandbox detector",
				fset.Position(lit.Pos()), flatpakInfoPath)
			return true
		})
	})
}
