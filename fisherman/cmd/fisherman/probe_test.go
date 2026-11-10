package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
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

// selectionProber is fixtureProber that also records the commands it ran.
func selectionProber(t *testing.T, calls *[]string) *probe.Prober {
	t.Helper()
	p := fixtureProber(t, nil)
	inner := p.Run
	p.Run = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(append([]string{name}, args...), " "))
		return inner(name, args...)
	}
	return p
}

func TestRunProbeSelection(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		code      int
		stderr    string
		transport string
		updateRef string
	}{
		{"source", []string{"--json", "--source", "containers-storage:ghcr.io/example/os:stable"}, 0, "", "containers-storage", "ghcr.io/example/os:stable"},
		{"source=", []string{"--json", "--source=ghcr.io/example/os:stable", "--target-imgref=ghcr.io/example/os:lts"}, 0, "", "registry", "ghcr.io/example/os:lts"},
		{"live root", []string{"--json", "--live-root"}, 0, "", "live-root", ""},
		{"no source", []string{"--json"}, 0, "", "", ""},
		{"missing value", []string{"--json", "--source"}, 2, "--source needs a value", "", ""},
		{"bad source", []string{"--json", "--source", "$(SECRET-SENTINEL)"}, 2, "invalid image reference", "", ""},
		{"bad target", []string{"--json", "--target-imgref", "a b SECRET-SENTINEL"}, 2, "invalid image reference", "", ""},
		{"both", []string{"--json", "--live-root", "--source", "ghcr.io/x"}, 2, "exclude each other", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			withProber(t, selectionProber(t, &calls))
			var out, errOut bytes.Buffer
			if code := runProbe(tt.args, &out, &errOut); code != tt.code {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tt.code, errOut.String())
			}
			if strings.Contains(errOut.String(), "SECRET-SENTINEL") {
				t.Errorf("stderr echoes the reference: %q", errOut.String())
			}
			if tt.code != 0 {
				if !strings.Contains(errOut.String(), tt.stderr) || out.Len() != 0 || len(calls) != 0 {
					t.Errorf("stderr %q, stdout %q, calls %v", errOut.String(), out.String(), calls)
				}
				return
			}
			var res probe.Result
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			c := res.Capabilities
			if c.Version != probe.CapabilitiesVersion || c.Source.Transport != tt.transport || c.Target.UpdateRef != tt.updateRef {
				t.Errorf("capabilities = %+v", c)
			}
			// This fixture host has no bootc: nothing is established, and
			// no source-root command runs.
			if c.ExecutionRoot.Established {
				t.Errorf("execution root established on a host without bootc: %+v", c.ExecutionRoot)
			}
			for _, call := range calls {
				if call == "bootc install print-configuration" || call == "bootc container inspect --json" {
					t.Errorf("ran %q", call)
				}
			}
		})
	}
}

// The probe's unified_storage answer and the recipe rule agree: the probe
// says fisherman refuses it, and a recipe that asks for it is refused by the
// same validation that runs before any disk step.
func TestUnifiedStorageAnswerMatchesRecipeRule(t *testing.T) {
	var calls []string
	withProber(t, selectionProber(t, &calls))
	var out, errOut bytes.Buffer
	if code := runProbe([]string{"--json", "--source", "ghcr.io/example/os:stable"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	var res probe.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	f := res.Capabilities.Features[probe.FeatUnifiedStorage]
	if f.Status != probe.CapUnsupported || f.Reason != probe.ReasonFishermanRefuses {
		t.Fatalf("unified_storage = %+v", f)
	}
	ps := recipe.CheckUnifiedStorage(true)
	if len(ps) != 1 || ps[0].Code != recipe.CodeUnifiedStorageUnsupported {
		t.Errorf("recipe rule does not refuse unifiedStorage: %+v", ps)
	}
	if len(recipe.CheckUnifiedStorage(false)) != 0 {
		t.Error("recipe rule refuses unifiedStorage: false")
	}
}

// The production runner bounds every probe command.
func TestProbeRunIsBounded(t *testing.T) {
	if probeCommandTimeout <= 0 || probeCommandTimeout > time.Minute || probeMaxOutput <= 0 || probeMaxOutput > 64<<20 {
		t.Errorf("limits %v / %d", probeCommandTimeout, probeMaxOutput)
	}
	if _, err := probeRun("sh", "-c", "exit 0"); err != nil {
		t.Errorf("probeRun: %v", err)
	}
}
