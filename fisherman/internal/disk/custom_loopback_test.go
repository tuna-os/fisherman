package disk_test

// Integration test for the customMounts (manual layout) install path.
// It builds a real 3-partition GPT disk on a loopback device, seeds canary
// files on the partitions the recipe marks "unformatted", and drives
// disk.ApplyCustomLayout against those real block devices.
//
// Like the loopback tests in internal/install, it needs root and real mkfs
// tooling, so it skips automatically when either is missing. CI runs it via
// `sudo go test` (bootcrew-vm.yml's unit-tests job and
// .github/workflows/custom-mounts-e2e.yml).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
)

const (
	espCanaryFile  = "vendorfw/apple_fw.bin"
	espCanaryData  = "PRESERVED_VENDOR_FIRMWARE_CANARY"
	dataCanaryFile = "user_data.txt"
	dataCanaryData = "PRESERVED_USER_DATA_CANARY"

	// GPT type GUIDs used by the layout under test.
	gptESP       = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
	gptLinuxData = "0FC63DAF-8483-4772-8E79-3D69D8477DE4"
)

// run executes cmd and fails the test with its combined output on error.
func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// skipOrFail skips when the host cannot provide what this test needs (no root,
// missing mkfs tooling, no loop devices), which is the common case for a plain
// `go test` run. The dedicated E2E workflow sets FISHERMAN_LOOPBACK_E2E=require
// so a host that cannot run it fails loudly instead of reporting a green skip.
func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("FISHERMAN_LOOPBACK_E2E") == "require" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// seedCanary mounts part, writes rel with content, and unmounts again.
func seedCanary(t *testing.T, part, rel, content string) {
	t.Helper()
	dir := t.TempDir()
	run(t, "mount", part, dir)
	defer run(t, "umount", dir)

	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for canary %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing canary %s: %v", path, err)
	}
}

// loopbackLayout builds the 3-partition disk and returns its partition
// devices: an ESP (fat32), a root partition (unformatted, the install target)
// and a data partition (ext4). Teardown is registered with t.Cleanup.
func loopbackLayout(t *testing.T) (esp, root, data string) {
	t.Helper()

	if os.Getuid() != 0 {
		skipOrFail(t, "requires root: run as root or with sudo go test")
	}
	for _, tool := range []string{"losetup", "sfdisk", "mkfs.fat", "mkfs.ext4", "mkfs.xfs", "mount", "umount", "blkid"} {
		if _, err := exec.LookPath(tool); err != nil {
			skipOrFail(t, "requires %s: not found in PATH", tool)
		}
	}

	img := filepath.Join(t.TempDir(), "custom-mounts.img")
	// Sparse: only the formatted blocks are ever written. XFS needs ≥300 MiB,
	// so the root partition gets 600 MiB.
	if err := os.WriteFile(img, nil, 0o600); err != nil {
		t.Fatalf("creating disk image: %v", err)
	}
	if err := os.Truncate(img, 1536*1024*1024); err != nil {
		t.Fatalf("sizing disk image: %v", err)
	}

	// p1: ESP, p2: install target (root), p3: neighbouring data partition.
	sfdisk := exec.Command("sfdisk", img)
	sfdisk.Stdin = strings.NewReader("label: gpt\n" +
		"size=128M, type=" + gptESP + ", name=\"ESP\"\n" +
		"size=600M, type=" + gptLinuxData + ", name=\"Root\"\n" +
		"type=" + gptLinuxData + ", name=\"Data\"\n")
	if out, err := sfdisk.CombinedOutput(); err != nil {
		t.Fatalf("sfdisk: %v\n%s", err, out)
	}

	out, err := exec.Command("losetup", "--find", "--show", "--partscan", img).CombinedOutput()
	if err != nil {
		// Containers without loop-device access (and hosts with every loop
		// device busy) land here.
		skipOrFail(t, "losetup: %v\n%s", err, out)
	}
	loop := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		exec.Command("losetup", "-d", loop).Run() //nolint:errcheck // best-effort teardown
	})

	esp, root, data = loop+"p1", loop+"p2", loop+"p3"
	for _, part := range []string{esp, root, data} {
		if _, err := os.Stat(part); err != nil {
			skipOrFail(t, "kernel did not expose partition %s (no partscan support): %v", part, err)
		}
	}

	// The ESP and the data partition come pre-formatted with user content; the
	// root partition is left raw because fisherman is the one that formats it.
	run(t, "mkfs.fat", "-F32", esp)
	run(t, "mkfs.ext4", "-F", data)
	seedCanary(t, esp, espCanaryFile, espCanaryData)
	seedCanary(t, data, dataCanaryFile, dataCanaryData)

	return esp, root, data
}

// TestApplyCustomLayout_Loopback is the E2E contract for manual layouts as
// used by bootc-installer-asahi and wootc: the target root is formatted, and
// partitions marked "unformatted" are mounted with their contents intact. On
// Apple Silicon the ESP holds non-redistributable vendor firmware, so a stray
// mkfs there is unrecoverable without DFU.
func TestApplyCustomLayout_Loopback(t *testing.T) {
	esp, root, data := loopbackLayout(t)

	targetBase := filepath.Join(t.TempDir(), "target")
	specs := []disk.MountSpec{
		{Partition: root, Target: "/", Fstype: "xfs"},
		{Partition: esp, Target: "/boot/efi", Fstype: "unformatted"},
		{Partition: data, Target: "/home", Fstype: "unformatted"},
	}

	var mountedPaths []string
	t.Cleanup(func() {
		for i := len(mountedPaths) - 1; i >= 0; i-- {
			exec.Command("umount", "-R", mountedPaths[i]).Run() //nolint:errcheck // best-effort teardown
		}
	})

	targetMount, efiPart, mounted, err := disk.ApplyCustomLayout(specs, targetBase)
	mountedPaths = mounted
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}

	if targetMount != targetBase {
		t.Errorf("targetMount = %q, want %q", targetMount, targetBase)
	}
	// The caller needs efiPart for the EFI BootNext lookup.
	if efiPart != esp {
		t.Errorf("efiPart = %q, want %q", efiPart, esp)
	}
	if len(mounted) != 3 {
		t.Fatalf("mounted = %v, want 3 entries", mounted)
	}

	// The unformatted partitions must still carry their pre-existing files,
	// readable through the mounts fisherman just made.
	canaries := map[string]string{
		filepath.Join(targetBase, "boot/efi", espCanaryFile): espCanaryData,
		filepath.Join(targetBase, "home", dataCanaryFile):    dataCanaryData,
	}
	for path, want := range canaries {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("canary %s missing — an unformatted partition was reformatted: %v", path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("canary %s = %q, want %q", path, got, want)
		}
	}

	// The ESP keeps its original filesystem, and the target root gets the one
	// the recipe asked for.
	for part, want := range map[string]string{esp: "vfat", root: "xfs"} {
		got := strings.TrimSpace(run(t, "blkid", "-s", "TYPE", "-o", "value", part))
		if got != want {
			t.Errorf("blkid %s = %q, want %q", part, got, want)
		}
	}
}
