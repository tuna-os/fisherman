package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/install"
)

// The image source check (#364) and the SELinux label workaround (#370) run
// before the first disk step. These tests run the real install entry point
// in a child process, with owned stand-ins for the host programs on its
// PATH, a regular file as the "disk", and no network: each stand-in records
// its argv. A refused source or a workaround that cannot be built must
// leave no step event and no partition, format, LUKS or mount command in
// that record. The positive cases prove the same run gets past the check
// to the first disk step.

// mutatingTools are the programs that change a disk.
var mutatingTools = []string{"sfdisk", "wipefs", "mkfs.fat", "mkfs.ext4", "mkfs.xfs", "cryptsetup", "mount", "zpool"}

const sourcePreflightTool = `#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >> "$CAPTURE"
case "${0##*/}" in
skopeo)
	case "$*" in
	*--raw*) f="$FIXTURE/raw" ;;
	*docker://*) f="$FIXTURE/remote" ;;
	*) f="$FIXTURE/local" ;;
	esac
	if [ -f "$f.out" ]; then cat "$f.out"; exit 0; fi
	if [ -f "$f.err" ]; then cat "$f.err" >&2; fi
	exit 1 ;;
cc)
	echo "cc: fatal error: dlfcn.h: No such file or directory" >&2
	exit 1 ;;
esac
exit 93
`

func TestSourcePreflightRefusesBeforeDiskWork(t *testing.T) {
	if os.Getenv("FISHERMAN_SOURCE_CHILD") == "1" {
		hostMediaRequired = func() (bool, error) { return false, nil }
		hostMediaInput = func() (*install.MediaInput, error) { return nil, nil }
		install.InspectRetryWaits = []time.Duration{0, 0}
		install.HostSelinuxActive = func() bool { return os.Getenv("SELINUX") == "1" }
		os.Args = []string{os.Args[0], os.Getenv("FISHERMAN_SOURCE_RECIPE")}
		main()
		os.Exit(99)
	}

	dir, err := os.MkdirTemp(".", ".source-preflight-")
	if err != nil {
		t.Fatal(err)
	}
	if dir, err = filepath.Abs(dir); err != nil {
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
	// expandPath puts the tools directory next to the binary first on PATH.
	tools := filepath.Join(dir, "tools")
	if err = os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range append([]string{"skopeo", "podman", "cc", "bootc", "umount", "udevadm", "lsblk", "findmnt", "blkid", "partprobe"}, mutatingTools...) {
		if err = os.WriteFile(filepath.Join(tools, name), []byte(sourcePreflightTool), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	disk := filepath.Join(dir, "owned-file")
	if err = os.WriteFile(disk, []byte("private fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	arch := runtime.GOARCH
	other := "s390x"
	if arch == other {
		other = "amd64"
	}
	image := func(labels, arch string) string {
		return fmt.Sprintf(`{"Digest":"sha256:%s","Layers":["sha256:l1"],"Labels":%s,"Architecture":%q,"Os":"linux"}`,
			strings.Repeat("a", 64), labels, arch)
	}
	bootc := image(`{"containers.bootc":"1"}`, arch)

	for _, tt := range []struct {
		name    string
		files   map[string]string // FIXTURE/<name> contents
		selinux bool
		refused string // code; "" means the install reaches the disk
	}{
		{"unavailable", map[string]string{"remote.err": "pinging container registry ghcr.io: dial tcp: lookup ghcr.io: no such host"}, false, "image_source_unreachable"},
		{"unauthorized", map[string]string{"remote.err": "Requesting bearer token: invalid status code from registry 401 (Unauthorized)"}, false, "image_source_unauthorized"},
		{"missing manifest", map[string]string{"remote.err": "reading manifest stable in ghcr.io/example/os: manifest unknown"}, false, "image_not_found"},
		{"malformed inspect", map[string]string{"remote.out": `{"Layers":"x"}`}, false, "image_inspect_malformed"},
		{"wrong architecture", map[string]string{"remote.out": image(`{"containers.bootc":"1"}`, other)}, false, "image_platform_mismatch"},
		{"ordinary container", map[string]string{"remote.out": image(`null`, arch)}, false, "image_not_bootc"},
		{"selinux workaround without a compiler", map[string]string{"remote.out": bootc}, true, "selinux_workaround_unavailable"},
		{"supported source", map[string]string{"remote.out": bootc}, false, ""},
		{"cached and offline", map[string]string{"remote.err": "dial tcp: connect: network is unreachable", "local.out": bootc}, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			caseDir := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-"))
			if err := os.Mkdir(caseDir, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(caseDir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			rec, err := json.Marshal(map[string]any{
				"disk": disk, "filesystem": "ext4", "hostname": "fixture", "image": "ghcr.io/example/os:stable",
				"encryption": map[string]any{"type": "none"}, "selinuxDisabled": tt.selinux,
			})
			if err != nil {
				t.Fatal(err)
			}
			recipePath := filepath.Join(caseDir, "recipe.json")
			if err := os.WriteFile(recipePath, rec, 0o600); err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(caseDir, "argv")
			selinux := "0"
			if tt.selinux {
				selinux = "1"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestSourcePreflightRefusesBeforeDiskWork$")
			cmd.Env = append(os.Environ(), "FISHERMAN_SOURCE_CHILD=1", "FISHERMAN_SOURCE_RECIPE="+recipePath,
				"CAPTURE="+capture, "FIXTURE="+caseDir, "SELINUX="+selinux)
			out, runErr := cmd.Output()
			exit, ok := runErr.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				t.Fatalf("exit %v, want 1\n%s", runErr, out)
			}
			argv, _ := os.ReadFile(capture)
			var mutated []string
			for _, line := range strings.Split(string(argv), "\n") {
				for _, tool := range mutatingTools {
					if strings.HasPrefix(line, tool+" ") {
						mutated = append(mutated, line)
					}
				}
			}
			last := lastEvent(t, out)
			stepped := strings.Contains(string(out), `"type":"step"`)
			if tt.refused == "" {
				if !stepped {
					t.Fatalf("the install never reached the first disk step; last event %v\nargv:\n%s", last, argv)
				}
				return
			}
			if len(mutated) > 0 || stepped {
				t.Fatalf("a refused install reached the disk: %v\n%s", mutated, out)
			}
			if last["type"] != "error" || last["code"] != tt.refused {
				t.Fatalf("last event %v, want an error with code %s", last, tt.refused)
			}
			if tt.refused != "selinux_workaround_unavailable" && last["field"] != "image" {
				t.Errorf("field = %v, want image", last["field"])
			}
			if b, err := os.ReadFile(disk); err != nil || string(b) != "private fixture" {
				t.Fatal("the fixture disk changed")
			}
		})
	}
}

// lastEvent returns the last JSON event line of out.
func lastEvent(t *testing.T, out []byte) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var ev map[string]any
		if json.Unmarshal([]byte(lines[i]), &ev) == nil && ev["type"] != nil {
			return ev
		}
	}
	t.Fatalf("no event in\n%s", out)
	return nil
}
