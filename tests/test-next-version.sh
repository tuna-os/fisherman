#!/usr/bin/env bash
# Tests for scripts/next-version.sh, run against throwaway git repos.
#
# The two cases worth having are the ones that fail silently: a bot-prefixed
# `[sec-check] feat:` reading as a patch because the type was never found,
# and a pre-1.0 `feat!:` claiming 1.0.0 on a project that has not declared
# stability. Both look like a working script until someone reads the tag.

set -uo pipefail
SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/scripts/next-version.sh"
pass=0; fail=0

# make_repo <tag|-> <message>...
# A message may carry a body: everything after a literal '|' becomes it,
# which is the only way to exercise the BREAKING CHANGE trailer.
make_repo() {
    local tag="$1"; shift
    local dir; dir="$(mktemp -d)"
    git -C "$dir" init -q
    git -C "$dir" config user.email t@t; git -C "$dir" config user.name t
    git -C "$dir" commit -q --allow-empty -m "chore: init"
    [ "$tag" != "-" ] && git -C "$dir" tag "$tag"
    local s
    for s in "$@"; do
        if [[ "$s" == *"|"* ]]; then
            git -C "$dir" commit -q --allow-empty -m "${s%%|*}" -m "${s#*|}"
        else
            git -C "$dir" commit -q --allow-empty -m "$s"
        fi
    done
    echo "$dir"
}

check() {
    local name="$1" want="$2" dir="$3"
    local got; got="$(cd "$dir" && bash "$SCRIPT" 2>/dev/null)"
    local rc=$?
    if [ "$got" = "$want" ]; then
        printf '  ok   %-52s %s\n' "$name" "$got"; pass=$((pass+1))
    else
        printf '  FAIL %-52s want %s got %s (rc=%d)\n' "$name" "$want" "${got:-<none>}" "$rc"; fail=$((fail+1))
    fi
    rm -rf "$dir"
}

echo "next-version.sh"
check "docs only -> patch"            v1.2.4 "$(make_repo v1.2.3 'docs: readme')"
check "fix -> patch"                  v1.2.4 "$(make_repo v1.2.3 'fix(disk): off-by-one')"
check "feat -> minor"                 v1.3.0 "$(make_repo v1.2.3 'fix: a' 'feat(luks): b')"
check "feat! -> major"                v2.0.0 "$(make_repo v1.2.3 'feat(api)!: drop v1')"
check "BREAKING CHANGE body -> major"  v2.0.0 "$(make_repo v1.2.3 'refactor: rework|BREAKING CHANGE: recipe schema v2')"
check "BREAKING-CHANGE hyphen -> major" v2.0.0 "$(make_repo v1.2.3 'refactor: rework|BREAKING-CHANGE: recipe schema v2')"
check "body mentioning it mid-line"    v1.2.4 "$(make_repo v1.2.3 'fix: x|not a BREAKING CHANGE: trailer')"
check "no tag yet -> 0.0.1"           v0.0.1 "$(make_repo - 'fix: first')"

# Bot prefixes: without stripping, every one of these reads as an unknown
# type and collapses to patch.
check "[sec-check] feat -> minor"     v1.3.0 "$(make_repo v1.2.3 '[sec-check] feat: tls')"
check "[guide] docs -> patch"         v1.2.4 "$(make_repo v1.2.3 '[guide] docs: notes')"

# Pre-1.0: breaking is a minor, because major 0 is the stability disclaimer.
check "pre-1.0 feat! -> minor"        v0.4.0 "$(make_repo v0.3.0 'feat!: rework recipe')"
check "pre-1.0 feat -> minor"         v0.4.0 "$(make_repo v0.3.0 'feat: add')"
check "pre-1.0 fix -> patch"          v0.3.1 "$(make_repo v0.3.0 'fix: y')"

echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ]
