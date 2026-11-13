package post

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
	"github.com/tuna-os/fisherman/internal/sha512crypt"
)

// On ostree targets, useradd --create-home resolves the deployment's
// /home -> var/home symlink and creates the home inside the DEPLOYMENT's
// var — masked at runtime by the stateroot var mount. CreateUser must
// relocate it into the stateroot var and write a tmpfiles.d snippet so
// first boot recreates/relabels it (wootc E2E run 20260723T0423: passwd
// had the user, the booted var/home had nothing).
func TestCreateUserRelocatesHomeToStaterootVar(t *testing.T) {
	sysroot := t.TempDir()
	deployDir := filepath.Join(sysroot, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDeployFn := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeployFn }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	var calls [][]string
	origRunFn := runner.RunFn
	defer func() { runner.RunFn = origRunFn }()
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		switch name {
		case "ls":
			if _, err := os.Stat(args[len(args)-1]); err != nil {
				return err
			}
			return nil
		case "mkdir":
			return os.MkdirAll(args[len(args)-1], 0o755)
		case "chroot":
			if len(args) > 1 && args[1] == "useradd" {
				// Simulate the real symlink-following behavior: the home
				// lands in the deployment's own var/home.
				return os.MkdirAll(filepath.Join(deployDir, "var", "home", "alice"), 0o700)
			}
			return nil
		case "mv":
			return os.Rename(args[len(args)-2], args[len(args)-1])
		}
		return nil
	}

	if err := CreateUser(sysroot, UserConfig{Username: "alice"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	stateHome := filepath.Join(sysroot, "ostree", "deploy", "default", "var", "home", "alice")
	if _, err := os.Stat(stateHome); err != nil {
		t.Errorf("home not relocated to stateroot var: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deployDir, "var", "home", "alice")); !os.IsNotExist(err) {
		t.Error("orphaned home still present in deployment var")
	}

	snippet, err := os.ReadFile(filepath.Join(deployDir, "etc", "tmpfiles.d", "fisherman-home-alice.conf"))
	if err != nil {
		t.Fatalf("tmpfiles snippet missing: %v", err)
	}
	for _, want := range []string{
		"C /var/home/alice 0700 alice alice - /etc/skel",
		"Z /var/home/alice - alice alice -",
	} {
		if !strings.Contains(string(snippet), want) {
			t.Errorf("tmpfiles snippet missing %q; got:\n%s", want, snippet)
		}
	}
}

// useradd --root from a booted host initializes the host PAM/SELinux stack
// and fails against a writable target etc (wootc run 20260723T0738). User
// tooling must run via chroot with the target's own binaries.
func TestCreateUserUsesChrootNotRootFlag(t *testing.T) {
	sysroot := t.TempDir()
	deployDir := filepath.Join(sysroot, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origDeployFn := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeployFn }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	var calls [][]string
	origRunFn := runner.RunFn
	defer func() { runner.RunFn = origRunFn }()
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if name == "ls" {
			if _, err := os.Stat(args[len(args)-1]); err != nil {
				return err
			}
		}
		return nil
	}

	if err := CreateUser(sysroot, UserConfig{Username: "bob", Password: "$6$salt$hash"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	var sawUseradd, sawChpasswd bool
	for _, c := range calls {
		if c[0] == "useradd" || c[0] == "chpasswd" {
			t.Errorf("direct %s call (must go through chroot): %v", c[0], c)
		}
		if c[0] == "chroot" && len(c) > 2 && c[2] == "useradd" {
			sawUseradd = true
		}
		if c[0] == "chroot" && len(c) > 2 && c[2] == "chpasswd" && c[3] == "-e" {
			sawChpasswd = true
		}
	}
	if !sawUseradd || !sawChpasswd {
		t.Errorf("missing chroot useradd/chpasswd calls: %v", calls)
	}
}

// composefs-native has no chrootable rootfs during deploy — useradd must run
// with --root (not chroot), pointed at the deploy root (parent of the
// state/deploy/<hash>/etc dir). dakota exit-127 regression, GH 20260724T1508.
func TestCreateUserComposeFsUsesRootFlag(t *testing.T) {
	sysroot := t.TempDir()
	etcDir := filepath.Join(sysroot, "state", "deploy", "abc123", "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// isComposeFsNative keys off state/deploy existing (via the ls stub below).
	origEtcFn := ComposeFsDeployEtcDirFn
	defer func() { ComposeFsDeployEtcDirFn = origEtcFn }()
	ComposeFsDeployEtcDirFn = func(string) (string, error) { return etcDir, nil }

	var calls [][]string
	origRunFn := runner.RunFn
	defer func() { runner.RunFn = origRunFn }()
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if name == "ls" {
			_, err := os.Stat(args[len(args)-1])
			return err
		}
		return nil
	}

	if err := CreateUser(sysroot, UserConfig{Username: "carol", Password: "$6$s$h"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	wantRoot := filepath.Join(sysroot, "state", "deploy", "abc123")
	var sawUseradd bool
	for _, c := range calls {
		if c[0] == "chroot" {
			t.Errorf("composefs must not chroot: %v", c)
		}
		if c[0] == "useradd" && c[1] == "--root" && c[2] == wantRoot {
			sawUseradd = true
		}
	}
	if !sawUseradd {
		t.Errorf("expected useradd --root %s; calls: %v", wantRoot, calls)
	}
}

// Groups that do not exist on the target must be skipped, not passed to
// useradd (which exits 6 and aborts the install — e.g. libvirt on images
// without virt-manager).
func TestCreateUserSkipsMissingGroups(t *testing.T) {
	sysroot := t.TempDir()
	deployDir := filepath.Join(sysroot, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(filepath.Join(deployDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	groupFile := "root:x:0:\nwheel:x:10:alice\ndialout:x:18:\n"
	if err := os.WriteFile(filepath.Join(deployDir, "etc", "group"), []byte(groupFile), 0o644); err != nil {
		t.Fatal(err)
	}
	origDeployFn := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeployFn }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	var calls [][]string
	origRunFn := runner.RunFn
	defer func() { runner.RunFn = origRunFn }()
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if name == "ls" {
			if _, err := os.Stat(args[len(args)-1]); err != nil {
				return err
			}
			return nil
		}
		if name == "mkdir" {
			return os.MkdirAll(args[len(args)-1], 0o755)
		}
		return nil
	}

	u := UserConfig{Username: "dave", Groups: []string{"wheel", "libvirt", "dialout", "docker"}}
	if err := CreateUser(sysroot, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, c := range calls {
		if c[0] == "chroot" && len(c) > 2 && c[2] == "useradd" {
			joined := strings.Join(c, " ")
			if strings.Contains(joined, "libvirt") || strings.Contains(joined, "docker") {
				t.Errorf("missing groups passed to useradd: %v", c)
			}
			if !strings.Contains(joined, "--groups wheel,dialout") {
				t.Errorf("existing groups not passed through: %v", c)
			}
			return
		}
	}
	t.Errorf("no chroot useradd call recorded: %v", calls)
}

// When the target group file cannot be read, verification is impossible —
// keep every group and let useradd decide rather than stripping them all.
func TestCreateUserKeepsGroupsWhenGroupFileMissing(t *testing.T) {
	sysroot := t.TempDir()
	deployDir := filepath.Join(sysroot, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origDeployFn := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeployFn }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	var calls [][]string
	origRunFn := runner.RunFn
	defer func() { runner.RunFn = origRunFn }()
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if name == "ls" {
			if _, err := os.Stat(args[len(args)-1]); err != nil {
				return err
			}
			return nil
		}
		if name == "mkdir" {
			return os.MkdirAll(args[len(args)-1], 0o755)
		}
		return nil
	}

	u := UserConfig{Username: "erin", Groups: []string{"wheel"}}
	if err := CreateUser(sysroot, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, c := range calls {
		if c[0] == "chroot" && len(c) > 2 && c[2] == "useradd" {
			if !strings.Contains(strings.Join(c, " "), "--groups wheel") {
				t.Errorf("groups stripped without a group file to check: %v", c)
			}
			return
		}
	}
	t.Errorf("no chroot useradd call recorded: %v", calls)
}

type chpasswdCall struct {
	args  []string
	stdin string
}

// fakeComposeFsUser points CreateUser at a composefs deploy root under a temp
// sysroot and records every chpasswd invocation with its stdin.
func fakeComposeFsUser(t *testing.T) (sysroot, root string, chpasswd *[]chpasswdCall) {
	t.Helper()
	sysroot = t.TempDir()
	etcDir := filepath.Join(sysroot, "state", "deploy", "abc123", "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origEtcFn := ComposeFsDeployEtcDirFn
	t.Cleanup(func() { ComposeFsDeployEtcDirFn = origEtcFn })
	ComposeFsDeployEtcDirFn = func(string) (string, error) { return etcDir, nil }

	var calls []chpasswdCall
	origRunFn := runner.RunFn
	t.Cleanup(func() { runner.RunFn = origRunFn })
	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		switch name {
		case "ls":
			_, err := os.Stat(args[len(args)-1])
			return err
		case "chroot":
			t.Errorf("composefs must not chroot: %v", args)
		case "chpasswd":
			in, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatal(err)
			}
			calls = append(calls, chpasswdCall{args: args, stdin: string(in)})
		}
		return nil
	}
	return sysroot, filepath.Dir(etcDir), &calls
}

// chpasswd without -e hashes through PAM, and a composefs deploy root has no
// PAM modules: "pam_chauthtok() failed, error: Module is unknown"
// (projectbluefin/utah#1129). A plaintext password must be hashed in-process
// and handed to chpasswd -e as a $6$ crypt string.
func TestCreateUserComposeFsHashesPlaintextPassword(t *testing.T) {
	sysroot, root, calls := fakeComposeFsUser(t)
	const plain = "hunter2-plaintext"

	if err := CreateUser(sysroot, UserConfig{Username: "dave", Password: plain}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want one chpasswd call, got %v", *calls)
	}
	c := (*calls)[0]
	if want := []string{"--root", root, "-e"}; strings.Join(c.args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("chpasswd args = %q; want %q", c.args, want)
	}
	for _, a := range c.args {
		if strings.Contains(a, plain) {
			t.Errorf("plaintext password leaked into chpasswd args: %q", c.args)
		}
	}
	hash, ok := strings.CutPrefix(strings.TrimSuffix(c.stdin, "\n"), "dave:")
	if !ok || !strings.HasPrefix(hash, "$6$") || strings.Contains(c.stdin, plain) {
		t.Fatalf("chpasswd stdin = %q; want dave:$6$<salt>$<hash>", c.stdin)
	}
	if again, err := sha512crypt.Crypt(plain, hash); err != nil || again != hash {
		t.Errorf("stdin hash %q does not verify the password (got %q, %v)", hash, again, err)
	}
}

// A pre-hashed crypt string reaches chpasswd -e verbatim, never re-hashed.
func TestCreateUserComposeFsPassesPreHashedPassword(t *testing.T) {
	sysroot, root, calls := fakeComposeFsUser(t)
	const hashed = "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"

	if err := CreateUser(sysroot, UserConfig{Username: "carol", Password: hashed}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want one chpasswd call, got %v", *calls)
	}
	c := (*calls)[0]
	if want := []string{"--root", root, "-e"}; strings.Join(c.args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("chpasswd args = %q; want %q", c.args, want)
	}
	if want := "carol:" + hashed + "\n"; c.stdin != want {
		t.Errorf("chpasswd stdin = %q; want %q", c.stdin, want)
	}
}

// A plaintext password that merely starts with "$" is not a crypt setting:
// it must take the hash-in-process path, not reach chpasswd -e verbatim
// (which would store the literal text and lock the account).
func TestCreateUserComposeFsHashesDollarLeadingPlaintextPassword(t *testing.T) {
	_, _, calls := fakeComposeFsUser(t)
	const plain = "$notacryptsetting"

	if err := CreateUser("sysroot-ignored", UserConfig{Username: "erin", Password: plain}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want one chpasswd call, got %v", *calls)
	}
	c := (*calls)[0]
	if len(c.args) != 3 || c.args[0] != "--root" || c.args[2] != "-e" {
		t.Errorf("chpasswd args = %q; want [--root <root> -e]", c.args)
	}
	hash, ok := strings.CutPrefix(strings.TrimSuffix(c.stdin, "\n"), "erin:")
	if !ok || !strings.HasPrefix(hash, "$6$") || strings.Contains(c.stdin, plain) {
		t.Fatalf("chpasswd stdin = %q; want erin:$6$<salt>$<hash>", c.stdin)
	}
	if again, err := sha512crypt.Crypt(plain, hash); err != nil || again != hash {
		t.Errorf("stdin hash %q does not verify the password (got %q, %v)", hash, again, err)
	}
}

// A plaintext password shaped like "$id$..." with a non-whitelisted id is
// not a crypt setting either: "$ecret$1" must be hashed in-process, not
// stored verbatim (tuna-os/fisherman#414).
func TestCreateUserComposeFsHashesDollarIdLookalikePassword(t *testing.T) {
	_, _, calls := fakeComposeFsUser(t)
	const plain = "$ecret$1"

	if err := CreateUser("sysroot-ignored", UserConfig{Username: "gina", Password: plain}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want one chpasswd call, got %v", *calls)
	}
	c := (*calls)[0]
	hash, ok := strings.CutPrefix(strings.TrimSuffix(c.stdin, "\n"), "gina:")
	if !ok || !strings.HasPrefix(hash, "$6$") || strings.Contains(c.stdin, plain) {
		t.Fatalf("chpasswd stdin = %q; want gina:$6$<salt>$<hash>", c.stdin)
	}
	if again, err := sha512crypt.Crypt(plain, hash); err != nil || again != hash {
		t.Errorf("stdin hash %q does not verify the password (got %q, %v)", hash, again, err)
	}
}

// The ostree branch keeps chroot chpasswd with the target's own PAM stack:
// a plaintext password goes in as-is, without -e.
func TestCreateUserOstreePlaintextPasswordUnchanged(t *testing.T) {
	sysroot := t.TempDir()
	deployDir := filepath.Join(sysroot, "ostree", "deploy", "default", "deploy", "abc123.0")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origDeployFn := DeploymentDirFn
	defer func() { DeploymentDirFn = origDeployFn }()
	DeploymentDirFn = func(string) (string, error) { return deployDir, nil }

	var chpasswd []chpasswdCall
	origRunFn := runner.RunFn
	defer func() { runner.RunFn = origRunFn }()
	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		if name == "ls" {
			_, err := os.Stat(args[len(args)-1])
			return err
		}
		if name == "chroot" && len(args) > 1 && args[1] == "chpasswd" {
			in, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatal(err)
			}
			chpasswd = append(chpasswd, chpasswdCall{args: args, stdin: string(in)})
		}
		return nil
	}

	if err := CreateUser(sysroot, UserConfig{Username: "frank", Password: "plain"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(chpasswd) != 1 {
		t.Fatalf("want one chroot chpasswd call, got %v", chpasswd)
	}
	c := chpasswd[0]
	if want := []string{deployDir, "chpasswd"}; strings.Join(c.args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("chroot args = %q; want %q", c.args, want)
	}
	if c.stdin != "frank:plain\n" {
		t.Errorf("chpasswd stdin = %q; want %q", c.stdin, "frank:plain\n")
	}
}
