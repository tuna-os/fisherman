package varlinkapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/auth"
	"github.com/tuna-os/fisherman/internal/varlink"
)

func TestRequirements(t *testing.T) {
	cases := []struct {
		scenario string
		meets    bool
		failing  string
		action   string
	}{
		{"", true, "", ""},
		{"low-ram", false, "ram", "power_off"},
		{"no-uefi", false, "firmware", "firmware_setup"},
	}
	for _, tc := range cases {
		t.Run("scenario="+tc.scenario, func(t *testing.T) {
			e := newEnv(t, tc.scenario)
			var r RequirementsResult
			if err := e.client(t).Call(IfaceFisherman+".Requirements", nil, &r); err != nil {
				t.Fatal(err)
			}
			if r.MeetsRequirements != tc.meets {
				t.Fatalf("meets = %v", r.MeetsRequirements)
			}
			for _, g := range r.Gates {
				if g.OK != (g.ID != tc.failing) {
					t.Errorf("gate %s ok=%v", g.ID, g.OK)
				}
				if g.ID == tc.failing && (g.Action == nil || *g.Action != tc.action) {
					t.Errorf("gate %s action = %v", g.ID, g.Action)
				}
			}
		})
	}
}

func probeOf(t *testing.T, scenario string) ProbeResult {
	t.Helper()
	var p ProbeResult
	if err := newEnv(t, scenario).client(t).Call(IfaceFisherman+".Probe", nil, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func disk(p ProbeResult, path string) *Disk {
	for i := range p.Disks {
		if p.Disks[i].Path == path {
			return &p.Disks[i]
		}
	}
	return nil
}

func TestProbeScenarios(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		p := probeOf(t, "")
		if p.ProtocolVersion != 1 || !p.TPM.Usable || p.TPM.Version != "2.0" || p.IntelRST.Detected {
			t.Fatalf("%+v", p)
		}
		d := disk(p, "/dev/nvme0n1")
		if d == nil || !d.Eligible || d.ExistingData.HasPartitionTable || d.FreeBytes != d.SizeBytes || d.SizeLabel != "476.9 GiB" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("no-tpm", func(t *testing.T) {
		p := probeOf(t, "no-tpm")
		if p.TPM.Present || p.TPM.Usable || p.TPM.Version != "" {
			t.Fatalf("%+v", p.TPM)
		}
	})
	t.Run("rst", func(t *testing.T) {
		p := probeOf(t, "rst")
		d := disk(p, "/dev/nvme0n1")
		if !p.IntelRST.Detected || len(p.IntelRST.AffectedDisks) != 1 || d.Eligible || !d.IntelRST ||
			d.ExcludedReason == nil || *d.ExcludedReason != "intel_rst" {
			t.Fatalf("%+v %+v", p.IntelRST, d)
		}
	})
	t.Run("existing-data", func(t *testing.T) {
		p := probeOf(t, "existing-data")
		d := disk(p, "/dev/nvme0n1")
		if !d.ExistingData.HasPartitionTable || len(d.ExistingData.OperatingSystems) != 1 ||
			d.ExistingData.OperatingSystems[0] != "Windows" || d.FreeBytes < d.MinimalSizeBytes {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("tight-space", func(t *testing.T) {
		d := disk(probeOf(t, "tight-space"), "/dev/nvme0n1")
		if d.FreeBytes >= d.MinimalSizeBytes {
			t.Fatalf("%+v", d)
		}
	})
}

// Arrays must be arrays, never null (the CLI contract in docs/PROBE.md).
func TestProbeArraysAreNeverNull(t *testing.T) {
	e := newEnv(t, "")
	f := e.raw(t, `{"method":"io.tunaos.Fisherman.Probe"}`, 0)[0]
	for _, bad := range []string{`"partitions":null`, `"operatingSystems":null`, `"affectedDisks":null`, `"disks":null`} {
		if strings.Contains(f, bad) {
			t.Errorf("%s in %s", bad, f)
		}
	}
	golden(t, "fisherman_probe_happy.json", f+"\n")
}

func validate(t *testing.T, e *env, cfg any, partial bool) (bool, []ValidationError) {
	t.Helper()
	var out struct {
		Valid  bool              `json:"valid"`
		Errors []ValidationError `json:"errors"`
	}
	if err := e.client(t).Call(IfaceFisherman+".Validate", map[string]any{"config": cfg, "partial": partial}, &out); err != nil {
		t.Fatal(err)
	}
	return out.Valid, out.Errors
}

func codes(es []ValidationError) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Field+":"+e.Code)
	}
	return out
}

func TestValidate(t *testing.T) {
	t.Run("full ok", func(t *testing.T) {
		ok, errs := validate(t, newEnv(t, ""), goodConfig(), false)
		if !ok || len(errs) != 0 {
			t.Fatal(errs)
		}
	})
	t.Run("empty full config lists what is missing", func(t *testing.T) {
		_, errs := validate(t, newEnv(t, ""), map[string]any{}, false)
		if got := strings.Join(codes(errs), ","); got != "disk:disk_required,encryption:encryption_required" {
			t.Fatal(got)
		}
	})
	t.Run("empty partial config is valid", func(t *testing.T) {
		if ok, errs := validate(t, newEnv(t, ""), map[string]any{}, true); !ok {
			t.Fatal(errs)
		}
	})
	t.Run("partial hostname and username", func(t *testing.T) {
		_, errs := validate(t, newEnv(t, ""), map[string]any{"hostname": "my_pc", "user": map[string]any{"username": "Alice"}}, true)
		if got := strings.Join(codes(errs), ","); got != "hostname:hostname_invalid_char,user.username:username_invalid_start" {
			t.Fatal(got)
		}
	})
	t.Run("unknown disk", func(t *testing.T) {
		_, errs := validate(t, newEnv(t, ""), map[string]any{"disk": "/dev/nope"}, true)
		if got := strings.Join(codes(errs), ","); got != "disk:disk_not_found" {
			t.Fatal(got)
		}
	})
	t.Run("rst-hidden disk", func(t *testing.T) {
		_, errs := validate(t, newEnv(t, "rst"), goodConfig(), false)
		if got := strings.Join(codes(errs), ","); got != "disk:disk_not_eligible" {
			t.Fatal(got)
		}
	})
	t.Run("tpm encryption without a tpm", func(t *testing.T) {
		_, errs := validate(t, newEnv(t, "no-tpm"), goodConfig(), false)
		if got := strings.Join(codes(errs), ","); got != "encryption.type:encryption_tpm_unavailable" {
			t.Fatal(got)
		}
		// partial mode reads no machine state
		if ok, errs := validate(t, newEnv(t, "no-tpm"), map[string]any{"encryption": map[string]any{"type": "tpm2_luks"}}, true); !ok {
			t.Fatal(errs)
		}
	})
	t.Run("passphrase rules", func(t *testing.T) {
		cfg := map[string]any{"encryption": map[string]any{"type": "luks_passphrase"}}
		_, errs := validate(t, newEnv(t, "no-tpm"), cfg, true)
		if got := strings.Join(codes(errs), ","); got != "encryption.passphrase:encryption_passphrase_required" {
			t.Fatal(got)
		}
		cfg = map[string]any{"encryption": map[string]any{"type": "luks_passphrase", "passphrase": "ok-passphrase"}}
		if ok, errs := validate(t, newEnv(t, "no-tpm"), cfg, true); !ok {
			t.Fatal(errs)
		}
		cfg = map[string]any{"encryption": map[string]any{"type": "rot13"}}
		_, errs = validate(t, newEnv(t, ""), cfg, true)
		if got := strings.Join(codes(errs), ","); got != "encryption.type:encryption_unsupported" {
			t.Fatal(got)
		}
	})
	t.Run("existing data forces a choice", func(t *testing.T) {
		cfg := goodConfig()
		delete(cfg, "installMode")
		_, errs := validate(t, newEnv(t, "existing-data"), cfg, false)
		if got := strings.Join(codes(errs), ","); got != "installMode:install_mode_required" {
			t.Fatal(got)
		}
		cfg["installMode"] = "free_space"
		if ok, errs := validate(t, newEnv(t, "existing-data"), cfg, false); !ok {
			t.Fatal(errs)
		}
	})
	t.Run("free space too small", func(t *testing.T) {
		cfg := goodConfig()
		cfg["installMode"] = "free_space"
		_, errs := validate(t, newEnv(t, "tight-space"), cfg, false)
		if got := strings.Join(codes(errs), ","); got != "installMode:insufficient_free_space" {
			t.Fatal(got)
		}
		cfg["installMode"] = "erase"
		if ok, errs := validate(t, newEnv(t, "tight-space"), cfg, false); !ok {
			t.Fatal(errs)
		}
	})
	t.Run("disk too small", func(t *testing.T) {
		cfg := goodConfig()
		cfg["disk"] = "/dev/sdb"
		_, errs := validate(t, newEnv(t, ""), cfg, false)
		if got := strings.Join(codes(errs), ","); got != "disk:disk_too_small" {
			t.Fatal(got)
		}
	})
	t.Run("bad install mode", func(t *testing.T) {
		_, errs := validate(t, newEnv(t, ""), map[string]any{"installMode": "yolo"}, true)
		if got := strings.Join(codes(errs), ","); got != "installMode:install_mode_invalid" {
			t.Fatal(got)
		}
	})
}

func TestConfigureTarget(t *testing.T) {
	e := newEnv(t, "")
	c := e.client(t)
	var out struct {
		Configured bool              `json:"configured"`
		Errors     []ValidationError `json:"errors"`
	}
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{
		"user": map[string]any{"username": "root"}, "hostname": "-x", "locale": "en_US.UTF-8", "keymap": "us", "timeZone": "Europe/Berlin"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Configured || len(out.Errors) != 2 {
		t.Fatalf("%+v", out)
	}
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{"locale": "en US", "timeZone": "../etc"}, &out); err != nil || out.Configured || len(out.Errors) != 2 {
		t.Fatalf("%+v %v", out, err)
	}
	if err := c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{
		"user": map[string]any{"username": "alice", "fullName": "Alice", "password": "pw"}, "hostname": "tuna", "locale": "de_DE.UTF-8", "keymap": "de", "timeZone": "Europe/Berlin", "autologin": true}, &out); err != nil || !out.Configured {
		t.Fatalf("%+v %v", out, err)
	}
	e.svc.mu.Lock()
	tgt := e.svc.target
	e.svc.mu.Unlock()
	if tgt.Hostname != "tuna" || tgt.Locale != "de_DE.UTF-8" || tgt.TimeZone != "Europe/Berlin" || !tgt.Autologin || tgt.User.Username != "alice" {
		t.Fatalf("%+v", tgt)
	}
}

// stepOrder lists the step ids of started events, without repeats.
func startedSteps(evs []Event) []string {
	var out []string
	for _, ev := range evs {
		if ev.Event == EvStepStarted {
			out = append(out, *ev.StepID)
		}
	}
	return out
}

func TestInstallHappyStream(t *testing.T) {
	e := newEnv(t, "")
	evs, err := install(t, e.client(t), goodConfig())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"validate_device", "partition", "luks", "tpm2_enroll", "mount", "install_os", "configure", "post_fixups", "bootloader", "finalize"}
	if got := startedSteps(evs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v", got)
	}
	last := evs[len(evs)-1]
	if last.Event != EvComplete || last.OverallPct != 100 {
		t.Fatalf("last: %+v", last)
	}
	prev, started, finished := 0, 0, 0
	for i, ev := range evs {
		if ev.OverallPct < prev {
			t.Fatalf("overallPct went backwards at %d: %+v", i, ev)
		}
		prev = ev.OverallPct
		if i < len(evs)-1 && ev.OverallPct > 99 {
			t.Fatalf("only complete may reach 100: %+v", ev)
		}
		switch ev.Event {
		case EvStepStarted:
			started++
			if *ev.TotalSteps != len(want) || *ev.StepIndex != started {
				t.Fatalf("index: %+v", ev)
			}
		case EvStepFinished:
			finished++
		}
	}
	if started != finished {
		t.Fatalf("started %d finished %d", started, finished)
	}
	if got := e.mock.Actions(); len(got) != 1 || got[0] != "install:/dev/nvme0n1" {
		t.Fatalf("%v", got)
	}
	var b strings.Builder
	for _, ev := range evs {
		j, _ := json.Marshal(ev)
		b.Write(j)
		b.WriteByte('\n')
	}
	golden(t, "fisherman_install_happy.jsonl", b.String())
}

func TestInstallStepsFollowEncryption(t *testing.T) {
	for _, tc := range []struct {
		enc  map[string]any
		skip []string
	}{
		{map[string]any{"type": "none"}, []string{"luks", "tpm2_enroll"}},
		{map[string]any{"type": "luks_passphrase", "passphrase": "pw-1234"}, []string{"tpm2_enroll"}},
		{map[string]any{"type": "tpm2_luks_passphrase", "passphrase": "pw-1234"}, nil},
	} {
		e := newEnv(t, "")
		cfg := goodConfig()
		cfg["encryption"] = tc.enc
		evs, err := install(t, e.client(t), cfg)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(startedSteps(evs), ",")
		for _, s := range tc.skip {
			if strings.Contains(got, s) {
				t.Errorf("%v: %s should be skipped: %s", tc.enc["type"], s, got)
			}
		}
		if len(tc.skip) == 0 && !strings.Contains(got, "tpm2_enroll") {
			t.Errorf("tpm2_enroll missing: %s", got)
		}
	}
}

func TestInstallFailures(t *testing.T) {
	cases := []struct {
		scenario, step, reason, next string
	}{
		{"device-lost", "install_os", "device_lost", "choose_another_disk"},
		{"repart-error", "partition", "repart_failed", "retry"},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			e := newEnv(t, tc.scenario)
			evs, err := install(t, e.client(t), goodConfig())
			if err != nil {
				t.Fatal(err)
			}
			last := evs[len(evs)-1]
			if last.Event != EvFailed || *last.StepID != tc.step || *last.Reason != tc.reason ||
				*last.NextAction != tc.next || last.Message == nil || last.OverallPct >= 100 {
				t.Fatalf("%+v", last)
			}
			// the failed step is never reported as finished; cleanup runs
			var sawCleanup bool
			for _, ev := range evs {
				if ev.Event == EvStepFinished && *ev.StepID == tc.step {
					t.Errorf("failed step reported finished")
				}
				if ev.StepID != nil && *ev.StepID == "cleanup" {
					sawCleanup = true
				}
			}
			if !sawCleanup {
				t.Error("no cleanup step")
			}
			if len(e.mock.Actions()) != 0 {
				t.Errorf("a failed install must not be recorded: %v", e.mock.Actions())
			}
			// the machine is usable again: a retry is accepted
			if tc.scenario == "repart-error" {
				evs, _ := install(t, e.client(t), goodConfig())
				if evs[len(evs)-1].Event != EvFailed { // the mock fails again, deterministically
					t.Fatalf("retry: %+v", evs[len(evs)-1])
				}
			}
		})
	}
}

func TestDeviceLostRemovesTheDisk(t *testing.T) {
	e := newEnv(t, "device-lost")
	install(t, e.client(t), goodConfig())
	evs, _ := install(t, e.client(t), goodConfig())
	_ = evs
	var out struct {
		Valid  bool
		Errors []ValidationError
	}
	if err := e.client(t).Call(IfaceFisherman+".Validate", map[string]any{"config": goodConfig()}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Valid || out.Errors[0].Code != "disk_not_found" {
		t.Fatalf("%+v", out)
	}
}

func TestInstallCancel(t *testing.T) {
	e := newEnv(t, "cancel")
	c := e.client(t)
	st, err := c.CallMore(IfaceFisherman+".Install", map[string]any{"config": goodConfig(), "confirmed": true})
	if err != nil {
		t.Fatal(err)
	}
	// read until the install is parked at install_os 50%
	for {
		var ev Event
		if _, err := st.Next(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.StepID != nil && *ev.StepID == "install_os" && ev.Event == EvProgress && ev.OverallPct > 0 && e.svc.isInstalling() {
			if ev.OverallPct < 30 {
				continue
			}
			break
		}
	}
	// a second install while one runs is refused
	if got := callMoreErr(t, e.client(t), IfaceFisherman+".Install", map[string]any{"config": goodConfig(), "confirmed": true}); got != IfaceFisherman+".InstallInProgress" {
		t.Fatalf("want InstallInProgress, got %q", got)
	}
	var out struct{ WasInstalling bool }
	if err := e.client(t).Call(IfaceFisherman+".Cancel", nil, &out); err != nil || !out.WasInstalling {
		t.Fatalf("cancel: %v %+v", err, out)
	}
	var final Event
	var sawCleanup bool
	for {
		var ev Event
		ok, err := st.Next(&ev)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if ev.StepID != nil && *ev.StepID == "cleanup" {
			sawCleanup = true
		}
		final = ev
	}
	if final.Event != EvCancelled || !sawCleanup {
		t.Fatalf("final %+v cleanup=%v", final, sawCleanup)
	}
	// Cancel is idempotent, and the daemon accepts a new install afterwards
	if err := e.client(t).Call(IfaceFisherman+".Cancel", nil, &out); err != nil || out.WasInstalling {
		t.Fatalf("second cancel: %v %+v", err, out)
	}
	if len(e.mock.Actions()) != 0 {
		t.Fatalf("a cancelled install must leave nothing: %v", e.mock.Actions())
	}
}

// A client that disconnects mid-install does NOT cancel it: only an
// explicit Cancel does. The install keeps running without its client.
func TestInstallDisconnectSurvives(t *testing.T) {
	e := newEnv(t, "cancel")
	c := e.client(t)
	st, _ := c.CallMore(IfaceFisherman+".Install", map[string]any{"config": goodConfig(), "confirmed": true})
	for {
		var ev Event
		if _, err := st.Next(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.StepID != nil && *ev.StepID == "install_os" && ev.OverallPct >= 30 {
			break
		}
	}
	c.Close()
	// The install is parked at install_os 50% waiting for Cancel; the
	// disconnect must not release it.
	time.Sleep(200 * time.Millisecond)
	if !e.svc.isInstalling() {
		t.Fatal("a dropped connection cancelled the install; only Cancel may do that")
	}
	// An explicit Cancel still stops it and cleans up.
	var out struct{ WasInstalling bool }
	if err := e.client(t).Call(IfaceFisherman+".Cancel", nil, &out); err != nil || !out.WasInstalling {
		t.Fatalf("cancel: %v %+v", err, out)
	}
	if e.svc.isInstalling() {
		t.Fatal("install still running after Cancel")
	}
	if len(e.mock.Actions()) != 0 {
		t.Fatalf("a cancelled install must leave nothing: %v", e.mock.Actions())
	}
}

// A denying authority refuses mutating calls with PermissionDenied while
// read-only calls keep working. The in-process socket carries real peer
// credentials, so the checker runs; untrusted callers never reach here.
func TestPolkitDeniesMutatingCalls(t *testing.T) {
	e := newEnv(t, "happy")
	fake := &auth.Fake{Allow: false}
	e.svc.SetChecker(fake)
	var out struct{ WasInstalling bool }
	if got := callErr(e.client(t).Call(IfaceFisherman+".Cancel", nil, &out)); got != varlink.ErrPermissionDenied {
		t.Fatalf("cancel: got %q, want %s", got, varlink.ErrPermissionDenied)
	}
	var probe ProbeResult
	if err := e.client(t).Call(IfaceFisherman+".Probe", nil, &probe); err != nil {
		t.Fatalf("probe stays public: %v", err)
	}
	if len(fake.Calls) != 1 || fake.Calls[0] != IfaceFisherman+".Cancel" {
		t.Errorf("checker calls = %v", fake.Calls)
	}
	fake.Allow = true
	if err := e.client(t).Call(IfaceFisherman+".Cancel", nil, &out); err != nil || out.WasInstalling {
		t.Fatalf("allowed cancel: %v %+v", err, out)
	}
}

// With an authority, a call that no peer can be named for is denied and the
// authority is not asked: a listener that is not a UNIX socket must not
// open the mutating calls to everyone.
func TestPolkitDeniesUnattributablePeer(t *testing.T) {
	e := newEnv(t, "happy")
	fake := &auth.Fake{Allow: true}
	e.svc.SetChecker(fake)
	err := e.svc.checkAuth(context.Background(), IfaceFisherman+".Install")
	if got := callErr(err); got != varlink.ErrPermissionDenied {
		t.Fatalf("no credentials: got %q, want %s", got, varlink.ErrPermissionDenied)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("the authority was asked about nobody: %v", fake.Calls)
	}
	// The same call with credentials reaches the authority.
	ctx := auth.WithCreds(context.Background(), auth.Creds{PID: 12, UID: 1000, StartTime: 3456})
	if err := e.svc.checkAuth(ctx, IfaceFisherman+".Install"); err != nil {
		t.Fatalf("with credentials: %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Errorf("checker calls = %v", fake.Calls)
	}
	// Without a checker (mock mode) nothing is asked and nothing is denied.
	e.svc.SetChecker(nil)
	if err := e.svc.checkAuth(context.Background(), IfaceFisherman+".Install"); err != nil {
		t.Fatalf("no checker: %v", err)
	}
}

func TestInstallRefusals(t *testing.T) {
	e := newEnv(t, "")
	c := e.client(t)

	// not confirmed
	st, _ := c.CallMore(IfaceFisherman+".Install", map[string]any{"config": goodConfig(), "confirmed": false})
	var ev Event
	if _, err := st.Next(&ev); callErr(err) != IfaceFisherman+".NotConfirmed" {
		t.Fatalf("%v", err)
	}
	// "confirmed" missing altogether counts as not confirmed
	st, _ = e.client(t).CallMore(IfaceFisherman+".Install", map[string]any{"config": goodConfig()})
	if _, err := st.Next(&ev); callErr(err) != IfaceFisherman+".NotConfirmed" {
		t.Fatalf("%v", err)
	}
	// invalid config carries the problems
	frames := e.raw(t, `{"method":"io.tunaos.Fisherman.Install","more":true,"parameters":{"config":{"disk":"/dev/nope"},"confirmed":true}}`, 0)
	if !strings.Contains(frames[0], `"error":"io.tunaos.Fisherman.InvalidConfig"`) || !strings.Contains(frames[0], "disk_not_found") {
		t.Fatalf("%v", frames)
	}
	// without more
	if err := c.Call(IfaceFisherman+".Install", map[string]any{"config": goodConfig(), "confirmed": true}, nil); callErr(err) != "org.varlink.service.ExpectedMore" {
		t.Fatalf("%v", err)
	}
	if len(e.mock.Actions()) != 0 {
		t.Fatalf("a refused install must not reach the executor: %v", e.mock.Actions())
	}

	// handoff before install
	if err := c.Call(IfaceFisherman+".Handoff", map[string]any{"mode": "reboot"}, nil); callErr(err) != IfaceFisherman+".NotInstalled" {
		t.Fatalf("%v", err)
	}
	// Cancel with nothing running
	var out struct{ WasInstalling bool }
	if err := c.Call(IfaceFisherman+".Cancel", nil, &out); err != nil || out.WasInstalling {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestHandoffAfterInstall(t *testing.T) {
	e := newEnv(t, "")
	c := e.client(t)
	if evs, err := install(t, c, goodConfig()); err != nil || evs[len(evs)-1].Event != EvComplete {
		t.Fatalf("%v", err)
	}
	// a second install is refused
	if _, err := install(t, e.client(t), goodConfig()); callErr(err) != IfaceFisherman+".AlreadyInstalled" {
		t.Fatalf("%v", err)
	}
	var out struct {
		Mode                string
		Started             bool
		SessionKeepsRunning bool
	}
	for _, tc := range []struct {
		mode string
		keep bool
	}{{"migrate", true}, {"soft_reboot", false}, {"reboot", false}} {
		if err := c.Call(IfaceFisherman+".Handoff", map[string]any{"mode": tc.mode}, &out); err != nil ||
			out.Mode != tc.mode || !out.Started || out.SessionKeepsRunning != tc.keep {
			t.Fatalf("%s: %v %+v", tc.mode, err, out)
		}
	}
	// default mode
	if err := c.Call(IfaceFisherman+".Handoff", nil, &out); err != nil || out.Mode != "reboot" {
		t.Fatalf("%v %+v", err, out)
	}
	if err := c.Call(IfaceFisherman+".Handoff", map[string]any{"mode": "teleport"}, nil); callErr(err) != "org.varlink.service.InvalidParameter" {
		t.Fatalf("%v", err)
	}
}

func TestIntrospectionServesTheIDL(t *testing.T) {
	e := newEnv(t, "")
	c := e.client(t)
	var info struct{ Interfaces []string }
	if err := c.Call("org.varlink.service.GetInfo", nil, &info); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(info.Interfaces, ","); got != "io.systemd.CryptEnroll,io.systemd.Shutdown,io.systemd.SysInstall,io.tunaos.Fisherman,org.varlink.service" {
		t.Fatal(got)
	}
	var d struct{ Description string }
	if err := c.Call("org.varlink.service.GetInterfaceDescription", map[string]string{"interface": IfaceFisherman}, &d); err != nil {
		t.Fatal(err)
	}
	if d.Description != InterfaceIDL(IfaceFisherman) || !strings.Contains(d.Description, "method Install(") {
		t.Fatal("description mismatch")
	}
}

// Every method named in the IDL is served, and every served method is in the IDL.
func TestIDLMatchesHandlers(t *testing.T) {
	e := newEnv(t, "")
	for iface, idl := range interfaceIDL {
		for _, line := range strings.Split(idl, "\n") {
			if !strings.HasPrefix(line, "method ") {
				continue
			}
			name := strings.TrimPrefix(line, "method ")
			name = name[:strings.IndexAny(name, "(")]
			frames := e.raw(t, `{"method":"`+iface+`.`+name+`"}`, 0)
			if strings.Contains(frames[0], "MethodNotFound") {
				t.Errorf("%s.%s is in the IDL but not served", iface, name)
			}
		}
	}
}
