package install

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Options configures a bootc installation.
type Options struct {
	// SourceImgref is the OCI image to run bootc from (e.g. "quay.io/tuna-os/yellowfin:gnome-hwe").
	// When empty, fisherman is assumed to already be running inside the container image
	// (live-ISO mode) and bootc is called directly.
	// When set, bootc is invoked via `podman run --privileged <image> bootc install to-filesystem`
	// per the bootc requirement that install must run FROM the container image.
	SourceImgref string
	// TargetImgref is the update-tracking reference written into the installed system
	// for day-2 updates (--target-imgref). If empty, defaults to SourceImgref.
	TargetImgref string
	// SelinuxDisabled passes --disable-selinux when true.
	SelinuxDisabled bool
	// UnifiedStorage passes --experimental-unified-storage when true.
	// See: https://bootc-dev.github.io/bootc/unified-storage.html
	UnifiedStorage bool
	// Target is the path to the mounted root filesystem on the host.
	Target string
}

// BootcInstall installs a bootc image to a pre-mounted filesystem.
//
// If opts.SourceImgref is set, bootc is run inside the source container via
// `podman run --privileged`, as required by the bootc documentation.
// If opts.SourceImgref is empty (live-ISO mode), bootc is called directly —
// bootc auto-detects the running container image as the install source.
func BootcInstall(opts Options) error {
	if opts.SourceImgref != "" {
		return bootcViaContainer(opts)
	}
	return bootcDirect(opts)
}

// bootcViaContainer runs bootc from inside the source container image.
// This is the required approach when not already running inside the image.
func bootcViaContainer(opts Options) error {
	targetImgref := opts.TargetImgref
	if targetImgref == "" {
		targetImgref = opts.SourceImgref
	}

	bootcArgs := []string{"install", "to-filesystem"}
	if targetImgref != "" {
		bootcArgs = append(bootcArgs, "--target-imgref", targetImgref)
	}
	if opts.SelinuxDisabled {
		bootcArgs = append(bootcArgs, "--disable-selinux")
	}
	if opts.UnifiedStorage {
		bootcArgs = append(bootcArgs, "--experimental-unified-storage")
	}
	bootcArgs = append(bootcArgs, "--skip-finalize")
	bootcArgs = append(bootcArgs, "/target")

	podmanArgs := []string{
		"run", "--rm",
		"--privileged",
		"--pid=host",
		"--security-opt", "label=type:unconfined_t",
		"-v", "/dev:/dev",
		// Required: gives bootc access to its own image layers in container storage.
		"-v", "/var/lib/containers:/var/lib/containers",
		// Use shared propagation so submounts (e.g. /boot/efi) created on the host
		// before launching the container are visible inside it at /target.
		"--mount", fmt.Sprintf("type=bind,src=%s,dst=/target,bind-propagation=rslave", opts.Target),
		opts.SourceImgref,
	}
	podmanArgs = append(podmanArgs, "bootc")
	podmanArgs = append(podmanArgs, bootcArgs...)

	fmt.Fprintf(os.Stdout, "+ podman %s\n", strings.Join(podmanArgs, " "))

	cmd := exec.Command("podman", podmanArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bootc install to-filesystem (via container): %w", err)
	}
	return nil
}

// bootcDirect calls bootc install to-filesystem directly.
// Only valid when fisherman is already running inside the bootc container image
// (i.e. on the live ISO), where bootc auto-detects the source image.
func bootcDirect(opts Options) error {
	args := []string{"install", "to-filesystem"}
	if opts.TargetImgref != "" {
		args = append(args, "--target-imgref", opts.TargetImgref)
	}
	if opts.SelinuxDisabled {
		args = append(args, "--disable-selinux")
	}
	if opts.UnifiedStorage {
		args = append(args, "--experimental-unified-storage")
	}
	args = append(args, "--skip-finalize")
	args = append(args, opts.Target)

	fmt.Fprintf(os.Stdout, "+ bootc %s\n", strings.Join(args, " "))

	cmd := exec.Command("bootc", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bootc install to-filesystem: %w", err)
	}
	return nil
}
