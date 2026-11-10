package validation

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

func toolNames(ts []ToolRequirement) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Tool)
	}
	return out
}

// tuna-os/fisherman#415: the list came from `filesystem`, which is empty on a
// manual layout, always asked for sfdisk, and never for mkfs.ext3, mkswap or
// swapon.
func TestRequiredTools(t *testing.T) {
	manual := func(mounts ...recipe.CustomMount) *recipe.Recipe { return &recipe.Recipe{CustomMounts: mounts} }
	tests := []struct {
		name string
		r    *recipe.Recipe
		want []string
	}{
		{"auto xfs", &recipe.Recipe{Filesystem: "xfs"},
			[]string{"sfdisk", "mkfs.fat", "mkfs.ext4", "mkfs.xfs", "skopeo", "podman"}},
		{"auto ext4", &recipe.Recipe{Filesystem: "ext4"},
			[]string{"sfdisk", "mkfs.fat", "mkfs.ext4", "skopeo", "podman"}},
		{"auto btrfs with luks and tpm", &recipe.Recipe{Filesystem: "btrfs", Encryption: recipe.Encryption{Type: "tpm2-luks-passphrase"}},
			[]string{"sfdisk", "mkfs.fat", "mkfs.ext4", "mkfs.btrfs", "cryptsetup", "systemd-cryptenroll", "skopeo", "podman"}},
		{"auto luks passphrase", &recipe.Recipe{Filesystem: "xfs", Encryption: recipe.Encryption{Type: "luks-passphrase"}},
			[]string{"sfdisk", "mkfs.fat", "mkfs.ext4", "mkfs.xfs", "cryptsetup", "skopeo", "podman"}},
		{"auto zfs", &recipe.Recipe{Filesystem: "zfs"},
			[]string{"sfdisk", "mkfs.fat", "mkfs.ext4", "zpool", "zfs", "skopeo", "podman"}},
		{"auto ext4 with a formatted /var disk", &recipe.Recipe{Filesystem: "ext4", VarDisk: &recipe.VarDiskSpec{Disk: "/dev/sdb"}},
			[]string{"sfdisk", "mkfs.fat", "mkfs.ext4", "mkfs.xfs", "skopeo", "podman"}},
		{"auto ext4 with a kept /var disk", &recipe.Recipe{Filesystem: "ext4", VarDisk: &recipe.VarDiskSpec{Disk: "/dev/sdb", KeepExisting: true}},
			[]string{"sfdisk", "mkfs.fat", "mkfs.ext4", "skopeo", "podman"}},

		{"manual xfs root: mkfs.xfs, and no sfdisk", manual(
			recipe.CustomMount{Partition: "/dev/sda2", Target: "/", Fstype: "xfs"},
			recipe.CustomMount{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "unformatted"}),
			[]string{"mkfs.xfs", "skopeo", "podman"}},
		{"manual: every fstype, in recipe order, once", manual(
			recipe.CustomMount{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "fat32"},
			recipe.CustomMount{Partition: "/dev/sda2", Target: "/boot", Fstype: "ext3"},
			recipe.CustomMount{Partition: "/dev/sda3", Target: "/", Fstype: "btrfs"},
			recipe.CustomMount{Partition: "/dev/sda4", Target: "/home", Fstype: "ext4"},
			recipe.CustomMount{Partition: "/dev/sda5", Target: "/srv", Fstype: "xfs"},
			recipe.CustomMount{Partition: "/dev/sda6", Target: "/data", Fstype: "xfs"},
			recipe.CustomMount{Partition: "/dev/sda7", Target: "swap", Fstype: "swap"}),
			[]string{"mkfs.fat", "mkfs.ext3", "mkfs.btrfs", "mkfs.ext4", "mkfs.xfs", "mkswap", "swapon", "swapoff", "skopeo", "podman"}},
		{"manual: nothing formatted", manual(
			recipe.CustomMount{Partition: "/dev/sda2", Target: "/", Fstype: ""},
			recipe.CustomMount{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "unformatted"},
			// Not activated by the install: target swap needs fstype swap.
			recipe.CustomMount{Partition: "/dev/sda3", Target: "swap", Fstype: "unformatted"},
			// No target: neither formatted nor mounted.
			recipe.CustomMount{Partition: "/dev/sda4", Target: "", Fstype: "xfs"}),
			[]string{"skopeo", "podman"}},
		{"manual ignores `filesystem` and encryption tools", &recipe.Recipe{Filesystem: "btrfs",
			CustomMounts: []recipe.CustomMount{{Partition: "/dev/sda2", Target: "/", Fstype: "ext4"}}},
			[]string{"mkfs.ext4", "skopeo", "podman"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolNames(RequiredTools(tt.r)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RequiredTools = %v\nwant           %v", got, tt.want)
			}
		})
	}
}

func TestMissingToolsAndValidator(t *testing.T) {
	r := &recipe.Recipe{CustomMounts: []recipe.CustomMount{
		{Partition: "/dev/sda2", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda3", Target: "swap", Fstype: "swap"},
	}}
	absent := map[string]bool{"mkfs.xfs": true, "swapon": true}
	look := func(tool string) error {
		if absent[tool] {
			return errors.New("not found")
		}
		return nil
	}
	if got := toolNames(MissingTools(r, look)); !reflect.DeepEqual(got, []string{"mkfs.xfs", "swapon"}) {
		t.Errorf("MissingTools = %v", got)
	}
	v := NewRecipeDependentToolValidator(r)
	v.Look = look
	err := v.Validate()
	if err == nil || !strings.Contains(err.Error(), `"mkfs.xfs"`) || !strings.Contains(err.Error(), `"xfsprogs"`) {
		t.Errorf("Validate() = %v, want the first missing tool and its package", err)
	}
	v.Look = func(string) error { return nil }
	if err := v.Validate(); err != nil {
		t.Errorf("Validate() with every tool present = %v", err)
	}
}

// Inside a Flatpak sandbox the tools run on the host (runner wraps them in
// flatpak-spawn --host), so the lookup must ask the host too.
func TestHostLookPathAsksTheHostInASandbox(t *testing.T) {
	oldIn, oldOut := inFlatpak, hostOutput
	t.Cleanup(func() { inFlatpak, hostOutput = oldIn, oldOut })

	var calls []string
	inFlatpak = func() bool { return true }
	hostOutput = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if args[len(args)-1] == "mkfs.xfs" {
			return nil, errors.New("exit status 1")
		}
		return []byte("/usr/sbin/tool\n"), nil
	}
	// "sh" exists in any sandbox PATH; the host is asked all the same.
	if err := HostLookPath("sh"); err != nil {
		t.Errorf("HostLookPath(sh) = %v", err)
	}
	if err := HostLookPath("mkfs.xfs"); err == nil {
		t.Error("HostLookPath(mkfs.xfs) = nil, though the host does not have it")
	}
	want := []string{`sh -c command -v -- "$1" sh sh`, `sh -c command -v -- "$1" sh mkfs.xfs`}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("host calls = %q, want %q", calls, want)
	}

	inFlatpak = func() bool { return false }
	hostOutput = func(string, ...string) ([]byte, error) { t.Error("asked the host outside a sandbox"); return nil, nil }
	if err := HostLookPath("sh"); err != nil {
		t.Errorf("native HostLookPath(sh) = %v", err)
	}
	if err := HostLookPath("fisherman-no-such-tool"); err == nil {
		t.Error("native HostLookPath found a tool that does not exist")
	}
}
