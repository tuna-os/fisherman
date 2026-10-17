# Security Policy

## Reporting Vulnerabilities

If you discover a security vulnerability in fisherman, please report it responsibly by following GitHub's coordinated vulnerability disclosure process:

1. **Do not file a public issue.** Instead, use GitHub's [Security Advisory](https://github.com/tuna-os/fisherman/security/advisories) feature or email security@tuna-os.org.
2. **Provide details:** affected versions, reproduction steps, and the impact.
3. **Allow reasonable time for a fix:** we aim to release security patches within 7 days of confirmation.

All security vulnerabilities will be acknowledged and addressed before public disclosure. Contributors who discover and responsibly report vulnerabilities will be credited.

## Threat Model

### What fisherman is designed to protect against

fisherman assumes a **trusted host** and **trusted frontends**. It makes these guarantees:

- **Disk integrity:** A completed fisherman install creates a bitwise correct filesystem layout on the target disk, matching the partition layout, formatting, and bootloader expectations.
- **Encryption strength:** When LUKS encryption is requested, the encrypted root filesystem is generated and sealed correctly; the passphrase/TPM unlock flow is not weakened by fisherman.
- **Data isolation:** Multiple simultaneous installs on the same host do not corrupt each other's target disks or scratch directories.
- **Cleanup:** All temporary files and mount points are cleaned up after installation, even on error paths. A failed install leaves the target disk in a recoverable state.

### What fisherman does not protect against

- **Malicious frontends.** fisherman executes recipes provided by its caller without further validation of the caller's privileges or intent.
- **Compromised host.** If the host is already compromised before fisherman runs, fisherman cannot guarantee the security of the installed system. SELinux policy and other security controls are applied *during* installation but assume the bootc image itself is trusted.
- **Network-based attacks.** fisherman does not defend against MITM attacks on container image pulls; it relies on the underlying podman/skopeo security model.
- **Passive data recovery.** Deleted files may be recoverable from an unencrypted disk or a LUKS-encrypted disk if the key material is available.

## Security-Relevant Invariants

The following behaviors have security implications and must not be simplified or moved:

### Scratch Directory Isolation

- **Two-level cleanup:** Regular files, mounts, and LUKS mappings are cleaned up in dependency order (leaf to root). Post-removal paths (like the scratch dir itself) are removed only *after* all unmounts and LUKS closes complete. This prevents accidental overwrites or data leaks.
- **Private tempdir:** The scratch directory is created with `0700` permissions to prevent unprivileged users from reading partial installation state.
- **Location:** On live media, scratch lives under `/<target>/.fisherman-scratch` to respect constrained overlayfs/tmpfs; on normal hosts it is `/var/fisherman-tmp`. This separation ensures CI and recovery scenarios work correctly.

### SELinux Bypass Shim

- **Privilege isolation:** The SELinux bypass shim (generated in `BuildSelinuxBypassShim`) runs as root and is bind-mounted into the privileged bootc container. Its build path must be:
  - Private to root (no world-writable directories like `/tmp`)
  - Protected with `O_EXCL` and `O_NOFOLLOW` during creation
  - Located in the scratch dir (subject to proper cleanup)
  
  **See:** #161 (CVE details TBD) — a prior implementation wrote to `/tmp` with insufficient protection, allowing local privilege escalation.

### Partition Layout

- **GRUB compatibility:** GRUB cannot read modern XFS features; therefore:
  - GRUB images always use 3 partitions (EFI, ext4 `/boot`, root) even unencrypted
  - `/boot` is always ext4 on its own partition
  - This is not a performance optimization; it is a hard requirement for GRUB to boot the system
  
  **See:** README.md partition-layout table.

- **Systemd-boot simplification:** systemd-boot can read kernel+initrd directly from FAT32, so dakota images use 2 partitions (EFI, root). The ESP must still be at least 2 GiB for kernel staging.

### Recipe Validation Before Disk Operations

- **Fail-fast:** `fisherman validate` is called before any disk operations. All recipe fields that could damage a disk (hostname, username, filesystem choice, partition boundaries) are validated synchronously with stable error codes.
- **Error codes:** See `docs/VALIDATE.md` for the full reference. Integrators should map these to user-facing messages before allowing installation.
- **JSON output:** `fisherman validate --json` returns structured error objects. Frontends must not skip validation errors; they are the primary protection against misconfigured installs.

### Image Source Verification

- **Empty image rule:** On non-live media, an empty `image` field is rejected (exit 1). This prevents accidentally deploying the host system instead of the intended image.
- **Live detection:** `fisherman probe --json` reports whether the host is running from live media via the `live.is_live` field. Frontends should use this to decide whether to accept an empty image.

## Post-Install Verification

After fisherman completes successfully (exit 0), integrators and users should verify:

1. **Partitions exist and are readable:**
   ```bash
   lsblk /dev/X  # Check layout matches recipe (3 for GRUB, 2 for systemd-boot)
   ```

2. **Bootloader is installed (use `bootctl` or `grub-editenv`):**
   ```bash
   bootctl status     # For systemd-boot
   grub-editenv list  # For GRUB
   ```

3. **For encrypted installs, test the unlock flow:**
   ```bash
   # Reboot the system and verify Plymouth prompt + LUKS unlock works
   ```

4. **Verify the deployed image:**
   ```bash
   bootc status   # After first boot
   ```

## Privilege Boundaries

fisherman is invoked through privilege-escalation frontends:

| Frontend | Escalation | stdin/recipe | Notes |
|---|---|---|---|
| sudo | sudo (Linux escalation) | recipe on stdin or file | fisherman validates `sudo` presence in environment |
| pkexec | PolicyKit | recipe on stdin or file | requires PolicyKit policy allowing installer |
| flatpak-spawn --host | Flatpak sandbox escape | recipe on stdin | used by GNOME/KDE installers running sandboxed |

**Key boundary:** fisherman does not authenticate the frontend or its recipe. The frontend is responsible for:
- Collecting user input securely
- Prompting for confirmation before calling fisherman
- Verifying the recipe matches user intent (especially the disk and image)

## Dependencies and Supply Chain

fisherman depends on:

- **Go runtime** (v1.22+): vendored deps in `go.mod`, audited via `go mod tidy` and CI lint
- **bootc** (bootable container CLI): called via `podman run --privileged`; assumes bootc image is from a trusted registry
- **podman**: container runtime; inherits podman's security posture
- **skopeo**: image copy tool; used for offline image store discovery
- **System utilities:** cryptsetup, mkfs, mount, etc.

All of these are assumed to be from a trusted source. If any are compromised, so is the resulting install.

## Change Log

- **2026-10-17** (initial draft): Documented threat model, invariants, and post-install verification.
