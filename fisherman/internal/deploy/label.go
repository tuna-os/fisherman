package deploy

import (
	"syscall"
	"unsafe"
)

const selinuxXattr = "security.selinux"

// InheritLabel gives path the SELinux label of its parent directory, which
// is what the kernel does for a new file under a loaded policy. A file
// fisherman creates in the target from a host with no SELinux (or another
// policy) has no label, and boots as unlabeled_t: a confined service of the
// installed system may then not read it.
//
// It is best-effort and silent: a target with no labels, a filesystem with
// no xattrs and a host that refuses the write all leave path as it was.
func InheritLabel(path string) {
	parent := path
	for len(parent) > 1 && parent[len(parent)-1] == '/' {
		parent = parent[:len(parent)-1]
	}
	i := len(parent) - 1
	for i >= 0 && parent[i] != '/' {
		i--
	}
	if i <= 0 {
		return
	}
	parent = parent[:i]

	buf := make([]byte, 256)
	n, err := syscall.Getxattr(parent, selinuxXattr, buf)
	if err != nil || n <= 0 {
		return
	}
	_ = lsetxattr(path, selinuxXattr, buf[:n])
}

// lsetxattr sets an extended attribute without following a final symlink.
func lsetxattr(path, attr string, data []byte) error {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	a, err := syscall.BytePtrFromString(attr)
	if err != nil {
		return err
	}
	var d unsafe.Pointer
	if len(data) > 0 {
		d = unsafe.Pointer(&data[0])
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_LSETXATTR,
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(a)), uintptr(d), uintptr(len(data)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
