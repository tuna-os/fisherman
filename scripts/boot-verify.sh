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
VM_TIMEOUT="${3:-600}"  # 10 minutes: network boot timeout + disk boot + systemd startup
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

# For systemd-boot images, UEFI variables need to be properly initialized
# Use a writable copy of OVMF_VARS.fd instead of the read-only template
# This allows OVMF firmware to set up boot entries correctly
if [ "$IMAGE_NAME" = "debian-bootc" ] || [ "$IMAGE_NAME" = "debian-bootc-composefs" ] || \
   [ "$IMAGE_NAME" = "arch-bootc" ] || [ "$IMAGE_NAME" = "arch-bootc-composefs" ] || \
   [ "$IMAGE_NAME" = "fedora-bootc" ] || [ "$IMAGE_NAME" = "fedora-bootc-composefs" ]; then
  
  echo "=== Systemd-boot image detected (debian-bootc/arch-bootc) ==="
  echo "Note: Using persistent UEFI variables for proper boot entry initialization"
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
SERIAL_LOG="/tmp/bootcrew-serial-$$.log"

# Use a writable copy of OVMF_VARS.fd so UEFI variables can be initialized
OVMF_VARS_TEMP="/tmp/ovmf-vars-$$.fd"
cp "$OVMF_VARS" "$OVMF_VARS_TEMP"

# Pre-create serial log with world-readable permissions
touch "$SERIAL_LOG"
chmod 666 "$SERIAL_LOG"

sudo timeout "$VM_TIMEOUT" "$QEMU_BIN" \
  -enable-kvm \
  -cpu host \
  -m "$VM_MEMORY" \
  -drive file="$LOOPDEV",format=raw,if=virtio \
  -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
  -drive if=pflash,format=raw,file="$OVMF_VARS_TEMP" \
  -boot order=d \
  -netdev user,id=net0,hostfwd=tcp:127.0.0.1:"$SSH_PORT"-:22 \
  -device virtio-net-pci,netdev=net0 \
  -chardev file,path="$SERIAL_LOG",id=char0 \
  -serial chardev:char0 \
  -nographic \
  -no-reboot >/dev/null 2>&1 &

QEMU_PID=$!
echo "QEMU PID: $QEMU_PID"
echo ""

# Trap to clean up temp files on exit
trap "rm -f '$OVMF_VARS_TEMP' '$SERIAL_LOG'" EXIT

# Wait for SSH to be ready (using password auth)
# For systemd-boot images, this may take longer due to network boot timeout
echo "Waiting for VM to boot and SSH to be ready (up to 120s)..."
SSH_READY=0
for i in {1..120}; do
  sleep 2
  if sshpass -p "bootcrew-test" "$SSH_BIN" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
         -o ConnectTimeout=1 -o PubkeyAuthentication=no root@127.0.0.1 -p "$SSH_PORT" \
         "echo OK" 2>/dev/null; then
    echo "✅ SSH connection successful"
    SSH_READY=1
    break
  fi
  if [ $((i % 10)) -eq 0 ]; then
    echo "  Waiting... ($i/120)"
  fi
done

if [ $SSH_READY -eq 0 ]; then
  echo "❌ SSH connection failed (timeout)"
  
  # Print serial log for debugging
  if [ -f "$SERIAL_LOG" ]; then
    echo ""
    echo "=== Serial Console Output (for debugging) ==="
    tail -150 "$SERIAL_LOG"
    rm -f "$SERIAL_LOG"
  fi
  
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
