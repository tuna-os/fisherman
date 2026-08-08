package disk

import (
	"io"
	"reflect"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
)

// formatPartition is the manual-layout counterpart to FormatRoot, and the two
// must agree on mkfs options that the deployment later depends on. This lives
// in package disk rather than disk_test because formatPartition is unexported.
func TestFormatPartition(t *testing.T) {
	tests := []struct {
		name     string
		fstype   string
		wantName string
		wantArgs []string
		wantErr  bool
	}{
		{
			name:     "fat32",
			fstype:   "fat32",
			wantName: "mkfs.fat",
			wantArgs: []string{"-F32", "/dev/sda1"},
		},
		{
			// -O verity must match FormatRoot: composefs deployments call
			// FS_IOC_ENABLE_VERITY, which ext4 rejects unless the feature was
			// set at format time. Dropping it fails the install after the
			// partition is already formatted.
			name:     "ext4 enables verity for composefs",
			fstype:   "ext4",
			wantName: "mkfs.ext4",
			wantArgs: []string{"-F", "-O", "verity", "/dev/sda1"},
		},
		{
			name:     "ext3",
			fstype:   "ext3",
			wantName: "mkfs.ext3",
			wantArgs: []string{"-F", "/dev/sda1"},
		},
		{
			name:     "xfs",
			fstype:   "xfs",
			wantName: "mkfs.xfs",
			wantArgs: []string{"-f", "/dev/sda1"},
		},
		{
			name:     "btrfs",
			fstype:   "btrfs",
			wantName: "mkfs.btrfs",
			wantArgs: []string{"-f", "/dev/sda1"},
		},
		{
			name:    "unsupported",
			fstype:  "reiserfs",
			wantErr: true,
		},
		{
			name:    "unsupported empty",
			fstype:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotName string
			var gotArgs []string
			calls := 0
			runner.RunFn = func(_ io.Reader, name string, args ...string) error {
				calls++
				gotName, gotArgs = name, args
				return nil
			}
			t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

			err := formatPartition("/dev/sda1", tt.fstype)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("formatPartition(%q) = nil, want error", tt.fstype)
				}
				if calls != 0 {
					t.Errorf("formatPartition(%q) ran %d command(s); an unsupported fstype must not touch the disk", tt.fstype, calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("formatPartition(%q): %v", tt.fstype, err)
			}
			if calls != 1 {
				t.Fatalf("formatPartition(%q) ran %d commands, want 1", tt.fstype, calls)
			}
			if gotName != tt.wantName {
				t.Errorf("command = %q, want %q", gotName, tt.wantName)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("args = %q, want %q", gotArgs, tt.wantArgs)
			}
		})
	}
}
