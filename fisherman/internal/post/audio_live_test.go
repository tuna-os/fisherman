package post

// Coverage for the live-session audio naming path that previously had zero
// coverage (tuna-os/fisherman#144): ApplyAudioConfigLive, the pactl fallback
// detector detectAudioDevicesPactl, and getDeviceDescription.
//
// Uses the Exec injection pattern from post_helpers_test.go, but with a mock
// that distinguishes commands by full argv and records every invocation so
// the WirePlumber restart can be asserted.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
)

// audioCmd is a canned Command implementation.
type audioCmd struct {
	out []byte
	err error
}

func (c *audioCmd) Run() error   { return c.err }
func (c *audioCmd) Start() error { return c.err }
func (c *audioCmd) Wait() error  { return c.err }
func (c *audioCmd) Output() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.out, nil
}
func (c *audioCmd) SetStdin(io.Reader)  {}
func (c *audioCmd) SetStdout(io.Writer) {}
func (c *audioCmd) SetStderr(io.Writer) {}

// audioExecMock returns canned outputs keyed by full command line and records
// every invocation.
type audioExecMock struct {
	mu      sync.Mutex
	runs    []string
	byKey   map[string]*audioCmd
	failAll bool // when set, every command fails (pw-cli → pactl fallback)
}

func (m *audioExecMock) Command(name string, args ...string) runner.Command {
	key := name
	if len(args) > 0 {
		key += " " + strings.Join(args, " ")
	}
	m.mu.Lock()
	m.runs = append(m.runs, key)
	m.mu.Unlock()

	if m.failAll {
		return &audioCmd{err: fmt.Errorf("mock: command failed")}
	}
	if c, ok := m.byKey[key]; ok {
		return c
	}
	return &audioCmd{err: fmt.Errorf("mock: no response for %q", key)}
}

func (m *audioExecMock) ran(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r == key {
			return true
		}
	}
	return false
}

func setupAudioMock(t *testing.T, m *audioExecMock) {
	t.Helper()
	old := Exec
	Exec = m
	t.Cleanup(func() { Exec = old })
}

const pactlSinksShort = "0\talsa_output.pci-0000_c5_00.6.analog-stereo\tFamily 17h/19h HD Audio Controller Analog Stereo\tanalog-stereo\n" +
	"1\talsa_output.pci-0000_c5_00.6.iec958-stereo\tS/PDIF Digital Output\tiec958-stereo\n"

const pactlSourcesShort = "0\talsa_input.pci-0000_c5_00.6.analog-stereo\tFamily 17h/19h HD Audio Controller Analog Stereo\tanalog-stereo\n"

const pactlSinksJSON = `[
  {
    "name": "alsa_output.pci-0000_c5_00.6.analog-stereo",
    "description" = "Family 17h/19h HD Audio Controller Analog Stereo"
  },
  {
    "name": "alsa_output.pci-0000_c5_00.6.iec958-stereo",
    "description" = "S/PDIF Digital Output"
  }
]
`

const pactlSourcesJSON = `[
  {
    "name": "alsa_input.pci-0000_c5_00.6.analog-stereo",
    "description" = "Family 17h/19h HD Audio Controller Analog Stereo"
  }
]
`

// pactlFallbackMock wires the pactl outputs and makes pw-cli fail so the
// pactl fallback path is exercised.
func pactlFallbackMock() *audioExecMock {
	return &audioExecMock{
		byKey: map[string]*audioCmd{
			"pw-cli ls Node":                   {err: fmt.Errorf("pw-cli: connection closed")},
			"pactl list sinks short":           {out: []byte(pactlSinksShort)},
			"pactl list sources short":         {out: []byte(pactlSourcesShort)},
			"pactl --format=json list sinks":   {out: []byte(pactlSinksJSON)},
			"pactl --format=json list sources": {out: []byte(pactlSourcesJSON)},
		},
	}
}

// ── ApplyAudioConfigLive ──────────────────────────────────────────────────

func TestApplyAudioConfigLive_PactlFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mock := pactlFallbackMock()
	setupAudioMock(t, mock)

	if err := ApplyAudioConfigLive(); err != nil {
		t.Fatalf("ApplyAudioConfigLive: %v", err)
	}

	confPath := filepath.Join(home, ".config", "wireplumber", "wireplumber.conf.d", "60-friendly-audio-names.conf")
	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("user wireplumber config not written: %v", err)
	}
	content := string(data)

	// Sink with friendly description → renamed to Laptop Speakers.
	if !strings.Contains(content, "Laptop Speakers") {
		t.Error("expected 'Laptop Speakers' rename rule")
	}
	// Source → Laptop Microphone (sink/source disambiguation).
	if !strings.Contains(content, "Laptop Microphone") {
		t.Error("expected 'Laptop Microphone' rename rule for source device")
	}
	// S/PDIF (iec958) → hidden.
	if !strings.Contains(content, "node.disabled = true") {
		t.Error("expected hide rule for S/PDIF device")
	}

	// WirePlumber must be restarted so the rules apply immediately.
	if !mock.ran("systemctl --user restart wireplumber") {
		t.Errorf("expected systemctl --user restart wireplumber, runs: %v", mock.runs)
	}
}

func TestApplyAudioConfigLive_PwCliSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mock := &audioExecMock{
		byKey: map[string]*audioCmd{
			"pw-cli ls Node": {out: []byte(`	id 42, type PipeWire:Interface:Node/3, version 3
		node.name = "alsa_output.pci-0000_c5_00.6.analog-stereo"
		node.description = "Family 17h/19h HD Audio Controller Analog Stereo"
		media.class = "Audio/Sink"
	id 43, type PipeWire:Interface:Node/3, version 3
		node.name = "alsa_output.pci-0000_c5_00.6.iec958-stereo"
		node.description = "S/PDIF Digital Output"
		media.class = "Audio/Sink"
`)},
		},
	}
	setupAudioMock(t, mock)

	if err := ApplyAudioConfigLive(); err != nil {
		t.Fatalf("ApplyAudioConfigLive: %v", err)
	}

	confPath := filepath.Join(home, ".config", "wireplumber", "wireplumber.conf.d", "60-friendly-audio-names.conf")
	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("user wireplumber config not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Laptop Speakers") || !strings.Contains(content, "node.disabled = true") {
		t.Errorf("config missing expected rules:\n%s", content)
	}
	if !mock.ran("systemctl --user restart wireplumber") {
		t.Errorf("expected wireplumber restart, runs: %v", mock.runs)
	}
}

func TestApplyAudioConfigLive_NoDevicesNoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// pw-cli succeeds with no nodes; pactl must not even be consulted.
	mock := &audioExecMock{
		byKey: map[string]*audioCmd{
			"pw-cli ls Node": {out: []byte("")},
		},
	}
	setupAudioMock(t, mock)

	if err := ApplyAudioConfigLive(); err != nil {
		t.Fatalf("ApplyAudioConfigLive: %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".config", "wireplumber", "wireplumber.conf.d", "60-friendly-audio-names.conf")); !os.IsNotExist(err) {
		t.Errorf("no-device run must not write a config file (stat err = %v)", err)
	}
	if mock.ran("systemctl --user restart wireplumber") {
		t.Error("no-device run must not restart WirePlumber")
	}
}

func TestApplyAudioConfigLive_DetectionErrorPropagates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Everything fails — pw-cli AND pactl → the detection error must surface.
	mock := &audioExecMock{failAll: true}
	setupAudioMock(t, mock)

	err := ApplyAudioConfigLive()
	if err == nil {
		t.Fatal("expected error when audio detection fails, got nil")
	}
	if !strings.Contains(err.Error(), "detecting audio devices") {
		t.Errorf("error = %v, want 'detecting audio devices'", err)
	}
}

// ── detectAudioDevicesPactl ───────────────────────────────────────────────

func TestDetectAudioDevicesPactl(t *testing.T) {
	mock := pactlFallbackMock()
	setupAudioMock(t, mock)

	devices, err := detectAudioDevicesPactl()
	if err != nil {
		t.Fatalf("detectAudioDevicesPactl: %v", err)
	}

	if len(devices) != 3 {
		t.Fatalf("expected 3 devices (2 sinks + 1 source), got %d: %+v", len(devices), devices)
	}

	// First device: sink with resolved description from `pactl --format=json`.
	if devices[0].NodeName != "alsa_output.pci-0000_c5_00.6.analog-stereo" {
		t.Errorf("device[0].NodeName = %q", devices[0].NodeName)
	}
	if devices[0].Description != "Family 17h/19h HD Audio Controller Analog Stereo" {
		t.Errorf("device[0].Description = %q", devices[0].Description)
	}
	if devices[0].Class != "Audio/Sink" {
		t.Errorf("device[0].Class = %q, want Audio/Sink", devices[0].Class)
	}

	// S/PDIF sink.
	if devices[1].Description != "S/PDIF Digital Output" {
		t.Errorf("device[1].Description = %q", devices[1].Description)
	}

	// Source classified correctly.
	if devices[2].Class != "Audio/Source" {
		t.Errorf("device[2].Class = %q, want Audio/Source", devices[2].Class)
	}
}

func TestDetectAudioDevicesPactl_NoDevicesErrors(t *testing.T) {
	// pactl itself must fail (no sinks/sources) → error.
	mock := &audioExecMock{
		byKey: map[string]*audioCmd{
			"pactl list sinks short":   {err: fmt.Errorf("no pactl")},
			"pactl list sources short": {err: fmt.Errorf("no pactl")},
		},
	}
	setupAudioMock(t, mock)

	_, err := detectAudioDevicesPactl()
	if err == nil {
		t.Fatal("expected error when pactl finds no devices, got nil")
	}
	if !strings.Contains(err.Error(), "no audio devices found via pactl") {
		t.Errorf("error = %v, want 'no audio devices found via pactl'", err)
	}
}

// ── getDeviceDescription ──────────────────────────────────────────────────

func TestGetDeviceDescription(t *testing.T) {
	mock := &audioExecMock{
		byKey: map[string]*audioCmd{
			"pactl --format=json list sinks": {out: []byte(pactlSinksJSON)},
		},
	}
	setupAudioMock(t, mock)

	got := getDeviceDescription("sink", "alsa_output.pci-0000_c5_00.6.iec958-stereo")
	if got != "S/PDIF Digital Output" {
		t.Errorf("getDeviceDescription = %q, want 'S/PDIF Digital Output'", got)
	}
}

func TestGetDeviceDescription_FallsBackToRawName(t *testing.T) {
	// pactl --format=json fails → fall back to the raw device name rather
	// than failing the whole detection.
	mock := &audioExecMock{
		byKey: map[string]*audioCmd{
			"pactl --format=json list sinks": {err: fmt.Errorf("pactl died")},
		},
	}
	setupAudioMock(t, mock)

	got := getDeviceDescription("sink", "alsa_output.pci-0000_c5_00.6.analog-stereo")
	if got != "alsa_output.pci-0000_c5_00.6.analog-stereo" {
		t.Errorf("getDeviceDescription = %q, want raw name fallback", got)
	}
}

func TestGetDeviceDescription_UnknownDevice(t *testing.T) {
	mock := &audioExecMock{
		byKey: map[string]*audioCmd{
			"pactl --format=json list sinks": {out: []byte(pactlSinksJSON)},
		},
	}
	setupAudioMock(t, mock)

	// Name not present in output → no description found → raw name.
	got := getDeviceDescription("sink", "alsa_output.something-else")
	if got != "alsa_output.something-else" {
		t.Errorf("getDeviceDescription = %q, want raw name for unknown device", got)
	}
}
