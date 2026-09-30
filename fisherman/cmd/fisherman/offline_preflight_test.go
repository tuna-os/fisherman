package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
