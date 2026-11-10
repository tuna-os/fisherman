package post_test

// Post-install steps against sysroots shaped like real installs
// (deploytest), not flat temp dirs: tuna-os/fisherman#407 and #360 were
// invisible to flat fixtures, where <target>/etc and <target>/var are as
// good as any other path.

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/runner"
)

// realFileCommands lets the harmless file commands the post steps shell out
// to (ls, mkdir, cp, tee) really run, so the fixture ends up with what an
// install would write. Everything else is recorded and succeeds.
func realFileCommands(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	runner.RunFn = func(stdin io.Reader, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "ls", "mkdir", "cp", "tee":
			cmd := exec.Command(name, args...)
			cmd.Stdin = stdin
			return cmd.Run()
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })
	return &calls
}

func fixtures(t *testing.T) map[string]deploytest.Sysroot {
	t.Helper()
	return map[string]deploytest.Sysroot{
		"ostree":    deploytest.Ostree(t),
		"composefs": deploytest.ComposeFs(t),
	}
}

func assertNoStrays(t *testing.T, fx deploytest.Sysroot) {
	t.Helper()
	if s := deploytest.Strays(fx); len(s) != 0 {
		t.Errorf("wrote %v into the physical sysroot, which no booted system reads", s)
	}
}

func TestResolveDeployment_Fixtures(t *testing.T) {
	for name, fx := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			realFileCommands(t)
			d, err := post.ResolveDeployment(fx.Path)
			if err != nil {
				t.Fatal(err)
			}
			if d.Layout.String() != name || d.Root != fx.Root || d.Etc != fx.Etc || d.Var != fx.Var {
				t.Errorf("layout=%s root=%s etc=%s var=%s\nwant %s %s %s %s",
					d.Layout, d.Root, d.Etc, d.Var, name, fx.Root, fx.Etc, fx.Var)
			}
		})
	}
}

// A recipe varDisk is mounted over /var at boot: that mount is the booted
// /var, not the stateroot's.
func TestResolveDeployment_BootedVarOverride(t *testing.T) {
	fx := deploytest.Ostree(t)
	realFileCommands(t)
	varDisk := filepath.Join(fx.Path, "var")
	post.SetBootedVar(varDisk)
	t.Cleanup(func() { post.SetBootedVar("") })
	d, err := post.ResolveDeployment(fx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Var != varDisk || d.Etc != fx.Etc {
		t.Errorf("var=%s etc=%s, want var=%s etc=%s", d.Var, d.Etc, varDisk, fx.Etc)
	}
}

func TestResolveDeployment_NoDeployment(t *testing.T) {
	realFileCommands(t)
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "ostree", "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := post.ResolveDeployment(sysroot); err == nil {
		t.Error("no error for a target bootc did not install into")
	}
}

func TestWriteHostname_Fixtures(t *testing.T) {
	for name, fx := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			realFileCommands(t)
			if err := post.WriteHostname(fx.Path, "fixturehost"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(fx.Etc, "hostname"))
			if err != nil || string(got) != "fixturehost\n" {
				t.Errorf("hostname in the deployment etc = %q, %v", got, err)
			}
			assertNoStrays(t, fx)
		})
	}
}

// CreateUser on a real-shaped ostree sysroot: the home goes to the stateroot
// var (the booted /var/home), and the tmpfiles snippet to the deployment etc.
func TestCreateUser_OstreeFixture(t *testing.T) {
	fx := deploytest.Ostree(t)
	calls := realFileCommands(t)
	if err := post.CreateUser(fx.Path, post.UserConfig{Username: "ada", Groups: []string{"wheel", "nope"}}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(fx.Var, "home")); err != nil || !fi.IsDir() {
		t.Errorf("stateroot home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fx.Etc, "tmpfiles.d", "fisherman-home-ada.conf")); err != nil {
		t.Errorf("tmpfiles snippet in the deployment etc: %v", err)
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "chroot "+fx.Root+" useradd --create-home --shell /bin/bash --groups wheel ada") {
		t.Errorf("useradd did not run in the deployment root with the groups the image has:\n%s", joined)
	}
	assertNoStrays(t, fx)
}

func TestCreateUser_ComposeFsFixture(t *testing.T) {
	fx := deploytest.ComposeFs(t)
	calls := realFileCommands(t)
	if err := post.CreateUser(fx.Path, post.UserConfig{Username: "ada"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(*calls, "\n"), "useradd --root "+fx.Root+" --shell /bin/bash ada") {
		t.Errorf("useradd --root did not target the deployment root:\n%s", strings.Join(*calls, "\n"))
	}
	if _, err := os.Stat(filepath.Join(fx.Etc, "tmpfiles.d", "fisherman-home-ada.conf")); err != nil {
		t.Errorf("tmpfiles snippet in the deployment etc: %v", err)
	}
	assertNoStrays(t, fx)
}

func TestEnablePrintServices_Fixtures(t *testing.T) {
	for name, fx := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			realFileCommands(t)
			post.EnablePrintServices(fx.Path)
			link := filepath.Join(fx.Etc, "systemd", "system", "multi-user.target.wants", "avahi-daemon.service")
			if _, err := os.Lstat(link); err != nil {
				t.Errorf("wants link in the deployment etc: %v", err)
			}
			assertNoStrays(t, fx)
		})
	}
}
