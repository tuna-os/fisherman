#!/bin/bash
# tests/check-validation.sh
#
# Validates that the E2E checks in verify-installation.sh and boot-verify.sh
# actually catch the bugs they are designed to detect.
#
# Runs entirely in /tmp — no real disk/VM needed.
#
# Exit codes: 0 = all tests passed, 1 = one or more tests failed.

set -uo pipefail

PASS=0
FAIL=0

# ── helpers ────────────────────────────────────────────────────────────────

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }

# expect_exit NAME EXPECTED_EXIT CMD...
#   Runs CMD and asserts the exit code equals EXPECTED_EXIT.
expect_exit() {
  local name="$1" want="$2"
  shift 2
  local got=0
  "$@" >/dev/null 2>&1 || got=$?
  if [ "$got" -eq "$want" ]; then
    green "  PASS  $name"
    PASS=$((PASS + 1))
  else
    red   "  FAIL  $name  (exit $got, want $want)"
    FAIL=$((FAIL + 1))
  fi
}

# ── installer-app absence check (from verify-installation.sh) ──────────────
#
# The check is pure filesystem logic; we inline it here so we can set ROOT_DIR
# to a temp directory without mounting any real partitions.

INSTALLER_IDS="org.bootcinstaller.Installer org.bootcinstaller.Installer.Devel org.tunaos.Installer org.tunaos.Installer.Devel"

# check_installer_apps ROOT_DIR COMPOSEFS
#   Returns 0 (no installer apps) or 1 (installer apps found) — mirrors the
#   exact logic in verify-installation.sh so changes there are reflected here.
check_installer_apps() {
  local root_dir="$1" composefs="$2"
  local flatpak_app_dir

  if [ "$composefs" = "true" ]; then
    flatpak_app_dir="$root_dir/state/os/default/var/lib/flatpak/app"
  else
    flatpak_app_dir="$root_dir/ostree/deploy/default/var/lib/flatpak/app"
  fi

  [ -d "$flatpak_app_dir" ] || return 0   # no flatpak dir — nothing to check

  local found=""
  for appid in $INSTALLER_IDS; do
    [ -d "$flatpak_app_dir/$appid" ] && found="$found $appid"
  done

  [ -z "$found" ] && return 0
  return 1
}

echo ""
echo "━━━ installer-app absence check ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Test 1 – ostree layout, installer app present → check must FAIL (exit 1)
D=$(mktemp -d)
mkdir -p "$D/ostree/deploy/default/var/lib/flatpak/app/org.tunaos.Installer"
expect_exit "ostree: installer app present → must be caught (exit 1)" 1 \
  check_installer_apps "$D" "false"
rm -rf "$D"

# Test 2 – ostree layout, no installer apps present → check must PASS (exit 0)
D=$(mktemp -d)
mkdir -p "$D/ostree/deploy/default/var/lib/flatpak/app/org.mozilla.firefox"
expect_exit "ostree: only user app present → must pass (exit 0)" 0 \
  check_installer_apps "$D" "false"
rm -rf "$D"

# Test 3 – composefs layout, installer app present → check must FAIL (exit 1)
D=$(mktemp -d)
mkdir -p "$D/state/os/default/var/lib/flatpak/app/org.bootcinstaller.Installer"
expect_exit "composefs: installer app present → must be caught (exit 1)" 1 \
  check_installer_apps "$D" "true"
rm -rf "$D"

# Test 4 – composefs layout, no flatpak dir at all → must PASS (exit 0)
D=$(mktemp -d)
expect_exit "composefs: no flatpak dir at all → must pass (exit 0)" 0 \
  check_installer_apps "$D" "true"
rm -rf "$D"

# Test 5 – all four installer IDs are detected, not just the first
D=$(mktemp -d)
for ID in $INSTALLER_IDS; do
  mkdir -p "$D/ostree/deploy/default/var/lib/flatpak/app/$ID"
done
expect_exit "ostree: all four installer IDs detected (exit 1)" 1 \
  check_installer_apps "$D" "false"
rm -rf "$D"

# Test 6 – .Devel variant alone is also caught
D=$(mktemp -d)
mkdir -p "$D/ostree/deploy/default/var/lib/flatpak/app/org.tunaos.Installer.Devel"
expect_exit "ostree: .Devel variant alone → caught (exit 1)" 1 \
  check_installer_apps "$D" "false"
rm -rf "$D"

# Test 7 – empty flatpak/app dir → must PASS (exit 0)
D=$(mktemp -d)
mkdir -p "$D/ostree/deploy/default/var/lib/flatpak/app"
expect_exit "ostree: flatpak/app dir exists but is empty → must pass (exit 0)" 0 \
  check_installer_apps "$D" "false"
rm -rf "$D"

# ── efibootmgr parse check (from boot-verify.sh) ──────────────────────────
#
# The check parses the output of efibootmgr run inside the guest via SSH.
# We test the grep pattern and the EFIBOOT_FAIL logic directly.

# check_efiboot_output OUTPUT
#   Returns 0 if a Boot#### entry is present, 1 if not.
check_efiboot_output() {
  local output="$1"

  # Empty output (tool not available) → skip (return 0, not a failure)
  [ -z "$output" ] && return 0

  # "not supported" means non-EFI firmware → skip (return 0)
  echo "$output" | grep -qi 'not supported' && return 0

  # At least one Boot#### entry → success
  echo "$output" | grep -qE '^Boot[0-9A-Fa-f]{4}' && return 0

  # No entries → bug caught (return 1)
  return 1
}

echo ""
echo "━━━ efibootmgr UEFI entry check ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Test 8 – efibootmgr returns valid entry → check must PASS (exit 0)
GOOD_EFIBOOT="BootCurrent: 0001
Timeout: 1 seconds
BootOrder: 0001,0000
Boot0000* Linux Boot Manager	HD(1,GPT,abc,0x800,0x100000)/File(\EFI\systemd\systemd-bootx64.efi)
Boot0001* EFI DVD/CDROM"
expect_exit "efibootmgr: valid Boot#### entry present → pass (exit 0)" 0 \
  check_efiboot_output "$GOOD_EFIBOOT"

# Test 9 – efibootmgr returns no Boot#### entries → bug caught (exit 1)
BAD_EFIBOOT="BootCurrent: 0000
Timeout: 1 seconds
BootOrder: "
expect_exit "efibootmgr: no Boot#### entries → bug caught (exit 1)" 1 \
  check_efiboot_output "$BAD_EFIBOOT"

# Test 10 – efibootmgr empty (tool absent) → graceful skip (exit 0)
expect_exit "efibootmgr: empty output (tool absent) → skip (exit 0)" 0 \
  check_efiboot_output ""

# Test 11 – efibootmgr says "not supported" (non-EFI firmware) → skip (exit 0)
expect_exit "efibootmgr: 'not supported' → skip (exit 0)" 0 \
  check_efiboot_output "EFI variables are not supported on this system."

# Test 12 – efibootmgr present but only BootOrder line (all entries deleted — pre-PR#2 state)
PRE_PR2="EFI variables are supported on this system.
BootCurrent: 0000
BootOrder: "
expect_exit "efibootmgr: EFI supported but no entries (pre-PR#2 state) → bug caught (exit 1)" 1 \
  check_efiboot_output "$PRE_PR2"

# ── bootcrew matrix validity (tests/bootcrew-matrix.yaml) ──────────────────
#
# The recipe validator rejects composefs_backend with an xfs filesystem:
# composefs needs fs-verity and XFS has none, so the install exits before it
# starts. #97 dropped that pair from three entries; ubuntu-bootc and
# opensuse-bootc kept it for another month because vm_boot: false keeps them
# out of the PR gate while nightly still runs them (#106). This check makes
# the pair impossible to reintroduce quietly.
#
# Parsed with awk rather than a YAML library so the harness keeps its "no
# dependencies, runs anywhere" property. Entries are `  - name:` at two
# spaces; commented-out entries are indented past that and are skipped.
check_matrix_combos() {
  local file="$1"
  [ -f "$file" ] || return 2
  local bad
  bad=$(awk '
    /^  - name:/            { check(); name=$3; fs=""; cf="" }
    /^    filesystem:/      { fs=$2 }
    /^    composefs_backend:/ { cf=$2 }
    END                     { check() }
    function check() {
      if (name != "" && fs == "xfs" && cf == "true") print name
    }
  ' "$file")
  if [ -n "$bad" ]; then
    printf 'xfs + composefs_backend is rejected by the recipe validator: %s\n' "$bad" >&2
    return 1
  fi
  return 0
}

MATRIX_GOOD=$(mktemp)
cat > "$MATRIX_GOOD" <<'YAML'
images:
  - name: good-ext4
    filesystem: ext4
    composefs_backend: true
  - name: good-xfs
    filesystem: xfs
    composefs_backend: false
YAML

MATRIX_BAD=$(mktemp)
cat > "$MATRIX_BAD" <<'YAML'
images:
  - name: good-ext4
    filesystem: ext4
    composefs_backend: true
  - name: bad-xfs-composefs
    filesystem: xfs
    composefs_backend: true
YAML

# Test 13 – a matrix with no invalid pair passes.
expect_exit "matrix: no xfs+composefs pair → pass (exit 0)" 0 \
  check_matrix_combos "$MATRIX_GOOD"

# Test 14 – the pair #97 removed is caught, including in the last entry
# (the END-of-file case the awk parser has to handle explicitly).
expect_exit "matrix: xfs+composefs pair → caught (exit 1)" 1 \
  check_matrix_combos "$MATRIX_BAD"

# Test 15 – the real matrix this repository ships.
expect_exit "matrix: tests/bootcrew-matrix.yaml is valid (exit 0)" 0 \
  check_matrix_combos "$(dirname "$0")/bootcrew-matrix.yaml"

rm -f "$MATRIX_GOOD" "$MATRIX_BAD"

# ── installation receipt checks (scripts/boot-receipt.sh, #385) ─────────
#
# boot-verify.sh fetches the receipt and the first-boot collection over SSH
# and runs `fisherman verify`. Here a directory stands in for the VM and a
# script for fisherman: an honest one, and one that says "verified" to
# everything. The negative checks must catch the second.

# shellcheck source=../scripts/boot-receipt.sh
source "$(dirname "$0")/../scripts/boot-receipt.sh"

RECEIPT_VM=$(mktemp -d)
mkdir -p "$RECEIPT_VM/var/lib/fisherman/boot-receipt" "$RECEIPT_VM/var/tmp"
cat >"$RECEIPT_VM/var/lib/fisherman/install-receipt.json" <<'JSON'
{"schema":"fisherman.install-receipt","version":1,"install_id":"0123456789abcdef0123456789abcdef","deployment":{"id":"1111111111111111111111111111111111111111111111111111111111111111"},"expected":{"platform_manifest_digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}}
JSON
echo '{}' >"$RECEIPT_VM/var/lib/fisherman/boot-receipt/collection.json"
echo '{}' >"$RECEIPT_VM/var/lib/fisherman/boot-receipt/bootc-status.json"

# fake_vm_run CMD — run CMD with the VM's paths moved into RECEIPT_VM.
fake_vm_run() {
  sh -c "$(printf '%s' "$1" | sed "s#/var/#$RECEIPT_VM/var/#g")"
}

FAKE_FM_HONEST=$(mktemp)
cat >"$FAKE_FM_HONEST" <<'SH'
#!/bin/sh
# verified only for the receipt's own identity, and only with evidence from
# the installed system (a collection, or --live without --receipt).
receipt="" live=0
while [ $# -gt 0 ]; do
  case "$1" in --receipt) receipt="$2"; shift ;; --live) live=1 ;; esac
  shift
done
if [ "$live" = 1 ] && [ -n "$receipt" ]; then echo '{"outcome":"unavailable"}'; exit 1; fi
if [ -n "$receipt" ] && grep -q eeeeeeee "$receipt"; then echo '{"outcome":"mismatch"}'; exit 1; fi
echo '{"outcome":"verified"}'
SH
FAKE_FM_ALWAYS=$(mktemp)
printf '#!/bin/sh\necho %s\n' "'{\"outcome\":\"verified\"}'" >"$FAKE_FM_ALWAYS"
chmod +x "$FAKE_FM_HONEST" "$FAKE_FM_ALWAYS"
RECEIPT_OUT=$(mktemp -d)
export RECEIPT_WAIT_ITERS=1 RECEIPT_WAIT_SECONDS=0

expect_exit "receipt: honest verifier, matching receipt → pass (exit 0)" 0 \
  check_boot_receipt fake_vm_run "$RECEIPT_OUT/honest" "$FAKE_FM_HONEST"
expect_exit "receipt: verifier that always says verified → caught (exit 1)" 1 \
  check_boot_receipt fake_vm_run "$RECEIPT_OUT/always" "$FAKE_FM_ALWAYS"
rm -f "$RECEIPT_VM/var/lib/fisherman/install-receipt.json"
expect_exit "receipt: no receipt on the installed system → caught (exit 1)" 1 \
  check_boot_receipt fake_vm_run "$RECEIPT_OUT/missing" "$FAKE_FM_HONEST"

rm -rf "$RECEIPT_VM" "$RECEIPT_OUT" "$FAKE_FM_HONEST" "$FAKE_FM_ALWAYS"

# ── bootc store reuse identity (scripts/store-reuse-lib.sh, #384) ────────
#
# The offline VM profile judges the disk installed from a reused bootc store
# by the install's source_reuse events, the receipt and the installed
# system's own bootc status. Each way that judgement can be wrong must fail.

# shellcheck source=../scripts/store-reuse-lib.sh
source "$(dirname "$0")/../scripts/store-reuse-lib.sh"

SR=$(mktemp -d)
SR_SRC="sha256:$(printf '1%.0s' $(seq 1 64))"
SR_COPY="sha256:$(printf 'c%.0s' $(seq 1 64))"
SR_REF="ghcr.io/example/os:stable"
sr_events() { # sr_events FILE SOURCE_DIGEST COPY_DIGEST
  {
    echo '+ bootc image copy-to-storage --source docker://x --target localhost/fisherman-reuse:x'
    echo "{\"type\":\"source_reuse\",\"phase\":\"planned\",\"digest\":\"$2\",\"estimate\":{\"scratch_bytes\":5}}"
    echo "{\"type\":\"source_reuse\",\"phase\":\"verified\",\"digest\":\"$2\",\"destination_digest\":\"$3\",\"measured\":{\"copy_ms\":1200}}"
    echo '{"type":"complete","message":"Installation complete!","elapsed_ms":9000}'
  } >"$1"
}
sr_status() { # sr_status FILE DIGEST IMAGE ARCH
  printf '{"status":{"booted":{"image":{"image":{"image":"%s","transport":"registry"},"imageDigest":"%s","architecture":"%s"}}}}\n' \
    "$3" "$2" "$4" >"$1"
}
sr_receipt() { printf '{"expected":{"platform_manifest_digest":"%s"}}\n' "$2" >"$1"; }

sr_events "$SR/ev" "$SR_SRC" "$SR_COPY"
sr_receipt "$SR/receipt" "$SR_COPY"
sr_status "$SR/status" "$SR_COPY" "$SR_REF" x86_64
expect_exit "store reuse: booted the verified copy → pass (exit 0)" 0 \
  store_reuse_identity "$SR/ev" "$SR/receipt" "$SR/status" "$SR_REF" x86_64 "$SR_SRC"
sr_status "$SR/status-orig" "$SR_SRC" "$SR_REF" x86_64
expect_exit "store reuse: booted another digest than the copy → caught (exit 1)" 1 \
  store_reuse_identity "$SR/ev" "$SR/receipt" "$SR/status-orig" "$SR_REF" x86_64 "$SR_SRC"
sr_status "$SR/status-ref" "$SR_COPY" "localhost/fisherman-reuse:x" x86_64
expect_exit "store reuse: follows the local copy's name → caught (exit 1)" 1 \
  store_reuse_identity "$SR/ev" "$SR/receipt" "$SR/status-ref" "$SR_REF" x86_64 "$SR_SRC"
sr_status "$SR/status-arch" "$SR_COPY" "$SR_REF" aarch64
expect_exit "store reuse: another platform → caught (exit 1)" 1 \
  store_reuse_identity "$SR/ev" "$SR/receipt" "$SR/status-arch" "$SR_REF" x86_64 "$SR_SRC"
sr_receipt "$SR/receipt-other" "$SR_SRC"
expect_exit "store reuse: receipt expects another digest → caught (exit 1)" 1 \
  store_reuse_identity "$SR/ev" "$SR/receipt-other" "$SR/status" "$SR_REF" x86_64 "$SR_SRC"
sr_events "$SR/ev-other" "sha256:$(printf '2%.0s' $(seq 1 64))" "$SR_COPY"
expect_exit "store reuse: the copy verified another item → caught (exit 1)" 1 \
  store_reuse_identity "$SR/ev-other" "$SR/receipt" "$SR/status" "$SR_REF" x86_64 "$SR_SRC"
grep -v verified "$SR/ev" >"$SR/ev-unverified"
expect_exit "store reuse: no verified event → caught (exit 1)" 1 \
  store_reuse_identity "$SR/ev-unverified" "$SR/receipt" "$SR/status" "$SR_REF" x86_64 "$SR_SRC"
echo 'not json' >"$SR/status-bad"
expect_exit "store reuse: malformed status → caught (exit 1)" 1 \
  store_reuse_identity "$SR/ev" "$SR/receipt" "$SR/status-bad" "$SR_REF" x86_64 "$SR_SRC"
expect_exit "store reuse: event fields are read (copy_ms)" 0 \
  test "$(reuse_field "$SR/ev" verified measured.copy_ms)" = 1200
rm -rf "$SR"

# ── summary ────────────────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
printf 'Results: %d passed, %d failed\n' "$PASS" "$FAIL"

[ "$FAIL" -eq 0 ]
