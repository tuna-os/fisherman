package deploy

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// user.* stands in for security.selinux: any user may set it, and the
// copy logic is the same.
func TestLsetxattr(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lsetxattr(f, "user.fisherman", []byte("label")); err != nil {
		t.Skipf("filesystem has no user xattrs: %v", err)
	}
	buf := make([]byte, 32)
	n, err := syscall.Getxattr(f, "user.fisherman", buf)
	if err != nil || string(buf[:n]) != "label" {
		t.Errorf("xattr = %q, %v", buf[:n], err)
	}
}

func TestInheritLabel_NoLabelIsSilent(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	InheritLabel(f)       // parent has no label (or the host refuses): no panic, no change
	InheritLabel("/")     // no parent
	InheritLabel("nodir") // relative, no parent
}
