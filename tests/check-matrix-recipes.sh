#!/bin/bash
# tests/check-matrix-recipes.sh
#
# Expands every entry of tests/bootcrew-matrix.yaml into the recipe that
# `just bootcrew-ci-test` would install (scripts/matrix-recipe.sh) and passes
# each one to `fisherman validate --json --partial`. A matrix entry that the
# validator rejects fails here in seconds, not after a 20-minute VM job.
#
# --partial, because the two things a full validation also checks are not true
# on the machine that runs this: the target disks are loop devices created
# later, and a tpm2-* entry needs a TPM.
#
# Needs: go, jq, python3 with PyYAML. No disk, VM or network.
# Usage: tests/check-matrix-recipes.sh [path/to/fisherman]
#
# Exit codes: 0 = every entry expands and validates, 1 = at least one did not.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

FISHERMAN="${1:-}"
if [ -z "$FISHERMAN" ]; then
  FISHERMAN="$WORK/fisherman"
  (cd "$ROOT/fisherman" && go build -o "$FISHERMAN" ./cmd/fisherman/) || {
    echo "FAIL: could not build fisherman"
    exit 1
  }
fi

python3 - "$ROOT/tests/bootcrew-matrix.yaml" > "$WORK/entries.jsonl" <<'PY' || { echo "FAIL: could not read the matrix"; exit 1; }
import json, sys, yaml
for entry in yaml.safe_load(open(sys.argv[1]))["images"]:
    print(json.dumps(entry))
PY

FAIL=0
COUNT=0
while IFS= read -r entry; do
  COUNT=$((COUNT + 1))
  name=$(printf '%s' "$entry" | jq -r '.name')
  if ! recipe=$(bash "$ROOT/scripts/matrix-recipe.sh" "$entry" /dev/loop98 /dev/loop99 2>&1); then
    echo "FAIL  $name: matrix-recipe.sh refused the entry: $recipe"
    FAIL=1
    continue
  fi
  # validate exits non-zero when the recipe has errors; the report says why.
  report=$(printf '%s' "$recipe" | "$FISHERMAN" validate --json --partial - 2>&1)
  if [ "$(printf '%s' "$report" | jq -r '.valid' 2>/dev/null)" = "true" ]; then
    echo "PASS  $name"
  else
    echo "FAIL  $name: fisherman validate rejects the recipe:"
    printf '%s' "$report" | jq -r '.errors[]? | "        \(.field): \(.code): \(.message)"' 2>/dev/null \
      || printf '        %s\n' "$report"
    FAIL=1
  fi
done < "$WORK/entries.jsonl"

if [ "$COUNT" -eq 0 ]; then
  echo "FAIL: the matrix has no entries"
  exit 1
fi
echo "$COUNT matrix entries checked"
exit "$FAIL"
