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

// tuna-os/fisherman#415: a manual layout needed only a root entry.
func TestCheckCustomMountsLayout(t *testing.T) {
	root := func(fs string) CustomMount { return CustomMount{Partition: "/dev/sda3", Target: "/", Fstype: fs} }
	esp := CustomMount{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "unformatted"}
	boot := func(fs string) CustomMount { return CustomMount{Partition: "/dev/sda2", Target: "/boot", Fstype: fs} }
	tests := []struct {
		name       string
		bootloader string
		mounts     []CustomMount
		want       []string
	}{
		{"xfs root, esp, ext4 /boot", "", []CustomMount{root("xfs"), esp, boot("ext4")}, nil},
		{"xfs root, esp, kept /boot", "grub2", []CustomMount{root("xfs"), esp, boot("unformatted")}, nil},
		{"ext4 root needs no /boot", "", []CustomMount{root("ext4"), esp}, nil},
		{"btrfs root needs no /boot", "grub2", []CustomMount{root("btrfs"), esp}, nil},
		{"kept root: fstype unknown, not judged", "", []CustomMount{root("unformatted"), esp}, nil},
		{"systemd-boot reads the ESP", "systemd", []CustomMount{root("xfs"), esp}, nil},
		{"trailing slash on the ESP target", "systemd", []CustomMount{root("xfs"), {Partition: "/dev/sda1", Target: "/boot/efi/", Fstype: "fat32"}}, nil},
		{"no esp", "", []CustomMount{root("ext4")}, []string{"customMounts:custom_mounts_no_esp"}},
		{"/efi is not where the install looks", "systemd", []CustomMount{root("ext4"), {Partition: "/dev/sda1", Target: "/efi", Fstype: "fat32"}},
			[]string{"customMounts:custom_mounts_no_esp"}},
		{"grub2, xfs root, no /boot", "grub2", []CustomMount{root("xfs"), esp}, []string{"customMounts:custom_mounts_no_boot"}},
		{"default bootloader is grub2", "", []CustomMount{root("xfs"), esp}, []string{"customMounts:custom_mounts_no_boot"}},
		{"an xfs /boot does not help", "", []CustomMount{root("xfs"), esp, boot("xfs")}, []string{"customMounts:custom_mounts_no_boot"}},
		{"neither", "", []CustomMount{root("xfs")}, []string{"customMounts:custom_mounts_no_esp", "customMounts:custom_mounts_no_boot"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Recipe{Bootloader: tt.bootloader, CustomMounts: tt.mounts}
			if got := codes(CheckCustomMountsLayout(r)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CheckCustomMountsLayout = %v, want %v", got, tt.want)
			}
		})
	}
	// Problems runs it.
	r := &Recipe{Hostname: "h", CustomMounts: []CustomMount{{Partition: "/dev/null", Target: "/", Fstype: "xfs"}}}
	got := codes(r.Problems())
	want := []string{"customMounts:custom_mounts_no_esp", "customMounts:custom_mounts_no_boot"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Problems() = %v, want %v", got, want)
	}
}

// tuna-os/fisherman#414: image and targetImgref reached skopeo, podman and
// bootc's --target-imgref unchecked, after the disk was partitioned.
func TestCheckImageRefs(t *testing.T) {
	for _, ok := range []string{"", "ghcr.io/example/os:stable", "docker://quay.io/a/b@sha256:" + strings.Repeat("a", 64),
		"containers-storage:localhost/x", "oci:/run/media/oci", "x"} {
		r := &Recipe{Image: ok, TargetImgref: ok}
		if ps := CheckImageRefs(r); ps != nil {
			t.Errorf("CheckImageRefs(%q) = %v, want valid", ok, ps)
		}
	}
	for _, bad := range []string{"-v", "--privileged", " ", "a b", "ghcr.io/x\nghcr.io/y", "x\t", "x\x00", "x y", "x "} {
		got := codes(CheckImageRefs(&Recipe{Image: bad, TargetImgref: bad}))
		want := []string{"image:" + CodeImageInvalid, "targetImgref:" + CodeTargetImgrefInvalid}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("CheckImageRefs(%q) = %v, want %v", bad, got, want)
		}
	}
	// With sourceReuse the sourceReuse rules judge both fields: one
	// problem per field, not two.
	r := &Recipe{Image: "-v", TargetImgref: "a b", SourceReuse: &SourceReuse{}}
	if ps := CheckImageRefs(r); ps != nil {
		t.Errorf("CheckImageRefs with sourceReuse = %v, want nil", ps)
	}
	// Problems runs it.
	r = &Recipe{Disk: "/dev/null", Filesystem: "xfs", Hostname: "h", Image: "ghcr.io/x\n", TargetImgref: "-x"}
	got := map[string]bool{}
	for _, p := range r.Problems() {
		got[p.Field+":"+p.Code] = true
	}
	for _, want := range []string{"image:" + CodeImageInvalid, "targetImgref:" + CodeTargetImgrefInvalid} {
		if !got[want] {
			t.Errorf("Problems() did not report %s: %v", want, got)
		}
	}
}
