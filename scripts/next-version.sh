#!/usr/bin/env bash
# Print the next semver tag, derived from the conventional-commit subjects
# since the last one.
#
#   scripts/next-version.sh [<range-end>]
#
# The bump is the strongest signal in the range:
#
#   major   a `!` before the colon (`feat!:`, `fix(api)!:`) or a
#           `BREAKING CHANGE:` / `BREAKING-CHANGE:` trailer in the body
#   minor   any `feat:` / `feat(scope):`
#   patch   anything else, including a range of nothing but docs and chores
#
# Patch is the floor rather than "no release" on purpose: this runs from the
# promotion gate, which only fires on a commit that already passed CI, and a
# release that ships only a CI fix is still worth a tag. Nothing is published
# when there are no commits at all -- the caller checks that.
#
# Bot prefixes are stripped before the type is read. Commits arrive here as
# `[sec-check] fix: ...` and `[guide] docs: ...`, and without this every one
# of them reads as an unrecognised type and silently becomes a patch -- which
# is right for docs and wrong for a feat.
#
# Before 1.0.0 a breaking change bumps the minor, per semver item 4: the
# major is 0 precisely to say the API is not stable yet, and spending it on
# the first `feat!:` would claim a stability the project has not declared.

set -euo pipefail

END="${1:-HEAD}"

last_tag() {
    git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1
}

LAST="$(last_tag || true)"
LAST="${LAST:-v0.0.0}"

if [ "$LAST" = "v0.0.0" ]; then
    RANGE="$END"
else
    RANGE="${LAST}..${END}"
fi

# Subjects only for the type; full bodies for the BREAKING CHANGE trailer.
SUBJECTS="$(git log "$RANGE" --no-merges --pretty=format:'%s' || true)"
BODIES="$(git log "$RANGE" --no-merges --pretty=format:'%b' || true)"

if [ -z "$SUBJECTS" ]; then
    echo "no commits since ${LAST}" >&2
    exit 3
fi

# Strip a leading bot prefix: "[sec-check] fix: x" -> "fix: x"
STRIPPED="$(printf '%s\n' "$SUBJECTS" | sed -E 's/^\[[^]]+\][[:space:]]*//')"

BUMP=patch
if printf '%s\n' "$BODIES" | grep -qE '^BREAKING[ -]CHANGE:' \
   || printf '%s\n' "$STRIPPED" | grep -qE '^[a-zA-Z]+(\([^)]*\))?!:'; then
    BUMP=major
elif printf '%s\n' "$STRIPPED" | grep -qE '^feat(\([^)]*\))?:'; then
    BUMP=minor
fi

IFS='.' read -r MAJOR MINOR PATCH <<< "${LAST#v}"

# Pre-1.0: a breaking change is a minor, not a major. See the note above.
if [ "$BUMP" = major ] && [ "$MAJOR" -eq 0 ]; then
    BUMP=minor
fi

case "$BUMP" in
    major) NEXT="v$((MAJOR + 1)).0.0" ;;
    minor) NEXT="v${MAJOR}.$((MINOR + 1)).0" ;;
    patch) NEXT="v${MAJOR}.${MINOR}.$((PATCH + 1))" ;;
esac

echo "last=$LAST bump=$BUMP next=$NEXT" >&2
echo "$NEXT"
