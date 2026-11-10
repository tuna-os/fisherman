package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bootcCurrent is a current bootc: every probed subcommand exists.
var bootcCurrent = map[string]string{
	"bootc --version":                          "bootc 1.17.1\n",
	"bootc container inspect --help":           "Output information about the container image\n",
	"bootc install print-configuration --help": "Output JSON to stdout that contains the merged installation configuration\n",
	"bootc install finalize --help":            "Execute this as the penultimate step of an installation\n",
}

// exitStatus returns a real *exec.ExitError with the given code, which is
// what runner.Output hands the probe when a host command fails.
func exitStatus(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != code {
		t.Fatalf("could not make exit status %d: %v", code, err)
	}
	return fmt.Errorf("bootc: %w", err)
}

// TestBootcGolden pins the `bootc` object for each host this slice of
// tuna-os/fisherman#383 covers. Regenerate with `go test ./internal/probe
// -update`.
func TestBootcGolden(t *testing.T) {
	usage := exitStatus(t, 2) // clap: unrecognized subcommand
	tests := []struct {
		fixture string
		host    *fakeHost
	}{
		{"bootc-current", &fakeHost{canned: bootcCurrent}},
		// An older bootc: no `container inspect`, no `install finalize`.
		{"bootc-older", &fakeHost{
			canned: map[string]string{
				"bootc --version":                          "bootc 1.1.4\n",
				"bootc install print-configuration --help": "Output JSON\n",
			},
			fail: map[string]error{
				"bootc container inspect --help": usage,
				"bootc install finalize --help":  usage,
			},
		}},
		// A host without bootc (a generic live image, a developer laptop).
		{"bootc-missing", &fakeHost{fail: map[string]error{
			"bootc --version": fmt.Errorf("bootc --version: %w", &exec.Error{Name: "bootc", Err: exec.ErrNotFound}),
		}}},
		// The same, seen through `flatpak-spawn --host`: the host shell's 127.
		{"bootc-missing", &fakeHost{fail: map[string]error{"bootc --version": exitStatus(t, 127)}}},
		// bootc exists and prints something that is not a version.
		{"bootc-malformed", &fakeHost{canned: map[string]string{
			"bootc --version":                          "\x1b[31merror\x1b[0m: /etc/passwd root:x:0:0 <script>alert(1)</script>\nbootc 9.9.9\n",
			"bootc container inspect --help":           "",
			"bootc install print-configuration --help": "",
			"bootc install finalize --help":            "",
		}}},
		// bootc exists and crashes.
		{"bootc-failed", &fakeHost{fail: map[string]error{"bootc --version": exitStatus(t, 1)}}},
		// One help call dies for a reason that is not "no such subcommand".
		{"bootc-check-failed", &fakeHost{
			canned: map[string]string{
				"bootc --version":                "bootc 1.17.1\n",
				"bootc container inspect --help": "",
				"bootc install finalize --help":  "",
			},
			fail: map[string]error{"bootc install print-configuration --help": exitStatus(t, 137)},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			p := &Prober{Root: t.TempDir(), Run: tt.host.run, Getenv: noEnv}
			got, err := json.MarshalIndent(p.Bootc(), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", "golden", tt.fixture+".json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("bootc object differs from %s:\n%s", golden, got)
			}
		})
	}
}

// Every capability the probe knows is present in every answer, with one of
// the three statuses and a reason, so a frontend never meets a missing key.
func TestBootcCapabilitiesAlwaysComplete(t *testing.T) {
	names := []string{CapContainerInspect, CapPrintConfiguration, CapInstallFinalize,
		CapDeploymentMount, CapUnifiedStorage, CapBootcImageStorage}
	hosts := map[string]*fakeHost{
		"current":   {canned: bootcCurrent},
		"missing":   {fail: map[string]error{"bootc --version": exec.ErrNotFound}},
		"no runner": nil,
	}
	for name, host := range hosts {
		p := &Prober{Root: t.TempDir(), Getenv: noEnv}
		if host != nil {
			p.Run = host.run
		}
		b := p.Bootc()
		if b.Context != BootcContextLiveHost {
			t.Errorf("%s: context = %q", name, b.Context)
		}
		for _, n := range names {
			c, ok := b.Capabilities[n]
			if !ok || c.Reason == "" || (c.Status != CapSupported && c.Status != CapUnsupported && c.Status != CapUnknown) {
				t.Errorf("%s: capability %s = %+v (present %v)", name, n, c, ok)
			}
		}
		if len(b.Capabilities) != len(names) {
			t.Errorf("%s: %d capabilities, want %d", name, len(b.Capabilities), len(names))
		}
	}
}

// What only the selected image can answer stays unknown, whatever the host's
// bootc version is: the probe never infers a feature from a version number.
func TestBootcNeverInfersFromVersion(t *testing.T) {
	for _, version := range []string{"bootc 0.1.0\n", "bootc 1.17.1\n", "bootc 99.0.0\n"} {
		canned := map[string]string{}
		for k, v := range bootcCurrent {
			canned[k] = v
		}
		canned["bootc --version"] = version
		b := (&Prober{Root: t.TempDir(), Run: (&fakeHost{canned: canned}).run, Getenv: noEnv}).Bootc()
		for _, n := range []string{CapDeploymentMount, CapUnifiedStorage, CapBootcImageStorage} {
			if got := b.Capabilities[n]; got != (Capability{CapUnknown, ReasonNotEstablished}) {
				t.Errorf("%s with %q = %+v, want unknown/not_established", n, strings.TrimSpace(version), got)
			}
		}
	}
}

// The bootc probe only asks for a version and for help text.
func TestBootcRunsOnlyReadOnlyCommands(t *testing.T) {
	host := &fakeHost{canned: bootcCurrent}
	(&Prober{Root: t.TempDir(), Run: host.run, Getenv: noEnv}).Bootc()
	for _, call := range host.calls {
		if call != "bootc --version" && !strings.HasSuffix(call, " --help") {
			t.Errorf("bootc probe ran %q", call)
		}
	}
	if len(host.calls) != 4 {
		t.Errorf("bootc probe ran %d commands, want 4: %v", len(host.calls), host.calls)
	}
}

func TestBootcVersionParsing(t *testing.T) {
	for out, want := range map[string]string{
		"bootc 1.17.1\n":                        "1.17.1",
		"bootc 1.1.4\n":                         "1.1.4",
		"bootc 1.2\n":                           "1.2",
		"bootc v1.3.0\n":                        "1.3.0",
		"bootc 1.17.1-rc.1+g0a1b2c\n":           "1.17.1-rc.1+g0a1b2c",
		"  bootc 1.17.1  \n\n":                  "1.17.1",
		"":                                      "",
		"bootc\n":                               "",
		"bootc one.two\n":                       "",
		"podman version 5.4.0\n":                "",
		"bootc 1.17.1; rm -rf /\n":              "",
		"bootc 1.17.1 extra\n":                  "",
		"error: no such option\nbootc 1.17.1\n": "",
	} {
		canned := map[string]string{"bootc --version": out}
		b := (&Prober{Root: t.TempDir(), Run: (&fakeHost{canned: canned}).run, Getenv: noEnv}).Bootc()
		if b.Version != want {
			t.Errorf("version from %q = %q, want %q", out, b.Version, want)
		}
		if (b.Version == "") != (b.VersionReason == VersionMalformed) {
			t.Errorf("version %q from %q has version_reason %q", b.Version, out, b.VersionReason)
		}
		if !b.Found {
			t.Errorf("%q: bootc ran, so found must be true", out)
		}
	}
}
