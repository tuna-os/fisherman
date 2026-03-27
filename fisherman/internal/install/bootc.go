package install

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Options configures a bootc installation.
type Options struct {
	// SourceImgref is the OCI image to install from (always passed as --source-imgref).
	SourceImgref string
	// TargetImgref is the update-tracking reference (passed as --target-imgref only when
	// non-empty and different from SourceImgref).
	TargetImgref string
	// SelinuxDisabled passes --disable-selinux when true.
	SelinuxDisabled bool
	// Target is the path to the mounted root filesystem.
	Target string
}

// BootcInstall runs `bootc install to-filesystem` with real-time stdout/stderr relay.
func BootcInstall(opts Options) error {
	args := []string{"install", "to-filesystem"}

	args = append(args, "--source-imgref", opts.SourceImgref)

	if opts.TargetImgref != "" {
		args = append(args, "--target-imgref", opts.TargetImgref)
	}

	if opts.SelinuxDisabled {
		args = append(args, "--disable-selinux")
	}

	args = append(args, opts.Target)

	cmd := exec.Command("bootc", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout

	fmt.Fprintf(os.Stdout, "+ bootc %s\n", strings.Join(args, " "))

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bootc install to-filesystem: %w", err)
	}
	return nil
}
