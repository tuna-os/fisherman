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

# Artifact collection.
#
# ARTIFACTS_DIR (env, optional): directory where the serial log, qemu
#   stdout/stderr, and any post-mortem captures are persisted. Defaults to
#   /tmp/bootcrew-artifacts. The directory is created if it doesn't exist.
#   CI should point this at $GITHUB_WORKSPACE/artifacts so the upload step
#   can grab everything.
#
# BOOTCREW_KEEP_VM (env, optional): when set to "1" and the SSH probe
#   fails, the QEMU process and serial log are left running so a maintainer
#   can attach (e.g. via the QEMU monitor socket) and poke the guest.
#   Useful for local debugging. Always-off in CI.
ARTIFACTS_DIR="${ARTIFACTS_DIR:-/tmp/bootcrew-artifacts}"
mkdir -p "$ARTIFACTS_DIR" 2>/dev/null || true
RUN_TAG="${IMAGE_NAME:-vm}-$$"

# Start QEMU with SSH port forwarding
echo "Starting QEMU..."
SERIAL_LOG="$ARTIFACTS_DIR/serial-$RUN_TAG.log"
QEMU_MONITOR_SOCK="/tmp/qemu-monitor-$$.sock"
QEMU_STDOUT_LOG="$ARTIFACTS_DIR/qemu-stdout-$RUN_TAG.log"
echo "Artifacts dir: $ARTIFACTS_DIR (serial=$SERIAL_LOG)"

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

# Clean up temp files (but never the artifacts: serial log + qemu stdout log).
# When BOOTCREW_KEEP_VM=1 and we hit a failure we leave the QEMU monitor
# socket alone too so an operator can attach to the still-running guest.
cleanup_temp() {
  if [ "$BOOTCREW_KEEP_VM" = "1" ] && [ -n "$FAIL_PATH" ]; then
    echo "BOOTCREW_KEEP_VM=1: leaving QEMU monitor socket at $QEMU_MONITOR_SOCK for inspection"
    sudo rm -f "$OVMF_VARS_TEMP" 2>/dev/null || true
    return
  fi
  sudo rm -f "$OVMF_VARS_TEMP" "$QEMU_MONITOR_SOCK" 2>/dev/null || true
}
trap cleanup_temp EXIT

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

# Authenticate with the ephemeral key injected into this CI target.
ssh_root() {
  "$SSH_BIN" -i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null -o ConnectTimeout=1 \
    -p "$SSH_PORT" root@127.0.0.1 "$@"
}

# Wait for SSH to be ready (using public-key auth)
# For LUKS VMs, luks-unlock.py runs concurrently and injects the passphrase;
# SSH becomes available once the system has fully booted after unlock.
echo "Waiting for VM to boot and SSH to be ready (up to 240s)..."
SSH_READY=0
for i in {1..120}; do
  sleep 2
  if ssh_root "echo OK" 2>/dev/null; then
    echo "✅ SSH connection successful"
    SSH_READY=1
    # Capture a screendump as visual evidence the guest reached login.
    # Pattern adapted from projectbluefin/dakota-iso E2E flow.
    if [ -S "$QEMU_MONITOR_SOCK" ] && command -v socat >/dev/null 2>&1; then
      sudo sh -c "echo 'screendump $ARTIFACTS_DIR/screen-ready-$RUN_TAG.ppm' \
        | socat - UNIX-CONNECT:$QEMU_MONITOR_SOCK" 2>/dev/null || true
    fi
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
  FAIL_PATH=1

  if [ -f "$SERIAL_LOG" ]; then
    echo ""
    echo "=== Serial Console Output (for debugging) ==="
    sudo cat "$SERIAL_LOG" || cat "$SERIAL_LOG" || true
    echo "(Full log: $SERIAL_LOG — kept in artifacts dir)"
  fi

  if [ -f "$QEMU_STDOUT_LOG" ]; then
    echo ""
    echo "=== QEMU stdout/stderr ==="
    sudo cat "$QEMU_STDOUT_LOG" || cat "$QEMU_STDOUT_LOG" || true
  fi

  # The guest may have got far enough that the kernel is alive but SSH
  # never came up (e.g. failed initramfs, getty crash). Try a last-ditch
  # capture via the QEMU monitor: info status + a screendump of the
  # current display. Both no-ops if the monitor socket is gone.
  if [ -S "$QEMU_MONITOR_SOCK" ] && command -v socat >/dev/null 2>&1; then
    echo ""
    echo "=== QEMU monitor: info status ==="
    sudo sh -c "echo 'info status' | socat - UNIX-CONNECT:$QEMU_MONITOR_SOCK" 2>/dev/null \
      | tee "$ARTIFACTS_DIR/qemu-info-status-$RUN_TAG.log" || true
    # Capture the current framebuffer — often the only evidence we have when
    # the guest is wedged at the Plymouth prompt or an emergency shell.
    sudo sh -c "echo 'screendump $ARTIFACTS_DIR/screen-fail-$RUN_TAG.ppm' \
      | socat - UNIX-CONNECT:$QEMU_MONITOR_SOCK" 2>/dev/null || true
  fi

  if [ "$BOOTCREW_KEEP_VM" = "1" ]; then
    echo ""
    echo "BOOTCREW_KEEP_VM=1: leaving QEMU PID $QEMU_PID running for inspection."
    echo "  Serial:  $SERIAL_LOG"
    echo "  Stdout:  $QEMU_STDOUT_LOG"
    echo "  Monitor: $QEMU_MONITOR_SOCK"
    echo "  Kill with: sudo kill $QEMU_PID"
    exit 1
  fi

  kill $QEMU_PID 2>/dev/null || true
  exit 1
fi

if [ "$LUKS_EXIT" -ne 0 ]; then
  # SSH already succeeded above, so LUKS was definitely unlocked.
  # luks-unlock.py brightness-based detection can produce false positives
  # (e.g. Plymouth screen dims after passphrase accepted). Treat as warning only.
  echo "⚠️  luks-unlock.py reported non-zero exit ($LUKS_EXIT), but SSH succeeded — LUKS is working"
fi

echo ""
echo "=== System Information ==="
ssh_root "uname -a" || true

echo ""
echo "=== bootc status ==="
ssh_root "bootc status" 2>/dev/null \
    | tee "$ARTIFACTS_DIR/bootc-status-$RUN_TAG.log" || echo "⚠️  bootc not available"

echo ""
echo "=== bootctl status ==="
ssh_root "bootctl status" 2>/dev/null \
    | tee "$ARTIFACTS_DIR/bootctl-status-$RUN_TAG.log" || echo "⚠️  bootctl not available"

# Verify UEFI boot entries were written by efibootmgr (PR #2: -v /sys:/sys).
# Without /sys:/sys in the podman run, efibootmgr inside the bootc container
# cannot reach /sys/firmware/efi/efivars and silently skips registering entries.
# We check both efibootmgr (direct NVRAM) and bootctl (systemd-boot entries) so
# the test covers both GRUB and systemd-boot images.
echo ""
echo "=== UEFI boot entry verification (efibootmgr) ==="
EFIBOOT_OUT=$(ssh_root "efibootmgr" 2>/dev/null || true)
if [ -n "$EFIBOOT_OUT" ]; then
  echo "$EFIBOOT_OUT" | tee "$ARTIFACTS_DIR/efibootmgr-$RUN_TAG.log"
  # Assert at least one boot entry exists (e.g. "Boot0000*" line).
  if echo "$EFIBOOT_OUT" | grep -qE '^Boot[0-9A-Fa-f]{4}'; then
    echo "✅ UEFI boot entries present (efibootmgr)"
  else
    echo "❌ FAIL: no UEFI boot entries found — efibootmgr likely could not reach NVRAM."
    echo "   Regression: fisherman must pass -v /sys:/sys to the bootc container (PR #2)."
    # Treat as a warning for non-EFI images (e.g. GRUB on legacy BIOS).
    # Fail only when the image is systemd-boot based (which always registers entries).
    if echo "$EFIBOOT_OUT" | grep -qi 'not supported'; then
      echo "⚠️  efibootmgr: EFI not supported on this firmware — skipping assertion"
    else
      EFIBOOT_FAIL=1
    fi
  fi
else
  echo "⚠️  efibootmgr not available in guest — skipping UEFI entry assertion"
fi

echo ""
echo "=== journalctl -b (last boot) ==="
# Capture into artifacts always (cheap; ~1-2 MB) but only echo the tail to
# the CI log so we don't drown the rest of the workflow output.
ssh_root "journalctl -b --no-pager" 2>/dev/null \
    > "$ARTIFACTS_DIR/journal-$RUN_TAG.log" \
    && echo "(saved to $ARTIFACTS_DIR/journal-$RUN_TAG.log; tail follows)" \
    && tail -80 "$ARTIFACTS_DIR/journal-$RUN_TAG.log" \
    || echo "⚠️  journalctl capture failed"

echo ""
echo "=== bootc status (JSON) ==="
ssh_root "bootc status --json-pretty" 2>/dev/null || echo "⚠️  json output not available"

echo ""
echo "=== Checking for upgrade availability ==="
ssh_root "bootc upgrade --check" 2>/dev/null || echo "⚠️  upgrade check not available"

echo ""
echo "=== Shutting down VM ==="
ssh_root "shutdown -h now" 2>/dev/null || true

sleep 5
kill $QEMU_PID 2>/dev/null || true
wait $QEMU_PID 2>/dev/null || true

if [ "${EFIBOOT_FAIL:-0}" = "1" ]; then
  echo "❌ FAIL: UEFI boot entry assertion failed (see efibootmgr output above)"
  exit 1
fi

echo "✅ VM verification complete"
