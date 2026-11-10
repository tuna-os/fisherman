package recipe

import (
	"reflect"
	"strings"
	"testing"
)

// tuna-os/fisherman#414: these reached zpool, cryptsetup and `podman run`
// unchecked, after the disk was partitioned.

func TestCheckZFSPoolName(t *testing.T) {
	for _, ok := range []string{"", "rpool", "tank", "Pool_1", "my-pool.2", "a", strings.Repeat("a", 63)} {
		if ps := CheckZFSPoolName(ok); ps != nil {
			t.Errorf("CheckZFSPoolName(%q) = %v, want valid", ok, ps)
		}
	}
	for _, bad := range []string{"-f", "1pool", "_pool", "my pool", "a/b", "a:b", "pool\n", "mirror", "mirror2",
		"raidz", "raidz2", "draid", "spare", "log", "c0", "c1t0d0", "ü", strings.Repeat("a", 64)} {
		ps := CheckZFSPoolName(bad)
		if len(ps) != 1 || ps[0].Code != CodeZFSPoolNameInvalid || ps[0].Field != "zfsPoolName" {
			t.Errorf("CheckZFSPoolName(%q) = %v, want %s", bad, ps, CodeZFSPoolNameInvalid)
		}
	}
}

func TestCheckLuksMapperName(t *testing.T) {
	for _, ok := range []string{"", "fisherman-root", "luks-1234", "ci_job.7+x", "0", strings.Repeat("a", 63)} {
		if ps := CheckLuksMapperName(ok); ps != nil {
			t.Errorf("CheckLuksMapperName(%q) = %v, want valid", ok, ps)
		}
	}
	for _, bad := range []string{"../sda", "a/b", "..", ".", ".hidden", "-x", "a b", "a\nb", "control", "a=b", strings.Repeat("a", 64)} {
		ps := CheckLuksMapperName(bad)
		if len(ps) != 1 || ps[0].Code != CodeLuksMapperNameInvalid || ps[0].Field != "luksMapperName" {
			t.Errorf("CheckLuksMapperName(%q) = %v, want %s", bad, ps, CodeLuksMapperNameInvalid)
		}
	}
}

func TestCheckAdditionalImageStores(t *testing.T) {
	if ps := CheckAdditionalImageStores(nil); ps != nil {
		t.Errorf("absent stores: %v", ps)
	}
	if ps := CheckAdditionalImageStores([]string{}); ps != nil {
		t.Errorf("empty stores: %v", ps)
	}
	if ps := CheckAdditionalImageStores([]string{"/usr/share/tuna-installer/oci-store", "/var/lib/superiso-store/"}); ps != nil {
		t.Errorf("valid stores: %v", ps)
	}
	stores := []string{"/ok", "relative:path", "relative", "/a:/b", "/a/../etc", "/a,b", "/a\nb", ""}
	var got []string
	for _, p := range CheckAdditionalImageStores(stores) {
		if p.Code != CodeAdditionalImageStoreInvalid {
			t.Errorf("code = %q", p.Code)
		}
		got = append(got, p.Field)
	}
	want := []string{"additionalImageStores[1]", "additionalImageStores[2]", "additionalImageStores[3]",
		"additionalImageStores[4]", "additionalImageStores[5]", "additionalImageStores[6]", "additionalImageStores[7]"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
}

// The recipes the issue ran through `validate`, which all returned valid.
func TestProblemsRejectsIssue414Names(t *testing.T) {
	r := Recipe{Disk: "/dev/null", Filesystem: "zfs", Hostname: "h",
		ZFSPoolName: "-f", LuksMapperName: "../sda", AdditionalImageStores: []string{"relative:path"}}
	want := map[string]bool{CodeZFSPoolNameInvalid: true, CodeLuksMapperNameInvalid: true, CodeAdditionalImageStoreInvalid: true}
	for _, p := range r.Problems() {
		delete(want, p.Code)
	}
	if len(want) != 0 {
		t.Errorf("Problems() did not report %v", want)
	}
}
