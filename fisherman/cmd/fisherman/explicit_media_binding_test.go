package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// Falsification: unrelated OCI/custom references cannot become media-bound;
// exact bindings must preserve target identity and invoke the strict verifier.
func TestExplicitMediaBindingCompatibility(t *testing.T) {
	oldHost, oldVerify := hostMediaInput, preflightExplicitOCIMedia
	t.Cleanup(func() { hostMediaInput, preflightExplicitOCIMedia = oldHost, oldVerify })
	m := &install.MediaInput{OCILayoutPath: "/run/initramfs/live/OCI", TargetImgref: "registry.example/os:stable"}
	for _, tc := range []struct {
		name, source, target   string
		present, reject, bound bool
	}{
		{"exact", "oci:/run/initramfs/live/OCI", m.TargetImgref, true, false, true},
		{"media fallback target", "oci:/run/initramfs/live/OCI", "", true, false, true},
		{"target mismatch", "oci:/run/initramfs/live/OCI", "registry.example/other:stable", true, true, false},
		{"unclean", "oci:/run/initramfs/live/OCI/.", m.TargetImgref, true, true, false},
		{"unrelated standalone", "oci:/srv/operator/layout", m.TargetImgref, true, false, false},
		{"unrelated unclean standalone", "oci:/srv/operator/./layout", m.TargetImgref, true, false, false},
		{"unrelated relative standalone", "oci:operator/layout", m.TargetImgref, true, false, false},
		{"missing descriptor standalone", "oci:/run/initramfs/live/OCI", m.TargetImgref, false, false, false},
		{"custom remote", "registry.example/custom:stable", m.TargetImgref, true, false, false},
		{"native local", "containers-storage:localhost/build:tag", m.TargetImgref, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			hostMediaInput = func() (*install.MediaInput, error) {
				if tc.present {
					return m, nil
				}
				return nil, nil
			}
			preflightExplicitOCIMedia = func(got *install.MediaInput) error {
				calls++
				if got != m {
					t.Fatal("different descriptor verified")
				}
				return nil
			}
			got, err := resolveExplicitOCIMediaInput(&recipe.Recipe{Image: tc.source, TargetImgref: tc.target})
			if (err != nil) != tc.reject || (got != nil) != tc.bound || (calls == 1) != tc.bound {
				t.Fatalf("bound=%v calls=%d error=%v", got != nil, calls, err)
			}
		})
	}
	hostMediaInput = func() (*install.MediaInput, error) { return m, errors.New("malformed descriptor") }
	if _, err := resolveExplicitOCIMediaInput(&recipe.Recipe{Image: "oci:" + m.OCILayoutPath}); err == nil {
		t.Fatal("malformed descriptor accepted")
	}
	hostMediaInput = func() (*install.MediaInput, error) { return m, nil }
	preflightExplicitOCIMedia = func(*install.MediaInput) error { return errors.New("fixture hash mismatch") }
	if _, err := resolveExplicitOCIMediaInput(&recipe.Recipe{Image: "oci:" + m.OCILayoutPath}); err == nil {
		t.Fatal("strict verifier refusal ignored")
	}
}

// The real CLI is reexecuted with a controlled strict-verifier refusal. This
// ordering test is deliberately separate from filesystem hash tests: no real
// read-only mount or disk is required, and no host command may run.
func TestExplicitMediaCLIRefusesBeforeToolsOrDisk(t *testing.T) {
	if os.Getenv("FISHERMAN_BOUND_CHILD") == "1" {
		m := &install.MediaInput{
			OCILayoutPath: os.Getenv("BOUND_LAYOUT"), TargetImgref: "registry.example/custom:stable",
			PlatformManifestDigest: "sha256:" + strings.Repeat("a", 64),
		}
		hostMediaInput = func() (*install.MediaInput, error) { return m, nil }
		preflightExplicitOCIMedia = func(got *install.MediaInput) error {
			if got != m {
				panic("different descriptor")
			}
			if err := os.WriteFile(os.Getenv("BOUND_MARKER"), []byte(got.PlatformManifestDigest), 0600); err != nil {
				panic(err)
			}
			return errors.New("fixture strict expected-digest refusal")
		}
		os.Args = []string{os.Args[0], os.Getenv("BOUND_RECIPE")}
		main()
		os.Exit(99)
	}
	dir := t.TempDir()
	disk := filepath.Join(dir, "owned-file")
	if err := os.WriteFile(disk, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	layout, marker := filepath.Join(dir, "layout"), filepath.Join(dir, "verified")
	raw, err := json.Marshal(map[string]any{
		"disk": disk, "image": "oci:" + layout, "targetImgref": "registry.example/custom:stable",
		"filesystem": "ext4", "hostname": "fixture", "encryption": map[string]string{"type": "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rpath := filepath.Join(dir, "recipe.json")
	if err := os.WriteFile(rpath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "unexpected-tools")
	for _, name := range []string{"sfdisk", "mkfs.fat", "mkfs.ext4", "skopeo", "podman", "mount", "cryptsetup"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf called >> \"$BOUND_CAPTURE\"\nexit 93\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestExplicitMediaCLIRefusesBeforeToolsOrDisk$")
	cmd.Env = append(os.Environ(), "FISHERMAN_BOUND_CHILD=1", "BOUND_LAYOUT="+layout,
		"BOUND_MARKER="+marker, "BOUND_RECIPE="+rpath, "BOUND_CAPTURE="+capture, "PATH="+dir)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil || !strings.Contains(string(out), "fixture strict expected-digest refusal") {
		t.Fatalf("strict preflight not reached: %v %s", runErr, out)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "sha256:"+strings.Repeat("a", 64) {
		t.Fatal("exact expected digest did not reach verifier")
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("host tool reached before refusal")
	}
	if got, err := os.ReadFile(disk); err != nil || string(got) != "unchanged" {
		t.Fatal("owned file mutated")
	}
}

// Falsification: #346's native-context guard must run before strict OCI work;
// an early guard refusal must reach neither that verifier nor host tools.
// The native case calls the real context guard. The refusal case injects its
// error at the command seam; post tests separately exercise sandbox detection.
func TestExplicitMediaStackedGuardOrdering(t *testing.T) {
	if mode := os.Getenv("FISHERMAN_STACK_CHILD"); mode != "" {
		record := func(value string) {
			f, err := os.OpenFile(os.Getenv("STACK_ORDER"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			if _, err := f.WriteString(value + "\n"); err != nil {
				panic(err)
			}
			if err := f.Close(); err != nil {
				panic(err)
			}
		}
		realGuard := checkTargetFlatpakIntentContext
		checkTargetFlatpakIntentContext = func(intent *recipe.FlatpakIntent) error {
			if intent == nil || intent.Target.Mode != "replace" {
				panic("lost explicit Flatpak intent")
			}
			record("guard")
			if mode == "guard refusal" {
				return errors.New("fixture native-context refusal")
			}
			return realGuard(intent)
		}
		m := &install.MediaInput{OCILayoutPath: os.Getenv("BOUND_LAYOUT"), TargetImgref: "registry.example/custom:stable"}
		hostMediaInput = func() (*install.MediaInput, error) { return m, nil }
		preflightExplicitOCIMedia = func(got *install.MediaInput) error {
			if got != m {
				panic("different descriptor")
			}
			record("media")
			return errors.New("fixture strict expected-digest refusal")
		}
		os.Args = []string{os.Args[0], os.Getenv("BOUND_RECIPE")}
		main()
		os.Exit(99)
	}
	for _, mode := range []string{"native", "guard refusal"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			disk := filepath.Join(dir, "owned-file")
			if err := os.WriteFile(disk, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			layout, order := filepath.Join(dir, "layout"), filepath.Join(dir, "order")
			raw, err := json.Marshal(map[string]any{
				"disk": disk, "image": "oci:" + layout, "targetImgref": "registry.example/custom:stable",
				"filesystem": "ext4", "hostname": "fixture", "encryption": map[string]string{"type": "none"},
				"flatpakIntent": map[string]any{
					"version": 1, "live": map[string]any{"mode": "inherit"},
					"target": map[string]any{"mode": "replace", "apps": []any{}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			rpath := filepath.Join(dir, "recipe.json")
			if err := os.WriteFile(rpath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(dir, "unexpected-tools")
			for _, name := range []string{"sfdisk", "mkfs.fat", "mkfs.ext4", "skopeo", "podman", "mount", "cryptsetup", "flatpak-spawn"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf called >> \"$BOUND_CAPTURE\"\nexit 93\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, self, "-test.run=^TestExplicitMediaStackedGuardOrdering$")
			cmd.Env = append(os.Environ(), "FISHERMAN_STACK_CHILD="+mode, "BOUND_LAYOUT="+layout,
				"STACK_ORDER="+order, "BOUND_RECIPE="+rpath, "BOUND_CAPTURE="+capture, "PATH="+dir)
			out, runErr := cmd.CombinedOutput()
			wantError, wantOrder := "fixture strict expected-digest refusal", "guard\nmedia\n"
			if mode == "guard refusal" {
				wantError, wantOrder = "fixture native-context refusal", "guard\n"
			}
			if runErr == nil || !strings.Contains(string(out), wantError) {
				t.Fatalf("wrong CLI refusal: %v %s", runErr, out)
			}
			if raw, err := os.ReadFile(order); err != nil || string(raw) != wantOrder {
				t.Fatalf("wrong guard/verifier order: %q %v", raw, err)
			}
			if _, err := os.Stat(capture); !os.IsNotExist(err) {
				t.Fatal("host tool reached before refusal")
			}
			if got, err := os.ReadFile(disk); err != nil || string(got) != "unchanged" {
				t.Fatal("owned file mutated")
			}
		})
	}
}
