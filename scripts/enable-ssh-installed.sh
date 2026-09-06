#!/bin/bash
# Enable SSH in the installed bootc system
# Usage: enable-ssh-installed.sh <LOOPDEV> <COMPOSEFS> <SSH_PUBKEY_FILE> [LUKS_PASSPHRASE]

set -e

LOOPDEV="${1:?LOOPDEV required}"
COMPOSEFS="${2:?COMPOSEFS required}"
SSH_PUBKEY_FILE="${3:?SSH_PUBKEY_FILE required}"
LUKS_PASSPHRASE="${4:-}"  # optional; if set, opens the LUKS root container before mounting

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
LUKS_MAPPER="fisherman-ssh-$$"
LUKS_OPENED=0

# Cleanup trap: unmount and close the LUKS container on any exit. Mirrors
# scripts/verify-installation.sh, which opens the same container right after
# this script and has to find it closed again.
cleanup_enable_ssh() {
  sudo umount -R "$MOUNT_DIR" 2>/dev/null || true
  if [ "$LUKS_OPENED" -eq 1 ]; then
    sudo cryptsetup luksClose "$LUKS_MAPPER" 2>/dev/null || true
  fi
  rmdir "$MOUNT_DIR" 2>/dev/null || true
}
trap cleanup_enable_ssh EXIT

# Open the LUKS container if a passphrase was given, otherwise mount directly.
MOUNT_SRC="$ROOT_PART"
if [ -n "$LUKS_PASSPHRASE" ]; then
  LUKS_TYPE=$(sudo blkid -s TYPE -o value "$ROOT_PART" 2>/dev/null || true)
  if [ "$LUKS_TYPE" = "crypto_LUKS" ]; then
    echo "Opening LUKS container on $ROOT_PART..."
    echo -n "$LUKS_PASSPHRASE" | sudo cryptsetup luksOpen "$ROOT_PART" "$LUKS_MAPPER" --key-file=-
    LUKS_OPENED=1
    MOUNT_SRC="/dev/mapper/$LUKS_MAPPER"
  else
    echo "WARNING: LUKS_PASSPHRASE provided but $ROOT_PART is not crypto_LUKS (type: $LUKS_TYPE) — mounting directly"
  fi
fi

echo "Mounting root filesystem at $MOUNT_DIR..."
sudo mount "$MOUNT_SRC" "$MOUNT_DIR" || {
  echo "WARNING: Could not mount root partition $MOUNT_SRC"
  exit 1
}

# Determine the rootfs directory based on storage type, and — separately —
# the stateroot var: the directory the booted system mounts over /var.
#
# These are NOT the same tree. Every bootc layout keeps the deployment (the
# image content plus its writable /etc) apart from the persistent state, and
# a deployment's own var/ is never what the running system sees as /var:
#
#   ostree (centos-bootc, GRUB):
#     deployment: ostree/deploy/default/deploy/<hash>.0   (or under sysroot/)
#     /var:       ostree/deploy/default/var
#   composefs-native (dakota, systemd-boot):
#     deployment: state/deploy/<hash>   (only the writable etc/ lives here)
#     /var:       state/os/default/var
#
# fisherman's own post-install code already learned this the hard way for
# user homes (internal/post/user.go: "the DEPLOYMENT's own var/ — which the
# booted system never sees"). This script had not: it wrote root's
# authorized_keys into <deployment>/var/roothome, which is exactly the tree
# the guest never mounts, so every post-#172 (key-only auth) run booted to a
# login prompt with sshd running and still timed out on the SSH probe.
STATE_VAR=""
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
  STATE_VAR="$MOUNT_DIR/sysroot/ostree/deploy/default/var"
elif [ -d "$MOUNT_DIR/ostree/deploy/default/deploy" ]; then
  # ostree-based (centos-bootc) without sysroot - find the hash subdirectory
  DEPLOY_BASE="$MOUNT_DIR/ostree/deploy/default/deploy"
  HASH_DIR=$(sudo ls -d "$DEPLOY_BASE"/*.0 2>/dev/null | head -1)
  if [ -z "$HASH_DIR" ]; then
    echo "WARNING: Could not find deployment hash directory in $DEPLOY_BASE"
    exit 1
  fi
  ROOTFS="$HASH_DIR"
  STATE_VAR="$MOUNT_DIR/ostree/deploy/default/var"
elif [ -d "$MOUNT_DIR/state/deploy" ]; then
  # fisherman's own composefs layout, which none of the branches above match
  # (from PR #196).
  #
  # The composefs branch expects /etc/hostname at the mount root; the two
  # ostree branches expect `<name>.0` directories. fisherman writes neither:
  # it deploys to state/deploy/<hash>, with a bare 128-character hash and no
  # `.0` suffix (see the install log's `mkdir -p
  # /mnt/fisherman-target/state/deploy/<hash>/etc`).
  #
  # Falling through to the else left ROOTFS at the mount point, whose only
  # entries are lost+found, state and var, so the "essential directories not
  # found" guard below rejected a perfectly good install. That guard was
  # right about what it saw and wrong about what it meant.
  DEPLOY_BASE="$MOUNT_DIR/state/deploy"
  HASH_DIR=$(sudo ls -d "$DEPLOY_BASE"/*/ 2>/dev/null | head -1)
  if [ -z "$HASH_DIR" ]; then
    echo "WARNING: Could not find deployment directory in $DEPLOY_BASE"
    sudo ls -la "$DEPLOY_BASE" 2>/dev/null | head -20
    exit 1
  fi
  ROOTFS="${HASH_DIR%/}"
  STATE_VAR="$MOUNT_DIR/state/os/default/var"
else
  ROOTFS="$MOUNT_DIR"
fi

echo "Root filesystem: $ROOTFS"
[ -n "$STATE_VAR" ] && echo "Stateroot var:   $STATE_VAR"

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

# Write the public key to DEST_FILE (creating its directory) with the mode
# and SELinux label sshd insists on. The disks are written from an Ubuntu
# runner with no SELinux policy loaded, so nothing labels these files for us;
# on an enforcing guest (Fedora/CentOS bootc) an unlabeled authorized_keys
# is unlabeled_t and sshd_t may not read it. setfattr on security.selinux
# works without SELinux on the writing host, and is harmless on guests that
# never look at it (Debian, Arch).
install_authorized_keys() {
  local dest_file="$1" label="$2"
  local dest_dir
  dest_dir=$(dirname "$dest_file")
  sudo mkdir -p "$dest_dir"
  sudo cp "$SSH_PUBKEY_FILE" "$dest_file"
  sudo chown root:root "$dest_dir" "$dest_file"
  sudo chmod 700 "$dest_dir"
  sudo chmod 600 "$dest_file"
  if command -v setfattr >/dev/null 2>&1; then
    sudo setfattr -n security.selinux -v "$label" "$dest_dir" "$dest_file" 2>/dev/null || true
  fi
  echo "  authorized_keys -> ${dest_file#"$MOUNT_DIR"}"
}

# Where does the *booted* system look for root's authorized_keys?
#
# 1. root's home from the deployment's /etc/passwd (/root on every bootc
#    image seen so far, /var/roothome would also be fine).
# 2. On bootc images /root is a symlink to var/roothome, so resolve one level
#    of symlink inside the deployment. composefs-native deployments only carry
#    etc/, so there is no symlink to inspect; /root -> var/roothome is a bootc
#    invariant (internal/post/user.go relies on the /home one) and we assume it.
# 3. Anything under /var lives in the stateroot var, not the deployment.
echo "Injecting SSH public key..."
ROOT_HOME=$(sudo awk -F: '$1 == "root" { print $6 }' "$ROOTFS/etc/passwd" 2>/dev/null | head -1)
ROOT_HOME="${ROOT_HOME:-/root}"
if [ "$ROOT_HOME" = "/root" ]; then
  if sudo test -L "$ROOTFS/root"; then
    LINK_TARGET=$(sudo readlink "$ROOTFS/root")
    case "$LINK_TARGET" in
      /*) ROOT_HOME="$LINK_TARGET" ;;
      *)  ROOT_HOME="/$LINK_TARGET" ;;
    esac
  elif [ -n "$STATE_VAR" ] && ! sudo test -d "$ROOTFS/root"; then
    ROOT_HOME="/var/roothome"
  fi
fi
echo "  root home on target: $ROOT_HOME"

case "$ROOT_HOME" in
  /var/*)
    if [ -n "$STATE_VAR" ]; then
      HOME_ON_DISK="$STATE_VAR/${ROOT_HOME#/var/}"
    else
      HOME_ON_DISK="$ROOTFS$ROOT_HOME"
    fi
    ;;
  *)
    HOME_ON_DISK="$ROOTFS$ROOT_HOME"
    ;;
esac
install_authorized_keys "$HOME_ON_DISK/.ssh/authorized_keys" "system_u:object_r:ssh_home_t:s0"
# The home itself may have been created just now by mkdir -p; label it the
# way restorecon would (/root and /var/roothome are both admin_home_t).
if command -v setfattr >/dev/null 2>&1; then
  sudo setfattr -n security.selinux -v "system_u:object_r:admin_home_t:s0" "$HOME_ON_DISK" 2>/dev/null || true
fi

# Belt and braces: also register the key through a location that does not
# depend on getting root's home or the var mapping right. sshd_config on
# Fedora, CentOS, Debian and Arch all `Include /etc/ssh/sshd_config.d/*.conf`
# before their own directives, and the deployment's etc/ is the writable /etc
# on every layout above, so a drop-in there is honoured on first boot.
# `AuthorizedKeysFile` keeps the default home-relative path as the second entry.
if sudo test -d "$ROOTFS/etc/ssh"; then
  install_authorized_keys "$ROOTFS/etc/ssh/authorized_keys.d/root" "system_u:object_r:etc_t:s0"
  sudo chmod 755 "$ROOTFS/etc/ssh/authorized_keys.d"
  sudo mkdir -p "$ROOTFS/etc/ssh/sshd_config.d"
  printf '%s\n' \
    "# Written by scripts/enable-ssh-installed.sh for the bootcrew E2E harness." \
    "AuthorizedKeysFile /etc/ssh/authorized_keys.d/%u .ssh/authorized_keys" \
    "PubkeyAuthentication yes" \
    "PermitRootLogin prohibit-password" \
    "PasswordAuthentication no" \
    | sudo tee "$ROOTFS/etc/ssh/sshd_config.d/10-fisherman-ci.conf" >/dev/null
  sudo chmod 644 "$ROOTFS/etc/ssh/sshd_config.d/10-fisherman-ci.conf"
  if command -v setfattr >/dev/null 2>&1; then
    sudo setfattr -n security.selinux -v "system_u:object_r:etc_t:s0" \
      "$ROOTFS/etc/ssh/sshd_config.d" "$ROOTFS/etc/ssh/sshd_config.d/10-fisherman-ci.conf" 2>/dev/null || true
  fi
  echo "  sshd drop-in -> ${ROOTFS#"$MOUNT_DIR"}/etc/ssh/sshd_config.d/10-fisherman-ci.conf"
else
  echo "WARNING: no etc/ssh in deployment — skipping sshd_config.d drop-in"
fi

echo "✅ SSH enabled in installed system"
