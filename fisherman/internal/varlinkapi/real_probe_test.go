package varlinkapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hostFixture(t *testing.T, memKB int, uefi bool) *installFixture {
	fx := newInstallFixture(t, true)
	writeFixture(t, fx.root, "proc/meminfo", fmt.Sprintf("MemTotal:       %d kB\n", memKB))
	writeFixture(t, fx.root, "sys/devices/system/cpu/online", "0-7\n")
	if uefi {
		if err := os.MkdirAll(filepath.Join(fx.root, "sys/firmware/efi"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return fx
}

func TestRealRequirements(t *testing.T) {
	fx := hostFixture(t, 16*1024*1024, true)
	res, err := fx.real.Requirements(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.MeetsRequirements || len(res.Gates) != 3 {
		t.Fatalf("result = %+v", res)
	}
	ids := []string{res.Gates[0].ID, res.Gates[1].ID, res.Gates[2].ID}
	if strings.Join(ids, ",") != "ram,cpu,firmware" {
		t.Errorf("gate ids = %v, want the mock's", ids)
	}
}

func TestRealRequirementsBlocks(t *testing.T) {
	fx := hostFixture(t, 2*1024*1024, false)
	res, err := fx.real.Requirements(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.MeetsRequirements {
		t.Fatal("2 GiB and no UEFI must not meet the requirements")
	}
	if g := res.Gates[0]; g.OK || g.Action == nil || *g.Action != "power_off" {
		t.Errorf("ram gate = %+v", g)
	}
	if g := res.Gates[2]; g.OK || g.Action == nil || *g.Action != "firmware_setup" || *g.Actual != "BIOS" {
		t.Errorf("firmware gate = %+v", g)
	}
}

func TestRealProbe(t *testing.T) {
	fx := hostFixture(t, 16*1024*1024, true)
	res, err := fx.real.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.ProtocolVersion != 1 || len(res.Disks) != 2 {
		t.Fatalf("probe = %+v", res)
	}
	var disk, medium *Disk
	for i := range res.Disks {
		switch res.Disks[i].Path {
		case "/dev/fake1":
			disk = &res.Disks[i]
		case "/dev/fakemed":
			medium = &res.Disks[i]
		}
	}
	if disk == nil || medium == nil {
		t.Fatalf("disks = %+v", res.Disks)
	}
	if !disk.Eligible || disk.Model != "Fake Disk" || disk.Transport != "nvme" {
		t.Errorf("disk = %+v", disk)
	}
	if !disk.ExistingData.HasPartitionTable || *disk.ExistingData.PartitionTableType != "gpt" ||
		len(disk.ExistingData.Partitions) != 1 || strings.Join(disk.ExistingData.OperatingSystems, ",") != "Windows" {
		t.Errorf("existing data = %+v", disk.ExistingData)
	}
	if disk.FreeBytes < 60<<30 || disk.FreeBytes >= disk.SizeBytes {
		t.Errorf("free = %d of %d", disk.FreeBytes, disk.SizeBytes)
	}
	if disk.ForeignLabel {
		t.Error("a GPT is not a foreign label")
	}
	if disk.MinimalSizeBytes == 0 {
		t.Error("minimalSizeBytes is unset")
	}
	if medium.Eligible || medium.ExcludedReason == nil || *medium.ExcludedReason != "boot_disk" || !medium.Removable {
		t.Errorf("medium = %+v", medium)
	}
	if !res.TPM.Present || !res.TPM.Usable || res.TPM.Version != "2.0" {
		t.Errorf("tpm = %+v", res.TPM)
	}
	if !res.Live.IsLive {
		t.Error("the tunaos.live karg means a live session")
	}
	if res.Handoff.Mode != "reboot" {
		t.Errorf("handoff = %+v, want reboot until #4908/#4909", res.Handoff)
	}
	if res.IntelRST.Detected || res.IntelRST.AffectedDisks == nil {
		t.Errorf("intel rst = %+v", res.IntelRST)
	}
}

func TestRealProbeIntelVMD(t *testing.T) {
	fx := hostFixture(t, 16*1024*1024, true)
	// the disk sits in the VMD domain 10000
	real := filepath.Join(fx.root, "sys/devices/pci0000:00/0000:00:0e.0/pci10000:e0/10000:e0:06.0/10000:e1:00.0/block/fake1")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fx.root, "sys/block"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(fx.root, "sys/block/fake1")); err != nil {
		t.Fatal(err)
	}
	res, err := fx.real.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IntelRST.Detected || res.IntelRST.Mode == nil || *res.IntelRST.Mode != "vmd" ||
		strings.Join(res.IntelRST.AffectedDisks, ",") != "/dev/fake1" {
		t.Errorf("intel rst = %+v", res.IntelRST)
	}
	for _, d := range res.Disks {
		if d.Path == "/dev/fake1" && (d.Eligible || !d.IntelRST || *d.ExcludedReason != "intel_rst") {
			t.Errorf("a disk behind VMD must be excluded: %+v", d)
		}
	}
}

func TestRealProbeIntelRAIDController(t *testing.T) {
	fx := hostFixture(t, 16*1024*1024, true)
	writeFixture(t, fx.root, "sys/bus/pci/devices/0000:00:17.0/vendor", "0x8086\n")
	writeFixture(t, fx.root, "sys/bus/pci/devices/0000:00:17.0/class", "0x010400\n")
	res, err := fx.real.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IntelRST.Detected || *res.IntelRST.Mode != "raid" {
		t.Errorf("intel rst = %+v", res.IntelRST)
	}
}

func TestRealShutdown(t *testing.T) {
	for kind, want := range map[string]string{
		"PowerOff": "systemctl poweroff --check-inhibitors=yes --no-block",
		"Reboot":   "systemctl reboot --check-inhibitors=yes --no-block",
		"Halt":     "systemctl halt --check-inhibitors=yes --no-block",
	} {
		fx := newInstallFixture(t, false)
		if err := fx.real.Shutdown(context.Background(), kind, ShutdownParams{}); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got := strings.TrimSpace(fx.cmd.normalized()); got != want {
			t.Errorf("%s ran %q, want %q", kind, got, want)
		}
	}
	fx := newInstallFixture(t, false)
	skip := true
	if err := fx.real.Shutdown(context.Background(), "Reboot", ShutdownParams{SkipInhibitors: &skip}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(fx.cmd.normalized()); got != "systemctl reboot --check-inhibitors=no --no-block" {
		t.Errorf("skipInhibitors ran %q", got)
	}
}

func TestRealShutdownDeferred(t *testing.T) {
	for _, kind := range []string{"KExec", "SoftReboot"} {
		fx := newInstallFixture(t, false)
		if err := fx.real.Shutdown(context.Background(), kind, ShutdownParams{}); !errors.Is(err, errShutdownDeferred) {
			t.Errorf("%s: err = %v, want deferred", kind, err)
		}
		if fx.cmd.normalized() != "" {
			t.Errorf("%s ran %q", kind, fx.cmd.normalized())
		}
	}
}

func TestRealShutdownInhibited(t *testing.T) {
	fx := newInstallFixture(t, false)
	fx.cmd.hook = func(_ context.Context, name string, _ []string, _ CmdOpts) ([]byte, error, bool) {
		return nil, &CmdError{Name: name, Err: errors.New("exit status 1"), Stderr: "Operation inhibited by \"Updater\""}, true
	}
	var bi *BlockedByInhibitor
	if err := fx.real.Shutdown(context.Background(), "PowerOff", ShutdownParams{}); !errors.As(err, &bi) {
		t.Errorf("err = %v, want BlockedByInhibitor", err)
	}
}

func TestRealHandoffRebootUsesCmd(t *testing.T) {
	fx := newInstallFixture(t, false)
	keeps, err := fx.real.Handoff(context.Background(), "reboot")
	if err != nil || keeps {
		t.Fatalf("keeps = %v, err = %v", keeps, err)
	}
	if got := strings.TrimSpace(fx.cmd.normalized()); got != "systemctl reboot" {
		t.Errorf("ran %q", got)
	}
}
