// Package deploytest builds sysroot fixtures shaped like what `bootc install
// to-filesystem` leaves on a mounted target, so post-install tests run
// against the real directory layout and not a flat temp dir.
package deploytest

import (
	"os"
	"path/filepath"
	"testing"
)

// OstreeChecksum and ComposeFsVerity name the fixture deployments.
const (
	OstreeChecksum  = "8f3c1d7a2b6e4f90a1c5d3e7b9f20468ace13579bdf02468ace13579bdf02468"
	ComposeFsVerity = "5b1e0c7d9a3f4e62b8d0c1a7f3e59b24d6c8a0e2f4b6d8a0c2e4f6a8b0d2c4e6f8a1c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1c3"
)

// Sysroot is a fixture target.
type Sysroot struct {
	// Path is the mounted target (the physical sysroot).
	Path string
	// Root, Etc and Var are where the booted system's trees are.
	Root, Etc, Var string
}

func mkdirs(t testing.TB, base string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if err := os.MkdirAll(filepath.Join(base, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Ostree builds an ostree-backend target (GRUB layout): the physical
// sysroot has boot/ and ostree/ and nothing else a booted system reads.
//
//	<path>/boot/loader/entries/ostree-1.conf
//	<path>/ostree/repo/
//	<path>/ostree/deploy/default/var/{home,lib}
//	<path>/ostree/deploy/default/deploy/<csum>.0/{etc,usr,var,home->var/home}
//	<path>/ostree/deploy/default/deploy/<csum>.0.origin
func Ostree(t testing.TB) Sysroot {
	t.Helper()
	path := t.TempDir()
	stateroot := filepath.Join(path, "ostree", "deploy", "default")
	root := filepath.Join(stateroot, "deploy", OstreeChecksum+".0")
	mkdirs(t, path, "boot/efi", "ostree/repo/objects")
	mkdirs(t, stateroot, "var/home", "var/lib")
	mkdirs(t, root, "etc/systemd/system", "usr/bin", "usr/lib/systemd/system", "usr/share", "var", "sysroot")
	write(t, root+".origin", "[origin]\ncontainer-image-reference=ostree-unverified-image:docker://example.test/os:latest\n")
	write(t, filepath.Join(root, "etc", "passwd"), "root:x:0:0:root:/root:/bin/bash\n")
	write(t, filepath.Join(root, "etc", "group"), "root:x:0:\nwheel:x:10:\n")
	write(t, filepath.Join(root, "usr", "lib", "os-release"), "ID=fixture\n")
	if err := os.Symlink("var/home", filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "boot", "loader", "entries", "ostree-1.conf"),
		"title Fixture\nversion 1\noptions root=UUID=0000 rw ostree=/ostree/boot.1/default/"+OstreeChecksum+"/0\nlinux /boot/ostree/default-x/vmlinuz\n")
	return Sysroot{Path: path, Root: root, Etc: filepath.Join(root, "etc"), Var: filepath.Join(stateroot, "var")}
}

// ComposeFs builds a composefs-native target (systemd-boot layout): there
// is no /usr on the mounted target, only the deployment's etc and the
// shared state.
//
//	<path>/boot/efi/loader/entries/bootc.conf      (composefs=<verity>)
//	<path>/composefs/{images,objects}/
//	<path>/ostree/                                 (the backend creates it)
//	<path>/state/deploy/<verity>/etc/
//	<path>/state/deploy/<verity>/<verity>.origin
//	<path>/state/os/default/var/
func ComposeFs(t testing.TB) Sysroot {
	t.Helper()
	path := t.TempDir()
	root := filepath.Join(path, "state", "deploy", ComposeFsVerity)
	mkdirs(t, path, "composefs/images", "composefs/objects", "ostree", "boot/efi/EFI/Linux")
	mkdirs(t, root, "etc/systemd/system")
	mkdirs(t, path, "state/os/default/var/home", "state/os/default/var/lib")
	write(t, filepath.Join(root, ComposeFsVerity+".origin"), "[origin]\ncontainer-image-reference=example.test/os:latest\n")
	write(t, filepath.Join(root, "etc", "passwd"), "root:x:0:0:root:/root:/bin/bash\n")
	write(t, filepath.Join(root, "etc", "group"), "root:x:0:\nwheel:x:10:\n")
	write(t, filepath.Join(path, "boot", "efi", "loader", "entries", "bootc.conf"),
		"title Fixture\noptions composefs="+ComposeFsVerity+" rw\nlinux /EFI/Linux/vmlinuz\n")
	return Sysroot{Path: path, Root: root, Etc: filepath.Join(root, "etc"),
		Var: filepath.Join(path, "state", "os", "default", "var")}
}

// Strays lists the paths under the physical sysroot that a correct
// post-install step never creates: they are what the pre-resolver code
// wrote, and no booted system reads them.
func Strays(s Sysroot) []string {
	var found []string
	for _, rel := range []string{"etc", "var", "usr", "home"} {
		if _, err := os.Lstat(filepath.Join(s.Path, rel)); err == nil {
			found = append(found, rel)
		}
	}
	return found
}
