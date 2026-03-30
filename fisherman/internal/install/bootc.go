package install

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/tuna-os/fisherman/internal/progress"
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
	// ComposeFsBackend passes --composefs-backend when true.
	// Required for images using the composefs-native deployment backend (e.g. ghcr.io/bootcrew/*).
	ComposeFsBackend bool
	// Target is the path to the mounted root filesystem on the host.
	Target string
	// NeedsPull is the result of a pre-flight CheckImage call. When false,
	// the image pull is skipped (image already in containers-storage).
	NeedsPull bool
	// LayerCount is the number of image layers from CheckImage, used to
	// show "layer N/total" progress. 0 means unknown.
	LayerCount int
}

// BuildBootcArgs builds the argument slice for `bootc install to-filesystem`.
// resolvedTargetImgref is the --target-imgref value (empty to omit the flag).
// installTarget is the final positional argument (e.g. "/target" in container mode,
// or opts.Target in direct mode).
func BuildBootcArgs(opts Options, resolvedTargetImgref, installTarget string) []string {
	args := []string{"install", "to-filesystem"}
	if resolvedTargetImgref != "" {
		args = append(args, "--target-imgref", resolvedTargetImgref)
	}
	if opts.SelinuxDisabled {
		args = append(args, "--disable-selinux")
	}
	if opts.UnifiedStorage {
		args = append(args, "--experimental-unified-storage")
	}
	if opts.ComposeFsBackend {
		args = append(args, "--composefs-backend")
	}
	args = append(args, "--skip-finalize")
	args = append(args, installTarget)
	return args
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

	if opts.NeedsPull {
		if err := pullImage(opts.SourceImgref, opts.LayerCount); err != nil {
			return fmt.Errorf("pulling image: %w", err)
		}
	} else {
		progress.Substep("Image already up to date, skipping pull")
	}

	bootcArgs := BuildBootcArgs(opts, targetImgref, "/target")

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
	if err := runWithSubsteps(cmd); err != nil {
		return fmt.Errorf("bootc install to-filesystem (via container): %w", err)
	}
	return nil
}

// bootcDirect calls bootc install to-filesystem directly.
// Only valid when fisherman is already running inside the bootc container image
// (i.e. on the live ISO), where bootc auto-detects the source image.
func bootcDirect(opts Options) error {
	args := BuildBootcArgs(opts, opts.TargetImgref, opts.Target)

	fmt.Fprintf(os.Stdout, "+ bootc %s\n", strings.Join(args, " "))

	cmd := exec.Command("bootc", args...)
	if err := runWithSubsteps(cmd); err != nil {
		return fmt.Errorf("bootc install to-filesystem: %w", err)
	}
	return nil
}

// pullImage uses skopeo to download the container image into podman's storage.
// layerCount is the expected number of layers (from CheckImage), used for
// "layer N/total" progress display. Pass 0 if unknown.
func pullImage(image string, layerCount int) error {
	progress.Substep("Pulling container image")
	if layerCount > 0 {
		progress.Substep(fmt.Sprintf("Pulling image: %d layers to download", layerCount))
	}

	fmt.Fprintf(os.Stdout, "+ skopeo copy docker://%s containers-storage:%s\n", image, image)
	cmd := exec.Command("skopeo", "copy", "docker://"+image, "containers-storage:"+image)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
		layersDone := 0
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(os.Stdout, line)
			lower := strings.ToLower(line)

			// Count each blob start line — skopeo emits one per blob when piped
			// (no "done" suffix in non-TTY output).
			if strings.HasPrefix(lower, "copying blob sha256:") {
				layersDone++
				if layerCount > 0 {
					progress.Substep(fmt.Sprintf("Pulling image: layer %d/%d", layersDone, layerCount))
				} else {
					progress.Substep(fmt.Sprintf("Pulling image: layer %d", layersDone))
				}
			} else if strings.Contains(lower, "copying config") {
				progress.Substep("Pulling image: copying config")
			} else if strings.Contains(lower, "writing manifest") {
				progress.Substep("Pulling image: writing manifest")
			}
		}
	}()

	err := cmd.Wait()
	pw.Close()
	<-done

	if err != nil {
		return fmt.Errorf("skopeo copy %s: %w", image, err)
	}
	progress.Substep("Image pulled successfully")
	return nil
}

// ImageCheck holds the result of a pre-flight image inspection.
type ImageCheck struct {
	NeedsPull  bool // true if the image is absent or stale in containers-storage
	LayerCount int  // number of layers in the remote image; 0 if unknown
}

// DefaultSkopeoInspect runs `skopeo inspect <args>` and returns stdout.
func DefaultSkopeoInspect(args ...string) ([]byte, error) {
	return exec.Command("skopeo", append([]string{"inspect"}, args...)...).Output()
}

// SkopeoInspectFn is the function used by CheckImage to call skopeo inspect.
// Replace in tests to avoid network calls.
var SkopeoInspectFn = DefaultSkopeoInspect

// CheckImage compares the remote and local (containers-storage) image digests
// to determine whether a pull is required. It also returns the remote layer count.
// On any error (network, auth, not cached), NeedsPull is true (safe fallback).
func CheckImage(image string) ImageCheck {
	type manifest struct {
		Digest string   `json:"Digest"`
		Layers []string `json:"Layers"`
	}

	// 1. Fetch remote normalized manifest (resolves fat/multi-arch manifests).
	fmt.Fprintf(os.Stdout, "+ skopeo inspect docker://%s\n", image)
	remoteOut, err := SkopeoInspectFn("docker://" + image)
	if err != nil {
		return ImageCheck{NeedsPull: true}
	}
	var remote manifest
	if err := json.Unmarshal(remoteOut, &remote); err != nil {
		return ImageCheck{NeedsPull: true}
	}

	// 2. Fetch local digest from containers-storage.
	fmt.Fprintf(os.Stdout, "+ skopeo inspect containers-storage:%s\n", image)
	localOut, err := SkopeoInspectFn("containers-storage:" + image)
	if err != nil {
		// Image not present locally.
		return ImageCheck{NeedsPull: true, LayerCount: len(remote.Layers)}
	}
	var local manifest
	if err := json.Unmarshal(localOut, &local); err != nil {
		return ImageCheck{NeedsPull: true, LayerCount: len(remote.Layers)}
	}

	// 3. Compare digests.
	needsPull := remote.Digest == "" || remote.Digest != local.Digest
	return ImageCheck{NeedsPull: needsPull, LayerCount: len(remote.Layers)}
}

// runWithSubsteps runs a command, relays its combined stdout/stderr line-by-line
// to our stdout, and emits JSON substep events when it recognises bootc progress.
func runWithSubsteps(cmd *exec.Cmd) error {
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		return err
	}

	// Read lines in a goroutine so we don't block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(pr)
		// Increase buffer for long ostree/podman lines.
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
		lastSubstep := ""
		for scanner.Scan() {
			line := scanner.Text()
			// Always relay the raw line to the VTE terminal.
			fmt.Fprintln(os.Stdout, line)
			// Detect bootc / ostree / podman progress keywords and emit substep.
			if sub := ClassifyLine(line); sub != "" && sub != lastSubstep {
				lastSubstep = sub
				progress.Substep(sub)
			}
		}
	}()

	err := cmd.Wait()
	pw.Close()
	<-done
	return err
}

// ClassifyLine maps a raw bootc/ostree/podman output line to a human-readable
// substep description, or "" if the line is not interesting.
func ClassifyLine(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "installing image:"):
		return "Pulling container image"
	case strings.Contains(lower, "layers") && strings.Contains(lower, "needed"):
		// e.g. "layers already present: 0; layers needed: 64 (3.7 GB)"
		if i := strings.Index(lower, "layers needed:"); i >= 0 {
			rest := strings.TrimSpace(line[i+len("layers needed:"):])
			return "Deploying: " + rest
		}
		return "Downloading image layers"
	case strings.Contains(lower, "initializing ostree"):
		return "Initializing ostree layout"
	case strings.Contains(lower, "deploying container image"):
		return "Deploying OS (this may take a while)"
	case strings.Contains(lower, "bootloader:"):
		return "Detected bootloader"
	case strings.Contains(lower, "installing bootloader"):
		return "Installing bootloader"
	case strings.Contains(lower, "efibootmgr"):
		return "Configuring EFI boot entry"
	case strings.Contains(lower, "installed:") && strings.Contains(lower, "grub"):
		return "Configuring GRUB"
	case strings.Contains(lower, "installation complete"):
		return "bootc installation complete"
	case strings.Contains(lower, "selinux"):
		return "Configuring SELinux"
	case strings.Contains(lower, "generating initramfs") || strings.Contains(lower, "dracut"):
		return "Generating initramfs"
	}
	return ""
}
