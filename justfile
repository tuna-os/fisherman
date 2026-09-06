# Bootcrew local test recipes
# Usage: just bootcrew-vm IMAGE_NAME [IMAGE_REPO]
# Example: just bootcrew-vm debian-bootc ghcr.io/bootcrew
# Example: just bootcrew-vm centos-bootc quay.io/centos-bootc
#
# CI-specific recipes: bootcrew-ci-matrix, bootcrew-ci-test
# These read from tests/bootcrew-matrix.yaml and run the full E2E flow

set shell := ["bash", "-c"]
set dotenv-load := true

# Default values
DISK_SIZE := "120G"
VM_TIMEOUT := "300"
SSH_PORT := "2222"
VM_MEMORY := "2G"

# CI-specific: where recipes store temporary data
CI_ARTIFACTS := "/tmp"

# Prepare SSH keys for testing
setup-ssh-keys:
  #!/bin/bash
  set -e
  mkdir -p /tmp/bootcrew-ssh
  if [ ! -f /tmp/bootcrew-ssh/id_rsa ]; then
    echo "Generating SSH key..."
    ssh-keygen -t rsa -b 2048 -f /tmp/bootcrew-ssh/id_rsa -N "" -C "bootcrew-test"
    chmod 600 /tmp/bootcrew-ssh/id_rsa
  fi
  echo "✅ SSH keys ready at /tmp/bootcrew-ssh/"
  cat /tmp/bootcrew-ssh/id_rsa.pub

# Prepare SSH-enabled container image (using podman exec)
# SSH-enabled images are pre-built and pushed to GHCR
# This recipe is kept for local testing if needed
build-ssh-enabled-image IMAGE:
  #!/bin/bash
  set -e
  
  echo "Building SSH-enabled image locally for testing..."
  echo "Base image: {{ IMAGE }}"
  
  # Build temporary local image for local testing
  TEMP_TAG="localhost/fisherman-ssh-test:latest"
  podman build \
    --build-arg BASE_IMAGE="{{ IMAGE }}" \
    --tag "$TEMP_TAG" \
    -f scripts/Containerfile.ssh-enable .
  
  echo "✅ Built SSH-enabled image locally: $TEMP_TAG"
  echo "For CI, SSH-enabled images are hosted at ghcr.io/tuna-os/fisherman/<image>:ssh-enabled"

# Build debian-bootc with SSH pre-installed (Containerfile approach)
build-debian-bootc-ssh:
  #!/bin/bash
  set -e
  echo "Building debian-bootc-ssh image..."
  podman build -f Containerfile.debian-ssh -t ghcr.io/bootcrew/debian-bootc:ci-ssh-enabled .
  echo "✅ Built: ghcr.io/bootcrew/debian-bootc:ci-ssh-enabled"

# Build fisherman binary
build:
  #!/bin/bash
  set -e
  echo "Building fisherman..."
  cd fisherman && go build -o /tmp/fisherman ./cmd/fisherman/
  echo "✅ Built: /tmp/fisherman"

# Create a loop device for testing
setup-loop DISK_FILE:
  #!/bin/bash
  set -e
  DISK_FILE="{{ DISK_FILE }}"
  echo "Creating loop device at $DISK_FILE..."
  
  # Create sparse file
  truncate -s {{ DISK_SIZE }} "$DISK_FILE"
  
  # Create loop device
  LOOPDEV=$(sudo losetup --find --show "$DISK_FILE")
  echo "$LOOPDEV" > /tmp/bootcrew-loopdev.txt
  echo "✅ Loop device: $LOOPDEV"

# Clean up loop device
cleanup-loop:
  #!/bin/bash
  if [ -f /tmp/bootcrew-loopdev.txt ]; then
    LOOPDEV=$(cat /tmp/bootcrew-loopdev.txt)
    echo "Cleaning up $LOOPDEV..."
    sudo losetup -d "$LOOPDEV" || true
    rm -f /tmp/bootcrew-loopdev.txt
  fi

# Generate fisherman recipe for installation
generate-recipe IMAGE FILESYSTEM COMPOSEFS BOOTLOADER="":
  #!/bin/bash
  set -e
  IMAGE="{{ IMAGE }}"
  FILESYSTEM="{{ FILESYSTEM }}"
  COMPOSEFS="{{ COMPOSEFS }}"
  BOOTLOADER="{{ BOOTLOADER }}"
  LOOPDEV=$(cat /tmp/bootcrew-loopdev.txt)
  
  BOOTLOADER_JSON=""
  if [ -n "$BOOTLOADER" ]; then
    BOOTLOADER_JSON=", \"bootloader\": \"$BOOTLOADER\""
  fi
  
  cat > /tmp/bootcrew-recipe.json << EOF
  {
    "disk": "$LOOPDEV",
    "filesystem": "$FILESYSTEM",
    "composeFsBackend": $COMPOSEFS,
    "unifiedStorage": false,
    "selinuxDisabled": false,
    "encryption": {"type": "none"},
    "image": "$IMAGE",
    "hostname": "bootcrew-test",
    "flatpaks": []$BOOTLOADER_JSON
  }
  EOF
  
  echo "✅ Recipe: /tmp/bootcrew-recipe.json"
  cat /tmp/bootcrew-recipe.json

# Run fisherman installation
install:
  #!/bin/bash
  set -e
  if [ ! -f /tmp/fisherman ]; then
    echo "❌ fisherman binary not found. Run 'just build' first"
    exit 1
  fi
  if [ ! -f /tmp/bootcrew-recipe.json ]; then
    echo "❌ recipe not found. Run 'just generate-recipe' first"
    exit 1
  fi
  
  echo "Running fisherman installation..."
  sudo /tmp/fisherman /tmp/bootcrew-recipe.json
  echo "✅ Installation complete"

# Boot VM and verify with SSH
boot-verify LOOPDEV="" IMAGE_NAME="" LUKS_PASSPHRASE="":
  #!/bin/bash
  set -e
  
  if [ -z "{{ LOOPDEV }}" ]; then
    LOOPDEV=$(cat /tmp/bootcrew-loopdev.txt 2>/dev/null || true)
  else
    LOOPDEV="{{ LOOPDEV }}"
  fi
  
  if [ -z "$LOOPDEV" ]; then
    echo "ERROR: LOOPDEV not provided and not found in /tmp/bootcrew-loopdev.txt"
    exit 1
  fi
  
  IMAGE_NAME="{{ IMAGE_NAME }}"
  bash scripts/boot-verify.sh {{ SSH_PORT }} /tmp/bootcrew-ssh/id_rsa {{ VM_TIMEOUT }} {{ VM_MEMORY }} "$LOOPDEV" "$IMAGE_NAME" "{{ LUKS_PASSPHRASE }}"

# Full bootcrew test (debian-bootc by default)
bootcrew-vm IMAGE="quay.io/centos-bootc/centos-bootc:c10s" FILESYSTEM="xfs" COMPOSEFS="false":
  #!/bin/bash
  set -e
  echo "==================================="
  echo "Bootcrew VM Test"
  echo "Image: {{ IMAGE }}"
  echo "Filesystem: {{ FILESYSTEM }}"
  echo "ComposFS Backend: {{ COMPOSEFS }}"
  echo "==================================="
  echo ""
  
  DISK_FILE="/tmp/bootcrew-test-disk.img"
  
  # Setup
  just setup-ssh-keys
  just prepare-ssh-image "{{ IMAGE }}"
  just build
  
  # Create SSH-enabled image variant (strip tag and add :ci-ssh-enabled)
  SSH_IMAGE=$(echo "{{ IMAGE }}" | sed 's/:.*/:ci-ssh-enabled/')
  
  just setup-loop "$DISK_FILE"
  just generate-recipe "$SSH_IMAGE" "{{ FILESYSTEM }}" "{{ COMPOSEFS }}"
  
  # Install
  just install

  # Grant the test VM access with the ephemeral key created above.
  bash scripts/enable-ssh-installed.sh "$LOOPDEV" "{{ COMPOSEFS }}" /tmp/bootcrew-ssh/id_rsa.pub
  
  # Boot and verify
  just boot-verify
  
  # Cleanup
  echo ""
  echo "=== Cleanup ==="
  just cleanup-loop
  rm -f "$DISK_FILE"
  
  echo "✅ Bootcrew test complete!"

# ========================================
# CI-specific recipes (GitHub Actions)
# ========================================

# Validate that E2E checks catch their target bugs (no real disk/VM required).
# Tests the installer-Flatpak absence check (PR #1) and the efibootmgr UEFI
# entry check (PR #2) against mock filesystem/output states.
test-checks:
  #!/bin/bash
  bash tests/check-validation.sh

# Verify installation partitions and basic structure
verify-installation LOOPDEV COMPOSEFS LUKS_PASSPHRASE="":
  #!/bin/bash
  bash scripts/verify-installation.sh "{{ LOOPDEV }}" "{{ COMPOSEFS }}" "{{ LUKS_PASSPHRASE }}"

# Verify bootc status on running VM (offline check)
verify-bootc-offline LOOPDEV COMPOSEFS:
  #!/bin/bash
  set -e
  LOOPDEV="{{ LOOPDEV }}"
  COMPOSEFS="{{ COMPOSEFS }}"
  
  echo "=== Offline bootc verification ==="
  
  ROOT_PART="${LOOPDEV}p3"
  ROOT_DIR=$(mktemp -d)
  
  sudo mount "$ROOT_PART" "$ROOT_DIR" || {
    echo "WARNING: Could not mount root for bootc verification"
    rmdir "$ROOT_DIR" 2>/dev/null || true
    exit 0
  }
  
  if [ -f "$ROOT_DIR/usr/bin/bootc" ] || [ -f "$ROOT_DIR/bin/bootc" ]; then
    echo "✅ bootc binary found at sysroot"
  else
    if [ "$COMPOSEFS" = "true" ]; then
      echo "⚠️  bootc not found at sysroot (composefs-native, may be in tree)"
    else
      echo "⚠️  bootc not found at sysroot (offline check only, will verify during boot)"
    fi
  fi
  
  sudo umount "$ROOT_DIR" || true
  rmdir "$ROOT_DIR" || true
  echo "✅ offline filesystem verification passed"

# Run a single bootcrew test from CI matrix entry
# Used by: just bootcrew-ci-test '{"name":"debian-bootc","image":"...",...}'
bootcrew-ci-test IMAGE_JSON:
  #!/bin/bash
  set -e
  
  IMAGE_JSON='{{ IMAGE_JSON }}'
  IMAGE=$(echo "$IMAGE_JSON" | jq -r '.image')
  FILESYSTEM=$(echo "$IMAGE_JSON" | jq -r '.filesystem // "xfs"')
  COMPOSEFS=$(echo "$IMAGE_JSON" | jq -r '.composefs_backend // false')
  UNIFIED=$(echo "$IMAGE_JSON" | jq -r '.unified_storage // false')
  SELINUX=$(echo "$IMAGE_JSON" | jq -r '.selinux_disabled // false')
  IMAGE_NAME=$(echo "$IMAGE_JSON" | jq -r '.name')
  LUKS=$(echo "$IMAGE_JSON" | jq -r '.luks // false')
  LUKS_PASSPHRASE=$(echo "$IMAGE_JSON" | jq -r '.luks_passphrase // ""')
  SSH_NAME=$(echo "$IMAGE_JSON" | jq -r '.ssh_image_name // .name')
  VM_TIMEOUT=$(echo "$IMAGE_JSON" | jq -r '.vm_timeout // 600')
  
  echo "=========================================="
  echo "Bootcrew CI Test: $IMAGE_NAME"
  echo "Image: $IMAGE"
  echo "Filesystem: $FILESYSTEM"
  echo "ComposFS Backend: $COMPOSEFS"
  echo "LUKS: $LUKS"
  echo "=========================================="
  echo ""
  
  DISK_FILE="{{ CI_ARTIFACTS }}/bootcrew-${IMAGE_NAME}-disk.img"
  
  # SSH-enabled images are pre-built and pushed to: ghcr.io/tuna-os/fisherman/<image>:ssh-enabled
  # LUKS variants reuse the base image's ssh-enabled tag via ssh_image_name.
  SSH_IMAGE="ghcr.io/tuna-os/fisherman/${SSH_NAME}:ssh-enabled"
  
  echo "Using pre-built SSH-enabled image: $SSH_IMAGE"
  
  # Build and create disk
  just setup-loop "$DISK_FILE"
  LOOPDEV=$(cat /tmp/bootcrew-loopdev.txt)
  
  # Determine bootloader from matrix entry (preferred) or fall back to name-based detection.
  BOOTLOADER_TYPE=$(echo "$IMAGE_JSON" | jq -r '.bootloader // ""')
  if [ -z "$BOOTLOADER_TYPE" ]; then
    case "$IMAGE_NAME" in
      debian-bootc*|arch-bootc*)
        BOOTLOADER_TYPE="systemd"
        ;;
    esac
  fi
  BOOTLOADER=""
  if [ -n "$BOOTLOADER_TYPE" ]; then
    BOOTLOADER="\"bootloader\": \"$BOOTLOADER_TYPE\","
  fi
  
  # Build encryption block.
  if [ "$LUKS" = "true" ]; then
    ENCRYPTION="{\"type\": \"luks-passphrase\", \"passphrase\": \"$LUKS_PASSPHRASE\"}"
  else
    ENCRYPTION='{"type": "none"}'
  fi
  
  cat > {{ CI_ARTIFACTS }}/recipe.json << EOF
  {
    "disk": "$LOOPDEV",
    "filesystem": "$FILESYSTEM",
    "composeFsBackend": $COMPOSEFS,
    "unifiedStorage": $UNIFIED,
    "selinuxDisabled": $SELINUX,
    "encryption": $ENCRYPTION,
    "image": "$SSH_IMAGE",
    "hostname": "ci-test",
    $BOOTLOADER
    "flatpaks": []
  }
  EOF
  
  # Install
  if [ ! -f /tmp/fisherman ]; then
    just build
  fi
  
  echo "Installing system..."
  sudo /tmp/fisherman {{ CI_ARTIFACTS }}/recipe.json

  # The registry image has password authentication disabled. Inject this
  # run's public key into the installed target for the localhost-only VM test
  # (opens the LUKS container if a passphrase is set).
  bash scripts/enable-ssh-installed.sh "$LOOPDEV" "$COMPOSEFS" /tmp/bootcrew-ssh/id_rsa.pub "$LUKS_PASSPHRASE"
  
  # Verify installation (opens LUKS container if passphrase is set).
  just verify-installation "$LOOPDEV" "$COMPOSEFS" "$LUKS_PASSPHRASE"
  
  # Patch BLS entries to add console=ttyS0 so that serial output is visible
  # in CI logs and (for LUKS) luks-unlock.py can detect the Plymouth prompt.
  #
  # Also boot the guest with SELinux permissive (enforcing=0). The harness
  # writes root's authorized_keys and an sshd_config.d drop-in into the
  # installed disk from this Ubuntu runner, which has no SELinux policy, so
  # those files land unlabeled. On an enforcing guest (centos-bootc) sshd_t
  # cannot read an unlabeled file, sshd fails to start, and every SSH probe
  # is reset before the banner (kex_exchange_identification: Connection
  # reset by peer) — while yellowfin, whose image already carries
  # selinux=0 in its kernel args, boots and accepts the same key. Permissive
  # keeps the labeling gap visible as AVC denials in the journal instead of
  # silently failing the boot verification; a real fix is labeling the files
  # (enable-ssh-installed.sh tries via setfattr) or relabeling on first boot.
  echo ""
  echo "=== Patching BLS entries for serial console ==="
  patch_bls_console() {
    local part="$1" label="$2"
    local MNT
    MNT=$(mktemp -d)
    sudo mount "$part" "$MNT" 2>/dev/null || { rmdir "$MNT" 2>/dev/null; return; }
    local patched=0
    for conf in "$MNT"/loader/entries/*.conf; do
      [ -f "$conf" ] || continue
      if ! sudo grep -q "console=ttyS0" "$conf"; then
        sudo sed -i 's/^options /options console=ttyS0,115200 console=tty0 /' "$conf"
        patched=1
        echo "  Patched ($label): $(basename "$conf")"
      fi
      if ! sudo grep -Eq "selinux=0|enforcing=0" "$conf"; then
        sudo sed -i 's/^options /options enforcing=0 /' "$conf"
        patched=1
        echo "  Patched ($label, enforcing=0): $(basename "$conf")"
      fi
      [ "$patched" -eq 1 ] && sudo grep "^options" "$conf"
    done
    [ "$patched" -eq 0 ] && echo "  No BLS entries on $label (or already patched)"
    sudo umount "$MNT"
    rmdir "$MNT"
  }
  patch_bls_console "${LOOPDEV}p1" "EFI"
  # Only attempt /boot if it's not LUKS-encrypted (it never is, but be safe).
  BOOT_TYPE=$(sudo blkid -s TYPE -o value "${LOOPDEV}p2" 2>/dev/null || true)
  if [ "$BOOT_TYPE" != "crypto_LUKS" ]; then
    patch_bls_console "${LOOPDEV}p2" "boot"
  fi
  echo "✅ BLS console patching done"
  
  # Boot VM and verify bootc status (pass LUKS passphrase for unlock).
  echo ""
  echo "=== Booting VM for bootc status verification ==="
  bash scripts/boot-verify.sh "2222" "/tmp/bootcrew-ssh/id_rsa" "$VM_TIMEOUT" "2G" "$LOOPDEV" "$IMAGE_NAME" "$LUKS_PASSPHRASE"
  
  # Cleanup
  echo ""
  echo "=== Cleanup ==="
  just cleanup-loop
  rm -f "$DISK_FILE"
  
  echo "✅ Test complete: $IMAGE_NAME"

# Install just on CI runner
ci-install-tools:
  #!/bin/bash
  set -e
  echo "Installing tools..."
  sudo apt-get update -qq
  sudo apt-get install -y podman xfsprogs btrfs-progs cryptsetup-bin ostree qemu-system-x86 ovmf flatpak jq openssh-client just yq socat python3
  echo "✅ Tools installed"

# Show help
@help:
  echo "Bootcrew test recipes"
  echo ""
  echo "🚀 Quick start:"
  echo "  just bootcrew-vm                                    # Full test (centos-bootc, ostree)"
  echo "  just bootcrew-vm quay.io/debian-bootc/debian-bootc:latest xfs true"
  echo ""
  echo "🔧 Manual steps:"
  echo "  just setup-ssh-keys                                 # Generate SSH keys"
  echo "  just prepare-ssh-image <IMAGE>                      # Add sshd to image"
  echo "  just build                                          # Build fisherman"
  echo "  just setup-loop /tmp/disk.img                       # Create loop device"
  echo "  just generate-recipe <IMAGE> xfs false              # Generate recipe"
  echo "  just install                                        # Run fisherman installation"
  echo "  just boot-verify                                    # Boot VM and verify bootc"
  echo "  just cleanup-loop                                   # Cleanup"
  echo ""
  echo "🤖 CI recipes (GitHub Actions):"
  echo "  just ci-install-tools                               # Install dependencies"
  echo "  just bootcrew-ci-test '{...}'                       # Single matrix entry"
