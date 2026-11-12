# Installing from the live host's bootc store: `sourceReuse`

Phase 2 of [#384](https://github.com/tuna-os/fisherman/issues/384). A
recipe can ask fisherman to take its source from an image the live host's
own bootc already keeps, instead of pulling it or reading a store the recipe
names. This is an explicit, experimental acquisition step: it writes to the
host, so it is never part of `fisherman probe`, of `fisherman validate`, or
of an install that does not ask for it. Phase 1 (the read-only inventory,
`bootc_store` in [PROBE.md](PROBE.md#bootc-store)) is where the item comes
from. Phase 3 (`image pull-from-default-storage`, `image cmd pull`) is not
implemented.

"Live host" means the machine fisherman runs on (probe's `live-host`
context), not live media. The reuse is refused on live media.

## The recipe field

```json
{
  "disk": "/dev/vdb",
  "filesystem": "xfs",
  "encryption": {"type": "none"},
  "hostname": "workstation",
  "targetImgref": "ghcr.io/example/os:stable",
  "experimental": ["bootc_store_reuse"],
  "sourceReuse": {
    "store": "bootc",
    "digest": "sha256:5f0c9a2d7e4b1c8a3f6e9d2b5c8a1f4e7d0c3b6a9f2e5d8c1b4a7f0e3d6c9b2a",
    "platform": "x86_64"
  }
}
```

| Field | Meaning |
|---|---|
| `sourceReuse.store` | `"bootc"`: the live host's bootc-owned ostree storage. Nothing else is accepted. |
| `sourceReuse.digest` | The item's manifest digest, its immutable identity: `bootc_store.items[].digest` from `fisherman probe --json`. Never a name: a tag on the host can move to another image, and two images can share a name. |
| `sourceReuse.platform` | The item's architecture, `bootc_store.items[].platform` (`x86_64`, `aarch64`). The OCI spelling bootc status uses (`amd64`, `arm64`) is accepted and means the same. The item, this machine and the copy must all be this platform. |
| `image` | Must be empty. The source is the reused item. |
| `targetImgref` | Required, and a registry reference (no `containers-storage:`, `oci:`, `localhost/` or credentials). It is what the installed system follows for updates. fisherman never derives it from the copy, whose name means nothing after the install. |
| `experimental` | Must contain `"bootc_store_reuse"`. Only an advisory VM profile installs this way. |

Absent `sourceReuse`: the ordinary source paths, unchanged. There is no
default that turns it on.

### Which items qualify

An item of `bootc_store.items[]` with `storage` `"ostree"`, `availability`
`"deployed"`, `matches_machine` `true` and the recipe's digest and platform.
The host must be bootc-booted (`bootc_store.available`), installed rather
than live media, with an `ostree` booted backend. An image only in bootc's
containers-storage (a logically bound image) or only named by `bootc image
list` is not one: `copy-to-storage` exports from ostree.

## What the install does

All of it runs before the first disk step, so a refusal or a failed copy
leaves the target disk as it was.

1. **Resolve** (read-only, the same as `validate`): `bootc status --json`,
   the live-media signals and bootc's store index file give the
   deployments that carry the digest: role, reference, transport, ostree
   commit, stateroot and serial.
2. **Check the CLI**: `bootc image copy-to-storage --help` must exist and
   offer `--source` and `--target`. Without them bootc would copy the
   booted image to `localhost/bootc`, which fisherman never does.
3. **Check the source did not move**: `ostree rev-parse
   --repo=/sysroot/ostree/repo <ref>` of the image's ostree ref
   (`ostree/container/image/` plus ostree-ext's escaping of
   `docker://<name>`) must be the deployment's own commit. A staged or
   cached update moves the ref; the copy would then export another image
   under the same name.
4. **Estimate and refuse when it will not fit**: fisherman walks the
   deployment's checkout (`/sysroot/ostree/deploy/<stateroot>/deploy/<commit>.<serial>`,
   one filesystem, hard links once) for the uncompressed tar bootc writes
   under `/var/tmp` (scratch) and the unpacked image in containers-storage
   (store). It needs scratch + store + 1 GiB free (each on its own
   filesystem when they differ).
5. **Name the destination**: `localhost/fisherman-reuse:<12 hex of the
   digest>-<16 random hex>`. If containers-storage already has the name the
   install stops: fisherman never overwrites a name it did not create.
6. **Copy**: `/var/fisherman-tmp/source-reuse/var-tmp` is bind-mounted over
   `/var/tmp` (ostree-ext opens `/var/tmp` itself, not `$TMPDIR`), then
   `bootc image copy-to-storage --source docker://<name> --target
   localhost/fisherman-reuse:…`, then the bind is released. Free space is
   sampled during the copy.
7. **Verify**: the ostree ref still points at the same commit; `bootc
   status` still has a deployment with that commit and the digest; `skopeo
   inspect containers-storage:<destination>` (local) gives a sha256
   manifest digest and `linux/<the platform>`.
8. **Install** from `containers-storage:<destination>` with `--target-imgref
   <targetImgref>`, through the host's bootc (the direct local-source path).
   Afterwards the installed commit's `ostree.manifest-digest` must equal the
   copy's digest, or the install fails
   (`source_reuse_installed_digest_differs`).
9. **Release**, on success, failure and cancel alike (it is registered
   before the copy starts and runs in the install's teardown): unmount the
   `/var/tmp` bind if it is still there, `podman rmi --ignore` the
   destination (layers another image uses stay), delete the scratch.

### The copy is a different manifest

bootc v1.17.1's export re-serialises the layers from ostree ("we can't
guarantee we reserialize the same tar stream or compression", ostree-ext
`export_to_oci`), so the copy has a new manifest digest. fisherman reports
both: `digest` is the item, `destination_digest` the copy, and the installed
system boots the copy. Its installation receipt
([RECEIPT.md](RECEIPT.md)) expects the copy's digest, and `fisherman verify`
checks that one. The update reference is the item's registry reference, so
the first `bootc upgrade` sees a different digest from the registry's and
fetches the image: the reuse saves the download at install time, not at the
first update. That trade-off is what the VM profile measures.

### The allowlist

Every command goes through `reuse.Allowed`, before the host sees it:

| Program | Exactly |
|---|---|
| `bootc` | `image copy-to-storage --help`; `image copy-to-storage --source <transport:name> --target localhost/fisherman-reuse:<tag>` |
| `ostree` | `rev-parse --repo=/sysroot/ostree/repo ostree/container/image/<escaped>` |
| `skopeo` | `inspect containers-storage:localhost/fisherman-reuse:<tag>` |
| `podman` | `rmi --ignore localhost/fisherman-reuse:<tag>` (also allowed during teardown) |
| `mount` / `umount` | `--bind <scratch>/var-tmp /var/tmp` / `/var/tmp` |

No `image set-unified`, no `pull-from-default-storage`, no `image cmd`, no
pull, push, login, prune or GC, no other image name, no credentials.

## Problem codes

`fisherman validate --json` reports the static and host codes with field
`sourceReuse` (or the sub-field); the install reports all of them in its
`error` event. [VALIDATE.md](VALIDATE.md#source-reuse) has the table.

| Code | When |
|---|---|
| `source_reuse_experimental` | `experimental` lacks `bootc_store_reuse` |
| `source_reuse_store_unsupported` | `store` is not `bootc` |
| `source_reuse_digest_invalid` | `digest` is not `sha256:<64 lowercase hex>` |
| `source_reuse_platform_invalid` | `platform` is not a short token |
| `source_reuse_image_conflict` | `image` is set as well |
| `source_reuse_target_imgref_required` | no `targetImgref` |
| `source_reuse_target_imgref_invalid` | `targetImgref` is local, localhost, has credentials or is not a reference |
| `source_reuse_backend_unsupported` | `composeFsBackend`, or `filesystem: zfs` |
| `source_reuse_host_not_bootc` | `bootc_store` unavailable (no bootc, status failed or malformed, not bootc-booted) |
| `source_reuse_live_media` | the host booted from live media |
| `source_reuse_backend_not_qualified` | the host's booted backend is not ostree |
| `source_reuse_item_not_found` | no ostree deployment has the digest |
| `source_reuse_item_unverified` | the deployment's reference, platform, transport or commit is unknown |
| `source_reuse_platform_mismatch` | the item is another platform than the recipe's or this machine's |
| `source_reuse_sandboxed` | install: fisherman runs inside a Flatpak sandbox, not natively on the host |
| `source_reuse_command_unsupported` | install: bootc has no `copy-to-storage`, or none with `--source`/`--target` |
| `source_reuse_source_unavailable` | install: the image's ostree ref cannot be read |
| `source_reuse_source_moved` | install: the ref points at another commit (a staged or cached update) |
| `source_reuse_estimate_failed` | install: the deployment checkout cannot be walked |
| `source_reuse_insufficient_space` | install: the estimate does not fit |
| `source_reuse_destination_exists` | install: the destination name exists |
| `source_reuse_transfer_failed` | install: the copy (or its bind) failed |
| `source_reuse_disk_exhausted` | install: the copy failed with the disk (nearly) full |
| `source_reuse_verify_failed` | install: the copy cannot be inspected, or has no digest |
| `source_reuse_identity_mismatch` | install: the source moved during the copy, the deployment changed, or the copy is another platform |
| `source_reuse_installed_digest_differs` | install: bootc installed something other than the verified copy |

## Progress events

The install emits `{"type":"source_reuse"}` events (version 1) before the
first step: `planned` (with `estimate`), `copying`, `verified` (with
`destination_digest` and `measured`), `failed` (with `code` and `reason`,
before the `error` event), and `released` (from the teardown, `reason`
`removed` or `remove_failed`).
[JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md#source_reuse) has the
fields.

## The offline VM profile (advisory)

`store-reuse-advisory` in `bootcrew-vm.yml`, from `store_reuse` in
`tests/bootcrew-matrix.yaml`, runs `scripts/store-reuse-vm.sh`:

1. fisherman on the runner installs the centos-bootc ssh-enabled image from
   the registry onto disk A (the existing path, timed);
2. disk A boots in QEMU with a blank disk B and only the SSH forward
   (`-netdev user,restrict=on`); the profile first shows that neither
   `curl https://quay.io/v2/` nor `skopeo inspect docker://<image>` works
   from the VM;
3. in the VM, as root: the booted image's digest validates; a digest the
   host lacks is refused by `validate` and by the install, with disk B
   untouched; an install cancelled (SIGTERM) during the copy exits 130 and
   leaves no copy, no `/var/tmp` bind, no scratch and a blank disk B; then
   the install from the VM's own bootc store onto disk B, after which
   nothing is left behind either;
4. disk B boots through `boot-verify.sh` with the receipt checks required,
   and `store_reuse_identity` (`scripts/store-reuse-lib.sh`) checks that it
   booted the verified copy, which the receipt also expects, follows the
   original update reference and is the item's platform;
5. the job summary compares install time and installed-root size with step
   1, and reports the copy time, the scratch and store estimates, the
   measured peak scratch, the store the copy held and the change of the VM's
   `/var`.

It is advisory: `continue-on-error`, and not in `required-canaries`. The
reuse stays experimental until its numbers against the existing path say
whether any profile should use it by default. `tests/check-validation.sh`
proves the identity verdict catches a wrong digest, a wrong update
reference, a wrong platform, a receipt for another image, a copy of another
item, a missing verification and a malformed status.
