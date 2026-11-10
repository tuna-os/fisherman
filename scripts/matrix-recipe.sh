#!/bin/bash
# Turn one tests/bootcrew-matrix.yaml entry into the fisherman recipe that
# `just bootcrew-ci-test` installs. Prints the recipe on stdout.
#
# Usage: matrix-recipe.sh IMAGE_JSON DISK [VAR_DISK] [LUKS_PASSPHRASE]
#
#   IMAGE_JSON       the matrix entry as JSON
#   DISK             the install target (a loop device in CI)
#   VAR_DISK         the second disk; required when the entry sets var_disk
#   LUKS_PASSPHRASE  overrides the entry's luks_passphrase
#
# Matrix fields read here (see the header of tests/bootcrew-matrix.yaml):
#   name, ssh_image_name, filesystem, composefs_backend, unified_storage,
#   selinux_disabled, bootloader, luks, luks_passphrase, encryption_type,
#   user, var_disk
#
# It is a script of its own, and not a heredoc in the justfile, so that
# tests/check-validation.sh can expand every matrix entry and pass each recipe
# to `fisherman validate` without a disk or a VM.

set -euo pipefail

IMAGE_JSON="${1:?IMAGE_JSON required}"
DISK="${2:?DISK required}"
VAR_DISK="${3:-}"
PASSPHRASE_OVERRIDE="${4:-}"

field() { printf '%s' "$IMAGE_JSON" | jq -r "$1"; }

NAME=$(field '.name')
SSH_NAME=$(field '.ssh_image_name // .name')
LUKS=$(field '.luks // false')
ENCRYPTION_TYPE=$(field '.encryption_type // ""')
PASSPHRASE=$(field '.luks_passphrase // ""')
[ -n "$PASSPHRASE_OVERRIDE" ] && PASSPHRASE="$PASSPHRASE_OVERRIDE"
WANT_VAR_DISK=$(field '.var_disk // false')

# The matrix entry names the bootloader. Entries from before that field
# existed fall back to their name.
BOOTLOADER=$(field '.bootloader // ""')
if [ -z "$BOOTLOADER" ]; then
  case "$NAME" in
    debian-bootc*|arch-bootc*) BOOTLOADER="systemd" ;;
  esac
fi

# luks: true alone means a passphrase-only container. encryption_type picks
# one of the TPM modes. tpm2-luks takes no passphrase: fisherman generates
# the recovery key and reports it in a recovery_key event.
if [ "$LUKS" != "true" ]; then
  if [ -n "$ENCRYPTION_TYPE" ] && [ "$ENCRYPTION_TYPE" != "none" ]; then
    echo "matrix entry $NAME sets encryption_type=$ENCRYPTION_TYPE without luks: true" >&2
    exit 1
  fi
  ENCRYPTION_TYPE="none"
elif [ -z "$ENCRYPTION_TYPE" ]; then
  ENCRYPTION_TYPE="luks-passphrase"
fi
case "$ENCRYPTION_TYPE" in
  none|tpm2-luks) PASSPHRASE="" ;;
  luks-passphrase|tpm2-luks-passphrase)
    if [ -z "$PASSPHRASE" ]; then
      echo "matrix entry $NAME: encryption_type=$ENCRYPTION_TYPE needs luks_passphrase" >&2
      exit 1
    fi
    ;;
  *)
    echo "matrix entry $NAME: unknown encryption_type '$ENCRYPTION_TYPE'" >&2
    exit 1
    ;;
esac

if [ "$WANT_VAR_DISK" = "true" ] && [ -z "$VAR_DISK" ]; then
  echo "matrix entry $NAME sets var_disk: true but no VAR_DISK was given" >&2
  exit 1
fi
[ "$WANT_VAR_DISK" = "true" ] || VAR_DISK=""

# SSH-enabled images are pre-built by build-ssh-images.yml and pushed to
# ghcr.io/tuna-os/fisherman/<name>:ssh-enabled. Variants of one base image
# (LUKS, user, var disk) reuse its tag through ssh_image_name.
SSH_IMAGE="ghcr.io/tuna-os/fisherman/${SSH_NAME}:ssh-enabled"

printf '%s' "$IMAGE_JSON" | jq \
  --arg disk "$DISK" \
  --arg image "$SSH_IMAGE" \
  --arg bootloader "$BOOTLOADER" \
  --arg enc_type "$ENCRYPTION_TYPE" \
  --arg passphrase "$PASSPHRASE" \
  --arg var_disk "$VAR_DISK" \
  '{
    disk: $disk,
    filesystem: (.filesystem // "xfs"),
    composeFsBackend: (.composefs_backend // false),
    unifiedStorage: (.unified_storage // false),
    selinuxDisabled: (.selinux_disabled // false),
    encryption: (if $passphrase == ""
                 then {type: $enc_type}
                 else {type: $enc_type, passphrase: $passphrase} end),
    image: $image,
    hostname: "ci-test",
    flatpaks: []
  }
  + (if $bootloader == "" then {} else {bootloader: $bootloader} end)
  + (if .user then {user: .user} else {} end)
  + (if $var_disk == "" then {} else {varDisk: {disk: $var_disk, keepExisting: false}} end)'
