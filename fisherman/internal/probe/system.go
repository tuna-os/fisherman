package probe

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Minimum requirements, taken from bootc-installer's GNOME frontend
// (bootc_installer/core/system.py), the only frontend that checked them.
const (
	// MinRAMBytes is GNOME's `free -b` threshold. It is decimal on purpose:
	// a "4 GB" machine reports a little under 4 GiB of MemTotal once the
	// firmware and kernel have taken their share, and must still pass.
	MinRAMBytes int64 = 3_800_000_000
	// MinCPUCores is GNOME's threshold: physical cores (lscpu
	// cores-per-socket x sockets), not hardware threads.
	MinCPUCores = 2
)

// Unmet requirement identifiers.
const (
	UnmetRAM  = "ram"
	UnmetCPU  = "cpu"
	UnmetUEFI = "uefi"
)

// System is the machine's resources and whether they meet the minimum.
type System struct {
	RAMBytes   int64 `json:"ram_bytes"`
	CPUThreads int   `json:"cpu_threads"`
	// CPUCores counts distinct physical cores. It equals CPUThreads when
	// the kernel exposes no topology.
	CPUCores          int      `json:"cpu_cores"`
	UEFI              bool     `json:"uefi"`
	MeetsRequirements bool     `json:"meets_requirements"`
	Unmet             []string `json:"unmet"`
}

// System reads RAM, CPU and firmware facts.
func (p *Prober) System() (System, error) {
	ram, err := p.ramBytes()
	if err != nil {
		return System{}, err
	}
	threads, cores, err := p.cpuCounts()
	if err != nil {
		return System{}, err
	}
	s := System{
		RAMBytes:   ram,
		CPUThreads: threads,
		CPUCores:   cores,
		UEFI:       p.uefi(),
		Unmet:      []string{},
	}
	if s.RAMBytes < MinRAMBytes {
		s.Unmet = append(s.Unmet, UnmetRAM)
	}
	if s.CPUCores < MinCPUCores {
		s.Unmet = append(s.Unmet, UnmetCPU)
	}
	if !s.UEFI {
		s.Unmet = append(s.Unmet, UnmetUEFI)
	}
	s.MeetsRequirements = len(s.Unmet) == 0
	return s, nil
}

// ramBytes is MemTotal from /proc/meminfo, the figure `free -b` prints as
// "total".
func (p *Prober) ramBytes() (int64, error) {
	f, err := os.Open(p.local("/proc/meminfo"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("MemTotal %q: %w", fields[1], err)
			}
			return kb * 1024, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("no MemTotal in /proc/meminfo")
}

var cpuDirRe = regexp.MustCompile(`^cpu[0-9]+$`)

// cpuCounts returns online hardware threads and distinct physical cores.
func (p *Prober) cpuCounts() (threads, cores int, err error) {
	online, err := p.onlineCPUs()
	if err != nil {
		return 0, 0, err
	}
	seen := map[string]bool{}
	for _, cpu := range online {
		topo := filepath.Join("/sys/devices/system/cpu", cpu, "topology")
		pkg, ok1 := p.kernelValue(topo + "/physical_package_id")
		core, ok2 := p.kernelValue(topo + "/core_id")
		if !ok1 || !ok2 {
			// No topology: count threads as cores rather than guess.
			return len(online), len(online), nil
		}
		seen[pkg+":"+core] = true
	}
	return len(online), len(seen), nil
}

// kernelValue is readKernelFile for a value whose absence is an answer.
func (p *Prober) kernelValue(path string) (string, bool) {
	v, err := p.readKernelFile(path)
	return v, err == nil
}

// onlineCPUs lists cpuN names from /sys/devices/system/cpu/online (a range
// list like "0-3,6"), falling back to the cpuN directories.
func (p *Prober) onlineCPUs() ([]string, error) {
	if s, err := p.readKernelFile("/sys/devices/system/cpu/online"); err == nil && s != "" {
		ids, err := parseCPUList(s)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(ids))
		for _, id := range ids {
			names = append(names, "cpu"+strconv.Itoa(id))
		}
		return names, nil
	}
	entries, err := os.ReadDir(p.local("/sys/devices/system/cpu"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if cpuDirRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no CPUs under /sys/devices/system/cpu")
	}
	return names, nil
}

// parseCPUList parses the kernel's cpulist format ("0-3,6,8-9").
func parseCPUList(s string) ([]int, error) {
	var ids []int
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("cpu list %q: %w", s, err)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil {
				return nil, fmt.Errorf("cpu list %q: %w", s, err)
			}
		}
		for i := a; i <= b; i++ {
			ids = append(ids, i)
		}
	}
	return ids, nil
}

// uefi reports whether the host booted through UEFI. A Flatpak sandbox does
// not mount /sys/firmware, so there the host is asked. (GNOME used to assume
// true inside a Flatpak.)
func (p *Prober) uefi() bool {
	if fi, err := os.Stat(p.local("/sys/firmware/efi")); err == nil && fi.IsDir() {
		return true
	}
	if !p.inFlatpak() {
		return false
	}
	_, err := p.run("test", "-d", "/sys/firmware/efi")
	return err == nil
}
