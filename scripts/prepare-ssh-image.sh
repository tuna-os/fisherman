#!/bin/bash
# Prepare SSH-enabled container image with dynamic tool discovery
# Usage: ./prepare-ssh-image.sh IMAGE [SSH_PUBKEY_FILE]

set -e

# Source tool discovery helpers
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./find-tools.sh
source "$SCRIPT_DIR/find-tools.sh"

IMAGE="${1}"
SSH_PUBKEY_FILE="${2:-/tmp/bootcrew-ssh/id_rsa.pub}"

if [ -z "$IMAGE" ]; then
  echo "❌ Usage: $0 IMAGE [SSH_PUBKEY_FILE]"
  exit 1
fi

SSH_IMAGE="${IMAGE%:*}:ci-ssh-enabled"

echo "Preparing SSH-enabled image: $SSH_IMAGE"
echo "Source image: $IMAGE"
echo "SSH pubkey: $SSH_PUBKEY_FILE"
echo ""

# Find podman and sudo
PODMAN_BIN=$(find_podman) || {
  echo "❌ Podman not found"
  show_tool_info
  exit 1
}

SUDO_BIN=$(find_sudo) || {
  echo "❌ Sudo not found"
  show_tool_info
  exit 1
}

# Try to use pre-built SSH image if it exists locally
if $SUDO_BIN "$PODMAN_BIN" image exists "$SSH_IMAGE" 2>/dev/null; then
  echo "✅ Using pre-built SSH image"
  echo "SSH_IMAGE=$SSH_IMAGE"
  exit 0
fi

echo "Building SSH-enabled image locally..."

# Pull original image
echo "Pulling original image..."
$SUDO_BIN "$PODMAN_BIN" pull "$IMAGE"

# Create and start container (exec requires running container)
echo "Creating and starting container..."
CONTAINER=$($SUDO_BIN "$PODMAN_BIN" run -d "$IMAGE" sleep 3600)
echo "Container: $CONTAINER"
sleep 2  # Give container time to start

# Install sshd (detect package manager)
echo "Installing sshd..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" sh -c '
  if command -v apt-get >/dev/null 2>&1; then
    mkdir -p /var/lib/dpkg/status.d /var/lib/apt/lists/partial /var/cache/apt/archives/partial /var/log/apt
    touch /var/lib/dpkg/status /var/lib/apt/extended_states 2>/dev/null || true
  fi
  
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update >/dev/null 2>&1 && apt-get install -y openssh-server 2>&1 | grep -v "^Get:" || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y openssh-server openssh-clients 2>&1 | grep -v "^Installing " || true
  elif command -v zypper >/dev/null 2>&1; then
    zypper install -y openssh 2>&1 | grep -v "^Installing " || true
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy openssh --noconfirm 2>&1 | grep -v "^Installing " || true
  fi
  if ! command -v sshd >/dev/null 2>&1 && ! command -v ssh >/dev/null 2>&1; then
    echo "Warning: SSH not found, trying busybox dropbear" >&2
  fi
' || echo "WARNING: sshd installation had issues (continuing anyway)"

# Enable sshd/ssh
echo "Enabling SSH daemon..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" sh -c '
  for svc in sshd ssh openssh-server; do
    systemctl enable $svc 2>/dev/null && { echo "Enabled: $svc"; break; }
  done
  true  # Always succeed even if enable fails
' || true

# Enable DHCP networking on minimal images that lack NetworkManager.
echo "Configuring fallback DHCP networking..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" sh -c '
  if [ ! -f /usr/lib/systemd/system/NetworkManager.service ] && [ -f /usr/lib/systemd/system/systemd-networkd.service ]; then
    mkdir -p /etc/systemd/network /etc/systemd/system/multi-user.target.wants
    printf "[Match]\nName=en* eth*\n\n[Network]\nDHCP=yes\nLinkLocalAddressing=yes\nIPv6AcceptRA=yes\n" > /etc/systemd/network/20-wired.network
    ln -sf /usr/lib/systemd/system/systemd-networkd.service /etc/systemd/system/multi-user.target.wants/systemd-networkd.service
    ln -sf /usr/lib/systemd/system/systemd-networkd.service /etc/systemd/system/dbus-org.freedesktop.network1.service
  fi
' || true

# Generate host keys (must be done before system startup)
echo "Generating SSH host keys..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" sh -c '
  if ! test -f /etc/ssh/ssh_host_rsa_key; then
    ssh-keygen -A 2>/dev/null || {
      # Fallback: create manually
      ssh-keygen -t rsa -N "" -f /etc/ssh/ssh_host_rsa_key 2>/dev/null || true
      ssh-keygen -t ed25519 -N "" -f /etc/ssh/ssh_host_ed25519_key 2>/dev/null || true
    }
  fi
  ls -la /etc/ssh/ssh_host_*key 2>/dev/null || echo "Host keys may be missing"
' || true

# Configure SSH
echo "Configuring SSH..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" sh -c '
  # For Debian, /root is a symlink to var/roothome which may not exist yet
  mkdir -p /var/roothome/.ssh 2>/dev/null || true
  mkdir -p /root/.ssh 2>/dev/null || true
  chmod 700 /root/.ssh 2>/dev/null || true
  sed -i "s/^#PermitRootLogin .*/PermitRootLogin prohibit-password/" /etc/ssh/sshd_config 2>/dev/null || true
  sed -i "s/^#PasswordAuthentication .*/PasswordAuthentication no/" /etc/ssh/sshd_config 2>/dev/null || true
  sed -i "s/^#PubkeyAuthentication .*/PubkeyAuthentication yes/" /etc/ssh/sshd_config 2>/dev/null || true
  echo "PermitRootLogin prohibit-password" >> /etc/ssh/sshd_config 2>/dev/null || true
  echo "PasswordAuthentication no" >> /etc/ssh/sshd_config 2>/dev/null || true
  echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config 2>/dev/null || true
' || true

# Add SSH public key using podman cp (more reliable than piping)
if [ -f "$SSH_PUBKEY_FILE" ]; then
  echo "Injecting SSH public key..."
  $SUDO_BIN "$PODMAN_BIN" cp "$SSH_PUBKEY_FILE" "$CONTAINER:/root/.ssh/authorized_keys" || {
    echo "⚠️  Failed to copy SSH public key"
    exit 1
  }
  $SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" chmod 600 /root/.ssh/authorized_keys || true
else
  echo "⚠️  SSH public key file not found at $SSH_PUBKEY_FILE"
fi

# Stop and commit container to new image
echo "Stopping container..."
$SUDO_BIN "$PODMAN_BIN" stop "$CONTAINER" || true
echo "Committing image..."
$SUDO_BIN "$PODMAN_BIN" commit "$CONTAINER" "$SSH_IMAGE" || exit 1
$SUDO_BIN "$PODMAN_BIN" rm "$CONTAINER" || true

echo "✅ SSH-enabled image created: $SSH_IMAGE"
echo "SSH_IMAGE=$SSH_IMAGE"
