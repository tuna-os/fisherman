package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tuna-os/fisherman/internal/recipe"
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
// disk: the recipe rules and existence checks (recipe.Problems) plus the TPM
// check, which needs the machine. The install path and both forms of
// `validate` call it, so validate accepts exactly what the install accepts.
func installProblems(r *recipe.Recipe) []recipe.Problem {
	ps := r.Problems()
	if recipe.NeedsTPM2(r.Encryption.Type) {
		ps = append(ps, r.CheckTPM(tpm2Usable())...)
	}
	return ps
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
	return ps, nil
}

const validateUsage = `usage: fisherman validate <recipe.json>
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
	r, err := recipe.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%serror:%s loading %s: %v\n", "\033[31m", "\033[0m", path, err)
		os.Exit(1)
	}
	if ps := installProblems(r); len(ps) > 0 {
		fmt.Fprintf(os.Stderr, "%serror:%s invalid recipe: %v\n", "\033[31m", "\033[0m", ps[0])
		os.Exit(1)
	}

	fmt.Printf("%s✓%s %s is valid\n", "\033[32m", "\033[0m", path)
	fmt.Printf("  disk:       %s\n", r.Disk)
	fmt.Printf("  image:      %s\n", r.Image)
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
	default:
		var r recipe.Recipe
		if err = json.Unmarshal(raw, &r); err == nil {
			ps = installProblems(&r)
		}
	}
	if err != nil && ps == nil {
		ps = []recipe.Problem{{Code: recipe.CodeRecipeMalformed, Message: fmt.Sprintf("parsing recipe: %v", err)}}
	}

	res := validateResult{ProtocolVersion: validateProtocolVersion, Valid: len(ps) == 0, Errors: ps}
	if res.Errors == nil {
		res.Errors = []recipe.Problem{}
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		fmt.Fprintf(stderr, "validate: encoding output: %v\n", err)
		return 1
	}
	if !res.Valid {
		return 1
	}
	return 0
}
