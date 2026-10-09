# Runbook: a published fisherman release is bad

`promote.yml` ships a new `prod` tag automatically, with no human in the
loop once the required checks are green and the soak has elapsed (see
`CONTRIBUTING.md`). That is deliberate, but it means the first a human
hears of a bad release may be a bug report against a binary that is
already attached to a GitHub Release and already vendored by
`tuna-os/bootc-installer`'s submodule pointer.

This is the path back. It does not cover a bad disk partitioning or
install behavior reaching a real machine — fisherman runs as root against
block devices, and `docs/PROBE.md` and the VM matrix
(`tests/bootcrew-matrix.yaml`) are what catch that before merge. This is
about a release artifact (binary, checksum, changelog) that should not
have gone out.

## 1. Confirm it is actually bad

- Read the release page: `https://github.com/tuna-os/fisherman/releases/tag/<tag>`.
- Check which checks were green on the tagged commit:
  ```bash
  gh api repos/tuna-os/fisherman/commits/<sha>/check-runs --paginate \
    --jq '.check_runs[] | [.name, .conclusion] | @tsv'
  ```
  If `required-canaries`, `lint`, `unit-tests`, or `actionlint` is not
  `success`, the gate was bypassed with `force: true` on `promote.yml` or
  the hand-cut `release-cut.yml` path — note which, it changes who needs
  to know.
- Check whether `tuna-os/bootc-installer` has already bumped its submodule
  pointer to the bad tag (`git submodule status` there, or check its
  recent commits touching the `fisherman` path). If it has, any frontend
  build after that bump is affected too, not just a direct consumer of
  this repo's releases.

## 2. Stop the bleeding: pull the GitHub Release

This does not delete the tag or the git history, only the binaries and
the release page, so it is reversible and safe to do first.

```bash
gh release delete <tag> --repo tuna-os/fisherman --yes
```

`go install` and anything resolving `@latest` from tags will no longer
offer it. The git tag itself still exists; leave it — deleting a tag
that `release-publish.yml` or a human may have already fetched into a
local clone just creates a second confusing problem (a dangling ref
other mirrors still have) without undoing anything the release page
deletion didn't already undo.

## 3. Decide: patch forward, or re-point `prod`

**Patch forward (default).** Fix the bug on `dev` as a normal PR, let it
soak and promote itself through `promote.yml`. This is almost always
right: `promote.yml` refuses to rewind `prod`, so this is the path of
least resistance and leaves the full history intact. Prefer this unless
the bad release is actively being installed by someone right now and a
fix cannot land and soak fast enough.

**Re-point `prod` at the last known-good tag.** Only when the bad release
is actively harmful and a forward fix is not fast enough:

```bash
# Confirm the target is actually an ancestor of the current prod tip —
# it will be, since prod only ever moves forward, but check before pushing.
git fetch origin prod
git merge-base --is-ancestor <last-good-sha> origin/prod && echo "ok: ancestor"

# This is a force-push to a protected branch. It needs the same repo-admin
# bypass that docs/BRANCH_PROTECTION.md documents for hotfixes, and it does
# NOT rewind history other clones have already fetched -- treat this as a
# "point prod's ref backward," not an undo.
git push --force origin <last-good-sha>:refs/heads/prod
```

Do not run `promote-reconcile.yml` for this. That workflow repairs a
*divergence* (prod and dev sharing no common ancestry after a squash) —
a different failure from "prod points at a commit with a real bug in
it." Running it here does nothing useful and risks confusing the next
person who reads `CONTRIBUTING.md`'s divergence section and assumes that
is what happened.

After moving `prod` back, the next ordinary commit on `dev` that passes
the gate will promote forward again. If the bad commit is still on `dev`
too (it usually is — `prod` only ever reflects a `dev` commit), the fix
still has to land on `dev` before the next promotion, or the same bug
promotes right back.

## 4. Tell `tuna-os/bootc-installer`

If step 1 found the submodule pointer already bumped past the bad tag,
file an issue there (or, if you have operations coverage there this
session, a PR) pointing its submodule back to the last known-good
fisherman tag. This repo's rollback does not reach consumers who already
pinned the bad commit.

## 5. Write it up

Use `runbooks/incident-template.md`. Two prior incidents are already
documented in `CONTRIBUTING.md` and `.goreleaser.yml`'s own comments
(#220's squash-divergence, and v0.3.0's tag-with-no-publish) — add this
one to that same institutional memory rather than letting it live only
in a closed issue.

## Why there is no `fisherman rollback` command

Nothing in this repo takes back an install already written to a disk —
by the time `bootc install to-filesystem` has run, the target's own
bootloader rollback (the 2 GiB ESP sized for "booted entry + rollback +
staged upgrade", see `README.md`) is the only lever, and that is
`bootc`'s contract, not fisherman's. This runbook is scoped to the
release artifact only.
