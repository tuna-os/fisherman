package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// hostFixture builds a Prober over a fake root holding files (relative path
// -> content; a trailing "/" makes a directory). bootc status reports
// liveImage. Nothing reads the real host.
func hostFixture(t *testing.T, files map[string]string, liveImage string) *probe.Prober {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &probe.Prober{
		Root:   root,
		Getenv: func(string) string { return "" },
		Run: func(name string, args ...string) ([]byte, error) {
			if name == "bootc" && liveImage != "" {
				return []byte(`{"status":{"booted":{"image":{"image":{"image":"` + liveImage + `"}}}}}`), nil
			}
			return nil, errors.New("exit status 1")
		},
	}
}

const offlineIndex = `[{"names":["ghcr.io/example/os:stable"]}]`

func TestCheckImageSource(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		image     string
		wantErr   bool
		wantLive  bool
		wantImage string
	}{
		{name: "image set, not live", files: nil, image: "ghcr.io/example/os:stable"},
		{name: "empty image, installed host", files: map[string]string{"run/ostree-booted": ""}, wantErr: true},
		{name: "empty image, ostree live", files: map[string]string{"run/ostree-live": ""}, wantLive: true, wantImage: "ghcr.io/example/os:stable"},
		{name: "empty image, dracut live", files: map[string]string{"proc/cmdline": "root=live:CDLABEL=x rd.live.image quiet"}, wantLive: true, wantImage: "ghcr.io/example/os:stable"},
		{name: "empty image, opt-in flag", files: map[string]string{"etc/bootc-installer/live-iso-mode": ""}, wantLive: true, wantImage: "ghcr.io/example/os:stable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withProber(t, hostFixture(t, tt.files, "ghcr.io/example/os:stable"))
			live, err := checkImageSource(&recipe.Recipe{Image: tt.image})
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkImageSource() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, recipe.ErrImageRequired) {
				t.Errorf("error = %v, want ErrImageRequired", err)
			}
			if live.IsLive != tt.wantLive || live.LiveImage != tt.wantImage {
				t.Errorf("live = %+v, want is_live=%v image=%q", live, tt.wantLive, tt.wantImage)
			}
		})
	}
}

// An image that is set must not cost a probe: nothing about the host is
// needed to accept it.
func TestCheckImageSourceDoesNotProbeWhenImageSet(t *testing.T) {
	old := hostLive
	hostLive = func() probe.Live { t.Fatal("probed the host for a recipe with an image"); return probe.Live{} }
	t.Cleanup(func() { hostLive = old })
	if _, err := checkImageSource(&recipe.Recipe{Image: "ghcr.io/example/os:stable"}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveImageStores(t *testing.T) {
	withProber(t, hostFixture(t, map[string]string{
		"usr/share/tuna-installer/oci-store/overlay-images/images.json": offlineIndex,
		"etc/tuna-installer/offline-stores":                             "# listed\n/srv/listed\n/srv/missing\n",
		"srv/listed/":                                                   "",
	}, ""))

	tests := []struct {
		name           string
		stores         []string
		want           []string
		wantDiscovered bool
	}{
		{name: "absent: discovered on the host", stores: nil,
			want: []string{"/srv/listed", "/usr/share/tuna-installer/oci-store"}, wantDiscovered: true},
		{name: "explicit list honoured", stores: []string{"/run/media/iso/store"},
			want: []string{"/run/media/iso/store"}},
		{name: "explicit [] opts out", stores: []string{}, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, discovered := resolveImageStores(&recipe.Recipe{AdditionalImageStores: tt.stores})
			if !reflect.DeepEqual(got, tt.want) || discovered != tt.wantDiscovered {
				t.Errorf("resolveImageStores() = %#v, %v; want %#v, %v", got, discovered, tt.want, tt.wantDiscovered)
			}
		})
	}
}

func TestResolveImageStoresNoneOnHost(t *testing.T) {
	withProber(t, hostFixture(t, nil, ""))
	got, discovered := resolveImageStores(&recipe.Recipe{})
	if len(got) != 0 || !discovered {
		t.Errorf("resolveImageStores() = %#v, %v; want none, discovered", got, discovered)
	}
}

func TestLoadRecipeFromStdin(t *testing.T) {
	old := recipeStdin
	t.Cleanup(func() { recipeStdin = old })

	recipeStdin = strings.NewReader(`{"hostname":"piped"}`)
	r, err := loadRecipe("-")
	if err != nil || r.Hostname != "piped" {
		t.Fatalf("loadRecipe(-) = %+v, %v", r, err)
	}
	recipeStdin = strings.NewReader("")
	if _, err := loadRecipe("-"); !errors.Is(err, recipe.ErrEmptyRecipe) {
		t.Errorf("loadRecipe(-) on empty stdin: %v, want ErrEmptyRecipe", err)
	}
}

func TestStdinArgIsNotASubcommand(t *testing.T) {
	if looksLikeSubcommand("-") {
		t.Error(`looksLikeSubcommand("-") = true; "-" is the stdin recipe`)
	}
	if !looksLikeSubcommand("--") {
		t.Error(`looksLikeSubcommand("--") = false; only a lone "-" means stdin`)
	}
}

func TestRunValidateStdinLiveEmptyImage(t *testing.T) {
	withProber(t, hostFixture(t, map[string]string{
		"run/ostree-live": "",
		"usr/share/tuna-installer/oci-store/overlay-images/images.json": offlineIndex,
	}, "ghcr.io/example/os:stable"))
	disk := filepath.Join(t.TempDir(), "disk")
	if err := os.WriteFile(disk, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := recipeStdin
	recipeStdin = strings.NewReader(`{"disk":"` + disk + `","filesystem":"xfs","hostname":"h"}`)
	t.Cleanup(func() { recipeStdin = old })

	out, _ := captureOutput(t, func() { runValidate([]string{"-"}) })
	for _, want := range []string{
		"recipe on stdin is valid",
		"(the running live image ghcr.io/example/os:stable)",
		"offline:    /usr/share/tuna-installer/oci-store",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("validate output missing %q:\n%s", want, out)
		}
	}
}

// The real CLI, re-executed: `fisherman -` and `fisherman validate -` with a
// recipe on stdin, against a fixture host that is not live media. Every case
// fails before any host tool or disk is touched.
func TestStdinRecipeCLI(t *testing.T) {
	if os.Getenv("FISHERMAN_STDIN_CHILD") == "1" {
		root := os.Getenv("FISHERMAN_STDIN_ROOT")
		newProber = func() *probe.Prober {
			return &probe.Prober{Root: root, Getenv: func(string) string { return "" },
				Run: func(string, ...string) ([]byte, error) { return nil, errors.New("exit status 1") }}
		}
		os.Args = append([]string{os.Args[0]}, strings.Fields(os.Getenv("FISHERMAN_STDIN_ARGS"))...)
		main()
		os.Exit(99)
	}

	disk := filepath.Join(t.TempDir(), "disk")
	if err := os.WriteFile(disk, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	noImage, _ := json.Marshal(map[string]any{"disk": disk, "filesystem": "ext4", "hostname": "h"})

	tests := []struct {
		name      string
		args      string
		stdin     string
		wantCode  int
		wantError string // in the install's {"type":"error"} event, or validate's stderr
	}{
		{"install: invalid JSON", "-", `{"disk":`, 1, "loading recipe: parsing recipe"},
		{"install: empty stdin", "-", "", 1, "loading recipe: recipe is empty"},
		{"install: empty image, not live", "-", string(noImage), 1, "invalid recipe: image is required"},
		{"validate: invalid JSON", "validate -", "nope", 1, "parsing recipe"},
		{"validate: empty stdin", "validate -", "", 1, "recipe is empty"},
		{"validate: empty image, not live", "validate -", string(noImage), 1, "image is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStdinRecipeCLI$", "-test.count=1")
			cmd.Env = append(os.Environ(),
				"FISHERMAN_STDIN_CHILD=1",
				"FISHERMAN_STDIN_ROOT="+t.TempDir(),
				"FISHERMAN_STDIN_ARGS="+tt.args)
			cmd.Stdin = strings.NewReader(tt.stdin)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tt.wantCode {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.wantCode, stdout.String(), stderr.String())
			}
			if strings.HasPrefix(tt.args, "validate") {
				if !strings.Contains(stderr.String(), tt.wantError) {
					t.Errorf("stderr missing %q:\n%s", tt.wantError, stderr.String())
				}
				return
			}
			var last map[string]any
			for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
				var ev map[string]any
				if json.Unmarshal([]byte(line), &ev) == nil {
					last = ev
				}
			}
			if last["type"] != "error" || !strings.Contains(last["message"].(string), tt.wantError) {
				t.Errorf("last event = %v, want error containing %q\nstdout:\n%s", last, tt.wantError, stdout.String())
			}
		})
	}
}
