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
)

// TestEscapingRecipePathsRefusedBeforeDiskWork runs the real install entry
// point on recipes whose host paths escape the target (#333) and checks that
// each is refused as an invalid recipe before fisherman looks up a single
// host tool, so nothing is partitioned, formatted or mounted.
func TestEscapingRecipePathsRefusedBeforeDiskWork(t *testing.T) {
	if os.Getenv("FISHERMAN_PATHS_CHILD") == "1" {
		lookPath = func(name string) (string, error) {
			_ = os.WriteFile(os.Getenv("FISHERMAN_PATHS_TOOL_MARKER"), []byte(name), 0o600)
			return "", errors.New("owned tool boundary refusal")
		}
		os.Args = []string{os.Args[0], os.Getenv("FISHERMAN_PATHS_RECIPE")}
		main()
		os.Exit(99)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, code string
		edit       func(r map[string]any, part string)
	}{
		{"custom mount target with ..", "customMounts[1]: target must be", func(r map[string]any, part string) {
			r["customMounts"] = []any{
				map[string]any{"partition": part, "target": "/", "fstype": "ext4"},
				map[string]any{"partition": part, "target": "/../../etc", "fstype": "ext4"},
			}
		}},
		{"relative custom mount target", "customMounts[1]: target must be", func(r map[string]any, part string) {
			r["customMounts"] = []any{
				map[string]any{"partition": part, "target": "/", "fstype": "ext4"},
				map[string]any{"partition": part, "target": "etc", "fstype": "ext4"},
			}
		}},
		{"targetMount is the host root", "targetMount must be", func(r map[string]any, _ string) {
			r["targetMount"] = "/"
		}},
		{"targetMount with ..", "targetMount must be", func(r map[string]any, _ string) {
			r["targetMount"] = "/mnt/../etc"
		}},
		{"flatpakVarPath with ..", "flatpakVarPath must stay", func(r map[string]any, _ string) {
			r["flatpakVarPath"] = "../../etc"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			disk := filepath.Join(dir, "disk")
			if err := os.WriteFile(disk, []byte("preserved private bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			r := map[string]any{"disk": disk, "filesystem": "ext4", "hostname": "fixture",
				"image": "fixture.invalid/selected:tag", "encryption": map[string]any{"type": "none"}}
			tc.edit(r, disk)
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			recipePath := filepath.Join(dir, "recipe.json")
			if err := os.WriteFile(recipePath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "tool-boundary")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, self, "-test.run=^TestEscapingRecipePathsRefusedBeforeDiskWork$")
			cmd.Env = append(os.Environ(), "FISHERMAN_PATHS_CHILD=1",
				"FISHERMAN_PATHS_RECIPE="+recipePath, "FISHERMAN_PATHS_TOOL_MARKER="+marker)
			out, runErr := cmd.CombinedOutput()
			status, ok := runErr.(*exec.ExitError)
			if !ok || status.ExitCode() != 1 || !strings.Contains(string(out), "invalid recipe") ||
				!strings.Contains(string(out), tc.code) {
				t.Fatalf("recipe was not refused as invalid (%q): %v\n%s", tc.code, runErr, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("a host tool was looked up before the recipe was refused")
			}
			if after, err := os.ReadFile(disk); err != nil || string(after) != "preserved private bytes" {
				t.Fatal("disk fixture mutated")
			}
		})
	}
}
