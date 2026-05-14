# Branch Protection Settings

This file documents the branch protection rules for `tuna-os/fisherman`.

## TL;DR

Configure GitHub branch protection to require **one** status check per
protected branch: **`required-canaries`**. That single check is produced
by `.github/workflows/bootcrew-vm.yml` and aggregates:

- the `lint` job (golangci-lint),
- the `unit-tests` job (`go test -race`),
- and every required-canary E2E install (`dakota` = composefs+systemd-boot,
  `centos-bootc` = ostree+grub2).

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
result of `lint` + `unit-tests` + every `required: true` image in
`tests/bootcrew-matrix.yaml`.

## What runs where

| Workflow | Trigger | Required? | Notes |
|----------|---------|-----------|-------|
| `bootcrew-vm.yml` lint | every PR | yes (via gate) | golangci-lint on `fisherman/` |
| `bootcrew-vm.yml` unit-tests | every PR | yes (via gate) | `go test -race -coverprofile` |
| `bootcrew-vm.yml` vm-boot-required | every PR | yes (via gate) | dakota, centos-bootc |
| `bootcrew-vm.yml` vm-boot-advisory | every PR | **no** | arch-bootc, dakota-luks, debian-bootc, … |
| `bootcrew-nightly.yml` | weekly cron | no | full matrix; comprehensive regression |
| `build-ssh-images.yml` | weekly cron | no | refreshes pre-baked SSH-enabled images |

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
