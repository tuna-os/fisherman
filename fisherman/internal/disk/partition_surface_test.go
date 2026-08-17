package disk_test

// Tests for the destructive partition surface that previously had zero
// coverage (tuna-os/fisherman#143): PartitionZFS (sfdisk script construction),
// FindSystemdBootPartitions (lsblk JSON parsing) and the RescanPartitions /
// loopRescan re-attach path.
//
// Two established patterns are used:
//   - the shared runner recorder from partition_test.go for runner.Run /
//     runner.RunWithStdin invocations (sfdisk, partprobe, udevadm, …)
//   - the fake-binary-on-PATH pattern from internal/install (bootc_test.go)
//     for direct exec.Command invocations (lsblk, losetup)

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
)

// emptyMountsFile points procMountsPath at an empty file so unmountAll finds
// no mounts and never tries real udisksctl/umount/fuser invocations.
func emptyMountsFile(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "mounts")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	disk.SetProcMountsPath(f.Name())
	t.Cleanup(func() { disk.SetProcMountsPath("/proc/mounts") })
}

// fakeBinOnPATH installs a shell script named bin under a fresh temp dir and
// prepends that dir to PATH (restored via t.Cleanup).
func fakeBinOnPATH(t *testing.T, bin, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake %s: %v", bin, err)
	}
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+":"+oldPath); err != nil {
		t.Fatalf("setting PATH: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
}

// findCalls returns all recorded calls for the given command name.
func findCalls(rec *recorder, name string) []execCall {
	var out []execCall
	for _, c := range rec.calls {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func anyCall(calls []execCall, name, arg string) bool {
	for _, c := range calls {
		if c.name == name && containsArg(c.args, arg) {
			return true
		}
	}
	return false
}

// ── PartitionZFS ──────────────────────────────────────────────────────────

// TestPartitionZFS_Script is a regression test for the ZFS partition layout:
// the sfdisk script must create a 1 GiB EFI System partition and a Linux
// partition for the ZFS pool, with --wipe=always, and must NOT fall into the
// --force --no-reread / partprobe path on a clean first attempt.
func TestPartitionZFS_Script(t *testing.T) {
	emptyMountsFile(t)
	rec := setupRecorder(t)

	if err := disk.PartitionZFS("/dev/sda"); err != nil {
		t.Fatalf("PartitionZFS: %v", err)
	}

	sfdiskCalls := findCalls(rec, "sfdisk")
	if len(sfdiskCalls) != 1 {
		t.Fatalf("expected exactly 1 sfdisk call, got %d (%+v)", len(sfdiskCalls), rec.calls)
	}
	call := sfdiskCalls[0]

	// --wipe=always and the target disk; no --force on the clean path.
	if !containsArg(call.args, "--wipe=always") {
		t.Errorf("sfdisk args missing --wipe=always: %v", call.args)
	}
	if containsArg(call.args, "--force") {
		t.Errorf("clean sfdisk path must not use --force: %v", call.args)
	}
	if last := call.args[len(call.args)-1]; last != "/dev/sda" {
		t.Errorf("sfdisk target = %q, want /dev/sda (args %v)", last, call.args)
	}

	// The exact GPT script: label gpt, 1 GiB uefi, zfs-pool linux.
	for _, want := range []string{
		"label: gpt",
		`size=1GiB, type=uefi, name="EFI-SYSTEM"`,
		`type=linux, name="zfs-pool"`,
	} {
		if !strings.Contains(call.stdin, want) {
			t.Errorf("sfdisk stdin missing %q:\n%s", want, call.stdin)
		}
	}
	// ZFS layout must NOT contain the grub /boot partition or a 2 GiB ESP.
	if strings.Contains(call.stdin, `name="boot"`) {
		t.Errorf("ZFS script must not contain grub /boot partition:\n%s", call.stdin)
	}
	if strings.Contains(call.stdin, "size=2GiB") {
		t.Errorf("ZFS script must use 1 GiB ESP, not 2 GiB:\n%s", call.stdin)
	}

	// Clean path must not invoke partprobe (no --no-reread fallback).
	if n := len(findCalls(rec, "partprobe")); n != 0 {
		t.Errorf("expected no partprobe on clean sfdisk path, got %d calls", n)
	}
	// A settle must still run after writing the table.
	if n := len(findCalls(rec, "udevadm")); n == 0 {
		t.Error("expected at least one udevadm settle after sfdisk")
	}
}

// ── FindSystemdBootPartitions ─────────────────────────────────────────────

const lsblkSDBootJSON = `{
  "blockdevices": [
    {
      "name": "/dev/sda",
      "parttype": "",
      "children": [
        { "name": "/dev/sda1", "parttype": "21686148-6449-6e6f-744e-656564454649" },
        { "name": "/dev/sda2", "parttype": "c12a7328-f81f-11d2-ba4b-00a0c93ec93b" },
        { "name": "/dev/sda3", "parttype": "4f68bce3-e8cd-4db1-96e7-fbcaf984b709" }
      ]
    }
  ]
}`

func TestFindSystemdBootPartitions(t *testing.T) {
	fakeBinOnPATH(t, "lsblk", "#!/bin/sh\ncat <<'EOF'\n"+lsblkSDBootJSON+"\nEOF\n")

	efi, root, err := disk.FindSystemdBootPartitions("/dev/sda")
	if err != nil {
		t.Fatalf("FindSystemdBootPartitions: %v", err)
	}
	if efi != "/dev/sda2" {
		t.Errorf("EFI partition = %q, want /dev/sda2", efi)
	}
	if root != "/dev/sda3" {
		t.Errorf("root partition = %q, want /dev/sda3", root)
	}
}

func TestFindSystemdBootPartitions_MalformedJSON(t *testing.T) {
	fakeBinOnPATH(t, "lsblk", "#!/bin/sh\necho 'not json'\n")

	_, _, err := disk.FindSystemdBootPartitions("/dev/sda")
	if err == nil {
		t.Fatal("expected error on malformed lsblk JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing lsblk output") {
		t.Errorf("error = %v, want 'parsing lsblk output'", err)
	}
}

func TestFindSystemdBootPartitions_MissingPartitions(t *testing.T) {
	// Only an EFI partition — no root → must error rather than return a
	// half-baked layout that later installs to the wrong device.
	fakeBinOnPATH(t, "lsblk", "#!/bin/sh\ncat <<'EOF'\n{\"blockdevices\":[{\"name\":\"/dev/sda\",\"children\":[{\"name\":\"/dev/sda2\",\"parttype\":\"c12a7328-f81f-11d2-ba4b-00a0c93ec93b\"}]}]}\nEOF\n")

	_, _, err := disk.FindSystemdBootPartitions("/dev/sda")
	if err == nil {
		t.Fatal("expected error when root partition missing, got nil")
	}
	if !strings.Contains(err.Error(), "could not identify EFI and root") {
		t.Errorf("error = %v, want 'could not identify EFI and root'", err)
	}
}

func TestFindSystemdBootPartitions_LSBLKError(t *testing.T) {
	fakeBinOnPATH(t, "lsblk", "#!/bin/sh\necho 'lsblk failed' >&2\nexit 1\n")

	_, _, err := disk.FindSystemdBootPartitions("/dev/sda")
	if err == nil {
		t.Fatal("expected error when lsblk fails, got nil")
	}
	if !strings.Contains(err.Error(), "lsblk:") {
		t.Errorf("error = %v, want 'lsblk:' prefix", err)
	}
}

// ── RescanPartitions ──────────────────────────────────────────────────────

func TestRescanPartitions_BlockDevice(t *testing.T) {
	rec := setupRecorder(t)

	if err := disk.RescanPartitions("/dev/sda"); err != nil {
		t.Fatalf("RescanPartitions: %v", err)
	}

	if !anyCall(rec.calls, "partprobe", "/dev/sda") {
		t.Errorf("expected partprobe /dev/sda, calls: %+v", rec.calls)
	}
	if n := len(findCalls(rec, "udevadm")); n == 0 {
		t.Error("expected udevadm settle for real block device")
	}
}

func TestRescanPartitions_LoopDeviceDetachAndReattach(t *testing.T) {
	// Verify the full losetup sequence on a loop device: query backing file →
	// detach → re-attach with --partscan. Each invocation appends its argv to
	// a shared log file, so no real loop device is touched.
	dir := t.TempDir()
	logFile := filepath.Join(dir, "losetup.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %s
if [ "$1" = "--noheadings" ]; then
  echo "/var/tmp/fisherman-rootfs.img"
fi
exit 0
`, logFile)
	fakeBinOnPATH(t, "losetup", script)
	rec := setupRecorder(t)

	if err := disk.RescanPartitions("/dev/loop9"); err != nil {
		t.Fatalf("RescanPartitions: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("losetup was never invoked: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{
		"--noheadings -O BACK-FILE /dev/loop9",
		"-d /dev/loop9",
		"-P /dev/loop9 /var/tmp/fisherman-rootfs.img",
	}
	if len(lines) != len(want) {
		t.Fatalf("expected %d losetup invocations, got %d: %q", len(want), len(lines), lines)
	}
	for i, w := range want {
		if strings.TrimSpace(lines[i]) != w {
			t.Errorf("losetup call %d = %q, want %q", i+1, lines[i], w)
		}
	}

	// After re-attach the kernel partition nodes must be forced: partprobe
	// + udevadm settle on the loop device.
	if !anyCall(rec.calls, "partprobe", "/dev/loop9") {
		t.Errorf("expected partprobe /dev/loop9 after loop reattach, calls: %+v", rec.calls)
	}
	if n := len(findCalls(rec, "udevadm")); n == 0 {
		t.Error("expected udevadm settle after loop rescan")
	}
}

func TestRescanPartitions_LoopReattachFailure(t *testing.T) {
	// Reattach failure must surface as an error, not be swallowed — a silent
	// rescan failure leaves the kernel without partition nodes.
	dir := t.TempDir()
	logFile := filepath.Join(dir, "losetup.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %s
if [ "$1" = "--noheadings" ]; then
  echo "/var/tmp/fisherman-rootfs.img"
elif [ "$1" = "-P" ]; then
  echo "loop: could not attach" >&2
  exit 1
fi
exit 0
`, logFile)
	fakeBinOnPATH(t, "losetup", script)
	setupRecorder(t)

	err := disk.RescanPartitions("/dev/loop9")
	if err == nil {
		t.Fatal("expected error when losetup reattach fails, got nil")
	}
	if !strings.Contains(err.Error(), "reattach with partscan") {
		t.Errorf("error = %v, want 'reattach with partscan'", err)
	}
}

func TestRescanPartitions_LoopQueryFailure(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "losetup.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %s
if [ "$1" = "--noheadings" ]; then
  echo "loop: cannot open device" >&2
  exit 1
fi
exit 0
`, logFile)
	fakeBinOnPATH(t, "losetup", script)
	setupRecorder(t)

	err := disk.RescanPartitions("/dev/loop9")
	if err == nil {
		t.Fatal("expected error when losetup query fails, got nil")
	}
	if !strings.Contains(err.Error(), "query backing file") {
		t.Errorf("error = %v, want 'query backing file'", err)
	}
}
