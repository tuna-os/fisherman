#!/bin/bash
# Verify installation partitions and basic structure
# Usage: ./verify-installation.sh LOOPDEV [COMPOSEFS]
# Uses dynamic tool discovery for CI and local environments

set -e

# Source tool discovery helpers
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./find-tools.sh
source "$SCRIPT_DIR/find-tools.sh"

LOOPDEV="${1}"
COMPOSEFS="${2:-false}"

if [ -z "$LOOPDEV" ]; then
  echo "❌ Usage: $0 LOOPDEV [COMPOSEFS]"
  exit 1
fi

# Find required tools
MOUNT_BIN=$(find_mount) || {
  echo "❌ Mount command not found"
  show_tool_info
  exit 1
}

SUDO_BIN=$(find_sudo) || {
  echo "❌ Sudo not found"
  show_tool_info
  exit 1
}

echo "=== Verifying installation ==="

# 1. Check partition layout (2 or 3 partitions depending on bootloader)
# - Composefs systemd-boot: 2 partitions (EFI + root)
# - GRUB: 3 partitions (EFI + /boot + root)
PART_COUNT=$(sudo lsblk "$LOOPDEV" -o NAME -nr | grep -c "^${LOOPDEV##*/}p" || true)
if [ "$PART_COUNT" -lt 2 ] || [ "$PART_COUNT" -gt 3 ]; then
  echo "FAIL: expected 2-3 partitions, got $PART_COUNT"
  sudo lsblk -o NAME,SIZE,FSTYPE,LABEL "$LOOPDEV"
  exit 1
fi

echo "✅ Partition layout correct ($PART_COUNT partitions)"

# 2. Mount and verify partitions
# For 2-partition layout (systemd-boot composefs):
#   p1 = EFI, p2 = root
# For 3-partition layout (GRUB):
#   p1 = EFI, p2 = /boot, p3 = root
if [ "$PART_COUNT" -eq 2 ]; then
  BOOT_PART="${LOOPDEV}p1"  # This is just EFI for 2-partition
  ROOT_PART="${LOOPDEV}p2"
else
  BOOT_PART="${LOOPDEV}p2"
  ROOT_PART="${LOOPDEV}p3"
fi

# Always verify the EFI partition (p1) for BOOTX64.EFI, regardless of layout.
EFI_PART="${LOOPDEV}p1"
EFI_DIR=$(mktemp -d)
sudo "$MOUNT_BIN" "$EFI_PART" "$EFI_DIR"

if [ ! -f "$EFI_DIR/EFI/BOOT/BOOTX64.EFI" ]; then
  echo "FAIL: EFI/BOOT/BOOTX64.EFI not found on EFI partition $EFI_PART"
  echo "--- EFI partition contents ---"
  find "$EFI_DIR" -type f 2>/dev/null || true
  $SUDO_BIN umount "$EFI_DIR" || true
  rmdir "$EFI_DIR"
  exit 1
fi
echo "✅ EFI/BOOT/BOOTX64.EFI present on EFI partition"

$SUDO_BIN umount "$EFI_DIR"
rmdir "$EFI_DIR"

VERIFY_DIR=$(mktemp -d)
sudo "$MOUNT_BIN" "$BOOT_PART" "$VERIFY_DIR"

ROOT_DIR=$(mktemp -d)
sudo "$MOUNT_BIN" "$ROOT_PART" "$ROOT_DIR"

# Debug: show root structure
echo "--- Root directory structure ---"
$SUDO_BIN ls -F "$ROOT_DIR"
if [ -d "$ROOT_DIR/ostree" ]; then
  echo "--- Ostree structure ---"
  $SUDO_BIN ls -R "$ROOT_DIR/ostree" | head -n 20
fi

# Verify hostname
if [ "$COMPOSEFS" = "true" ]; then
  if [ ! -f "$ROOT_DIR/etc/hostname" ]; then
    echo "FAIL: $ROOT_DIR/etc/hostname not found (composefs-native)"
    $SUDO_BIN umount "$ROOT_DIR" || true
    $SUDO_BIN umount "$VERIFY_DIR" || true
    exit 1
  fi
  echo "✅ composefs-native hostname at $ROOT_DIR/etc/hostname"
else
  echo "✅ ostree-based installation verified"
fi

$SUDO_BIN umount "$ROOT_DIR"
$SUDO_BIN umount "$VERIFY_DIR"
rmdir "$ROOT_DIR"
rmdir "$VERIFY_DIR"
echo "✅ Installation verification passed"
