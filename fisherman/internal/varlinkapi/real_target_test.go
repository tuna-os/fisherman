package varlinkapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetINI(t *testing.T) {
	got := setINI("[daemon]\nWaylandEnable=true\nAutomaticLoginEnable=False\n\n[security]\nX=1\n", "daemon",
		map[string]string{"AutomaticLoginEnable": "True", "AutomaticLogin": "ada"})
	want := "[daemon]\nWaylandEnable=true\nAutomaticLoginEnable=True\nAutomaticLogin=ada\n\n[security]\nX=1\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if got := setINI("", "daemon", map[string]string{"A": "1"}); got != "[daemon]\nA=1\n" {
		t.Errorf("empty file: %q", got)
	}
}

func TestConfigureTargetRefusesOutsideScratch(t *testing.T) {
	fx := newInstallFixture(t, false)
	for _, root := range []string{"/", "", "/etc", fx.root, filepath.Join(fx.root, "elsewhere")} {
		_, err := fx.real.configureTarget(context.Background(), root, []string{filepath.Join(root, "x")}, TargetConfig{Hostname: "h"})
		if err == nil {
			t.Errorf("root %q was accepted", root)
		}
	}
	if fx.cmd.normalized() != "" {
		t.Errorf("a command ran against a refused root: %s", fx.cmd.normalized())
	}
	root := filepath.Join(fx.work, "install-1", "mnt", "root")
	if _, err := fx.real.configureTarget(context.Background(), root, []string{"/etc"}, TargetConfig{Hostname: "h"}); err == nil {
		t.Error("a deployment outside the target root was accepted")
	}
}

func TestConfigureTargetNoUserSkipsAccount(t *testing.T) {
	fx := newInstallFixture(t, false)
	root := filepath.Join(fx.work, "install-1", "mnt", "root")
	dep := filepath.Join(root, "ostree/deploy/default/deploy/a.0")
	if err := os.MkdirAll(dep, 0o755); err != nil {
		t.Fatal(err)
	}
	warns, err := fx.real.configureTarget(context.Background(), root, []string{dep},
		TargetConfig{Locale: "de_DE.UTF-8", Autologin: true})
	if err != nil {
		t.Fatal(err)
	}
	got := fx.cmd.normalized()
	if strings.Contains(got, "useradd") || !strings.Contains(got, "--locale=de_DE.UTF-8") {
		t.Errorf("commands:\n%s", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "needs a user") {
		t.Errorf("warnings = %v", warns)
	}
}

// TestConfigureTargetPasswordOnStdin: the password never appears in an
// argument list or a recorded line; only its length does.
func TestConfigureTargetPasswordOnStdin(t *testing.T) {
	fx := newInstallFixture(t, false)
	root := filepath.Join(fx.work, "install-1", "mnt", "root")
	dep := filepath.Join(root, "ostree/deploy/default/deploy/a.0")
	if err := os.MkdirAll(filepath.Join(dep, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep, "etc/group"), []byte("sudo:x:27:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdin string
	fx.cmd.hook = func(_ context.Context, name string, _ []string, o CmdOpts) ([]byte, error, bool) {
		if name == "chpasswd" {
			stdin = string(o.Stdin)
			if !o.Quiet {
				t.Error("chpasswd must run Quiet")
			}
		}
		return nil, nil, false
	}
	admin := false
	_, err := fx.real.configureTarget(context.Background(), root, []string{dep},
		TargetConfig{User: &User{Username: "bob", Password: ptr(Secret("s3cret pw")), Admin: &admin}})
	if err != nil {
		t.Fatal(err)
	}
	if stdin != "bob:s3cret pw\n" {
		t.Errorf("stdin = %q", stdin)
	}
	got := fx.cmd.normalized()
	if strings.Contains(got, "s3cret") {
		t.Errorf("the password reached a recorded command:\n%s", got)
	}
	if strings.Contains(got, "--groups") {
		t.Errorf("a non-admin got an admin group:\n%s", got)
	}
	admin = true
	fx2 := newInstallFixture(t, false)
	root2 := filepath.Join(fx2.work, "install-1", "mnt", "root")
	dep2 := filepath.Join(root2, "ostree/deploy/default/deploy/a.0")
	if err := os.MkdirAll(filepath.Join(dep2, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep2, "etc/group"), []byte("sudo:x:27:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fx2.real.configureTarget(context.Background(), root2, []string{dep2},
		TargetConfig{User: &User{Username: "eve", Admin: &admin}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fx2.cmd.normalized(), "--groups sudo") {
		t.Errorf("an admin on a sudo image: %s", fx2.cmd.normalized())
	}
}
