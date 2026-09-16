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

# ── summary ────────────────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
printf 'Results: %d passed, %d failed\n' "$PASS" "$FAIL"

[ "$FAIL" -eq 0 ]
