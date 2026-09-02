#!/bin/bash
# Enable SSH in the installed bootc system
# Usage: enable-ssh-installed.sh <LOOPDEV> <COMPOSEFS> <SSH_PUBKEY_FILE>

set -e

LOOPDEV="${1:?LOOPDEV required}"
COMPOSEFS="${2:?COMPOSEFS required}"
SSH_PUBKEY_FILE="${3:?SSH_PUBKEY_FILE required}"

echo "Enabling SSH in installed system..."

# Find and mount the root partition.
#
# The root partition is NOT always the third one, and hardcoding p3 made every
# 2-partition target fail here with "special device /dev/loop0p3 does not
# exist" after a fully successful install:
#
#   Composefs + systemd-boot: 2 partitions — p1 = EFI, p2 = root
#   GRUB:                     3 partitions — p1 = EFI, p2 = /boot, p3 = root
#
# Root is the last partition in both, so count them and take that one. This is
# the same rule scripts/verify-installation.sh already applies; the two scripts
# read the same disks and disagreeing about their layout is how this went
# unnoticed.
if [[ "$LOOPDEV" == /dev/loop* ]]; then
  PART_SUFFIX="p"
else
  PART_SUFFIX=""
fi

PART_COUNT=$(sudo lsblk "$LOOPDEV" -o NAME -nr | grep -c "^${LOOPDEV##*/}${PART_SUFFIX}[0-9]" || true)
if [ "$PART_COUNT" -lt 2 ] || [ "$PART_COUNT" -gt 3 ]; then
  echo "WARNING: expected 2-3 partitions on $LOOPDEV, got $PART_COUNT"
  sudo lsblk -o NAME,SIZE,FSTYPE,LABEL "$LOOPDEV" || true
  exit 1
fi

ROOT_PART="${LOOPDEV}${PART_SUFFIX}${PART_COUNT}"
echo "Root partition: $ROOT_PART (${PART_COUNT}-partition layout)"

# Create temporary mount point
MOUNT_DIR=$(mktemp -d)
trap "sudo umount -R '$MOUNT_DIR' 2>/dev/null || true; rmdir '$MOUNT_DIR' 2>/dev/null || true" EXIT

echo "Mounting root filesystem at $MOUNT_DIR..."
sudo mount "$ROOT_PART" "$MOUNT_DIR" || {
  echo "WARNING: Could not mount root partition $ROOT_PART"
  exit 1
}

# Determine the rootfs directory based on storage type
# For composefs, check if the mounted directory has /etc/hostname (composefs native)
if [ "$COMPOSEFS" = "true" ] && [ -f "$MOUNT_DIR/etc/hostname" ]; then
  # Direct composefs mount - root is mounted directly
  ROOTFS="$MOUNT_DIR"
elif [ -d "$MOUNT_DIR/sysroot/ostree/deploy/default/deploy" ]; then
  # ostree-based (centos-bootc) with sysroot - find the hash subdirectory
  DEPLOY_BASE="$MOUNT_DIR/sysroot/ostree/deploy/default/deploy"
  HASH_DIR=$(sudo ls -d "$DEPLOY_BASE"/*.0 2>/dev/null | head -1)
  if [ -z "$HASH_DIR" ]; then
    echo "WARNING: Could not find deployment hash directory in $DEPLOY_BASE"
    exit 1
  fi
  ROOTFS="$HASH_DIR"
elif [ -d "$MOUNT_DIR/ostree/deploy/default/deploy" ]; then
  # ostree-based (centos-bootc) without sysroot - find the hash subdirectory
  DEPLOY_BASE="$MOUNT_DIR/ostree/deploy/default/deploy"
  HASH_DIR=$(sudo ls -d "$DEPLOY_BASE"/*.0 2>/dev/null | head -1)
  if [ -z "$HASH_DIR" ]; then
    echo "WARNING: Could not find deployment hash directory in $DEPLOY_BASE"
    exit 1
  fi
  ROOTFS="$HASH_DIR"
else
  ROOTFS="$MOUNT_DIR"
fi

echo "Root filesystem: $ROOTFS"

# Verify the rootfs is valid - check for common directories
if [ ! -d "$ROOTFS/usr" ] && [ ! -d "$ROOTFS/bin" ] && [ ! -d "$ROOTFS/etc" ]; then
  echo "WARNING: Invalid rootfs - essential directories not found at $ROOTFS"
  echo "Directory contents:"
  sudo ls -la "$ROOTFS" 2>/dev/null | head -20
  exit 1
fi

# Enable SSH in the installed system
echo "Installing SSH in deployed system..."
sudo chroot "$ROOTFS" sh -c '
  if command -v apt-get >/dev/null 2>&1; then
    # Debian-based
    apt-get update >/dev/null 2>&1 && apt-get install -y openssh-server 2>&1 | grep -v "^Get:" || true
    systemctl enable ssh 2>/dev/null || true
  elif command -v dnf >/dev/null 2>&1; then
    # Fedora/RHEL-based
    dnf install -y openssh-server openssh-clients 2>&1 | grep -v "^Installing " || true
    systemctl enable sshd 2>/dev/null || true
  fi
  
  # Generate host keys if they do not exist.
  # NOTE: no apostrophes in this block — it is a single-quoted sh -c script,
  # and one in a comment closes the quote early. That is not a harmless typo:
  # the remaining lines then run in the OUTER shell, where the sshd_config
  # appends below target the /etc/ssh/sshd_config of the CI RUNNER itself.
  if [ ! -f /etc/ssh/ssh_host_rsa_key ]; then
    ssh-keygen -A
  fi
  
  # CI uses an ephemeral key injected below; never enable password root login.
  sed -i "s/^#PermitRootLogin .*/PermitRootLogin prohibit-password/" /etc/ssh/sshd_config 2>/dev/null || true
  sed -i "s/^#PasswordAuthentication .*/PasswordAuthentication no/" /etc/ssh/sshd_config 2>/dev/null || true
  sed -i "s/^#PubkeyAuthentication .*/PubkeyAuthentication yes/" /etc/ssh/sshd_config 2>/dev/null || true
  echo "PermitRootLogin prohibit-password" >> /etc/ssh/sshd_config 2>/dev/null || true
  echo "PasswordAuthentication no" >> /etc/ssh/sshd_config 2>/dev/null || true
  echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config 2>/dev/null || true
' 2>/dev/null || true

# Create SSH directory and inject key
echo "Injecting SSH public key..."
sudo mkdir -p "$ROOTFS/root/.ssh" 2>/dev/null || true
# For Debian, handle the /root -> var/roothome symlink
if sudo test -L "$ROOTFS/root"; then
  sudo mkdir -p "$ROOTFS/var/roothome/.ssh" 2>/dev/null || true
  sudo cp "$SSH_PUBKEY_FILE" "$ROOTFS/var/roothome/.ssh/authorized_keys" || true
  sudo chmod 700 "$ROOTFS/var/roothome/.ssh" 2>/dev/null || true
  sudo chmod 600 "$ROOTFS/var/roothome/.ssh/authorized_keys" 2>/dev/null || true
else
  sudo cp "$SSH_PUBKEY_FILE" "$ROOTFS/root/.ssh/authorized_keys" || true
  sudo chmod 700 "$ROOTFS/root/.ssh" 2>/dev/null || true
  sudo chmod 600 "$ROOTFS/root/.ssh/authorized_keys" 2>/dev/null || true
fi

echo "✅ SSH enabled in installed system"
