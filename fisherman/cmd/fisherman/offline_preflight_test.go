package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Actual CLI preflight with an owned regular file and controlled tool processes.
// No OS installation or disk mutation is proved by this fixture.
func TestActualLocalSourcePreflightRefusesBeforeDiskWork(t *testing.T) {
	if os.Getenv("FISHERMAN_PREFLIGHT_CHILD") == "1" {
		hostMediaRequired = func() (bool, error) { return false, nil }
		hostMediaInput = func() (*install.MediaInput, error) { return nil, nil }
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

// A host receipt is authoritative only for its exact source and day-2 target.
func TestResolveMediaInputBinding(t *testing.T) {
	oldMedia, oldRequired := hostMediaInput, hostMediaRequired
	oldSandboxed, oldPreflight := hostMediaSandboxed, preflightBoundMediaInput
	defer func() {
		hostMediaInput, hostMediaRequired = oldMedia, oldRequired
		hostMediaSandboxed, preflightBoundMediaInput = oldSandboxed, oldPreflight
	}()
	hostMediaSandboxed = func() bool { return false }
	embedded := testBoundMediaInput()
	for _, tt := range []struct {
		name     string
		media    *install.MediaInput
		err      error
		image    string
		target   string
		required bool
		want     bool
		wantErr  bool
	}{
		{"empty live recipe", embedded, nil, "", "", true, true, false},
		{"exact source and target", embedded, nil, embedded.Image, embedded.TargetImgref, true, true, false},
		{"exact OCI source", embedded, nil, "oci:" + embedded.OCILayoutPath, embedded.TargetImgref, true, true, false},
		{"noncanonical OCI alias", embedded, nil, "oci:" + embedded.OCILayoutPath + "/.", "", true, false, true},
		{"optional exact OCI source", embedded, nil, "oci:" + embedded.OCILayoutPath, "", false, true, false},
		{"optional unrelated OCI source", embedded, nil, "oci:/srv/operator/layout", "", false, false, false},
		{"optional missing OCI receipt", nil, nil, "oci:/srv/operator/layout", "", false, false, false},
		{"optional malformed receipt leaves standalone OCI unbound", nil, errTestMediaFailure, "oci:/srv/operator/layout", "", false, false, false},
		{"mandatory malformed receipt refuses explicit OCI", nil, errTestMediaFailure, "oci:/srv/operator/layout", "", true, false, true},
		{"target is not source", embedded, nil, embedded.TargetImgref, "", true, false, true},
		{"different explicit source", embedded, nil, "ghcr.io/example/other:latest", "", true, false, true},
		{"different explicit target", embedded, nil, embedded.Image, "ghcr.io/example/other:latest", true, false, true},
		{"missing mandatory input", nil, nil, embedded.Image, "", true, false, true},
		{"unreadable mandatory input", nil, errTestMediaFailure, embedded.Image, "", true, false, true},
		{"empty mandatory input", &install.MediaInput{}, nil, "", "", true, false, true},
		{"nonlive explicit network", nil, nil, "ghcr.io/example/network:latest", "", false, false, false},
		{"nonlive different source", embedded, nil, "ghcr.io/example/network:latest", "", false, false, false},
		{"nonlive unrelated invalid receipt", nil, errTestMediaFailure, "ghcr.io/example/network:latest", "", false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hostMediaInput = func() (*install.MediaInput, error) { return tt.media, tt.err }
			hostMediaRequired = func() (bool, error) { return tt.required, nil }
			calls := 0
			preflightBoundMediaInput = func(got *install.MediaInput) error {
				calls++
				if got != tt.media {
					t.Fatal("verified a different media input")
				}
				return nil
			}
			got, err := resolveMediaInput(&recipe.Recipe{Image: tt.image, TargetImgref: tt.target})
			if (err != nil) != tt.wantErr || (got != nil) != tt.want {
				t.Fatalf("binding=%+v err=%v, want binding=%v error=%v", got, err, tt.want, tt.wantErr)
			}
			if (calls == 1) != tt.want {
				t.Fatalf("strict verifier calls=%d, want bound=%v", calls, tt.want)
			}
		})
	}
}

func testBoundMediaInput() *install.MediaInput {
	return &install.MediaInput{
		SchemaVersion:          1,
		Image:                  "ghcr.io/example/source:qualified",
		TargetImgref:           "ghcr.io/example/os:stable",
		OCILayoutPath:          "/run/initramfs/live/OCI",
		Platform:               runtime.GOOS + "/" + runtime.GOARCH,
		PlatformManifestDigest: "sha256:" + strings.Repeat("a", 64),
		ConfigDigest:           "sha256:" + strings.Repeat("b", 64),
	}
}

// Exercise main's actual refusal boundary. The strict verifier's filesystem
// behavior has its own shared tests; this test proves consumer ordering and
// refusal propagation without mounting filesystems or touching any device.
func TestActualMandatoryMediaRefusesBeforeHostCommands(t *testing.T) {
	if os.Getenv("FISHERMAN_MEDIA_CHILD") == "1" {
		mode := os.Getenv("FISHERMAN_MEDIA_CASE")
		mi := testBoundMediaInput()
		hostMediaRequired = func() (bool, error) { return true, nil }
		hostMediaSandboxed = func() bool { return mode == "sandbox" }
		hostMediaInput = func() (*install.MediaInput, error) {
			switch mode {
			case "sandbox":
				fmt.Fprintln(os.Stderr, "FORBIDDEN MEDIA READ")
				os.Exit(97)
			case "missing", "empty-missing", "composefs-missing":
				return nil, nil
			case "malformed":
				return nil, errTestMediaFailure
			case "source":
				mi.Image = "ghcr.io/example/wrong:stable"
			case "target":
				mi.TargetImgref = "ghcr.io/example/wrong:stable"
			}
			return mi, nil
		}
		preflightBoundMediaInput = func(got *install.MediaInput) error {
			if got != mi {
				panic("verified a different media input")
			}
			fmt.Fprintln(os.Stderr, "STRICT VERIFIER REACHED")
			return errors.New("controlled shared strict verifier refusal")
		}
		runner.OutputFn = func(name string, args ...string) ([]byte, error) {
			fmt.Fprintf(os.Stderr, "FORBIDDEN HOST COMMAND: %s %v\n", name, args)
			os.Exit(97)
			return nil, nil
		}
		os.Args = []string{os.Args[0], "-"}
		image := mi.Image
		switch mode {
		case "empty-missing", "empty-verifier":
			image = ""
		case "oci-verifier":
			image = "oci:" + mi.OCILayoutPath
		case "noncanonical":
			image = "oci:" + mi.OCILayoutPath + "/."
		}
		composefs := mode == "composefs-missing" || mode == "composefs-verifier"
		recipeStdin = strings.NewReader(fmt.Sprintf(`{"disk":"/never-a-device","filesystem":"ext4","hostname":"fixture","image":%q,"targetImgref":"ghcr.io/example/os:stable","composeFsBackend":%t,"encryption":{"type":"none"}}`, image, composefs))
		main()
		os.Exit(99)
	}
	for _, mode := range []string{"missing", "empty-missing", "composefs-missing", "malformed", "source", "target", "sandbox", "noncanonical", "verifier", "empty-verifier", "composefs-verifier", "oci-verifier"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestActualMandatoryMediaRefusesBeforeHostCommands$")
			command.Env = append(os.Environ(), "FISHERMAN_MEDIA_CHILD=1", "FISHERMAN_MEDIA_CASE="+mode)
			output, err := command.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "required local source is absent or unreadable; refusing installation") || strings.Contains(string(output), "FORBIDDEN") {
				t.Fatalf("wrong preflight boundary: %v %s", err, output)
			}
			if got := strings.Count(string(output), "STRICT VERIFIER REACHED"); got != 0 && !strings.HasSuffix(mode, "verifier") || strings.HasSuffix(mode, "verifier") && got != 1 {
				t.Fatalf("wrong strict verifier invocation count: %v %s", err, output)
			}
		})
	}
}

// Actual CLI preflight for `oci:` recipe sources: missing and truncated
// layouts refuse with exit 1 before sfdisk/mkfs/cryptsetup run, for both
// plain and encrypted recipes. No registry fallback is possible: the fake
// skopeo would record any inspection, and the disk fixture must not change.
func TestActualOCILayoutPreflightRefusesBeforeDiskWork(t *testing.T) {
	if os.Getenv("FISHERMAN_PREFLIGHT_CHILD") == "1" {
		hostMediaRequired = func() (bool, error) { return false, nil }
		hostMediaInput = func() (*install.MediaInput, error) { return nil, errTestMediaFailure }
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
				if !strings.Contains(string(output), "Checking image cache...") || strings.Contains(string(output), errTestMediaFailure.Error()) {
					t.Fatalf("optional malformed receipt displaced standalone OCI validation %s: %s", name, output)
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
