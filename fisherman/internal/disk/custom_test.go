package disk_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/runner"
)

// execCall and recorder are reused from partition_test.go in this directory.

func TestApplyCustomLayout_SortsRootFirst(t *testing.T) {
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	rec := setupRecorder(t)

	specs := []disk.MountSpec{
		{Partition: "/dev/sda2", Target: "/boot", Fstype: "ext4"},
		{Partition: "/dev/sda1", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda3", Target: "/boot/efi", Fstype: "fat32"},
	}

	targetMount, efiPart, mounted, err := disk.ApplyCustomLayout(specs, targetBase)
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}
	if targetMount != targetBase {
		t.Errorf("targetMount = %q, want %q", targetMount, targetBase)
	}
	if efiPart != "/dev/sda3" {
		t.Errorf("efiPart = %q, want /dev/sda3", efiPart)
	}
	if len(mounted) != 3 {
		t.Fatalf("mounted = %d entries, want 3: %v", len(mounted), mounted)
	}

	// Root must be formatted first, then /boot, then /boot/efi.
	// Collect only format calls (mkfs.*).
	var formatCalls []execCall
	for _, c := range rec.calls {
		if strings.HasPrefix(c.name, "mkfs.") {
			formatCalls = append(formatCalls, c)
		}
	}
	if len(formatCalls) != 3 {
		t.Fatalf("expected 3 format calls, got %d", len(formatCalls))
	}

	// The device arg is always the last one.
	assertDevice := func(c execCall, wantDevice string) {
		t.Helper()
		if got := c.args[len(c.args)-1]; got != wantDevice {
			t.Errorf("format call device = %q, want %q (args: %v)", got, wantDevice, c.args)
		}
	}
	assertDevice(formatCalls[0], "/dev/sda1") // root first
	assertDevice(formatCalls[1], "/dev/sda2") // /boot second
	assertDevice(formatCalls[2], "/dev/sda3") // /boot/efi third

	// Mount calls must also be in the right order: root mount point is targetBase,
	// /boot is targetBase+/boot, /boot/efi is targetBase+/boot/efi.
	for _, want := range []string{targetBase, targetBase + "/boot", targetBase + "/boot/efi"} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("mount point %q was not created: %v", want, err)
		}
	}
}

func TestApplyCustomLayout_PreservesUnformattedPartitions(t *testing.T) {
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	rec := setupRecorder(t)

	// An ESP marked "unformatted" must NOT be formatted — it holds the
	// bootloader and, on Apple Silicon, non-redistributable vendor firmware.
	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda2", Target: "/boot/efi", Fstype: "unformatted"},
	}

	targetMount, efiPart, mounted, err := disk.ApplyCustomLayout(specs, targetBase)
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}
	_ = targetMount
	if efiPart != "/dev/sda2" {
		t.Errorf("efiPart = %q, want /dev/sda2", efiPart)
	}
	if len(mounted) != 2 {
		t.Fatalf("mounted = %d entries, want 2: %v", len(mounted), mounted)
	}

	// There must be exactly one format call (root) — the ESP is unformatted.
	var formatCalls []execCall
	for _, c := range rec.calls {
		if strings.HasPrefix(c.name, "mkfs.") {
			formatCalls = append(formatCalls, c)
		}
	}
	if len(formatCalls) != 1 {
		t.Fatalf("expected 1 format call (root only), got %d: %+v", len(formatCalls), formatCalls)
	}
	if formatCalls[0].args[len(formatCalls[0].args)-1] != "/dev/sda1" {
		t.Errorf("the one format call should be for /dev/sda1 (root), got %v", formatCalls[0].args)
	}

	// Both partitions must be mounted.
	var mountCalls []execCall
	for _, c := range rec.calls {
		if c.name == "mount" {
			mountCalls = append(mountCalls, c)
		}
	}
	if len(mountCalls) != 2 {
		t.Fatalf("expected 2 mount calls, got %d", len(mountCalls))
	}
}

func TestApplyCustomLayout_EmptyFstypeSameAsUnformatted(t *testing.T) {
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	rec := setupRecorder(t)

	// Both "" and "unformatted" mean "mount without formatting".
	// recipe.Validate() accepts both.
	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda2", Target: "/boot/efi", Fstype: ""},
	}

	_, _, _, err := disk.ApplyCustomLayout(specs, targetBase)
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}

	var formatCalls []execCall
	for _, c := range rec.calls {
		if strings.HasPrefix(c.name, "mkfs.") {
			formatCalls = append(formatCalls, c)
		}
	}
	if len(formatCalls) != 1 {
		t.Fatalf("expected 1 format call (root only), got %d", len(formatCalls))
	}
}

func TestApplyCustomLayout_HandlesSwap(t *testing.T) {
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	rec := setupRecorder(t)

	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda3", Target: "swap", Fstype: "swap"},
		{Partition: "/dev/sda2", Target: "/boot/efi", Fstype: "fat32"},
	}

	_, _, _, err := disk.ApplyCustomLayout(specs, targetBase)
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}

	// Swap should trigger mkswap + swapon.
	var swapCalls []execCall
	for _, c := range rec.calls {
		if c.name == "mkswap" || c.name == "swapon" {
			swapCalls = append(swapCalls, c)
		}
	}
	if len(swapCalls) != 2 {
		t.Errorf("expected mkswap+swapon, got %d calls: %+v", len(swapCalls), swapCalls)
	}
	if len(swapCalls) >= 2 {
		if swapCalls[0].name != "mkswap" || swapCalls[0].args[0] != "/dev/sda3" {
			t.Errorf("mkswap call: %v", swapCalls[0])
		}
		if swapCalls[1].name != "swapon" || swapCalls[1].args[0] != "/dev/sda3" {
			t.Errorf("swapon call: %v", swapCalls[1])
		}
	}
}

func TestApplyCustomLayout_EmptySpecs(t *testing.T) {
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	_, _, _, err := disk.ApplyCustomLayout(nil, targetBase)
	if err == nil {
		t.Fatal("expected error for nil specs")
	}
	if !strings.Contains(err.Error(), "no mount specs") {
		t.Errorf("error = %q, want 'no mount specs'", err.Error())
	}

	_, _, _, err = disk.ApplyCustomLayout([]disk.MountSpec{}, targetBase)
	if err == nil {
		t.Fatal("expected error for empty specs")
	}
}

func TestApplyCustomLayout_Classification(t *testing.T) {
	// The custom path must return the correct efiPart and mount list so the
	// caller (main.go) can clean up and find BootNext. This is a regression
	// test: if efiPart comes back empty, BootNext lookup silently fails and
	// the installed system never becomes the default boot target.
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	rec := setupRecorder(t)

	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "fat32"},
		{Partition: "/dev/sda2", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda4", Target: "/home", Fstype: "ext4"},
	}

	targetMount, efiPart, mounted, err := disk.ApplyCustomLayout(specs, targetBase)
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}

	if targetMount != targetBase {
		t.Errorf("targetMount = %q, want %q", targetMount, targetBase)
	}
	if efiPart != "/dev/sda1" {
		t.Errorf("efiPart = %q, want /dev/sda1 (the caller needs this for BootNext)", efiPart)
	}

	// All three partitions must be in the mounted list.
	if len(mounted) != 3 {
		t.Fatalf("mounted = %d entries, want 3: %v", len(mounted), mounted)
	}
	// Mounted paths: root is targetBase, /boot/efi is targetBase+/boot/efi,
	// /home is targetBase+/home.
	for _, want := range []string{targetBase, targetBase + "/boot/efi", targetBase + "/home"} {
		found := false
		for _, m := range mounted {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mounted list missing %q (got %v)", want, mounted)
		}
	}
}

func TestApplyCustomLayout_AllSupportedFstypes(t *testing.T) {
	// Verify that every fstype accepted by recipe.Validate() actually
	// makes it through ApplyCustomLayout without erroring. The Validate()
	// check was added intentionally, but the format switch in formatPartition
	// must stay in sync.
	tests := []struct {
		name     string
		fstype   string
		wantMkfs string
		wantArgs []string
	}{
		{"fat32", "fat32", "mkfs.fat", []string{"-F32"}},
		{"ext3", "ext3", "mkfs.ext3", []string{"-F"}},
		{"ext4", "ext4", "mkfs.ext4", []string{"-F", "-O", "verity"}},
		{"xfs", "xfs", "mkfs.xfs", []string{"-f"}},
		{"btrfs", "btrfs", "mkfs.btrfs", []string{"-f"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			targetBase := filepath.Join(dir, "target")

			var gotName string
			var gotArgs []string
			runner.RunFn = func(_ io.Reader, name string, args ...string) error {
				gotName = name
				gotArgs = args
				return nil
			}
			t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

			// formatPartition is unexported; call it via ApplyCustomLayout.
			specs := []disk.MountSpec{
				{Partition: "/dev/sda1", Target: "/", Fstype: tt.fstype},
			}
			_, _, _, err := disk.ApplyCustomLayout(specs, targetBase)
			if err != nil {
				t.Fatalf("ApplyCustomLayout(%q): %v", tt.fstype, err)
			}

			if gotName != tt.wantMkfs {
				t.Errorf("command = %q, want %q", gotName, tt.wantMkfs)
			}
			// Device is always the last arg.
			if gotArgs[len(gotArgs)-1] != "/dev/sda1" {
				t.Errorf("device arg = %q, want /dev/sda1 (args: %v)", gotArgs[len(gotArgs)-1], gotArgs)
			}
			// Check other args.
			for i, want := range tt.wantArgs {
				if i >= len(gotArgs)-1 {
					break
				}
				if gotArgs[i] != want {
					t.Errorf("args[%d] = %q, want %q", i, gotArgs[i], want)
				}
			}
		})
	}
}

func TestApplyCustomLayout_FormatFailure(t *testing.T) {
	// A format failure must propagate and leave previously-mounted paths
	// in the returned list so Cleanup can still unmount them.
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	calls := 0
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls++
		if calls == 2 {
			// The second call is mkfs for /boot — fail it.
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda2", Target: "/boot", Fstype: "ext4"},
	}

	_, _, mounted, err := disk.ApplyCustomLayout(specs, targetBase)
	if err == nil {
		t.Fatal("expected error from format failure")
	}
	// Root was mounted before the failure; it must be in mounted so caller
	// can unmount it.
	if len(mounted) != 1 {
		t.Errorf("expected 1 mounted path (root) on failure, got %d: %v", len(mounted), mounted)
	}
	if len(mounted) > 0 && mounted[0] != targetBase {
		t.Errorf("mounted[0] = %q, want %q", mounted[0], targetBase)
	}
}

func TestApplyCustomLayout_NoRootError(t *testing.T) {
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	rec := setupRecorder(t)

	// A manual layout without "/" must fail — no swap-only or ESP-only recipes.
	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "fat32"},
		{Partition: "/dev/sda2", Target: "/boot", Fstype: "ext4"},
	}

	_, _, _, err := disk.ApplyCustomLayout(specs, targetBase)
	if err == nil {
		t.Fatal("expected error for specs without root partition")
	}
	// No mounts happened.
	if len(rec.calls) > 0 {
		t.Errorf("expected no calls when root is missing, got %d: %+v", len(rec.calls), rec.calls)
	}
}



// TestApplyCustomLayout_RealDirLayout verifies the exact directory structure
// created under targetBase matches what the caller (main.go) expects: root at
// targetBase, sub-mounts at targetBase + target.
func TestApplyCustomLayout_RealDirLayout(t *testing.T) {
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	rec := setupRecorder(t)

	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda3", Target: "/boot/efi", Fstype: "fat32"},
		{Partition: "/dev/sda4", Target: "/var", Fstype: "ext4"},
	}

	_, _, _, err := disk.ApplyCustomLayout(specs, targetBase)
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}

	// The directories must exist and be real (os.MkdirAll succeeded).
	for _, want := range []string{targetBase, targetBase + "/boot/efi", targetBase + "/var"} {
		info, err := os.Stat(want)
		if err != nil {
			t.Errorf("expected directory %q to exist: %v", want, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", want)
		}
	}

	// Mount calls must target these exact directories.
	var mountDirs []string
	for _, c := range rec.calls {
		if c.name == "mount" {
			// mount device dir  → last arg is the directory
			mountDirs = append(mountDirs, c.args[len(c.args)-1])
		}
	}
	for _, want := range []string{targetBase, targetBase + "/boot/efi", targetBase + "/var"} {
		found := false
		for _, d := range mountDirs {
			if d == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no mount call for directory %q (mount dirs: %v)", want, mountDirs)
		}
	}
}

// TestApplyCustomLayout_PreserveCanary asserts that an unformatted partition
// never sees mkfs, which is the whole point: its contents survive.
// This is the key promise whose violation destroys the Apple Silicon ESP
// (containing non-redistributable vendor firmware, unrecoverable without DFU).
func TestApplyCustomLayout_PreserveCanary(t *testing.T) {
	dir := t.TempDir()
	targetBase := filepath.Join(dir, "target")

	rec := setupRecorder(t)

	// Three partitions: root gets formatted, ESP is unformatted, neighbour is ext4.
	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "unformatted"},
		{Partition: "/dev/sda2", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda4", Target: "/neighbour", Fstype: "ext4"},
	}

	_, efiPart, _, err := disk.ApplyCustomLayout(specs, targetBase)
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}
	if efiPart != "/dev/sda1" {
		t.Errorf("efiPart = %q, want /dev/sda1", efiPart)
	}

	// The ESP (/dev/sda1) must have ZERO format calls.
	for _, c := range rec.calls {
		if strings.HasPrefix(c.name, "mkfs.") && c.args[len(c.args)-1] == "/dev/sda1" {
			t.Errorf("UNFORMATTED ESP /dev/sda1 received a format call: %s %v", c.name, c.args)
		}
	}

	// The root and neighbour must both be formatted.
	var formattedDevices []string
	for _, c := range rec.calls {
		if strings.HasPrefix(c.name, "mkfs.") {
			formattedDevices = append(formattedDevices, c.args[len(c.args)-1])
		}
	}
	if len(formattedDevices) != 2 {
		t.Errorf("expected 2 format calls (root + neighbour), got %d: %v", len(formattedDevices), formattedDevices)
	}
	if !equalSlice(formattedDevices, []string{"/dev/sda2", "/dev/sda4"}) {
		t.Errorf("formatted = %v, want [/dev/sda2 /dev/sda4] (root first)", formattedDevices)
	}
}


