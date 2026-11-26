package varlinkapi

import (
	"context"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/varlink"
)

// Errno values used for io.systemd.System errors.
const (
	errnoEIO       = 5
	errnoEBUSY     = 16
	errnoENODEV    = 19
	errnoECANCELED = 125
)

// systemErr is systemd's generic errno error (io.systemd.System).
func systemErr(errno int, name, desc string) *varlink.Error {
	return &varlink.Error{Name: "io.systemd.System",
		Params: map[string]any{"errno": errno, "errnoName": name, "description": desc}}
}

func sysInstallErr(name string, params any) *varlink.Error {
	return &varlink.Error{Name: IfaceSysInstall + "." + name, Params: params}
}

// candidate converts a disk to a candidate-device reply. The fit rules are:
// too small beats everything; a foreign label conflicts; a disk with data
// but not enough free space cannot be installed beside it.
func candidate(d Disk) CandidateDevice {
	fit := FitEnough
	switch {
	case d.SizeBytes < d.MinimalSizeBytes:
		fit = FitTooSmall
	case d.ForeignLabel:
		fit = FitConflict
	case d.ExistingData.HasPartitionTable && d.FreeBytes < d.MinimalSizeBytes:
		fit = FitInsufficient
	}
	c := CandidateDevice{
		Node:             ptr(d.Path),
		Symlinks:         d.Symlinks,
		SizeBytes:        ptr(d.SizeBytes),
		Fit:              ptr(fit),
		MinimalSizeBytes: ptr(d.MinimalSizeBytes),
		CurrentSizeBytes: ptr(d.SizeBytes - d.FreeBytes),
	}
	if d.DiskSeq != 0 {
		c.DiskSeq = ptr(d.DiskSeq)
	}
	if d.Vendor != "" {
		c.Vendor = ptr(d.Vendor)
	}
	if d.Model != "" {
		c.Model = ptr(d.Model)
	} else if d.Vendor == "" && d.Transport != "" {
		// gnome-setup shows vendor and model and no path: a virtio disk has
		// neither, and its row was the PCI id "0x1af4".
		c.Model = ptr(probe.TransportLabel(d.Transport) + " disk")
	}
	if d.Transport != "" {
		c.Subsystem = ptr(d.Transport)
	}
	return c
}

func (s *Service) registerSysInstall(srv *varlink.Server) {
	srv.Handle(IfaceSysInstall+".ListCandidateDevices", s.listCandidateDevices)
	srv.Handle(IfaceSysInstall+".Run", s.run)
}

func (s *Service) listCandidateDevices(ctx context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
	var p struct {
		Definitions []string `json:"definitions"`
		Subscribe   *bool    `json:"subscribe"`
	}
	if err := c.Decode(&p); err != nil {
		return invalidParam("definitions")
	}
	subscribe := p.Subscribe != nil && *p.Subscribe
	if !c.More {
		return &varlink.Error{Name: varlink.ErrExpectedMore}
	}
	// Subscribe before the snapshot so that no hot-plug event is lost.
	var events <-chan DeviceEvent
	if subscribe {
		wctx, stop := context.WithCancel(ctx)
		defer stop()
		ev, err := s.exec.WatchDevices(wctx)
		if err != nil {
			return err
		}
		events = ev
	}
	pr, err := s.exec.Probe(ctx)
	if err != nil {
		return err
	}
	var cands []CandidateDevice
	for _, d := range pr.Disks {
		if d.Eligible {
			cands = append(cands, candidate(d))
		}
	}
	if len(cands) == 0 {
		return sysInstallErr("NoCandidateDevices", nil)
	}
	if !subscribe {
		for i, cd := range cands {
			if err := w.Reply(cd, i < len(cands)-1); err != nil {
				return err
			}
		}
		return nil
	}
	add := "add"
	for _, cd := range cands {
		cd.Action = &add
		if err := w.Reply(cd, true); err != nil {
			return err
		}
	}
	if err := w.Reply(CandidateDevice{Action: ptr("ready")}, true); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-events:
			var cd CandidateDevice
			switch ev.Action {
			case "remove":
				cd = CandidateDevice{Node: ptr(ev.Disk.Path), Action: ptr("remove")}
			case "add":
				if !ev.Disk.Eligible {
					continue
				}
				cd = candidate(ev.Disk)
				cd.Action = ptr("add")
			default:
				continue
			}
			if err := w.Reply(cd, true); err != nil {
				return err
			}
		}
	}
}

// phaseFor maps a step started to the SysInstall phases it announces.
var phaseFor = map[string][]string{
	StepValidateDevice: {PhaseValidateBlockDevice},
	StepPartition:      {PhaseLoadCredentials, PhaseEncryptCredentials, PhaseInstallPartitions},
	StepMount:          {PhaseMountPartitions},
	StepInstallOS:      {PhaseInstallKernel},
	StepBootloader:     {PhaseInstallBootloader},
	StepFinalize:       {PhaseUnmountPartitions},
}

func (s *Service) run(ctx context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
	var p RunParams
	if err := c.Decode(&p); err != nil || p.Node == "" {
		return invalidParam("node")
	}
	if err := s.checkAuth(ctx, IfaceSysInstall+".Run"); err != nil {
		return err
	}
	pr, err := s.exec.Probe(ctx)
	if err != nil {
		return err
	}
	var disk *Disk
	for i := range pr.Disks {
		if pr.Disks[i].Path == p.Node && pr.Disks[i].Eligible {
			disk = &pr.Disks[i]
		}
	}
	if disk == nil {
		return systemErr(errnoENODEV, "ENODEV", "no such block device: "+p.Node)
	}
	switch {
	case disk.SizeBytes < disk.MinimalSizeBytes:
		return sysInstallErr("DiskTooSmall", map[string]any{
			"minimalSizeBytes": disk.MinimalSizeBytes, "currentSizeBytes": disk.SizeBytes})
	case !p.Erase && disk.ForeignLabel:
		return sysInstallErr("ConflictingDiskLabelPresent", nil)
	case !p.Erase && disk.ExistingData.HasPartitionTable && disk.FreeBytes < disk.MinimalSizeBytes:
		return sysInstallErr("InsufficientFreeSpace", map[string]any{
			"minimalSizeBytes": disk.MinimalSizeBytes,
			"needFreeBytes":    disk.MinimalSizeBytes - disk.FreeBytes,
			"currentSizeBytes": disk.SizeBytes})
	}

	ictx, finish, err := s.begin(context.WithoutCancel(ctx))
	switch err {
	case nil:
	case errInstalling:
		return systemErr(errnoEBUSY, "EBUSY", "an install is already running")
	default:
		return systemErr(errnoEBUSY, "EBUSY", "the system is already installed")
	}

	// gnome-setup replaces the empty password over io.systemd.CryptEnroll
	// afterwards, so Run encrypts with an empty one (and seals to the TPM
	// when there is one), like systemd-sysinstall does.
	enc := Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret(""))}
	if pr.TPM.Usable {
		enc.Type = "tpm2_luks"
	}
	plan := s.planFor(Config{}, *disk)
	plan.Erase, plan.Encryption = p.Erase, enc
	// copyLocale, copyKeymap and copyTimezone carry the live session's
	// settings to the installed system, as systemd-sysinstall does. Without
	// them the first boot of an albacore:gnome install showed the language
	// "Unspecified" and an empty keyboard list in gnome-initial-setup, whose
	// Next button then stayed off (tunaOS run 45690452449).
	plan.Target.CopyLocale = p.CopyLocale != nil && *p.CopyLocale
	plan.Target.CopyKeymap = p.CopyKeymap != nil && *p.CopyKeymap
	plan.Target.CopyTimezone = p.CopyTimezone != nil && *p.CopyTimezone

	more := c.More
	final := s.runInstall(ictx, plan, func(ev Event) {
		if !more || ev.StepID == nil {
			return
		}
		switch ev.Event {
		case EvStepStarted:
			for _, ph := range phaseFor[*ev.StepID] {
				_ = w.Reply(RunOutput{Phase: ptr(ph)}, true)
			}
		case EvProgress:
			// systemd-sysinstall reports a percentage for partitioning only.
			if *ev.StepID == StepPartition {
				_ = w.Reply(RunOutput{Phase: ptr(PhaseInstallPartitions), Progress: ptr(int64(ev.stepPct))}, true)
			}
		}
	})
	finish(final.Event == EvComplete)
	switch final.Event {
	case EvComplete:
		// systemd-sysinstall ends the stream with an empty reply, which
		// gnome-setup expects and ignores.
		return w.Reply(varlink.NoParameters, false)
	case EvCancelled:
		if ctx.Err() != nil {
			return ctx.Err() // the client left; nothing to send
		}
		return systemErr(errnoECANCELED, "ECANCELED", "install cancelled")
	default:
		msg := "install failed"
		if final.Message != nil {
			msg = *final.Message
		}
		if final.Reason != nil && *final.Reason == ReasonDeviceLost {
			return systemErr(errnoENODEV, "ENODEV", msg)
		}
		return systemErr(errnoEIO, "EIO", msg)
	}
}
