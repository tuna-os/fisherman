package recipe

import "testing"

func fieldCodes(ps []Problem) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Field+":"+p.Code)
	}
	return out
}

// tuna-os/fisherman#413: ZFS has no VM test, so `validate` and the install
// refuse it unless the recipe says it knows.
func TestZFSIsExperimental(t *testing.T) {
	plain := &Recipe{Filesystem: "zfs"}
	if got := fieldCodes(CheckExperimentalFilesystem(plain)); len(got) != 1 || got[0] != "filesystem:zfs_experimental" {
		t.Errorf("zfs without the opt-in: %v", got)
	}
	opted := &Recipe{Filesystem: "zfs", Experimental: []string{"zfs"}}
	if got := CheckExperimentalFilesystem(opted); len(got) != 0 {
		t.Errorf("zfs with the opt-in: %v", got)
	}
	for _, fs := range []string{"xfs", "ext4", "btrfs", ""} {
		if got := CheckExperimentalFilesystem(&Recipe{Filesystem: fs}); len(got) != 0 {
			t.Errorf("%q: %v", fs, got)
		}
	}
	// The opt-in is harmless on another filesystem.
	if got := CheckExperimentalFilesystem(&Recipe{Filesystem: "xfs", Experimental: []string{"zfs"}}); len(got) != 0 {
		t.Errorf("xfs with the zfs opt-in: %v", got)
	}
}

func TestCheckExperimental(t *testing.T) {
	if got := CheckExperimental(nil); len(got) != 0 {
		t.Errorf("nil: %v", got)
	}
	if got := CheckExperimental([]string{"zfs"}); len(got) != 0 {
		t.Errorf("zfs: %v", got)
	}
	got := fieldCodes(CheckExperimental([]string{"zfs", "bcachefs"}))
	if len(got) != 1 || got[0] != "experimental[1]:experimental_unsupported" {
		t.Errorf("unknown entry: %v", got)
	}
}

// The whole rule set (what the install and `validate` run): a zfs recipe is
// refused with the stable code, and accepted with the opt-in.
func TestProblems_ZFSOptIn(t *testing.T) {
	r := Recipe{Disk: "/dev/null", Filesystem: "zfs", Hostname: "h", Image: "x"}
	found := false
	for _, p := range r.Problems() {
		if p.Code == CodeZFSExperimental {
			found = true
		}
	}
	if !found {
		t.Errorf("Problems() of a zfs recipe lack %s: %v", CodeZFSExperimental, fieldCodes(r.Problems()))
	}
	r.Experimental = []string{"zfs"}
	for _, p := range r.Problems() {
		if p.Code == CodeZFSExperimental || p.Code == CodeExperimentalUnsupported {
			t.Errorf("opted-in zfs recipe still refused: %v", p)
		}
	}
	// A manual layout has no `filesystem`; the rule does not apply.
	m := Recipe{Hostname: "h", Image: "x", CustomMounts: []CustomMount{{Partition: "/dev/null", Target: "/", Fstype: "xfs"}}}
	for _, p := range m.Problems() {
		if p.Code == CodeZFSExperimental {
			t.Errorf("manual layout refused as zfs: %v", p)
		}
	}
}
