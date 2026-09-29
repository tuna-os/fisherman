# fisherman Roadmap

**Last updated**: 2026-09-25 | **Maintainer**: tuna-os (hanthor) / installer maintainers

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
| `bootc-installer` | Submodule → `tuna-os/fisherman`, branch `dev` |
| `wootc` | Submodule → `tuna-os/fisherman`, pinned commit |
| `tromso` | Live-ISO install path; builds a patched binary for some test cells |
| `tunaOS`, `xfce-linux` | Recipe schema and install-path references |

This is the highest fan-in component in the org, and the only one whose bugs
write to a stranger's disk. Changes here are evaluated against that table, not
against a single caller.

---

## Current Status (2026-09-25)

- **`dev` is the working branch and the default branch.** There is no `main`.
- **The release path was dispatched and now runs automatically.** `v0.3.0`
  and `v0.4.0` both published 2026-09-19 with working assets. Promotion is no
  longer a manual cut-then-merge: `promote.yml` fast-forwards `prod` to any
  `dev` commit that has sat green for 30 minutes, tags it, and publishes the
  binaries (see `CONTRIBUTING.md`'s Releasing section). `dev` is **4 commits
  ahead** of `v0.4.0`, not behind any release.
- **`prod` is caught up.** The squash-merge/diverged-history problem that
  stalled `v0.3.0`'s promotion (#226) was fixed in #228 by switching the
  promotion PR to a merge commit; `prod` is now 4 commits behind `dev`, 0
  ahead — not the 255-ahead gap recorded here previously.
- **6 branches** exist in the repository (`gh api repos/tuna-os/fisherman/branches --paginate`), down from the 80 recorded at the last refresh.
- **`CHANGELOG.md` has no `[0.3.0]`/`[0.4.0]` heading yet** — the entries that
  shipped in those releases are still sitting under `[Unreleased]`. Tracked in
  #239.
- Historical context, superseded by the above: `release-cut.yml` (manual, on
  `dev`) used to compute the next semver, push the tag, and open the
  `dev` → `prod` PR; the tag push triggered `release-publish.yml`, which ran
  `go test -race ./...` and then GoReleaser
  (`linux/amd64`, `linux/arm64`, `CGO_ENABLED=0`). That two-step manual process
  is what shipped `v0.3.0`; `promote.yml` replaced it for everything after.
- **Fixes reach consumers unevenly.** `wootc` pins `e2b31660` (2026-08-11) —
  now **70 behind `dev`, 1 ahead**, so diverged rather than merely stale — and
  builds fisherman from that submodule in its own CI. `aaca69d` (#161), which
  moved the root-built, `LD_PRELOAD`ed SELinux bypass shim out of predictable
  world-writable `/tmp` paths, merged on 2026-08-23 and is in the behind set.
  So the shipped Windows-hosted installer still builds the pre-fix shim.
  `bootc-installer` pinned a different upstream entirely until D1 was resolved;
  its URL now points here (tuna-os/bootc-installer#73, 2026-09-07) and its pin
  moved to `027fa25c` (2026-08-29), **15 behind `dev`, 0 ahead**, which does
  carry #161.

**Direction of travel since 2026-08-23**, recorded because every distribution
line has moved the wrong way while this file said the work was near-term:
`prod` distance 230 → 245 → 255, branches 68 → 79 → 80, untagged delta
164 → 179 → 189, `wootc` pin distance 45 → 60 → 70 (measured 08-23, 09-03,
09-10). The one line that moved the right way is `bootc-installer`, whose pin
went from a diverged commit reached via the fork's URL (`6092be78`, 140 behind /
14 ahead) to a commit 15 behind `dev`.

**The `prod`-distance and branch-count trend broke on 09-19**: two releases
shipped, `prod` distance collapsed from 255 to 4, and branches dropped from 80
to 6. The `wootc` pin distance is not re-measured here — it needs its own
refresh (#206).

---

## Open decisions

These are not tasks; they are questions that block the tasks under them.
Tracked in #162.

### D1 — Where does fisherman live? — **RESOLVED 2026-09-05: here**

`tuna-os/fisherman` is the origin and is actively maintained.
[`projectbluefin/fisherman`](https://github.com/projectbluefin/fisherman) is a
fork kept in sync with it. Send fixes here.

This was blocking because the repository gave three answers at once — a GitHub
description reading `⚠️ MOVED → github.com/projectbluefin/fisherman`, a
`README.md` calling this repository the origin, and PR #59 merging 14
install-path fixes *from* the fork. A contributor could not tell where to send a
patch, and the most visible surface said the repo was dead.

Aligning the four surfaces D1 named:

| surface | state |
|---|---|
| `README.md` | already correct — says origin, actively maintained |
| `AGENTS.md` | already correct — "This repository is the origin." |
| `tuna-installer-kde`'s README | already correct — links `tuna-os/fisherman` |
| `bootc-installer` submodule URL | realigned to `tuna-os/fisherman` (see below) |
| **GitHub repository description** | **a repo setting, not a file — must be set by a maintainer** |

The description is the one surface no commit can reach. Until it is changed, the
first thing a visitor reads still contradicts every file in the tree, which is
the whole of what made D1 blocking.

`bootc-installer` pinned `6092be78` via the fork's URL. That commit is present
in this repository (it is the merge of projectbluefin#10), so repointing the URL
resolves to the identical tree — no code moves, the submodule simply fetches the
canonical remote. **Which** commit each consumer pins is D2 below and is
deliberately not touched here.

What remains open after the resolution, verified 2026-09-10:

- the GitHub description is still `⚠️ MOVED → github.com/projectbluefin/fisherman`
  (11 forks, 24 open issues read it first);
- `wootc`'s `.gitmodules` points at `tuna-os/fisherman`, but `wootc`'s
  `ROADMAP.md` still links fisherman as `projectbluefin/fisherman`.

`bootc-installer` has since moved its pin as well as its URL:
tuna-os/bootc-installer#73 (2026-09-07) advanced it from `6092be78` to
`027fa25c`, so the paragraph above describes the URL realignment, not the
current pin.

### D2 — What does a consumer pin?

A release with assets, a tag, or a branch — and who is responsible for
advancing dependents' pins when a fix lands. Today the answer differs per
consumer, which is why security work on `dev` has no defined path into shipped
installers.

This is no longer hypothetical. #161 merged on 2026-08-23 and, eighteen days
later, is still absent from `wootc`, the consumer that builds this repository's
submodule into a shipped installer in its own CI. It reached `bootc-installer`
only because a D1 URL fix happened to advance the pin, not because a policy
said it should. The cost of leaving D2 open is now measured in weeks
of exposure per fix, for every fix.

### D3 — What is `prod` for? — **answered by the tooling, and resolved**

`promote.yml` fast-forwards `dev` into `prod` once a commit sits green for 30
minutes; `release-cut.yml`/`release-publish.yml` cut the tag and publish
assets. `prod` is the release branch and is now 4 commits behind `dev`, not
the 255-commit gap recorded here previously — releases resumed with v0.3.0
and v0.4.0 (2026-09-19).

---

## Near-term (through 2026 Q3)

Q3 closes 2026-09-30. Every distribution row below moved away from done between
08-23 and 09-10; only the D1 and SELinux rows moved toward it.

| Item | Tracking | Status |
|------|----------|--------|
| ~~Dispatch `release-cut.yml` and cut `v0.3.0` from `dev` with assets~~ | #162, #205 | ✅ Done — v0.3.0 and v0.4.0 both published 2026-09-19; `promote.yml` now automates every release after |
| State the pin policy (D2), then move `wootc`'s diverged pin onto it — this is what carries #161 into a shipped installer | #162, #206 | 🔴 Open — pin now 70 behind / 1 ahead, was 45 then 60 behind |
| Propagate D1's answer to every surface that still contradicts it | #162 | 🟡 In progress — resolved here (#210) and in `bootc-installer` (#73); GitHub description still says MOVED, `wootc` `ROADMAP.md` still links the fork |
| SELinux is not silently disabled on install | #160 | 🟡 In progress — #161 on `dev` and in `bootc-installer`'s pin, not in `wootc` |
| ~~Move `prod`~~ (D3 resolved — releases resumed, gap collapsed to 4 commits) | #205 | 🟢 Mostly done — `prod` 4 behind, 6 branches remain (down from 80); branch-backlog prune itself may still be open, see #205 |
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
