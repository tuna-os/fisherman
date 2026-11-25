package varlinkapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/postfix"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// prober builds the shared legacy prober (internal/probe) over Cmd. The
// context bounds every command the prober runs.
func (r Real) prober(ctx context.Context) *probe.Prober {
	return &probe.Prober{
		Root: r.Root,
		Run: func(name string, args ...string) ([]byte, error) {
			return r.cmd().Run(ctx, name, args, CmdOpts{})
		},
		Sandboxed: runner.InFlatpak,
	}
}

// Requirements reports the RAM, CPU and firmware gates the legacy frontends
// enforce (internal/probe System). The ids, severities and copy match the
// mock, so a frontend renders both backends the same.
func (r Real) Requirements(ctx context.Context) (RequirementsResult, error) {
	sys, err := r.prober(ctx).System()
	if err != nil {
		return RequirementsResult{}, fmt.Errorf("reading system facts: %w", err)
	}
	unmet := map[string]bool{}
	for _, u := range sys.Unmet {
		unmet[u] = true
	}
	fw := "BIOS"
	if sys.UEFI {
		fw = "UEFI"
	}
	gates := []Gate{
		{ID: "ram", OK: !unmet[probe.UnmetRAM], Severity: "block",
			Required: ptr(probe.SizeLabel(probe.MinRAMBytes)), Actual: ptr(probe.SizeLabel(sys.RAMBytes)),
			Message: "At least 4 GB of memory"},
		{ID: "cpu", OK: !unmet[probe.UnmetCPU], Severity: "block",
			Required: ptr(fmt.Sprintf("%d cores", probe.MinCPUCores)), Actual: ptr(fmt.Sprintf("%d cores", sys.CPUCores)),
			Message: "At least 2 CPU cores"},
		{ID: "firmware", OK: !unmet[probe.UnmetUEFI], Severity: "block",
			Required: ptr("UEFI"), Actual: ptr(fw), Message: "UEFI firmware"},
	}
	if unmet[probe.UnmetRAM] {
		gates[0].Action = ptr("power_off")
	}
	if unmet[probe.UnmetCPU] {
		gates[1].Action = ptr("power_off")
	}
	if unmet[probe.UnmetUEFI] {
		gates[2].Action = ptr("firmware_setup")
	}
	return RequirementsResult{MeetsRequirements: len(sys.Unmet) == 0, Gates: gates}, nil
}

// Probe reads the machine: disks (with existing data, free space and Intel
// RST), the TPM, the live session and the handoff decision.
//
// There is no offline-media field: the medium itself is the install source
// (a block copy), so an offline store is never read.
func (r Real) Probe(ctx context.Context) (ProbeResult, error) {
	p := r.prober(ctx)
	pdisks, err := p.Disks()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("listing disks: %w", err)
	}
	tree, err := r.lsblkTree(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	rst := r.detectRST(pdisks, tree)

	disks := make([]Disk, 0, len(pdisks))
	for _, pd := range pdisks {
		d := Disk{
			Path:             pd.Path,
			SizeBytes:        pd.SizeBytes,
			SizeLabel:        pd.SizeLabel,
			Model:            pd.Model,
			Vendor:           vendorName(pd.Vendor),
			Transport:        pd.Transport,
			Removable:        pd.Removable,
			Eligible:         pd.Eligible,
			MinimalSizeBytes: probe.MinDiskBytes,
			ExistingData:     ExistingData{Partitions: []Partition{}, OperatingSystems: []string{}},
			FreeBytes:        pd.SizeBytes,
		}
		if pd.ExcludedReason != "" {
			d.ExcludedReason = ptr(pd.ExcludedReason)
		}
		if pd.Eligible {
			r.inspectDisk(ctx, &d, tree[pd.Path])
		}
		if rst.affected[pd.Path] {
			d.IntelRST = true
			if d.Eligible {
				d.Eligible = false
				d.ExcludedReason = ptr("intel_rst")
			}
		}
		disks = append(disks, d)
	}

	live := p.Live()
	res := ProbeResult{
		ProtocolVersion: 1,
		Disks:           disks,
		TPM:             tpmOf(p.TPM()),
		IntelRST:        rst.result(),
		Live:            Live{IsLive: live.IsLive || r.cmdlineHas(postfix.MediumLiveKarg)},
		Eula:            ResolveEULA(),
	}
	if live.LiveImage != "" {
		res.Live.LiveImage = ptr(live.LiveImage)
	}
	res.Handoff, err = r.DecideHandoff(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	return res, nil
}

// pciID is what virtio puts in /sys/block/vda/device/vendor: the id of the PCI
// vendor, where a SCSI disk has a name.
var pciID = regexp.MustCompile(`^0[xX][0-9a-fA-F]+$`)

// vendorName returns v, or "" for an id that names nothing to a person.
func vendorName(v string) string {
	if pciID.MatchString(v) {
		return ""
	}
	return v
}

func tpmOf(t probe.TPM) Tpm { return Tpm{Present: t.Present, Version: t.Version, Usable: t.Usable} }

// cmdlineHas reports whether the kernel command line carries word.
func (r Real) cmdlineHas(word string) bool {
	path := r.CmdlinePath
	if path == "" {
		path = "/proc/cmdline"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(string(b)) {
		if f == word {
			return true
		}
	}
	return false
}

// ---- lsblk and sfdisk ------------------------------------------------------

// blk is one node of lsblk -J -b -p -o NAME,TYPE,SIZE,FSTYPE,LABEL.
type blk struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Size     int64   `json:"size"`
	FSType   *string `json:"fstype"`
	Label    *string `json:"label"`
	Children []blk   `json:"children"`
}

// lsblkTree returns the top-level devices by path.
func (r Real) lsblkTree(ctx context.Context) (map[string]blk, error) {
	out, err := r.cmd().Run(ctx, "lsblk", []string{"-J", "-b", "-p", "-o", "NAME,TYPE,SIZE,FSTYPE,LABEL"}, CmdOpts{})
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var parsed struct {
		Blockdevices []blk `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}
	tree := map[string]blk{}
	for _, d := range parsed.Blockdevices {
		tree[d.Name] = d
	}
	return tree, nil
}

// sfdiskTable is the subset of sfdisk --json that free-space math needs.
type sfdiskTable struct {
	PartitionTable struct {
		Label      string `json:"label"`
		SectorSize int64  `json:"sectorsize"`
		FirstLBA   int64  `json:"firstlba"`
		LastLBA    int64  `json:"lastlba"`
		Partitions []struct {
			Start int64 `json:"start"`
			Size  int64 `json:"size"`
		} `json:"partitions"`
	} `json:"partitiontable"`
}

// freeBytes sums the gaps between partitions. A disk without a table is all
// free; a table sfdisk does not report is treated the same.
func freeBytes(t sfdiskTable, size int64) int64 {
	pt := t.PartitionTable
	if pt.LastLBA == 0 {
		return size
	}
	ss := pt.SectorSize
	if ss == 0 {
		ss = 512
	}
	type span struct{ start, end int64 }
	var used []span
	for _, p := range pt.Partitions {
		used = append(used, span{p.Start, p.Start + p.Size})
	}
	sort.Slice(used, func(i, j int) bool { return used[i].start < used[j].start })
	var free int64
	next := pt.FirstLBA
	for _, u := range used {
		if u.start > next {
			free += u.start - next
		}
		if u.end > next {
			next = u.end
		}
	}
	if pt.LastLBA+1 > next {
		free += pt.LastLBA + 1 - next
	}
	return free * ss
}

// inspectDisk fills the existing-data facts of an eligible disk.
func (r Real) inspectDisk(ctx context.Context, d *Disk, node blk) {
	var table sfdiskTable
	if out, err := r.cmd().Run(ctx, "sfdisk", []string{"--json", d.Path}, CmdOpts{}); err == nil {
		_ = json.Unmarshal(out, &table) // an unreadable table reads as empty
	}
	d.FreeBytes = freeBytes(table, d.SizeBytes)
	ed := &d.ExistingData
	if label := table.PartitionTable.Label; label != "" {
		ed.HasPartitionTable = true
		ed.PartitionTableType = ptr(label)
		// systemd-repart extends a GPT; anything else cannot share the disk.
		d.ForeignLabel = label != "gpt" && len(table.PartitionTable.Partitions) > 0
	}
	seen := map[string]bool{}
	for _, c := range node.Children {
		pt := Partition{Path: c.Name, SizeBytes: c.Size}
		if c.FSType != nil && *c.FSType != "" {
			pt.FSType = c.FSType
		}
		if c.Label != nil && *c.Label != "" {
			pt.Label = c.Label
		}
		ed.Partitions = append(ed.Partitions, pt)
		fs := ""
		if c.FSType != nil {
			fs = *c.FSType
		}
		osName := ""
		switch fs {
		case "ntfs", "BitLocker":
			osName = "Windows"
		case "crypto_LUKS":
			ed.HasLUKS = true
		case "ext4", "btrfs", "xfs", "ext3":
			osName = "Linux"
		}
		if osName != "" && !seen[osName] {
			seen[osName] = true
			ed.OperatingSystems = append(ed.OperatingSystems, osName)
		}
	}
	if node.FSType != nil && *node.FSType == "crypto_LUKS" {
		ed.HasLUKS = true
	}
}

// ---- Intel RST / VMD --------------------------------------------------------

// rstFacts is what the sysfs and lsblk scans found.
type rstFacts struct {
	mode     string // "raid" (RAID-mode SATA controller), "vmd", or ""
	affected map[string]bool
}

func (f rstFacts) result() IntelRST {
	out := IntelRST{Detected: f.mode != "", AffectedDisks: []string{}}
	if f.mode != "" {
		out.Mode = ptr(f.mode)
	}
	for d := range f.affected {
		out.AffectedDisks = append(out.AffectedDisks, d)
	}
	sort.Strings(out.AffectedDisks)
	return out
}

// vmdDomain matches a PCI domain segment other than 0000 in a sysfs device
// path: devices behind an Intel VMD controller sit in their own domain
// (pci10000:e0).
var vmdDomain = regexp.MustCompile(`/pci([0-9a-f]+):[0-9a-f]+/`)

// detectRST finds Intel RST in the two forms that hide disks: a SATA
// controller in RAID mode (PCI class 0104 from vendor 8086) and an Intel VMD
// controller (driver vmd) with NVMe disks in its domain. A disk that carries
// Intel RAID metadata (isw_raid_member) is affected as well.
func (r Real) detectRST(pdisks []probe.Disk, tree map[string]blk) rstFacts {
	f := rstFacts{affected: map[string]bool{}}
	sys := r.sysRoot()
	devs, _ := filepath.Glob(filepath.Join(sys, "bus/pci/devices/*"))
	for _, dev := range devs {
		if readTrim(filepath.Join(dev, "vendor")) != "0x8086" {
			continue
		}
		if strings.HasPrefix(readTrim(filepath.Join(dev, "class")), "0x0104") {
			f.mode = "raid"
		}
		if drv, err := filepath.EvalSymlinks(filepath.Join(dev, "driver")); err == nil && filepath.Base(drv) == "vmd" {
			f.mode = "vmd"
		}
	}
	for _, d := range pdisks {
		if isRSTMember(tree[d.Path]) {
			f.affected[d.Path] = true
			if f.mode == "" {
				f.mode = "raid"
			}
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Join(sys, "block", filepath.Base(d.Path)))
		if err != nil {
			continue
		}
		for _, m := range vmdDomain.FindAllStringSubmatch(real+"/", -1) {
			if m[1] != "0000" {
				f.affected[d.Path] = true
				if f.mode == "" {
					f.mode = "vmd"
				}
				break
			}
		}
	}
	return f
}

func isRSTMember(d blk) bool {
	if d.FSType != nil && *d.FSType == "isw_raid_member" {
		return true
	}
	for _, c := range d.Children {
		if isRSTMember(c) {
			return true
		}
	}
	return false
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---- hot plug ---------------------------------------------------------------

// watchInterval is how often WatchDevices looks for added and removed
// disks. The kernel's udev events would be sharper, but they need a netlink
// socket; a short poll over lsblk is enough for a disk list.
const watchInterval = 2 * time.Second

// WatchDevices polls the disk list and reports changes until ctx ends.
func (r Real) WatchDevices(ctx context.Context) (<-chan DeviceEvent, error) {
	snap, err := r.diskSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	ch := make(chan DeviceEvent, 16)
	go func() {
		defer close(ch)
		t := time.NewTicker(watchInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			now, err := r.diskSnapshot(ctx)
			if err != nil {
				continue
			}
			send := func(ev DeviceEvent) {
				select {
				case ch <- ev:
				case <-ctx.Done():
				}
			}
			for path, d := range now {
				if _, ok := snap[path]; !ok {
					send(DeviceEvent{Action: "add", Disk: d})
				}
			}
			for path, d := range snap {
				if _, ok := now[path]; !ok {
					send(DeviceEvent{Action: "remove", Disk: d})
				}
			}
			snap = now
		}
	}()
	return ch, nil
}

func (r Real) diskSnapshot(ctx context.Context) (map[string]Disk, error) {
	res, err := r.Probe(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Disk{}
	for _, d := range res.Disks {
		out[d.Path] = d
	}
	return out, nil
}
