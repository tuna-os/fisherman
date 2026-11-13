package probe

import (
	"go/build"
	"testing"
)

// probe is read-only (docs/PROBE.md). It must not link the install executor;
// the offline-media contract it shares with install lives in
// internal/mediainput.
func TestProbeDoesNotImportInstall(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("reading package: %v", err)
	}
	for _, imp := range pkg.Imports {
		if imp == "github.com/tuna-os/fisherman/internal/install" {
			t.Fatalf("probe imports %s; share types through internal/mediainput instead", imp)
		}
	}
}
