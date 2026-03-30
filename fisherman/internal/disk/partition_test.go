package disk_test

import (
	"io"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/runner"
)

// execCall and recorder are shared across all disk package tests in this directory.
type execCall struct {
	name  string
	args  []string
	stdin string
}

type recorder struct {
	calls []execCall
	err   error
}

func (r *recorder) run(stdin io.Reader, name string, args ...string) error {
	s := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		s = string(b)
	}
	r.calls = append(r.calls, execCall{name: name, args: args, stdin: s})
	return r.err
}

func setupRecorder(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	runner.RunFn = rec.run
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })
	return rec
}

// ── PartSuffix / PartName ──────────────────────────────────────────────────

func TestPartSuffix(t *testing.T) {
	tests := []struct {
		disk string
		want string
	}{
		{"/dev/nvme0n1", "p"},
		{"/dev/mmcblk0", "p"},
		{"/dev/sda", ""},
		{"/dev/vda", ""},
		{"/dev/xvda", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := disk.PartSuffix(tt.disk)
		if got != tt.want {
			t.Errorf("PartSuffix(%q) = %q, want %q", tt.disk, got, tt.want)
		}
	}
}

func TestPartName(t *testing.T) {
	tests := []struct {
		disk string
		num  int
		want string
	}{
		{"/dev/nvme0n1", 1, "/dev/nvme0n1p1"},
		{"/dev/nvme0n1", 3, "/dev/nvme0n1p3"},
		{"/dev/sda", 1, "/dev/sda1"},
		{"/dev/sda", 3, "/dev/sda3"},
		{"/dev/vda", 2, "/dev/vda2"},
	}
	for _, tt := range tests {
		got := disk.PartName(tt.disk, tt.num)
		if got != tt.want {
			t.Errorf("PartName(%q, %d) = %q, want %q", tt.disk, tt.num, got, tt.want)
		}
	}
}

// ── Partition sfdisk script ────────────────────────────────────────────────

// sfdiskStdin finds the sfdisk call in rec.calls and returns its stdin content.
// Fails the test if sfdisk was not called exactly once.
func sfdiskStdin(t *testing.T, rec *recorder) string {
	t.Helper()
	var found []execCall
	for _, c := range rec.calls {
		if c.name == "sfdisk" {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 sfdisk call, got %d (all calls: %+v)", len(found), rec.calls)
	}
	return found[0].stdin
}

// TestPartition_SfdiskScript verifies that Partition() produces a 3-partition
// GPT layout: EFI (512MiB) + /boot ext4 (1GiB) + root (remaining).
//
// This is a regression test for the 2→3 partition layout change. Any attempt
// to drop the separate ext4 /boot partition will be caught here.
func TestPartition_SfdiskScript(t *testing.T) {
	rec := setupRecorder(t)

	// /dev/sda: doesn't start with "loop", so loopRescan won't be called.
	if err := disk.Partition("/dev/sda"); err != nil {
		t.Fatalf("Partition: %v", err)
	}

	script := sfdiskStdin(t, rec)

	// Must declare a GPT partition table.
	if !strings.Contains(script, "label: gpt") {
		t.Errorf("sfdisk script missing 'label: gpt':\n%s", script)
	}

	// Count non-empty, non-label lines — these are the partition lines.
	var partLines []string
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "label:") {
			continue
		}
		partLines = append(partLines, line)
	}
	if len(partLines) != 3 {
		t.Errorf("expected exactly 3 partition lines, got %d:\n%s", len(partLines), script)
	}

	// Partition 1: EFI System, 512 MiB.
	if !strings.Contains(script, "size=512MiB") || !strings.Contains(script, "type=uefi") {
		t.Errorf("EFI partition (size=512MiB, type=uefi) not found in sfdisk script:\n%s", script)
	}

	// Partition 2: Linux /boot, 1 GiB.
	// Must have an explicit size (so it doesn't consume remaining space).
	if !strings.Contains(script, "size=1GiB") || !strings.Contains(script, "type=linux") {
		t.Errorf("/boot partition (size=1GiB, type=linux) not found in sfdisk script:\n%s", script)
	}

	// Partition 3: root — must NOT have a size= field (fills remaining space).
	rootLine := partLines[2]
	if strings.Contains(rootLine, "size=") {
		t.Errorf("root partition should have no size= (fills remaining space), got: %q", rootLine)
	}
	if !strings.Contains(rootLine, "type=linux") {
		t.Errorf("root partition missing type=linux: %q", rootLine)
	}
}

// TestPartitionEncrypted_SameLayout verifies that PartitionEncrypted produces
// the identical 3-partition sfdisk script as Partition. Both encrypted and
// unencrypted installs share the same disk layout; only LUKS setup differs.
func TestPartitionEncrypted_SameLayout(t *testing.T) {
	recPlain := setupRecorder(t)
	if err := disk.Partition("/dev/sda"); err != nil {
		t.Fatalf("Partition: %v", err)
	}
	plainScript := sfdiskStdin(t, recPlain)

	// Reset recorder for the encrypted call.
	recEnc := setupRecorder(t)
	if err := disk.PartitionEncrypted("/dev/sda"); err != nil {
		t.Fatalf("PartitionEncrypted: %v", err)
	}
	encScript := sfdiskStdin(t, recEnc)

	if plainScript != encScript {
		t.Errorf("PartitionEncrypted sfdisk script differs from Partition:\nplain:\n%s\nencrypted:\n%s", plainScript, encScript)
	}
}

// TestPartition_SfdiskArgs verifies the command-line arguments passed to sfdisk.
func TestPartition_SfdiskArgs(t *testing.T) {
	rec := setupRecorder(t)
	if err := disk.Partition("/dev/sda"); err != nil {
		t.Fatalf("Partition: %v", err)
	}

	var sfdiskCall *execCall
	for i := range rec.calls {
		if rec.calls[i].name == "sfdisk" {
			sfdiskCall = &rec.calls[i]
			break
		}
	}
	if sfdiskCall == nil {
		t.Fatal("sfdisk not called")
	}
	wantArgs := []string{"--wipe=always", "/dev/sda"}
	if !equalSlice(sfdiskCall.args, wantArgs) {
		t.Errorf("sfdisk args = %v, want %v", sfdiskCall.args, wantArgs)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
