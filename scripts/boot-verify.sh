#!/bin/bash
# Verify system boot via SSH and query bootc status
# Usage: ./boot-verify.sh [SSH_PORT] [SSH_KEY] [VM_TIMEOUT] [VM_MEMORY] [LOOPDEV] [IMAGE_NAME]
# Works with dynamic tool discovery for both CI and local environments

set -e

# Source tool discovery helpers
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./find-tools.sh
source "$SCRIPT_DIR/find-tools.sh"

SSH_PORT="${1:-2222}"
SSH_KEY="${2:-/tmp/bootcrew-ssh/id_rsa}"
VM_TIMEOUT="${3:-300}"
VM_MEMORY="${4:-2G}"
LOOPDEV="${5}"
IMAGE_NAME="${6}"

if [ -z "$LOOPDEV" ]; then
  LOOPDEV=$(cat /tmp/bootcrew-loopdev.txt 2>/dev/null)
  if [ -z "$LOOPDEV" ]; then
    echo "❌ LOOPDEV not provided and /tmp/bootcrew-loopdev.txt not found"
    exit 1
  fi
fi

if [ ! -f "$SSH_KEY" ]; then
  echo "❌ SSH key not found at $SSH_KEY"
  exit 1
fi

# Find required tools
QEMU_BIN=$(find_qemu) || {
  echo "❌ QEMU not found. Install qemu-kvm or qemu-system-x86"
  show_tool_info
  exit 1
}

OVMF_PATH=$(find_ovmf) || {
  echo "❌ OVMF firmware not found"
  show_tool_info
  exit 1
}

SSH_BIN=$(find_ssh) || {
  echo "❌ SSH client not found"
  show_tool_info
  exit 1
}

# Parse OVMF paths (returned as code:vars)
OVMF_CODE="${OVMF_PATH%:*}"
OVMF_VARS="${OVMF_PATH#*:}"

# Patch GRUB for debian-bootc (composefs) if needed
# composefs images expect GPT auto-discovery but fisherman creates type=linux partitions
# So we need to add root=/dev/vda3 kernel parameter
if [ "$IMAGE_NAME" = "debian-bootc" ] || [ "$IMAGE_NAME" = "debian-bootc-composefs" ]; then
  echo "=== Patching GRUB for debian-bootc (composefs) ==="
  MNTDIR="/tmp/bootcrew-grub-mnt-$$"
  mkdir -p "$MNTDIR"
  
  # Try to mount the root partition (vda3) to access GRUB config
  if sudo mount "$LOOPDEV"p3 "$MNTDIR" 2>/dev/null; then
    GRUB_CONF="$MNTDIR/boot/grub2/grub.cfg"
    if [ -f "$GRUB_CONF" ]; then
      # Check if root= parameter is already present
      if ! sudo grep -q "root=" "$GRUB_CONF"; then
        echo "Adding root=/dev/vda3 kernel parameter to GRUB config..."
        sudo sed -i 's/^[[:space:]]*linux[[:space:]]/&root=\/dev\/vda3 /' "$GRUB_CONF"
      fi
    fi
    sudo umount "$MNTDIR"
  fi
  rm -rf "$MNTDIR"
  echo ""
fi

echo "=== Booting VM for SSH verification ==="
echo "LOOPDEV: $LOOPDEV"
echo "SSH Port: $SSH_PORT"
echo "SSH Key: $SSH_KEY"
echo "QEMU: $QEMU_BIN"
echo "OVMF_CODE: $OVMF_CODE"
echo "OVMF_VARS: $OVMF_VARS"
echo ""

# Start QEMU with SSH port forwarding
echo "Starting QEMU..."
sudo timeout "$VM_TIMEOUT" "$QEMU_BIN" \
  -enable-kvm \
  -cpu host \
  -m "$VM_MEMORY" \
  -drive file="$LOOPDEV",format=raw,if=virtio \
  -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
  -drive if=pflash,format=raw,snapshot=on,file="$OVMF_VARS" \
  -netdev user,id=net0,hostfwd=tcp:127.0.0.1:"$SSH_PORT"-:22 \
  -device virtio-net-pci,netdev=net0 \
  -nographic \
  -no-reboot &

QEMU_PID=$!
echo "QEMU PID: $QEMU_PID"
echo ""

# Wait for SSH to be ready (using password auth)
echo "Waiting for VM to boot and SSH to be ready (up to 60s)..."
SSH_READY=0
for i in {1..60}; do
  sleep 2
  if sshpass -p "bootcrew-test" "$SSH_BIN" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
         -o ConnectTimeout=1 -o PubkeyAuthentication=no root@127.0.0.1 -p "$SSH_PORT" \
         "echo OK" 2>/dev/null; then
    echo "✅ SSH connection successful"
    SSH_READY=1
    break
  fi
  if [ $((i % 10)) -eq 0 ]; then
    echo "  Waiting... ($i/60)"
  fi
done

if [ $SSH_READY -eq 0 ]; then
  echo "❌ SSH connection failed (timeout)"
  kill $QEMU_PID 2>/dev/null || true
  exit 1
fi

echo ""
echo "=== System Information ==="
sshpass -p "bootcrew-test" "$SSH_BIN" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o PubkeyAuthentication=no root@127.0.0.1 -p "$SSH_PORT" "uname -a" || true

echo ""
echo "=== bootc status ==="
sshpass -p "bootcrew-test" "$SSH_BIN" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o PubkeyAuthentication=no root@127.0.0.1 -p "$SSH_PORT" "bootc status" 2>/dev/null || echo "⚠️  bootc not available"

echo ""
echo "=== bootc status (JSON) ==="
sshpass -p "bootcrew-test" "$SSH_BIN" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o PubkeyAuthentication=no root@127.0.0.1 -p "$SSH_PORT" "bootc status --json-pretty" 2>/dev/null || echo "⚠️  json output not available"

echo ""
echo "=== Checking for upgrade availability ==="
sshpass -p "bootcrew-test" "$SSH_BIN" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o PubkeyAuthentication=no root@127.0.0.1 -p "$SSH_PORT" "bootc upgrade --check" 2>/dev/null || echo "⚠️  upgrade check not available"

echo ""
echo "=== Shutting down VM ==="
sshpass -p "bootcrew-test" "$SSH_BIN" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o PubkeyAuthentication=no root@127.0.0.1 -p "$SSH_PORT" "shutdown -h now" 2>/dev/null || true

sleep 5
kill $QEMU_PID 2>/dev/null || true
wait $QEMU_PID 2>/dev/null || true

echo "✅ VM verification complete"
