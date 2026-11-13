#!/bin/bash
# scripts/store-reuse-lib.sh — helpers for the bootc store reuse VM profile
# (#384 phase 2), sourced by scripts/store-reuse-vm.sh and, with fakes, by
# tests/check-validation.sh. Pure functions over files: no VM, no disk.

# reuse_field EVENTS PHASE KEY — print KEY (a dotted path) of the last
# source_reuse event with PHASE in an NDJSON event stream, or nothing.
reuse_field() {
  python3 -I -c 'import json,sys
want_phase, key = sys.argv[2], sys.argv[3]
found = None
for line in open(sys.argv[1], errors="replace"):
    line = line.strip()
    if not line.startswith("{"):
        continue
    try:
        e = json.loads(line)
    except ValueError:
        continue
    if e.get("type") == "source_reuse" and e.get("phase") == want_phase:
        found = e
if found is not None:
    v = found
    for k in key.split("."):
        v = v.get(k) if isinstance(v, dict) else None
    if v is not None:
        print(v)' "$1" "$2" "$3" 2>/dev/null || true
}

# event_field EVENTS TYPE KEY — KEY of the last event of TYPE.
event_field() {
  python3 -I -c 'import json,sys
found = None
for line in open(sys.argv[1], errors="replace"):
    line = line.strip()
    if not line.startswith("{"):
        continue
    try:
        e = json.loads(line)
    except ValueError:
        continue
    if e.get("type") == sys.argv[2]:
        found = e
if found is not None and found.get(sys.argv[3]) is not None:
    print(found[sys.argv[3]])' "$1" "$2" "$3" 2>/dev/null || true
}

# booted_field STATUS KEY — from `bootc status --json` output: digest, image,
# arch of the booted entry.
booted_field() {
  python3 -I -c 'import json,sys
try:
    b = json.load(open(sys.argv[1]))["status"]["booted"]["image"]
except Exception:
    sys.exit(0)
print({"digest": b.get("imageDigest", ""), "image": b.get("image", {}).get("image", ""),
       "arch": b.get("architecture", "")}[sys.argv[2]])' "$1" "$2" 2>/dev/null || true
}

# receipt_expected_digest RECEIPT — the receipt's expected platform digest.
receipt_expected_digest() {
  python3 -I -c 'import json,sys
try:
    print(json.load(open(sys.argv[1]))["expected"]["platform_manifest_digest"] or "")
except Exception:
    pass' "$1" 2>/dev/null || true
}

# store_reuse_identity EVENTS RECEIPT STATUS TARGET_REF PLATFORM SOURCE_DIGEST
#   Judge the reused install after its first boot. EVENTS is the install's
#   event stream, RECEIPT its install receipt, STATUS the installed system's
#   own `bootc status --json` (the first-boot collection). Returns 0 when:
#   the copy was verified for SOURCE_DIGEST; the system booted the copy's
#   digest, which is also what the receipt expects; it follows TARGET_REF;
#   and it is a PLATFORM image.
store_reuse_identity() {
  local events="$1" receipt="$2" status="$3" target="$4" platform="$5" source_digest="$6" rc=0
  local verified_digest copy_digest booted expected image arch
  verified_digest=$(reuse_field "$events" verified digest)
  copy_digest=$(reuse_field "$events" verified destination_digest)
  booted=$(booted_field "$status" digest)
  expected=$(receipt_expected_digest "$receipt")
  image=$(booted_field "$status" image)
  arch=$(booted_field "$status" arch)
  if [ -z "$verified_digest" ] || [ "$verified_digest" != "$source_digest" ]; then
    echo "❌ identity: no verified copy of $source_digest in the install events (got '${verified_digest}')"
    rc=1
  fi
  if [ -z "$copy_digest" ] || [ "$booted" != "$copy_digest" ]; then
    echo "❌ identity: booted '${booted}', the verified copy is '${copy_digest}'"
    rc=1
  fi
  if [ -z "$expected" ] || [ "$expected" != "$booted" ]; then
    echo "❌ identity: the receipt expects '${expected}', booted '${booted}'"
    rc=1
  fi
  if [ "$image" != "$target" ]; then
    echo "❌ identity: the installed system follows '${image}', want '${target}'"
    rc=1
  fi
  if [ "$arch" != "$platform" ]; then
    echo "❌ identity: booted platform '${arch}', want '${platform}'"
    rc=1
  fi
  if [ "$rc" = 0 ]; then
    echo "✅ identity: booted the verified copy ${copy_digest} of ${source_digest} (${platform}), following ${target}"
  fi
  return $rc
}

# bytes_gib N — N bytes as GiB with one decimal ("-" when empty).
bytes_gib() {
  if [ -z "${1:-}" ]; then echo "-"; return; fi
  awk -v b="$1" 'BEGIN { printf "%.2f GiB", b / 1073741824 }'
}

# ms_s N — N milliseconds as seconds ("-" when empty).
ms_s() {
  if [ -z "${1:-}" ]; then echo "-"; return; fi
  awk -v m="$1" 'BEGIN { printf "%.0f s", m / 1000 }'
}
