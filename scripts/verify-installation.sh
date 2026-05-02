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

# 1. Check 3-partition layout
LABEL_COUNT=$($SUDO_BIN lsblk -o LABEL "$LOOPDEV" | grep -cE 'EFI-SYSTEM|boot|root' || true)
if [ "$LABEL_COUNT" -ne 3 ]; then
  echo "FAIL: expected 3 labelled partitions, got $LABEL_COUNT"
  $SUDO_BIN lsblk -o NAME,SIZE,FSTYPE,LABEL "$LOOPDEV"
  exit 1
fi

echo "✅ Partition layout correct (3 partitions)"

# 2. Mount and verify partitions
BOOT_PART="${LOOPDEV}p2"
VERIFY_DIR=$(mktemp -d)
$SUDO_BIN "$MOUNT_BIN" "$BOOT_PART" "$VERIFY_DIR"

ROOT_PART="${LOOPDEV}p3"
ROOT_DIR=$(mktemp -d)
$SUDO_BIN "$MOUNT_BIN" "$ROOT_PART" "$ROOT_DIR"

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
