package install_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/install"
)

// TestComposeFsMountStrategy_Issue38 is a regression test for issue #38.
//
// Issue #38: When installing composefs images to btrfs targets with overlay storage
// driver, the entire scratch directory was mounted to /var/tmp. On btrfs-on-LUKS
// targets this caused the OCI cache to be invisible inside the bootc container:
//
//	"failed to invoke method OpenImage: open /var/tmp/oci-cache/index.json: no such file"
//
// The fix mounts the OCI cache at containerOCICachePath (/run/fisherman/oci-cache)
// — a dedicated path under /run that avoids /var/tmp interactions — and keeps
// --tmpfs /var/tmp for bootc's own ephemeral scratch space.
func TestComposeFsMountStrategy_Issue38(t *testing.T) {
	controlledMountCommands(t)
	tmpDir := t.TempDir()

	install.SkopeoExportOCIFn = func(image, destDir, tmpdir string) error {
		return os.MkdirAll(destDir, 0755)
	}
	t.Cleanup(func() { install.SkopeoExportOCIFn = install.DefaultSkopeoExportOCI })

	scratchDir := filepath.Join(tmpDir, "scratch")
	if err := os.MkdirAll(scratchDir, 0755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	target := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	// Capture stdout to verify the logged podman command line.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	_ = install.BootcInstall(install.Options{
		ComposeFsBackend: true,
		SourceImgref:     "containers-storage:ghcr.io/projectbluefin/dakota:latest",
		TargetImgref:     "ghcr.io/projectbluefin/dakota:latest",
		Target:           target,
		ScratchDir:       scratchDir,
		NeedsPull:        false,
	})

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint:errcheck
	output := buf.String()

	ociCacheHost := filepath.Join(scratchDir, "oci-cache")
	const containerOCICachePath = "/run/fisherman/oci-cache"

	// Composefs must bind-mount the OCI cache at the dedicated /run path, not /var/tmp.
	wantMount := ociCacheHost + ":" + containerOCICachePath + ":ro"
	if !strings.Contains(output, wantMount) {
		t.Errorf("podman command missing OCI cache bind-mount %q\ngot: %s", wantMount, output)
	}

	// --source-imgref must point to the container-side path.
	wantSourceImgref := "--source-imgref oci:" + containerOCICachePath
	if !strings.Contains(output, wantSourceImgref) {
		t.Errorf("podman command missing %q\ngot: %s", wantSourceImgref, output)
	}

	// --tmpfs /var/tmp must be present.
	if !strings.Contains(output, "--tmpfs /var/tmp") {
		t.Errorf("podman command missing '--tmpfs /var/tmp'\ngot: %s", output)
	}

	// Must NOT mount the old full scratch dir at /var/tmp.
	oldMount := scratchDir + ":/var/tmp"
	if strings.Contains(output, oldMount) {
		t.Errorf("podman command contains old broken scratch mount %q\ngot: %s", oldMount, output)
	}
}

// TestComposeFsVsStandardMountSeparation verifies composefs and standard installs
// use different mount strategies and neither panics.
func TestComposeFsVsStandardMountSeparation(t *testing.T) {
	controlledMountCommands(t)
	tmpDir := t.TempDir()

	install.SkopeoExportOCIFn = func(image, destDir, tmpdir string) error {
		return os.MkdirAll(destDir, 0755)
	}
	t.Cleanup(func() { install.SkopeoExportOCIFn = install.DefaultSkopeoExportOCI })

	scratchDir := filepath.Join(tmpDir, "scratch")
	os.MkdirAll(scratchDir, 0755) //nolint:errcheck
	target := filepath.Join(tmpDir, "target")
	os.MkdirAll(target, 0755) //nolint:errcheck

	// Capture composefs podman command.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	_ = install.BootcInstall(install.Options{
		ComposeFsBackend: true,
		SourceImgref:     "containers-storage:test:latest",
		TargetImgref:     "test:latest",
		Target:           target,
		ScratchDir:       scratchDir,
		NeedsPull:        false,
	})
	w.Close()
	os.Stdout = oldStdout
	var composefsBuf bytes.Buffer
	io.Copy(&composefsBuf, r) //nolint:errcheck
	composefsOut := composefsBuf.String()

	// Composefs must NOT use scratch:/var/tmp mount (old broken pattern).
	if strings.Contains(composefsOut, scratchDir+":/var/tmp") {
		t.Errorf("composefs path uses old scratch:/var/tmp mount: %s", composefsOut)
	}
	// Composefs must use the dedicated /run/fisherman/oci-cache path.
	if !strings.Contains(composefsOut, "/run/fisherman/oci-cache") {
		t.Errorf("composefs path missing /run/fisherman/oci-cache: %s", composefsOut)
	}

	// Standard (non-composefs) install should still use scratch:/var/tmp.
	r2, w2, _ := os.Pipe()
	os.Stdout = w2
	_ = install.BootcInstall(install.Options{
		ComposeFsBackend: false,
		SourceImgref:     "containers-storage:test:latest",
		TargetImgref:     "test:latest",
		Target:           target,
		ScratchDir:       scratchDir,
	})
	w2.Close()
	os.Stdout = oldStdout
	var standardBuf bytes.Buffer
	io.Copy(&standardBuf, r2) //nolint:errcheck
	standardOut := standardBuf.String()

	if !strings.Contains(standardOut, scratchDir+":/var/tmp") {
		t.Errorf("standard path missing scratch:/var/tmp mount: %s", standardOut)
	}
}

// TestBootcViaContainer_MountsSys is a regression test for projectbluefin/fisherman PR #2.
// Without -v /sys:/sys, efibootmgr cannot read or write UEFI variables from
// inside the bootc container, so UEFI boot entries are never updated.
func TestBootcViaContainer_MountsSys(t *testing.T) {
	controlledMountCommands(t)
	tmpDir := t.TempDir()

	install.SkopeoExportOCIFn = func(image, destDir, tmpdir string) error { return nil }
	t.Cleanup(func() { install.SkopeoExportOCIFn = install.DefaultSkopeoExportOCI })

	target := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	_ = install.BootcInstall(install.Options{
		SourceImgref: "containers-storage:ghcr.io/projectbluefin/dakota:latest",
		TargetImgref: "ghcr.io/projectbluefin/dakota:latest",
		Target:       target,
		ScratchDir:   tmpDir,
		NeedsPull:    false,
	})

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint:errcheck
	output := buf.String()

	if !strings.Contains(output, "-v /sys:/sys") {
		t.Errorf("bootcViaContainer missing '-v /sys:/sys' mount; efibootmgr cannot set UEFI variables without it\ngot: %s", output)
	}
}

// TestBootcToDiskViaContainer_MountsSys is a regression test for projectbluefin/fisherman PR #2.
// Both container-based install paths (bootcViaContainer and bootcToDiskViaContainer)
// must bind-mount /sys so that efibootmgr can write firmware UEFI entries.
func TestBootcToDiskViaContainer_MountsSys(t *testing.T) {
	controlledMountCommands(t)
	tmpDir := t.TempDir()

	install.SkopeoExportOCIFn = func(image, destDir, tmpdir string) error { return nil }
	t.Cleanup(func() { install.SkopeoExportOCIFn = install.DefaultSkopeoExportOCI })

	// BootcToDisk with SourceImgref set routes through bootcToDiskViaContainer.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	_, _ = install.BootcToDisk(install.Options{
		SourceImgref: "containers-storage:ghcr.io/projectbluefin/dakota:latest",
		TargetImgref: "ghcr.io/projectbluefin/dakota:latest",
		ScratchDir:   tmpDir,
	}, "/dev/sda", "ext4")

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint:errcheck
	output := buf.String()

	if !strings.Contains(output, "-v /sys:/sys") {
		t.Errorf("bootcToDiskViaContainer missing '-v /sys:/sys' mount\ngot: %s", output)
	}
}

// Both boundaries are mandatory. The command factory records actual calls;
// private PATH sentinels prevent real host execution if that seam is removed.
func controlledMountCommands(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	capture := filepath.Join(dir, "capture")
	fallback := filepath.Join(dir, "unexpected-default-command")
	script := filepath.Join(dir, "owned-command")
	raw := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + capture + "\"\nexit 93\n"
	if err := os.WriteFile(script, []byte(raw), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"podman", "mount", "umount", "skopeo", "losetup", "flatpak-spawn"} {
		guard := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + fallback + "\"\nexit 94\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(guard), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	old := install.CommandFn
	install.CommandFn = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, append([]string{name}, args...)...)
	}
	t.Cleanup(func() {
		install.CommandFn = old
		if _, err := os.Stat(fallback); !os.IsNotExist(err) {
			t.Error("default host-command path reached; private sentinel prevented execution")
		}
		raw, err := os.ReadFile(capture)
		if err != nil || !strings.Contains(string(raw), "podman ") || !strings.Contains(string(raw), "-v /sys:/sys") {
			t.Errorf("actual command seam did not observe podman/sys args: %v %s", err, raw)
		}
	})
}

// Existing entrypoint tests already use owned bootc/podman executables, but
// their auxiliary mount helpers formerly fell through to real host commands.
func controlledAncillaryMountCommands(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	primary := filepath.Join(dir, "owned-ancillary")
	fallback := filepath.Join(dir, "unexpected-default-ancillary")
	for _, name := range []string{"mount", "umount", "losetup"} {
		guard := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + fallback + "\"\nexit 94\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(guard), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(dir, "owned-command")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+primary+"\"\nexit 93\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	old := install.CommandFn
	install.CommandFn = func(name string, args ...string) *exec.Cmd {
		if name == "mount" || name == "umount" || name == "losetup" {
			return exec.Command(script, append([]string{name}, args...)...)
		}
		return old(name, args...)
	}
	t.Cleanup(func() {
		install.CommandFn = old
		if _, err := os.Stat(fallback); !os.IsNotExist(err) {
			t.Error("default ancillary command reached; private sentinel prevented host mutation")
		}
	})
}
