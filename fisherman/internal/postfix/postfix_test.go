package postfix

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	oldRoot = "aaaaaaaa-1111-2222-3333-444444444444"
	newRoot = "bbbbbbbb-1111-2222-3333-444444444444"
	oldBoot = "cccccccc-1111-2222-3333-444444444444"
	newBoot = "dddddddd-1111-2222-3333-444444444444"
	oldESP  = "1234-ABCD"
	newESP  = "5678-EF01"
	luks    = "99999999-aaaa-bbbb-cccc-dddddddddddd"
)

// copyTree duplicates a fixture tree into a temp dir for tests that mutate.
func copyTree(t *testing.T, name string) string {
	t.Helper()
	dst := t.TempDir()
	out, err := runCopy("testdata/"+name, dst)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runCopy(src, dst string) (string, error) {
	root := filepath.Join(dst, "root")
	if err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(root, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, fi.Mode().Perm())
	}); err != nil {
		return "", err
	}
	return root, nil
}

type recorded struct {
	name string
	args []string
}

type fakeRunner struct {
	calls []recorded
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, recorded{name, args})
	return f.err
}

func (f *fakeRunner) argv(name string) []string {
	for _, c := range f.calls {
		if c.name == name {
			return c.args
		}
	}
	return nil
}

// mountTree maps the copied fixture at root to a Target: the grub tree has
// a separate /boot, the systemd tree is ESP-only.
func mountTree(root, layout string) Target {
	if layout == "grub" {
		return Target{Root: root, Boot: filepath.Join(root, "boot"), EFI: filepath.Join(root, "efi")}
	}
	return Target{Root: root, EFI: filepath.Join(root, "efi")}
}

func uuidMap() map[string]string {
	return map[string]string{oldRoot: newRoot, oldBoot: newBoot, oldESP: newESP}
}

func TestApplyGrubLayout(t *testing.T) {
	root := copyTree(t, "grub")
	fake := &fakeRunner{}
	rep, err := Apply(context.Background(), Options{
		Target:   mountTree(root, "grub"),
		UUIDMap:  uuidMap(),
		LUKSUUID: luks,
		TPM2:     true,
		Disk:     "/dev/vda",
		ESPPArt:  "1",
		Grow:     true,
	}, fake.Run)
	if err != nil {
		t.Fatal(err)
	}

	entry := filepath.Join(root, "boot", "loader", "entries", "ostree-1-grub.conf")
	b, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"root=UUID=" + newRoot,
		"rd.luks.uuid=" + luks,
		"rd.luks.options=" + luks + "=tpm2-device=auto,discard",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("entry misses %q:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), oldRoot) {
		t.Errorf("entry still names the old root:\n%s", b)
	}

	// The live entry is gone, and no entry keeps the live karg.
	if _, err := os.Stat(filepath.Join(root, "boot", "loader", "entries", "utah-live.conf")); !os.IsNotExist(err) {
		t.Error("live entry was not removed")
	}
	grubenv, err := os.ReadFile(filepath.Join(root, "boot", "grub2", "grubenv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(grubenv) != 1024 || strings.Contains(string(grubenv), "saved_entry=utah-live") {
		t.Errorf("grubenv not scrubbed to 1024 bytes")
	}
	if !strings.Contains(string(grubenv), "boot_success=0") {
		t.Errorf("grubenv lost unrelated lines:\n%s", grubenv)
	}

	// GRUB configs and fstab name the fresh UUIDs.
	grubcfg, _ := os.ReadFile(filepath.Join(root, "boot", "grub2", "grub.cfg"))
	if !strings.Contains(string(grubcfg), newESP) || strings.Contains(string(grubcfg), oldESP) {
		t.Errorf("grub.cfg not rewritten:\n%s", grubcfg)
	}
	fstab, _ := os.ReadFile(filepath.Join(root, "ostree", "deploy", "fedora", "deploy", "abc123.0", "etc", "fstab"))
	for _, want := range []string{newRoot, newBoot, newESP} {
		if !strings.Contains(string(fstab), want) {
			t.Errorf("fstab misses %q:\n%s", want, fstab)
		}
	}

	crypttab, err := os.ReadFile(filepath.Join(root, "ostree", "deploy", "fedora", "deploy", "abc123.0", "etc", "crypttab"))
	if err != nil {
		t.Fatal(err)
	}
	want := "luks-" + luks + " UUID=" + luks + " none tpm2-device=auto,discard\n"
	if string(crypttab) != want {
		t.Errorf("crypttab = %q, want %q", crypttab, want)
	}

	// Privileged steps went through the runner with Utah's arguments.
	resize := fake.argv("btrfs")
	if len(resize) != 4 || resize[0] != "filesystem" || resize[3] != root {
		t.Errorf("btrfs resize argv = %v", resize)
	}
	boot := fake.argv("efibootmgr")
	joined := strings.Join(boot, " ")
	for _, want := range []string{"--create", "--disk /dev/vda", "--part 1", "--label TunaOS", `--loader \EFI\fedora\shimx64.efi`} {
		if !strings.Contains(joined, want) {
			t.Errorf("efibootmgr argv misses %q: %v", want, boot)
		}
	}

	if len(rep.Rewrote) == 0 || len(rep.Entries) == 0 || len(rep.Crypttabs) == 0 || rep.Loader == "" {
		t.Errorf("report is empty: %+v", rep)
	}
}

func TestApplySystemdLayout(t *testing.T) {
	root := copyTree(t, "systemd")
	fake := &fakeRunner{}
	rep, err := Apply(context.Background(), Options{
		Target:   mountTree(root, "systemd"),
		UUIDMap:  uuidMap(),
		LUKSUUID: luks,
		Disk:     "/dev/vda",
		ESPPArt:  "1",
	}, fake.Run)
	if err != nil {
		t.Fatal(err)
	}

	// gpt-auto entries carry no root= karg (dakota#1920): they still gain
	// rd.luks.uuid, and the entry is otherwise untouched.
	entry := filepath.Join(root, "efi", "loader", "entries", "ostree-1-sd.conf")
	b, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "root=") {
		t.Errorf("gpt-auto entry gained a root= karg:\n%s", b)
	}
	if !strings.Contains(string(b), "rd.luks.uuid="+luks) {
		t.Errorf("gpt-auto entry misses rd.luks.uuid:\n%s", b)
	}
	if !strings.Contains(string(b), "rd.luks.options="+luks+"=discard") {
		t.Errorf("no-TPM entry wants plain discard:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(root, "efi", "loader", "entries", "utah-live.conf")); !os.IsNotExist(err) {
		t.Error("live entry was not removed")
	}

	// No shim on the ESP-only layout: no firmware entry, no efibootmgr call.
	if rep.Loader != "" {
		t.Errorf("loader = %q, want empty (no shim)", rep.Loader)
	}
	if fake.argv("efibootmgr") != nil {
		t.Errorf("efibootmgr must not run without shim: %v", fake.calls)
	}
	if fake.argv("btrfs") != nil {
		t.Errorf("grow is off: %v", fake.calls)
	}
}

func TestEnsureLUKSOptsIdempotent(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "test.conf")
	if err := os.WriteFile(entry, []byte("title T\noptions rw quiet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureLUKSOpts(dir, luks, false); err != nil {
		t.Fatal(err)
	}
	touched, err := EnsureLUKSOpts(dir, luks, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 0 {
		t.Errorf("second run touched %v", touched)
	}
	b, _ := os.ReadFile(entry)
	if n := strings.Count(string(b), "rd.luks.uuid="); n != 1 {
		t.Errorf("rd.luks.uuid appears %d times:\n%s", n, b)
	}
}

func TestCheckMachineID(t *testing.T) {
	dir := t.TempDir()
	deploy := filepath.Join(dir, "d")
	if err := os.MkdirAll(filepath.Join(deploy, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckMachineID(deploy); err != nil {
		t.Errorf("missing machine-id is pristine: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploy, "etc", "machine-id"), []byte("uninitialized\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckMachineID(deploy); err != nil {
		t.Errorf("uninitialized is pristine: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploy, "etc", "machine-id"), []byte("9f3a2c1d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckMachineID(deploy); err == nil {
		t.Error("a baked-in machine-id must fail")
	}
}

func TestFindDeployments(t *testing.T) {
	root := copyTree(t, "grub")
	deploys, err := FindDeployments(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(deploys) != 1 || !strings.HasSuffix(deploys[0], "abc123.0") {
		t.Errorf("deployments = %v", deploys)
	}
	if _, err := FindDeployments(t.TempDir()); err == nil {
		t.Error("no deployment must fail")
	}
}

func TestShimLoader(t *testing.T) {
	root := copyTree(t, "grub")
	loader, err := ShimLoader(filepath.Join(root, "efi"))
	if err != nil {
		t.Fatal(err)
	}
	if loader != `\EFI\fedora\shimx64.efi` {
		t.Errorf("loader = %q", loader)
	}
	loader, err = ShimLoader(filepath.Join(copyTree(t, "systemd"), "efi"))
	if err != nil {
		t.Fatal(err)
	}
	if loader != "" {
		t.Errorf("systemd-boot layout has no shim, got %q", loader)
	}
}

func TestRemoveMediumExtras(t *testing.T) {
	root, efi := t.TempDir(), t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(efi, "EFI", "tunaos-live", "vmlinuz"))
	mk(filepath.Join(efi, "EFI", "fedora", "shimx64.efi"))
	mk(filepath.Join(root, "ostree/deploy/default/var/lib/extensions/medium-sysinstall.raw"))
	mk(filepath.Join(root, "ostree/deploy/default/var/lib/extensions/keep-me.raw"))
	mk(filepath.Join(root, "state/os/default/var/lib/extensions/medium-sysinstall.raw"))
	gone, err := RemoveMediumExtras(root, efi)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 3 {
		t.Errorf("removed %v, want the live dir and two sysexts", gone)
	}
	for _, p := range []string{
		filepath.Join(efi, "EFI", "tunaos-live"),
		filepath.Join(root, "ostree/deploy/default/var/lib/extensions/medium-sysinstall.raw"),
		filepath.Join(root, "state/os/default/var/lib/extensions/medium-sysinstall.raw"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range []string{
		filepath.Join(efi, "EFI", "fedora", "shimx64.efi"),
		filepath.Join(root, "ostree/deploy/default/var/lib/extensions/keep-me.raw"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
	// a second run finds nothing and does not fail
	if gone, err := RemoveMediumExtras(root, efi); err != nil || len(gone) != 0 {
		t.Errorf("second run = %v, %v", gone, err)
	}
}

func TestGrowRoot(t *testing.T) {
	for _, c := range []struct {
		fs, dev, want string
		wantErr       bool
	}{
		{"", "", "btrfs filesystem resize max /mnt", false},
		{"btrfs", "", "btrfs filesystem resize max /mnt", false},
		{"xfs", "", "xfs_growfs /mnt", false},
		{"ext4", "/dev/mapper/r", "resize2fs /dev/mapper/r", false},
		{"ext4", "", "", true},
		{"zfs", "", "", true},
	} {
		var got string
		err := growRoot(context.Background(), Options{Target: Target{Root: "/mnt"}, RootFS: c.fs, RootDev: c.dev},
			func(_ context.Context, name string, args ...string) error {
				got = name + " " + strings.Join(args, " ")
				return nil
			})
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("fs %q: ran %q, err %v; want %q (err %v)", c.fs, got, err, c.want, c.wantErr)
		}
	}
}
