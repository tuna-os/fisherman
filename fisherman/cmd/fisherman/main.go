package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
	"github.com/tuna-os/fisherman/internal/validation"
)

const (
	defaultTargetMount = "/mnt/fisherman-target"
	defaultLuksMapper  = "fisherman-root"
)

// cleanup is global so fatal() can tear everything down on any error path.
var cleanup = &post.Cleanup{}

// bindMount is disk.BindMount by default; tests replace it to avoid real mounts.
var bindMount = disk.BindMount

// bootcInstall is install.BootcInstall by default; tests replace it.
var bootcInstall = install.BootcInstall

// prepareSelinuxWorkaround is install.PrepareSelinuxWorkaround by default;
// tests replace it.
var prepareSelinuxWorkaround = install.PrepareSelinuxWorkaround

// fatal ends the install: it stops child processes, tears down every mount
// and LUKS mapping registered on cleanup, emits one {"type":"error"} event
// (noting any teardown failure) and exits with exitFailure. If a cancel
// signal is already being handled it defers to that path instead. See
// terminator in terminate.go.
func fatal(format string, args ...any) {
	term.fail(fmt.Sprintf(format, args...))
}

// fatalProblem is fatal for a failure with a stable problem code (a recipe
// rule or a preflight check): the error event carries the code and field
// that `fisherman validate --json` reports for the same problem.
func fatalProblem(prefix string, p recipe.Problem) {
	term.failProblem(prefix+": "+p.Message, p)
}

// fatalLoad reports a recipe that could not be loaded. A strict-parse
// problem (an unknown field) keeps its code. Any other failure gets the
// code `fisherman validate --json` gives it: recipe_malformed for empty
// or invalid JSON, recipe_unreadable for a recipe that cannot be read.
func fatalLoad(err error) {
	var p recipe.Problem
	if errors.As(err, &p) {
		fatalProblem("loading recipe", p)
		return
	}
	code := recipe.CodeRecipeUnreadable
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.Is(err, recipe.ErrEmptyRecipe) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &syntax) || errors.As(err, &typeErr) {
		code = recipe.CodeRecipeMalformed
	}
	msg := fmt.Sprintf("loading recipe: %v", err)
	term.failProblem(msg, recipe.Problem{Code: code, Message: msg})
}

// bootcFailureCode is the failure code of a bootcInstall error: pull_failed
// for the image pull (install.PullError, through any wrapping), else
// bootc_install_failed.
func bootcFailureCode(err error) string {
	var pull *install.PullError
	if errors.As(err, &pull) {
		return progress.FailurePullFailed
	}
	return progress.FailureBootcInstallFailed
}

// fatalCode is fatal for a runtime failure: the error event carries code,
// one of the progress.Failure* codes, so a frontend need not read the
// message to tell which operation failed.
func fatalCode(code, format string, args ...any) {
	term.failCode(fmt.Sprintf(format, args...), code)
}

// teardown releases everything the install registered: a filesystem the
// finalize step froze, the swap a manual layout activated, then the mounts,
// the LUKS mapping and the scratch directories on cleanup. Every exit path
// runs it.
func teardown() error {
	// The thaw comes first: a frozen filesystem cannot be unmounted cleanly,
	// and its LUKS mapping cannot be closed.
	thawErr := freezes.ThawAll()
	swapErr := swaps.Off()
	return errors.Join(thawErr, swapErr, cleanup.Run())
}

// geteuid is os.Geteuid by default; replaced in tests.
var geteuid = os.Geteuid

// lookPath is exec.LookPath by default; replaced in tests.
var lookPath = exec.LookPath

// isSpaceConstrained reports whether the given path is on a filesystem that is
// too small for multi-gigabyte scratch I/O. This covers:
//   - tmpfs: RAM-backed, used by some live ISOs for /var
//   - overlayfs: used by dmsquash-live (dracut) live ISOs where / and /var
//     are an overlay on top of a squashfs; the writable upper layer has only
//     a few GiB available
func isSpaceConstrained(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	const (
		tmpfsMagic   = 0x01021994
		overlayMagic = 0x794c7630
	)
	return st.Type == tmpfsMagic || st.Type == overlayMagic
}

// varIsSpaceConstrained reports whether the host's /var is too small for
// scratch I/O, so scratch goes on the target disk. The install and `plan`
// ask it; tests replace it.
var varIsSpaceConstrained = func() bool { return isSpaceConstrained("/var") }

func prepareScratchDir(activeTargetMount string, liveISO bool) (string, error) {
	scratchDir := "/var/fisherman-tmp"
	if liveISO {
		scratchDir = filepath.Join(activeTargetMount, ".fisherman-scratch")
		progress.Info("Live environment detected (/var is space-constrained) — using target disk for scratch I/O")
	}
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return "", err
	}
	if liveISO {
		// Self-bind so bootc sees a mount point, not a plain directory.
		if err := bindMount(scratchDir, scratchDir); err != nil {
			// Nothing is mounted and nothing is registered: leave no
			// directory behind on the target either (#291).
			_ = os.Remove(scratchDir)
			return "", err
		}
		// One registration for the bind mount AND the directory under it:
		// teardown releases the bind and deletes the directory while the
		// target filesystem is still mounted, before any other unmount and
		// before the LUKS close. Deleting it after them (as a post-removal)
		// only deleted an empty mountpoint on the host and left the OCI cache
		// on the installed disk (#307).
		cleanup.AddTargetScratch(scratchDir)
	}
	return scratchDir, nil
}

// expandPath prepends standard sbin directories and any tools staged alongside
// this binary to PATH.  pkexec strips the user's PATH to a minimal safe set
// that omits /usr/sbin and /sbin on many immutable distros (e.g. GnomeOS).
func expandPath() {
	current := os.Getenv("PATH")
	// Build candidate prefix: staged tools dir (sibling of this binary) first,
	// then the standard sbin locations that pkexec commonly strips.
	prefix := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if exe, err := os.Executable(); err == nil {
		toolsDir := filepath.Join(filepath.Dir(exe), "tools")
		if info, err := os.Stat(toolsDir); err == nil && info.IsDir() {
			prefix = toolsDir + ":" + prefix
		}
	}
	if current != "" {
		prefix = prefix + ":" + current
	}
	os.Setenv("PATH", prefix)
}

// checkRequiredTools verifies that every host program this recipe's install
// runs (validation.RequiredTools) is on the host before any disk is touched.
// It returns the first missing one, which names the tool and its package.
func checkRequiredTools(r *recipe.Recipe) error {
	look := validation.HostLookPath
	if !runner.InFlatpak() {
		// Natively the host's PATH is this process's (after expandPath).
		look = func(tool string) error { _, err := lookPath(tool); return err }
	}
	if missing := validation.MissingTools(r, look); len(missing) > 0 {
		return missing[0]
	}
	return nil
}

var version = "dev"

func printHelp() {
	fmt.Printf(`fisherman — bootc disk installer backend

Usage:
  fisherman <recipe.json>          run an installation from a recipe file
  fisherman -                      run an installation, reading the recipe JSON from stdin
  fisherman validate <recipe.json> validate a recipe without installing ("-" reads stdin)
  fisherman validate --json [--partial] <recipe.json|->
                                   every problem as JSON (docs/VALIDATE.md)
  fisherman plan --json -          what an install of the recipe on stdin would do, as
                                   JSON; writes nothing, pulls nothing (docs/PLAN.md)
  fisherman images [<query>]       list or search the image catalog
  fisherman scan <disk>            scan disk for Windows data available to migrate
  fisherman probe --json           print disks, TPM, RAM/CPU/UEFI, live and offline facts
  fisherman verify --json          on the installed system: compare the installation
                                   receipt with the booted image (docs/RECEIPT.md)
  fisherman diagnostics --output-dir <dir> [--recipe <f>] [--progress-log <f>] [--probe]
                                   write a local support bundle of allowlisted facts,
                                   no secrets (docs/DIAGNOSTICS.md)
  fisherman version                print version information
  fisherman help                   show this help

Options for 'images':
  --file <path>   use a specific images.json instead of auto-detecting
  --plain         plain text output (no ANSI color or tree characters)

Examples:
  fisherman /tmp/recipe.json
  fisherman - < /tmp/recipe.json
  fisherman validate /tmp/recipe.json
  echo '{"hostname":"my-pc"}' | fisherman validate --json --partial -
  fisherman plan --json - < /tmp/recipe.json
  fisherman images
  fisherman images Bluefin
  fisherman images "GNOME 50"
  fisherman images --plain yellowfin
  fisherman scan /dev/nvme0n1
  fisherman probe --json
`)
}

// looksLikeSubcommand reports whether arg is a mistyped command rather than a
// recipe path. The bare positional form (`fisherman recipe.json`) is the
// install entry point, so the fall-through to recipe.Load must stay; but a
// flag, or a bare word with no path separator and no file behind it, is a
// command this backend does not have (`fisherman install --recipe …`, #178).
// Naming it beats "loading recipe: open install: no such file or directory".
func looksLikeSubcommand(arg string) bool {
	if arg == recipe.StdinArg {
		return false // `fisherman -`: the recipe is on stdin
	}
	if strings.HasPrefix(arg, "-") {
		return true
	}
	if strings.ContainsAny(arg, `/\`) || strings.HasSuffix(arg, ".json") {
		return false
	}
	_, err := os.Stat(arg)
	return err != nil
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(exitFailure)
	}

	switch os.Args[1] {
	case "help", "--help", "-h":
		printHelp()
		return
	case "version", "--version":
		fmt.Printf("fisherman %s\n", version)
		return
	case "images":
		runImages(os.Args[2:])
		return
	case "validate":
		runValidate(os.Args[2:])
		return
	case "probe":
		os.Exit(runProbe(os.Args[2:], os.Stdout, os.Stderr))
	case "verify":
		os.Exit(runVerify(os.Args[2:], os.Stdout, os.Stderr))
	case "plan":
		os.Exit(runPlan(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "diagnostics":
		os.Exit(runDiagnostics(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "scan":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: fisherman scan <disk>\n")
			os.Exit(exitFailure)
		}
		output, err := slurper.ScanJSON(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			os.Exit(exitFailure)
		}
		fmt.Println(output)
		return
	}

	if looksLikeSubcommand(os.Args[1]) {
		fmt.Fprintf(os.Stderr, "fisherman: unknown command %q, and no such recipe file\n\n", os.Args[1])
		printHelp()
		os.Exit(exitUsage)
	}

	// Anything else is an install of the recipe at os.Args[1] ("-" for
	// stdin): the orchestrator (orchestrator.go) runs it phase by phase and
	// ends the process itself on a failure or a cancel.
	liveInstaller().run(os.Args[1])
}

// sourceIdentityLine says what the pre-flight resolved the install source
// to. It is the source only: the installed system follows targetImgref.
func sourceIdentityLine(src install.ImageSource) string {
	line := "Source image " + src.Ref
	if src.ManifestDigest != "" {
		line += ": manifest " + src.ManifestDigest
	}
	if src.PlatformDigest != "" && src.PlatformDigest != src.ManifestDigest {
		line += ", platform manifest " + src.PlatformDigest
	}
	if src.Platform != "" {
		line += " (" + src.Platform + ")"
	}
	if src.Cached {
		line += ", from local containers-storage"
	}
	return line + "; a pull can still fail after this check, and a tag can move"
}

func buildBootcInstallOptions(
	r *recipe.Recipe,
	sourceImgref, targetImgref, activeTargetMount, scratchDir string,
	imageStores []string,
	needsPull bool,
	layerCount int,
) install.Options {
	var kernelArgs []string
	if rootOpts := r.RootMountOptions(); rootOpts != "" {
		kernelArgs = append(kernelArgs, "rootflags="+rootOpts)
	}

	return install.Options{
		SourceImgref:          sourceImgref,
		TargetImgref:          targetImgref,
		SelinuxDisabled:       r.SelinuxDisabled,
		UnifiedStorage:        r.UnifiedStorage,
		ComposeFsBackend:      r.UsesComposeFs(),
		GenericImage:          r.GenericImage,
		Bootloader:            r.Bootloader,
		Target:                activeTargetMount,
		ScratchDir:            scratchDir,
		NeedsPull:             needsPull,
		LayerCount:            layerCount,
		AdditionalImageStores: imageStores,
		KernelArguments:       kernelArgs,
	}
}
