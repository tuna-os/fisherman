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
LUKS_PASSPHRASE="${7:-}"  # optional; if set, run luks-unlock.py for Plymouth passphrase entry

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

# Helper function to add UEFI boot entry for systemd-boot images
# This is a workaround for systemd-boot not creating UEFI entries via efibootmgr
add_uefi_boot_entry_workaround() {
  local ovmf_vars="$1"
  
  # Check if efivar is available to manipulate EFI variables
  if ! command -v efibootmgr &>/dev/null; then
    echo "⚠️  efibootmgr not available - UEFI boot entry workaround skipped"
    return 1
  fi
  
  # For systemd-boot images, we would need to pre-create Boot0000 entry
  # This is complex as it requires understanding UEFI variable format
  # A proper implementation would require:
  # 1. Parsing UEFI boot entry format (GUID, device path, attributes)
  # 2. Writing to the OVMF_VARS.fd file's EFI variable storage
  # 3. Ensuring proper CRC32 checksums
  #
  # For now, this is documented as a possible workaround
  # Real fix: upstream images should pre-install GRUB for boot entry creation
  
  return 1
}

# Start QEMU with SSH port forwarding
echo "Starting QEMU..."
SERIAL_LOG="/tmp/bootcrew-serial-$$.log"
QEMU_MONITOR_SOCK="/tmp/qemu-monitor-$$.sock"
QEMU_STDOUT_LOG="/tmp/qemu-stdout-$$.log"

# Use a writable copy of OVMF_VARS.fd so UEFI variables can be initialized
OVMF_VARS_TEMP="/tmp/ovmf-vars-$$.fd"
cp "$OVMF_VARS" "$OVMF_VARS_TEMP"

# Attempt UEFI boot entry workaround for systemd-boot images
if [ "$IMAGE_NAME" = "debian-bootc" ] || [ "$IMAGE_NAME" = "debian-bootc-composefs" ] || \
   [ "$IMAGE_NAME" = "arch-bootc" ] || [ "$IMAGE_NAME" = "arch-bootc-composefs" ]; then
  echo "=== Attempting OVMF_VARS boot entry workaround for systemd-boot ==="
  if add_uefi_boot_entry_workaround "$OVMF_VARS_TEMP"; then
    echo "✓ Added UEFI boot entry to OVMF_VARS"
  else
    echo "⚠️  Could not add UEFI boot entry - will rely on BOOTX64.EFI fallback"
  fi
  echo ""
fi

# Pre-create serial log with proper permissions for sudo-run QEMU to write
sudo sh -c "rm -f \"$SERIAL_LOG\" 2>/dev/null; touch \"$SERIAL_LOG\" && chmod 666 \"$SERIAL_LOG\"" || true

# Trap to clean up temp files on all exits
trap "sudo rm -f '$OVMF_VARS_TEMP' '$QEMU_MONITOR_SOCK' '$QEMU_STDOUT_LOG' 2>/dev/null || true" EXIT

sudo timeout "$VM_TIMEOUT" "$QEMU_BIN" \
  -machine q35 \
  -enable-kvm \
  -cpu host \
  -m "$VM_MEMORY" \
  -device ahci,id=ahci0 \
  -drive file="$LOOPDEV",format=raw,if=none,id=disk0 \
  -device ide-hd,drive=disk0,bus=ahci0.0,bootindex=1 \
  -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
  -drive if=pflash,format=raw,file="$OVMF_VARS_TEMP" \
  -netdev user,id=net0,hostfwd=tcp:127.0.0.1:"$SSH_PORT"-:22 \
  -device virtio-net-pci,netdev=net0 \
  -monitor "unix:$QEMU_MONITOR_SOCK,server=on,wait=off" \
  -serial "file:$SERIAL_LOG" \
  -display none \
  -no-reboot >"$QEMU_STDOUT_LOG" 2>&1 &

QEMU_PID=$!
echo "QEMU PID: $QEMU_PID"
echo ""

LUKS_PID=""
if [ -n "$LUKS_PASSPHRASE" ]; then
  echo "=== LUKS mode: starting passphrase injector ==="
  # Run as sudo so it can connect to the root-owned QEMU monitor socket.
  sudo python3 "$SCRIPT_DIR/luks-unlock.py" qemu \
    "$QEMU_MONITOR_SOCK" "$LUKS_PASSPHRASE" "$SERIAL_LOG" &
  LUKS_PID=$!
  echo "luks-unlock.py PID: $LUKS_PID"
  echo ""
fi

# Wait for SSH to be ready (using password auth)
# For LUKS VMs, luks-unlock.py runs concurrently and injects the passphrase;
# SSH becomes available once the system has fully booted after unlock.
echo "Waiting for VM to boot and SSH to be ready (up to 240s)..."
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

# Collect luks-unlock.py exit code (if running)
LUKS_EXIT=0
if [ -n "$LUKS_PID" ]; then
  wait "$LUKS_PID" 2>/dev/null && LUKS_EXIT=0 || LUKS_EXIT=$?
  if [ "$LUKS_EXIT" -eq 2 ]; then
    echo "❌ luks-unlock.py: passphrase sent but emergency shell detected (LUKS boot failed)"
  elif [ "$LUKS_EXIT" -ne 0 ]; then
    echo "❌ luks-unlock.py exited with code $LUKS_EXIT (Plymouth prompt not detected)"
  else
    echo "✅ luks-unlock.py: passphrase injected successfully"
  fi
fi

if [ $SSH_READY -eq 0 ]; then
  echo "❌ SSH connection failed (timeout)"

  if [ -f "$SERIAL_LOG" ]; then
    echo ""
    echo "=== Serial Console Output (for debugging) ==="
    sudo cat "$SERIAL_LOG" || cat "$SERIAL_LOG" || true
    PERSISTENT_LOG="/tmp/bootcrew-serial-last.log"
    sudo cp "$SERIAL_LOG" "$PERSISTENT_LOG" 2>/dev/null || cp "$SERIAL_LOG" "$PERSISTENT_LOG" 2>/dev/null || true
    echo "(Full log saved to: $PERSISTENT_LOG)"
    sudo rm -f "$SERIAL_LOG" 2>/dev/null || rm -f "$SERIAL_LOG" 2>/dev/null || true
  fi

  if [ -f "$QEMU_STDOUT_LOG" ]; then
    echo ""
    echo "=== QEMU stdout/stderr ==="
    sudo cat "$QEMU_STDOUT_LOG" || cat "$QEMU_STDOUT_LOG" || true
  fi

  kill $QEMU_PID 2>/dev/null || true
  exit 1
fi

if [ "$LUKS_EXIT" -ne 0 ]; then
  echo "❌ LUKS test failed (luks-unlock.py exit code $LUKS_EXIT)"
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
