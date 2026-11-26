package varlinkapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/fisherman/internal/handoff"
	"github.com/tuna-os/fisherman/internal/probe"
)

const (
	gib = int64(1) << 30
	// minInstallBytes is the smallest disk the mock treats as installable.
	minInstallBytes = 20 * gib
)

// Mock is an Executor that touches nothing: no disk, TPM, socket or
// process. It follows a Scenario, and is deterministic.
type Mock struct {
	sc        Scenario
	stepDelay time.Duration

	mu       sync.Mutex
	disks    []Disk
	watchers map[chan DeviceEvent]struct{}
	volumes  map[string][]mockSlot
	actions  []string
	shutdown bool
}

type mockSlot struct {
	num    int64
	typ    string
	secret string // password and recovery slots only
}

var _ Executor = (*Mock)(nil)

// NewMock returns a mock for the scenario. stepDelay is the pause between
// progress ticks (0 for tests); a UI demo might use 50ms.
func NewMock(sc Scenario, stepDelay time.Duration) *Mock {
	m := &Mock{sc: sc, stepDelay: stepDelay,
		watchers: map[chan DeviceEvent]struct{}{}, volumes: map[string][]mockSlot{}}
	m.disks = mockDisks(sc)
	return m
}

// Actions returns what the mock recorded as done: "install:<disk>",
// "handoff:<mode>", "shutdown:<kind>". Tests read it to prove that a call
// reached the executor.
func (m *Mock) Actions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.actions...)
}

func (m *Mock) record(a string) {
	m.mu.Lock()
	m.actions = append(m.actions, a)
	m.mu.Unlock()
}

func mockDisks(sc Scenario) []Disk {
	empty := ExistingData{Partitions: []Partition{}, OperatingSystems: []string{}}
	nvme := Disk{
		Path: "/dev/nvme0n1", SizeBytes: 512_110_190_592, Model: "Mock NVMe 512", Vendor: "Mockware",
		Transport: "nvme", Eligible: true, MinimalSizeBytes: minInstallBytes,
		ExistingData: empty, Symlinks: []string{"/dev/disk/by-id/nvme-Mock_NVMe_512"}, DiskSeq: 1,
	}
	nvme.FreeBytes = nvme.SizeBytes
	windows := 400 * gib
	if sc.TightSpace {
		windows = 460 * gib
	}
	if sc.ExistingData {
		nvme.ExistingData = ExistingData{
			HasPartitionTable: true, PartitionTableType: ptr("gpt"),
			Partitions: []Partition{
				{Path: "/dev/nvme0n1p1", FSType: ptr("vfat"), Label: ptr("SYSTEM"), SizeBytes: 260 << 20},
				{Path: "/dev/nvme0n1p2", FSType: ptr("ntfs"), Label: ptr("Windows"), SizeBytes: windows},
			},
			OperatingSystems: []string{"Windows"},
		}
		nvme.FreeBytes = nvme.SizeBytes - windows - 260<<20
	}
	stick := Disk{
		Path: "/dev/sdb", SizeBytes: 8 * gib, Model: "Mock USB Stick", Vendor: "Mockware",
		Transport: "usb", Removable: true, Eligible: true, MinimalSizeBytes: minInstallBytes,
		ExistingData: empty, FreeBytes: 8 * gib, DiskSeq: 2,
	}
	disks := []Disk{nvme, stick}
	if sc.NoDisks {
		return []Disk{}
	}
	if sc.RST {
		disks[0].Eligible = false
		disks[0].ExcludedReason = ptr("intel_rst")
		disks[0].IntelRST = true
	}
	if sc.ExistingData {
		disks = append(disks, Disk{
			Path: "/dev/sdc", SizeBytes: 1024 * gib, Model: "Mock Legacy HDD", Vendor: "Mockware",
			Transport: "sata", Eligible: true, MinimalSizeBytes: minInstallBytes,
			ExistingData: ExistingData{
				HasPartitionTable: true, PartitionTableType: ptr("dos"),
				Partitions:       []Partition{{Path: "/dev/sdc1", FSType: ptr("ext4"), SizeBytes: 100 * gib}},
				OperatingSystems: []string{}},
			FreeBytes: 924 * gib, ForeignLabel: true, DiskSeq: 3,
		})
	}
	for i := range disks {
		disks[i].SizeLabel = probe.SizeLabel(disks[i].SizeBytes)
	}
	return disks
}

func (m *Mock) Requirements(context.Context) (RequirementsResult, error) {
	ram := int64(16 * gib)
	if m.sc.LowRAM {
		ram = 2 * gib
	}
	ramOK := ram >= probe.MinRAMBytes
	uefiOK := !m.sc.NoUEFI
	gates := []Gate{
		{ID: "ram", OK: ramOK, Severity: "block", Required: ptr(probe.SizeLabel(probe.MinRAMBytes)),
			Actual: ptr(probe.SizeLabel(ram)), Message: "At least 4 GB of memory"},
		{ID: "cpu", OK: !m.sc.LowCPU, Severity: "block", Required: ptr("2 cores"),
			Actual:  ptr(map[bool]string{true: "1 core", false: "8 cores"}[m.sc.LowCPU]),
			Message: "At least 2 CPU cores"},
		{ID: "firmware", OK: uefiOK, Severity: "block", Required: ptr("UEFI"),
			Actual:  ptr(map[bool]string{true: "UEFI", false: "BIOS"}[uefiOK]),
			Message: "UEFI firmware"},
	}
	if !ramOK {
		gates[0].Action = ptr("power_off")
	}
	if !uefiOK {
		gates[2].Action = ptr("firmware_setup")
	}
	if m.sc.LowCPU {
		gates[1].Action = ptr("power_off")
	}
	return RequirementsResult{MeetsRequirements: ramOK && uefiOK && !m.sc.LowCPU, Gates: gates}, nil
}

func (m *Mock) Probe(context.Context) (ProbeResult, error) {
	m.mu.Lock()
	disks := append([]Disk(nil), m.disks...)
	m.mu.Unlock()
	res := ProbeResult{
		ProtocolVersion: 1,
		Disks:           disks,
		TPM:             Tpm{Present: !m.sc.NoTPM, Version: "2.0", Usable: !m.sc.NoTPM},
		IntelRST:        IntelRST{AffectedDisks: []string{}},
		Live:            Live{IsLive: true, LiveImage: ptr("ghcr.io/tuna-os/mock:live")},
		Handoff:         m.decision(),
		Eula:            ResolveEULA(),
	}
	if m.sc.NoTPM {
		res.TPM.Version = ""
	}
	if m.sc.NotLive {
		res.Live = Live{IsLive: false}
	}
	if m.sc.RST {
		res.IntelRST = IntelRST{Detected: true, Mode: ptr("vmd"), AffectedDisks: []string{"/dev/nvme0n1"}}
	}
	return res, nil
}

func (m *Mock) WatchDevices(ctx context.Context) (<-chan DeviceEvent, error) {
	ch := make(chan DeviceEvent, 16)
	m.mu.Lock()
	m.watchers[ch] = struct{}{}
	m.mu.Unlock()
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		delete(m.watchers, ch)
		m.mu.Unlock()
	}()
	return ch, nil
}

func (m *Mock) emitDevice(ev DeviceEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.watchers {
		select {
		case ch <- ev:
		default: // a slow watcher misses an event rather than block the install
		}
	}
}

// tick pauses for the configured delay; false means ctx ended.
func (m *Mock) tick(ctx context.Context) bool {
	if m.stepDelay <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(m.stepDelay)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (m *Mock) Install(ctx context.Context, plan InstallPlan, rep Reporter) error {
	cleanup := func() {
		rep.Start(StepCleanup)
		rep.Progress(StepCleanup, 100, "mock cleanup: nothing was changed")
		rep.Done(StepCleanup)
	}
	for _, step := range PlannedSteps(plan) {
		rep.Start(step)
		for _, pct := range []int{0, 25, 50, 75, 100} {
			if !m.tick(ctx) {
				cleanup()
				return ctx.Err()
			}
			rep.Progress(step, pct, "")
			if pct != 50 {
				continue
			}
			if m.sc.HoldStep == step {
				<-ctx.Done()
				cleanup()
				return ctx.Err()
			}
			if m.sc.FailStep == step {
				err := m.fail(plan, step)
				cleanup()
				return err
			}
		}
		rep.Done(step)
	}
	m.record("install:" + plan.Disk.Path)
	m.createVolume(plan)
	return nil
}

func (m *Mock) fail(plan InstallPlan, step string) *InstallError {
	switch m.sc.FailReason {
	case ReasonDeviceLost:
		m.mu.Lock()
		var kept []Disk
		var lost Disk
		for _, d := range m.disks {
			if d.Path == plan.Disk.Path {
				lost = d
				continue
			}
			kept = append(kept, d)
		}
		m.disks = kept
		m.mu.Unlock()
		m.emitDevice(DeviceEvent{Action: "remove", Disk: lost})
		return &InstallError{Step: step, Reason: ReasonDeviceLost,
			Message:    fmt.Sprintf("%s disappeared while the install was running", plan.Disk.Path),
			NextAction: ActionChooseAnotherDisk}
	case ReasonRepartError:
		return &InstallError{Step: step, Reason: ReasonRepartError,
			Message:    "systemd-repart exited with status 1: mock injected failure",
			NextAction: ActionRetry}
	}
	return &InstallError{Step: step, Reason: ReasonInternal, Message: "mock injected failure",
		NextAction: ActionReportBug}
}

// createVolume records the LUKS slots a finished install leaves behind.
// Run (gnome-setup) leaves an empty password, which gnome-setup replaces over
// io.systemd.CryptEnroll. The tunaos Install enrols the chosen passphrase.
func (m *Mock) createVolume(plan InstallPlan) {
	if plan.Encryption.Type == "" || plan.Encryption.Type == "none" {
		return
	}
	var slots []mockSlot
	pass := ""
	if plan.Encryption.Passphrase != nil {
		pass = plan.Encryption.Passphrase.Reveal()
	}
	slots = append(slots, mockSlot{num: 0, typ: MechPassword, secret: pass})
	if plan.Encryption.Type == "tpm2_luks" || plan.Encryption.Type == "tpm2_luks_passphrase" {
		slots = append(slots, mockSlot{num: 1, typ: MechTPM2})
	}
	m.mu.Lock()
	m.volumes[plan.Disk.Path] = slots
	m.mu.Unlock()
}

// recoveryAlphabet is systemd's modhex alphabet for recovery keys.
const recoveryAlphabet = "cbdefghijklnrtuv"

// FixedRecoveryKey is the key of the fixed-key scenario: a key of the right
// shape that no machine has, so a screenshot can show it.
const FixedRecoveryKey = "cbdefghi-jklnrtuv-cbdefghi-jklnrtuv-cbdefghi-jklnrtuv-cbdefghi-jklnrtuv"

func (m *Mock) MakeRecoveryKey(context.Context) (Secret, error) {
	if m.sc.RecoveryKeyError {
		return "", ErrNoDevice
	}
	if m.sc.FixedKey {
		return Secret(FixedRecoveryKey), nil
	}
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%8 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(recoveryAlphabet[int(c)%len(recoveryAlphabet)])
	}
	return Secret(sb.String()), nil
}

func (m *Mock) Enroll(ctx context.Context, p EnrollParams) (EnrollResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	slots, ok := m.volumes[p.Node]
	if !ok {
		return EnrollResult{}, ErrNoDevice
	}
	if m.sc.EnrollError {
		return EnrollResult{}, ErrPasswordIncorrect
	}
	if m.sc.RecoveryKeyError && p.Mechanism == MechRecovery {
		return EnrollResult{}, ErrNoDevice
	}
	switch p.Mechanism {
	case MechPassword, MechRecovery:
	case MechPKCS11, MechTPM2:
		return EnrollResult{}, ErrVolumeForeign // not enrollable through this interface
	case MechFIDO2:
		return EnrollResult{}, ErrFidoDeviceNotFound
	default:
		return EnrollResult{}, fmt.Errorf("unknown mechanism %q", p.Mechanism)
	}
	if p.UnlockPassword == nil {
		return EnrollResult{}, ErrPasswordRequired
	}
	unlocked := false
	for _, s := range slots {
		if s.typ == MechPassword && s.secret == p.UnlockPassword.Reveal() {
			unlocked = true
		}
	}
	if !unlocked {
		return EnrollResult{}, ErrPasswordIncorrect
	}

	secret := ""
	var key Secret
	switch p.Mechanism {
	case MechPassword:
		if p.Password == nil {
			return EnrollResult{}, ErrPasswordRequired
		}
		secret = p.Password.Reveal()
	case MechRecovery:
		key = Secret("")
		if p.RecoveryKey != nil && p.RecoveryKey.Reveal() != "" {
			key = *p.RecoveryKey
		} else {
			k, err := m.MakeRecoveryKey(ctx)
			if err != nil {
				return EnrollResult{}, err
			}
			key = k
		}
		secret = key.Reveal()
	}

	// Wipe first, as systemd-cryptenroll does, then add.
	var wiped []int64
	if len(p.WipeSlots) > 0 {
		drop := map[int64]bool{}
		for _, w := range p.WipeSlots {
			drop[w] = true
		}
		var kept []mockSlot
		for _, s := range slots {
			if drop[s.num] {
				wiped = append(wiped, s.num)
				continue
			}
			kept = append(kept, s)
		}
		slots = kept
	}
	num := int64(0)
	for used := true; used; num++ {
		used = false
		for _, s := range slots {
			if s.num == num {
				used = true
			}
		}
		if !used {
			break
		}
	}
	slots = append(slots, mockSlot{num: num, typ: p.Mechanism, secret: secret})
	m.volumes[p.Node] = slots
	res := EnrollResult{Slot: &num, WipedSlots: wiped, State: ptr("enrolled")}
	if p.Mechanism == MechRecovery {
		res.RecoveryKey = &key
	}
	return res, nil
}

func (m *Mock) ListSlots(_ context.Context, node string) ([]Slot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	slots, ok := m.volumes[node]
	if !ok {
		return nil, ErrNoDevice
	}
	out := make([]Slot, 0, len(slots))
	for _, s := range slots {
		out = append(out, Slot{Slot: s.num, Type: s.typ})
	}
	return out, nil
}

func (m *Mock) Shutdown(_ context.Context, kind string, p ShutdownParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown {
		return ErrAlreadyInProgress
	}
	if m.sc.Inhibitor && (p.SkipInhibitors == nil || !*p.SkipInhibitors) {
		return &BlockedByInhibitor{Who: "Mock Updater", Why: "installing updates"}
	}
	m.shutdown = true
	m.actions = append(m.actions, "shutdown:"+kind)
	return nil
}

func (m *Mock) Handoff(_ context.Context, mode string) (bool, error) {
	if m.sc.HandoffError {
		return false, systemErr(errnoEIO, "EIO", "mock injected handoff failure")
	}
	m.record("handoff:" + mode)
	return mode == "migrate", nil
}

// decision is the daemon's takeover decision: the scenario mode, or
// reboot when no handoff scenario is set.
func (m *Mock) decision() HandoffDecision {
	switch m.sc.HandoffMode {
	case handoff.ModeMigrate:
		return HandoffDecision{Mode: handoff.ModeMigrate, Reason: handoff.ReasonBtrfsSeed}
	case handoff.ModeSoftReboot:
		return HandoffDecision{Mode: handoff.ModeSoftReboot, Reason: handoff.ReasonEL10}
	default:
		return HandoffDecision{Mode: handoff.ModeReboot, Reason: handoff.ReasonDefault}
	}
}

// DecideHandoff implements Handoffer: the mock reports its scenario mode.
func (m *Mock) DecideHandoff(context.Context) (HandoffDecision, error) {
	return m.decision(), nil
}
