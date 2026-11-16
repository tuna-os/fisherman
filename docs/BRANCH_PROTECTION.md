# Branch Protection Settings

This file documents the rules GitHub enforces on `tuna-os/fisherman`, and
the gate that `promote.yml` adds before `prod`. The rules are rulesets.
Classic branch protection is off on every branch.

## TL;DR

There are two gates.

1. **Merge to `dev`.** The repository ruleset `tier-a: required checks`
   (id 26122349) requires the `lint`, `unit-tests` and
   **`required-canaries`** checks on every pull request. It requires no
   approval and no up-to-date branch.
2. **Promote to `prod`.** `promote.yml` fast-forwards `prod` only to a
   `dev` commit whose push run of `bootcrew-vm.yml` passed
   `required-canaries` again, and whose other gated workflows passed (see
   `GATED_WORKFLOWS` and `REQUIRED_JOBS` in `promote.yml`).

`required-canaries` aggregates:

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
accepts a skipped job only when `setup` scoped it out. A job skipped
because a dependency failed still fails the gate.

A pull request therefore cannot merge with a failing VM install or arm64
unit test that `setup` put in scope. The `dev` push run checks the merged
commit once more with the full suite before `promote.yml` moves `prod`.

## Rules in force

| Ruleset | Scope | Rules |
|---------|-------|-------|
| `baseline: protect default branch` (org, id 26122157) | default branch (`dev`) | no deletion, no force push |
| `tier-a: required checks` (repository, id 26122349) | `dev` | status checks `lint`, `unit-tests` and `required-canaries` (GitHub Actions) |

`prod` has no rules. `promote.yml` pushes to it with the workflow token
(`git push origin "$SHA:refs/heads/$PROD_BRANCH"`), and a "require a pull
request" rule would reject that push. Nobody else pushes to `prod`.

Read the live rules with:

```bash
gh api repos/tuna-os/fisherman/rules/branches/dev
gh api repos/tuna-os/fisherman/rules/branches/prod   # []
```

## Why one aggregate instead of N checks?

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
`required: true` image in `tests/bootcrew-matrix.yaml`. `promote.yml`
names it, not the matrix jobs, for the same reason.

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
| `bootcrew-vm.yml` `lint` | every PR | yes (merge and promote) | golangci-lint on `fisherman/`, plus the no-disk script tests |
| `bootcrew-vm.yml` `unit-tests` | every PR | yes (merge and promote) | `go test -race -coverprofile` |
| `bootcrew-vm.yml` `unit-tests-arm64` | not draft, not prose-only | yes (via `required-canaries`) | build, vet and tests on an arm64 runner |
| `bootcrew-vm.yml` `tui-unit-tests` | not draft, not prose-only | no | `go vet` and `go test -race` for `tui/` |
| `bootcrew-vm.yml` `vm-boot-required` | not draft, changes outside prose and `tui/` | yes (via `required-canaries`) | every `required: true` image in the matrix |
| `bootcrew-vm.yml` `vm-boot-advisory` | only matrix or store-reuse script changes | **no** | the other `vm_boot: true` images; `continue-on-error` |
| `bootcrew-vm.yml` `store-reuse-advisory` | only matrix or store-reuse script changes | **no** | `store_reuse` profiles, see `docs/SOURCE-REUSE.md` |
| `bootcrew-nightly.yml` | never (weekly cron) | no | full matrix; comprehensive regression |
| `build-ssh-images.yml` | never (weekly cron, and pushes that change its inputs) | no | refreshes pre-baked SSH-enabled images |

## Promoting an advisory image to required

When an image stabilises (no flakes for ~2 consecutive weeks of nightly
runs), edit `tests/bootcrew-matrix.yaml` and add `required: true` next to
its `vm_boot: true` line. The next push will run it under
`vm-boot-required`, and `required-canaries` then blocks merges and
promotion on it.
No ruleset change is needed.

## Demoting a flaky required canary

Same file, remove (or comment) the `required: true` line. The image stays
in the workflow but moves to the advisory bucket so it doesn't block
merges or promotion while you investigate.

## Bypass

Only repo admins can bypass; do so only for documentation-only PRs or
hotfix releases where the workflow is broken for unrelated infra reasons
(e.g. a GitHub Actions outage). Always re-run after merging to confirm.
