package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/probe"
)

// sourceRootProber is a Prober over a fake root whose store holds name at
// storeDigest and whose host booted bootedDigest ("" for a host without
// bootc). It records every command.
func sourceRootProber(t *testing.T, name, storeDigest, bootedDigest string, calls *[]string) *probe.Prober {
	t.Helper()
	root := t.TempDir()
	idx := filepath.Join(root, "var/lib/containers/storage/overlay-images/images.json")
	if err := os.MkdirAll(filepath.Dir(idx), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idx, []byte(fmt.Sprintf(`[{"names":[%q],"digest":%q}]`, name, storeDigest)), 0o644); err != nil {
		t.Fatal(err)
	}
	return &probe.Prober{
		Root:   root,
		Getenv: func(string) string { return "" },
		Run: func(cmd string, args ...string) ([]byte, error) {
			*calls = append(*calls, strings.Join(append([]string{cmd}, args...), " "))
			if cmd != "bootc" || bootedDigest == "" {
				return nil, errors.New("exit status 1")
			}
			return []byte(fmt.Sprintf(`{"status":{"booted":{"image":{"image":{"image":%q},"imageDigest":%q},"ostree":{"checksum":"0a"}}}}`,
				name, bootedDigest)), nil
		},
	}
}

// establishSourceRoot asks the probe only for a local containers-storage
// source that bootc could install directly, and copies its answer.
func TestEstablishSourceRoot(t *testing.T) {
	const name = "ghcr.io/example/os:stable"
	booted := "sha256:" + strings.Repeat("1", 64)
	other := "sha256:" + strings.Repeat("2", 64)
	local := install.Options{SourceImgref: "containers-storage:" + name, TargetImgref: name}
	for _, c := range []struct {
		name        string
		opts        install.Options
		store, host string
		want        install.SourceRoot
		wantNoProbe bool
	}{
		{"established", local, booted, booted, install.SourceRoot{Established: true, Reason: probe.RootDigestMatch, Backend: probe.BackendOstree}, false},
		{"mismatch", local, other, booted, install.SourceRoot{Reason: probe.RootDigestMismatch}, false},
		{"absent", local, booted, "", install.SourceRoot{Reason: probe.RootHostNotBootc}, false},
		{"registry source: no probe", install.Options{SourceImgref: name}, booted, booted, install.SourceRoot{}, true},
		{"composefs: no probe", install.Options{SourceImgref: local.SourceImgref, ComposeFsBackend: true}, booted, booted, install.SourceRoot{}, true},
		{"reuse copy: no probe", install.Options{SourceImgref: local.SourceImgref, RunInSourceContainer: true}, booted, booted, install.SourceRoot{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			withProber(t, sourceRootProber(t, name, c.store, c.host, &calls))
			if got := establishSourceRoot(c.opts, nil); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if c.wantNoProbe && len(calls) != 0 {
				t.Errorf("probed: %v", calls)
			}
			for _, call := range calls {
				if strings.HasPrefix(call, "bootc ") && call != "bootc status --json" {
					t.Errorf("ran %q", call)
				}
			}
		})
	}
}
