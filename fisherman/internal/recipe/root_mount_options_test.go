package recipe_test

import (
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

func TestRootMountOptions(t *testing.T) {
	tests := []struct {
		name string
		r    recipe.Recipe
		want string
	}{
		{
			name: "automatic btrfs",
			r:    recipe.Recipe{Filesystem: "btrfs"},
			want: "compress=zstd:1",
		},
		{
			name: "automatic btrfs preserves root subvolume",
			r:    recipe.Recipe{Filesystem: "btrfs", BtrfsSubvolumes: true},
			want: "subvol=@,compress=zstd:1",
		},
		{
			name: "automatic xfs is unchanged",
			r:    recipe.Recipe{Filesystem: "xfs"},
		},
		{
			name: "manual btrfs ignores automatic filesystem and subvolumes",
			r: recipe.Recipe{
				Filesystem:      "xfs",
				BtrfsSubvolumes: true,
				CustomMounts: []recipe.CustomMount{
					{Partition: "/dev/example1", Target: "/boot/efi", Fstype: "fat32"},
					{Partition: "/dev/example2", Target: "/", Fstype: "btrfs"},
				},
			},
			want: "compress=zstd:1",
		},
		{
			name: "manual ext4 root does not inherit btrfs data partition options",
			r: recipe.Recipe{
				Filesystem:      "btrfs",
				BtrfsSubvolumes: true,
				CustomMounts: []recipe.CustomMount{
					{Partition: "/dev/example1", Target: "/home", Fstype: "btrfs"},
					{Partition: "/dev/example2", Target: "/", Fstype: "ext4"},
				},
			},
		},
		{
			name: "manual existing root is not reconfigured",
			r: recipe.Recipe{
				Filesystem: "btrfs",
				CustomMounts: []recipe.CustomMount{
					{Partition: "/dev/example1", Target: "/", Fstype: "unformatted"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.RootMountOptions(); got != tt.want {
				t.Fatalf("root mount options = %q, want %q", got, tt.want)
			}
		})
	}
}
