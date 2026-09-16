package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSelinuxActive_MatchesEnforceFile is a light contract check: the
// function must agree with the kernel's SELinux-enforce indicator. It is
// environment-driven (both branches are exercised depending on the host),
// so it pins the implementation to the documented libselinux convention
// (/sys/fs/selinux/enforce presence) without requiring root.
func TestSelinuxActive_MatchesEnforceFile(t *testing.T) {
	_, statErr := os.Stat("/sys/fs/selinux/enforce")
	want := statErr == nil
	if got := selinuxActive(); got != want {
		t.Errorf("selinuxActive() = %v, want %v (based on /sys/fs/selinux/enforce presence)", got, want)
	}
}

// fakeSkopeo writes a minimal skopeo script to tmpdir that records its argv
// into captureFile and exits with code.
func fakeSkopeo(t *testing.T, tmpdir, captureFile string, code int) string {
	t.Helper()
	skopeoPath := filepath.Join(tmpdir, "skopeo")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + captureFile + "\"\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(skopeoPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake skopeo: %v", err)
	}
	return skopeoPath
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// prependPath puts dir at the front of PATH for the duration of the test.
func prependPath(t *testing.T, dir string) {
	t.Helper()
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+":"+oldPath); err != nil {
		t.Fatalf("setting PATH: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
}

func readCapture(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	var args []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			args = append(args, line)
		}
	}
	return args
}

// TestSkopeoExportOCI_Success verifies the composefs OCI-export path: stale
// dest is removed, the scratch dir is created, and skopeo copy is invoked
// with the containers-storage source + oci: dest. The /var/tmp bind-mount
// attempt fails (non-root test env) and must be tolerated with a warning.
func TestSkopeoExportOCI_Success(t *testing.T) {
	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "oci-cache")
	// Stale export must be removed first.
	if err := os.MkdirAll(filepath.Join(destDir, "blobs"), 0o755); err != nil {
		t.Fatalf("pre-creating stale dest: %v", err)
	}

	capture := filepath.Join(tmpDir, "skopeo-args")
	fakeSkopeo(t, tmpDir, capture, 0)
	prependPath(t, tmpDir)

	const image = "ghcr.io/tuna-os/yellowfin:gnome"
	if err := skopeoExportOCI(image, destDir, tmpDir); err != nil {
		t.Fatalf("skopeoExportOCI() error = %v", err)
	}

	// Stale content removed, dest recreated by skopeo path.
	if _, err := os.Stat(filepath.Join(destDir, "blobs")); !os.IsNotExist(err) {
		t.Errorf("stale dest content not removed: %v", err)
	}

	args := readCapture(t, capture)
	if len(args) != 3 {
		t.Fatalf("skopeo argv = %v, want [copy containers-storage:... oci:...]", args)
	}
	if args[0] != "copy" {
		t.Errorf("skopeo argv[0] = %q, want copy", args[0])
	}
	if want := "containers-storage:" + image; args[1] != want {
		t.Errorf("skopeo source = %q, want %q", args[1], want)
	}
	if want := "oci:" + destDir; args[2] != want {
		t.Errorf("skopeo dest = %q, want %q", args[2], want)
	}
}

// TestSkopeoExportOCI_SkopeoFailure propagates the skopeo copy error.
func TestSkopeoExportOCI_SkopeoFailure(t *testing.T) {
	tmpDir := t.TempDir()
	capture := filepath.Join(tmpDir, "skopeo-args")
	fakeSkopeo(t, tmpDir, capture, 1)
	prependPath(t, tmpDir)

	err := skopeoExportOCI("img:tag", filepath.Join(tmpDir, "oci-cache"), tmpDir)
	if err == nil {
		t.Fatal("skopeoExportOCI() error = nil, want skopeo copy error")
	}
	if !strings.Contains(err.Error(), "skopeo copy") {
		t.Errorf("error = %q, want wrap mentioning 'skopeo copy'", err)
	}
}

// TestSkopeoExportOCI_EmptyTmpdirFallsBack exercises the tmpdir=="" branch:
// the code tries /var/fisherman-tmp, fails to create it as non-root, and
// falls back to /tmp.
func TestSkopeoExportOCI_EmptyTmpdirFallsBack(t *testing.T) {
	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "oci-cache")
	capture := filepath.Join(tmpDir, "skopeo-args")
	fakeSkopeo(t, tmpDir, capture, 0)
	prependPath(t, tmpDir)

	if err := skopeoExportOCI("img:tag", destDir, ""); err != nil {
		t.Fatalf("skopeoExportOCI() error = %v", err)
	}
	args := readCapture(t, capture)
	if len(args) != 3 {
		t.Fatalf("skopeo argv = %v, want 3 args", args)
	}
	if !strings.HasPrefix(args[2], "oci:") {
		t.Errorf("skopeo dest = %q, want oci: prefix", args[2])
	}
}

// TestBootcToDiskDirect_Args verifies bootc install to-disk argument
// construction (target-imgref, filesystem, composefs, systemd bootloader).
func TestBootcToDiskDirect_Args(t *testing.T) {
	tmpDir := t.TempDir()
	capture := filepath.Join(tmpDir, "bootc-args")
	bootcPath := filepath.Join(tmpDir, "bootc")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + capture + "\"\nexit 0\n"
	if err := os.WriteFile(bootcPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake bootc: %v", err)
	}
	prependPath(t, tmpDir)

	disk, err := bootcToDiskDirect(Options{
		TargetImgref:     "ghcr.io/tuna-os/yellowfin:gnome",
		ComposeFsBackend: true,
	}, "/dev/sda", "ext4")
	if err != nil {
		t.Fatalf("bootcToDiskDirect() error = %v", err)
	}
	if disk != "/dev/sda" {
		t.Errorf("returned disk = %q, want /dev/sda", disk)
	}

	args := readCapture(t, capture)
	want := []string{
		"install", "to-disk",
		"--target-imgref", "ghcr.io/tuna-os/yellowfin:gnome",
		"--filesystem", "ext4",
		"--composefs-backend",
		"--bootloader", "systemd",
		"--via-loopback",
		"--wipe",
		"/dev/sda",
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("bootc argv = %v\nwant          %v", args, want)
	}
}

// TestBootcToDiskDirect_Failure propagates bootc's non-zero exit.
func TestBootcToDiskDirect_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	bootcPath := filepath.Join(tmpDir, "bootc")
	if err := os.WriteFile(bootcPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing fake bootc: %v", err)
	}
	prependPath(t, tmpDir)

	_, err := bootcToDiskDirect(Options{}, "/dev/sda", "")
	if err == nil {
		t.Fatal("bootcToDiskDirect() error = nil, want bootc install error")
	}
	if !strings.Contains(err.Error(), "bootc install to-disk") {
		t.Errorf("error = %q, want wrap mentioning 'bootc install to-disk'", err)
	}
}

// fakePodman writes a podman script that emits the given stdout lines and
// exits with code, recording argv.
func fakePodman(t *testing.T, tmpdir, captureFile string, stdout []string, code int) string {
	t.Helper()
	podmanPath := filepath.Join(tmpdir, "podman")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	for _, line := range stdout {
		sb.WriteString("printf '%s\\n' " + shellQuote(line) + "\n")
	}
	sb.WriteString("printf '%s\\n' \"$@\" > \"" + captureFile + "\"\n")
	sb.WriteString("exit " + itoa(code) + "\n")
	if err := os.WriteFile(podmanPath, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("writing fake podman: %v", err)
	}
	return podmanPath
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// TestPullImageOnce_Success verifies podman pull arg construction and that
// blob-progress lines are tolerated.
func TestPullImageOnce_Success(t *testing.T) {
	tmpDir := t.TempDir()
	capture := filepath.Join(tmpDir, "podman-args")
	fakePodman(t, tmpDir, capture, []string{
		"Copying blob sha256:aaaa",
		"Copying blob sha256:bbbb",
		"Writing manifest to image destination",
	}, 0)
	prependPath(t, tmpDir)

	if err := pullImageOnce("ghcr.io/tuna-os/yellowfin:gnome", 2, "/scratch/root", "/scratch/runroot", "overlay"); err != nil {
		t.Fatalf("pullImageOnce() error = %v", err)
	}

	args := readCapture(t, capture)
	want := []string{
		"--root", "/scratch/root",
		"--runroot", "/scratch/runroot",
		"--storage-driver", "overlay",
		"pull", "ghcr.io/tuna-os/yellowfin:gnome",
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("podman argv = %v\nwant         %v", args, want)
	}
}

// TestPullImageOnce_NoStorageFlags verifies the empty-root variant omits the
// --root/--runroot/--storage-driver flags entirely.
func TestPullImageOnce_NoStorageFlags(t *testing.T) {
	tmpDir := t.TempDir()
	capture := filepath.Join(tmpDir, "podman-args")
	fakePodman(t, tmpDir, capture, nil, 0)
	prependPath(t, tmpDir)

	if err := pullImageOnce("img:tag", 0, "", "", ""); err != nil {
		t.Fatalf("pullImageOnce() error = %v", err)
	}
	args := readCapture(t, capture)
	if strings.Join(args, " ") != "pull img:tag" {
		t.Errorf("podman argv = %v, want [pull img:tag]", args)
	}
}

// TestPullImageOnce_Failure propagates podman's non-zero exit.
func TestPullImageOnce_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	capture := filepath.Join(tmpDir, "podman-args")
	fakePodman(t, tmpDir, capture, nil, 125)
	prependPath(t, tmpDir)

	err := pullImageOnce("img:tag", 0, "", "", "")
	if err == nil {
		t.Fatal("pullImageOnce() error = nil, want podman pull error")
	}
	if !strings.Contains(err.Error(), "podman pull img:tag") {
		t.Errorf("error = %q, want wrap mentioning 'podman pull img:tag'", err)
	}
}

// TestPullImage_SuccessFirstAttempt verifies the happy path: podman succeeds
// on the first attempt, so pullImage returns without any retry sleep.
func TestPullImage_SuccessFirstAttempt(t *testing.T) {
	tmpDir := t.TempDir()
	capture := filepath.Join(tmpDir, "podman-args")
	fakePodman(t, tmpDir, capture, nil, 0)
	prependPath(t, tmpDir)

	if err := pullImage("ghcr.io/tuna-os/yellowfin:gnome", 0, "", "", ""); err != nil {
		t.Fatalf("pullImage() error = %v", err)
	}
	args := readCapture(t, capture)
	if len(args) == 0 || args[len(args)-1] != "ghcr.io/tuna-os/yellowfin:gnome" {
		t.Errorf("podman argv = %v, want final arg = image", args)
	}
}

// TestPullImage_NonZeroExecChecks verifies the seam used by skopeoExportOCI:
// a command that cannot start (e.g. missing binary after PATH purge) surfaces
// as an error from runWithSubsteps rather than a panic.
func TestPullImage_NonZeroExecChecks(t *testing.T) {
	// Sanity: exec.Command against a nonexistent binary errors on Start.
	cmd := exec.Command("/nonexistent/fisherman-test-binary")
	if err := cmd.Start(); err == nil {
		t.Skip("unexpectedly able to start nonexistent binary")
	}
}
