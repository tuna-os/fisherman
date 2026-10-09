# Incident report template

Copy this into a new issue titled `incident: <short description>` when a
release, promotion, or install-pipeline failure reaches `prod`, a tagged
release, or a real disk. Skip it for ordinary bugs caught in review or CI
on `dev` — this is for things that got out.

Two incidents already live in institutional memory this way, informally:
`CONTRIBUTING.md`'s divergence section (#220, a squashed promotion PR) and
`.goreleaser.yml`'s `before.hooks` comment (v0.3.0, a tag with no
published release). Neither has a self-contained writeup; this template
is so the next one does.

---

## Summary

One or two sentences: what broke, and what a user or downstream consumer
(`tuna-os/bootc-installer`'s submodule pin, a `go install` user, a
machine mid-install) actually experienced.

## Timeline

UTC timestamps. Include at least:

- when the bad commit landed on `dev`
- when it promoted (or was hand-cut) to `prod`
- when it was noticed, and how (CI, a bug report, a failed install)
- when the release was pulled or `prod` was repointed
- when a fix shipped

## Root cause

Not just the proximate bug — why the gate (`promote.yml`'s required
checks, the VM matrix in `tests/bootcrew-matrix.yaml`, branch protection
per `docs/BRANCH_PROTECTION.md`) did not catch it. One of:

- the gate was bypassed (`force: true`, or a repo-admin branch-protection
  override) — say by whom and why that was judged necessary at the time
- the gate ran and passed, but didn't test the failing path — say which
  canary or check should have and didn't
- the gate ran and failed, but the failure was misread as unrelated

## Impact

Who was affected and for how long. If `tuna-os/bootc-installer` had
already bumped its submodule pointer to the bad commit, say so and link
the bump.

## Resolution

What was actually done — link the PR that fixed `dev`, the `gh release
delete` or `prod` repoint from
`runbooks/bad-release-rollback.md`, and any downstream fix (a
submodule-pointer PR in `bootc-installer`).

## Prevention

Concrete, not aspirational. A new canary added to
`tests/bootcrew-matrix.yaml`, a new required check, a change to
`promote.yml`'s `REQUIRED_CHECKS`, or an explicit decision that this
class of failure is accepted risk and why. If the fix is a workflow file
under `.github/workflows/`, say so explicitly — that fix needs a human or
an agent with workflow-write permission to land; do not let that
requirement get lost between the incident issue and the PR that
addresses everything else.
