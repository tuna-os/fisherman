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

# Patch boot loader for debian-bootc (composefs) if needed
# composefs images expect GPT auto-discovery but fisherman creates type=linux partitions
# So we need to add root=/dev/vda3 kernel parameter to boot configuration
if [ "$IMAGE_NAME" = "debian-bootc" ] || [ "$IMAGE_NAME" = "debian-bootc-composefs" ]; then
  echo "=== Patching boot loader for debian-bootc (composefs) ==="
  
  FOUND_BOOT_CONFIG=0
  PATCHED=0
  MNTDIR="/tmp/bootcrew-grub-mnt-$$"
  mkdir -p "$MNTDIR"
  
  # Try each partition to find boot configuration
  for PARTITION in "$LOOPDEV"p2 "$LOOPDEV"p3 "$LOOPDEV"p1; do
    [ -b "$PARTITION" ] || continue
    
    if sudo mount "$PARTITION" "$MNTDIR" 2>/dev/null; then
      echo "✓ Mounted $PARTITION"
      
      # Check for systemd-boot/BLS format (/loader/entries)
      if [ -d "$MNTDIR/loader/entries" ]; then
        FOUND_BOOT_CONFIG=1
        echo "  ✓ Found /loader/entries (systemd-boot)"
        CONF_COUNT=0
        for conf in "$MNTDIR"/loader/entries/*.conf; do
          [ -f "$conf" ] || continue
          CONF_COUNT=$((CONF_COUNT + 1))
          NEEDS_ROOT=0
          NEEDS_SSH=0
          
          # Check if needs root= parameter
          if ! sudo grep -q "root=" "$conf"; then
            NEEDS_ROOT=1
          fi
          
          # Check if needs systemd.wants=ssh.service for composefs systems
          if ! sudo grep -q "systemd.wants=ssh" "$conf"; then
            NEEDS_SSH=1
          fi
          
          if [ "$NEEDS_ROOT" -eq 1 ] || [ "$NEEDS_SSH" -eq 1 ]; then
            echo "    Patching $(basename "$conf")..."
            [ "$NEEDS_ROOT" -eq 1 ] && sudo sed -i 's/^options /options root=\/dev\/vda3 /' "$conf"
            [ "$NEEDS_SSH" -eq 1 ] && sudo sed -i 's/^options /options systemd.wants=ssh.service /' "$conf"
            PATCHED=1
          fi
        done
        [ "$CONF_COUNT" -eq 0 ] && echo "    ⚠️  No .conf files found"
        [ "$CONF_COUNT" -gt 0 ] && [ "$PATCHED" -eq 0 ] && echo "    ✓ All entries already have root= and systemd parameters"
      fi
      
      # Check for GRUB format (/boot/grub2 or /boot/grub)
      if [ "$FOUND_BOOT_CONFIG" -eq 0 ]; then
        for GRUB_DIR in "$MNTDIR/boot/grub2" "$MNTDIR/boot/grub"; do
          if [ -f "$GRUB_DIR/grub.cfg" ]; then
            FOUND_BOOT_CONFIG=1
            echo "  ✓ Found GRUB config at $GRUB_DIR/grub.cfg"
            if ! sudo grep -q "root=/dev/vda3" "$GRUB_DIR/grub.cfg"; then
              echo "    Patching GRUB config..."
              sudo sed -i 's/^[[:space:]]*linux[[:space:]]/&root=\/dev\/vda3 /' "$GRUB_DIR/grub.cfg"
              PATCHED=1
            else
              echo "    ✓ root=/dev/vda3 already present"
            fi
            break
          fi
        done
      fi
      
      sudo umount "$MNTDIR" || true
      [ "$FOUND_BOOT_CONFIG" -eq 1 ] && break
    fi
  done
  
  if [ "$FOUND_BOOT_CONFIG" -eq 0 ]; then
    echo "⚠️  Boot configuration not found"
  elif [ "$PATCHED" -eq 1 ]; then
    echo "✓ Boot configuration patched successfully"
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
