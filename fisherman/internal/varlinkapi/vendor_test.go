package varlinkapi

import "testing"

func TestAPCIIDIsNoDiskName(t *testing.T) {
	for in, want := range map[string]string{"0x1af4": "", "0X1AF4": "", "ATA": "ATA", "0xyz": "0xyz", "": ""} {
		if got := vendorName(in); got != want {
			t.Errorf("vendorName(%q) = %q, want %q", in, got, want)
		}
	}
	c := candidate(Disk{Path: "/dev/vda", Transport: "virtio"})
	if c.Vendor != nil || c.Model == nil || *c.Model != "VirtIO disk" {
		t.Errorf("candidate = %+v", c)
	}
	named := candidate(Disk{Path: "/dev/sda", Vendor: "ATA", Transport: "sata"})
	if named.Model != nil || named.Vendor == nil || *named.Vendor != "ATA" {
		t.Errorf("candidate = %+v", named)
	}
}
