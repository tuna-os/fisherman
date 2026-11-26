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
