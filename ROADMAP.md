# fisherman Roadmap

**Last updated**: 2026-09-03 | **Maintainer**: tuna-os (hanthor) / installer maintainers

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

## Current Status (2026-09-03)

- **`dev` is the working branch and the default branch.** There is no `main`.
- **`prod` has not moved since 2026-05-01**; `dev` is **245 commits ahead** of
  it, zero behind.
- **Latest release is `v0.2.0` (2026-05-08), 118 days ago**; `dev` is **179
  commits ahead** of that tag. No consumer can pin a released artifact, so
  consumers pin branches, commits, or build their own.
- **79 branches** exist in the repository.
- **The release path is complete and has never been dispatched.**
  `release-cut.yml` (manual, on `dev`) computes the next semver, pushes the
  tag, and opens the `dev` → `prod` PR; the tag push triggers
  `release-publish.yml`, which runs `go test -race ./...` and then GoReleaser
  (`linux/amd64`, `linux/arm64`, `CGO_ENABLED=0`). `CHANGELOG.md` carries a
  populated `[Unreleased]` section. Everything above is one dispatch away.
- **Fixes are not reaching consumers.** `wootc` pins `e2b31660` (2026-08-11) —
  now **60 behind `dev`, 1 ahead**, so diverged rather than merely stale — and
  builds fisherman from that submodule in its own CI. `aaca69d` (#161), which
  moved the root-built, `LD_PRELOAD`ed SELinux bypass shim out of predictable
  world-writable `/tmp` paths, merged on 2026-08-23 and is in the behind set.
  So the shipped Windows-hosted installer still builds the pre-fix shim.
  `bootc-installer` pins a different upstream entirely.

**Direction of travel since 2026-08-23**, recorded because every line moved the
wrong way while this file said the work was near-term: `prod` distance
230 → 245, branches 68 → 79, untagged delta 164 → 179, `wootc` pin distance
45 → 60.

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

`README.md` has since answered for this repository — it states plainly that
this is the origin, actively maintained on `dev`, and that projectbluefin is a
fork kept in sync. Nothing else has moved. Verified 2026-09-03:

- the GitHub description is still `⚠️ MOVED → github.com/projectbluefin/fisherman`,
  and it is the surface a visitor reads first — before the README, and the only
  one visible from search results, the org listing, and every cross-repo link;
- `wootc`'s `.gitmodules` points at `tuna-os/fisherman` while `wootc`'s
  `ROADMAP.md` links fisherman as `projectbluefin/fisherman`;
- `bootc-installer`'s submodule still tracks `projectbluefin/fisherman`
  branch `dev`.

So D1 is settled in the one place that already agreed with itself and
unsettled in all three places it is actually read from. The repository has 10
forks and 26 open issues while its own description tells every one of those
readers it is dead.

### D2 — What does a consumer pin?

A release with assets, a tag, or a branch — and who is responsible for
advancing dependents' pins when a fix lands. Today the answer differs per
consumer, which is why security work on `dev` has no defined path into shipped
installers.

This is no longer hypothetical. #161 merged on 2026-08-23 and, eleven days
later, is still absent from the only consumer that builds this repository's
submodule in its own CI. The cost of leaving D2 open is now measured in weeks
of exposure per fix, for every fix.

### D3 — What is `prod` for? — **answered by the tooling**

`release-cut.yml` opens a PR from `dev` into `prod` as part of cutting a
release. `prod` is the release branch; it is 245 commits stale because releases
stopped, not because it is vestigial. No decision is outstanding — cutting a
release moves it, and the row below tracks that rather than the question.

---

## Near-term (through 2026 Q3)

Q3 closes 2026-09-30. Every row below that moved between 08-23 and 09-03 moved
away from done.

| Item | Tracking | Status |
|------|----------|--------|
| Dispatch `release-cut.yml` and cut `v0.3.0` from `dev` with assets | #162, #205 | 🔴 Open — 118 days since v0.2.0; tooling ready, never dispatched |
| State the pin policy (D2), then move `wootc`'s diverged pin onto it — this is what carries #161 into a shipped installer | #162, #206 | 🔴 Open — pin now 60 behind / 1 ahead, was 45 behind |
| Propagate D1's README answer to the consumers that still contradict it | #162, #206 | 🟡 In progress — settled here, unsettled in `wootc` and `bootc-installer` |
| SELinux is not silently disabled on install | #160 | 🟡 In progress — #161 landed on `dev`, not in any consumer |
| Move `prod` (D3 answered — it is the release branch) and prune the branch backlog | #205 | ⬜ Not started — `prod` 245 behind, 79 branches |
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

Updates are published after major milestones or quarterly. Propose changes via
PR to this file with an issue reference. A tracker cited here that closes must
move its row in the same PR, or the row must name a successor.

Status figures in this file are measured, not estimated. Each refresh should
re-measure the branch distances, the untagged delta, the branch count, and each
consumer's pin distance, and record the direction of travel since the previous
refresh — a number that only ever appears once cannot show drift.
