package varlinkapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureTargetAccessibility(t *testing.T) {
	e := newEnv(t, "")
	c := e.client(t)
	var out struct {
		Configured bool
		Errors     []ValidationError
	}
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{"hostname": "pc"}, &out); err != nil || !out.Configured {
		t.Fatalf("%v %+v", err, out)
	}
	if e.svc.target.Accessibility != nil {
		t.Fatalf("accessibility = %+v, want nil when nobody sent it", e.svc.target.Accessibility)
	}
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{"accessibility": map[string]any{"scalePercent": 50}}, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Errors) != 1 || out.Errors[0].Code != CodeScaleInvalid {
		t.Fatalf("errors = %+v, want one %s", out.Errors, CodeScaleInvalid)
	}
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{"accessibility": map[string]any{"screenReader": true, "scalePercent": 150}}, &out); err != nil || !out.Configured {
		t.Fatalf("%v %+v", err, out)
	}
	if a := e.svc.target.Accessibility; a == nil || !a.ScreenReader || a.ScalePercent != 150 {
		t.Fatalf("accessibility = %+v", a)
	}
}

// The first-login script, run with stand-ins for the session's tools: it
// turns the screen reader on, scales each enabled output at its current mode,
// and runs once.
func TestTheFirstLoginScriptAppliesTheChoicesOnce(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	dep := t.TempDir()
	if err := writeAccessibility(dep, Accessibility{ScreenReader: true, ScalePercent: 150}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{AccessibilityConf, AccessibilityScript, AccessibilityAutostart} {
		if _, err := os.Stat(filepath.Join(dep, f)); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := os.Stat(filepath.Join(dep, AccessibilityScript)); st.Mode().Perm() != 0o755 {
		t.Errorf("script mode %v", st.Mode())
	}
	if got := readFile(t, filepath.Join(dep, AccessibilityAutostart)); !strings.Contains(got, "Exec=/"+AccessibilityScript+"\n") {
		t.Errorf("autostart = %q", got)
	}
	bin, log, home := t.TempDir(), filepath.Join(t.TempDir(), "log"), t.TempDir()
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("gsettings", `echo "gsettings $*" >>`+log+"\n")
	stub("busctl", `echo "busctl $*" >>`+log+"\n")
	// The format of cosmic-randr list --kdl (cosmic-randr cli/src/main.rs).
	stub("cosmic-randr", `if [ "$1" = list ]; then cat <<'KDL'
output "eDP-1" enabled=#true {
  description make="BOE" model="0x0BCA"
  scale 1.00
  modes {
    mode 2560 1600 60002 current=#true preferred=#true
    mode 1920 1200 59950
  }
}
output "HDMI-A-1" enabled=#false {
  description model="Off"
  modes {
    mode 1920 1080 60000 current=#true
  }
}
KDL
else echo "cosmic-randr $*" >>`+log+`; fi
`)
	run := func() {
		cmd := exec.Command(filepath.Join(dep, AccessibilityScript))
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run()
	run()
	want := "gsettings set org.gnome.desktop.a11y.applications screen-reader-enabled true\n" +
		"busctl --user set-property org.a11y.Bus /org/a11y/bus org.a11y.Status ScreenReaderEnabled b true\n" +
		"cosmic-randr mode --scale 1.50 --refresh 60.002 eDP-1 2560 1600\n"
	if got := readFile(t, log); got != want {
		t.Errorf("the script ran\n%s\nwant (once)\n%s", got, want)
	}
}

func TestNoScaleNoReaderLeavesTheSession(t *testing.T) {
	text := accessibilityScript(false, "")
	if strings.Contains(text, "gsettings") || strings.Contains(text, "cosmic-randr") {
		t.Errorf("script = %q", text)
	}
	if err := writeAccessibility(t.TempDir(), Accessibility{ScalePercent: 400}); err == nil {
		t.Error("a scale of 400% was written")
	}
}
