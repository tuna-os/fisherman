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

# Try to pull pre-built SSH image first
if $SUDO_BIN "$PODMAN_BIN" pull "$SSH_IMAGE" 2>/dev/null; then
  echo "✅ Using pre-built SSH image"
  echo "SSH_IMAGE=$SSH_IMAGE"
  exit 0
fi

echo "Building SSH-enabled image locally..."

# Pull original image
echo "Pulling original image..."
$SUDO_BIN "$PODMAN_BIN" pull "$IMAGE"

# Create container
echo "Creating container..."
CONTAINER=$($SUDO_BIN "$PODMAN_BIN" create "$IMAGE" bash)
echo "Container: $CONTAINER"

# Install sshd (detect package manager)
echo "Installing sshd..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" sh -c '
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update && apt-get install -y openssh-server || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y openssh-server openssh-clients || true
  elif command -v zypper >/dev/null 2>&1; then
    zypper install -y openssh || true
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy openssh --noconfirm || true
  fi
' || echo "⚠️  sshd installation had issues (continuing anyway)"

# Enable sshd
echo "Enabling sshd..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" sh -c 'systemctl enable sshd || systemctl enable ssh || true' || true

# Generate host keys
echo "Generating SSH host keys..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" ssh-keygen -A || true

# Configure SSH
echo "Configuring SSH..."
$SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" sh -c '
  mkdir -p /root/.ssh && chmod 700 /root/.ssh
  sed -i "s/^#PermitRootLogin .*/PermitRootLogin yes/" /etc/ssh/sshd_config 2>/dev/null || true
  sed -i "s/^#PubkeyAuthentication .*/PubkeyAuthentication yes/" /etc/ssh/sshd_config 2>/dev/null || true
  echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config 2>/dev/null || true
  echo "PermitRootLogin yes" >> /etc/ssh/sshd_config 2>/dev/null || true
' || true

# Add SSH public key
if [ -f "$SSH_PUBKEY_FILE" ]; then
  echo "Injecting SSH public key..."
  cat "$SSH_PUBKEY_FILE" | $SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" tee -a /root/.ssh/authorized_keys > /dev/null || true
  $SUDO_BIN "$PODMAN_BIN" exec "$CONTAINER" chmod 600 /root/.ssh/authorized_keys || true
else
  echo "⚠️  SSH public key file not found at $SSH_PUBKEY_FILE"
fi

# Commit container to new image
echo "Committing image..."
$SUDO_BIN "$PODMAN_BIN" commit "$CONTAINER" "$SSH_IMAGE" || exit 1
$SUDO_BIN "$PODMAN_BIN" rm "$CONTAINER" || true

echo "✅ SSH-enabled image created: $SSH_IMAGE"
echo "SSH_IMAGE=$SSH_IMAGE"
