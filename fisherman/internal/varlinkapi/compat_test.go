package varlinkapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// These tests pin the wire format that hanthor/gnome-setup (branch utah)
// expects. The expectations come from its zlink proxies
// (src/interfaces/{sysinstall,crypt_enroll,shutdown}/*.rs) and from its demo
// server (systemd_sysinstall_demo.rs). The mirror structs below copy the
// Rust types field for field; strict decoding rejects any field the Rust
// client does not know.

// rustCandidateDevice mirrors sysinstall::CandidateDevice.
type rustCandidateDevice struct {
	Node             *string  `json:"node"`
	Symlinks         []string `json:"symlinks"`
	Diskseq          *int64   `json:"diskseq"`
	SizeBytes        *int64   `json:"sizeBytes"`
	Vendor           *string  `json:"vendor"`
	Model            *string  `json:"model"`
	Subsystem        *string  `json:"subsystem"`
	Fit              *string  `json:"fit"`
	MinimalSizeBytes *int64   `json:"minimalSizeBytes"`
	CurrentSizeBytes *int64   `json:"currentSizeBytes"`
	Action           *string  `json:"action"`
}

// rustRunOutput mirrors sysinstall::RunOutput.
type rustRunOutput struct {
	Phase    *string `json:"phase"`
	Object   *string `json:"object"`
	Progress *int64  `json:"progress"`
}

// rustReply is zlink's reply envelope.
type rustReply struct {
	Parameters json.RawMessage `json:"parameters"`
	Continues  *bool           `json:"continues"`
	Error      *string         `json:"error"`
}

func strict(t *testing.T, raw []byte, v any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		t.Fatalf("strict decode of %s: %v", raw, err)
	}
}

var (
	rustFits    = map[string]bool{"enough_free_space": true, "conflicting_disk_label_present": true, "insufficient_free_space": true, "disk_too_small": true}
	rustActions = map[string]bool{"add": true, "remove": true, "ready": true}
	rustPhases  = map[string]bool{"validate_block_device": true, "load_credentials": true, "encrypt_credentials": true,
		"install_partitions": true, "mount_partitions": true, "install_kernel": true, "install_bootloader": true, "unmount_partitions": true}
)

const (
	listCall = `{"method":"io.systemd.SysInstall.ListCandidateDevices","more":true,"parameters":{"definitions":["/usr/lib/repart.d"],"subscribe":true}}`
	// Run exactly as the zlink proxy serialises it: None arguments are omitted.
	runCall = `{"method":"io.systemd.SysInstall.Run","more":true,"parameters":{"node":"/dev/nvme0n1","definitions":["/usr/lib/repart.d"],"erase":true,"kernelImagePath":"/usr/lib/modules/6.12.0/vmlinuz","variables":true,"copyLocale":true,"copyKeymap":true,"copyTimezone":true}}`
)

func TestCompatListCandidateDevicesGolden(t *testing.T) {
	e := newEnv(t, "existing-data")
	// 3 devices + the "ready" marker
	frames := e.raw(t, listCall, 4)
	golden(t, "sysinstall_list_subscribe.jsonl", joinLines(frames))

	var sawReady bool
	for _, f := range frames {
		var r rustReply
		strict(t, []byte(f), &r)
		var cd rustCandidateDevice
		strict(t, r.Parameters, &cd)
		if cd.Action == nil || !rustActions[*cd.Action] {
			t.Fatalf("subscribe frames carry an action: %s", f)
		}
		if *cd.Action == "ready" {
			sawReady = true
			if cd.Node != nil {
				t.Fatalf("ready carries no device: %s", f)
			}
			continue
		}
		if cd.Node == nil || cd.Fit == nil || !rustFits[*cd.Fit] {
			t.Fatalf("bad candidate: %s", f)
		}
	}
	if !sawReady {
		t.Fatal("no ready action")
	}
}

func TestCompatListCandidateDevicesNoSubscribeGolden(t *testing.T) {
	e := newEnv(t, "")
	frames := e.raw(t, `{"method":"io.systemd.SysInstall.ListCandidateDevices","more":true,"parameters":{"definitions":["/usr/lib/repart.d"]}}`, 0)
	golden(t, "sysinstall_list_plain.jsonl", joinLines(frames))
	for i, f := range frames {
		var r rustReply
		strict(t, []byte(f), &r)
		var cd rustCandidateDevice
		strict(t, r.Parameters, &cd)
		if cd.Action != nil {
			t.Fatalf("without subscribe there is no action: %s", f)
		}
		last := i == len(frames)-1
		if (r.Continues != nil && *r.Continues) == last {
			t.Fatalf("continues wrong on frame %d: %s", i, f)
		}
	}
}

func TestCompatListFitRules(t *testing.T) {
	e := newEnv(t, "tight-space")
	c := e.client(t)
	st, err := c.CallMore(IfaceSysInstall+".ListCandidateDevices", map[string]any{"definitions": []string{"/x"}})
	if err != nil {
		t.Fatal(err)
	}
	fits := map[string]string{}
	for {
		var cd rustCandidateDevice
		ok, err := st.Next(&cd)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		fits[*cd.Node] = *cd.Fit
	}
	want := map[string]string{
		"/dev/nvme0n1": "insufficient_free_space",
		"/dev/sdb":     "disk_too_small",
		"/dev/sdc":     "conflicting_disk_label_present",
	}
	for k, v := range want {
		if fits[k] != v {
			t.Errorf("%s fit = %q, want %q (all: %v)", k, fits[k], v, fits)
		}
	}
}

func TestCompatRunGolden(t *testing.T) {
	e := newEnv(t, "")
	frames := e.raw(t, runCall, 0)
	golden(t, "sysinstall_run_ok.jsonl", joinLines(frames))

	// every frame but the last is a phase; the last is the empty reply
	// gnome-setup ignores (zlink MissingParameters).
	last := frames[len(frames)-1]
	if last != `{}` {
		t.Fatalf("final frame must be an empty reply, got %s", last)
	}
	var phases []string
	for _, f := range frames[:len(frames)-1] {
		var r rustReply
		strict(t, []byte(f), &r)
		if r.Continues == nil || !*r.Continues {
			t.Fatalf("intermediate frame must continue: %s", f)
		}
		var out rustRunOutput
		strict(t, r.Parameters, &out)
		if out.Phase == nil || !rustPhases[*out.Phase] {
			t.Fatalf("unknown phase: %s", f)
		}
		if out.Progress != nil && *out.Phase != "install_partitions" {
			t.Fatalf("only install_partitions reports progress: %s", f)
		}
		phases = append(phases, *out.Phase)
	}
	// phases arrive in the order of the Rust enum
	order := []string{"validate_block_device", "load_credentials", "encrypt_credentials", "install_partitions",
		"mount_partitions", "install_kernel", "install_bootloader", "unmount_partitions"}
	i := 0
	for _, p := range phases {
		for i < len(order) && order[i] != p {
			i++
		}
		if i == len(order) {
			t.Fatalf("phase %s out of order in %v", p, phases)
		}
	}
	if phases[len(phases)-1] != "unmount_partitions" || phases[0] != "validate_block_device" {
		t.Fatalf("phases: %v", phases)
	}
}

func TestCompatRunPartitionProgress(t *testing.T) {
	e := newEnv(t, "")
	var prog []int64
	for _, f := range e.raw(t, runCall, 0) {
		var r rustReply
		_ = json.Unmarshal([]byte(f), &r)
		var out rustRunOutput
		_ = json.Unmarshal(r.Parameters, &out)
		if out.Progress != nil {
			prog = append(prog, *out.Progress)
		}
	}
	if len(prog) == 0 || prog[0] != 0 || prog[len(prog)-1] != 100 {
		t.Fatalf("progress should run 0..100, got %v", prog)
	}
	for i := 1; i < len(prog); i++ {
		if prog[i] < prog[i-1] {
			t.Fatalf("progress went backwards: %v", prog)
		}
	}
}

// Optional arguments may also arrive as JSON null; both forms must work.
func TestCompatRunNullArguments(t *testing.T) {
	e := newEnv(t, "")
	frames := e.raw(t, `{"method":"io.systemd.SysInstall.Run","more":true,"parameters":{"node":"/dev/nvme0n1","definitions":null,"erase":false,"kernelImagePath":null,"variables":true,"copyLocale":null,"copyKeymap":null,"copyTimezone":null}}`, 0)
	if frames[len(frames)-1] != `{}` {
		t.Fatalf("frames: %v", frames)
	}
}

func TestCompatRunErrorsGolden(t *testing.T) {
	cases := []struct {
		name, scenario, node string
		erase                bool
	}{
		{"disk_too_small", "", "/dev/sdb", true},
		{"conflicting_label", "existing-data", "/dev/sdc", false},
		{"insufficient_free_space", "tight-space", "/dev/nvme0n1", false},
		{"no_such_node", "", "/dev/nope", true},
	}
	var all strings.Builder
	for _, tc := range cases {
		e := newEnv(t, tc.scenario)
		call, _ := json.Marshal(map[string]any{"method": "io.systemd.SysInstall.Run", "more": true,
			"parameters": map[string]any{"node": tc.node, "definitions": []string{"/x"}, "erase": tc.erase, "variables": true}})
		frames := e.raw(t, string(call), 0)
		if len(frames) != 1 {
			t.Fatalf("%s: want one error frame, got %v", tc.name, frames)
		}
		all.WriteString(tc.name + " " + frames[0] + "\n")
	}
	golden(t, "sysinstall_run_errors.txt", all.String())
}

func TestCompatErasingAConflictingDiskIsAllowed(t *testing.T) {
	e := newEnv(t, "existing-data")
	frames := e.raw(t, strings.Replace(strings.Replace(runCall, "/dev/nvme0n1", "/dev/sdc", 1), `"erase":true`, `"erase":true`, 1), 0)
	if frames[len(frames)-1] != `{}` {
		t.Fatalf("erase=true must override a foreign label: %v", frames)
	}
}

func TestCompatRunNeedsKnownEligibleNode(t *testing.T) {
	e := newEnv(t, "rst") // the NVMe disk is hidden by RST
	frames := e.raw(t, runCall, 0)
	if !strings.Contains(frames[0], `"error":"io.systemd.System"`) {
		t.Fatalf("a hidden disk cannot be installed to: %v", frames)
	}
}

func TestCompatNoCandidates(t *testing.T) {
	// RST hides the NVMe disk; the 8 GiB stick still lists (as too small).
	e := newEnv(t, "rst")
	frames := e.raw(t, `{"method":"io.systemd.SysInstall.ListCandidateDevices","more":true,"parameters":{"definitions":[]}}`, 0)
	if len(frames) != 1 || !strings.Contains(frames[0], `"fit":"disk_too_small"`) {
		t.Fatalf("%v", frames)
	}
}

func TestCompatListNeedsMore(t *testing.T) {
	e := newEnv(t, "")
	c := e.client(t)
	err := c.Call(IfaceSysInstall+".ListCandidateDevices", map[string]any{"definitions": []string{}}, nil)
	if callErr(err) != "org.varlink.service.ExpectedMore" {
		t.Fatalf("%v", err)
	}
}

// The subscribe stream must report a hot-unplug as {"node":..,"action":"remove"}.
func TestCompatSubscribeRemove(t *testing.T) {
	e := newEnv(t, "device-lost")
	sub := e.client(t)
	st, err := sub.CallMore(IfaceSysInstall+".ListCandidateDevices", map[string]any{"definitions": []string{}, "subscribe": true})
	if err != nil {
		t.Fatal(err)
	}
	for { // drain the initial adds up to ready
		var cd rustCandidateDevice
		if _, err := st.Next(&cd); err != nil {
			t.Fatal(err)
		}
		if *cd.Action == "ready" {
			break
		}
	}
	c := e.client(t)
	evs, _ := install(t, c, goodConfig())
	if len(evs) == 0 || evs[len(evs)-1].Event != EvFailed {
		t.Fatalf("the install should fail: %+v", evs)
	}
	var cd rustCandidateDevice
	if _, err := st.Next(&cd); err != nil {
		t.Fatal(err)
	}
	if *cd.Action != "remove" || *cd.Node != "/dev/nvme0n1" {
		t.Fatalf("want remove of the lost disk, got %+v", cd)
	}
}

// ---- CryptEnroll and Shutdown ----------------------------------------------

type rustEnrollOutput struct {
	Slot        *int64  `json:"slot"`
	WipedSlots  []int64 `json:"wipedSlots"`
	RecoveryKey *string `json:"recoveryKey"`
	State       *string `json:"state"`
}

type rustListSlotsOutput struct {
	Slot *int64  `json:"slot"`
	Type *string `json:"type"`
}

// TestCompatGnomeSetupFlow replays the calls gnome-setup makes after a
// successful install without a TPM: enroll the user's passphrase next to the
// empty password, list slots, then enroll a recovery key while wiping the
// empty one, then reboot.
func TestCompatGnomeSetupFlow(t *testing.T) {
	e := newEnv(t, "no-tpm")

	// 1. Run
	frames := e.raw(t, runCall, 0)
	if frames[len(frames)-1] != `{}` {
		t.Fatalf("run: %v", frames)
	}

	c := e.client(t)
	// 2. MakeRecoveryKey
	var mk struct {
		RecoveryKey string `json:"recoveryKey"`
	}
	if err := c.Call(IfaceCryptEnroll+".MakeRecoveryKey", nil, &mk); err != nil {
		t.Fatal(err)
	}
	if len(mk.RecoveryKey) != 71 || strings.Count(mk.RecoveryKey, "-") != 7 {
		t.Fatalf("recovery key shape: %q", mk.RecoveryKey)
	}

	// 3. Enroll the passphrase, unlocking with the empty password
	var en rustEnrollOutput
	err := c.Call(IfaceCryptEnroll+".Enroll", map[string]any{
		"node": "/dev/nvme0n1", "mechanism": "password", "unlockPassword": "", "password": "correct horse"}, &en)
	if err != nil || en.Slot == nil {
		t.Fatalf("enroll password: %v %+v", err, en)
	}
	passSlot := *en.Slot

	// 4. ListSlots (more)
	st, err := c.CallMore(IfaceCryptEnroll+".ListSlots", map[string]any{"node": "/dev/nvme0n1"})
	if err != nil {
		t.Fatal(err)
	}
	var wipe []int64
	for {
		var sl rustListSlotsOutput
		ok, err := st.Next(&sl)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if *sl.Type == "password" && *sl.Slot != passSlot {
			wipe = append(wipe, *sl.Slot)
		}
	}
	if len(wipe) != 1 {
		t.Fatalf("expected the empty password slot to wipe, got %v", wipe)
	}

	// 5. Enroll recovery, wipe the empty password
	var rec rustEnrollOutput
	err = c.Call(IfaceCryptEnroll+".Enroll", map[string]any{
		"node": "/dev/nvme0n1", "mechanism": "recovery", "unlockPassword": "", "wipeSlots": wipe}, &rec)
	if err != nil || rec.RecoveryKey == nil || len(rec.WipedSlots) != 1 {
		t.Fatalf("enroll recovery: %v %+v", err, rec)
	}

	// 6. The empty password no longer unlocks
	err = c.Call(IfaceCryptEnroll+".Enroll", map[string]any{
		"node": "/dev/nvme0n1", "mechanism": "recovery", "unlockPassword": ""}, nil)
	if callErr(err) != IfaceCryptEnroll+".PasswordIncorrect" {
		t.Fatalf("want PasswordIncorrect, got %v", err)
	}

	// 7. Reboot with skipInhibitors, as gnome-setup does
	if err := c.Call(IfaceShutdown+".Reboot", map[string]any{"allowInteractiveAuthentication": false, "skipInhibitors": true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.mock.Actions(); got[len(got)-1] != "shutdown:Reboot" {
		t.Fatalf("actions: %v", got)
	}
}

func TestCryptEnrollErrors(t *testing.T) {
	e := newEnv(t, "")
	c := e.client(t)
	call := func(p map[string]any) string {
		return callErr(c.Call(IfaceCryptEnroll+".Enroll", p, nil))
	}
	if got := call(map[string]any{"node": "/dev/nope", "mechanism": "password", "unlockPassword": ""}); got != "io.systemd.System" {
		t.Errorf("unknown node: %s", got)
	}
	e.raw(t, runCall, 0)
	if got := call(map[string]any{"node": "/dev/nvme0n1", "mechanism": "password", "password": "x"}); got != IfaceCryptEnroll+".PasswordRequired" {
		t.Errorf("no unlock password: %s", got)
	}
	if got := call(map[string]any{"node": "/dev/nvme0n1", "mechanism": "password", "unlockPassword": "wrong", "password": "x"}); got != IfaceCryptEnroll+".PasswordIncorrect" {
		t.Errorf("wrong unlock password: %s", got)
	}
	if got := call(map[string]any{"node": "/dev/nvme0n1", "mechanism": "tpm2", "unlockPassword": ""}); got != IfaceCryptEnroll+".VolumeUnderForeignManagement" {
		t.Errorf("tpm2: %s", got)
	}
	if got := call(map[string]any{"node": "/dev/nvme0n1", "mechanism": "fido2", "unlockPassword": ""}); got != IfaceCryptEnroll+".FidoDeviceNotFound" {
		t.Errorf("fido2: %s", got)
	}
	err := c.Call(IfaceCryptEnroll+".ListSlots", map[string]any{"node": "/dev/nvme0n1"}, nil)
	if callErr(err) != "org.varlink.service.ExpectedMore" {
		t.Errorf("ListSlots without more: %v", err)
	}
}

func TestShutdownWireAndErrors(t *testing.T) {
	e := newEnv(t, "inhibitor")
	// Rust unit return: any successful reply works, but pin the bytes.
	frames := e.raw(t, `{"method":"io.systemd.Shutdown.Reboot","parameters":{"allowInteractiveAuthentication":false,"skipInhibitors":false}}`, 0)
	golden(t, "shutdown_blocked.jsonl", joinLines(frames))

	frames = e.raw(t, `{"method":"io.systemd.Shutdown.PowerOff","parameters":{"skipInhibitors":true}}`, 0)
	if frames[0] != `{"parameters":{}}` {
		t.Fatalf("%v", frames)
	}
	frames = e.raw(t, `{"method":"io.systemd.Shutdown.Halt","parameters":{"skipInhibitors":true}}`, 0)
	if !strings.Contains(frames[0], "io.systemd.Shutdown.AlreadyInProgress") {
		t.Fatalf("%v", frames)
	}
	for _, m := range []string{"PowerOff", "Reboot", "Halt", "KExec", "SoftReboot"} {
		e2 := newEnv(t, "")
		fr := e2.raw(t, `{"method":"io.systemd.Shutdown.`+m+`","parameters":{}}`, 0)
		if fr[0] != `{"parameters":{}}` {
			t.Errorf("%s: %v", m, fr)
		}
	}
}
