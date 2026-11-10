package recipe

import (
	"errors"
	"reflect"
	"testing"
)

func codes(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Field+":"+p.Code)
	}
	return out
}

func TestCheckTarget(t *testing.T) {
	ok := func() *TargetFacts { return &TargetFacts{Device: "/dev/sda", SizeBytes: 100 << 30, MinBytes: 50 << 30} }
	with := func(f func(*TargetFacts)) *TargetFacts { t := ok(); f(t); return t }
	auto := Recipe{Disk: "/dev/sda", Filesystem: "xfs"}
	withVar := Recipe{Disk: "/dev/sda", Filesystem: "xfs", VarDisk: &VarDiskSpec{Disk: "/dev/sdb"}}
	manual := Recipe{CustomMounts: []CustomMount{{Partition: "/dev/sda2", Target: "/", Fstype: "xfs"}}}

	tests := []struct {
		name     string
		r        Recipe
		override []string
		disk     *TargetFacts
		varDisk  *TargetFacts
		uefi     bool
		want     []string
	}{
		{"eligible", auto, nil, ok(), nil, true, nil},
		{"partition or file", auto, nil, with(func(f *TargetFacts) { f.NotDisk = true }), nil, true, []string{"disk:disk_not_disk"}},
		{"boot disk", auto, nil, with(func(f *TargetFacts) { f.BootDisk = true }), nil, true, []string{"disk:disk_boot_disk"}},
		{"read-only", auto, nil, with(func(f *TargetFacts) { f.ReadOnly = true }), nil, true, []string{"disk:disk_read_only"}},
		{"too small", auto, nil, with(func(f *TargetFacts) { f.TooSmall = true }), nil, true, []string{"disk:disk_too_small"}},
		{"bios", auto, nil, ok(), nil, false, []string{":firmware_not_uefi"}},
		{"every reason at once", auto, nil,
			with(func(f *TargetFacts) { f.BootDisk, f.ReadOnly, f.TooSmall = true, true, true }), nil, false,
			[]string{"disk:disk_boot_disk", "disk:disk_read_only", "disk:disk_too_small", ":firmware_not_uefi"}},
		{"verdict unavailable fails closed", auto, nil, &TargetFacts{Err: errors.New("lsblk: not found")}, nil, true,
			[]string{"disk:disk_unverified"}},

		// The explicit override covers too_small and uefi, and nothing else.
		{"override too_small", auto, []string{"too_small"}, with(func(f *TargetFacts) { f.TooSmall = true }), nil, true, nil},
		{"override uefi", auto, []string{"uefi"}, ok(), nil, false, nil},
		{"override does not reach the hard errors", auto, []string{"too_small", "uefi"},
			with(func(f *TargetFacts) { f.BootDisk, f.ReadOnly, f.TooSmall = true, true, true }), nil, false,
			[]string{"disk:disk_boot_disk", "disk:disk_read_only"}},
		{"override does not reach not_disk", auto, []string{"too_small", "uefi"},
			with(func(f *TargetFacts) { f.NotDisk = true }), nil, true, []string{"disk:disk_not_disk"}},

		// A loop device is an image for another machine: this machine's
		// firmware does not matter. probe never marks one too small.
		{"loop device on a bios host", auto, nil, with(func(f *TargetFacts) { f.Loop = true }), nil, false, nil},
		{"loop device that holds the system", auto, nil,
			with(func(f *TargetFacts) { f.Loop, f.BootDisk = true, true }), nil, false, []string{"disk:disk_boot_disk"}},

		{"varDisk eligible", withVar, nil, ok(), with(func(f *TargetFacts) { f.Device = "/dev/sdb" }), true, nil},
		{"varDisk is a partition", withVar, nil, ok(), with(func(f *TargetFacts) { f.NotDisk = true }), true,
			[]string{"varDisk.disk:var_disk_not_disk"}},
		{"varDisk holds the system, read-only", withVar, nil, ok(),
			with(func(f *TargetFacts) { f.Device = "/dev/sdb"; f.BootDisk, f.ReadOnly = true, true }), true,
			[]string{"varDisk.disk:var_disk_boot_disk", "varDisk.disk:var_disk_read_only"}},
		{"varDisk has no minimum size", withVar, nil, ok(),
			with(func(f *TargetFacts) { f.Device = "/dev/sdb"; f.TooSmall = true }), true, nil},
		{"varDisk unverified", withVar, nil, ok(), &TargetFacts{Err: errors.New("x")}, true,
			[]string{"varDisk.disk:var_disk_unverified"}},
		{"varDisk is another name for the disk", withVar, nil, ok(), ok(), true,
			[]string{"varDisk.disk:var_disk_same_as_disk"}},

		{"manual layout: no disk verdict, firmware still applies", manual, nil, nil, nil, false, []string{":firmware_not_uefi"}},
		{"manual layout on uefi", manual, nil, nil, nil, true, nil},
		{"missing disk is Problems' to report", auto, nil, nil, nil, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.r
			r.Override = tt.override
			if got := codes(r.CheckTarget(tt.disk, tt.varDisk, tt.uefi)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CheckTarget = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckOverride(t *testing.T) {
	if ps := CheckOverride([]string{"too_small", "uefi"}); ps != nil {
		t.Errorf("valid override rejected: %v", ps)
	}
	if ps := CheckOverride(nil); ps != nil {
		t.Errorf("absent override rejected: %v", ps)
	}
	for _, bad := range []string{"boot_disk", "read_only", "not_disk", "", "UEFI"} {
		ps := CheckOverride([]string{"uefi", bad})
		if len(ps) != 1 || ps[0].Code != CodeOverrideUnsupported || ps[0].Field != "override[1]" {
			t.Errorf("CheckOverride(%q) = %v", bad, ps)
		}
	}
	// Problems runs it.
	r := Recipe{Disk: "/dev/null", Filesystem: "xfs", Hostname: "h", Override: []string{"boot_disk"}}
	found := false
	for _, p := range r.Problems() {
		found = found || p.Code == CodeOverrideUnsupported
	}
	if !found {
		t.Error("Problems() does not apply the override rule")
	}
}
