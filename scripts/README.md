# Bootcrew Test Scripts

This directory contains shell scripts that power the bootcrew E2E testing workflow. These scripts are called by the `justfile` recipes and can also be run directly.

## Scripts

### `boot-verify.sh`
Boots a QEMU VM with the installed disk and verifies system state via SSH.

**Usage:**
```bash
./boot-verify.sh [SSH_PORT] [SSH_KEY] [VM_TIMEOUT] [VM_MEMORY] [LOOPDEV] [IMAGE_NAME] [LUKS_PASSPHRASE]
```

**Defaults:**
- SSH_PORT: 2222
- SSH_KEY: /tmp/bootcrew-ssh/id_rsa
- VM_TIMEOUT: 600 seconds
- VM_MEMORY: 2G
- LOOPDEV: read from /tmp/bootcrew-loopdev.txt if not provided
- IMAGE_NAME: empty; pass the matrix image name when image-specific boot handling is needed
- LUKS_PASSPHRASE: empty; when set, injects the passphrase at the Plymouth prompt

**What it does:**
1. Starts QEMU with KVM acceleration
2. Forwards SSH port from host:2222 → guest:22
3. Optionally injects a LUKS passphrase through the QEMU monitor
4. Polls SSH until connection succeeds
5. Runs `bootc status` and captures JSON output
6. Runs `bootc upgrade --check`
7. Gracefully shuts down VM via SSH

**Example:**
```bash
./boot-verify.sh 2222 /tmp/id_rsa 600 2G /dev/loop0 dakota hunter2
```

### `prepare-ssh-image.sh`
Takes a container image and adds sshd + SSH public key authentication.

**Usage:**
```bash
./prepare-ssh-image.sh IMAGE [SSH_PUBKEY_FILE]
```

**Defaults:**
- SSH_PUBKEY_FILE: /tmp/bootcrew-ssh/id_rsa.pub

**What it does:**
1. Attempts to pull pre-built SSH image (tag: `:ci-ssh-enabled`)
2. If not found, creates a new image:
   - Pulls base image
   - Auto-detects package manager (apt-get/dnf/zypper/pacman)
   - Installs openssh-server
   - Generates SSH host keys
   - Enables sshd
   - Injects public key to `/root/.ssh/authorized_keys`
   - Commits as `:ci-ssh-enabled` image
3. Outputs `SSH_IMAGE=<image>` for use in CI

**Example:**
```bash
./prepare-ssh-image.sh quay.io/centos-bootc/centos-bootc:c10s /tmp/ssh/id_rsa.pub
```

### `verify-installation.sh`
Verifies that fisherman created a valid systemd-boot or GRUB partition layout with the expected content.

**Usage:**
```bash
./verify-installation.sh LOOPDEV [COMPOSEFS] [LUKS_PASSPHRASE]
```

**Defaults:**
- COMPOSEFS: false
- LUKS_PASSPHRASE: empty; when set and the root partition is LUKS, opens it for verification

**What it does:**
1. Accepts either a 2-partition systemd-boot layout (EFI, root) or a 3-partition GRUB layout (EFI, boot, root)
2. Verifies the fallback EFI executable and reports discovered boot entries
3. Opens an encrypted root partition when a passphrase is supplied
4. Mounts the boot and root partitions and verifies:
   - Hostname configuration (composefs-native: `/etc/hostname`, ostree: deployment structure)
   - Filesystem structure
   - Installer Flatpak removal
5. Unmounts partitions and closes any LUKS mapping
6. Exits 0 if all checks pass, 1 if any check fails

**Example:**
```bash
./verify-installation.sh /dev/loop0 false
./verify-installation.sh /dev/loop0 true  # composefs backend
./verify-installation.sh /dev/loop0 false hunter2  # encrypted root
```

## Integration with justfile

All scripts are called from `justfile` recipes. Most recipes should use the `just` command rather than calling scripts directly:

```bash
# Use just recipes (simpler)
just prepare-ssh-image quay.io/centos-bootc/centos-bootc:c10s
just boot-verify
just verify-installation /dev/loop0 false

# Or call scripts directly (for advanced usage)
bash scripts/prepare-ssh-image.sh quay.io/centos-bootc/centos-bootc:c10s
bash scripts/boot-verify.sh
bash scripts/verify-installation.sh /dev/loop0 false
```

## Boot verification inputs

The `just boot-verify` recipe supplies these positional script inputs:

- `SSH_PORT`: Port for QEMU SSH forwarding (default: 2222)
- `SSH_KEY`: Path to SSH private key (default: `/tmp/bootcrew-ssh/id_rsa`)
- `VM_TIMEOUT`: QEMU timeout in seconds (script default: 600; `just` recipe default: 300)
- `VM_MEMORY`: QEMU memory allocation (default: 2G)
- `LOOPDEV`: Loop device path (default: read from `/tmp/bootcrew-loopdev.txt`)

The script also reads these environment variables:

- `ARTIFACTS_DIR`: Directory for QEMU serial and stdout logs (default: `/tmp/bootcrew-artifacts`)
- `BOOTCREW_KEEP_VM`: Set to `1` to leave a failed VM and monitor socket running for local debugging

## CI Usage

The GitHub Actions workflow calls scripts via `just` recipes:

```yaml
- name: Setup SSH
  run: just setup-ssh-keys

- name: Prepare image
  run: just prepare-ssh-image "quay.io/centos-bootc/centos-bootc:c10s"

- name: Boot and verify
  run: just boot-verify
```

## Troubleshooting

### SSH connection timeout
- Increase `VM_TIMEOUT` (e.g., 600s for slower runners)
- Check QEMU is running: `ps aux | grep qemu-system`
- Check SSH key has correct permissions: `chmod 600 <key>`

### Installation verification fails
- Check disk space: `df -h /`
- Verify loop device: `losetup -a`
- Check partitions: `sudo lsblk /dev/loop0`

### Image preparation fails
- Ensure podman is running: `podman version`
- Check internet connectivity for image pulls
- Verify target image exists: `podman pull <image>`

## Development Notes

- All scripts use `set -e` to fail fast on errors
- Use `sudo` for privileged operations (lsblk, mount, losetup, podman with sudo)
- SSH operations use key-based auth (no password)
- QEMU runs in `-nographic` mode for CI compatibility
- Scripts are idempotent where possible (e.g., re-running doesn't duplicate work)
