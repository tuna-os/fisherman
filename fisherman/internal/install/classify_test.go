package install_test

import (
	"testing"

	"github.com/tuna-os/fisherman/internal/install"
)

// TestClassifyLine is a table-driven test for the bootc/ostree/podman log-line
// classifier. The strings on the left come from real bootc/ostree output; the
// substep on the right is what we surface to the GUI. If upstream changes
// wording, this test breaks before silent substep regressions hit production.
func TestClassifyLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"empty", "", ""},
		{"unrelated chatter", "+ podman run --rm --privileged ...", ""},

		{"installing image",
			"Installing image: ghcr.io/projectbluefin/dakota:latest",
			"Deploying image"},

		{"layers needed with size",
			"layers already present: 0; layers needed: 64 (3.7 GB)",
			"Writing 64 (3.7 GB) to disk — this may take several minutes"},

		{"layers needed no size",
			"some context: layers needed: 12",
			"Writing 12 to disk — this may take several minutes"},

		{"initializing ostree",
			"Initializing ostree layout",
			"Initializing ostree layout"},

		{"deploying container image",
			"Deploying container image...done (2 minutes)",
			"OS deployed, installing bootloader"},

		{"bootloader detected",
			"bootloader: systemd",
			"Detected bootloader"},

		{"installing bootloader",
			"Installing bootloader",
			"Installing bootloader"},

		{"efibootmgr",
			"Running efibootmgr -c -d /dev/sda -p 1 -L Fedora",
			"Configuring EFI boot entry"},

		{"grub installed",
			"Installed: grub2-tools-2.06-x86_64",
			"Configuring GRUB"},

		{"installed without grub does not match grub",
			"Installed: some-other-package",
			""},

		{"installation complete",
			"Installation complete",
			"bootc installation complete"},

		{"selinux",
			"Configuring SELinux policy",
			"Configuring SELinux"},

		{"dracut initramfs",
			"running dracut to regenerate initramfs",
			"Generating initramfs"},

		{"generating initramfs phrasing",
			"Generating initramfs for kernel 6.12",
			"Generating initramfs"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := install.ClassifyLine(tc.line); got != tc.want {
				t.Errorf("ClassifyLine(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}
