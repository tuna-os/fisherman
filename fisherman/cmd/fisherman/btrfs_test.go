package main

import (
	"testing"

	"github.com/tuna-os/fisherman/internal/disk"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// The options the install mounts the btrfs root with, the options the retag
// remount uses (r.RootMountOptions) and the installed system's rootflags are
// one string. If they drift, the remount lands in another subvolume than the
// one bootc installed into (tuna-os/fisherman#416).
func TestBtrfsSubvolumeOptionsAgree(t *testing.T) {
	r := &recipe.Recipe{Filesystem: "btrfs", BtrfsSubvolumes: true}
	if got := r.RootMountOptions(); got != disk.BtrfsSubvolumeMountOptions {
		t.Errorf("recipe root options %q != install mount options %q", got, disk.BtrfsSubvolumeMountOptions)
	}
	opts := buildBootcInstallOptions(r, "img", "", "/mnt/t", "/var/fisherman-tmp", nil, false, 0)
	want := "rootflags=" + disk.BtrfsSubvolumeMountOptions
	found := false
	for _, a := range opts.KernelArguments {
		if a == want {
			found = true
		}
	}
	if !found {
		t.Errorf("kernel arguments %v lack %q", opts.KernelArguments, want)
	}
	// Without subvolumes the remount still needs the compression option.
	if got := (&recipe.Recipe{Filesystem: "btrfs"}).RootMountOptions(); got != "compress=zstd:1" {
		t.Errorf("plain btrfs root options = %q", got)
	}
	if got := (&recipe.Recipe{Filesystem: "xfs"}).RootMountOptions(); got != "" {
		t.Errorf("xfs root options = %q, want none", got)
	}
}
