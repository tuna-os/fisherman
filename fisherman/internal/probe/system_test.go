package probe

import (
	"reflect"
	"strconv"
	"testing"
)

const meminfo16G = "MemTotal:       16303128 kB\nMemFree:         1234567 kB\n"

// cpuTopology lays out sysfs for the given (package, core) per online CPU.
func cpuTopology(files map[string]string, online string, topo [][2]string) {
	files["sys/devices/system/cpu/online"] = online + "\n"
	for i, pc := range topo {
		dir := "sys/devices/system/cpu/cpu" + strconv.Itoa(i) + "/topology/"
		files[dir+"physical_package_id"] = pc[0] + "\n"
		files[dir+"core_id"] = pc[1] + "\n"
	}
}

func TestSystem(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		host  string // a host-only file the sandbox cannot see
		want  System
	}{
		{
			name: "meets everything",
			files: func() map[string]string {
				f := map[string]string{"proc/meminfo": meminfo16G, "sys/firmware/efi/": ""}
				cpuTopology(f, "0-3", [][2]string{{"0", "0"}, {"0", "1"}, {"0", "0"}, {"0", "1"}})
				return f
			}(),
			want: System{RAMBytes: 16303128 * 1024, CPUThreads: 4, CPUCores: 2, UEFI: true, MeetsRequirements: true, Unmet: []string{}},
		},
		{
			name: "4 GB machine passes GNOME's decimal threshold",
			files: func() map[string]string {
				f := map[string]string{"proc/meminfo": "MemTotal: 3800000 kB\n", "sys/firmware/efi/": ""}
				cpuTopology(f, "0-1", [][2]string{{"0", "0"}, {"0", "1"}})
				return f
			}(),
			want: System{RAMBytes: 3800000 * 1024, CPUThreads: 2, CPUCores: 2, UEFI: true, MeetsRequirements: true, Unmet: []string{}},
		},
		{
			name: "low ram, one core with two threads, BIOS",
			files: func() map[string]string {
				f := map[string]string{"proc/meminfo": "MemTotal: 2000000 kB\n"}
				cpuTopology(f, "0-1", [][2]string{{"0", "0"}, {"0", "0"}})
				return f
			}(),
			want: System{RAMBytes: 2000000 * 1024, CPUThreads: 2, CPUCores: 1, UEFI: false, Unmet: []string{UnmetRAM, UnmetCPU, UnmetUEFI}},
		},
		{
			name: "two sockets with same core ids count separately",
			files: func() map[string]string {
				f := map[string]string{"proc/meminfo": meminfo16G, "sys/firmware/efi/": ""}
				cpuTopology(f, "0-1", [][2]string{{"0", "0"}, {"1", "0"}})
				return f
			}(),
			want: System{RAMBytes: 16303128 * 1024, CPUThreads: 2, CPUCores: 2, UEFI: true, MeetsRequirements: true, Unmet: []string{}},
		},
		{
			name: "no topology and no online file: count cpu dirs as cores",
			files: map[string]string{
				"proc/meminfo": meminfo16G, "sys/firmware/efi/": "",
				"sys/devices/system/cpu/cpu0/": "", "sys/devices/system/cpu/cpu1/": "", "sys/devices/system/cpu/cpufreq/": "",
			},
			want: System{RAMBytes: 16303128 * 1024, CPUThreads: 2, CPUCores: 2, UEFI: true, MeetsRequirements: true, Unmet: []string{}},
		},
		{
			name: "flatpak: no /sys/firmware in the sandbox, host says UEFI",
			files: func() map[string]string {
				f := map[string]string{"proc/meminfo": meminfo16G, flatpakMarker: ""}
				cpuTopology(f, "0-3,6-7", [][2]string{{"0", "0"}, {"0", "1"}, {"0", "2"}, {"0", "3"}, {}, {}, {"0", "4"}, {"0", "5"}})
				return f
			}(),
			host: "sys/firmware/efi/",
			want: System{RAMBytes: 16303128 * 1024, CPUThreads: 6, CPUCores: 6, UEFI: true, MeetsRequirements: true, Unmet: []string{}},
		},
		{
			name: "flatpak: host says BIOS",
			files: func() map[string]string {
				f := map[string]string{"proc/meminfo": meminfo16G, flatpakMarker: ""}
				cpuTopology(f, "0-1", [][2]string{{"0", "0"}, {"0", "1"}})
				return f
			}(),
			want: System{RAMBytes: 16303128 * 1024, CPUThreads: 2, CPUCores: 2, Unmet: []string{UnmetUEFI}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &fakeHost{}
			if tt.host != "" {
				host.root = fakeRoot(t, map[string]string{tt.host: ""})
			}
			p := newProber(t, tt.files, host, nil)
			got, err := p.System()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("System() =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

func TestSystemNoHostQueryOutsideFlatpak(t *testing.T) {
	f := map[string]string{"proc/meminfo": meminfo16G}
	cpuTopology(f, "0", [][2]string{{"0", "0"}})
	host := &fakeHost{}
	p := &Prober{Root: fakeRoot(t, f), Run: host.run, Getenv: noEnv}
	if _, err := p.System(); err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 0 {
		t.Errorf("ran host commands outside a sandbox: %v", host.calls)
	}
}

func TestSystemErrors(t *testing.T) {
	tests := map[string]map[string]string{
		"no meminfo":        {"sys/devices/system/cpu/online": "0\n"},
		"meminfo no total":  {"proc/meminfo": "MemFree: 1 kB\n", "sys/devices/system/cpu/online": "0\n"},
		"meminfo bad total": {"proc/meminfo": "MemTotal: lots kB\n", "sys/devices/system/cpu/online": "0\n"},
		"no cpus":           {"proc/meminfo": meminfo16G, "sys/devices/system/cpu/": ""},
		"bad cpu list":      {"proc/meminfo": meminfo16G, "sys/devices/system/cpu/online": "0-x\n"},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			p := &Prober{Root: fakeRoot(t, files), Getenv: noEnv}
			if _, err := p.System(); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestParseCPUList(t *testing.T) {
	got, err := parseCPUList("0-2,5,7-8")
	if err != nil || !reflect.DeepEqual(got, []int{0, 1, 2, 5, 7, 8}) {
		t.Errorf("parseCPUList = %v, %v", got, err)
	}
}
