package disk

import (
	"fmt"
	"reflect"
	"testing"
)

// The layout table writes the sfdisk scripts the partition functions wrote
// before it existed (#371), line for line.
func TestLayoutScripts(t *testing.T) {
	for _, c := range []struct {
		layout Layout
		want   string
	}{
		{LayoutGRUB2, "label: gpt\n\n" +
			`size=2GiB, type=uefi, name="EFI-SYSTEM"` + "\n" +
			`size=2GiB, type=linux, name="boot"` + "\n" +
			`type=linux, name="root"` + "\n"},
		{LayoutSystemdBoot, "label: gpt\n\n" +
			`size=2GiB, type=uefi, name="EFI-SYSTEM"` + "\n" +
			fmt.Sprintf(`type=%s, name="root"`, LinuxRootPartType()) + "\n"},
		{LayoutZFS, "label: gpt\n\n" +
			`size=1GiB, type=uefi, name="EFI-SYSTEM"` + "\n" +
			`type=linux, name="zfs-pool"` + "\n"},
	} {
		parts, err := LayoutPartitions(c.layout)
		if err != nil {
			t.Fatal(err)
		}
		if got := layoutScript(parts); got != c.want {
			t.Errorf("%s script:\n%s\nwant:\n%s", c.layout, got, c.want)
		}
		plan, err := parsePlan(layoutScript(parts))
		if err != nil || len(plan) != len(parts) {
			t.Errorf("%s: verifyLayout reads %d partitions (%v), the table has %d", c.layout, len(plan), err, len(parts))
		}
	}
	if _, err := LayoutPartitions("nope"); err == nil {
		t.Error("an unknown layout is accepted")
	}
}

func TestLayoutPartNumbers(t *testing.T) {
	for _, c := range []struct {
		layout           Layout
		efi, boot, rootN int
	}{
		{LayoutGRUB2, 1, 2, 3},
		{LayoutSystemdBoot, 1, 0, 2},
		{LayoutZFS, 1, 0, 2},
	} {
		if got := [3]int{c.layout.PartNumber(RoleEFI), c.layout.PartNumber(RoleBoot), c.layout.PartNumber(RoleRoot)}; got != [3]int{c.efi, c.boot, c.rootN} {
			t.Errorf("%s: efi/boot/root = %v", c.layout, got)
		}
	}
}

func TestMountActionsAndOrder(t *testing.T) {
	specs := []MountSpec{
		{"/dev/sda1", "/boot/efi", "unformatted"},
		{"/dev/sda2", "/boot", "ext4"},
		{"/dev/sda5", "", "xfs"},
		{"/dev/sda3", "/", ""},
		{"/dev/sda4", "swap", "swap"},
		{"/dev/sda6", "swap", "xfs"},
	}
	var got []string
	for _, s := range OrderMounts(specs) {
		got = append(got, s.Partition+" "+string(s.Action()))
	}
	want := []string{"/dev/sda3 preserve", "/dev/sda5 ignore", "/dev/sda4 swap", "/dev/sda6 ignore", "/dev/sda2 format", "/dev/sda1 preserve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order and actions %v, want %v", got, want)
	}
	if specs[0].Partition != "/dev/sda1" {
		t.Error("OrderMounts changed its argument")
	}
}
