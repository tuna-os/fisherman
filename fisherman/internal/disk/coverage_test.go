package disk_test

// Tests for the remaining uncovered surface of internal/disk (previously 0%):
// MountType, MountTmpfs, UmountRecursive, MountBoot, FinalizeFilesystem,
// MountEFI, FormatVar, UUID (format.go), every exported zfs.go helper, and
// ApplyCustomLayout / swap activation (custom.go).
//
// The shared recorder helpers (setupRecorder, assertSingleCall, execCall,
// equalSlice, contains) live in partition_test.go.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/runner"
)

// ── MountType ─────────────────────────────────────────────────────────────

func TestMountType(t *testing.T) {
	tests := []struct {
		name     string
		dev      string
		target   string
		fstype   string
		opts     string
		wantName string
		wantArgs []string
	}{
		{
			name:     "typed xfs with opts",
			dev:      "/dev/sda3",
			target:   "/mnt/root",
			fstype:   "xfs",
			opts:     "discard",
			wantName: "mount",
			wantArgs: []string{"-t", "xfs", "-o", "discard", "/dev/sda3", "/mnt/root"},
		},
		{
			name:     "typed ext4 without opts",
			dev:      "/dev/sda3",
			target:   "/mnt/root",
			fstype:   "ext4",
			opts:     "",
			wantName: "mount",
			wantArgs: []string{"-t", "ext4", "/dev/sda3", "/mnt/root"},
		},
		{
			// Empty fstype must fall back to Mount's auto-detect (no -t).
			name:     "empty fstype falls back to auto-detect",
			dev:      "/dev/sda3",
			target:   "/mnt/root",
			fstype:   "",
			opts:     "ro",
			wantName: "mount",
			wantArgs: []string{"-o", "ro", "/dev/sda3", "/mnt/root"},
		},
		{
			// zfs has no mount(8) type; it must fall back to Mount too.
			name:     "zfs falls back to auto-detect",
			dev:      "/dev/sda3",
			target:   "/mnt/root",
			fstype:   "zfs",
			opts:     "",
			wantName: "mount",
			wantArgs: []string{"/dev/sda3", "/mnt/root"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := setupRecorder(t)
			if err := disk.MountType(tt.dev, tt.target, tt.fstype, tt.opts); err != nil {
				t.Fatalf("MountType: %v", err)
			}
			assertSingleCall(t, rec, tt.wantName, tt.wantArgs)
		})
	}
}

func TestMountType_ErrorCarriesBlkidAndDmesg(t *testing.T) {
	rec := &recorder{err: errors.New("mount: exit status 1")}
	runner.RunFn = rec.run
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		switch name {
		case "blkid":
			return []byte("/dev/sda3: UUID=\"abc-123\" TYPE=\"xfs\"\n"), nil
		case "sh":
			return []byte("[  123.456] xfs filesystem being mounted at /mnt/root supports timestamps until 2038\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() {
		runner.RunFn = runner.DefaultRun
		runner.OutputFn = runner.DefaultOutput
	})

	err := disk.MountType("/dev/sda3", "/mnt/root", "xfs", "")
	if err == nil {
		t.Fatal("MountType: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "blkid: /dev/sda3: UUID=\"abc-123\"") {
		t.Errorf("error missing blkid probe: %v", err)
	}
	if !strings.Contains(err.Error(), "dmesg: [  123.456] xfs") {
		t.Errorf("error missing dmesg tail: %v", err)
	}
	if !strings.Contains(err.Error(), "mount: exit status 1") {
		t.Errorf("error missing wrapped mount error: %v", err)
	}
}

// ── MountTmpfs ─────────────────────────────────────────────────────────────

func TestMountTmpfs(t *testing.T) {
	rec := setupRecorder(t)
	path := filepath.Join(t.TempDir(), "overlay")

	if err := disk.MountTmpfs(path, "4G"); err != nil {
		t.Fatalf("MountTmpfs: %v", err)
	}
	assertSingleCall(t, rec, "mount", []string{"-t", "tmpfs", "-o", "size=4G", "tmpfs", path})

	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Errorf("MountTmpfs did not create target dir %s (err=%v)", path, err)
	}
}

func TestMountTmpfs_MkdirFailure(t *testing.T) {
	rec := setupRecorder(t)
	// A file where the directory should go forces MkdirAll to fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "sub")

	err := disk.MountTmpfs(path, "1G")
	if err == nil {
		t.Fatal("MountTmpfs: expected mkdir error, got nil")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error should mention mkdir, got: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("MountTmpfs ran %d commands after mkdir failure; must not mount", len(rec.calls))
	}
}

// ── UmountRecursive ────────────────────────────────────────────────────────

func TestUmountRecursive(t *testing.T) {
	rec := setupRecorder(t)
	if err := disk.UmountRecursive("/mnt/root"); err != nil {
		t.Fatalf("UmountRecursive: %v", err)
	}
	assertSingleCall(t, rec, "umount", []string{"-R", "/mnt/root"})
}

// ── MountBoot / MountEFI ───────────────────────────────────────────────────

func TestMountBoot(t *testing.T) {
	rec := setupRecorder(t)
	rootMount := filepath.Join(t.TempDir(), "root")

	if err := disk.MountBoot(rootMount, "/dev/sda2"); err != nil {
		t.Fatalf("MountBoot: %v", err)
	}
	assertSingleCall(t, rec, "mount", []string{"/dev/sda2", rootMount + "/boot"})

	if fi, err := os.Stat(rootMount + "/boot"); err != nil || !fi.IsDir() {
		t.Errorf("MountBoot did not create %s/boot (err=%v)", rootMount, err)
	}
}

func TestMountEFI(t *testing.T) {
	rec := setupRecorder(t)
	rootMount := filepath.Join(t.TempDir(), "root")

	if err := disk.MountEFI(rootMount, "/dev/sda1"); err != nil {
		t.Fatalf("MountEFI: %v", err)
	}
	assertSingleCall(t, rec, "mount", []string{"/dev/sda1", rootMount + "/boot/efi"})

	if fi, err := os.Stat(rootMount + "/boot/efi"); err != nil || !fi.IsDir() {
		t.Errorf("MountEFI did not create %s/boot/efi (err=%v)", rootMount, err)
	}
}

// ── FinalizeFilesystem ─────────────────────────────────────────────────────

func TestFinalizeFilesystem(t *testing.T) {
	rec := setupRecorder(t)
	path := "/mnt/root"

	if err := disk.FinalizeFilesystem(path); err != nil {
		t.Fatalf("FinalizeFilesystem: %v", err)
	}

	want := []execCall{
		{name: "fstrim", args: []string{"--quiet-unsupported", "-v", path}},
		{name: "mount", args: []string{"-o", "remount,ro", path}},
		{name: "fsfreeze", args: []string{"-f", path}},
		{name: "fsfreeze", args: []string{"-u", path}},
	}
	if len(rec.calls) != len(want) {
		t.Fatalf("expected %d calls, got %d: %+v", len(want), len(rec.calls), rec.calls)
	}
	for i, w := range want {
		if rec.calls[i].name != w.name {
			t.Errorf("call %d name = %q, want %q", i, rec.calls[i].name, w.name)
		}
		if !equalSlice(rec.calls[i].args, w.args) {
			t.Errorf("call %d args = %v, want %v", i, rec.calls[i].args, w.args)
		}
	}
}

func TestFinalizeFilesystem_StopsOnFstrimError(t *testing.T) {
	rec := &recorder{err: errors.New("fstrim: not supported")}
	runner.RunFn = rec.run
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	err := disk.FinalizeFilesystem("/mnt/root")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "fstrim") {
		t.Errorf("error should wrap fstrim failure, got: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Errorf("expected only the fstrim call before abort, got %d calls", len(rec.calls))
	}
}

// ── FormatVar ──────────────────────────────────────────────────────────────

func TestFormatVar(t *testing.T) {
	rec := setupRecorder(t)
	if err := disk.FormatVar("/dev/sda4"); err != nil {
		t.Fatalf("FormatVar: %v", err)
	}
	assertSingleCall(t, rec, "mkfs.xfs", []string{"-f", "-L", "var", "/dev/sda4"})
}

// ── UUID ───────────────────────────────────────────────────────────────────

func TestUUID(t *testing.T) {
	orig := runner.OutputFn
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		if name != "blkid" {
			t.Errorf("UUID called %q, want blkid", name)
		}
		return []byte("9f1a-2b3c-4d5e\n"), nil
	}
	t.Cleanup(func() { runner.OutputFn = orig })

	if got := disk.UUID("/dev/sda3"); got != "9f1a-2b3c-4d5e" {
		t.Errorf("UUID = %q, want 9f1a-2b3c-4d5e", got)
	}
}

func TestUUID_ErrorReturnsEmpty(t *testing.T) {
	orig := runner.OutputFn
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		return nil, errors.New("blkid: not found")
	}
	t.Cleanup(func() { runner.OutputFn = orig })

	if got := disk.UUID("/dev/sda3"); got != "" {
		t.Errorf("UUID on error = %q, want empty (fstab write must be skipped)", got)
	}
}

// ── zfs.go ─────────────────────────────────────────────────────────────────

func TestPoolName(t *testing.T) {
	tests := []struct{ name, want string }{
		{"", "rpool"},
		{"tank", "tank"},
	}
	for _, tt := range tests {
		if got := disk.PoolName(tt.name); got != tt.want {
			t.Errorf("PoolName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCreateZFSPool(t *testing.T) {
	rec := setupRecorder(t)
	if err := disk.CreateZFSPool("rpool", "/dev/sdb", "/mnt/target"); err != nil {
		t.Fatalf("CreateZFSPool: %v", err)
	}
	assertSingleCall(t, rec, "zpool", []string{
		"create",
		"-R", "/mnt/target",
		"-O", "compression=zstd",
		"-O", "mountpoint=/",
		"-O", "acltype=posixacl",
		"-O", "xattr=sa",
		"-O", "dnodesize=auto",
		"-O", "relatime=on",
		"-o", "ashift=12",
		"-f",
		"rpool", "/dev/sdb",
	})
}

func TestCreateZFSRootDataset(t *testing.T) {
	rec := setupRecorder(t)
	if err := disk.CreateZFSRootDataset("rpool"); err != nil {
		t.Fatalf("CreateZFSRootDataset: %v", err)
	}
	assertSingleCall(t, rec, "zfs", []string{"create", "-o", "mountpoint=/", "rpool/root"})
}

func TestMountZFSRoot(t *testing.T) {
	rec := setupRecorder(t)
	altroot := filepath.Join(t.TempDir(), "target")

	if err := disk.MountZFSRoot("rpool", altroot); err != nil {
		t.Fatalf("MountZFSRoot: %v", err)
	}
	assertSingleCall(t, rec, "zfs", []string{"mount", "rpool/root"})

	if fi, err := os.Stat(altroot); err != nil || !fi.IsDir() {
		t.Errorf("MountZFSRoot did not create altroot %s (err=%v)", altroot, err)
	}
}

func TestMountZFSRoot_AlreadyMountedIsTolerated(t *testing.T) {
	rec := &recorder{err: errors.New("cannot mount 'rpool/root': dataset already mounted")}
	runner.RunFn = rec.run
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	if err := disk.MountZFSRoot("rpool", filepath.Join(t.TempDir(), "target")); err != nil {
		t.Fatalf("MountZFSRoot with 'already mounted': %v", err)
	}
}

func TestMountZFSRoot_OtherErrorPropagates(t *testing.T) {
	rec := &recorder{err: errors.New("zfs mount: pool is busy")}
	runner.RunFn = rec.run
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	err := disk.MountZFSRoot("rpool", filepath.Join(t.TempDir(), "target"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "pool is busy") {
		t.Errorf("error should carry the zfs failure, got: %v", err)
	}
}

func TestExportZFSPool(t *testing.T) {
	rec := setupRecorder(t)
	if err := disk.ExportZFSPool("rpool"); err != nil {
		t.Fatalf("ExportZFSPool: %v", err)
	}
	assertSingleCall(t, rec, "zpool", []string{"export", "rpool"})
}

func TestSetZFSCachefile(t *testing.T) {
	rec := setupRecorder(t)
	targetRoot := filepath.Join(t.TempDir(), "root")

	if err := disk.SetZFSCachefile("rpool", targetRoot); err != nil {
		t.Fatalf("SetZFSCachefile: %v", err)
	}
	cacheFile := filepath.Join(targetRoot, "etc", "zfs", "zpool.cache")
	assertSingleCall(t, rec, "zpool", []string{"set", "cachefile=" + cacheFile, "rpool"})

	if fi, err := os.Stat(filepath.Join(targetRoot, "etc", "zfs")); err != nil || !fi.IsDir() {
		t.Errorf("SetZFSCachefile did not create cache dir (err=%v)", err)
	}
}

func TestWriteHostID(t *testing.T) {
	targetRoot := filepath.Join(t.TempDir(), "root")

	if err := disk.WriteHostID(targetRoot); err != nil {
		t.Fatalf("WriteHostID: %v", err)
	}

	hostidFile := filepath.Join(targetRoot, "etc", "hostid")
	data, err := os.ReadFile(hostidFile)
	if err != nil {
		t.Fatalf("hostid not written: %v", err)
	}
	if len(data) != 4 {
		t.Errorf("hostid = %d bytes, want 4 (zgenhostid format)", len(data))
	}
}

// ── custom.go: ApplyCustomLayout ───────────────────────────────────────────

func TestApplyCustomLayout_NoSpecs(t *testing.T) {
	rec := setupRecorder(t)
	_, _, _, err := disk.ApplyCustomLayout(nil, "/mnt/target")
	if err == nil {
		t.Fatal("expected error for empty specs, got nil")
	}
	if len(rec.calls) != 0 {
		t.Errorf("ran %d commands with no specs; must not touch disk", len(rec.calls))
	}
}

func TestApplyCustomLayout_RootAndEFI(t *testing.T) {
	rec := setupRecorder(t)
	targetBase := filepath.Join(t.TempDir(), "target")

	specs := []disk.MountSpec{
		{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "fat32"},
		{Partition: "/dev/sda3", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda4", Target: "/boot", Fstype: "ext4"},
	}

	targetMount, efiPart, mounted, err := disk.ApplyCustomLayout(specs, targetBase)
	if err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}

	// "/" must be mounted at targetBase itself.
	if targetMount != targetBase {
		t.Errorf("targetMount = %q, want %q", targetMount, targetBase)
	}
	if efiPart != "/dev/sda1" {
		t.Errorf("efiPart = %q, want /dev/sda1", efiPart)
	}

	// Order matters: "/" sorts first, then "/boot", then "/boot/efi".
	wantOrder := []string{targetBase, targetBase + "/boot", targetBase + "/boot/efi"}
	if len(mounted) != len(wantOrder) {
		t.Fatalf("mounted = %v, want %v", mounted, wantOrder)
	}
	for i, w := range wantOrder {
		if mounted[i] != w {
			t.Errorf("mounted[%d] = %q, want %q", i, mounted[i], w)
		}
	}

	// Format calls: root first (xfs), then boot (ext4), then efi (fat32) in
	// sorted order.
	formats := map[string][]string{
		"mkfs.xfs":  {"-f", "/dev/sda3"},
		"mkfs.ext4": {"-F", "-O", "verity", "/dev/sda4"},
		"mkfs.fat":  {"-F32", "/dev/sda1"},
	}
	for _, c := range rec.calls {
		if wantArgs, ok := formats[c.name]; ok {
			if !equalSlice(c.args, wantArgs) {
				t.Errorf("format call %s args = %v, want %v", c.name, c.args, wantArgs)
			}
			delete(formats, c.name)
		}
	}
	if len(formats) != 0 {
		t.Errorf("missing format calls for: %v (calls: %+v)", formats, rec.calls)
	}
}

func TestApplyCustomLayout_SwapActivation(t *testing.T) {
	rec := setupRecorder(t)
	targetBase := filepath.Join(t.TempDir(), "target")

	specs := []disk.MountSpec{
		{Partition: "/dev/sda3", Target: "/", Fstype: "ext4"},
		{Partition: "/dev/sda5", Target: "swap", Fstype: "swap"},
	}

	if _, _, _, err := disk.ApplyCustomLayout(specs, targetBase); err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}

	var sawSwap, sawSwapon bool
	for _, c := range rec.calls {
		if c.name == "mkswap" {
			sawSwap = true
			if !equalSlice(c.args, []string{"/dev/sda5"}) {
				t.Errorf("mkswap args = %v, want [/dev/sda5]", c.args)
			}
		}
		if c.name == "swapon" {
			sawSwapon = true
			if !equalSlice(c.args, []string{"/dev/sda5"}) {
				t.Errorf("swapon args = %v, want [/dev/sda5]", c.args)
			}
		}
	}
	if !sawSwap || !sawSwapon {
		t.Errorf("swap activation incomplete: mkswap=%v swapon=%v (calls: %+v)", sawSwap, sawSwapon, rec.calls)
	}
}

func TestApplyCustomLayout_UnformattedKeepsPartition(t *testing.T) {
	rec := setupRecorder(t)
	targetBase := filepath.Join(t.TempDir(), "target")

	specs := []disk.MountSpec{
		{Partition: "/dev/sda3", Target: "/", Fstype: "unformatted"},
	}

	if _, _, _, err := disk.ApplyCustomLayout(specs, targetBase); err != nil {
		t.Fatalf("ApplyCustomLayout: %v", err)
	}

	for _, c := range rec.calls {
		if strings.HasPrefix(c.name, "mkfs") {
			t.Errorf("unformatted spec ran %q; must not reformat an existing filesystem", c.name)
		}
	}
}

func TestApplyCustomLayout_MountFailure(t *testing.T) {
	// mkfs.* succeeds, but the mount itself fails — the mounted list must not
	// include the target and the error must carry the mount failure.
	var calls []execCall
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls = append(calls, execCall{name: name, args: args})
		if name == "mount" {
			return errors.New("mount: no such device")
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	targetBase := filepath.Join(t.TempDir(), "target")
	specs := []disk.MountSpec{
		{Partition: "/dev/sda3", Target: "/", Fstype: "ext4"},
	}

	_, _, mounted, err := disk.ApplyCustomLayout(specs, targetBase)
	if err == nil {
		t.Fatal("expected mount error, got nil")
	}
	if !strings.Contains(err.Error(), "no such device") {
		t.Errorf("error should carry mount failure, got: %v", err)
	}
	if len(mounted) != 0 {
		t.Errorf("mounted = %v; failing mount must not register the target", mounted)
	}
}
