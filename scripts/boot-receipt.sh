#!/bin/bash
# scripts/boot-receipt.sh — the installation receipt checks (#385), sourced
# by boot-verify.sh after the installed VM answers on SSH, and by
# tests/check-validation.sh with fakes.
#
# They are advisory: boot-verify.sh reports them and fails only when
# BOOTCREW_RECEIPT_REQUIRED=1. Nothing here changes the installed system
# beyond a temporary copy of fisherman in /var/tmp, which it removes.

RECEIPT_PATH=/var/lib/fisherman/install-receipt.json
RECEIPT_COLLECTION=/var/lib/fisherman/boot-receipt

# receipt_outcome FILE — print the "outcome" of a result, or "none".
receipt_outcome() {
  python3 -I -c 'import json,sys
try:
    print(json.load(open(sys.argv[1]))["outcome"])
except Exception:
    print("none")' "$1" 2>/dev/null || echo none
}

# receipt_wrong_target IN OUT — a copy of the receipt that expects another
# image: a different deployment id and platform manifest digest.
receipt_wrong_target() {
  python3 -I -c 'import json,sys
r = json.load(open(sys.argv[1]))
r["deployment"]["id"] = "e" * len(r["deployment"].get("id") or "e" * 64)
if r["expected"].get("platform_manifest_digest"):
    r["expected"]["platform_manifest_digest"] = "sha256:" + "e" * 64
json.dump(r, open(sys.argv[2], "w"))' "$1" "$2"
}

# check_boot_receipt RUN OUTDIR FISHERMAN
#   RUN runs one shell command on the installed system (boot-verify.sh's
#   ssh_root). FISHERMAN is the fisherman binary that installed it.
#   Returns 0 when the receipt verified and both negative checks held.
check_boot_receipt() {
  local run="$1" out="$2" fm="$3" rc=0 outcome
  mkdir -p "$out/boot-receipt"

  # The collector is a multi-user.target unit; SSH can answer first.
  for _ in $(seq 1 "${RECEIPT_WAIT_ITERS:-60}"); do
    "$run" "test -e $RECEIPT_COLLECTION/collection.json" 2>/dev/null && break
    sleep "${RECEIPT_WAIT_SECONDS:-2}"
  done
  if ! "$run" "cat $RECEIPT_PATH" >"$out/install-receipt.json" 2>/dev/null; then
    echo "❌ receipt: $RECEIPT_PATH is missing on the installed system"
    return 1
  fi
  for f in collection.json bootc-status.json bootc-version.txt; do
    "$run" "cat $RECEIPT_COLLECTION/$f" >"$out/boot-receipt/$f" 2>/dev/null \
      || rm -f "$out/boot-receipt/$f"
  done
  "$run" "systemctl status --no-pager fisherman-boot-receipt.service" \
    >"$out/unit-status.txt" 2>&1 || true

  # 1. What the first boot collected, judged against the receipt.
  "$fm" verify --json --receipt "$out/install-receipt.json" \
    --collection "$out/boot-receipt" >"$out/verification-collected.json" 2>/dev/null
  outcome=$(receipt_outcome "$out/verification-collected.json")
  if [ "$outcome" = verified ]; then
    echo "✅ receipt (first-boot collection): verified"
  else
    echo "❌ receipt (first-boot collection): $outcome"
    rc=1
  fi

  # 2. fisherman verify --live on the installed system itself.
  if "$run" "cat > /var/tmp/fisherman-verify && chmod 0755 /var/tmp/fisherman-verify" <"$fm" 2>/dev/null; then
    "$run" "/var/tmp/fisherman-verify verify --json --live" \
      >"$out/verification-live.json" 2>/dev/null
    "$run" "rm -f /var/tmp/fisherman-verify" 2>/dev/null || true
    outcome=$(receipt_outcome "$out/verification-live.json")
    case "$outcome" in
      verified) echo "✅ receipt (live, on the installed system): verified" ;;
      none) echo "⚠️  receipt (live): fisherman did not run on the installed system; skipped" ;;
      *) echo "❌ receipt (live, on the installed system): $outcome"; rc=1 ;;
    esac
  else
    echo "⚠️  receipt (live): could not copy fisherman to the installed system; skipped"
  fi

  # 3. Wrong target: a receipt for another image must not verify.
  receipt_wrong_target "$out/install-receipt.json" "$out/wrong-target-receipt.json"
  "$fm" verify --json --receipt "$out/wrong-target-receipt.json" \
    --collection "$out/boot-receipt" >"$out/verification-wrong-target.json" 2>/dev/null
  outcome=$(receipt_outcome "$out/verification-wrong-target.json")
  if [ "$outcome" = mismatch ]; then
    echo "✅ receipt (wrong-target negative): mismatch"
  else
    echo "❌ receipt (wrong-target negative): $outcome, want mismatch"
    rc=1
  fi

  # 4. Wrong context: this host is not the installed system; its status
  #    (or its lack of bootc) must never verify the target.
  "$fm" verify --json --live --receipt "$out/install-receipt.json" \
    >"$out/verification-host.json" 2>/dev/null
  outcome=$(receipt_outcome "$out/verification-host.json")
  if [ "$outcome" != verified ]; then
    echo "✅ receipt (host context negative): $outcome"
  else
    echo "❌ receipt (host context negative): the installer host verified the target"
    rc=1
  fi
  return $rc
}
