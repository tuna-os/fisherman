package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// Falsification: delete the bootupdRequired match in layoutHint and the first
// row fails; drop the ComposeFsBackend guard and the second row fails.
func TestLayoutHint(t *testing.T) {
	failed := errors.New("bootc install to-filesystem: exit status 1 — last output:\n  error: Installing to disk: bootupd is required for ostree-based installs")
	tests := []struct {
		name string
		r    *recipe.Recipe
		err  error
		want string // substring of the hint, "" for none
	}{
		{"ostree layout on an image without bootupd", &recipe.Recipe{}, failed, "composeFsBackend=true"},
		{"already composefs: bootc would not say it", &recipe.Recipe{ComposeFsBackend: true}, failed, ""},
		{"generic image skips the bootupd check", &recipe.Recipe{GenericImage: true}, failed, ""},
		{"another bootc failure", &recipe.Recipe{}, errors.New("bootc install: No space left on device"), ""},
		{"no error", &recipe.Recipe{}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := layoutHint(tt.r, tt.err)
			if tt.want == "" && got != "" {
				t.Errorf("hint = %q, want none", got)
			}
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("hint = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}
