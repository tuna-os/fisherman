package varlinkapi

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/fisherman/internal/varlink"
)

// planSpy is the mock executor that keeps the plan of the last install.
type planSpy struct {
	*Mock
	mu   sync.Mutex
	plan InstallPlan
}

func (p *planSpy) Install(ctx context.Context, plan InstallPlan, rep Reporter) error {
	p.mu.Lock()
	p.plan = plan
	p.mu.Unlock()
	return p.Mock.Install(ctx, plan, rep)
}

func runWithSpy(t *testing.T, call string) TargetConfig {
	t.Helper()
	spy := &planSpy{Mock: NewMock(Scenario{}, 0)}
	log := slog.New(slog.NewTextHandler(&lockedBuf{}, nil))
	srv := varlink.NewServer(varlink.ServiceInfo{Product: "fishermand-test"}, log)
	NewService(spy, log).Register(srv)
	dir := t.TempDir()
	path := filepath.Join(dir, "s")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	e := &env{path: path}
	if frames := e.raw(t, call, 0); frames[len(frames)-1] != `{}` {
		t.Fatalf("run: %v", frames)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.plan.Target
}

// TestRunCarriesCopyFlags: gnome-setup asks Run to copy the live session's
// locale, keymap and time zone; the plan carries each flag as sent.
func TestRunCarriesCopyFlags(t *testing.T) {
	got := runWithSpy(t, runCall)
	if !got.CopyLocale || !got.CopyKeymap || !got.CopyTimezone {
		t.Errorf("copy flags from gnome-setup's call: %+v", got)
	}
	got = runWithSpy(t, `{"method":"io.systemd.SysInstall.Run","more":true,"parameters":{"node":"/dev/nvme0n1","erase":true,"variables":true,"copyLocale":null,"copyKeymap":false,"copyTimezone":true}}`)
	if got.CopyLocale || got.CopyKeymap || !got.CopyTimezone {
		t.Errorf("null and false copy nothing: %+v", got)
	}
}

// TestConfigureTargetCopiesFromRunningSystem: a copy flag becomes the
// systemd-firstboot --copy-* option; a value from ConfigureTarget wins.
func TestConfigureTargetCopiesFromRunningSystem(t *testing.T) {
	fx := newInstallFixture(t, false)
	root := filepath.Join(fx.work, "install-1", "mnt", "root")
	dep := filepath.Join(root, "ostree/deploy/default/deploy/a.0")
	if err := os.MkdirAll(dep, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := fx.real.configureTarget(context.Background(), root, []string{dep},
		TargetConfig{Keymap: "de", CopyLocale: true, CopyKeymap: true, CopyTimezone: true})
	if err != nil {
		t.Fatal(err)
	}
	got := fx.cmd.normalized()
	for _, want := range []string{"--copy-locale", "--keymap=de", "--copy-timezone"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--copy-keymap") {
		t.Errorf("--copy-keymap next to an explicit keymap:\n%s", got)
	}
}

// gisFixture is a deployment with gnome-initial-setup and the Ubuntu-style
// skip list in /usr/share, plus a live system with the given files.
func gisFixture(t *testing.T, live map[string]string) (*installFixture, string, string) {
	t.Helper()
	fx := newInstallFixture(t, false)
	root := filepath.Join(fx.work, "install-1", "mnt", "root")
	dep := filepath.Join(root, "ostree/deploy/default/deploy/a.0")
	files := map[string]string{
		"usr/libexec/gnome-initial-setup":           "",
		"usr/share/gnome-initial-setup/vendor.conf": "[pages]\nskip=ubuntu-pro\n\n[goa]\nproviders=owncloud\n",
	}
	for rel, body := range files {
		writeFile(t, filepath.Join(dep, rel), body)
	}
	for rel, body := range live {
		writeFile(t, filepath.Join(fx.root, rel), body)
	}
	// Stand-in for systemd-firstboot --copy-*: copy what the live system has.
	fx.cmd.hook = func(_ context.Context, name string, args []string, _ CmdOpts) ([]byte, error, bool) {
		if name != "systemd-firstboot" {
			return nil, nil, false
		}
		for _, a := range args {
			rel := map[string]string{"--copy-locale": "etc/locale.conf", "--copy-keymap": "etc/vconsole.conf"}[a]
			if b, err := os.ReadFile(filepath.Join(fx.root, rel)); rel != "" && err == nil {
				writeFile(t, filepath.Join(dep, rel), string(b))
			}
		}
		return nil, nil, true
	}
	return fx, root, dep
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGISHandoffCarried: the live session has a language and a keyboard, so
// the installed deployment skips both pages; the image's list stays.
func TestGISHandoffCarried(t *testing.T) {
	fx, root, dep := gisFixture(t, map[string]string{
		"etc/locale.conf":   "LANG=de_DE.UTF-8\n",
		"etc/vconsole.conf": "KEYMAP=de\nXKBLAYOUT=de\n",
	})
	all := TargetConfig{CopyLocale: true, CopyKeymap: true, CopyTimezone: true}
	if _, err := fx.real.configureTarget(context.Background(), root, []string{dep}, all); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dep, gisEtcVendorConf))
	if err != nil {
		t.Fatal(err)
	}
	want := "[pages]\nskip=ubuntu-pro;language;keyboard\n\n[goa]\nproviders=owncloud\n"
	if string(got) != want {
		t.Errorf("vendor.conf =\n%s\nwant\n%s", got, want)
	}
	// The live system is never touched.
	if _, err := os.Stat(filepath.Join(fx.root, gisEtcVendorConf)); err == nil {
		t.Error("vendor.conf written on the live system")
	}
	// A second deployment write keeps one entry per page.
	if _, err := fx.real.configureTarget(context.Background(), root, []string{dep}, all); err != nil {
		t.Fatal(err)
	}
	if got2, _ := os.ReadFile(filepath.Join(dep, gisEtcVendorConf)); string(got2) != want {
		t.Errorf("second run:\n%s", got2)
	}
}

// TestGISHandoffNotCarried: nothing to carry (no files, or LANG=C), or no
// copy asked, or a deployment without gnome-initial-setup: nothing written.
func TestGISHandoffNotCarried(t *testing.T) {
	cases := map[string]struct {
		live map[string]string
		cfg  TargetConfig
	}{
		"no live files":   {nil, TargetConfig{CopyLocale: true, CopyKeymap: true}},
		"C locale, no km": {map[string]string{"etc/locale.conf": "LANG=C.UTF-8\n"}, TargetConfig{CopyLocale: true, CopyKeymap: true}},
		"copy not asked":  {map[string]string{"etc/locale.conf": "LANG=de_DE.UTF-8\n", "etc/vconsole.conf": "KEYMAP=de\n"}, TargetConfig{Hostname: "h"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fx, root, dep := gisFixture(t, c.live)
			if _, err := fx.real.configureTarget(context.Background(), root, []string{dep}, c.cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dep, gisEtcVendorConf)); err == nil {
				t.Error("vendor.conf written although nothing was carried")
			}
		})
	}
	// Only the keyboard carried: only that page goes.
	fx, root, dep := gisFixture(t, map[string]string{"etc/locale.conf": "LANG=C\n", "etc/vconsole.conf": "XKBLAYOUT=fr\n"})
	if _, err := fx.real.configureTarget(context.Background(), root, []string{dep}, TargetConfig{CopyLocale: true, CopyKeymap: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dep, gisEtcVendorConf)); !strings.Contains(string(got), "skip=ubuntu-pro;keyboard\n") {
		t.Errorf("keyboard only:\n%s", got)
	}
	// The copy did not land (firstboot wrote nothing): nothing is hidden.
	fx, root, dep = gisFixture(t, map[string]string{"etc/locale.conf": "LANG=de_DE.UTF-8\n", "etc/vconsole.conf": "KEYMAP=de\n"})
	fx.cmd.hook = nil
	if _, err := fx.real.configureTarget(context.Background(), root, []string{dep}, TargetConfig{CopyLocale: true, CopyKeymap: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dep, gisEtcVendorConf)); err == nil {
		t.Error("vendor.conf written although the copy did not land")
	}
	// No gnome-initial-setup in the deployment.
	fx, root, dep = gisFixture(t, map[string]string{"etc/locale.conf": "LANG=de_DE.UTF-8\n"})
	os.RemoveAll(filepath.Join(dep, "usr"))
	if _, err := fx.real.configureTarget(context.Background(), root, []string{dep}, TargetConfig{CopyLocale: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dep, gisEtcVendorConf)); err == nil {
		t.Error("vendor.conf written without gnome-initial-setup")
	}
}
