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

# ── matrix recipes (scripts/matrix-recipe.py, #418) ─────────────────────
#
# Every matrix entry, as the harness turns it into a recipe, must pass the
# recipe rules that need no machine (`fisherman validate --json --partial`):
# a varDisk with LUKS or on composefs, or ZFS without the opt-in, is refused
# before any VM boots. Needs python3 with PyYAML and a fisherman binary
# (FISHERMAN_BIN, else built with go); skipped with a notice without them.
MATRIX_FILE="$(dirname "$0")/bootcrew-matrix.yaml"
FM_BIN="${FISHERMAN_BIN:-}"
if [ -z "$FM_BIN" ] && command -v go >/dev/null 2>&1; then
  FM_BIN=$(mktemp)
  (cd "$(dirname "$0")/../fisherman" && go build -o "$FM_BIN" ./cmd/fisherman) || FM_BIN=""
fi
check_matrix_recipes() {
  python3 - "$1" "$2" "$(dirname "$0")/../scripts/matrix-recipe.py" <<'PY'
import importlib.util, json, subprocess, sys, yaml
matrix, fisherman, script = sys.argv[1:4]
spec = importlib.util.spec_from_file_location("matrix_recipe", script)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
bad = 0
for entry in yaml.safe_load(open(matrix))["images"]:
    r = mod.recipe(entry, "/dev/loop0", "/dev/loop1")
    out = subprocess.run([fisherman, "validate", "--json", "--partial", "-"],
                         input=json.dumps(r).encode(), capture_output=True)
    errors = json.loads(out.stdout or b"{}").get("errors", [{"code": "no output"}])
    if out.returncode != 0 or errors:
        bad = 1
        print("%s: %s" % (entry.get("name"), [e.get("code") for e in errors]), file=sys.stderr)
sys.exit(bad)
PY
}
if [ -n "$FM_BIN" ] && python3 -c 'import yaml' 2>/dev/null; then
  expect_exit "matrix: every entry's recipe passes validate --partial (exit 0)" 0 \
    check_matrix_recipes "$MATRIX_FILE" "$FM_BIN"
  MATRIX_BAD_RECIPE=$(mktemp)
  cat > "$MATRIX_BAD_RECIPE" <<'YAML'
images:
  - name: luks-with-var-disk
    filesystem: xfs
    luks: true
    luks_passphrase: x
    var_disk: true
YAML
  expect_exit "matrix: a varDisk with LUKS → caught (exit 1)" 1 \
    check_matrix_recipes "$MATRIX_BAD_RECIPE" "$FM_BIN"
  rm -f "$MATRIX_BAD_RECIPE"
  [ -z "${FISHERMAN_BIN:-}" ] && rm -f "$FM_BIN"
else
  echo "  SKIP  matrix recipes: needs python3 with PyYAML and go (or FISHERMAN_BIN)"
fi

# ── guest checks (scripts/vm-checks/, #418) ────────────────────────────
#
# boot-verify.sh sources these once SSH answers. Here a function stands in
# for ssh_root and a script for the ssh client: each check must pass on a
# good guest and catch the bug it exists for.
VMC="$(dirname "$0")/../scripts/vm-checks"
VMC_TMP=$(mktemp -d)
# run_check SCRIPT: source SCRIPT in a subshell, as boot-verify.sh does.
# shellcheck disable=SC1090
run_check() { ( source "$1" ); }

GOOD_HASH=$(openssl passwd -6 -salt fishsalt 'bootcrew-user-test' 2>/dev/null || true)
OTHER_HASH=$(openssl passwd -6 -salt fishsalt 'another-password' 2>/dev/null || true)
# yescrypt ($y$), what CentOS Stream 10's chpasswd writes on an ostree target.
yescrypt() {  # yescrypt PASSWORD — a $y$ hash of PASSWORD, or nothing
  python3 - "$1" <<'PY' 2>/dev/null || true
import ctypes, ctypes.util, sys
lib = ctypes.CDLL(ctypes.util.find_library("crypt") or "libcrypt.so.1")
lib.crypt.restype = lib.crypt_gensalt.restype = ctypes.c_char_p
lib.crypt.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
lib.crypt_gensalt.argtypes = [ctypes.c_char_p, ctypes.c_ulong, ctypes.c_char_p, ctypes.c_int]
print(lib.crypt(sys.argv[1].encode(), lib.crypt_gensalt(b"$y$", 0, None, 0)).decode())
PY
}
YESCRYPT_HASH=$(yescrypt 'bootcrew-user-test')
YESCRYPT_OTHER=$(yescrypt 'another-password')
cat >"$VMC_TMP/ssh" <<'SH'
#!/bin/sh
# The user session: whatever FAKE_USER_SESSION says.
printf '%b\n' "$FAKE_USER_SESSION"
SH
chmod +x "$VMC_TMP/ssh"
user_guest() {  # user_guest SESSION SHADOW_HASH — then run user.sh
  FAKE_USER_SESSION="$1" FAKE_SHADOW="$2" SSH_BIN="$VMC_TMP/ssh" SSH_KEY=/dev/null SSH_PORT=1 \
    BOOTCREW_USER=bootcrew BOOTCREW_USER_PASSWORD=bootcrew-user-test run_check "$VMC/user.sh"
}
ssh_root() {
  case "$*" in
    *"getent group wheel"*) echo "wheel:x:10:bootcrew" ;;
    *"getent shadow"*) echo "$FAKE_SHADOW" ;;
    *crypto_LUKS*) echo "/dev/sda3" ;;
    *luksDump*) printf '%b\n' "$FAKE_LUKSDUMP" ;;
    *journalctl*) echo "enrolled" ;;
    *"ActiveState"*) echo "${FAKE_UNIT_STATE:-inactive}" ;;
    *tpm2-enroll.key*) [ "${FAKE_KEY_LEFT:-0}" = 1 ] ;;
    *findmnt*) printf '%b\n' "$FAKE_VAR" ;;
    *) return 1 ;;
  esac
}
GOOD_SESSION='bootcrew\nhome-ok\ngroups: bootcrew wheel'
if [ -n "$GOOD_HASH" ]; then
  expect_exit "guest user: signs in, owns its home, recipe password → pass (exit 0)" 0 \
    user_guest "$GOOD_SESSION" "$GOOD_HASH"
  expect_exit "guest user: another password → caught (exit 1)" 1 \
    user_guest "$GOOD_SESSION" "$OTHER_HASH"
  # ssh prints a warning before the session (seen in the first VM run).
  expect_exit "guest user: a warning line before the session → pass (exit 0)" 0 \
    user_guest "Warning: Permanently added '[127.0.0.1]:2222' (ED25519) to the list of known hosts.\\n$GOOD_SESSION" "$GOOD_HASH"
  expect_exit "guest user: locked account (!) → caught (exit 1)" 1 \
    user_guest "$GOOD_SESSION" '!'
  expect_exit "guest user: cannot sign in → caught (exit 1)" 1 \
    user_guest 'Permission denied (publickey)' "$GOOD_HASH"
  expect_exit "guest user: no home it owns → caught (exit 1)" 1 \
    user_guest 'bootcrew\ngroups: bootcrew wheel' "$GOOD_HASH"
  expect_exit "guest user: not in wheel → caught (exit 1)" 1 \
    user_guest 'bootcrew\nhome-ok\ngroups: bootcrew' "$GOOD_HASH"
else
  echo "  SKIP  guest user checks: openssl not found"
fi
case "$YESCRYPT_HASH" in
  '$y$'*)
    expect_exit "guest user: yescrypt hash of the recipe password → pass (exit 0)" 0 \
      user_guest "$GOOD_SESSION" "$YESCRYPT_HASH"
    expect_exit "guest user: yescrypt hash of another password → caught (exit 1)" 1 \
      user_guest "$GOOD_SESSION" "$YESCRYPT_OTHER" ;;
  *) echo "  SKIP  guest user yescrypt checks: no libcrypt yescrypt" ;;
esac

tpm_guest() { FAKE_LUKSDUMP="$1" FAKE_KEY_LEFT="$2" TPM2_WAIT_ITERS=1 run_check "$VMC/tpm2-enrolled.sh"; }
ENROLLED='Keyslots:\n  0: luks2\n  1: luks2\nTokens:\n  0: systemd-tpm2'
expect_exit "guest tpm2: token, slot 31 and key gone → pass (exit 0)" 0 tpm_guest "$ENROLLED" 0
expect_exit "guest tpm2: no systemd-tpm2 token → caught (exit 1)" 1 \
  tpm_guest 'Keyslots:\n  0: luks2\n  31: luks2' 0
expect_exit "guest tpm2: single-use slot 31 left → caught (exit 1)" 1 \
  tpm_guest "$ENROLLED\nKeyslots:\n  31: luks2" 0
expect_exit "guest tpm2: key file left → caught (exit 1)" 1 tpm_guest "$ENROLLED" 1
# The race the first VM run hit: the token is there while ExecStopPost has
# not yet removed slot 31 and the key file. The check must wait for the unit.
tpm_race() (  # a subshell: its ssh_root must not replace the shared one
  n="$VMC_TMP/tpm-race"; echo 0 >"$n"
  ssh_root() {
    case "$*" in
      *crypto_LUKS*) echo "/dev/sda3" ;;
      *ActiveState*) c=$(cat "$n"); echo $((c + 1)) >"$n"
                     if [ "$c" -lt 2 ]; then echo activating; else echo inactive; fi ;;
      *luksDump*) if [ "$(cat "$n")" -lt 2 ]; then printf '%b\n' "$ENROLLED\nKeyslots:\n  31: luks2"; else printf '%b\n' "$ENROLLED"; fi ;;
      *journalctl*) echo "enrolled" ;;
      *tpm2-enroll.key*) [ "$(cat "$n")" -lt 2 ] ;;
      *) return 1 ;;
    esac
  }
  TPM2_WAIT_ITERS=5 run_check "$VMC/tpm2-enrolled.sh"
)
expect_exit "guest tpm2: token first, cleanup a moment later → pass (exit 0)" 0 tpm_race
# A VM run under memory pressure: the first ssh calls fail. The check must
# try again, and must not call a failed query "no LUKS device".
tpm_ssh() (  # tpm_ssh FAILS ANSWER — the first FAILS queries fail, then ANSWER
  n="$VMC_TMP/tpm-ssh"; echo 0 >"$n"
  fails="$1" answer="$2"
  ssh_root() {
    case "$*" in
      *crypto_LUKS*) c=$(cat "$n"); echo $((c + 1)) >"$n"
                     [ "$c" -ge "$fails" ] || return 255
                     [ -z "$answer" ] || echo "$answer" ;;
      *luksDump*) printf '%b\n' "$ENROLLED" ;;
      *ActiveState*) echo inactive ;;
      *journalctl*) echo "enrolled" ;;
      *) return 1 ;;
    esac
  }
  TPM2_SSH_TRIES=4 TPM2_SSH_SLEEP=0 TPM2_WAIT_ITERS=1 run_check "$VMC/tpm2-enrolled.sh"
)
expect_exit "guest tpm2: ssh fails twice, then answers → pass (exit 0)" 0 tpm_ssh 2 /dev/sda3
expect_exit "guest tpm2: ssh never answers → caught (exit 1)" 1 tpm_ssh 9 /dev/sda3
expect_exit "guest tpm2: ssh answers, no LUKS device → caught (exit 1)" 1 tpm_ssh 0 ""

var_guest() { FAKE_VAR="$1" run_check "$VMC/var-disk.sh"; }
FSTAB_VAR='UUID=1 /var xfs defaults,nofail,x-systemd.device-timeout=30s,x-systemd.before=local-fs.target 0 0'
expect_exit "guest varDisk: /var is the xfs disk labelled var → pass (exit 0)" 0 \
  var_guest "/dev/sdb xfs var\n/var\n$FSTAB_VAR"
expect_exit "guest varDisk: /var is on the root → caught (exit 1)" 1 \
  var_guest "/dev/mapper/root[/ostree/deploy/default/var] xfs root\n/var\n$FSTAB_VAR"
expect_exit "guest varDisk: /var/home elsewhere → caught (exit 1)" 1 \
  var_guest "/dev/sdb xfs var\n/var/home\n$FSTAB_VAR"
expect_exit "guest varDisk: no fstab line → caught (exit 1)" 1 \
  var_guest "/dev/sdb xfs var\n/var"

# btrfs-subvolumes.sh: the guest answers with FAKE_BTRFS (printf %b).
btrfs_guest() (
  ssh_root() { printf '%b\n' "$FAKE_BTRFS"; }
  FAKE_BTRFS="$1" run_check "$VMC/btrfs-subvolumes.sh"
)
BT_ROOT='root: /sysroot /dev/vda2[/@] btrfs ro,relatime,compress=zstd:1,ssd,space_cache=v2,subvolid=256,subvol=/@'
BT_VAR='var: /dev/vda2[/@/state/os/default/var]'
BT_CMD='cmdline: BOOT_IMAGE=/vmlinuz rw rootflags=subvol=@,compress=zstd:1 console=ttyS0'
BT_LIST='subvol: ID 256 gen 12 top level 5 path @\nsubvol: ID 257 gen 13 top level 256 path @/state/os/default/var/lib/machines\nlisted: yes'
expect_exit "guest btrfs: @ mounted with the recipe's options, only @ on top → pass (exit 0)" 0 \
  btrfs_guest "$BT_ROOT\n$BT_VAR\n$BT_CMD\n$BT_LIST"
expect_exit "guest btrfs: no btrfs tool in the image → pass, list not checked (exit 0)" 0 \
  btrfs_guest "$BT_ROOT\n$BT_VAR\n$BT_CMD"
expect_exit "guest btrfs: /sysroot not a mount point, /var on @ → pass (exit 0)" 0 \
  btrfs_guest "root: /var /dev/vda2[/@/state/os/default/var] btrfs rw,compress=zstd:1,subvolid=256,subvol=/@\n$BT_VAR\n$BT_CMD"
expect_exit "guest btrfs: root at the top level (no subvolume) → caught (exit 1)" 1 \
  btrfs_guest "root: /sysroot /dev/vda2 btrfs rw,compress=zstd:1,subvolid=5,subvol=/\n$BT_VAR\n$BT_CMD"
expect_exit "guest btrfs: another subvolume (@root) → caught (exit 1)" 1 \
  btrfs_guest "root: /sysroot /dev/vda2[/@root] btrfs rw,compress=zstd:1,subvolid=256,subvol=/@root\n$BT_VAR\n$BT_CMD"
expect_exit "guest btrfs: no zstd compression → caught (exit 1)" 1 \
  btrfs_guest "root: /sysroot /dev/vda2[/@] btrfs rw,subvolid=256,subvol=/@\n$BT_VAR\n$BT_CMD"
expect_exit "guest btrfs: not btrfs at all → caught (exit 1)" 1 \
  btrfs_guest "root: /sysroot /dev/vda2 ext4 rw,relatime\n$BT_VAR\n$BT_CMD"
expect_exit "guest btrfs: no rootflags on the command line → caught (exit 1)" 1 \
  btrfs_guest "$BT_ROOT\n$BT_VAR\ncmdline: BOOT_IMAGE=/vmlinuz rw\n$BT_LIST"
expect_exit "guest btrfs: /var outside @ → caught (exit 1)" 1 \
  btrfs_guest "$BT_ROOT\nvar: /dev/vda2[/state/os/default/var]\n$BT_CMD\n$BT_LIST"
expect_exit "guest btrfs: @home and @snapshots on top (#416) → caught (exit 1)" 1 \
  btrfs_guest "$BT_ROOT\n$BT_VAR\n$BT_CMD\nsubvol: ID 256 gen 12 top level 5 path @\nsubvol: ID 258 gen 12 top level 5 path @home\nsubvol: ID 259 gen 12 top level 5 path @snapshots\nlisted: yes"

# The harness mounts @ to reach the system (scripts/find-tools.sh).
# shellcheck source=../scripts/find-tools.sh
source "$(dirname "$0")/../scripts/find-tools.sh"
mkdir -p "$VMC_TMP/top-sub/@" "$VMC_TMP/top-flat/state/deploy" "$VMC_TMP/top-ostree/@" "$VMC_TMP/top-ostree/ostree"
expect_exit "btrfs root: top level holds only @ → mount @ (exit 0)" 0 \
  test "$(btrfs_root_subvol "$VMC_TMP/top-sub")" = "@"
expect_exit "btrfs root: the system at the top level → no subvolume (exit 1)" 1 \
  btrfs_root_subvol "$VMC_TMP/top-flat"
expect_exit "btrfs root: an @ dir next to an ostree tree → no subvolume (exit 1)" 1 \
  btrfs_root_subvol "$VMC_TMP/top-ostree"

# capabilities.sh (#383): the report taken in the installing VM, held to
# the installed system. caps_json writes a probe output; each argument
# after the file is a Python assignment applied to its capabilities.
CAPS_DIGEST="sha256:$(printf 'a%.0s' $(seq 1 64))"
caps_json() {  # caps_json FILE KIND [PY-STATEMENT...] — KIND: match | other
  python3 -I - "$CAPS_DIGEST" "$@" <<'PY'
import json, sys
digest, path, kind, edits = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
src = ["native_inspection", "install_configuration", "finalization", "deployment_mount", "bootc_image_storage"]
if kind == "match":
    c = {"version": 1,
         "live_host": {"backend": "ostree", "bootc_version": "1.17.1", "digest": digest},
         "source": {"selected": True, "digest": digest, "digest_from": "reference", "transport": "registry"},
         "execution_root": {"context": "live-host", "established": True, "reason": "digest_match",
                            "backend": "ostree", "bootc_version": "1.17.1"},
         "features": {"native_inspection": {"status": "supported", "reason": "observed", "context": "source-root"},
                      "install_configuration": {"status": "supported", "reason": "observed", "context": "source-root"},
                      "finalization": {"status": "unknown", "reason": "not_qualified", "context": "source-root"},
                      "deployment_mount": {"status": "unknown", "reason": "not_qualified", "context": "source-root"},
                      "bootc_image_storage": {"status": "unknown", "reason": "not_qualified", "context": "source-root"}},
         "install_defaults": {"observed": True, "root_filesystem": "xfs", "block_setups": ["direct"], "bootloader": "grub"},
         "image": {"observed": True, "kernel_version": "6.12.0-100.el10.x86_64", "kernel_unified": False}}
else:
    c = {"version": 1,
         "live_host": {"backend": "ostree", "bootc_version": "1.17.1", "digest": digest},
         "source": {"selected": True, "digest": "sha256:" + "e" * 64, "digest_from": "reference"},
         "execution_root": {"context": "none", "established": False, "reason": "digest_mismatch",
                            "backend": "", "bootc_version": ""},
         "features": {n: {"status": "unknown", "reason": "source_root_unavailable", "context": "none"} for n in src},
         "install_defaults": {"observed": False, "block_setups": []},
         "image": {"observed": False}}
c["features"]["unified_storage"] = {"status": "unsupported", "reason": "fisherman_refuses", "context": "fisherman"}
for e in edits:
    exec(e, {"c": c})
json.dump({"protocol_version": 1, "capabilities": c}, open(path, "w"))
PY
}
CAPS_STATUS_OSTREE='{"status":{"booted":{"image":{"imageDigest":"x"},"ostree":{"checksum":"abc"}}}}'
CAPS_STATUS_CFS='{"status":{"booted":{"image":{"imageDigest":"x"},"composefs":{"verity":"abc"}}}}'
caps_guest() (  # caps_guest PROBE OTHER [GUEST-BOOTC [KERNEL [BOOTLOADER [STATUS]]]]
  ssh_root() {
    echo "bootc: ${G_BOOTC}"; echo "kernel: ${G_KERNEL}"; echo "bootloader: ${G_LOADER}"; echo "status: ${G_STATUS}"
  }
  G_BOOTC="${3:-bootc 1.17.1}" G_KERNEL="${4:-6.12.0-100.el10.x86_64}" G_LOADER="${5:-grub}" \
    G_STATUS="${6:-$CAPS_STATUS_OSTREE}" CAPS_PROBE="$1" CAPS_PROBE_OTHER="$2" \
    CAPS_LIVE_BOOTC="bootc 1.17.1" CAPS_SOURCE_DIGEST="$CAPS_DIGEST" CAPS_EXPECT_BACKEND=ostree \
    run_check "$VMC/capabilities.sh"
)
C="$VMC_TMP/caps"
mkdir -p "$C"
caps_json "$C/good" match
caps_json "$C/other" other
expect_exit "guest caps: the report matches the installing and installed bootc → pass (exit 0)" 0 \
  caps_guest "$C/good" "$C/other"
caps_json "$C/cmd-absent" match 'c["features"]["native_inspection"] = {"status": "unsupported", "reason": "command_absent", "context": "source-root"}' \
  'c["image"] = {"observed": False}'
expect_exit "guest caps: an older bootc without container inspect → pass (exit 0)" 0 \
  caps_guest "$C/cmd-absent" "$C/other"
caps_json "$C/no-loader" match 'c["install_defaults"]["bootloader"] = ""'
expect_exit "guest caps: print-configuration names no bootloader → pass (exit 0)" 0 \
  caps_guest "$C/no-loader" "$C/other"
caps_json "$C/unrooted" match 'c["execution_root"].update(established=False, reason="digest_mismatch", context="none")'
expect_exit "guest caps: no established execution root → caught (exit 1)" 1 \
  caps_guest "$C/unrooted" "$C/other"
expect_exit "guest caps: the installed system has another bootc → caught (exit 1)" 1 \
  caps_guest "$C/good" "$C/other" "bootc 1.16.0"
caps_json "$C/oldver" match 'c["execution_root"]["bootc_version"] = "1.16.0"'
expect_exit "guest caps: the report names another bootc version → caught (exit 1)" 1 \
  caps_guest "$C/oldver" "$C/other"
caps_json "$C/cfs" match 'c["execution_root"]["backend"] = "composefs"' 'c["live_host"]["backend"] = "composefs"'
expect_exit "guest caps: the report says composefs, the install is ostree → caught (exit 1)" 1 \
  caps_guest "$C/cfs" "$C/other"
expect_exit "guest caps: the installed system is composefs → caught (exit 1)" 1 \
  caps_guest "$C/good" "$C/other" "bootc 1.17.1" "6.12.0-100.el10.x86_64" grub "$CAPS_STATUS_CFS"
caps_json "$C/sdboot" match 'c["install_defaults"]["bootloader"] = "systemd"'
expect_exit "guest caps: print-configuration says systemd, GRUB installed → caught (exit 1)" 1 \
  caps_guest "$C/sdboot" "$C/other"
expect_exit "guest caps: container inspect names another kernel → caught (exit 1)" 1 \
  caps_guest "$C/good" "$C/other" "bootc 1.17.1" "6.11.0-1.el10.x86_64"
caps_json "$C/fromnone" match 'c["features"]["install_configuration"] = {"status": "supported", "reason": "observed", "context": "none"}'
expect_exit "guest caps: a feature answered outside the source root → caught (exit 1)" 1 \
  caps_guest "$C/fromnone" "$C/other"
caps_json "$C/helpsupported" match 'c["features"]["finalization"] = {"status": "supported", "reason": "command_present", "context": "source-root"}'
expect_exit "guest caps: supported from a help text, not observed → caught (exit 1)" 1 \
  caps_guest "$C/helpsupported" "$C/other"
caps_json "$C/wrongsrc" match 'c["source"]["digest"] = "sha256:" + "b" * 64'
expect_exit "guest caps: the report is for another source digest → caught (exit 1)" 1 \
  caps_guest "$C/wrongsrc" "$C/other"
# Negative case: the host's bootc answered for a digest the host did not boot.
caps_json "$C/other-leak" other 'c["execution_root"].update(established=True, reason="digest_match", context="live-host", bootc_version="1.17.1")'
expect_exit "guest caps: another digest gets the host's execution root → caught (exit 1)" 1 \
  caps_guest "$C/good" "$C/other-leak"
caps_json "$C/other-defaults" other 'c["install_defaults"] = {"observed": True, "bootloader": "grub", "block_setups": []}' \
  'c["features"]["install_configuration"] = {"status": "supported", "reason": "observed", "context": "source-root"}'
expect_exit "guest caps: another digest reports the host's defaults → caught (exit 1)" 1 \
  caps_guest "$C/good" "$C/other-defaults"
# The probe's own golden output (internal/probe/testdata/golden): a
# digest-pinned source on a matching host, and an unrelated host. Keeps the
# check on the shape the Go code really emits.
GOLDEN="$(dirname "$0")/../fisherman/internal/probe/testdata/golden"
jq '{capabilities: .}' "$GOLDEN/caps-pinned-reference.json" >"$C/golden-match"
jq '{capabilities: .}' "$GOLDEN/caps-unrelated-live-host.json" >"$C/golden-other"
golden_guest() (
  ssh_root() {
    echo "bootc: bootc 1.17.1"; echo "kernel: 6.12.0-55.el10.x86_64"; echo "bootloader: grub"
    echo "status: $CAPS_STATUS_OSTREE"
  }
  CAPS_PROBE="$C/golden-match" CAPS_PROBE_OTHER="$1" CAPS_LIVE_BOOTC="bootc 1.17.1" \
    CAPS_SOURCE_DIGEST="sha256:$(printf '1%.0s' $(seq 1 64))" CAPS_EXPECT_BACKEND=ostree \
    run_check "$VMC/capabilities.sh"
)
expect_exit "guest caps: the probe's golden pinned-reference and unrelated-host output → pass (exit 0)" 0 \
  golden_guest "$C/golden-other"
expect_exit "guest caps: golden pinned-reference as the negative case → caught (exit 1)" 1 \
  golden_guest "$C/golden-match"
echo 'probe: bootc status failed' >"$C/not-json"
expect_exit "guest caps: no capability report → caught (exit 1)" 1 \
  caps_guest "$C/not-json" "$C/other"
unset -f ssh_root
rm -rf "$VMC_TMP"

# ── summary ────────────────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
printf 'Results: %d passed, %d failed\n' "$PASS" "$FAIL"

[ "$FAIL" -eq 0 ]
