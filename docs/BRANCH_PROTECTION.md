# Branch Protection Settings

This file documents the branch protection rules for `tuna-os/fisherman`.

## TL;DR

Configure GitHub branch protection to require **one** status check per
protected branch: **`required-canaries`**. That single check is produced
by `.github/workflows/bootcrew-vm.yml` and aggregates:

- the `setup` job, which decides what a pull request has to run,
- the `lint` job (golangci-lint, plus the no-disk script tests such as
  `tests/check-validation.sh` and `tests/test-promote-gate.py`),
- the `unit-tests` job (`go test -race`),
- the `unit-tests-arm64` job (`arm64 unit tests`),
- and every `required: true` image in `tests/bootcrew-matrix.yaml`, run by
  `vm-boot-required`: the `dakota` (composefs + systemd-boot) and
  `centos-bootc` (ostree + grub2) canaries, and the install shapes #423
  made gating.

`setup` can scope the arm64 unit tests and the VM installs out of a pull
request (see [What runs where](#what-runs-where)). `required-canaries`
accepts a skipped job only when `setup` scoped it out; a job skipped
because a dependency failed still fails the gate.

We do **not** list individual matrix jobs in branch protection because that
list would break the moment we add or rename an image. The gate job
exists exactly to give branch protection a stable name to depend on.

## Configuration (GitHub CLI)

```bash
# prod (strict — production deployments cut from this branch)
gh api -X PUT repos/tuna-os/fisherman/branches/prod/protection \
  -f required_status_checks.strict=true \
  -F required_status_checks.contexts='["required-canaries"]' \
  -F enforce_admins=false \
  -F required_pull_request_reviews.required_approving_review_count=1 \
  -F required_pull_request_reviews.dismiss_stale_reviews=true \
  -F restrictions=null \
  -F allow_force_pushes=false \
  -F allow_deletions=false

# dev (same gate; lower review count if you want faster iteration)
gh api -X PUT repos/tuna-os/fisherman/branches/dev/protection \
  -f required_status_checks.strict=true \
  -F required_status_checks.contexts='["required-canaries"]' \
  -F enforce_admins=false \
  -F required_pull_request_reviews.required_approving_review_count=1 \
  -F required_pull_request_reviews.dismiss_stale_reviews=true \
  -F restrictions=null \
  -F allow_force_pushes=false \
  -F allow_deletions=false
```

## Manual Setup (GitHub Web UI)

1. Settings → Branches → Add rule (one rule per branch, `prod` first).
2. Branch name pattern: `prod` (then repeat for `dev`).
3. Enable:
   - ☑ Require a pull request before merging (1 approval, dismiss stale)
   - ☑ Require status checks to pass before merging
     - Search for and add: **`required-canaries`**
   - ☑ Require branches to be up to date before merging
   - ☑ Do not allow bypassing the above settings *(prod only)*
   - ☑ Restrict force pushes
   - ☑ Restrict deletions

## Why one gate instead of N checks?

The previous setup listed individual workflow names (`bootcrew-fast`,
`bootcrew-vm`). Two problems with that:

1. **Brittle.** Renaming a workflow or adding a new required image
   silently *weakens* protection — GitHub treats unknown names as
   "not required" but doesn't warn.
2. **Noisy on flaky images.** Listing every matrix job means a flake in
   one image blocks the merge. We split the matrix into **required**
   canaries (must pass) and **advisory** images (run `continue-on-error`
   so flakes show up in the workflow summary without blocking merges).

The `required-canaries` job in `bootcrew-vm.yml` is the single boolean
result of `setup` + `lint` + `unit-tests` + `unit-tests-arm64` + every
`required: true` image in `tests/bootcrew-matrix.yaml`.

## What runs where

Every push to `dev` or `prod` and the nightly `bootcrew-vm.yml` run on
`dev` get the full suite. `promote.yml` judges the push runs.
Pull requests are narrowed by the `setup` job of `bootcrew-vm.yml`, from
the files the pull request changes:

- **Prose only** (`*.md`, `docs/`, `LICENSE`): `lint` and `unit-tests`.
- **Prose and `tui/` only**: adds `unit-tests-arm64` and `tui-unit-tests`.
- **Anything else**: adds the required VM installs as well.
- **Draft**: `lint` and `unit-tests` only. Marking it ready for review
  starts a new run, scoped by the rules above.
- The advisory VM installs and the store-reuse profile run on a pull
  request only when it changes `tests/bootcrew-matrix.yaml` or
  `scripts/store-reuse-vm.sh` and the VM installs are in scope.

If `setup` cannot read the changed-file list, the arm64, `tui/` and
required VM jobs all run; the advisory jobs still do not.

| Workflow / job | Pull requests | Required? | Notes |
|----------------|---------------|-----------|-------|
| `bootcrew-vm.yml` `lint` | every PR | yes (via gate) | golangci-lint on `fisherman/`, plus the no-disk script tests |
| `bootcrew-vm.yml` `unit-tests` | every PR | yes (via gate) | `go test -race -coverprofile` |
| `bootcrew-vm.yml` `unit-tests-arm64` | not draft, not prose-only | yes (via gate) | build, vet and tests on an arm64 runner |
| `bootcrew-vm.yml` `tui-unit-tests` | not draft, not prose-only | no | `go vet` and `go test -race` for `tui/` |
| `bootcrew-vm.yml` `vm-boot-required` | not draft, changes outside prose and `tui/` | yes (via gate) | every `required: true` image in the matrix |
| `bootcrew-vm.yml` `vm-boot-advisory` | only matrix or store-reuse script changes | **no** | the other `vm_boot: true` images; `continue-on-error` |
| `bootcrew-vm.yml` `store-reuse-advisory` | only matrix or store-reuse script changes | **no** | `store_reuse` profiles, see `docs/SOURCE-REUSE.md` |
| `bootcrew-nightly.yml` | never (weekly cron) | no | full matrix; comprehensive regression |
| `build-ssh-images.yml` | never (weekly cron, and pushes that change its inputs) | no | refreshes pre-baked SSH-enabled images |

## Promoting an advisory image to required

When an image stabilises (no flakes for ~2 consecutive weeks of nightly
runs), edit `tests/bootcrew-matrix.yaml` and add `required: true` next to
its `vm_boot: true` line. The next push will run it under
`vm-boot-required`, blocking merges on it. No branch-protection change
needed — the gate picks it up automatically.

## Demoting a flaky required canary

Same file, remove (or comment) the `required: true` line. The image stays
in the workflow but moves to the advisory bucket so it doesn't block
merges while you investigate.

## Bypass

Only repo admins can bypass; do so only for documentation-only PRs or
hotfix releases where the workflow is broken for unrelated infra reasons
(e.g. a GitHub Actions outage). Always re-run after merging to confirm.
