package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/fisherman/tui/internal/backend"
	"github.com/tuna-os/fisherman/tui/internal/catalog"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

// The tests in this file run the TUI's recipes against the REAL fisherman,
// built from this repository, through `validate` only: no install, no disk.
// tui/ and fisherman/ are separate modules, so nothing else notices when the
// recipe one writes stops being the recipe the other reads (#367, #178).

var (
	realOnce sync.Once
	realDir  string
	realErr  error
)

// realFisherman builds ../../../fisherman once and puts it first on PATH.
func realFisherman(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "fisherman"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		t.Skipf("the fisherman module is not beside tui/ (%v)", err)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	realOnce.Do(func() {
		realDir, realErr = os.MkdirTemp("", "tui-real-fisherman-")
		if realErr != nil {
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(realDir, "fisherman"), "./cmd/fisherman")
		cmd.Dir = src
		// The module beside this one decides its own flags.
		cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			realErr = &buildError{err: err, out: string(out)}
		}
	})
	if realErr != nil {
		t.Fatalf("building the real fisherman: %v", realErr)
	}
	t.Setenv("PATH", realDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(realDir, "fisherman")
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

func TestMain(m *testing.M) {
	code := m.Run()
	if realDir != "" {
		_ = os.RemoveAll(realDir)
	}
	os.Exit(code)
}

func repoCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "data", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func codes(ps []backend.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

// TestRealBackend_ImageTable: for Dakota, Arch, Debian and a GRUB image from
// data/images.json, the recipe the TUI writes carries the image's boot
// stack, the real fisherman accepts its fields, and the same image with XFS
// is refused by the TUI and by fisherman when it needs composefs.
func TestRealBackend_ImageTable(t *testing.T) {
	bin := realFisherman(t)
	cat := repoCatalog(t)
	cases := []struct {
		name       string
		ref        string
		bootloader string
		composefs  bool
		filesystem string
	}{
		{"Dakota", "ghcr.io/projectbluefin/dakota:latest", "systemd", true, "btrfs"},
		{"Arch", "ghcr.io/bootcrew/arch-bootc:latest", "systemd", true, "ext4"},
		{"Debian", "ghcr.io/bootcrew/debian-bootc:latest", "systemd", true, "ext4"},
		{"Aurora (GRUB, from data/images.json)", "ghcr.io/ublue-os/aurora:latest", "", false, "btrfs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img, ok := cat.Resolve(tc.ref)
			if !ok {
				t.Fatalf("%s does not resolve", tc.ref)
			}
			cfg := &config.InstallConfig{
				Image: img, DiskDevice: "/dev/does-not-exist", Filesystem: img.Filesystem, Hostname: "marlin",
				Username: "alice", FullName: "Alice", Password: "hunter2-password",
				EncryptionEnabled: true, Passphrase: "correct horse battery",
			}

			// 1. What WriteRecipe's successor puts in the recipe.
			raw, err := cfg.Recipe()
			if err != nil {
				t.Fatalf("Recipe: %v", err)
			}
			var recipe map[string]any
			if err := json.Unmarshal(raw, &recipe); err != nil {
				t.Fatal(err)
			}
			if recipe["composeFsBackend"] != tc.composefs || recipe["bootloader"] != tc.bootloader || recipe["filesystem"] != tc.filesystem {
				t.Errorf("recipe has composeFsBackend=%v bootloader=%q filesystem=%q, want %v %q %q",
					recipe["composeFsBackend"], recipe["bootloader"], recipe["filesystem"], tc.composefs, tc.bootloader, tc.filesystem)
			}
			if _, hasUser := recipe["user"]; hasUser != img.NeedsUserCreation {
				t.Errorf("user block present = %v, want %v (needs_user_creation)", hasUser, img.NeedsUserCreation)
			}

			// 2. The real backend accepts every field the confirm page sends.
			if blocked := confirmBlocker(cfg); blocked != "" {
				t.Errorf("the real fisherman refuses the TUI's recipe: %s", blocked)
			}

			// 3. The full validator knows every key the TUI writes. (The
			// disk does not exist here, so it is not valid as a whole.)
			cmd := exec.Command(bin, "validate", "--json", "-")
			cmd.Stdin = bytes.NewReader(raw)
			out, _ := cmd.Output()
			var res struct {
				Errors []backend.Problem `json:"errors"`
			}
			if err := json.Unmarshal(out, &res); err != nil {
				t.Fatalf("fisherman validate --json -: %v\n%s", err, out)
			}
			for _, p := range res.Errors {
				if p.Code == "recipe_unknown_field" || p.Code == "recipe_malformed" {
					t.Errorf("fisherman does not read the TUI's recipe: %s: %s", p.Code, p.Message)
				}
			}

			// 4. XFS.
			cfg.Filesystem = "xfs"
			fields, err := cfg.Fields("filesystem", "composeFsBackend", "bootloader", "encryption")
			if err != nil {
				t.Fatal(err)
			}
			problems, err := backend.ValidatePartial(fields)
			if err != nil {
				t.Fatal(err)
			}
			refused := strings.Contains(strings.Join(codes(problems), " "), "composefs_requires_fsverity")
			if refused != tc.composefs {
				t.Errorf("fisherman on xfs: refused=%v (%v), want %v", refused, codes(problems), tc.composefs)
			}
			if blocked := confirmBlocker(cfg); (blocked != "") != tc.composefs {
				t.Errorf("the confirm page on xfs: blocked=%q, want blocked=%v", blocked, tc.composefs)
			}
		})
	}
}

// The page-level checks reach the real rules: fisherman's, not a copy.
func TestRealBackend_FieldChecks(t *testing.T) {
	realFisherman(t)
	cfg := testConfig()
	cfg.Hostname = "-router"
	if msg := checkFields(cfg, "hostname"); msg == "" {
		t.Error("fisherman accepts the hostname -router through the TUI")
	}
	cfg.Hostname = "router"
	if msg := checkFields(cfg, "hostname"); msg != "" {
		t.Errorf("hostname router: %s", msg)
	}
	cfg.Username = "root"
	if msg := checkFields(cfg, "user"); msg == "" {
		t.Error("fisherman accepts the username root through the TUI")
	}
	cfg.EncryptionEnabled, cfg.Passphrase = true, ""
	if msg := checkFields(cfg, "encryption"); msg == "" {
		t.Error("fisherman accepts an empty LUKS passphrase through the TUI")
	}
}

// The real probe output parses. It may fail on a machine with no lsblk;
// then there is nothing to parse and the TUI shows the error.
func TestRealBackend_Probe(t *testing.T) {
	realFisherman(t)
	disks, err := backend.Probe()
	if err != nil {
		t.Skipf("fisherman probe did not run here: %v", err)
	}
	for _, d := range disks {
		if d.Path == "" {
			t.Errorf("a disk without a path: %+v", d)
		}
		if !d.Eligible && d.ExcludedReason == "" {
			t.Errorf("an excluded disk without a reason: %+v", d)
		}
	}
}
