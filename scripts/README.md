# Bootcrew Test Scripts

This directory contains shell scripts that power the bootcrew E2E testing workflow. These scripts are called by the `justfile` recipes and can also be run directly.

## Scripts

### `boot-verify.sh`
Boots a QEMU VM with the installed disk and verifies system state via SSH.

**Usage:**
```bash
./boot-verify.sh [SSH_PORT] [SSH_KEY] [VM_TIMEOUT] [VM_MEMORY] [LOOPDEV]
```

**Defaults:**
- SSH_PORT: 2222
- SSH_KEY: /tmp/bootcrew-ssh/id_rsa
- VM_TIMEOUT: 300 seconds
- VM_MEMORY: 2G
- LOOPDEV: read from /tmp/bootcrew-loopdev.txt if not provided

**What it does:**
1. Starts QEMU with KVM acceleration
2. Forwards SSH port from host:2222 → guest:22
3. Polls SSH until connection succeeds (up to 60s)
4. Runs `bootc status` and captures JSON output
5. Runs `bootc upgrade --check`
6. Gracefully shuts down VM via SSH

**Example:**
```bash
./boot-verify.sh 2222 /tmp/id_rsa 300 2G /dev/loop0
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
Verifies that fisherman created a valid 3-partition layout with correct content.

**Usage:**
```bash
./verify-installation.sh LOOPDEV [COMPOSEFS]
```

**Defaults:**
- COMPOSEFS: false

**What it does:**
1. Checks for 3 labeled partitions (EFI-SYSTEM, boot, root)
2. Mounts boot partition and verifies layout
3. Mounts root partition and verifies:
   - Hostname configuration (composefs-native: `/etc/hostname`, ostree: deployment structure)
   - Filesystem structure
4. Unmounts both partitions
5. Exits 0 if all checks pass, 1 if any check fails

**Example:**
```bash
./verify-installation.sh /dev/loop0 false
./verify-installation.sh /dev/loop0 true  # composefs backend
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

## Environment Variables

- `SSH_PORT`: Port for QEMU SSH forwarding (default: 2222)
- `SSH_KEY`: Path to SSH private key (default: /tmp/bootcrew-ssh/id_rsa)
- `VM_TIMEOUT`: QEMU timeout in seconds (default: 300)
- `VM_MEMORY`: QEMU memory allocation (default: 2G)
- `LOOPDEV`: Loop device path (default: read from /tmp/bootcrew-loopdev.txt)

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
