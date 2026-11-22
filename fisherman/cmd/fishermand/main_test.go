//go:build linux

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/varlink"
)

// Real mode serves exactly one interface per socket: the fisherman socket
// answers only io.tunaos.Fisherman, and the sysinstall socket only
// io.systemd.SysInstall. Mutating calls hit the real polkit authority,
// which denies them without the image policy.
func TestRealModeServesOneInterfacePerSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "fmd-real")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// The daemon runs until the test binary exits, like TestServesSocketDir.
	go func() { _ = run([]string{"--socket-dir", dir}, io.Discard) }()

	dial := func(name string) *varlink.Client {
		t.Helper()
		var c *varlink.Client
		for range 100 {
			c, err = varlink.Dial(filepath.Join(dir, name))
			if err == nil {
				return c
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(err)
		return nil
	}
	infos := func(c *varlink.Client) []string {
		t.Helper()
		var info struct {
			Interfaces []string `json:"interfaces"`
		}
		if err := c.Call("org.varlink.service.GetInfo", nil, &info); err != nil {
			t.Fatal(err)
		}
		return info.Interfaces
	}
	fisher := dial("io.tunaos.Fisherman")
	defer fisher.Close()
	if got := infos(fisher); len(got) != 2 || got[0] != "io.tunaos.Fisherman" || got[1] != "org.varlink.service" {
		t.Errorf("fisherman socket serves %v", got)
	}
	sysinst := dial("io.systemd.SysInstall")
	defer sysinst.Close()
	if got := infos(sysinst); len(got) != 2 || got[0] != "io.systemd.SysInstall" || got[1] != "org.varlink.service" {
		t.Errorf("sysinstall socket serves %v", got)
	}
	// A foreign interface on this socket is unknown, not proxied.
	var out map[string]any
	if err := fisher.Call("io.systemd.SysInstall.ListCandidateDevices", map[string]any{}, &out); callErr(err) != "org.varlink.service.MethodNotFound" {
		t.Errorf("cross-interface call: %v", err)
	}
	// Mutating calls reach the real polkit authority, which denies them
	// without the image policy.
	if err := fisher.Call("io.tunaos.Fisherman.Cancel", nil, &out); callErr(err) != "org.varlink.service.PermissionDenied" {
		t.Errorf("cancel: %v", err)
	}
}

func callErr(err error) string {
	var ve *varlink.Error
	if errors.As(err, &ve) {
		return ve.Name
	}
	if err != nil {
		return "transport: " + err.Error()
	}
	return ""
}

func TestBadScenario(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--mock", "--scenario", "nope"}, &stderr); code != 2 || !strings.Contains(stderr.String(), "valid:") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}

func TestListScenarios(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	code := run([]string{"--list-scenarios"}, io.Discard)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatal(code)
	}
	for _, n := range []string{"happy", "no-tpm", "rst", "existing-data", "device-lost", "repart-error", "cancel", "handoff-migrate", "handoff-soft-reboot"} {
		if !strings.Contains(string(out), n) {
			t.Errorf("%s missing from %s", n, out)
		}
	}
}

// The daemon binds all four sockets in --socket-dir, with the file names
// gnome-setup connects to, mode 0600, and serves the scenario from the env.
func TestServesSocketDir(t *testing.T) {
	dir, err := os.MkdirTemp("", "fmd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("FISHERMAN_MOCK_SCENARIO", "no-tpm")

	done := make(chan int, 1)
	go func() { done <- run([]string{"--mock", "--socket-dir", dir}, io.Discard) }()

	var c *varlink.Client
	for i := 0; i < 100; i++ {
		c, err = varlink.Dial(filepath.Join(dir, "io.tunaos.Fisherman"))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var p struct{ TPM struct{ Usable bool } }
	if err := c.Call("io.tunaos.Fisherman.Probe", nil, &p); err != nil || p.TPM.Usable {
		t.Fatalf("%v %+v (the env scenario no-tpm should apply)", err, p)
	}
	// mock mode serves every interface on every socket: the sysinstall
	// socket answers the fisherman Probe too.
	sc, err := varlink.Dial(filepath.Join(dir, "io.systemd.SysInstall"))
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	var q struct{ TPM struct{ Usable bool } }
	if err := sc.Call("io.tunaos.Fisherman.Probe", nil, &q); err != nil || q.TPM.Usable {
		t.Fatalf("%v %+v (mock serves all interfaces on all sockets)", err, q)
	}
	for _, n := range []string{"io.systemd.SysInstall", "io.systemd.CryptEnroll", "io.systemd.Shutdown", "io.tunaos.Fisherman"} {
		fi, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", n, fi.Mode().Perm())
		}
	}
	// a second daemon on the same sockets must refuse, not steal them
	var stderr bytes.Buffer
	if code := run([]string{"--mock", "--socket-dir", dir}, &stderr); code != 1 || !strings.Contains(stderr.String(), "another daemon") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}
