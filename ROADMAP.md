# fisherman Roadmap

**Last updated**: 2026-11-12 | **Maintainer**: tuna-os (hanthor) / installer maintainers

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
| `bootc-installer` (the GNOME, KDE, COSMIC, Niri and XFCE frontends, one monorepo) | Submodule `fisherman/` → `tuna-os/fisherman`, `.gitmodules` branch `prod`; every Flatpak builds fisherman from that submodule. The frontends run the binary with the recipe on stdin. See [`docs/INTEGRATION.md`](docs/INTEGRATION.md). |
| `wootc` | Submodule → `tuna-os/fisherman`, pinned commit |
| `tromso` | Live-ISO install path; builds a patched binary for some test cells |
| `tunaOS`, `xfce-linux` | Recipe schema and install-path references |

This is the highest fan-in component in the org, and the only one whose bugs
write to a stranger's disk. Changes here are evaluated against that table, not
against a single caller.

The four `tuna-installer-*` frontend repositories of the last refresh are now
`frontends/<name>/` in `bootc-installer`, so they share its one pin.

---

## Current Status (2026-11-12)

Measured on 2026-11-12 against `origin/dev` `b5b9250`.

- **Releases ship.** 12 releases since `v0.4.0`, from `v0.5.0` (2026-10-24)
  to `v0.8.0` (2026-11-11), each with `linux/amd64` and `linux/arm64`
  archives and `checksums.txt`. `promote.yml` cut every one: it fast-forwards
  `prod` to a `dev` commit that is green and has sat for 30 minutes, tags it
  and publishes. `release-publish.yml` now pins GoReleaser to an exact
  version (#344).
- **`dev` is 6 commits ahead of `prod`** (`v0.8.0`), 0 behind. The 6 are the
  2026-11-12 merges (#443, #445 to #449).
- **`CHANGELOG.md` lags the releases.** It has no section for `0.5.0` to
  `0.8.0`: `[Unreleased]` holds all 79 commits since `v0.4.0` (13 `feat`, 25
  `fix`). The release notes are generated per tag; the file is not.
- **162 branches** exist (`git ls-remote --heads`), up from 6 at the last
  refresh. Most are agent and PR branches (`claude/` 44, `fix/` 20,
  `quality/` 16, `scanner/` 13, `guide/` 12, `arch/` 12).
- **Consumer pins.** `bootc-installer` pins `b5b9250`, the `dev` tip: 0
  behind `dev`, but 6 ahead of `prod`, so ahead of its own `branch = prod`.
  `wootc` pins `e2b31660` (2026-08-11): **164 behind `dev`, 1 ahead**. It
  still builds the shim from before #161 (#206).
- **The repository description** no longer says "MOVED"; D1 is closed on
  every surface this file named, except `wootc`'s `ROADMAP.md`, which this
  refresh did not re-measure.

What landed since the last refresh (2026-09-25), by area:

- **Fail before the wipe.** One rule set for `validate` and the install
  (#277), strict recipes that refuse unknown keys, target eligibility, a
  manual-layout preflight (#429), image-ref syntax (#445), image sources the
  install cannot use and the SELinux workaround (#448), all before the first
  disk step.
- **Facts for frontends.** `fisherman probe --json` with host bootc
  capabilities, GPU ids and the DMI model (#428), source-scoped capabilities
  (#436) and a bootc store inventory (#437).
- **Results a frontend can act on.** Stable failure codes, `step_id`,
  `disk_changed` and `cleanup` on the `error` event (#449); a `receipt` event
  (#441) and `fisherman verify` against the booted system (#438).
- **Install paths.** Offline OCI media with a pre-partition preflight
  (#340, #345, #393, #398, #399); installs from the host's bootc store,
  experimental (#443); locale, keyboard and time zone (#435).
- **Docs held to code.** New pages: VALIDATE, TROUBLESHOOTING, SECURITY,
  SCAN, and now RECIPE, CLI and INTEGRATION. Sync tests fail when VALIDATE,
  PROBE, JSON-PROGRESS-FORMAT, TROUBLESHOOTING, RECIPE, CLI or INTEGRATION
  disagrees with the code.

### Trend since the last refresh

| Measure | 2026-09-25 | 2026-11-12 | Direction |
|---|---|---|---|
| `dev` ahead of `prod` | 4 | 6 | flat: `prod` follows within a day |
| Releases with assets | 2 (`v0.3.0`, `v0.4.0`) | 14 (`v0.3.0` to `v0.8.0`) | toward done |
| Branches | 6 | 162 | away from done |
| `wootc` pin behind `dev` | 70 | 164 | away from done |
| `bootc-installer` pin behind `dev` | 15 | 0 | toward done |

---

## Open decisions

These are not tasks; they are questions that block the tasks under them.
#162, which tracked them, is closed; each open one now has its own issue.

### D1 — Where does fisherman live? — **RESOLVED 2026-09-05: here**

`tuna-os/fisherman` is the only origin. Send issues and fixes here.
[`projectbluefin/fisherman`](https://github.com/projectbluefin/fisherman) is a
fork; its changes reach fisherman only through a pull request here, and
consumers pin `tuna-os/fisherman` (#303).

### D2 — What does a consumer pin? — **partly answered**

`bootc-installer` answered for itself: one gitlink, moved by Renovate, that
tracks `prod`. [`docs/INTEGRATION.md`](docs/INTEGRATION.md) says to pin a
release (a tagged commit on `prod`). `wootc` has not moved onto that rule, and
#161 has now been absent from it for 81 days (#206).

### D3 — What is `prod` for? — **RESOLVED**

`prod` is the release branch. `promote.yml` owns it; nobody pushes it by
hand.

### D4 — Downstream product knowledge in core

`/var/lib/superiso-store` is still exposed whenever it exists
(`internal/install/bootc.go`). Keep that live-media default, or require the
ISO's recipe to name its store in `additionalImageStores` (#308).

---

## Historical: 2026 Q3 (closed 2026-09-30)

| Item | Tracking | Outcome |
|------|----------|---------|
| Cut `v0.3.0` from `dev` with assets | #162, #205 | ✅ Done 2026-09-19; `promote.yml` releases every green `dev` commit since |
| State the pin policy (D2), then move `wootc`'s pin | #162, #206 | 🔴 Not done: `wootc` is 164 behind `dev` |
| Propagate D1's answer to every surface | #162 | ✅ Done for the repository description and this repository |
| SELinux is not silently disabled on install | #160 | ✅ Closed |
| Move `prod` | #205 | ✅ Closed |
| The `plan.md` punch list | `plan.md` | 🟡 Scratch cleanup on fatal paths (`AddPostRemoval`) and the committed binary removal are done; `additionalImageStores` exists, but the `superiso-store` default stays (D4, #308) |

## 2026 Q4 (current, ends 2026-12-31)

| Item | Tracking | Status |
|------|----------|--------|
| Release cadence a dependent can plan against, with a changelog per release | #293 | 🟡 The cadence exists (12 releases since 2026-10-24); `CHANGELOG.md` has no per-release sections after `0.4.0` |
| Recipe schema versioned and published | #293 | 🟡 Published (bootc-installer `shared/recipe/`) and documented ([`docs/RECIPE.md`](docs/RECIPE.md)); unknown keys are refused; no version field yet |
| Destructive-path inventory | #293 | 🟡 Every recipe and source check runs before the first disk step, and the `error` event says whether a disk changed (`disk_changed`); there is no enumerated inventory yet |
| Retire downstream patching (`tromso`) | | ⚪ Not re-measured |
| Move `wootc`'s pin | #206 | 🔴 Open |
| Split `main()` | #313, #300 | 🟡 `main()` parses the command line and hands an install to `installer.run` (`cmd/fisherman/orchestrator.go`), which runs the pre-flight, pre-partition, disk, install, post-install and finalize phases, each in its own file with injected operations (#313); the Phase-1 interface (#300) is open |

## Forward look: 2027 Q1 (proposed, not committed)

- A recipe version field, so a frontend can tell which fields a fisherman
  accepts (#293).
- The Phase-1 interface on top of the install phases (#300).
- composefs installs with a separate `/var` disk (#252, blocked on upstream).
- Image-declared signed boot chains (#342).
- Measured transfer progress with rate and ETA (#380).

---

## How to Contribute

Fixes go to `dev`. Pick something from the Q4 table or from the open issues.
Changes to the install pipeline should say which of the consumers above they
were exercised against.

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
