//go:build linux

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/varlink"
)

func TestRefusesWithoutMock(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(nil, &stderr); code != 2 || !strings.Contains(stderr.String(), "--mock") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
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
	for _, n := range []string{"happy", "no-tpm", "rst", "existing-data", "device-lost", "repart-error", "cancel"} {
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
