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

// Actual CLI preflight with an owned regular file and controlled tool processes.
// No OS installation or disk mutation is proved by this fixture.
func TestActualLocalSourcePreflightRefusesBeforeDiskWork(t *testing.T) {
	if os.Getenv("FISHERMAN_PREFLIGHT_CHILD") == "1" {
		os.Args = []string{os.Args[0], os.Getenv("FISHERMAN_PREFLIGHT_RECIPE")}
		main()
		os.Exit(99)
	}
	dir, err := os.MkdirTemp(".", ".offline-preflight-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fisherman-test")
	if err = os.WriteFile(binary, raw, 0700); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(dir, "tools")
	if err = os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sfdisk", "mkfs.fat", "mkfs.ext4", "skopeo", "podman"} {
		script := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$CAPTURE\"\nif [ \"${0##*/}\" = skopeo ]; then if [ \"$MODE\" = malformed ]; then printf '{}'; exit 0; fi; exit 7; fi\nexit 93\n"
		if err = os.WriteFile(filepath.Join(tools, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	disk := filepath.Join(dir, "owned-file")
	if err = os.WriteFile(disk, []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	const source = "containers-storage:09dae9ca006837e4962e8f9362c7985758bab0c1c329e3f484e1365babc1ee20"
	raw, err = json.Marshal(map[string]any{"disk": disk, "filesystem": "ext4", "hostname": "fixture", "image": source, "encryption": map[string]any{"type": "none"}})
	if err != nil {
		t.Fatal(err)
	}
	recipe := filepath.Join(dir, "recipe.json")
	if err = os.WriteFile(recipe, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "malformed"} {
		capture := filepath.Join(dir, mode+".argv")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, binary, "-test.run=^TestActualLocalSourcePreflightRefusesBeforeDiskWork$")
		command.Env = append(os.Environ(), "FISHERMAN_PREFLIGHT_CHILD=1", "FISHERMAN_PREFLIGHT_RECIPE="+recipe, "CAPTURE="+capture, "MODE="+mode)
		output, runErr := command.CombinedOutput()
		cancel()
		exit, ok := runErr.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "required local source is absent or unreadable; refusing installation") {
			t.Fatalf("wrong refusal %s: %v %s", mode, runErr, output)
		}
		observed, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal("actual owned tool not executed:", err)
		}
		if string(observed) != "skopeo inspect "+source+"\n" {
			t.Fatalf("unexpected tool/network/mutation: %s", observed)
		}
		unchanged, err := os.ReadFile(disk)
		if err != nil || string(unchanged) != "private fixture" {
			t.Fatal("owned file changed")
		}
	}
}

// errTestMediaFailure stands in for a host media-input.json read failure.
var errTestMediaFailure = errors.New("controlled media input failure")

// image, and a recipe naming the embedded image uses its verified layout;
// anything else installs exactly as before (nil binding).
func TestResolveMediaInputBinding(t *testing.T) {
	old := hostMediaInput
	defer func() { hostMediaInput = old }()
	embedded := &install.MediaInput{
		Image:         "ghcr.io/example/os:stable",
		OCILayoutPath: "/run/media/layout",
		TargetImgref:  "ghcr.io/example/os:stable",
	}
	for _, tt := range []struct {
		name  string
		media *install.MediaInput
		err   error
		image string
		want  bool
	}{
		{"live session binds embedded image", embedded, nil, "", true},
		{"recipe naming embedded image binds", embedded, nil, "ghcr.io/example/os:stable", true},
		{"custom image does not bind", embedded, nil, "ghcr.io/example/other:latest", false},
		{"no media input does not bind", nil, nil, "", false},
		{"media read error does not bind", embedded, errTestMediaFailure, "", false},
		{"media without layout does not bind", &install.MediaInput{Image: "ghcr.io/example/os:stable"}, nil, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hostMediaInput = func() (*install.MediaInput, error) { return tt.media, tt.err }
			got := resolveMediaInput(&recipe.Recipe{Image: tt.image})
			if (got != nil) != tt.want {
				t.Fatalf("resolveMediaInput(%q) bound=%v, want bound=%v", tt.image, got != nil, tt.want)
			}
			if tt.want && got.OCILayoutPath != embedded.OCILayoutPath {
				t.Fatalf("bound wrong layout: %+v", got)
			}
		})
	}
}

// The gate main() applies to a bound media input refuses a digest-mismatched
// layout with the same wording as a missing local source, and never falls
// back to the registry.
func TestBoundMediaDigestMismatchRefuses(t *testing.T) {
	dir := t.TempDir()
	layout := filepath.Join(dir, "layout")
	if err := os.MkdirAll(filepath.Join(layout, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	present := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifest := `{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + present + `","size":11}]}`
	if err := os.WriteFile(filepath.Join(layout, "index.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	mi := &install.MediaInput{
		Image:                  "ghcr.io/example/os:stable",
		PlatformManifestDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		OCILayoutPath:          layout,
		TargetImgref:           "ghcr.io/example/os:stable",
	}
	// The exact gate from main(): PreflightOCILayout(layout, ExpectedDigest).
	if err := install.PreflightOCILayout(mi.OCILayoutPath, mi.ExpectedDigest()); err == nil {
		t.Fatal("digest-mismatched layout accepted")
	}
}

// Actual CLI preflight for `oci:` recipe sources: missing and truncated
// layouts refuse with exit 1 before sfdisk/mkfs/cryptsetup run, for both
// plain and encrypted recipes. No registry fallback is possible: the fake
// skopeo would record any inspection, and the disk fixture must not change.
func TestActualOCILayoutPreflightRefusesBeforeDiskWork(t *testing.T) {
	if os.Getenv("FISHERMAN_PREFLIGHT_CHILD") == "1" {
		os.Args = []string{os.Args[0], os.Getenv("FISHERMAN_PREFLIGHT_RECIPE")}
		main()
		os.Exit(99)
	}
	dir, err := os.MkdirTemp(".", ".oci-preflight-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fisherman-test")
	if err = os.WriteFile(binary, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(dir, "tools")
	if err = os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sfdisk", "mkfs.fat", "mkfs.ext4", "cryptsetup", "skopeo", "podman"} {
		script := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$CAPTURE\"\nexit 93\n"
		if err = os.WriteFile(filepath.Join(tools, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	disk := filepath.Join(dir, "owned-file")
	if err = os.WriteFile(disk, []byte("private fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A truncated layout: index.json names a manifest blob that is absent.
	truncated := filepath.Join(dir, "truncated-layout")
	if err = os.MkdirAll(truncated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(truncated, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	truncatedIndex := `{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json",` +
		`"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":11}]}`
	if err = os.WriteFile(filepath.Join(truncated, "index.json"), []byte(truncatedIndex), 0o644); err != nil {
		t.Fatal(err)
	}
	layouts := map[string]string{
		"missing":   filepath.Join(dir, "no-such-layout"),
		"truncated": truncated,
	}
	encryptions := map[string]any{
		"plain":     map[string]any{"type": "none"},
		"encrypted": map[string]any{"type": "luks-passphrase", "passphrase": "secret"},
	}
	for layoutName, layoutPath := range layouts {
		for encName, encryption := range encryptions {
			name := layoutName + "/" + encName
			t.Run(name, func(t *testing.T) {
				raw, err := json.Marshal(map[string]any{"disk": disk, "filesystem": "ext4", "hostname": "fixture", "image": "oci:" + layoutPath, "encryption": encryption})
				if err != nil {
					t.Fatal(err)
				}
				recipePath := filepath.Join(dir, strings.ReplaceAll(name, "/", "-")+".json")
				if err = os.WriteFile(recipePath, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				capture := filepath.Join(dir, strings.ReplaceAll(name, "/", "-")+".argv")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				command := exec.CommandContext(ctx, binary, "-test.run=^TestActualOCILayoutPreflightRefusesBeforeDiskWork$")
				command.Env = append(os.Environ(), "FISHERMAN_PREFLIGHT_CHILD=1", "FISHERMAN_PREFLIGHT_RECIPE="+recipePath, "CAPTURE="+capture)
				output, runErr := command.CombinedOutput()
				cancel()
				exit, ok := runErr.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "required local source is absent or unreadable; refusing installation") {
					t.Fatalf("wrong refusal %s: %v %s", name, runErr, output)
				}
				if observed, err := os.ReadFile(capture); err == nil {
					for _, line := range strings.Split(string(observed), "\n") {
						tool := strings.SplitN(line, " ", 2)[0]
						switch tool {
						case "sfdisk", "mkfs.fat", "mkfs.ext4", "cryptsetup":
							t.Fatalf("disk work before refusal %s: %s", name, observed)
						}
					}
				}
				unchanged, err := os.ReadFile(disk)
				if err != nil || string(unchanged) != "private fixture" {
					t.Fatal("owned file changed")
				}
			})
		}
	}
}
