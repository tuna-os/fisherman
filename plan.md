# Dev-branch cleanup plan

Punch list from review of dev (20 commits ahead of prod). Ordered by impact.

## 1. Fix scratch-dir leak on error paths (regression from `ffb143b`)

**Problem:** `defer os.RemoveAll(scratchDir)` in `main.go:493` never fires when
`fatal()` is reached, because `os.Exit(1)` skips deferred functions. On every
fatal error after `prepareScratchDir`, the OCI cache (~image size) is left on
the target disk.

**Fix:**
- Have `fatal()` run a final-cleanup step after `cleanup.Run()` that removes any
  registered "post-cleanup paths". Reintroduce `Cleanup.AddPostRemoval(path)`
  whose paths are removed **after** all unmounts and LUKS close, not interleaved
  with them (which was the bug the original commit fixed).
- Register `scratchDir` via `AddPostRemoval` instead of relying on `defer`.
- Add a regression test asserting that registered post-removal paths are
  deleted only after all unmounts.

## 2. Replace hardcoded `/var/lib/superiso-store` with a generic mechanism

**Problem:** `bootc.go` hardcodes `/var/lib/superiso-store` detection in two
places. fisherman should not know about a downstream ISO product.

**Fix:**
- Add `AdditionalImageStores []string` to `Recipe` (JSON
  `additionalImageStores`). Each entry is a host path bind-mounted read-only
  into the bootc container and listed under
  `storage.options.additionalimagestores` in the generated `storage.conf`.
- Remove all `superiso-store` strings and `os.Stat` probes from `bootc.go`.
- The existing caller-supplied `CONTAINERS_STORAGE_CONF` env override is
  retained as the pre-existing escape hatch.
- `writeLiveStorageConf` becomes `writeAdditionalStoresConf(scratchDir, paths)`.
- Update `bootcViaContainer` and `bootcToDiskViaContainer` to call a single
  shared helper `appendImageStoreArgs(podmanArgs, scratch, stores) ([]string, cleanupFn)`.
- Downstream (SuperISO / tacklebox) sets `additionalImageStores:
  ["/var/lib/superiso-store"]` in the recipe — no special-casing in fisherman.

## 3. Remove committed binary `fisherman/fisherman-overlay`

4.2 MB ELF in the repo since #23. Not in `.gitignore` (only `fisherman/fisherman` is).

- `git rm fisherman/fisherman-overlay`
- Update `.gitignore` to `fisherman/fisherman*` so future siblings don't sneak
  back in.

## 4. Plumb scratch dir through `skopeoExportOCI` TMPDIR

`bootc.go:627` hard-codes `TMPDIR=/var/fisherman-tmp`. On a live ISO that path
lives on the constrained overlay/tmpfs — the exact case the surrounding
scratch-redirect plumbing is supposed to fix.

- Change `SkopeoExportOCIFn` signature to `func(image, destDir, tmpdir string) error`.
- Callers pass `opts.scratchDir()`.

## 5. Move `writeLiveStorageConf` temp file out of the OCI cache dir

Currently the generated `fisherman-storage-*.conf` lands in `scratchDir`,
which is also the parent of `oci-cache/`. Put it under
`scratchDir/fisherman-conf/` so `ls` of the OCI cache stays clean and a
future `RemoveAll(ociDir)` doesn't surprise anyone.

## 6. Restore regression test for cleanup ordering

The deleted `TestCleanup_RemovesRegisteredPathsAfterUnmount` covered the old
contract. Replace with `TestCleanup_PostRemovalsHappenAfterAllUnmounts`
covering the new `AddPostRemoval` semantics from §1.

## 7. Move top-level docs into `docs/`

`BOOTCREW_JUST_REFACTOR.md`, `BRANCH_PROTECTION.md` → `docs/`. `CHANGELOG.md`
stays in the root (conventional).

## 8. Add `ClassifyLine` tests

Small table-driven test in `internal/install/bootc_test.go` so future bootc /
ostree / podman wording changes break a unit test instead of CI substep
output silently going blank.

## 9. Recipe-overridable mount paths (for parallel test runs)

`targetMount` and `luksMapper` are package-level constants. Add optional
recipe fields `targetMount` (default `/mnt/fisherman-target`) and
`luksMapperName` (default `fisherman-root`) so CI can run two installs on the
same host without colliding.

## 10. E2E test robustness

Reviewed the existing matrix (`tests/bootcrew-matrix.yaml`,
`scripts/boot-verify.sh`, `bootcrew-vm.yml`, `bootcrew-nightly.yml`).
Improvements:

- **AdditionalImageStores coverage:** add a CI job that materializes a fake
  offline image store (an `oci:` directory under `/tmp/fake-store`), writes
  a recipe with `additionalImageStores: ["/tmp/fake-store"]`, and asserts
  fisherman invokes `podman run` with `-v /tmp/fake-store:.../ :ro` and a
  `CONTAINERS_STORAGE_CONF` whose generated file lists it under
  `additionalimagestores`. This replaces the implicit (and untested)
  superiso-store probe.
- **Scratch-leak regression test:** unit test asserting fatal-path cleanup
  removes the scratch dir, AND an integration test under `tests/e2e/` that
  runs the binary against a deliberately-failing recipe and asserts no
  `.fisherman-scratch` remains on the target loop image.
- **Substep classification:** the new ClassifyLine table-driven test (§8)
  also gets fed real captured `bootc install` log excerpts from
  `tests/fixtures/bootc-logs/*.txt` so we catch wording drift in upstream
  bootc.
- **boot-verify.sh hardening:** currently relies on a single SSH probe with
  a fixed timeout. Improve by:
  - tee'ing the QEMU serial console to `${ARTIFACTS}/serial.log` always
    (today only on failure),
  - dumping `journalctl -b --no-pager` and `bootctl status` on failure into
    the artifacts dir for post-mortem,
  - adding a `--keep-vm-on-failure` flag so a maintainer can attach a
    debugger to a still-running guest.
- **Recipe-overridable mount paths (§9)** is also a testing win: lets
  bootcrew-vm.yml run two recipes in parallel on the same runner.
- **Deterministic temp paths:** today scratch lives at `/var/fisherman-tmp`
  or `<target>/.fisherman-scratch`. Add a `--scratch-dir` CLI flag (already
  plumbed through `Options.ScratchDir`) so tests can point scratch at a
  tmpfs they own and assert post-run cleanliness without racing other runs.
- **`go test -race ./...` in CI:** progress-pipe goroutines and the
  pull-output scanner all touch shared state; flagging this catches
  regressions before they hit the install path.

## 11. Adopt projectbluefin/dakota-iso E2E patterns

The dakota-iso repo (`.github/workflows/test-luks-install.yml` + the
`luks-test-qemu` justfile recipes) already runs the highest-fidelity E2E
test that exercises fisherman end-to-end: builds the live ISO, boots it in
QEMU, SCPs a recipe in, runs fisherman over SSH, reboots into the
installed disk, injects the LUKS passphrase at the Plymouth prompt via the
QEMU monitor `sendkey` channel, and verifies the system reaches login.

What we should pull into fisherman directly (in order of value/effort):

1. **`screendump` via QEMU monitor at three checkpoints** — live-ready,
   plymouth-prompt, final-boot. PPM files are tiny and convert to PNG with
   `convert` (already in standard CI images). Already partially adopted in
   §10; extend to all three checkpoints.
2. **`continue-on-error: true` on the heavy E2E step + always-run artifact
   upload step**. We added the always-on upload in §10; the
   continue-on-error semantics also matter so a flaky boot still leaves
   screenshots + journal in the PR.
3. **Two-stage boot test (live ISO → installed disk)** rather than only
   verifying the installed disk. Catches regressions where the install
   succeeds but the resulting disk doesn't boot — the case the recent
   scratch-leak commit (§1) was chasing. Requires building a fisherman ISO
   in CI, which is a bigger lift; track separately.
4. **SCP-the-recipe-and-SSH-fisherman** instead of running fisherman
   directly against a loop device. This is the *real* code path the
   user-facing installer goes through and would have caught the
   `superiso-store` and `storage.conf` regressions before they shipped.
5. **PR comments with pass/fail + screenshot links to an orphan
   `ci-screenshots` branch.** Visual review beats log diving for boot
   regressions. Cheap to adopt: the dakota-iso workflow's `commit_png`
   helper is reusable as-is.
6. **`free-disk-space` action**. GitHub-hosted runners have ~14 GiB free
   by default; a real ISO + qcow2 disk + OCI cache trips that quickly.
   Dakota frees ~30 GiB with `jlumbroso/free-disk-space@…`.

Nothing in the above requires changes to fisherman code; the integration
points are all in `.github/workflows/` and `scripts/`. Open as a follow-up
PR titled "adopt dakota-iso E2E patterns" once these cleanup items land.

## 12. AdditionalImageStores E2E coverage (replaces former §9)

Build on §2's unit tests (`appendImageStoreArgs`) with an integration
test that runs fisherman against a real fake-store layout:

```
testdata/fake-store/
  storage.conf
  overlay-images/
    <a real OCI dir produced by `skopeo copy docker://hello-world …`>
```

The CI job:
1. Creates the layout (skopeo copy of a tiny public image).
2. Writes a recipe with `image: containers-storage:hello-world` and
   `additionalImageStores: ["/tmp/fake-store"]`.
3. Stubs `bootc` with a script that echoes its argv and exits 0.
4. Runs `fisherman recipe.json` and asserts the echoed argv contains
   `-v /tmp/fake-store:/tmp/fake-store:ro` and
   `CONTAINERS_STORAGE_CONF=/var/tmp/fisherman-conf/...`.
5. Reads the generated storage.conf out of the scratch dir and asserts
   it lists `/tmp/fake-store` under `additionalimagestores`.

This replaces the implicit untested behavior the old superiso probe relied
on, and gives downstream (SuperISO / tacklebox) a concrete contract.

Deferred (not in this round): elapsed-time logging wrapper around
`runner.HostArgs` — useful but mechanical, separate PR.
