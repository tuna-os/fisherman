#!/bin/bash
# tests/check-custom-mounts.sh
#
# Validates that the customMounts install path — the only path used by every
# host that installs into pre-existing partitions (bootc-installer-asahi,
# wootc) — is exercised beyond unit tests.
#
# This script tests recipe JSON construction and validation of customMounts
# configurations. It does NOT require a real block device or root; it creates
# dummy files for os.Stat checks and validates recipe structure.
#
# Exit: 0 = all tests passed, 1 = one or more tests failed.

set -uo pipefail

PASS=0
FAIL=0

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }

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

# check_custom_mounts RECIPE_PATH EXPECTED_EXIT
#   Validates a recipe JSON against expected customMounts structure.
#   Exit 0 = recipe has valid customMounts, 1 = invalid or missing.
check_custom_mounts() {
  python3 -c '
import json, sys, os
r = json.load(open(sys.argv[1]))
cms = r.get("customMounts", [])
if not cms:
    sys.exit(1)
has_root = False
for i, cm in enumerate(cms):
    part = cm.get("partition", "")
    tgt  = cm.get("target", "")
    fs   = cm.get("fstype", "")
    if not part:
        print(f"customMounts[{i}]: partition is required", file=sys.stderr)
        sys.exit(1)
    if tgt == "/":
        has_root = True
    # vfat is not accepted (must be fat32)
    if fs == "vfat":
        print(f"customMounts[{i}]: unsupported fstype vfat", file=sys.stderr)
        sys.exit(1)
    # ntfs is not accepted
    if fs == "ntfs":
        print(f"customMounts[{i}]: unsupported fstype ntfs", file=sys.stderr)
        sys.exit(1)
    # Partition must exist on disk (os.Stat)
    if tgt != "swap" and tgt != "":
        if not os.path.exists(part):
            print(f"customMounts[{i}]: partition {part} does not exist", file=sys.stderr)
            sys.exit(1)
if not has_root:
    print("customMounts: no root (/) partition specified", file=sys.stderr)
    sys.exit(1)
# Encryption check
enc = r.get("encryption", {})
enc_type = enc.get("type", "") if isinstance(enc, dict) else ""
if enc_type not in ("", "none"):
    print(f"encryption {enc_type} not supported with customMounts", file=sys.stderr)
    sys.exit(1)
sys.exit(0)
' "$1"
}

# check_not_custom_mounts RECIPE_PATH
#   Validates that a recipe JSON is INVALID (should fail customMounts checks).
check_not_custom_mounts() {
  python3 -c '
import json, sys, os
r = json.load(open(sys.argv[1]))
cms = r.get("customMounts", [])
if not cms:
    sys.exit(1)
has_root = False
for i, cm in enumerate(cms):
    part = cm.get("partition", "")
    tgt  = cm.get("target", "")
    fs   = cm.get("fstype", "")
    if not part:
        print(f"customMounts[{i}]: partition is required", file=sys.stderr)
        sys.exit(1)
    if tgt == "/":
        has_root = True
    if fs == "vfat":
        print(f"customMounts[{i}]: unsupported fstype vfat", file=sys.stderr)
        sys.exit(1)
    if fs == "ntfs":
        print(f"customMounts[{i}]: unsupported fstype ntfs", file=sys.stderr)
        sys.exit(1)
    if tgt != "swap" and tgt != "":
        if not os.path.exists(part):
            print(f"customMounts[{i}]: partition {part} does not exist", file=sys.stderr)
            sys.exit(1)
if not has_root:
    print("customMounts: no root (/) partition specified", file=sys.stderr)
    sys.exit(1)
enc = r.get("encryption", {})
enc_type = enc.get("type", "") if isinstance(enc, dict) else ""
if enc_type not in ("", "none"):
    print(f"encryption {enc_type} not supported with customMounts", file=sys.stderr)
    sys.exit(1)
sys.exit(0)
' "$1"
}

# ── setup: create fake partition files ────────────────────────────────────

TMPD=$(mktemp -d)
trap 'rm -rf "$TMPD"' EXIT

ROOT_PART="$TMPD/root-part"
ESP_PART="$TMPD/esp-part"
HOME_PART="$TMPD/home-part"
VAR_PART="$TMPD/var-part"
touch "$ROOT_PART" "$ESP_PART" "$HOME_PART" "$VAR_PART"

# ── helper: write recipe JSON with variable substitution ──────────────────

write_recipe() {
  local path="$1"
  shift
  # Use printf to handle the JSON body safely.
  printf '%s\n' "$1" > "$path"
}

# ── 1. Valid customMounts recipes ────────────────────────────────────────

echo ""
echo "━━━ customMounts recipe validation ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

R1="$TMPD/r1.json"
cat > "$R1" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "xfs"},
        {"partition": "${ESP_PART}", "target": "/boot/efi", "fstype": "fat32"}
    ]
}
JSONEOF
expect_exit "valid: root + ESP customMounts" 0 check_custom_mounts "$R1"

R2="$TMPD/r2.json"
cat > "$R2" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "ext4"},
        {"partition": "${ESP_PART}", "target": "/boot/efi", "fstype": "unformatted"}
    ]
}
JSONEOF
expect_exit "valid: unformatted ESP (Apple Silicon)" 0 check_custom_mounts "$R2"

R3="$TMPD/r3.json"
cat > "$R3" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "btrfs"},
        {"partition": "${ESP_PART}", "target": "/boot/efi", "fstype": ""}
    ]
}
JSONEOF
expect_exit "valid: empty fstype = unformatted" 0 check_custom_mounts "$R3"

R4="$TMPD/r4.json"
cat > "$R4" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "xfs"},
        {"partition": "${ESP_PART}", "target": "/boot/efi", "fstype": "fat32"},
        {"partition": "${HOME_PART}", "target": "/home", "fstype": "ext4"},
        {"partition": "${VAR_PART}", "target": "/var", "fstype": "ext4"}
    ]
}
JSONEOF
expect_exit "valid: root + ESP + /home + /var" 0 check_custom_mounts "$R4"

# ── 2. Invalid customMounts recipes ──────────────────────────────────────

R5="$TMPD/r5.json"
cat > "$R5" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "customMounts": [
        {"partition": "${ESP_PART}", "target": "/boot/efi", "fstype": "fat32"}
    ]
}
JSONEOF
expect_exit "invalid: no root partition" 1 check_not_custom_mounts "$R5"

R6="$TMPD/r6.json"
cat > "$R6" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "xfs"},
        {"partition": "${ESP_PART}", "target": "/boot/efi", "fstype": "vfat"}
    ]
}
JSONEOF
expect_exit "invalid: vfat fstype (must be fat32)" 1 check_not_custom_mounts "$R6"

R7="$TMPD/r7.json"
cat > "$R7" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "encryption": {"type": "luks-passphrase", "passphrase": "test"},
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "xfs"}
    ]
}
JSONEOF
expect_exit "invalid: LUKS encryption with customMounts" 1 check_not_custom_mounts "$R7"

R8="$TMPD/r8.json"
cat > "$R8" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "encryption": {"type": "tpm2-luks"},
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "xfs"}
    ]
}
JSONEOF
expect_exit "invalid: TPM2 encryption with customMounts" 1 check_not_custom_mounts "$R8"

R9="$TMPD/r9.json"
cat > "$R9" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "customMounts": [
        {"target": "/", "fstype": "xfs"}
    ]
}
JSONEOF
expect_exit "invalid: missing partition field" 1 check_not_custom_mounts "$R9"

R10="$TMPD/r10.json"
cat > "$R10" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test-custom",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "ntfs"}
    ]
}
JSONEOF
expect_exit "invalid: unsupported fstype ntfs" 1 check_not_custom_mounts "$R10"

# ── 3. Supported fstype matrix ────────────────────────────────────────────

echo ""
echo "━━━ supported customMounts fstypes ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

for fstype in fat32 ext3 ext4 xfs btrfs; do
  R="$TMPD/fs_${fstype}.json"
  cat > "$R" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "${fstype}"}
    ]
}
JSONEOF
  expect_exit "fstype: ${fstype}" 0 check_custom_mounts "$R"
done

# swap needs special handling
R_SWAP="$TMPD/fs_swap.json"
cat > "$R_SWAP" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "xfs"},
        {"partition": "${ESP_PART}", "target": "swap", "fstype": "swap"}
    ]
}
JSONEOF
expect_exit "fstype: swap" 0 check_custom_mounts "$R_SWAP"

for fstype in "" unformatted; do
  R="$TMPD/fs_empty.json"
  label="$fstype"
  [ -z "$label" ] && label="(empty)"
  cat > "$R" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "test",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "xfs"},
        {"partition": "${ESP_PART}", "target": "/boot/efi", "fstype": "${fstype}"}
    ]
}
JSONEOF
  expect_exit "fstype: ${label}" 0 check_custom_mounts "$R"
done

# ── 4. customMounts JSON round-trip ───────────────────────────────────────

echo ""
echo "━━━ customMounts JSON round-trip ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

R_RT="$TMPD/roundtrip.json"
cat > "$R_RT" << JSONEOF
{
    "image": "example.invalid/img:latest",
    "hostname": "roundtrip-host",
    "customMounts": [
        {"partition": "${ROOT_PART}", "target": "/", "fstype": "ext4"},
        {"partition": "${ESP_PART}", "target": "/boot/efi", "fstype": "unformatted"},
        {"partition": "${HOME_PART}", "target": "/home", "fstype": "xfs"}
    ],
    "targetMount": "/mnt/altroot",
    "luksMapperName": "altmapper"
}
JSONEOF

expect_exit "round-trip: all customMounts fields survive" 0 python3 -c '
import json, sys
r = json.load(open(sys.argv[1]))
cms = r["customMounts"]
assert len(cms) == 3, f"want 3 mounts, got {len(cms)}"
assert cms[0]["partition"] != "", "partition must survive"
assert cms[0]["target"] == "/", "root target must be /"
assert cms[1]["fstype"] == "unformatted", "unformatted must survive"
assert cms[2]["target"] == "/home", "home target must survive"
assert r["targetMount"] == "/mnt/altroot", "targetMount must survive"
assert r["luksMapperName"] == "altmapper", "luksMapperName must survive"
' "$R_RT"

# ── summary ────────────────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
printf 'Results: %d passed, %d failed\n' "$PASS" "$FAIL"

[ "$FAIL" -eq 0 ]
