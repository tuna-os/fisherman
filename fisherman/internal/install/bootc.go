package install

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/runner"
)

// selinuxBypassCSrc is a minimal LD_PRELOAD shim that silently succeeds
// for security.selinux xattr writes. It is compiled at runtime and injected
// into the bootc container when installing a cross-distro image (e.g.
// AlmaLinux) on a host running a different SELinux policy (e.g. Fedora).
//
// Without the shim, two failures occur:
//  1. bootc's imgstorage calls lsetfilecon(), which calls lsetxattr() with
//     "security.selinux" labels from the image. The host kernel rejects
//     unknown types (EINVAL) because they don't exist in the loaded policy.
//  2. libostree writes raw "security.selinux" xattrs from the OCI layer
//     metadata via fsetxattr(). Same EINVAL from the kernel.
//
// The shim intercepts both syscalls and returns 0, so the install proceeds
// without SELinux labels. Since the target system has selinux=disabled, the
// missing labels are harmless.
const selinuxBypassCSrc = `
#define _GNU_SOURCE
#include <dlfcn.h>
#include <sys/xattr.h>
#include <string.h>

int lsetxattr(const char *path, const char *name, const void *value, size_t size, int flags) {
	if (name && strcmp(name, "security.selinux") == 0) return 0;
	static int (*real)(const char*,const char*,const void*,size_t,int);
	if (!real) real = dlsym(RTLD_NEXT, "lsetxattr");
	return real(path, name, value, size, flags);
}

int fsetxattr(int fd, const char *name, const void *value, size_t size, int flags) {
	if (name && strcmp(name, "security.selinux") == 0) return 0;
	static int (*real)(int,const char*,const void*,size_t,int);
	if (!real) real = dlsym(RTLD_NEXT, "fsetxattr");
	return real(fd, name, value, size, flags);
}
`

// BuildSelinuxBypassShim compiles selinuxBypassCSrc into a shared library
// at /tmp/fisherman-selinux-bypass.so and returns its path.
// Returns an error if cc is not available or compilation fails.
func BuildSelinuxBypassShim() (string, error) {
	const (
		srcPath = "/tmp/fisherman-selinux-bypass.c"
		soPath  = "/tmp/fisherman-selinux-bypass.so"
	)
	if err := os.WriteFile(srcPath, []byte(selinuxBypassCSrc), 0644); err != nil {
		return "", fmt.Errorf("writing shim source: %w", err)
	}
	defer os.Remove(srcPath)

	out, err := exec.Command("cc", "-shared", "-fPIC", "-O2", "-nostartfiles", "-ldl",
		"-o", soPath, srcPath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("compiling SELinux bypass shim: %w\n%s", err, out)
	}
	if err := os.Chmod(soPath, 0755); err != nil {
		return "", fmt.Errorf("chmod shim: %w", err)
	}
	return soPath, nil
}

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
	// Bootloader selects the bootloader passed to bootc via --bootloader.
	// Empty or "grub2" uses the default (grub2). "systemd" passes --bootloader systemd.
	Bootloader string
	// Target is the path to the mounted root filesystem on the host.
	Target string
	// ScratchDir is the host-side directory used for temporary I/O during
	// installation (OCI exports, layer blobs, etc.). It is bind-mounted at
	// /var/tmp by the caller. Defaults to "/var/fisherman-tmp" when empty.
	ScratchDir string
	// NeedsPull is the result of a pre-flight CheckImage call. When false,
	// the image pull is skipped (image already in containers-storage).
	NeedsPull bool
	// LayerCount is the number of image layers from CheckImage, used to
	// show "layer N/total" progress. 0 means unknown.
	LayerCount int
}

// scratchDir returns the host-side scratch directory from opts, falling back
// to the default "/var/fisherman-tmp" when unset.
func (o Options) scratchDir() string {
	if o.ScratchDir != "" {
		return o.ScratchDir
	}
	return "/var/fisherman-tmp"
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
		// composefs-backend requires raw OCI blobs; bootcViaContainer exports
		// the image to /var/fisherman-tmp/oci-cache (mounted at /var/tmp inside
		// the container) and passes this as the source.
		args = append(args, "--source-imgref", "oci:/var/tmp/oci-cache")
	}
	if opts.Bootloader != "" && opts.Bootloader != "grub2" {
		args = append(args, "--bootloader", opts.Bootloader)
	}
	args = append(args, "--skip-finalize")
	args = append(args, installTarget)
	return args
}

// selinuxActive reports whether the host kernel has the SELinux security module
// loaded and active. The presence of /sys/fs/selinux/enforce is the standard
// indicator used by libselinux's is_selinux_enabled().
func selinuxActive() bool {
	_, err := os.Stat("/sys/fs/selinux/enforce")
	return err == nil
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

	scratch := opts.scratchDir()

	// composefs-backend requires raw OCI blobs that podman pull doesn't
	// preserve in containers-storage. Export to an OCI layout first, then
	// pass --source-imgref oci:/var/tmp/oci-cache (BuildBootcArgs adds this
	// flag when ComposeFsBackend is true).
	// Note: SkopeoExportOCIFn emits its own progress substeps; don't duplicate them here.
	if opts.ComposeFsBackend {
		ociDir := filepath.Join(scratch, "oci-cache")
		if err := SkopeoExportOCIFn(opts.SourceImgref, ociDir); err != nil {
			return fmt.Errorf("exporting image to OCI layout: %w", err)
		}
	}

	podmanArgs := []string{
		"run", "--rm",
		"--privileged",
		"--pid=host",
		// label=disable fully disables SELinux labeling for this container,
		// allowing bootc to write security.selinux xattrs to the target
		// filesystem without the host SELinux policy interfering.
		"--security-opt", "label=disable",
		"-v", "/dev:/dev",
		// Ostree-based images (e.g. composefs) don't ship /var/tmp — it is created
		// by systemd-tmpfiles on first boot. containers-image needs /var/tmp to write
		// temp files when reconstructing layer blobs from containers-storage. Mount
		// the disk-backed fisherman scratch space so there is always enough room.
		"-v", scratch + ":/var/tmp:z",
		// Use shared propagation so submounts (e.g. /boot/efi) created on the host
		// before launching the container are visible inside it at /target.
		"--mount", fmt.Sprintf("type=bind,src=%s,dst=/target,bind-propagation=rslave", opts.Target),
	}

	if !opts.ComposeFsBackend {
		// Give bootc access to its own image layers in containers-storage.
		// For composefs, we use the OCI layout exported above instead.
		podmanArgs = append(podmanArgs, "-v", "/var/lib/containers:/var/lib/containers")
	}

	// When the target system has SELinux disabled and the host has SELinux
	// active, the host's loaded policy may not recognise the image's label
	// types (e.g. AlmaLinux label types on a Fedora host). Inject an
	// LD_PRELOAD shim that silently drops security.selinux xattr writes,
	// preventing EINVAL from the kernel during both bootc's imgstorage
	// setup (lsetfilecon/lsetxattr) and libostree's layer deployment
	// (fsetxattr). Since the target has selinux=disabled, missing per-file
	// labels are harmless.
	if opts.SelinuxDisabled && selinuxActive() {
		shimPath, shimErr := BuildSelinuxBypassShim()
		if shimErr != nil {
			progress.Info(fmt.Sprintf("warning: SELinux bypass shim unavailable (%v); install may fail on cross-policy images", shimErr))
		} else {
			defer os.Remove(shimPath)
			podmanArgs = append(podmanArgs,
				"-v", shimPath+":/fisherman-selinux-bypass.so:z",
				"-e", "LD_PRELOAD=/fisherman-selinux-bypass.so",
			)
		}
	}

	podmanArgs = append(podmanArgs, opts.SourceImgref)
	podmanArgs = append(podmanArgs, "bootc")
	podmanArgs = append(podmanArgs, bootcArgs...)

	name, args := runner.HostArgs("podman", podmanArgs)
	fmt.Fprintf(os.Stdout, "+ %s %s\n", name, strings.Join(args, " "))

	cmd := exec.Command(name, args...)
	if err := runWithSubsteps(cmd); err != nil {
		return fmt.Errorf("bootc install to-filesystem (via container): %w", err)
	}
	return nil
}

// bootcDirect calls bootc install to-filesystem directly.
// Only valid when fisherman is already running inside the bootc container image
// (i.e. on the live ISO), where bootc auto-detects the source image.
func bootcDirect(opts Options) error {
	bargs := BuildBootcArgs(opts, opts.TargetImgref, opts.Target)

	name, args := runner.HostArgs("bootc", bargs)
	fmt.Fprintf(os.Stdout, "+ %s %s\n", name, strings.Join(args, " "))

	cmd := exec.Command(name, args...)
	if err := runWithSubsteps(cmd); err != nil {
		return fmt.Errorf("bootc install to-filesystem: %w", err)
	}
	return nil
}

// BootcToDisk installs a bootc image directly to a block device using
// `bootc install to-disk --via-loopback`. Unlike BootcInstall (to-filesystem),
// this handles partitioning, formatting, OS deployment, and bootloader
// installation entirely inside the container. --via-loopback auto-enables
// --generic-image which skips EFI NVRAM writes and the bootupd presence check,
// required for images like Project Bluefin/Dakota that don't ship bootupd.
//
// diskDevice may be:
//   - A loop device (/dev/loopN): the backing file is found, the loop device
//     is detached, --via-loopback is passed to bootc, then the loop device is
//     re-attached with partscan on return.
//   - A raw image file (any path not starting with /dev/): --via-loopback is
//     passed directly; after install the file is loop-attached and the
//     assigned loop device is returned as effectiveDisk.
//   - A real block device (/dev/sda, /dev/nvme…): installed directly. Note
//     that images without bootupd (e.g. Dakota) will fail until
//     bootc adds first-class systemd-boot support without requiring bootupd.
//
// effectiveDisk is the block device to use for post-install mounts (may differ
// from diskDevice when a raw file is auto-loop-attached after install).
func BootcToDisk(opts Options, diskDevice, filesystem string) (effectiveDisk string, err error) {
	if opts.SourceImgref != "" {
		return bootcToDiskViaContainer(opts, diskDevice, filesystem)
	}
	eff, err := bootcToDiskDirect(opts, diskDevice, filesystem)
	return eff, err
}

func bootcToDiskViaContainer(opts Options, diskDevice, filesystem string) (effectiveDisk string, err error) {
	targetImgref := opts.TargetImgref
	if targetImgref == "" {
		targetImgref = opts.SourceImgref
	}

	if opts.NeedsPull {
		if err := pullImage(opts.SourceImgref, opts.LayerCount); err != nil {
			return "", fmt.Errorf("pulling image: %w", err)
		}
	} else {
		progress.Substep("Image already up to date, skipping pull")
	}

	bootcArgs := []string{"install", "to-disk"}
	if targetImgref != "" {
		bootcArgs = append(bootcArgs, "--target-imgref", targetImgref)
	}
	if filesystem != "" {
		bootcArgs = append(bootcArgs, "--filesystem", filesystem)
	}
	if opts.ComposeFsBackend {
		bootcArgs = append(bootcArgs, "--composefs-backend")
	}
	bootcArgs = append(bootcArgs, "--bootloader", "systemd", "--wipe")

	isFile := !strings.HasPrefix(diskDevice, "/dev/")
	isLoop := !isFile && strings.HasPrefix(filepath.Base(diskDevice), "loop")

	scratch := opts.scratchDir()

	podmanArgs := []string{
		"run", "--rm",
		"--privileged",
		"--pid=host",
		"--security-opt", "label=disable",
		"-v", "/dev:/dev",
		"-v", scratch + ":/var/tmp:z",
	}

	if opts.ComposeFsBackend {
		// composefs-backend requires raw OCI blobs (compressed layer tarballs)
		// that podman pull does not preserve in containers-storage. Export the
		// image to an OCI directory layout via skopeo so the blobs exist on disk,
		// then pass --source-imgref oci:/var/tmp/oci-cache to bootc.
		ociDir := filepath.Join(scratch, "oci-cache")
		if err := SkopeoExportOCIFn(opts.SourceImgref, ociDir); err != nil {
			return "", fmt.Errorf("exporting image to OCI layout: %w", err)
		}
		// Inside the container, the scratch dir is mounted at /var/tmp.
		bootcArgs = append(bootcArgs, "--source-imgref", "oci:/var/tmp/oci-cache")
		bootcArgs = append(bootcArgs, diskDevice)
		effectiveDisk = diskDevice
	} else {
		// Standard (grub2/ostree) path: bind containers-storage into the container
		// so bootc can read its image layers directly.
		podmanArgs = append(podmanArgs, "-v", "/var/lib/containers:/var/lib/containers")

		// --via-loopback is required for loop devices (BLKRRPART ioctl fails on
		// loop devices so partition nodes never appear inside the container).
		// For regular block devices on the non-composefs path, install directly.
		switch {
		case isFile:
			bootcArgs = append(bootcArgs, "--via-loopback", diskDevice)
		case isLoop:
			backingFile, ferr := loopBackingFile(diskDevice)
			if ferr != nil {
				return "", fmt.Errorf("querying loop backing file: %w", ferr)
			}
			if ferr := loopDetach(diskDevice); ferr != nil {
				return "", fmt.Errorf("detaching loop device: %w", ferr)
			}
			defer func() {
				if rerr := loopReattach(diskDevice, backingFile); rerr != nil {
					fmt.Fprintf(os.Stderr, "warning: re-attaching loop device: %v\n", rerr)
				}
			}()
			bootcArgs = append(bootcArgs, "--via-loopback", backingFile)
			effectiveDisk = diskDevice
		default:
			bootcArgs = append(bootcArgs, diskDevice)
			effectiveDisk = diskDevice
		}

		// For file-based installs, bind-mount the file's directory so the
		// container can access the path passed to --via-loopback.
		if isFile {
			dir := filepath.Dir(diskDevice)
			podmanArgs = append(podmanArgs, "-v", dir+":"+dir)
		}
	}

	podmanArgs = append(podmanArgs, opts.SourceImgref, "bootc")
	podmanArgs = append(podmanArgs, bootcArgs...)

	name, args := runner.HostArgs("podman", podmanArgs)
	fmt.Fprintf(os.Stdout, "+ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	if err := runWithSubsteps(cmd); err != nil {
		return "", fmt.Errorf("bootc install to-disk (via container): %w", err)
	}

	// For raw file targets, loop-attach the now-installed image so the caller
	// can find and mount partitions.
	if isFile && !opts.ComposeFsBackend {
		loopDev, lerr := loopAttachFile(diskDevice)
		if lerr != nil {
			return "", fmt.Errorf("loop-attaching installed image file: %w", lerr)
		}
		effectiveDisk = loopDev
	}
	return effectiveDisk, nil
}

// DefaultSkopeoExportOCI is the default implementation of SkopeoExportOCIFn.
var DefaultSkopeoExportOCI = skopeoExportOCI

// SkopeoExportOCIFn is the function used by bootcToDiskViaContainer to export
// a composefs image to an OCI layout. Replace in tests to avoid disk I/O.
var SkopeoExportOCIFn = skopeoExportOCI

// skopeoExportOCI exports an image from containers-storage to an OCI directory
// layout. The composefs-backend requires raw OCI blobs (compressed layer
// tarballs) that podman pull does not preserve; skopeo reconstructs them from
// the tar-split.gz metadata stored alongside the overlay diffs.
func skopeoExportOCI(image, destDir string) error {
	progress.Substep("Exporting image to OCI layout for composefs install")
	// Remove stale export if present.
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing old OCI cache: %w", err)
	}

	skopeoArgs := []string{
		"copy",
		"containers-storage:" + image,
		"oci:" + destDir,
	}
	name, args := runner.HostArgs("skopeo", skopeoArgs)
	fmt.Fprintf(os.Stdout, "+ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	if err := runWithSubsteps(cmd); err != nil {
		return fmt.Errorf("skopeo copy: %w", err)
	}
	progress.Substep("OCI export complete")
	return nil
}

// loopBackingFile returns the backing file path for a loop device.
func loopBackingFile(loopDev string) (string, error) {
	name, args := runner.HostArgs("losetup", []string{"--noheadings", "-O", "BACK-FILE", loopDev})
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// loopDetach detaches a loop device.
func loopDetach(loopDev string) error {
	name, args := runner.HostArgs("losetup", []string{"-d", loopDev})
	return exec.Command(name, args...).Run()
}

// loopReattach attaches backingFile to loopDev with --partscan so partition
// nodes are visible on the host.
func loopReattach(loopDev, backingFile string) error {
	name, args := runner.HostArgs("losetup", []string{"-P", loopDev, backingFile})
	return exec.Command(name, args...).Run()
}

// loopAttachFile attaches file to a free loop device with partscan and returns
// the assigned device path (e.g. /dev/loop2).
func loopAttachFile(file string) (string, error) {
	name, args := runner.HostArgs("losetup", []string{"--find", "--partscan", "--show", file})
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func bootcToDiskDirect(opts Options, diskDevice, filesystem string) (string, error) {
	bootcArgs := []string{"install", "to-disk"}
	if opts.TargetImgref != "" {
		bootcArgs = append(bootcArgs, "--target-imgref", opts.TargetImgref)
	}
	if filesystem != "" {
		bootcArgs = append(bootcArgs, "--filesystem", filesystem)
	}
	if opts.ComposeFsBackend {
		bootcArgs = append(bootcArgs, "--composefs-backend")
	}
	bootcArgs = append(bootcArgs,
		"--bootloader", "systemd",
		"--via-loopback",
		"--wipe",
		diskDevice,
	)

	name, args := runner.HostArgs("bootc", bootcArgs)
	fmt.Fprintf(os.Stdout, "+ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	if err := runWithSubsteps(cmd); err != nil {
		return "", fmt.Errorf("bootc install to-disk: %w", err)
	}
	return diskDevice, nil
}


// Using podman (rather than skopeo copy) ensures the image lands in the same
// storage location and format that bootc will read from inside its container,
// avoiding "file does not exist" blob errors when CONFIG_OVERLAY_FS_REDIRECT_DIR
// is set on the host kernel.
// layerCount is the expected number of layers from CheckImage, used for progress.
func pullImage(image string, layerCount int) error {
	progress.Substep("Pulling container image")
	if layerCount > 0 {
		progress.Substep(fmt.Sprintf("Pulling image: %d layers to download", layerCount))
	}

	podmanArgs := []string{"pull", image}
	name, args := runner.HostArgs("podman", podmanArgs)
	fmt.Fprintf(os.Stdout, "+ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
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

			// podman pull emits "Copying blob sha256:..." lines when not on a TTY.
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
		return fmt.Errorf("podman pull %s: %w", image, err)
	}
	progress.Substep("Image pulled successfully")
	return nil
}

// ImageCheck holds the result of a pre-flight image inspection.
type ImageCheck struct {
	NeedsPull  bool // true if the image is absent or stale in containers-storage
	LayerCount int  // number of layers in the remote image; 0 if unknown
	Offline    bool // true if the registry was unreachable but a local copy was found
}

// DefaultSkopeoInspect runs `skopeo inspect <args>` and returns stdout.
func DefaultSkopeoInspect(args ...string) ([]byte, error) {
	name, hargs := runner.HostArgs("skopeo", append([]string{"inspect"}, args...))
	fmt.Fprintf(os.Stdout, "+ %s %s\n", name, strings.Join(hargs, " "))
	return exec.Command(name, hargs...).Output()
}

// SkopeoInspectFn is the function used by CheckImage to call skopeo inspect.
// Replace in tests to avoid network calls.
var SkopeoInspectFn = DefaultSkopeoInspect

// CheckImage compares the remote and local (containers-storage) image digests
// to determine whether a pull is required. It also returns the remote layer count.
//
// If the remote registry is unreachable (offline), CheckImage falls back to
// checking local containers-storage: if the image is present locally it is used
// as-is (NeedsPull=false, Offline=true). This allows a full offline install when
// the image has been pre-pulled into podman storage.
func CheckImage(image string) ImageCheck {
	type manifest struct {
		Digest string   `json:"Digest"`
		Layers []string `json:"Layers"`
	}

	// 1. Fetch remote normalized manifest (resolves fat/multi-arch manifests).
	remoteOut, remoteErr := SkopeoInspectFn("docker://" + image)

	// 2. Fetch local digest from containers-storage.
	localOut, localErr := SkopeoInspectFn("containers-storage:" + image)

	// If offline (remote failed), fall back to the locally cached image.
	if remoteErr != nil {
		if localErr == nil {
			var local manifest
			if json.Unmarshal(localOut, &local) == nil {
				return ImageCheck{NeedsPull: false, LayerCount: len(local.Layers), Offline: true}
			}
		}
		// Not reachable and not cached locally; a pull attempt will follow (and fail).
		return ImageCheck{NeedsPull: true}
	}

	var remote manifest
	if err := json.Unmarshal(remoteOut, &remote); err != nil {
		return ImageCheck{NeedsPull: true}
	}

	// Image not present locally: pull needed.
	if localErr != nil {
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
		return "Deploying image"
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
