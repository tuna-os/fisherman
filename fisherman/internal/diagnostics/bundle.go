// Package diagnostics builds the local support bundle of
// `fisherman diagnostics` (tuna-os/fisherman#375): a JSON document of
// allowlisted facts about a failed or finished install, which the user
// reviews and then shares by hand. Every value in it comes through an
// allowlist; nothing is copied whole from an input. docs/DIAGNOSTICS.md
// is the contract.
package diagnostics

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// FormatVersion is the bundle's version. Fields are only ever added; a new
// version means a field changed meaning.
const FormatVersion = 1

// Format names the document.
const Format = "fisherman-diagnostics"

// MaxBundleBytes bounds the bundle. Build drops the oldest timeline events
// until the bundle fits.
const MaxBundleBytes = 4 << 20

// Section statuses.
const (
	// StatusIncluded: the section holds its allowlisted facts.
	StatusIncluded = "included"
	// StatusNotRequested: the caller gave no input for the section.
	StatusNotRequested = "not_requested"
	// StatusUnreadable: the input could not be read or decoded; nothing of
	// it is in the bundle.
	StatusUnreadable = "unreadable"
	// StatusTimedOut: the live probe did not answer in time.
	StatusTimedOut = "timed_out"
)

// Inputs are what the caller hands the export. A nil input is a section
// the caller did not ask for.
type Inputs struct {
	// Versions holds the fisherman and Go versions; Build adds the host's.
	Versions Versions
	// Recipe is the recipe JSON. It may be invalid; it is never copied.
	Recipe []byte
	// RecipeErr is set when the recipe could not be read.
	RecipeErr error
	// Progress is the progress log (fisherman's stdout, NDJSON), or its
	// end when ProgressTruncated.
	Progress          []byte
	ProgressTruncated bool
	ProgressErr       error
	// Probe is a probe report; ProbeStatus says why there is none.
	Probe       *probe.Result
	ProbeStatus string
	// Root is the filesystem root host facts are read from ("/").
	Root string
	// IncludeIdentifiers keeps host, user and device identifiers.
	IncludeIdentifiers bool
	// Now is the creation time.
	Now time.Time
}

// Bundle is the document.
type Bundle struct {
	Format   string           `json:"format"`
	Version  int              `json:"version"`
	Created  string           `json:"created"`
	Manifest Manifest         `json:"manifest"`
	Versions Versions         `json:"versions"`
	Probe    *ProbeSummary    `json:"probe,omitempty"`
	Recipe   *RecipeSummary   `json:"recipe,omitempty"`
	Progress *ProgressSummary `json:"progress,omitempty"`
	Mounts   Mounts           `json:"mounts"`
}

// Manifest says what the bundle holds and what it leaves out, so a person
// can review it before sharing it.
type Manifest struct {
	// Sections maps each section to its status.
	Sections map[string]string `json:"sections"`
	// IdentifiersMasked is true unless --include-identifiers: host names,
	// user names and device paths are tokens such as "device-1".
	IdentifiersMasked bool `json:"identifiers_masked"`
	// Redactions is how many secret values or shapes were replaced in
	// messages.
	Redactions int `json:"redactions"`
	// Omitted lists what the bundle never holds.
	Omitted []string `json:"omitted"`
	// Limits states the bounds the export applied.
	Limits map[string]int `json:"limits"`
	// Notes are the residual privacy limits a reviewer must know.
	Notes []string `json:"notes"`
}

// AlwaysOmitted is Manifest.Omitted. docs/DIAGNOSTICS.md lists the same
// items.
var AlwaysOmitted = []string{
	"encryption passphrase",
	"recovery key",
	"user password and password hash",
	"raw recipe, unknown recipe fields and their values",
	"info and substep message text, command arguments, stdout and stderr",
	"environment variables",
	"journals and configuration directories",
	"registry credentials and image references with credentials",
	"network, Wi-Fi and Bluetooth secrets",
	"SSH and WireGuard private keys",
	"Tailscale keys, URLs and state",
	"offline store paths and image names",
	"names of other device-mapper devices and mounts outside the target and scratch",
}

// Notes is Manifest.Notes.
var Notes = []string{
	"Error and complete messages are kept after redaction. Redaction removes the recipe's secrets, the log's recovery keys and known secret shapes; it cannot know a secret that matches none of them.",
	"The probe's machine model (DMI vendor and product), GPU ids, disk sizes and image references stay in the bundle; they can narrow down a machine.",
	"Masked identifiers are consistent inside one bundle, so two mentions of one device can be matched.",
}

// Build returns the bundle for in. It never fails: an input it cannot read
// is a section with a status and no content.
func Build(in Inputs) Bundle {
	rd := NewRedactor(in.IncludeIdentifiers)
	// Secrets first, so every message of every section is redacted.
	if in.Recipe != nil {
		for _, s := range recipeSecrets(in.Recipe) {
			rd.AddSecret(s)
		}
	}
	if in.Progress != nil {
		for _, s := range progressSecrets(in.Progress) {
			rd.AddSecret(s)
		}
	}
	root := in.Root
	if root == "" {
		root = "/"
	}
	b := Bundle{
		Format:  Format,
		Version: FormatVersion,
		Created: in.Now.UTC().Format(time.RFC3339),
		Manifest: Manifest{
			Sections:          map[string]string{"versions": StatusIncluded, "mounts": StatusIncluded},
			IdentifiersMasked: !in.IncludeIdentifiers,
			Omitted:           AlwaysOmitted,
			Notes:             Notes,
			Limits: map[string]int{
				"recipe_bytes":   recipe.MaxRecipeBytes,
				"progress_bytes": MaxProgressBytes,
				"timeline":       MaxTimeline,
				"bundle_bytes":   MaxBundleBytes,
				"mounts":         MaxMounts,
			},
		},
		Versions: hostVersions(root, Versions{
			Fisherman: token(in.Versions.Fisherman, wordRe), Go: token(in.Versions.Go, wordRe),
			OS: token(in.Versions.OS, wordRe), Arch: token(in.Versions.Arch, wordRe),
		}),
	}

	target, mapper := DefaultTargetMount, DefaultLuksMapper
	switch {
	case in.RecipeErr != nil:
		b.Manifest.Sections["recipe"] = StatusUnreadable
	case in.Recipe == nil:
		b.Manifest.Sections["recipe"] = StatusNotRequested
	default:
		if s, ok := summarizeRecipe(in.Recipe, rd); ok {
			b.Recipe = &s
			b.Manifest.Sections["recipe"] = StatusIncluded
			var r recipe.Recipe
			_ = json.Unmarshal(in.Recipe, &r)
			if r.TargetMount != "" && len(recipe.CheckTargetMount(r.TargetMount)) == 0 {
				target = filepath.Clean(r.TargetMount)
			}
			if r.LuksMapperName != "" && len(recipe.CheckLuksMapperName(r.LuksMapperName)) == 0 {
				mapper = r.LuksMapperName
			}
		} else {
			b.Manifest.Sections["recipe"] = StatusUnreadable
		}
	}

	switch {
	case in.Probe != nil:
		s := summarizeProbe(in.Probe, rd)
		b.Probe = &s
		b.Manifest.Sections["probe"] = StatusIncluded
	case in.ProbeStatus != "":
		b.Manifest.Sections["probe"] = in.ProbeStatus
	default:
		b.Manifest.Sections["probe"] = StatusNotRequested
	}

	switch {
	case in.ProgressErr != nil:
		b.Manifest.Sections["progress"] = StatusUnreadable
	case in.Progress == nil:
		b.Manifest.Sections["progress"] = StatusNotRequested
	default:
		s := summarizeProgress(in.Progress, in.ProgressTruncated, rd)
		b.Progress = &s
		b.Manifest.Sections["progress"] = StatusIncluded
	}

	b.Mounts = hostMounts(root, target, mapper, rd)
	b.Manifest.Redactions = rd.Redactions()
	return b
}

// Marshal encodes b, indented for review, and drops the oldest timeline
// events until it fits MaxBundleBytes.
func Marshal(b Bundle) ([]byte, error) {
	for {
		out, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			return nil, err
		}
		out = append(out, '\n')
		if len(out) <= MaxBundleBytes {
			return out, nil
		}
		if b.Progress == nil || len(b.Progress.Timeline) == 0 {
			return nil, fmt.Errorf("bundle is %d bytes, over the %d-byte bound", len(out), MaxBundleBytes)
		}
		drop := (len(b.Progress.Timeline) + 1) / 2
		p := *b.Progress
		p.Timeline = p.Timeline[drop:]
		p.Dropped += drop
		b.Progress = &p
	}
}

// FileName returns the bundle file name for a creation time and a random
// suffix: fisherman-diagnostics-20261112T080000Z-0123abcd.json.
func FileName(now time.Time, suffix string) string {
	return fmt.Sprintf("%s-%s-%s.json", Format, now.UTC().Format("20060102T150405Z"), suffix)
}

// Seams for tests.
var (
	randSuffix = func() (string, error) {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		return hex.EncodeToString(b[:]), nil
	}
	writeAll = func(f *os.File, data []byte) error {
		if _, err := f.Write(data); err != nil {
			return err
		}
		return f.Sync()
	}
)

// ErrOutputDir is returned for an output directory that is not a real
// directory (a symlink, a file, absent).
var ErrOutputDir = errors.New("output directory must be an existing directory, not a symlink")

// Write writes data to a new file in dir and returns its path. The file
// is created with mode 0600 and O_EXCL|O_NOFOLLOW, so it never replaces
// or follows an existing file or symlink; a name that is taken gets a new
// random suffix. On any write error the partial file is removed.
func Write(dir string, now time.Time, data []byte) (string, error) {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return "", ErrOutputDir
	}
	for attempt := 0; attempt < 5; attempt++ {
		suffix, err := randSuffix()
		if err != nil {
			return "", err
		}
		path := filepath.Join(dir, FileName(now, suffix))
		f, err := openExclusive(path)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := writeAll(f, data); err != nil {
			f.Close()
			os.Remove(path)
			return "", fmt.Errorf("writing %s: %w (no partial bundle is left)", path, err)
		}
		if err := f.Close(); err != nil {
			os.Remove(path)
			return "", fmt.Errorf("writing %s: %w (no partial bundle is left)", path, err)
		}
		return path, nil
	}
	return "", fmt.Errorf("no free bundle name in %s after 5 attempts", dir)
}

// openExclusive creates path for writing, mode 0600. It fails with
// os.ErrExist when anything, a dangling symlink included, has the name.
func openExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
}
