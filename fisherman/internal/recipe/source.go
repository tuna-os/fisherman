package recipe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// StdinArg is the recipe argument that means "read the recipe from stdin":
// `fisherman -` and `fisherman validate -`. A frontend pipes the JSON in, so
// it never writes a recipe (which can hold a LUKS passphrase and a user
// password) to a temporary file, and never has to find a path that both its
// sandbox and root on the host can see. pkexec, sudo and
// `flatpak-spawn --host` all pass stdin through.
const StdinArg = "-"

// MaxRecipeBytes bounds a recipe read from stdin. Real recipes are a few
// KiB; the bound only stops a stuck or hostile writer from growing memory
// without limit.
const MaxRecipeBytes = 1 << 20

// ErrEmptyRecipe is returned for a recipe with no content (an empty file, or
// stdin closed before anything was written).
var ErrEmptyRecipe = errors.New("recipe is empty")

// Parse decodes a recipe from JSON. Empty or whitespace-only input is
// ErrEmptyRecipe rather than an opaque "unexpected end of JSON input".
func Parse(data []byte) (*Recipe, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ErrEmptyRecipe
	}
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing recipe: %w", err)
	}
	return &r, nil
}

// Read reads a whole recipe from rd (stdin, for `fisherman -`) and parses it.
// It reads to EOF, so the writer must close its end.
func Read(rd io.Reader) (*Recipe, error) {
	data, err := io.ReadAll(io.LimitReader(rd, MaxRecipeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading recipe from stdin: %w", err)
	}
	if len(data) > MaxRecipeBytes {
		return nil, fmt.Errorf("reading recipe from stdin: larger than %d bytes", MaxRecipeBytes)
	}
	return Parse(data)
}

// LoadArg loads the recipe named by a command-line argument: StdinArg reads
// stdin, anything else is a file path (Load).
func LoadArg(arg string, stdin io.Reader) (*Recipe, error) {
	if arg == StdinArg {
		return Read(stdin)
	}
	return Load(arg)
}
