# Explicit Flatpak target intent (staged implementation)

This is an opt-in native-host backend contract, not an end-to-end ISO Builder feature. A Flatpak-packaged frontend may already launch the backend on the host: the released GNOME frontend does so. The published backend pin and frontend intent transport still need updating. Explicit replacement when the backend itself runs in a sandbox fails before partition work; old recipes and inherit retain their existing behavior.
Existing `flatpaks` recipes, including their empty-list fallback behavior, remain
unchanged when `flatpakIntent` is absent. Current frontends and browser media do
not yet emit this contract.

```json
{
  "flatpakIntent": {
    "version": 1,
    "live": { "mode": "inherit" },
    "target": {
      "mode": "replace",
      "apps": [
        {
          "id": "org.mozilla.firefox",
          "branch": "stable",
          "arch": "x86_64",
          "source": {
            "kind": "remote",
            "identity": "https://dl.flathub.org/repo/"
          }
        }
      ]
    }
  }
}
```

`inherit` defers unchanged image/legacy policy, even when that policy is unknown.
`replace` requires an explicit array. `apps: []` selects no target Flatpaks;
it does not select the old fallback set. JSON round trips preserve that array.
Unknown keys within the envelope, unknown versions and unsupported target sources are rejected by recipe validation
before partition work. The data model preserves source identity, but this first consumer rejects bundle target requests rather than treating them as Flathub. Live replace is also rejected because this entrypoint does not apply it. Only live inherit is supported at this stage.

## Target behavior

- Only an empty target Flatpak repository is supported. Existing target data is
  refused, never silently removed or retained under a misleading replace promise. The check is repeated after external commands; final cleanup uses directory-only removal so a concurrently appearing regular file is preserved.
- Selected Flathub refs are installed into a fresh staging repository beneath the
  target's writable `/var/lib`. Commands set `FLATPAK_SYSTEM_DIR` to that repository,
  pass `--system`, and retain the requested branch and architecture.
- This stage downloads selected apps and dependencies. It does not promise
  reuse of apps cached in the live session or offline operation.
- Every selected ref is checked with `flatpak info` before the repository is
  published at the target path with mode 0755 so ordinary users can traverse the system installation. The staging directory remains private while building. Download/deployment failures are fatal and the
  fresh staging tree is removed. No whole-host tar copy is used in this path.
- No hardcoded installer-name stripping occurs: unselected live apps are never
  copied. Non-Flathub/bundle target requests fail explicitly. Future cache reuse
  must use verified source-and-role facts, not a list of guessed installer IDs.
- After successful application, `/etc/systemd/system/flatpak-preinstall.service`
  is masked in the resolved target deployment. This prevents the known image
  preinstall service from restoring deselected defaults. It does not control
  unrelated first-boot services or remove native/image-embedded applications.
- A receipt containing only version, `appliedScope: target` and the target selection is recorded at `/etc/fisherman/flatpak-intent.json` in the target deployment. It does not claim live intent was applied. Any existing receipt, including dangling or internal symlinks, is refused before downloads. Complete state is published atomically with a no-overwrite hard link, so a concurrent destination is not replaced. A conflicting existing preinstall unit override is also refused.
- ostree, composefs-native and an explicit relative `flatpakVarPath` use the
  existing layout conventions. Traversal and escaping ancestor symlinks are
  rejected before external commands or directory creation. All existing descendant symlinks, including dangling links, are rejected; this intentionally refuses unfamiliar target layouts instead of following them.

A failure while publishing final policy/state remains a failure, even if the app
repository has already been placed. This initial implementation refuses a
nonempty repository on retry; reconciliation/resume support is a later stage.

## Remaining integration dependencies

1. **Tacklebox:** carry this source-aware envelope in BuildRequest and serialize
   explicit empty selections. Bind the manifest to the selected base/overlay
   digest. Do not advertise a consumer that the actual overlay does not contain.
2. **TunaOS live overlay:** supply a nonblocking post-desktop live consumer and a
   status record. Requests cannot remove already-baked live apps in this stage.
   Required live installer bytes must already be present and verified.
3. **bootc-installer/frontends:** update the fisherman release/submodule pin and
   pass the target envelope without reducing it to ID strings. The GNOME release
   [`9f582fe0`](https://github.com/tuna-os/bootc-installer/blob/9f582fe0ecc19561be1115dfef64a59e91ea5072/bootc_installer/utils/fisherman_runner.py)
   already stages and hashes the bundled backend and launches it through
   `flatpak-spawn --host`; its session passes the recipe on stdin. Preserve that
   boundary and hash verification instead of assuming a new helper is required.
   Verify capability, effective UID, mount namespace and target identity before
   partitioning, with bounded fake-host transport tests and subsequent VM proof.
   A fake runner cannot establish actual namespace behavior. Retain the early
   rejection for genuinely sandboxed backend execution. Other desktop frontend
   release paths remain unverified; their repository reads returned 404 during
   this audit. Alternatively add a shared host-side resolver only after verifying
   media/image binding; never import ambient intent for a different image.
4. **ISO Builder:** label image declarations, actual included inventory and requested
   additions separately. IDs from suggestedFlatpaks do not establish source URL,
   role, architecture or deployment. Explicit source resolution is required.
5. **Runtime proof:** selected-only repository content and role/source-preserving
   cache reuse; media transport through all frontends; booted live readiness;
   installed target reboot with the image's preinstall mechanism. No such boot or
   physical installation was run for this patch.

## Checks performed

The production target consumer was exercised using fake external commands and
real temporary target directories. Cases cover explicit none for both layouts,
exact-ref installation, duplicate selection, failed remote/download/deployment
checks, traversal and symlink rejection, existing data, missing/already-masked and
conflicting unit states. Recipe tests cover legacy absence, explicit-none JSON
round trips, unsupported sources and pre-install validation reporting.

Before the review fixes, `go test -p 1 ./...` passed all packages except the existing runner
`TestStopDescendants_EscalatesToSIGKILL` timing test; that failure was reproduced
unchanged on baseline commit `3c040fc48a83979ff8cb5748ce848794c831772f`.
`go vet ./...` and `git diff --check` passed on that revision. After review fixes, bounded production-consumer/recipe tests, command-package compilation and affected-package vet were rerun; no new full-suite pass is claimed. Added regressions cover state-file no-overwrite, dangling/internal symlinks, strict envelope decoding, unapplied live rejection, target-only receipts, sandbox rejection before filesystem access, published repository permissions and late-file preservation. An initial parallel run also failed
because shared temporary storage filled; the serial task-local-temp rerun resolved
those capacity failures. This is fake-executor runtime coverage, not evidence of
an actual Flatpak download, system installation or successful boot.

The redirected system repository mechanism is documented in the official
[Flatpak command reference](https://docs.flatpak.org/en/latest/flatpak-command-reference.html)
under `FLATPAK_SYSTEM_DIR`.
