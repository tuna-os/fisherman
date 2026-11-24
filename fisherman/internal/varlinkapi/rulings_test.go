package varlinkapi

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEULA(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "eula.xml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveEULAOverride(t *testing.T) {
	p := writeEULA(t, "<component><agreement type=\"eula\">text</agreement></component>")
	t.Setenv(EULAFileEnv, p)
	e := ResolveEULA()
	if !e.Available || e.Path == nil || *e.Path != p {
		t.Errorf("got %+v", e)
	}
}

func TestResolveEULAMissing(t *testing.T) {
	t.Setenv(EULAFileEnv, filepath.Join(t.TempDir(), "no-such.xml"))
	if e := ResolveEULA(); e.Available || e.Path != nil {
		t.Errorf("missing file must be unavailable: %+v", e)
	}
}

func TestResolveEULAEmpty(t *testing.T) {
	t.Setenv(EULAFileEnv, writeEULA(t, ""))
	if e := ResolveEULA(); e.Available {
		t.Errorf("empty file must be unavailable: %+v", e)
	}
}

func TestResolveEULADefaultPath(t *testing.T) {
	t.Setenv(EULAFileEnv, "")
	if e := ResolveEULA(); e.Available || e.Path != nil {
		t.Errorf("no test machine ships a license: %+v", e)
	}
	if DefaultEULAPath != "/usr/share/tuna-installer/eula.xml" {
		t.Errorf("shared path moved: %s", DefaultEULAPath)
	}
}

func TestProbeHandoffScenarios(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		mode     string
	}{
		{"", "reboot"},
		{"happy", "reboot"},
		{"handoff-migrate", "migrate"},
		{"handoff-soft-reboot", "soft_reboot"},
	} {
		p := probeOf(t, tc.scenario)
		if p.Handoff.Mode != tc.mode {
			t.Errorf("%q: handoff mode = %q, want %q", tc.scenario, p.Handoff.Mode, tc.mode)
		}
		if p.Handoff.Reason == "" {
			t.Errorf("%q: handoff reason is empty", tc.scenario)
		}
		if p.Eula.Available {
			t.Errorf("%q: no license on the test machine", tc.scenario)
		}
	}
}

func TestHandoffNoModeUsesDecision(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		mode     string
		keep     bool
	}{
		{"", "reboot", false},
		{"handoff-migrate", "migrate", true},
		{"handoff-soft-reboot", "soft_reboot", false},
	} {
		e := newEnv(t, tc.scenario)
		c := e.client(t)
		if evs, err := install(t, c, goodConfig()); err != nil || evs[len(evs)-1].Event != EvComplete {
			t.Fatalf("%s: %v", tc.scenario, err)
		}
		var out struct {
			Mode                string
			Started             bool
			SessionKeepsRunning bool
		}
		// No mode: the daemon decides, from the same block Probe reports.
		if err := c.Call(IfaceFisherman+".Handoff", nil, &out); err != nil ||
			out.Mode != tc.mode || !out.Started || out.SessionKeepsRunning != tc.keep {
			t.Fatalf("%s: %v %+v", tc.scenario, err, out)
		}
		got := e.mock.Actions()
		if len(got) == 0 || got[len(got)-1] != "handoff:"+tc.mode {
			t.Fatalf("%s: actions = %v", tc.scenario, got)
		}
	}
}

func TestConfigureTargetPrivacy(t *testing.T) {
	e := newEnv(t, "")
	c := e.client(t)
	var out struct {
		Configured bool
		Errors     []ValidationError
	}
	// Absent privacy leaves the target alone.
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{"hostname": "pc"}, &out); err != nil || !out.Configured {
		t.Fatalf("%v %+v", err, out)
	}
	if e.svc.target.Privacy != nil {
		t.Fatalf("privacy = %+v, want nil", e.svc.target.Privacy)
	}
	// Sent privacy stores; an absent locationServices defaults to false.
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{"privacy": map[string]any{}}, &out); err != nil || !out.Configured {
		t.Fatalf("%v %+v", err, out)
	}
	if p := e.svc.target.Privacy; p == nil || p.LocationServices {
		t.Fatalf("privacy = %+v, want present and false", p)
	}
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{"privacy": map[string]any{"locationServices": true}}, &out); err != nil || !out.Configured {
		t.Fatalf("%v %+v", err, out)
	}
	if p := e.svc.target.Privacy; p == nil || !p.LocationServices {
		t.Fatalf("privacy = %+v, want true", p)
	}
}
