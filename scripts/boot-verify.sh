#!/bin/bash
# Verify system boot via SSH and query bootc status
# Usage: ./boot-verify.sh [SSH_PORT] [SSH_KEY] [VM_TIMEOUT] [VM_MEMORY] [LOOPDEV] [IMAGE_NAME] [LUKS_PASSPHRASE]
# Works with dynamic tool discovery for both CI and local environments
#
# Optional environment (each one adds an assertion or a device; all are off
# by default, so a plain boot check behaves as before):
#   VERIFY_USER, VERIFY_USER_PASSWORD, VERIFY_USER_GROUPS
#       The account the recipe's `user` block asked for. Asserts that it
#       exists, has a home directory, is in each comma-separated group, can
#       open a login session, and that the password authenticates (and a
#       wrong one does not).
#   EXTRA_DISK
#       A second block device, attached as the second AHCI disk.
#   VERIFY_VAR_DISK=1
#       Asserts that /var is its own xfs filesystem labelled "var" on a
#       device other than the root (the recipe's `varDisk`).
#   TPM_STATE_DIR
#       Starts swtpm with its state in this directory and attaches it as a
#       TPM 2.0. Reuse the directory to boot the same "machine" again.
#   VERIFY_TPM_ENROLLED=1
#       Asserts that the first boot enrolled a systemd-tpm2 token in the LUKS
#       header and removed the transient key.
#   VERIFY_TPM_AUTOUNLOCK=1
#       For a boot with no LUKS_PASSPHRASE: the guest must reach SSH with no
#       passphrase typed, so only the TPM can have unlocked the root.

set -e

# Source tool discovery helpers
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./find-tools.sh
source "$SCRIPT_DIR/find-tools.sh"
# shellcheck source=./guest-checks.sh
source "$SCRIPT_DIR/guest-checks.sh"

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
  # swtpm --terminate exits with QEMU; this covers a QEMU that never
  # connected. The state directory is the caller's and is left alone.
  if [ -n "$SWTPM_PID_FILE" ] && sudo test -f "$SWTPM_PID_FILE"; then
    sudo kill "$(sudo cat "$SWTPM_PID_FILE")" 2>/dev/null || true
  fi
}
trap cleanup_temp EXIT

# Optional devices. QEMU_EXTRA_ARGS stays empty for a plain boot check.
QEMU_EXTRA_ARGS=()

if [ -n "${EXTRA_DISK:-}" ]; then
  if [ ! -b "$EXTRA_DISK" ]; then
    echo "❌ EXTRA_DISK=$EXTRA_DISK is not a block device"
    exit 1
  fi
  echo "Extra disk: $EXTRA_DISK (second AHCI disk)"
  QEMU_EXTRA_ARGS+=(
    -drive "file=$EXTRA_DISK,format=raw,if=none,id=disk1"
    -device "ide-hd,drive=disk1,bus=ahci0.1"
  )
fi

SWTPM_PID_FILE=""
if [ -n "${TPM_STATE_DIR:-}" ]; then
  if ! command -v swtpm >/dev/null 2>&1; then
    echo "❌ TPM_STATE_DIR is set but swtpm is not installed (apt: swtpm)"
    exit 1
  fi
  sudo mkdir -p "$TPM_STATE_DIR"
  TPM_SOCK="$TPM_STATE_DIR/swtpm.sock"
  SWTPM_PID_FILE="$TPM_STATE_DIR/swtpm.pid"
  sudo rm -f "$TPM_SOCK" "$SWTPM_PID_FILE"
  # --terminate: swtpm exits when QEMU closes the socket. The state directory
  # keeps the TPM's seeds, so a later boot with the same directory is the
  # same TPM, and a key sealed on the first boot unseals on the second.
  sudo swtpm socket --tpm2 \
    --tpmstate "dir=$TPM_STATE_DIR" \
    --ctrl "type=unixio,path=$TPM_SOCK" \
    --pid "file=$SWTPM_PID_FILE" \
    --log "file=$ARTIFACTS_DIR/swtpm-$RUN_TAG.log,level=1" \
    --terminate --daemon
  for _ in $(seq 1 50); do
    sudo test -S "$TPM_SOCK" && break
    sleep 0.1
  done
  if ! sudo test -S "$TPM_SOCK"; then
    echo "❌ swtpm did not create its control socket at $TPM_SOCK"
    sudo cat "$ARTIFACTS_DIR/swtpm-$RUN_TAG.log" 2>/dev/null || true
    exit 1
  fi
  echo "swtpm: TPM 2.0 emulator running (state: $TPM_STATE_DIR)"
  QEMU_EXTRA_ARGS+=(
    -chardev "socket,id=chrtpm,path=$TPM_SOCK"
    -tpmdev "emulator,id=tpm0,chardev=chrtpm"
    -device "tpm-tis,tpmdev=tpm0"
  )
fi

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
  "${QEMU_EXTRA_ARGS[@]}" \
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
SSH_WAIT_ITERS=180  # 360s: observed boots landing at 246-253s vs the prior 240s budget (issue #193)
echo "Waiting for VM to boot and SSH to be ready (up to $((SSH_WAIT_ITERS * 2))s)..."
SSH_READY=0
# Keep the stderr of the most recent probe. Discarding it left "SSH connection
# failed (timeout)" indistinguishable between "port forward never answered"
# and "sshd answered and rejected the key" — which is what every post-#172
# failure actually was.
SSH_ERR_LOG="$ARTIFACTS_DIR/ssh-probe-stderr-$RUN_TAG.log"
for i in $(seq 1 "$SSH_WAIT_ITERS"); do
  sleep 2
  if ssh_root "echo OK" 2>"$SSH_ERR_LOG"; then
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
    echo "  Waiting... ($i/$SSH_WAIT_ITERS)"
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

  echo ""
  echo "=== Last SSH probe stderr ==="
  cat "$SSH_ERR_LOG" 2>/dev/null || echo "(none captured)"
  # One more attempt with client debugging on, so the log shows whether the
  # TCP connection was refused/timed out or sshd replied and declined the key
  # ("Permission denied (publickey)", "Authentications that can continue").
  echo ""
  echo "=== Final SSH probe (verbose) ==="
  ssh_root -v "echo OK" > "$ARTIFACTS_DIR/ssh-probe-verbose-$RUN_TAG.log" 2>&1 || true
  tail -40 "$ARTIFACTS_DIR/ssh-probe-verbose-$RUN_TAG.log" 2>/dev/null || true

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

# ── Optional assertions (see the header) ─────────────────────────────────
# Each one sets CHECK_FAIL and keeps going, so one run reports every failed
# assertion and still captures the journal below.
CHECK_FAIL=0
check_fail() {
  echo "❌ FAIL: $*"
  CHECK_FAIL=1
}

if [ -n "${VERIFY_USER:-}" ]; then
  echo ""
  echo "=== User account verification ($VERIFY_USER) ==="
  PASSWD_LINE=$(ssh_root "getent passwd '$VERIFY_USER'" 2>/dev/null || true)
  if [ -z "$PASSWD_LINE" ]; then
    check_fail "user '$VERIFY_USER' does not exist in the booted system (getent passwd)"
    ssh_root "tail -5 /etc/passwd" 2>/dev/null || true
  else
    echo "✅ account exists: $PASSWD_LINE"
    USER_HOME=$(printf '%s' "$PASSWD_LINE" | cut -d: -f6)
    HOME_OWNER=$(ssh_root "stat -c %U '$USER_HOME'" 2>/dev/null || true)
    if [ "$HOME_OWNER" = "$VERIFY_USER" ]; then
      echo "✅ home directory $USER_HOME exists and belongs to $VERIFY_USER"
    else
      check_fail "home directory $USER_HOME is missing or not owned by $VERIFY_USER (owner: '${HOME_OWNER:-none}')"
    fi

    USER_GROUPS=$(ssh_root "id -nG '$VERIFY_USER'" 2>/dev/null || true)
    echo "groups: $USER_GROUPS"
    IFS=',' read -ra WANT_GROUPS <<< "${VERIFY_USER_GROUPS:-}"
    for g in "${WANT_GROUPS[@]}"; do
      [ -n "$g" ] || continue
      if printf '%s' "$USER_GROUPS" | tr ' ' '\n' | grep -qx "$g"; then
        echo "✅ member of group $g"
      else
        check_fail "user '$VERIFY_USER' is not in group '$g'"
      fi
    done

    # A login session: the shell is valid and the home is reachable.
    SESSION_USER=$(ssh_root "runuser -l '$VERIFY_USER' -c 'id -un'" 2>/dev/null | tail -1 || true)
    if [ "$SESSION_USER" = "$VERIFY_USER" ]; then
      echo "✅ login session opens for $VERIFY_USER"
    else
      check_fail "could not open a login session for '$VERIFY_USER' (runuser -l printed '${SESSION_USER}')"
    fi

    # The password. /etc/shadow must hold a real hash, and the recipe's
    # password must authenticate against it.
    SHADOW_FIELD=$(ssh_root "awk -F: -v u='$VERIFY_USER' '\$1 == u { print \$2 }' /etc/shadow" 2>/dev/null || true)
    SHADOW_STATE=$(classify_shadow_hash "$SHADOW_FIELD")
    if [ "$SHADOW_STATE" != "ok" ]; then
      check_fail "/etc/shadow entry for '$VERIFY_USER' is '$SHADOW_STATE', not a password hash — nobody can sign in with the recipe's password"
    else
      echo "✅ /etc/shadow holds a password hash (scheme ${SHADOW_FIELD:0:3})"
      # unix_chkpwd is pam_unix's own helper: the same code path a login
      # takes, against the guest's libcrypt. It reads a NUL-terminated
      # password on stdin and refuses a terminal, which SSH without -t is not.
      CHKPWD=$(ssh_root "command -v unix_chkpwd || ls /usr/sbin/unix_chkpwd /sbin/unix_chkpwd 2>/dev/null | head -1" 2>/dev/null | head -1 || true)
      if [ -n "$CHKPWD" ]; then
        if printf '%s\0' "$VERIFY_USER_PASSWORD" | ssh_root "'$CHKPWD' '$VERIFY_USER' nullok" 2>/dev/null; then
          echo "✅ the recipe's password authenticates (pam_unix unix_chkpwd)"
        else
          check_fail "the recipe's password does not authenticate for '$VERIFY_USER' (unix_chkpwd)"
        fi
        if printf '%s\0' "not-${VERIFY_USER_PASSWORD}" | ssh_root "'$CHKPWD' '$VERIFY_USER' nullok" 2>/dev/null; then
          check_fail "a wrong password authenticates for '$VERIFY_USER' — the password check proves nothing"
        else
          echo "✅ a wrong password is rejected"
        fi
      else
        # No pam_unix helper in the guest: recompute the hash on this host.
        HASH_RC=0
        verify_sha512crypt "$SHADOW_FIELD" "$VERIFY_USER_PASSWORD" || HASH_RC=$?
        case "$HASH_RC" in
          0) echo "✅ the recipe's password produces the stored hash (openssl, sha512crypt)" ;;
          1) check_fail "the recipe's password does not produce the hash stored for '$VERIFY_USER'" ;;
          *) check_fail "cannot verify the password for '$VERIFY_USER': no unix_chkpwd in the guest and the hash is not default-rounds sha512crypt" ;;
        esac
      fi
    fi
  fi
fi

if [ "${VERIFY_VAR_DISK:-0}" = "1" ]; then
  echo ""
  echo "=== /var disk verification ==="
  ssh_root "lsblk -o NAME,SIZE,TYPE,FSTYPE,LABEL,MOUNTPOINTS; echo; findmnt /var; echo; cat /etc/fstab" 2>/dev/null || true
  VAR_SRC=$(ssh_root "findmnt -n -o SOURCE --target /var" 2>/dev/null | tail -1 || true)
  VAR_FSTYPE=$(ssh_root "findmnt -n -o FSTYPE --target /var" 2>/dev/null | tail -1 || true)
  VAR_LABEL=$(ssh_root "findmnt -n -o LABEL --target /var" 2>/dev/null | tail -1 || true)
  ROOT_SRC=$(ssh_root "findmnt -n -o SOURCE /sysroot 2>/dev/null || findmnt -n -o SOURCE /" 2>/dev/null | tail -1 || true)
  if VAR_REASON=$(check_var_on_own_disk "$VAR_SRC" "$VAR_FSTYPE" "$VAR_LABEL" "$ROOT_SRC" 2>&1); then
    echo "✅ /var is $VAR_SRC ($VAR_FSTYPE, label $VAR_LABEL); root is $ROOT_SRC"
  else
    check_fail "the var disk is not mounted at /var: $VAR_REASON"
  fi
fi

if [ "${VERIFY_TPM_ENROLLED:-0}" = "1" ] || [ "${VERIFY_TPM_AUTOUNLOCK:-0}" = "1" ]; then
  echo ""
  echo "=== TPM2 verification ==="
  if ! ssh_root "test -e /dev/tpmrm0" 2>/dev/null; then
    check_fail "the guest has no /dev/tpmrm0 — the harness did not attach a TPM (TPM_STATE_DIR='${TPM_STATE_DIR:-}')"
  fi
  LUKS_DEV=$(ssh_root "blkid -t TYPE=crypto_LUKS -o device | head -1" 2>/dev/null || true)
  if [ -z "$LUKS_DEV" ]; then
    check_fail "no crypto_LUKS device in the guest"
  else
    # The first-boot oneshot is ordered after basic.target and may still be
    # running when sshd first answers.
    TOKEN_OK=0
    for _ in $(seq 1 30); do
      LUKS_DUMP=$(ssh_root "cryptsetup luksDump '$LUKS_DEV'" 2>/dev/null || true)
      if has_tpm2_token "$LUKS_DUMP"; then
        TOKEN_OK=1
        break
      fi
      sleep 4
    done
    if [ "$TOKEN_OK" -eq 1 ]; then
      echo "✅ systemd-tpm2 token enrolled on $LUKS_DEV"
    else
      check_fail "no systemd-tpm2 token on $LUKS_DEV after 120s — first-boot TPM2 enrolment did not run"
      echo "--- luksDump (tokens and keyslots) ---"
      printf '%s\n' "$LUKS_DUMP" | sed -n '/^Keyslots:/,$p' | grep -vE '^\s+(Salt|Digest|AF |Area|PBKDF|Time|Memory|Threads|Iterations)' | head -40
      echo "--- fisherman-tpm2-enroll.service ---"
      ssh_root "systemctl status fisherman-tpm2-enroll.service --no-pager; ls -la /etc/fisherman /usr/lib/systemd/system/fisherman-tpm2-enroll.service" 2>&1 || true
    fi
    if ssh_root "test -e /etc/fisherman/tpm2-enroll.key" 2>/dev/null; then
      check_fail "/etc/fisherman/tpm2-enroll.key still exists — the transient unlock key was not removed"
    else
      echo "✅ no transient unlock key at /etc/fisherman/tpm2-enroll.key"
    fi
  fi
  if [ "${VERIFY_TPM_AUTOUNLOCK:-0}" = "1" ]; then
    if [ -n "$LUKS_PASSPHRASE" ]; then
      check_fail "VERIFY_TPM_AUTOUNLOCK=1 with a LUKS_PASSPHRASE proves nothing: the injector may have unlocked the disk"
    else
      echo "✅ the guest reached SSH with no passphrase typed: the TPM unlocked the root"
    fi
  fi
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

if [ "$CHECK_FAIL" -ne 0 ]; then
  echo "❌ FAIL: one or more guest assertions failed (see ❌ lines above)"
  exit 1
fi

if [ "${EFIBOOT_FAIL:-0}" = "1" ]; then
  echo "❌ FAIL: UEFI boot entry assertion failed (see efibootmgr output above)"
  exit 1
fi

echo "✅ VM verification complete"
