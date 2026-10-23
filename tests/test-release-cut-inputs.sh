#!/usr/bin/env bash
# Tests that workflow_dispatch inputs never become shell source (#337).
#
# A `${{ inputs.x }}` expression inside a `run:` script is pasted into the
# script text before bash parses it, so an input such as `1.0.0$(cmd)` runs
# `cmd` in the job. release-cut.yml runs with contents: write and may hold
# RELEASE_TOKEN, so it passes inputs through env and validates the version.
#
# 1. No `run:` script in any workflow interpolates inputs directly.
# 2. release-cut.yml's "Compute next version" step accepts the valid inputs
#    and refuses the rest, without executing anything an input carries.

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pass=0; fail=0
ok()  { printf '  ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  FAIL %s\n' "$1"; fail=$((fail+1)); }

echo "release-cut inputs"

# ── 1. no inputs interpolated into run: scripts ────────────────────────────
offenders="$(python3 - "$ROOT/.github/workflows" <<'EOF'
import os, re, sys, yaml
pat = re.compile(r"\$\{\{[^}]*\b(inputs|github\.event\.inputs)\.")
d = sys.argv[1]
for name in sorted(os.listdir(d)):
    if not name.endswith((".yml", ".yaml")):
        continue
    wf = yaml.safe_load(open(os.path.join(d, name))) or {}
    for job_id, job in (wf.get("jobs") or {}).items():
        for i, step in enumerate(job.get("steps") or []):
            if pat.search(step.get("run") or ""):
                print(f"{name}: job {job_id} step {step.get('name', i)}")
EOF
)"
if [ -z "$offenders" ]; then
    ok "no run: script interpolates workflow inputs"
else
    bad "run: scripts interpolate workflow inputs:"; printf '       %s\n' "$offenders"
fi

# ── 2. the version step itself ─────────────────────────────────────────────
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
python3 - "$ROOT/.github/workflows/release-cut.yml" "$work/version.sh" <<'EOF'
import sys, yaml
wf = yaml.safe_load(open(sys.argv[1]))
step = next(s for s in wf["jobs"]["cut"]["steps"] if s.get("id") == "version")
open(sys.argv[2], "w").write(step["run"])
EOF

# A throwaway repo with one tag, and a stub next-version.sh for bump=auto.
repo="$work/repo"
git init -q "$repo"
git -C "$repo" config user.email t@t; git -C "$repo" config user.name t
git -C "$repo" commit -q --allow-empty -m "chore: init"
git -C "$repo" tag v1.2.3
mkdir -p "$repo/scripts"
printf '#!/bin/sh\necho v1.3.0\n' > "$repo/scripts/next-version.sh"
chmod +x "$repo/scripts/next-version.sh"

# expect <name> <version> <bump> <want: next version, or "fail">
expect() {
    local name="$1" version="$2" bump="$3" want="$4"
    : > "$work/out"
    (cd "$repo" && GITHUB_OUTPUT="$work/out" REQUESTED_VERSION="$version" \
        REQUESTED_BUMP="$bump" bash "$work/version.sh" >/dev/null 2>&1)
    local rc=$?
    local got; got="$(sed -n 's/^version=//p' "$work/out")"
    if [ "$want" = fail ]; then
        if [ $rc -ne 0 ] && [ -z "$got" ]; then ok "$name"; else bad "$name (rc=$rc, version=$got)"; fi
    else
        if [ $rc -eq 0 ] && [ "$got" = "$want" ]; then ok "$name"; else bad "$name (rc=$rc, version=$got, want $want)"; fi
    fi
}

expect "explicit version"            "2.0.0" auto  v2.0.0
expect "bump auto"                   ""      auto  v1.3.0
expect "bump patch"                  ""      patch v1.2.4
expect "bump minor"                  ""      minor v1.3.0
expect "bump major"                  ""      major v2.0.0
expect "unknown bump refused"        ""      nope  fail
expect "leading v refused"           "v2.0.0" auto fail
expect "pre-release suffix refused"  "2.0.0-rc1" auto fail
expect "newline refused"             $'2.0.0\nversion=9.9.9' auto fail

marker="$work/injected"
expect "command substitution refused" "1.0.0\$(touch $marker)" auto fail
expect "backticks refused"            "1.0.0\`touch $marker\`" auto fail
expect "injection in bump refused"    "" "patch\$(touch $marker)" fail
if [ -e "$marker" ]; then bad "an input was executed"; else ok "no input was executed"; fi

echo
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ]
