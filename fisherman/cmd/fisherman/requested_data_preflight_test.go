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

func TestActualRequestedDataRefusesVolatileRescueBeforeDiskWork(t *testing.T) {
	if os.Getenv("FISHERMAN_DATA_CHILD") == "1" {
		lookPath = func(name string) (string, error) {
			_ = os.WriteFile(os.Getenv("FISHERMAN_DATA_TOOL_MARKER"), []byte(name), 0600)
			return "", errors.New("owned tool boundary refusal")
		}
		os.Args = []string{os.Args[0], os.Getenv("FISHERMAN_DATA_RECIPE")}
		main()
		os.Exit(99)
	}
	dir := t.TempDir()
	disk := filepath.Join(dir, "private-target")
	source := filepath.Join(dir, "private-source")
	for path, data := range map[string]string{disk: "preserved private bytes", source: "selected source bytes"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(map[string]any{"disk": disk, "filesystem": "ext4", "hostname": "fixture", "image": "fixture.invalid/selected:tag", "encryption": map[string]any{"type": "none"}, "slurp": map[string]any{"sourcePartition": source, "users": []any{map[string]any{"name": "Selected", "categories": []string{"Documents"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	recipe := filepath.Join(dir, "recipe.json")
	if err = os.WriteFile(recipe, raw, 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "tool-boundary")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, self, "-test.run=^TestActualRequestedDataRefusesVolatileRescueBeforeDiskWork$")
	command.Env = append(os.Environ(), "FISHERMAN_DATA_CHILD=1", "FISHERMAN_DATA_RECIPE="+recipe, "FISHERMAN_DATA_TOOL_MARKER="+marker)
	output, runErr := command.CombinedOutput()
	status, ok := runErr.(*exec.ExitError)
	if !ok || status.ExitCode() != 1 || !strings.Contains(string(output), "requested data migration requires a verified durable rescue store; refusing installation before disk work") {
		t.Fatalf("actual selected request did not refuse: %v %s", runErr, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("tool boundary consumed before selected-data refusal")
	}
	for path, want := range map[string]string{disk: "preserved private bytes", source: "selected source bytes"} {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != want {
			t.Fatal("private input mutated")
		}
	}
}
