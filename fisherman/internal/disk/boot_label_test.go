package disk

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLabelBoot_SetsLabel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "efi")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// security.selinux is usually refused on a test host; user.* proves the
	// helper writes bootLabel as the attribute value.
	const attr = "user.fisherman.test"
	labelBoot(dir, attr)
	buf := make([]byte, 128)
	n, err := syscall.Getxattr(dir, attr, buf)
	if err != nil {
		t.Skipf("filesystem has no user xattrs: %v", err)
	}
	if got := string(buf[:n]); got != bootLabel {
		t.Fatalf("label = %q, want %q", got, bootLabel)
	}
}

func TestLabelBoot_RefusedIsSilent(t *testing.T) {
	labelBoot(filepath.Join(t.TempDir(), "missing"), selinuxXattr)
}
