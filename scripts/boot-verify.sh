#!/bin/bash
# Verify system boot via SSH and query bootc status
# Usage: ./boot-verify.sh [SSH_PORT] [SSH_KEY] [VM_TIMEOUT]

set -e

SSH_PORT="${1:-2222}"
SSH_KEY="${2:-/tmp/bootcrew-ssh/id_rsa}"
VM_TIMEOUT="${3:-300}"
VM_MEMORY="${4:-2G}"
LOOPDEV="${5}"

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

# Find OVMF firmware
OVMF_CODE=$(ls /usr/share/OVMF/OVMF_CODE{_4M,}.fd 2>/dev/null | head -1)
OVMF_SUFFIX=$(basename "$OVMF_CODE" | sed 's/OVMF_CODE//;s/\.fd//')
OVMF_VARS="/usr/share/OVMF/OVMF_VARS${OVMF_SUFFIX}.fd"

if [ -z "$OVMF_CODE" ] || [ ! -f "$OVMF_CODE" ]; then
  echo "❌ OVMF_CODE not found at /usr/share/OVMF/"
  exit 1
fi
if [ ! -f "$OVMF_VARS" ]; then
  echo "❌ OVMF_VARS not found"
  exit 1
fi

echo "=== Booting VM for SSH verification ==="
echo "LOOPDEV: $LOOPDEV"
echo "SSH Port: $SSH_PORT"
echo "SSH Key: $SSH_KEY"
echo "OVMF_CODE: $OVMF_CODE"
echo ""

# Start QEMU with SSH port forwarding
echo "Starting QEMU..."
sudo timeout "$VM_TIMEOUT" qemu-system-x86_64 \
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

# Wait for SSH to be ready
echo "Waiting for VM to boot and SSH to be ready (up to 60s)..."
SSH_READY=0
for i in {1..60}; do
  sleep 2
  if ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
         -o ConnectTimeout=1 -i "$SSH_KEY" root@127.0.0.1 -p "$SSH_PORT" \
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
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i "$SSH_KEY" root@127.0.0.1 -p "$SSH_PORT" "uname -a" || true

echo ""
echo "=== bootc status ==="
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i "$SSH_KEY" root@127.0.0.1 -p "$SSH_PORT" "bootc status" 2>/dev/null || echo "⚠️  bootc not available"

echo ""
echo "=== bootc status (JSON) ==="
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i "$SSH_KEY" root@127.0.0.1 -p "$SSH_PORT" "bootc status --json-pretty" 2>/dev/null || echo "⚠️  json output not available"

echo ""
echo "=== Checking for upgrade availability ==="
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i "$SSH_KEY" root@127.0.0.1 -p "$SSH_PORT" "bootc upgrade --check" 2>/dev/null || echo "⚠️  upgrade check not available"

echo ""
echo "=== Shutting down VM ==="
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i "$SSH_KEY" root@127.0.0.1 -p "$SSH_PORT" "shutdown -h now" 2>/dev/null || true

sleep 5
kill $QEMU_PID 2>/dev/null || true
wait $QEMU_PID 2>/dev/null || true

echo "✅ VM verification complete"
