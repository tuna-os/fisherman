package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootcInstall_SourceExecutionRoot runs BootcInstall for a local
// containers-storage source against stand-in bootc and podman programs
// (#363). Only a host that booted the source (an established execution
// root) installs with its own bootc; an unrelated host, a host whose
// identity is unknown and a host without bootc run the source image's own
// bootc in its container. In every case the source and the update origin
// stay separate: --target-imgref is the recipe's targetImgref, never the
// source.
func TestBootcInstall_SourceExecutionRoot(t *testing.T) {
	const source = "ghcr.io/example/os-nvidia:stable"
	const update = "ghcr.io/example/os:stable"
	for _, c := range []struct {
		name      string
		hostBootc bool
		root      SourceRoot
		direct    bool
	}{
		{"established: the host booted the source", true, SourceRoot{Established: true, Reason: "digest_match", Backend: "ostree"}, true},
		{"mismatch: the host booted another image", true, SourceRoot{Reason: "digest_mismatch"}, false},
		{"unknown: the host's digest is unreadable", true, SourceRoot{Reason: "host_digest_unknown"}, false},
		{"absent: no bootc on the host", false, SourceRoot{Reason: "host_not_bootc"}, false},
		{"conflicting backend: the host booted composefs", true, SourceRoot{Established: true, Reason: "digest_match", Backend: "composefs"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, err := filepath.Abs(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			podmanLog := filepath.Join(dir, "podman.argv")
			bootcLog := filepath.Join(dir, "bootc.argv")
			for name, log := range map[string]string{"podman": podmanLog, "bootc": bootcLog} {
				script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + log + "\n"
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			oldHas, oldSpace, oldExport := hostHasBootcFn, storageSpaceConstrainedFn, SkopeoExportOCIFn
			t.Cleanup(func() { hostHasBootcFn, storageSpaceConstrainedFn, SkopeoExportOCIFn = oldHas, oldSpace, oldExport })
			hostHasBootcFn = func() bool { return c.hostBootc }
			storageSpaceConstrainedFn = func() bool { return false }
			SkopeoExportOCIFn = func(string, string, string) error { return nil }

			opts := Options{SourceImgref: "containers-storage:" + source, TargetImgref: update,
				Target: filepath.Join(dir, "target"), ScratchDir: filepath.Join(dir, "scratch"), SourceRoot: c.root}
			if err := BootcInstall(opts); err != nil {
				t.Fatal(err)
			}
			bootcRaw, _ := os.ReadFile(bootcLog)
			podmanRaw, _ := os.ReadFile(podmanLog)
			bootcArgs, podmanArgs := "\n"+string(bootcRaw), "\n"+string(podmanRaw)
			if c.direct {
				if len(podmanRaw) != 0 {
					t.Fatalf("podman ran on the direct path:\n%s", podmanRaw)
				}
				for _, want := range []string{"\ninstall\nto-filesystem\n", "\n--source-imgref\ncontainers-storage:" + source + "\n",
					"\n--target-imgref\n" + update + "\n"} {
					if !strings.Contains(bootcArgs, want) {
						t.Errorf("host bootc argv lacks %q:\n%s", strings.TrimSpace(want), bootcRaw)
					}
				}
				return
			}
			if len(bootcRaw) != 0 {
				t.Fatalf("the host's bootc ran without an established source root:\n%s", bootcRaw)
			}
			for _, want := range []string{"\nrun\n", "\nbootc\ninstall\nto-filesystem\n", "\n--target-imgref\n" + update + "\n"} {
				if !strings.Contains(podmanArgs, want) {
					t.Errorf("podman argv lacks %q:\n%s", strings.TrimSpace(want), podmanRaw)
				}
			}
			if strings.Contains(podmanArgs, "\n--target-imgref\n"+source+"\n") {
				t.Errorf("the source became the update origin:\n%s", podmanRaw)
			}
		})
	}
}
