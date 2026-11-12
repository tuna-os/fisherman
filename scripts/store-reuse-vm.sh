#!/bin/bash
# scripts/store-reuse-vm.sh PROFILE_JSON
#
# The disposable offline VM profile for the bootc store reuse (#384 phase 2,
# docs/SOURCE-REUSE.md). ADVISORY: bootcrew-vm.yml runs it with
# continue-on-error, outside the required canaries.
#
#   1. Existing path: fisherman on the runner installs the profile's image
#      from the registry onto disk A (timed; the baseline).
#   2. Disk A boots in QEMU with a blank disk B and no network except the
#      SSH forward (slirp restrict=on): no registry is reachable, which the
#      profile proves before it starts.
#   3. Inside the VM, as root: a sourceReuse recipe for the booted image's
#      digest validates; one for a digest the host does not have is refused
#      by validate and by the install, with disk B untouched; an install that
#      is cancelled during the copy leaves no copy, no /var/tmp bind and no
#      scratch; then the install from the VM's own bootc store onto disk B.
#   4. Disk B boots (boot-verify.sh, receipt checks required) and must boot
#      the verified copy, follow the original update reference and be the
#      item's platform. The capability report taken in step 3 must match
#      the bootc, backend, bootloader and kernel that disk B shows, and a
#      report for a digest the VM did not boot must answer nothing (#383,
#      scripts/vm-checks/capabilities.sh).
#   5. The job summary compares copy time, scratch and disk with step 1.
#
# PROFILE_JSON: {"name", "ssh_image_name", "filesystem", "vm_memory",
# "vm_timeout"}. Env: ARTIFACTS_DIR, FISHERMAN_BIN (default /tmp/fisherman),
# GITHUB_STEP_SUMMARY. Needs root via sudo, KVM, qemu, OVMF, jq, python3.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./find-tools.sh
source "$SCRIPT_DIR/find-tools.sh"
# shellcheck source=./store-reuse-lib.sh
source "$SCRIPT_DIR/store-reuse-lib.sh"

PROFILE="${1:?PROFILE_JSON required}"
NAME=$(jq -r '.name' <<<"$PROFILE")
SSH_NAME=$(jq -r '.ssh_image_name // .name' <<<"$PROFILE")
FILESYSTEM=$(jq -r '.filesystem // "xfs"' <<<"$PROFILE")
VM_MEMORY=$(jq -r '.vm_memory // "4G"' <<<"$PROFILE")
VM_TIMEOUT=$(jq -r '.vm_timeout // 3000' <<<"$PROFILE")
# The reuse refuses composefs, so a profile is ostree unless it says otherwise.
BACKEND=$([ "$(jq -r '.composefs_backend // false' <<<"$PROFILE")" = true ] && echo composefs || echo ostree)
IMAGE="ghcr.io/tuna-os/fisherman/${SSH_NAME}:ssh-enabled"
FM="${FISHERMAN_BIN:-/tmp/fisherman}"
SSH_KEY=/tmp/bootcrew-ssh/id_rsa
ART="${ARTIFACTS_DIR:-/tmp/bootcrew-artifacts}/store-reuse-$NAME"
DISK_A=/tmp/store-reuse-a.img
DISK_B=/tmp/store-reuse-b.img
SSH_PORT=2223
mkdir -p "$ART"

QEMU_BIN=$(find_qemu)
OVMF_PATH=$(find_ovmf)
OVMF_CODE="${OVMF_PATH%:*}"
OVMF_VARS="${OVMF_PATH#*:}"

LOOP=""
QEMU_PID=""
FAILURES=()
note_fail() { FAILURES+=("$1"); echo "❌ $1"; }

cleanup() {
  if [ -n "$QEMU_PID" ]; then sudo kill "$QEMU_PID" 2>/dev/null || true; fi
  if [ -n "$LOOP" ]; then sudo losetup -d "$LOOP" 2>/dev/null || true; fi
  sudo rm -f "$DISK_A" "$DISK_B" /tmp/store-reuse-vars.fd /tmp/store-reuse-mon.sock 2>/dev/null || true
}
trap cleanup EXIT

now_ms() { date +%s%3N; }

# root_used LOOPDEV — bytes used on the installed root (last partition).
root_used() {
  local dev="$1" n mnt used
  n=$(lsblk "$dev" -o NAME -nr | grep -c "^${dev##*/}p[0-9]" || true)
  mnt=$(mktemp -d)
  if sudo mount -o ro "${dev}p${n}" "$mnt" 2>/dev/null; then
    used=$(df -B1 --output=used "$mnt" | tail -1 | tr -d ' ')
    sudo umount "$mnt"
  fi
  rmdir "$mnt"
  echo "${used:-}"
}

# patch_bls DEV — serial console and SELinux permissive in every BLS entry,
# for the same reasons the bootcrew-ci-test recipe gives (the justfile).
patch_bls() {
  local dev="$1" part mnt conf
  for part in "${dev}p1" "${dev}p2"; do
    [ "$(sudo blkid -s TYPE -o value "$part" 2>/dev/null || true)" = crypto_LUKS ] && continue
    mnt=$(mktemp -d)
    if sudo mount "$part" "$mnt" 2>/dev/null; then
      for conf in "$mnt"/loader/entries/*.conf; do
        [ -f "$conf" ] || continue
        sudo grep -q "console=ttyS0" "$conf" || sudo sed -i 's/^options /options console=ttyS0,115200 console=tty0 /' "$conf"
        sudo grep -Eq "selinux=0|enforcing=0" "$conf" || sudo sed -i 's/^options /options enforcing=0 /' "$conf"
      done
      sudo umount "$mnt"
    fi
    rmdir "$mnt"
  done
}

ssh_vm() {
  ssh -i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=5 -o ServerAliveInterval=15 -p "$SSH_PORT" root@127.0.0.1 "$@"
}

# ── 1. Existing path ────────────────────────────────────────────────────────
echo "=== 1. Existing path: install $IMAGE from the registry onto disk A ==="
truncate -s 80G "$DISK_A"
LOOP=$(sudo losetup --find --show --partscan "$DISK_A")
cat >"$ART/recipe-existing.json" <<EOF
{"disk": "$LOOP", "filesystem": "$FILESYSTEM", "encryption": {"type": "none"},
 "image": "$IMAGE", "hostname": "reuse-host", "flatpaks": []}
EOF
sudo df -B1 --output=avail /var | tail -1 | tr -d ' ' >"$ART/existing-var-avail-before"
T0=$(now_ms)
# The event stream belongs to the runner user: sudo runs fisherman only.
# shellcheck disable=SC2024
sudo "$FM" "$ART/recipe-existing.json" >"$ART/existing-events.ndjson" 2>"$ART/existing-stderr.log"
EXISTING_MS=$(( $(now_ms) - T0 ))
EXISTING_USED=$(root_used "$LOOP")
bash "$SCRIPT_DIR/enable-ssh-installed.sh" "$LOOP" false "${SSH_KEY}.pub"
patch_bls "$LOOP"
sudo losetup -d "$LOOP"
LOOP=""

# ── 2. Boot disk A offline, with blank disk B ───────────────────────────────
echo "=== 2. Boot disk A with blank disk B, no network but the SSH forward ==="
truncate -s 60G "$DISK_B"
cp "$OVMF_VARS" /tmp/store-reuse-vars.fd
# shellcheck disable=SC2024 # the logs belong to the runner user
sudo timeout "$VM_TIMEOUT" "$QEMU_BIN" -machine q35 -enable-kvm -cpu host -smp 2 -m "$VM_MEMORY" \
  -device ahci,id=ahci0 \
  -drive file="$DISK_A",format=raw,if=none,id=disk0 -device ide-hd,drive=disk0,bus=ahci0.0,bootindex=1 \
  -drive file="$DISK_B",format=raw,if=none,id=disk1 -device virtio-blk-pci,drive=disk1,serial=fmreuse \
  -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
  -drive if=pflash,format=raw,file=/tmp/store-reuse-vars.fd \
  -netdev user,id=net0,restrict=on,hostfwd=tcp:127.0.0.1:"$SSH_PORT"-:22 -device virtio-net-pci,netdev=net0 \
  -monitor unix:/tmp/store-reuse-mon.sock,server=on,wait=off \
  -serial file:"$ART/serial-a.log" -display none -no-reboot >"$ART/qemu-a.log" 2>&1 &
QEMU_PID=$!
for i in $(seq 1 180); do
  ssh_vm true 2>/dev/null && break
  sleep 2
  [ "$i" = 180 ] && { note_fail "disk A did not answer on SSH"; exit 1; }
done
echo "✅ disk A booted"

# ── 3. Inside the VM ────────────────────────────────────────────────────────
ssh_vm "bootc status --json" >"$ART/status-a.json"
SOURCE_DIGEST=$(booted_field "$ART/status-a.json" digest)
PLATFORM=$(booted_field "$ART/status-a.json" arch)
TARGET_REF=$(booted_field "$ART/status-a.json" image)
echo "booted: $TARGET_REF $SOURCE_DIGEST ($PLATFORM)"
if [ -z "$SOURCE_DIGEST" ] || [ -z "$PLATFORM" ]; then
  note_fail "disk A's bootc status names no digest/platform"
  exit 1
fi

# Offline proof: the registry, and the internet, are unreachable.
OFFLINE=yes
if ssh_vm "curl -sS -m 10 -o /dev/null https://quay.io/v2/" 2>/dev/null; then OFFLINE=no; fi
if ssh_vm "timeout 60 skopeo inspect --retry-times 0 docker://$IMAGE" >/dev/null 2>&1; then OFFLINE=no; fi
if [ "$OFFLINE" = yes ]; then
  echo "✅ offline: no registry is reachable from the VM"
else
  note_fail "the VM reached a registry; the profile is not offline"
fi

# Everything the profile puts on the VM lives in $G, never under /var/tmp:
# the copy and the install bind other directories over /var/tmp while they
# run, which would hide the event streams the profile polls.
G=/var/lib/fisherman-reuse-profile
ssh_vm "mkdir -p $G && cat > $G/fisherman && chmod 0755 $G/fisherman" <"$FM"
ssh_vm "$G/fisherman probe --json" >"$ART/probe-a.json" || true
jq '.bootc_store' "$ART/probe-a.json" >"$ART/bootc-store-a.json" 2>/dev/null || true

# The capability report (#383), taken where the install runs: for the booted
# image pinned by its digest, and for the same name pinned by a digest this
# host did not boot (the negative case). scripts/vm-checks/capabilities.sh
# holds both to the installed system after step 4.
CAPS_REF="${TARGET_REF%%@*}"
ssh_vm "$G/fisherman probe --json --source $CAPS_REF@$SOURCE_DIGEST --target-imgref $TARGET_REF" \
  >"$ART/probe-caps.json" 2>"$ART/probe-caps.err" || true
ssh_vm "$G/fisherman probe --json --source $CAPS_REF@sha256:$(printf 'e%.0s' $(seq 1 64)) --target-imgref $TARGET_REF" \
  >"$ART/probe-caps-other.json" 2>"$ART/probe-caps-other.err" || true
ssh_vm "bootc --version" 2>/dev/null | head -n 1 >"$ART/bootc-version-a.txt" || true
jq '.capabilities.execution_root' "$ART/probe-caps.json" 2>/dev/null || echo "⚠️  no capability report"

recipe() { # recipe DIGEST
  cat <<EOF
{"disk": "/dev/disk/by-id/virtio-fmreuse", "filesystem": "$FILESYSTEM", "encryption": {"type": "none"},
 "targetImgref": "$TARGET_REF", "experimental": ["bootc_store_reuse"],
 "sourceReuse": {"store": "bootc", "digest": "$1", "platform": "$PLATFORM"},
 "hostname": "reuse-target", "flatpaks": []}
EOF
}
recipe "$SOURCE_DIGEST" >"$ART/recipe-reuse.json"
recipe "sha256:0000000000000000000000000000000000000000000000000000000000000000" >"$ART/recipe-missing.json"
ssh_vm "cat > $G/recipe-reuse.json" <"$ART/recipe-reuse.json"
ssh_vm "cat > $G/recipe-missing.json" <"$ART/recipe-missing.json"

# disk_b_blank — disk B has no partition table and no filesystem.
disk_b_blank() { ! ssh_vm "blkid -p /dev/disk/by-id/virtio-fmreuse" >/dev/null 2>&1; }
# reuse_leftovers — print what a reuse left behind on the VM (nothing: ok).
reuse_leftovers() {
  ssh_vm 'podman images --format "{{.Repository}}:{{.Tag}}" | grep "^localhost/fisherman-reuse" || true
          test -e /var/fisherman-tmp/source-reuse && echo scratch:/var/fisherman-tmp/source-reuse
          findmnt -n -o SOURCE /var/tmp | grep fisherman && echo bind:/var/tmp || true'
}
# run_unit NAME RECIPE — run `fisherman -` as a transient unit (so neither
# the SSH session nor its end is fisherman's parent); prints nothing.
run_unit() {
  ssh_vm "systemctl reset-failed $1 2>/dev/null; systemd-run --unit=$1 --quiet \
    -p StandardInput=file:$2 -p StandardOutput=file:$G/$1.ndjson -p StandardError=file:$G/$1.err \
    $G/fisherman -"
}
wait_unit() { # wait_unit NAME SECONDS → exit status of fisherman
  local i
  for i in $(seq 1 "$2"); do
    ssh_vm "systemctl is-active --quiet $1" 2>/dev/null || break
    sleep 1
  done
  ssh_vm "systemctl show -p ExecMainStatus --value $1"
}

# 3a. validate: the item validates; a digest the host lacks does not.
if ssh_vm "$G/fisherman validate --json - < $G/recipe-reuse.json" >"$ART/validate-reuse.json"; then
  echo "✅ validate: the booted image's digest is a valid sourceReuse"
else
  note_fail "validate refused the booted image's digest: $(cat "$ART/validate-reuse.json")"
fi
ssh_vm "$G/fisherman validate --json - < $G/recipe-missing.json" >"$ART/validate-missing.json" || true
if grep -q source_reuse_item_not_found "$ART/validate-missing.json"; then
  echo "✅ validate: a digest the host lacks is source_reuse_item_not_found"
else
  note_fail "validate did not refuse a missing digest: $(cat "$ART/validate-missing.json")"
fi

# 3b. The install refuses the missing digest before touching anything.
run_unit fm-missing $G/recipe-missing.json
MISSING_STATUS=$(wait_unit fm-missing 120) || true
ssh_vm "cat $G/fm-missing.ndjson" >"$ART/missing-events.ndjson" || true
if [ "$MISSING_STATUS" = 1 ] && [ "$(event_field "$ART/missing-events.ndjson" error code)" = source_reuse_item_not_found ] \
  && disk_b_blank && [ -z "$(reuse_leftovers)" ]; then
  echo "✅ install: a missing digest is refused with disk B untouched and nothing left behind"
else
  note_fail "missing digest: exit $MISSING_STATUS, code $(event_field "$ART/missing-events.ndjson" error code), leftovers $(reuse_leftovers)"
fi

# 3c. A cancel during the copy cleans up.
run_unit fm-cancel $G/recipe-reuse.json
CANCELLED=no
for _ in $(seq 1 600); do
  if ssh_vm "grep -q '\"phase\":\"copying\"' $G/fm-cancel.ndjson" 2>/dev/null; then
    sleep 1
    ssh_vm "systemctl kill --kill-whom=main --signal=SIGTERM fm-cancel" || true
    CANCELLED=yes
    break
  fi
  ssh_vm "systemctl is-active --quiet fm-cancel" 2>/dev/null || break
  sleep 1
done
CANCEL_STATUS=$(wait_unit fm-cancel 300) || true
ssh_vm "cat $G/fm-cancel.ndjson" >"$ART/cancel-events.ndjson" || true
LEFT=$(reuse_leftovers) || true
if [ "$CANCELLED" = yes ] && [ "$CANCEL_STATUS" = 130 ] && [ -z "$LEFT" ] && disk_b_blank \
  && [ "$(reuse_field "$ART/cancel-events.ndjson" released reason)" = removed ]; then
  echo "✅ cancel during the copy: exit 130, copy removed, no bind, no scratch, disk B untouched"
else
  note_fail "cancel: signalled=$CANCELLED exit=$CANCEL_STATUS leftovers='$LEFT' released=$(reuse_field "$ART/cancel-events.ndjson" released reason)"
fi

# 3d. The install from the VM's own bootc store.
VAR_BEFORE=$(ssh_vm "df -B1 --output=avail /var | tail -1" | tr -d ' ') || true
run_unit fm-reuse $G/recipe-reuse.json
REUSE_STATUS=$(wait_unit fm-reuse 2400) || true
ssh_vm "cat $G/fm-reuse.ndjson" >"$ART/reuse-events.ndjson" || true
ssh_vm "cat $G/fm-reuse.err" >"$ART/reuse-stderr.log" 2>/dev/null || true
VAR_AFTER=$(ssh_vm "df -B1 --output=avail /var | tail -1" | tr -d ' ') || true
LEFT=$(reuse_leftovers) || true
if [ "$REUSE_STATUS" = 0 ] && [ -n "$(event_field "$ART/reuse-events.ndjson" complete message)" ]; then
  echo "✅ install from the VM's own bootc store onto disk B"
else
  note_fail "the reuse install failed: exit $REUSE_STATUS, $(event_field "$ART/reuse-events.ndjson" error code): $(event_field "$ART/reuse-events.ndjson" error message)"
fi
if [ -z "$LEFT" ]; then
  echo "✅ after the install: no copy, no bind, no scratch left"
else
  note_fail "after the install: leftovers '$LEFT'"
fi
REUSE_TOTAL_MS=$(event_field "$ART/reuse-events.ndjson" complete elapsed_ms)

ssh_vm "systemctl poweroff" 2>/dev/null || true
for _ in $(seq 1 60); do sudo kill -0 "$QEMU_PID" 2>/dev/null || break; sleep 2; done
sudo kill "$QEMU_PID" 2>/dev/null || true
QEMU_PID=""

# ── 4. Boot disk B and check its identity ───────────────────────────────────
BOOT_OK=no
IDENTITY=fail
REUSE_USED=""
if [ "$REUSE_STATUS" = 0 ]; then
  echo "=== 4. Boot disk B ==="
  LOOP=$(sudo losetup --find --show --partscan "$DISK_B")
  REUSE_USED=$(root_used "$LOOP")
  bash "$SCRIPT_DIR/enable-ssh-installed.sh" "$LOOP" false "${SSH_KEY}.pub"
  patch_bls "$LOOP"
  if ARTIFACTS_DIR="$ART/boot-b" BOOTCREW_RECEIPT_REQUIRED=1 FISHERMAN_BIN="$FM" \
    BOOTCREW_GUEST_CHECKS="$SCRIPT_DIR/vm-checks/capabilities.sh" \
    CAPS_PROBE="$ART/probe-caps.json" CAPS_PROBE_OTHER="$ART/probe-caps-other.json" \
    CAPS_LIVE_BOOTC="$(cat "$ART/bootc-version-a.txt" 2>/dev/null)" CAPS_SOURCE_DIGEST="$SOURCE_DIGEST" \
    CAPS_EXPECT_BACKEND="$BACKEND" \
    bash "$SCRIPT_DIR/boot-verify.sh" 2222 "$SSH_KEY" 900 2G "$LOOP" "$NAME-b" ""; then
    BOOT_OK=yes
  else
    note_fail "disk B did not boot, verify its receipt, or pass the capability check (boot-verify.sh)"
  fi
  RECEIPT_DIR=$(find "$ART/boot-b" -maxdepth 1 -type d -name 'receipt-*' 2>/dev/null | head -1 || true)
  if [ -n "$RECEIPT_DIR" ] && store_reuse_identity "$ART/reuse-events.ndjson" "$RECEIPT_DIR/install-receipt.json" \
      "$RECEIPT_DIR/boot-receipt/bootc-status.json" "$TARGET_REF" "$PLATFORM" "$SOURCE_DIGEST"; then
    IDENTITY=ok
  else
    note_fail "disk B's identity does not match the verified copy"
  fi
  sudo losetup -d "$LOOP"
  LOOP=""
fi

# ── 5. Summary ──────────────────────────────────────────────────────────────
EV="$ART/reuse-events.ndjson"
{
  echo "### bootc store reuse — $NAME (advisory, #384)"
  echo ""
  echo "Item: \`$TARGET_REF\` \`$SOURCE_DIGEST\` ($PLATFORM). VM offline: **$OFFLINE**."
  echo ""
  echo "| | existing path (runner, registry) | store reuse (VM, offline) |"
  echo "|---|---|---|"
  echo "| install wall time | $(ms_s "$EXISTING_MS") | $(ms_s "$REUSE_TOTAL_MS") |"
  echo "| copy-to-storage time | - | $(ms_s "$(reuse_field "$EV" verified measured.copy_ms)") |"
  echo "| scratch estimate | - | $(bytes_gib "$(reuse_field "$EV" planned estimate.scratch_bytes)") |"
  echo "| store estimate | - | $(bytes_gib "$(reuse_field "$EV" planned estimate.store_bytes)") |"
  echo "| peak scratch drop during copy | - | $(bytes_gib "$(reuse_field "$EV" verified measured.scratch_peak_bytes)") |"
  echo "| containers-storage held by the copy | - | $(bytes_gib "$(reuse_field "$EV" verified measured.store_bytes)") |"
  echo "| live host /var change over the install | - | $(bytes_gib "$(( ${VAR_BEFORE:-0} - ${VAR_AFTER:-0} ))") |"
  echo "| installed root used | $(bytes_gib "$EXISTING_USED") | $(bytes_gib "$REUSE_USED") |"
  echo ""
  echo "Copy digest \`$(reuse_field "$EV" verified destination_digest)\` (bootc re-serialises the layers, so it is not the item's digest)."
  echo "Refusal with disk untouched: see log. Cancel cleanup: $([ "$CANCELLED" = yes ] && echo exercised || echo NOT exercised). Boot: $BOOT_OK. Identity: $IDENTITY."
  if [ "${#FAILURES[@]}" -gt 0 ]; then
    echo ""
    printf -- '- ❌ %s\n' "${FAILURES[@]}"
  fi
} | tee "$ART/summary.md" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"

[ "${#FAILURES[@]}" -eq 0 ]
