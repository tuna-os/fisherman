# fisherman Roadmap

**Last updated**: 2026-08-23 | **Maintainer**: tuna-os (hanthor) / installer maintainers

---

## Mission

Be the universal bootc install backend: read a JSON recipe, execute the
partition → format → LUKS → `bootc install to-filesystem` → finalize pipeline,
and do it identically regardless of which frontend called it or which distro's
image is being written. Frontends own the conversation with the user;
fisherman owns the bytes that reach the disk.

Every fisherman install writes to a machine someone already owns. Correctness
and reversibility come before feature breadth, and a defect that can lose data
outranks anything else on this page.

---

## Who depends on this

| Consumer | How it consumes fisherman |
|---|---|
| `tuna-installer-kde` / `-cosmic` / `-niri` / `-xfce` | Frontends that shell out to the `fisherman` binary with a recipe |
| `bootc-installer` | Submodule → `projectbluefin/fisherman`, branch `dev` |
| `wootc` | Submodule → `tuna-os/fisherman`, pinned commit |
| `tromso` | Live-ISO install path; builds a patched binary for some test cells |
| `tunaOS`, `xfce-linux` | Recipe schema and install-path references |

This is the highest fan-in component in the org, and the only one whose bugs
write to a stranger's disk. Changes here are evaluated against that table, not
against a single caller.

---

## Current Status (2026-08-23)

- **`dev` is the working branch and the default branch.** There is no `main`.
- **`prod` has not moved since 2026-05-01**; `dev` is **230 commits ahead** of
  it, zero behind.
- **Latest release is `v0.2.0` (2026-05-08) with no release assets**; `dev` is
  **164 commits ahead** of that tag. No consumer can pin a released artifact,
  so consumers pin branches, commits, or build their own.
- **68 branches** exist in the repository.
- **Open safety work**: `selinuxDisabled` is hardcoded, so every install
  disables SELinux by default (#160). The first related fix landed on `dev`
  the same day (#161).
- **Consumers have diverged.** `wootc` pins `e2b31660` (2026-08-11), which
  GitHub reports as *diverged* from `dev` — 45 behind, 1 ahead. `bootc-installer`
  pins a different upstream entirely.

---

## Open decisions

These are not tasks; they are questions that block the tasks under them.
Tracked in #162.

### D1 — Where does fisherman live? (blocking)

The repository currently gives three answers:

- the GitHub description says `⚠️ MOVED → github.com/projectbluefin/fisherman`;
- `README.md` says this repository is the origin and projectbluefin is a fork
  kept in sync;
- PR #59 (merged 2026-07-26) is titled "sync: merge projectbluefin/fisherman
  main — 14 install-path fixes this fork lacks".

Whichever answer is correct, the description, the README, and
`tuna-installer-kde`'s README must agree, and the two submodule URLs above must
resolve to the same decision. Until then a contributor cannot tell where to send
a fix, and the most visible surface says this repo is dead.

### D2 — What does a consumer pin?

A release with assets, a tag, or a branch — and who is responsible for
advancing dependents' pins when a fix lands. Today the answer differs per
consumer, which is why security work on `dev` has no defined path into shipped
installers.

### D3 — What is `prod` for?

Either it is the release branch and it is 230 commits stale, or it is vestigial
and should be removed. Both are fine; the ambiguity is not.

---

## Near-term (through 2026 Q3)

| Item | Tracking | Status |
|------|----------|--------|
| Settle D1 and make every surface state the same answer | #162 | 🔴 Open |
| Cut a release from `dev` with binaries attached | #162 | 🔴 Open — last release 05-08, zero assets |
| State the pin policy (D2), then move `wootc`'s diverged pin onto it | #162 | 🔴 Open |
| SELinux is not silently disabled on install | #160 | 🟡 In progress — #161 landed |
| Resolve `prod` (D3) and prune the branch backlog | — | ⬜ Not started — 68 branches |
| Land the `plan.md` punch list: scratch-dir leak on fatal paths, generic `additionalImageStores` in place of the hardcoded `superiso-store` probe, committed-binary removal | `plan.md` | 🟡 In progress |

## Mid-term (2026 Q4)

| Item | Why |
|------|-----|
| Release cadence a dependent can plan against, with a changelog per release | Consumers currently track a branch because there is nothing else to track |
| Recipe schema versioned and published | Four frontends and two installers generate recipes; `fisherman-recipe.schema.json` is copied downstream |
| Retire downstream patching | `tromso` builds a patched binary for bootcDirect and carries a composefs hostname workaround — both belong upstream or in the recipe |
| Destructive-path inventory | Every path that can lose data, enumerated and gated, so dependents can cite it — `wootc`'s 1.0 gate needs exactly this evidence |

---

## How to Contribute

Fixes go to `dev`. Pick something from the near-term table or from `plan.md`,
which carries the current dev-branch punch list with the reasoning behind each
item. Changes to the install pipeline should say which of the consumers above
they were exercised against.

Read `docs/BRANCH_PROTECTION.md` before touching CI configuration.

---

## Roadmap Governance

This roadmap is maintained by the strategist agent. Updates are published after
major milestones or quarterly. Propose changes via PR to this file with an issue
reference. A tracker cited here that closes must move its row in the same PR, or
the row must name a successor.

---
*Generated by strategist agent at ACMM L6. Updated 2026-08-23.*
