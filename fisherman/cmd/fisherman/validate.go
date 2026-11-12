package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// validateProtocolVersion is the schema version of the `validate --json`
// output. Bump it on any change a frontend could notice (a removed or renamed
// field, a changed meaning); adding a field or a new error code does not.
const validateProtocolVersion = 1

// validateResult is the `validate --json` output. Schema: docs/VALIDATE.md.
type validateResult struct {
	ProtocolVersion int              `json:"protocol_version"`
	Valid           bool             `json:"valid"`
	Errors          []recipe.Problem `json:"errors"`
}

// tpm2Usable reports whether this machine has a usable TPM 2.0 — probe's
// tpm.usable, the same answer `fisherman probe --json` gives frontends. It
// only reads /sys and /dev. Tests replace it.
var tpm2Usable = func() bool { return newProber().TPM().Usable }

// installProblems is every rule an install enforces before it touches a
// disk: the recipe rules and existence checks (recipe.Problems) plus the two
// checks that need the machine, the TPM and, for an empty image, whether this
// host booted from live media (checkImageSource). The install path and both
// forms of `validate` call it, so validate accepts exactly what the install
// accepts.
func installProblems(r *recipe.Recipe) []recipe.Problem {
	_, imageErr := checkImageSource(r)
	return installProblemsWith(r, imageErr)
}

// installProblemsWith is installProblems with the image-source answer given:
// the install has it already (verified media input counts as live media).
func installProblemsWith(r *recipe.Recipe, imageErr error) []recipe.Problem {
	ps := r.Problems()
	if r.HasTPM2() {
		ps = append(ps, r.CheckTPM(tpm2Usable())...)
	}
	if imageErr != nil {
		ps = append(ps, recipe.Problem{Field: "image", Code: recipe.CodeImageRequired, Message: imageErr.Error()})
	}
	ps = append(ps, storeProblems(r, ps)...)
	ps = append(ps, reuseProblems(r, ps)...)
	ps = append(ps, targetProblems(r)...)
	return ps
}

// hostIsDir reports whether a HOST path is a directory. validate can run in
// a Flatpak sandbox, where the host's /usr and /etc are not visible, so there
// the host is asked. Tests replace it.
var hostIsDir = func(path string) bool {
	if fi, err := os.Stat(path); err == nil {
		return fi.IsDir()
	}
	if !runner.InFlatpak() {
		return false
	}
	_, err := runner.Output("test", "-d", path)
	return err == nil
}

// storeProblems checks that every store the recipe names exists on the host.
// A store whose path already broke the syntax rule (in so far) is skipped.
// Stores fisherman discovers itself (the field is absent) come from the
// probe, which lists only stores that exist.
func storeProblems(r *recipe.Recipe, soFar []recipe.Problem) []recipe.Problem {
	var ps []recipe.Problem
	for i, store := range r.AdditionalImageStores {
		field := fmt.Sprintf("additionalImageStores[%d]", i)
		reported := false
		for _, p := range soFar {
			reported = reported || p.Field == field
		}
		if !reported && !hostIsDir(store) {
			ps = append(ps, recipe.Problem{Field: field, Code: recipe.CodeAdditionalImageStoreNotFound,
				Message: fmt.Sprintf("additionalImageStores[%d]: %s is not a directory on the host", i, store)})
		}
	}
	return ps
}

// Host facts about the install target. They come from the same Prober as
// `fisherman probe`, so the disk list a frontend shows and the verdict on the
// recipe it then sends cannot disagree. Tests replace them.
var (
	hostTarget = func(path string) (probe.Target, error) { return newProber().Target(path) }
	hostUEFI   = func() bool { return newProber().UEFI() }
)

// targetFacts asks the host about one disk path. nil means "nothing to
// judge": the path is empty or does not exist, which recipe.Problems reports.
func targetFacts(path string) *recipe.TargetFacts {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	t, err := hostTarget(path)
	if err != nil {
		return &recipe.TargetFacts{Err: err, Device: path}
	}
	f := &recipe.TargetFacts{Device: t.Device, Loop: t.Loop, SizeBytes: t.SizeBytes, MinBytes: probe.MinDiskBytes,
		Serial: t.Serial, WWN: t.WWN}
	for _, reason := range t.Reasons {
		switch reason {
		case probe.ExcludedBootDisk:
			f.BootDisk = true
		case probe.ExcludedReadOnly:
			f.ReadOnly = true
		case probe.ExcludedTooSmall:
			f.TooSmall = true
		default:
			// ExcludedNotDisk, and any reason a later probe adds: not
			// eligible, and not something a recipe may override.
			f.NotDisk = true
		}
	}
	return f
}

// targetProblems applies the target-eligibility rules (recipe.CheckTarget)
// with this machine's answers.
func targetProblems(r *recipe.Recipe) []recipe.Problem {
	var disk, varDisk *recipe.TargetFacts
	if !r.IsManualLayout() {
		disk = targetFacts(r.Disk)
	}
	if r.HasVarDisk() {
		varDisk = targetFacts(r.VarDisk.Disk)
	}
	return r.CheckTarget(disk, varDisk, hostUEFI())
}

// resolveTargets replaces `disk` and `varDisk.disk` with their resolved
// device nodes, after validation has passed. disk.PartName builds partition
// names from the path, so a /dev/disk/by-id/… symlink must not reach it.
func resolveTargets(r *recipe.Recipe) {
	if !r.IsManualLayout() && r.Disk != "" {
		r.Disk = resolveDevice(r.Disk)
	}
	if r.VarDisk != nil && r.VarDisk.Disk != "" {
		r.VarDisk.Disk = resolveDevice(r.VarDisk.Disk)
	}
}

// partialProblems runs only the rules for the fields present in raw, a
// (possibly partial) recipe object, and never reads the machine: no disk,
// partition or TPM checks. It is what a frontend calls while a page is being
// filled in, before a whole recipe exists.
func partialProblems(raw []byte) ([]recipe.Problem, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, err
	}
	var r recipe.Recipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	has := func(k string) bool { _, ok := keys[k]; return ok }

	var ps []recipe.Problem
	if has("filesystem") {
		ps = append(ps, recipe.CheckFilesystem(r.Filesystem)...)
		ps = append(ps, recipe.CheckLayoutCombos(&r)...)
		ps = append(ps, recipe.CheckExperimentalFilesystem(&r)...)
	}
	if has("unifiedStorage") {
		ps = append(ps, recipe.CheckUnifiedStorage(r.UnifiedStorage)...)
	}
	if has("sourceReuse") {
		// Static rules only; whether the host has the item is full mode.
		ps = append(ps, recipe.CheckSourceReusePartial(&r, has("image"), has("targetImgref"))...)
	}
	if has("image") || has("targetImgref") {
		ps = append(ps, recipe.CheckImageRefs(&r)...)
	}
	if has("imageType") {
		ps = append(ps, recipe.CheckImageType(r.ImageType)...)
	}
	if has("bootloader") {
		ps = append(ps, recipe.CheckBootloader(r.Bootloader)...)
	}
	if has("encryption") {
		ps = append(ps, recipe.CheckEncryption(r.Encryption)...)
	}
	if has("hostname") {
		ps = append(ps, recipe.CheckHostname(r.Hostname)...)
	}
	if has("user") {
		var user map[string]json.RawMessage
		if err := json.Unmarshal(keys["user"], &user); err == nil {
			if _, ok := user["username"]; ok {
				ps = append(ps, recipe.CheckUsername(r.User.Username)...)
			}
		}
	}
	if has("slurp") {
		ps = append(ps, recipe.CheckSlurp(r.Slurp)...)
	}
	if has("distroID") {
		ps = append(ps, recipe.CheckDistroID(r.DistroID)...)
	}
	if has("brewTap") {
		ps = append(ps, recipe.CheckBrewTap(r.BrewTap)...)
	}
	if has("customMounts") {
		for i, cm := range r.CustomMounts {
			ps = append(ps, recipe.CheckCustomMountTarget(i, cm.Target)...)
		}
		ps = append(ps, recipe.CheckCustomMountsPersistence(&r)...)
	}
	if has("varDisk") {
		// Both read other fields of the same object: a page that sends
		// varDisk with encryption or composeFsBackend gets the verdict.
		ps = append(ps, recipe.CheckVarDiskEncryption(&r)...)
		ps = append(ps, recipe.CheckVarDiskBackend(&r)...)
	}
	if has("targetMount") {
		ps = append(ps, recipe.CheckTargetMount(r.TargetMount)...)
	}
	if has("flatpakVarPath") {
		ps = append(ps, recipe.CheckFlatpakVarPath(r.FlatpakVarPath)...)
	}
	if has("zfsPoolName") {
		ps = append(ps, recipe.CheckZFSPoolName(r.ZFSPoolName)...)
	}
	if has("luksMapperName") {
		ps = append(ps, recipe.CheckLuksMapperName(r.LuksMapperName)...)
	}
	if has("additionalImageStores") {
		ps = append(ps, recipe.CheckAdditionalImageStores(r.AdditionalImageStores)...)
	}
	if has("override") {
		ps = append(ps, recipe.CheckOverride(r.Override)...)
	}
	if has("diskIdentity") {
		// Its shape only; whether the device matches is full mode.
		ps = append(ps, recipe.CheckDiskIdentity(&r)...)
	}
	if has("experimental") {
		ps = append(ps, recipe.CheckExperimental(r.Experimental)...)
	}
	if has("locale") {
		ps = append(ps, recipe.CheckLocale(r.Locale)...)
	}
	if has("keyboard") {
		ps = append(ps, recipe.CheckKeyboard(r.Keyboard)...)
	}
	if has("timezone") {
		ps = append(ps, recipe.CheckTimezone(r.Timezone)...)
	}
	return ps, nil
}

const validateUsage = `usage: fisherman validate <recipe.json | ->
       fisherman validate --json [--partial] <recipe.json | ->

Validate a recipe without running an installation. Needs no root and
touches no disk.

  <recipe.json>  print a summary, or the first problem on stderr
  --json         print every problem as one JSON object on stdout
  --partial      check only the fields present (no disk, partition or TPM
                 checks), for validating a page while it is filled in
  -              read the recipe from stdin

Exit 0 if valid, 1 if invalid, 2 on bad arguments (--json only).
Schema and error codes: docs/VALIDATE.md.
`

func runValidate(args []string) {
	for _, a := range args {
		if a == "--json" {
			os.Exit(runValidateJSON(args, os.Stdin, os.Stdout, os.Stderr))
		}
	}

	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, validateUsage) }
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	path := fs.Arg(0)
	r, err := loadRecipe(path)
	if err != nil {
		var p recipe.Problem
		if errors.As(err, &p) {
			fmt.Fprintf(os.Stderr, "%serror:%s invalid recipe: %v\n", "\033[31m", "\033[0m", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "%serror:%s loading %s: %v\n", "\033[31m", "\033[0m", path, err)
		os.Exit(1)
	}
	if ps := installProblems(r); len(ps) > 0 {
		fmt.Fprintf(os.Stderr, "%serror:%s invalid recipe: %v\n", "\033[31m", "\033[0m", ps[0])
		os.Exit(1)
	}

	// installProblems has already rejected an empty image off live media;
	// this asks again only to name the running live image.
	live, _ := checkImageSource(r)
	if path == recipe.StdinArg {
		path = "recipe on stdin"
	}
	image := r.Image
	switch {
	case r.SourceReuse != nil:
		image = "(this host's bootc store, " + r.SourceReuse.Digest + "; updates from " + r.TargetImgref + ")"
	case image == "":
		image = strings.TrimSpace("(the running live image "+live.LiveImage) + ")"
	}
	stores, _ := resolveImageStores(r)
	fmt.Printf("%s✓%s %s is valid\n", "\033[32m", "\033[0m", path)
	fmt.Printf("  disk:       %s\n", r.Disk)
	fmt.Printf("  image:      %s\n", image)
	if len(stores) > 0 {
		fmt.Printf("  offline:    %s\n", strings.Join(stores, ", "))
	}
	fmt.Printf("  filesystem: %s\n", r.Filesystem)
	fmt.Printf("  encryption: %s\n", r.Encryption.Type)
	fmt.Printf("  hostname:   %s\n", r.Hostname)
	if r.User.Username != "" {
		fmt.Printf("  user:       %s\n", r.User.Username)
	}
	if r.Bootloader != "" {
		fmt.Printf("  bootloader: %s\n", r.Bootloader)
	}
}

// runValidateJSON implements `fisherman validate --json [--partial] <path|->`
// and returns the exit code: 0 valid, 1 invalid (including a recipe that
// cannot be read or parsed, which is reported as a problem), 2 usage.
func runValidateJSON(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	partial := false
	var src []string
	for _, a := range args {
		switch a {
		case "--json":
		case "--partial":
			partial = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, validateUsage)
			return 0
		case "-":
			src = append(src, a)
		default:
			if len(a) > 1 && a[0] == '-' {
				fmt.Fprintf(stderr, "validate: unknown argument %q\n\n%s", a, validateUsage)
				return 2
			}
			src = append(src, a)
		}
	}
	if len(src) != 1 {
		fmt.Fprintf(stderr, "validate: --json needs exactly one recipe path, or - for stdin\n\n%s", validateUsage)
		return 2
	}

	var raw []byte
	var err error
	if src[0] == "-" {
		// Minimal stdin support, local to validate: the recipe may carry a
		// LUKS passphrase, which must not go into a temp file or onto argv.
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(src[0])
	}

	var ps []recipe.Problem
	switch {
	case err != nil:
		ps = []recipe.Problem{{Code: recipe.CodeRecipeUnreadable, Message: fmt.Sprintf("reading recipe: %v", err)}}
	case partial:
		ps, err = partialProblems(raw)
		if err != nil && ps == nil {
			ps = []recipe.Problem{malformedProblem(err)}
		}
	default:
		_, ps = fullProblems(raw)
	}

	res := validateResult{ProtocolVersion: validateProtocolVersion, Valid: len(ps) == 0, Errors: ps}
	if res.Errors == nil {
		res.Errors = []recipe.Problem{}
	}
	if err := writeJSON(stdout, res); err != nil {
		fmt.Fprintf(stderr, "validate: encoding output: %v\n", err)
		return 1
	}
	if !res.Valid {
		return 1
	}
	return 0
}

// fullProblems is `validate --json` for a whole recipe: the unknown keys
// first (the install refuses them when it loads the recipe, recipe.Parse,
// before any other rule runs), then installProblems. It returns the parsed
// recipe, or nil when raw is not a recipe object. `fisherman plan` calls it
// too, so plan refuses exactly what validate refuses.
func fullProblems(raw []byte) (*recipe.Recipe, []recipe.Problem) {
	var r recipe.Recipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, []recipe.Problem{malformedProblem(err)}
	}
	return &r, append(recipe.UnknownFields(raw), installProblems(&r)...)
}

func malformedProblem(err error) recipe.Problem {
	return recipe.Problem{Code: recipe.CodeRecipeMalformed, Message: fmt.Sprintf("parsing recipe: %v", err)}
}

// writeJSON writes v as indented JSON with no HTML escaping, the form of
// every --json output.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
