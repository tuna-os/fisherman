package deploy_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy"
	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
)

func TestResolve_Ostree(t *testing.T) {
	fx := deploytest.Ostree(t)
	d, err := deploy.Resolve(fx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Layout != deploy.Ostree || d.Layout.String() != "ostree" {
		t.Errorf("layout = %v, want ostree", d.Layout)
	}
	if d.Root != fx.Root || d.Etc != fx.Etc || d.Var != fx.Var {
		t.Errorf("got root=%s etc=%s var=%s\nwant root=%s etc=%s var=%s", d.Root, d.Etc, d.Var, fx.Root, fx.Etc, fx.Var)
	}
	if d.Stateroot != "default" || d.Sysroot != fx.Path {
		t.Errorf("stateroot=%q sysroot=%q", d.Stateroot, d.Sysroot)
	}
	// The booted /var is the stateroot's, never the deployment's own var/
	// and never <target>/var.
	if d.Var == filepath.Join(d.Root, "var") || d.Var == filepath.Join(fx.Path, "var") {
		t.Errorf("var %s is not the stateroot var", d.Var)
	}
	usr, ok := d.Usr()
	if !ok || usr != filepath.Join(fx.Root, "usr") {
		t.Errorf("Usr() = %q, %v", usr, ok)
	}
}

func TestResolve_OstreeNamedStateroot(t *testing.T) {
	sysroot := t.TempDir()
	root := filepath.Join(sysroot, "ostree", "deploy", "fedora", "deploy", "abc.0")
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := deploy.Resolve(sysroot)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stateroot != "fedora" {
		t.Errorf("stateroot = %q, want fedora", d.Stateroot)
	}
	if want := filepath.Join(sysroot, "ostree", "deploy", "fedora", "var"); d.Var != want {
		t.Errorf("var = %s, want %s", d.Var, want)
	}
}

func TestResolve_OstreePrefersDefaultStateroot(t *testing.T) {
	sysroot := t.TempDir()
	for _, st := range []string{"aaa", "default"} {
		if err := os.MkdirAll(filepath.Join(sysroot, "ostree", "deploy", st, "deploy", "x.0", "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d, err := deploy.Resolve(sysroot)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stateroot != "default" {
		t.Errorf("stateroot = %q, want default", d.Stateroot)
	}
}

func TestResolve_ComposeFs(t *testing.T) {
	fx := deploytest.ComposeFs(t)
	d, err := deploy.Resolve(fx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Layout != deploy.ComposeFs || d.Layout.String() != "composefs" {
		t.Errorf("layout = %v, want composefs (the fixture also has /ostree)", d.Layout)
	}
	if d.Root != fx.Root || d.Etc != fx.Etc || d.Var != fx.Var {
		t.Errorf("got root=%s etc=%s var=%s\nwant root=%s etc=%s var=%s", d.Root, d.Etc, d.Var, fx.Root, fx.Etc, fx.Var)
	}
	if usr, ok := d.Usr(); ok {
		t.Errorf("Usr() = %q, true: composefs has no /usr on the mounted target", usr)
	}
	if _, known := d.HasUsrFile("bin/sh"); known {
		t.Error("HasUsrFile is known on composefs")
	}
}

// The boot entry names the deployment; an older directory that is newer on
// disk must not win over it.
func TestResolve_ComposeFsFollowsBootEntry(t *testing.T) {
	fx := deploytest.ComposeFs(t)
	stale := filepath.Join(fx.Path, "state", "deploy", "ffff", "etc")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := deploy.Resolve(fx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Etc != fx.Etc {
		t.Errorf("etc = %s, want the boot entry's deployment %s", d.Etc, fx.Etc)
	}
}

func TestResolve_ComposeFsWithoutBootEntryTakesTheOnlyDeployment(t *testing.T) {
	fx := deploytest.ComposeFs(t)
	if err := os.RemoveAll(filepath.Join(fx.Path, "boot")); err != nil {
		t.Fatal(err)
	}
	d, err := deploy.Resolve(fx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Etc != fx.Etc {
		t.Errorf("etc = %s, want %s", d.Etc, fx.Etc)
	}
}

func TestResolve_NoDeployment(t *testing.T) {
	// ostree: the directory exists, bootc deployed nothing.
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "ostree", "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := deploy.Resolve(sysroot); !errors.Is(err, deploy.ErrNoDeployment) {
		t.Errorf("ostree: err = %v, want ErrNoDeployment", err)
	}
	// composefs: state/deploy is empty.
	sysroot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "state", "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := deploy.Resolve(sysroot); !errors.Is(err, deploy.ErrNoDeployment) {
		t.Errorf("composefs: err = %v, want ErrNoDeployment", err)
	}
	// An empty target is neither.
	if _, err := deploy.Resolve(t.TempDir()); !errors.Is(err, deploy.ErrNoDeployment) {
		t.Errorf("empty: err = %v, want ErrNoDeployment", err)
	}
}

func TestHasUsrFileAndUnit(t *testing.T) {
	fx := deploytest.Ostree(t)
	d, err := deploy.Resolve(fx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if present, known := d.HasUsrFile("bin/systemd-cryptenroll"); present || !known {
		t.Errorf("missing file: present=%v known=%v", present, known)
	}
	if err := os.WriteFile(filepath.Join(fx.Root, "usr", "bin", "systemd-cryptenroll"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if present, known := d.HasUsrFile("sbin/nope", "bin/systemd-cryptenroll"); !present || !known {
		t.Errorf("present file: present=%v known=%v", present, known)
	}

	if present, known := d.HasUnit("cups.service"); present || !known {
		t.Errorf("missing unit: present=%v known=%v", present, known)
	}
	if err := os.WriteFile(filepath.Join(fx.Root, "usr", "lib", "systemd", "system", "cups.service"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if present, _ := d.HasUnit("cups.service"); !present {
		t.Error("unit in /usr/lib not found")
	}

	// composefs: only /etc is visible.
	cfx := deploytest.ComposeFs(t)
	cd, err := deploy.Resolve(cfx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if present, known := cd.HasUnit("cups.service"); present || known {
		t.Errorf("composefs unknown unit: present=%v known=%v", present, known)
	}
	if err := os.WriteFile(filepath.Join(cfx.Etc, "systemd", "system", "local.service"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if present, known := cd.HasUnit("local.service"); !present || !known {
		t.Errorf("composefs etc unit: present=%v known=%v", present, known)
	}
}

func TestEtcIsTransient(t *testing.T) {
	fx := deploytest.Ostree(t)
	d, err := deploy.Resolve(fx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if d.EtcIsTransient() {
		t.Error("no prepare-root.conf: transient")
	}
	conf := filepath.Join(fx.Root, "usr", "lib", "ostree", "prepare-root.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("[composefs]\nenabled = yes\n# c\n[etc]\ntransient = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !d.EtcIsTransient() {
		t.Error("[etc] transient=true not seen")
	}
	// /etc/ostree/prepare-root.conf overrides the image's.
	override := filepath.Join(fx.Etc, "ostree", "prepare-root.conf")
	if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("[etc]\ntransient=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d.EtcIsTransient() {
		t.Error("/etc override ignored")
	}
}

func TestResolver_Hooks(t *testing.T) {
	r := deploy.Resolver{
		IsComposeFs:      func(string) bool { return false },
		OstreeDeployment: func(string) (string, error) { return "/somewhere/else", nil },
	}
	d, err := r.Resolve("/mnt/t")
	if err != nil {
		t.Fatal(err)
	}
	if d.Etc != "/somewhere/else/etc" || d.Var != "/mnt/t/ostree/deploy/default/var" {
		t.Errorf("etc=%s var=%s", d.Etc, d.Var)
	}
	r = deploy.Resolver{
		IsComposeFs:  func(string) bool { return true },
		ComposeFsEtc: func(string) (string, error) { return "/mnt/t/state/deploy/h/etc", nil },
	}
	d, err = r.Resolve("/mnt/t")
	if err != nil {
		t.Fatal(err)
	}
	if d.Root != "/mnt/t/state/deploy/h" || d.Var != "/mnt/t/state/os/default/var" {
		t.Errorf("root=%s var=%s", d.Root, d.Var)
	}
}

func TestFixturesHaveNoStrays(t *testing.T) {
	if s := deploytest.Strays(deploytest.Ostree(t)); len(s) != 0 {
		t.Errorf("ostree fixture has %v in the physical sysroot", s)
	}
	if s := deploytest.Strays(deploytest.ComposeFs(t)); len(s) != 0 {
		t.Errorf("composefs fixture has %v in the physical sysroot", s)
	}
}
