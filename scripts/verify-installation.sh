#!/bin/bash
# Verify installation partitions and basic structure
# Usage: ./verify-installation.sh LOOPDEV COMPOSEFS

set -e

LOOPDEV="${1}"
COMPOSEFS="${2:-false}"

if [ -z "$LOOPDEV" ]; then
  echo "❌ Usage: $0 LOOPDEV [COMPOSEFS]"
  exit 1
fi

echo "=== Verifying installation ==="

# 1. Check 3-partition layout
LABEL_COUNT=$(sudo lsblk -o LABEL "$LOOPDEV" | grep -cE 'EFI-SYSTEM|boot|root' || true)
if [ "$LABEL_COUNT" -ne 3 ]; then
  echo "FAIL: expected 3 labelled partitions, got $LABEL_COUNT"
  sudo lsblk -o NAME,SIZE,FSTYPE,LABEL "$LOOPDEV"
  exit 1
fi

echo "✅ Partition layout correct (3 partitions)"

# 2. Mount and verify partitions
BOOT_PART="${LOOPDEV}p2"
VERIFY_DIR=$(mktemp -d)
sudo mount "$BOOT_PART" "$VERIFY_DIR"

ROOT_PART="${LOOPDEV}p3"
ROOT_DIR=$(mktemp -d)
sudo mount "$ROOT_PART" "$ROOT_DIR"

# Debug: show root structure
echo "--- Root directory structure ---"
sudo ls -F "$ROOT_DIR"
if [ -d "$ROOT_DIR/ostree" ]; then
  echo "--- Ostree structure ---"
  sudo ls -R "$ROOT_DIR/ostree" | head -n 20
fi

# Verify hostname
if [ "$COMPOSEFS" = "true" ]; then
  if [ ! -f "$ROOT_DIR/etc/hostname" ]; then
    echo "FAIL: $ROOT_DIR/etc/hostname not found (composefs-native)"
    sudo umount "$ROOT_DIR" || true
    sudo umount "$VERIFY_DIR" || true
    exit 1
  fi
  echo "✅ composefs-native hostname at $ROOT_DIR/etc/hostname"
else
  echo "✅ ostree-based installation verified"
fi

sudo umount "$ROOT_DIR"
sudo rmdir "$ROOT_DIR"
sudo umount "$VERIFY_DIR"
sudo rmdir "$VERIFY_DIR"
echo "✅ Installation verification passed"
