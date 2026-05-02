#!/bin/bash
# Enable SSH in the installed bootc system
# Usage: enable-ssh-installed.sh <LOOPDEV> <COMPOSEFS> <SSH_PUBKEY_FILE>

set -e

LOOPDEV="${1:?LOOPDEV required}"
COMPOSEFS="${2:?COMPOSEFS required}"
SSH_PUBKEY_FILE="${3:?SSH_PUBKEY_FILE required}"

echo "Enabling SSH in installed system..."

# Find and mount the root partition
if [[ "$LOOPDEV" == /dev/loop* ]]; then
  ROOT_PART="${LOOPDEV}p3"
else
  ROOT_PART="${LOOPDEV}3"
fi

# Create temporary mount point
MOUNT_DIR=$(mktemp -d)
trap "sudo umount -R '$MOUNT_DIR' 2>/dev/null || true; rmdir '$MOUNT_DIR' 2>/dev/null || true" EXIT

echo "Mounting root filesystem at $MOUNT_DIR..."
sudo mount "$ROOT_PART" "$MOUNT_DIR" || {
  echo "WARNING: Could not mount root partition $ROOT_PART"
  exit 1
}

# Determine the rootfs directory based on storage type
if [ "$COMPOSEFS" = "true" ]; then
  # For composefs, the root filesystem is mounted directly
  # It has /etc, /usr, etc. at the top level
  ROOTFS="$MOUNT_DIR"
elif [ -d "$MOUNT_DIR/sysroot/ostree/deploy/default/deploy" ]; then
  # ostree-based (centos-bootc) with sysroot
  DEPLOY_DIR=$(sudo find "$MOUNT_DIR/sysroot/ostree/deploy/default/deploy" -maxdepth 1 -type d | head -1)
  if [ -z "$DEPLOY_DIR" ]; then
    echo "WARNING: Could not find deployment directory"
    exit 1
  fi
  ROOTFS="$DEPLOY_DIR"
elif [ -d "$MOUNT_DIR/ostree/deploy/default/deploy" ]; then
  # ostree-based (centos-bootc) without sysroot
  DEPLOY_DIR=$(sudo find "$MOUNT_DIR/ostree/deploy/default/deploy" -maxdepth 1 -type d | head -1)
  if [ -z "$DEPLOY_DIR" ]; then
    echo "WARNING: Could not find deployment directory"
    exit 1
  fi
  ROOTFS="$DEPLOY_DIR"
else
  ROOTFS="$MOUNT_DIR"
fi

echo "Root filesystem: $ROOTFS"

# Verify the rootfs is valid
if [ ! -d "$ROOTFS/usr" ]; then
  echo "WARNING: Invalid rootfs - /usr not found at $ROOTFS"
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
  
  # Generate host keys if they don't exist
  if [ ! -f /etc/ssh/ssh_host_rsa_key ]; then
    ssh-keygen -A
  fi
  
  # Configure SSH
  sed -i "s/^#PermitRootLogin .*/PermitRootLogin yes/" /etc/ssh/sshd_config 2>/dev/null || true
  sed -i "s/^#PasswordAuthentication .*/PasswordAuthentication yes/" /etc/ssh/sshd_config 2>/dev/null || true
  sed -i "s/^#PubkeyAuthentication .*/PubkeyAuthentication yes/" /etc/ssh/sshd_config 2>/dev/null || true
' 2>/dev/null || true

# Set root password
echo "Setting root password..."
sudo chroot "$ROOTFS" sh -c "echo 'root:bootcrew-test' | chpasswd" 2>/dev/null || true

# Create SSH directory and inject key
echo "Injecting SSH public key..."
sudo mkdir -p "$ROOTFS/root/.ssh" 2>/dev/null || true
# For Debian composefs, handle the /root -> var/roothome symlink
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
