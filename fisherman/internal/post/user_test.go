package post

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
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
