package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/probe"
)

func withProber(t *testing.T, p *probe.Prober) {
	t.Helper()
	old := newProber
	newProber = func() *probe.Prober { return p }
	t.Cleanup(func() { newProber = old })
}

func fixtureProber(t *testing.T, lsblkErr error) *probe.Prober {
	t.Helper()
	root := t.TempDir()
	for p, c := range map[string]string{
		"proc/meminfo":                  "MemTotal: 8000000 kB\n",
		"sys/devices/system/cpu/online": "0-1\n",
	} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &probe.Prober{
		Root:   root,
		Getenv: func(string) string { return "" },
		Run: func(name string, args ...string) ([]byte, error) {
			if name != "lsblk" {
				return nil, errors.New("exit status 1")
			}
			if lsblkErr != nil {
				return nil, lsblkErr
			}
			return []byte(`{"blockdevices":[{"name":"/dev/nvme0n1","type":"disk","size":512110190592,"model":"Disk","tran":"nvme","rm":false,"hotplug":false,"ro":false,"mountpoints":[null]}]}`), nil
		},
	}
}

func TestRunProbeJSON(t *testing.T) {
	withProber(t, fixtureProber(t, nil))
	var out, errOut bytes.Buffer
	if code := runProbe([]string{"--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr not empty: %q", errOut.String())
	}
	var res probe.Result
	dec := json.NewDecoder(&out)
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v", err)
	}
	if dec.More() {
		t.Error("more than one JSON value on stdout")
	}
	if res.ProtocolVersion != probe.ProtocolVersion || len(res.Disks) != 1 || !res.Disks[0].Eligible {
		t.Errorf("unexpected result %+v", res)
	}
	if res.Disks[0].SizeLabel != "476.9 GiB" || res.Disks[0].TransportLabel != "NVMe" {
		t.Errorf("disk = %+v", res.Disks[0])
	}
}

func TestRunProbeErrors(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		lsblk  error
		code   int
		stderr string
	}{
		{"no --json", nil, nil, 2, "--json is required"},
		{"unknown flag", []string{"--json", "--yaml"}, nil, 2, `unknown argument "--yaml"`},
		{"lsblk fails", []string{"--json"}, errors.New("lsblk: not found"), 1, "probe: disks: lsblk: not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withProber(t, fixtureProber(t, tt.lsblk))
			var out, errOut bytes.Buffer
			if code := runProbe(tt.args, &out, &errOut); code != tt.code {
				t.Errorf("exit %d, want %d", code, tt.code)
			}
			if out.Len() != 0 {
				t.Errorf("stdout not empty on error: %q", out.String())
			}
			if !strings.Contains(errOut.String(), tt.stderr) {
				t.Errorf("stderr %q lacks %q", errOut.String(), tt.stderr)
			}
		})
	}
}

func TestRunProbeHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runProbe([]string{"--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "fisherman probe --json") {
		t.Errorf("help: exit %d, %q", code, out.String())
	}
}

func TestDefaultProberIsTheHost(t *testing.T) {
	p := newProber()
	if p.Root != "/" || p.Run == nil || p.Getenv == nil || p.Sandboxed == nil {
		t.Errorf("default prober = %+v", p)
	}
}

func TestHelpListsProbe(t *testing.T) {
	stdout, _ := captureOutput(t, printHelp)
	if !strings.Contains(stdout, "fisherman probe --json") {
		t.Error("help does not list probe")
	}
}
