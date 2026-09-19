#!/usr/bin/env bash
# Guards the one property promote-reconcile.yml exists to restore:
#
#   after a reconcile, prod must be an ancestor of dev
#
# promote.yml pushes a dev commit onto prod and refuses unless prod is an
# ancestor of it. A reconcile that merges dev INTO prod satisfies the
# opposite relation -- it looks like it worked, the trees match, and the
# gate still refuses forever. That mistake shipped once; this test is here
# so it cannot ship twice.
set -euo pipefail

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "  ok   - $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  FAIL - $1"; }
check() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want $3, got $2)"; fi; }

# A repo shaped like fisherman after #220: dev moved on, prod holds one
# squashed commit that shares no ancestry with what it flattened.
make_diverged_repo() {
  local d; d=$(mktemp -d)
  git -C "$d" init -q -b dev
  git -C "$d" config user.email t@t; git -C "$d" config user.name t
  echo base > "$d/f"; git -C "$d" add f
  git -C "$d" commit -qm "base"
  local BASE; BASE=$(git -C "$d" rev-parse HEAD)

  # dev gains real work
  echo devwork > "$d/f"; echo more > "$d/g"
  git -C "$d" add -A; git -C "$d" commit -qm "feat: dev work"

  # prod gets that same content as ONE commit that shares no ancestry with
  # the commits it flattened -- a squash-merge onto the previous prod tip.
  git -C "$d" checkout -q -B prod "$BASE"
  echo devwork > "$d/f"; echo more > "$d/g"
  git -C "$d" add -A
  git -C "$d" commit -qm "Release v0.3.0 (#220)"
  git -C "$d" checkout -q dev
  echo "$d"
}

# promote.yml's guard, verbatim in spirit: can it push $dev onto $prod?
gate_promotes() {
  git -C "$1" merge-base --is-ancestor "$(git -C "$1" rev-parse prod)" \
                                        "$(git -C "$1" rev-parse dev)"
}

echo "reconcile direction"

D=$(make_diverged_repo)
check "starts diverged: gate refuses" "$(gate_promotes "$D" && echo yes || echo no)" "no"
check "trees already match" \
  "$(git -C "$D" diff --quiet dev prod && echo same || echo differ)" "same"

# --- the WRONG direction: merge dev into prod ---
W=$(make_diverged_repo)
git -C "$W" checkout -q -B wrong prod
git -C "$W" merge --no-commit --no-ff dev >/dev/null 2>&1 || true
git -C "$W" read-tree --reset -u dev
git -C "$W" commit -qm "reconcile (wrong direction)"
git -C "$W" branch -q -f prod wrong
git -C "$W" checkout -q dev
check "wrong direction still matches dev's tree" \
  "$(git -C "$W" diff --quiet dev prod && echo same || echo differ)" "same"
check "wrong direction does NOT unblock the gate" \
  "$(gate_promotes "$W" && echo yes || echo no)" "no"

# --- the RIGHT direction: merge prod into dev ---
R=$(make_diverged_repo)
git -C "$R" checkout -q -B right dev
git -C "$R" merge --no-commit --no-ff prod >/dev/null 2>&1 || true
git -C "$R" read-tree --reset -u dev
DEVTREE=$(git -C "$R" rev-parse dev^{tree})
git -C "$R" commit -qm "reconcile (right direction)"
check "right direction changes no files" \
  "$(git -C "$R" rev-parse HEAD^{tree})" "$DEVTREE"
git -C "$R" branch -q -f dev right
git -C "$R" checkout -q dev
check "right direction unblocks the gate" \
  "$(gate_promotes "$R" && echo yes || echo no)" "yes"
check "right direction is a no-op on a second run" \
  "$(git -C "$R" merge-base --is-ancestor prod dev && echo noop || echo again)" "noop"

rm -rf "$D" "$W" "$R"
echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
