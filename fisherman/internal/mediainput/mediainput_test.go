package mediainput

import (
	"go/build"
	"strings"
	"testing"
)

// The contract is shared by read-only callers (probe) and the installer, so
// it must not pull in either of them, or anything else outside the standard
// library.
func TestImportsOnlyStandardLibrary(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("reading package: %v", err)
	}
	for _, imp := range pkg.Imports {
		if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
			t.Errorf("mediainput imports %q; it must stay standard-library-only", imp)
		}
	}
}

func TestValidateBindsDefaultStoreRoot(t *testing.T) {
	m := &MediaInput{
		Image:                 "ghcr.io/example/os:stable",
		TargetImgref:          "ghcr.io/example/os:stable",
		ContainersStoragePath: DefaultContainersStoragePath,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("default store rejected: %v", err)
	}
	m.ContainersStoragePath = "/srv/fixture/store"
	if err := m.Validate(); err == nil {
		t.Fatal("non-default store accepted by Validate")
	}
}

func TestValidateBoundUsesGivenRoot(t *testing.T) {
	m := &MediaInput{
		Image:                 "ghcr.io/example/os:stable",
		TargetImgref:          "ghcr.io/example/os:stable",
		ContainersStoragePath: "/srv/fixture/store",
	}
	if err := m.ValidateBound("/srv/fixture/store"); err != nil {
		t.Fatalf("bound fixture store rejected: %v", err)
	}
	if err := m.ValidateBound(DefaultContainersStoragePath); err == nil {
		t.Fatal("store outside the given root accepted")
	}
}

func TestValidSHA256Digest(t *testing.T) {
	good := "sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{good, true},
		{"sha256:" + strings.Repeat("A", 64), false},
		{"sha512:" + strings.Repeat("a", 64), false},
		{good[:70], false},
		{"", false},
	} {
		if got := ValidSHA256Digest(tt.in); got != tt.want {
			t.Errorf("ValidSHA256Digest(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
