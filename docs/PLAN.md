# `fisherman plan --json`

`fisherman plan --json -` reads a recipe on stdin and prints what an
install of it would do, as one JSON object on stdout. A frontend can show
this plan on its confirmation page instead of a summary that it builds
itself.

```sh
generate-recipe | fisherman plan --json -
generate-recipe | fisherman plan --json --read-image -   # also the image's identity and size
```

The plan comes from the same code as the install:

- the checks are the checks of `fisherman validate --json` (the same
  function), so `plan` refuses exactly what `validate` refuses;
- the steps are the install's step plan (`planSteps` in
  `cmd/fisherman/steps.go`), which the install announces one by one;
- the partitions of an automatic layout come from the table the install
  writes with sfdisk (`disk.LayoutPartitions` in
  `internal/disk/layout.go`);
- the order and the action of each manual mount come from the functions
  the manual layout runs (`disk.OrderMounts`, `MountSpec.Action`).

## What `plan` does not do

- It does not partition, format, mount, unlock, enrol a TPM, pull an
  image or change the system. It needs no root. Without `--read-image` it
  contacts no registry and runs no image command. The tests run it with a
  fake command runner that fails on any command that is not read-only, and
  with a fake registry: no test reaches the network.
- It reads what `validate` reads: the target disk's probe facts (one
  `lsblk` call, which also gives its serial, WWN and model, and the
  `/dev/disk/by-id` links), the TPM and the live-media marker. For a
  manual layout it reads each named partition's size from the same
  `lsblk` tree (one read-only call per partition). For scratch on the host
  it reads the free space of the filesystem that would hold
  `/var/fisherman-tmp` with `statfs`, at that directory or, when it does
  not exist yet, at its nearest existing parent; it never creates it.
- With `--read-image` it also reads the source image's metadata, never a
  layer ([Reading the image](#reading-the-image)).
- Its output holds no passphrase, user password or recovery key, and not
  the recipe. The validation messages are the ones `validate --json`
  prints, which hold no secret either. The tests plant sentinel secrets in
  the fixtures and look for them in the output.
- A plan is not a permission and not a proof. The install checks the
  recipe and the target device again before it writes. If the disk changed
  after the plan, the plan is wrong and the install decides. To make the
  install refuse a disk that is not the one the user confirmed, put its
  identity in the recipe ([Binding the install to the planned
  disk](#binding-the-install-to-the-planned-disk)); to make it refuse a
  machine that no longer gives the plan the user confirmed, put the plan's
  binding in it ([Binding the install to the confirmed
  plan](#binding-the-install-to-the-confirmed-plan)).

## Output

```json
{
  "protocol_version": 1,
  "valid": true,
  "errors": [],
  "plan": { "source": {}, "image": {}, "target": {}, "boot": {}, "encryption": {},
            "partitions": [], "var_disk": null, "destroys": [],
            "capacity": {}, "scratch": {}, "steps": [], "binding": {}, "unknown": [] }
}
```

| Field | Type | Meaning |
|---|---|---|
| `protocol_version` | integer | The schema version, now 1. It goes up on a change that a frontend could notice (a removed or renamed field, a changed meaning). A new field does not change it. |
| `valid` | boolean | The recipe passed every check of `validate --json`. |
| `errors` | array | The problems, exactly as `validate --json` reports them ([VALIDATE.md](VALIDATE.md)). Empty when `valid` is true. |
| `plan` | object or null | The plan. Null when `valid` is false. |

A value that only the install can resolve (it needs a pull, a registry or
the contents of the target disk), or that the disk does not report, is the
string `"unknown"`. A number that is not known is left out. Each one has an
entry in `plan.unknown` that names it and says why.

### `plan.source`

| Field | Meaning |
|---|---|
| `kind` | `registry`, `containers-storage`, `oci`, `live` (the running live image, for an empty `image`) or `source-reuse` (this host's bootc store, [SOURCE-REUSE.md](SOURCE-REUSE.md)). |
| `image` | The source image reference. For `live`, the running image that the host names, or `unknown`. |
| `target_imgref` | The reference the installed system tracks for updates: `targetImgref`, else the source. |
| `digest` | The source digest: for `source-reuse` the recipe's, else `plan.image.manifest_digest` when `--read-image` read it; else `unknown`. |
| `pull` | `none` when no registry is read (local, live and reused sources), else `unknown`: the install knows only when it checks the image cache. |

### `plan.image`

What `--read-image` read about the source image without a pull
(`install.InspectImageForPlan`). Without the flag, for a `live` or
`source-reuse` source, or when nothing answered, `read_from` is `none`, the
strings are `unknown`, the numbers are left out, and one `plan.unknown`
entry for `image` says why.

| Field | Meaning |
|---|---|
| `read_from` | `containers-storage` (a local copy: the one the install uses), `oci` (the OCI layout the recipe names), `registry` (the registry's manifest: the install pulls the image) or `none`. |
| `manifest_digest` | The digest of the manifest the reference names: an image index for a multi-platform image in a registry, else the image manifest. For a local copy, the digest containers-storage records. |
| `platform_digest` | The image manifest for this host's platform, from the registry's raw manifest; the same as `manifest_digest` for a single-platform image. `unknown` for a local copy, or when the index lists no build for this host (the install refuses that image). |
| `platform` | The `os/architecture` of the image configuration. |
| `layers` | The number of layers. |
| `compressed_bytes` | The sum of the layer sizes the manifest lists (skopeo's `LayersData`): what a pull downloads. Left out when a size is missing (skopeo before 1.6 prints none). |
| `unpacked_bytes` | The unpacked size of the layers, from `unpacked_from` ([Size estimates](#size-estimates)). Left out for an OCI layout (its manifest lists compressed sizes only), and for a registry image whose manifest lists no layer sizes. |
| `unpacked_from` | Where `unpacked_bytes` comes from: `containers-storage` (local containers-storage reports it: `podman image inspect`'s `Size`), `annotations` (a registry image whose every layer has the uncompressed-size annotation `io.containers.estargz.uncompressed-size`: their sum) or `estimate` (a registry image without them: `compressed_bytes` × 3, `install.UnpackedEstimateRatio`). Left out with `unpacked_bytes`. |

### `plan.target`

| Field | Meaning |
|---|---|
| `layout` | `auto` (fisherman partitions `disk`) or `manual` (`customMounts`). |
| `disk` | The disk as the recipe names it. Automatic layout only. |
| `device` | The device node after symlinks are resolved. The partition devices are built from it. |
| `size_bytes` | The disk size from the probe. |
| `loop` | The disk is a loop device. |
| `stable_id` | A stable identity: the WWN, else the serial, else `unknown`. |
| `identity` | What the disk reports about itself. Automatic layout only. |

`plan.target.identity` comes from the same `lsblk` call as the target
checks of `validate` (`probe.Target`). It writes nothing.

| Field | Meaning |
|---|---|
| `serial` | The serial (lsblk `SERIAL`, from udev), or `unknown`. |
| `wwn` | The World Wide Name (lsblk `WWN`: `0x…` for SATA and SAS, `eui.…` or `nvme.…` for NVMe), or `unknown`. |
| `model` | The model (lsblk `MODEL`), or `unknown`. |
| `size_bytes` | The size in bytes. |
| `by_id` | The `/dev/disk/by-id` links to the disk, sorted. Empty when udev made none or plan cannot read the directory. |
| `bound` | The recipe has a `diskIdentity` and the disk matches it. A recipe whose `diskIdentity` does not match is invalid and has no plan. |

Loop devices, most virtual disks and some USB bridges report no serial and
no WWN.

### `plan.boot`

| Field | Meaning |
|---|---|
| `bootloader` | `grub2` or `systemd` (systemd-boot; ZFS always uses it). |
| `partition_layout` | Automatic layout only: `grub2` (EFI, ext4 `/boot`, root), `systemd-boot` (EFI, root with the architecture's root type GUID) or `zfs` (EFI, ZFS pool). |
| `composefs` | bootc installs with the composefs backend. |

### `plan.encryption`

| Field | Meaning |
|---|---|
| `type` | The recipe's `encryption.type`; `none` when it is empty. |
| `luks` | The root becomes a LUKS2 volume. A manual layout is never encrypted. |
| `tpm2` | A TPM2 key is enrolled after the install. |
| `passphrase` | The recipe's passphrase unlocks the root. The passphrase itself is never in the plan. |
| `recovery_key` | `generated` for `tpm2-luks`: the install generates a recovery key and shows it in its progress output. Else `none`. |

### `plan.partitions`

One entry for each partition, in the order the install acts on it.

| Field | Meaning |
|---|---|
| `number` | The partition number. Automatic layout only. |
| `device` | The partition device. |
| `role` | `efi`, `boot`, `root`; for a manual layout also `swap`, `data` or `none` (a mount with no target). |
| `created` | The install creates the partition (automatic layout). |
| `gpt_name` | The GPT partition name. Automatic layout only. |
| `gpt_type` | The sfdisk type: `uefi`, `linux` or a type GUID. Automatic layout only. |
| `size` | The sfdisk size (`2GiB`), or `rest` for the rest of the disk. Automatic layout only. |
| `size_bytes` | The size in bytes. For `rest`, `plan.capacity.root_bytes`; left out when the disk size is unknown. For a manual layout, the size lsblk reports for the partition (left out, with a `plan.unknown` entry, when lsblk does not report it, and for a mount with action `ignore`). |
| `action` | `format` (the data is lost), `preserve` (mounted as it is), `swap` (mkswap, then swapon) or `ignore`. |
| `filesystem` | The filesystem after the install. `unknown` for a preserved partition: the plan does not read it. |
| `encryption` | `luks2` when the partition is a LUKS volume. |
| `zfs_pool` | The ZFS pool name. |
| `mount` | The mount point in the installed system, or `swap`. |
| `mount_options` | The root mount options the install uses and records (`compress=zstd:1`, `subvol=@,...`). |

### `plan.var_disk`

Null without `varDisk`.

| Field | Meaning |
|---|---|
| `disk` | The disk as the recipe names it. |
| `device` | The device node after symlinks are resolved. |
| `action` | `format` (wiped, then XFS) or `preserve` (`keepExisting`). |
| `filesystem` | `xfs`, or `unknown` for a preserved disk. |
| `mount` | Always `/var`. |

### `plan.destroys`

Every device whose data the install destroys.

| Field | Meaning |
|---|---|
| `device` | The disk or partition. |
| `effect` | `wipe_disk` (every partition and signature on the disk), `format` or `mkswap` (one partition). |

### `plan.capacity`

For an automatic layout, what the layout leaves for the root. For a manual
layout, the size of the root partition it names, which lsblk reports.
Earlier versions of protocol 1 printed null for a manual layout; a
frontend reads `plan.target.layout`, not the null, to tell the layouts
apart.

| Field | Meaning |
|---|---|
| `min_disk_bytes` | The smallest disk fisherman installs to without `"too_small"` in `override`: 50 GiB (`probe.MinDiskBytes`, the check `disk_too_small`). |
| `disk_minimum` | `met`, `overridden` (the disk is smaller and the recipe overrides `too_small`), `not_applicable` (a loop device: an image for another machine; or a manual layout, which has no disk minimum) or `unknown`. |
| `reserved_bytes` | The partitions before the root, plus 3 MiB for alignment and the GPT: sfdisk starts the first partition at 1 MiB, aligns each to 1 MiB and keeps the backup GPT at the end of the disk. Automatic layout only. |
| `root_bytes` | Automatic layout: the disk size less `reserved_bytes`, a lower bound for the root partition (or the ZFS pool). Manual layout: the size of the partition mounted at `/`. Left out when that size is unknown. |
| `root_minimum` | Always `unknown`: fisherman holds the root to no minimum of its own beyond the disk's. `root_need_bytes` and `image_fit` compare the root with the image. |
| `root_need_bytes` | The root one deployment of the image takes: `plan.image.unpacked_bytes` ([Size estimates](#size-estimates)). Left out when that is unknown. |
| `image_fit` | `enough`, `not_enough` or `unknown`: whether `root_bytes` holds the image ([Size estimates](#size-estimates)). For a manual layout whose root partition is kept (`fstype` `unformatted`), always `unknown`: plan does not read the free space inside it. A report only: neither `validate` nor the install refuses a recipe on it. |

### `plan.scratch`

| Field | Meaning |
|---|---|
| `path` | The scratch directory for image layers: `/var/fisherman-tmp`, or a directory on the target when the host's `/var` is tmpfs or overlayfs. The install decides it with the same check (`varIsSpaceConstrained`). |
| `on_target` | The scratch directory is on the target disk. |
| `bytes` | Always `unknown`, kept for protocol 1: the exact use depends on the path the install takes. `need_bytes` is the estimate. |
| `need_bytes` | An upper estimate of the scratch space: `plan.image.compressed_bytes` + `plan.image.unpacked_bytes` ([Size estimates](#size-estimates)). Left out when either is unknown. |
| `free_bytes` | Scratch on the host only: the free space (`statfs` `f_bavail` × `f_bsize`, what an unprivileged writer gets) of the filesystem that holds `path`. Left out for scratch on the target, when `statfs` fails, or when plan runs inside a Flatpak sandbox (whose `/var` is not the host's; frontends run fisherman on the host). |
| `free_path` | The directory `free_bytes` was read at: `path`, or its nearest existing parent when `path` does not exist yet (the install creates it). Left out with `free_bytes`. |
| `fit` | `enough`, `not_enough` or `unknown`: whether `free_bytes` holds the scratch the install may use ([Size estimates](#size-estimates)). Always `unknown` for scratch on the target: `plan.capacity.image_fit` includes it. A report only. |

### `plan.steps`

The install's progress steps, in order. The install announces each one with
a `step` event ([JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md)), and
their count is the step total it announces. A pull moves weight between the
steps but never adds or removes one, so the plan has no weights.

| Field | Meaning |
|---|---|
| `number` | 1 for the first step. |
| `step_id` | The stable step ID of the `step` event. |
| `name` | The step name of the `step` event. |

### `plan.binding`

What a frontend copies into the recipe's `planBinding`
([Binding the install to the confirmed
plan](#binding-the-install-to-the-confirmed-plan)).

| Field | Meaning |
|---|---|
| `digest` | `sha256:` and the hex SHA-256 of the plan's basis: the target (layout, device, size, loop, serial, WWN, model), `plan.boot`, each partition's number, device, role, GPT type, size, action, filesystem, encryption and mount, the `/var` disk's device and action, `plan.destroys`, `capacity.disk_minimum`, `root_bytes`, `root_need_bytes` and `image_fit`, and `scratch.path`, `on_target` and `fit`, with whether the image was read and a version of the basis. Not the source, the encryption settings or the steps (the recipe fixes them), the free space in bytes, the by-id links or the reasons. |
| `read_image` | The plan was made with `--read-image`. Copy it as `readImage`. |

### `plan.unknown`

| Field | Meaning |
|---|---|
| `field` | The plan field that is `unknown`, for example `source.digest` or `partitions[3].filesystem`. |
| `reason` | Why the plan cannot know it. |

## Reading the image

`--read-image` reads the source image's manifest, configuration and store
metadata, never a layer, and writes nothing:

- `containers-storage:` and `oci:` sources are read locally only (`skopeo
  inspect`, and `podman image inspect` for the unpacked size). Plan never
  asks a registry for them, as the install never pulls them.
- A registry reference is looked up in local containers-storage first,
  the copy the install would use. Only when there is none does plan ask
  the registry: `skopeo inspect --no-tags docker://…` for the digest,
  platform, layer sizes and layer annotations (the unpacked size when the
  layers record it), and `skopeo inspect --raw` for the platform
  manifest. Neither downloads a layer. It asks once, with no retry. This is the only network read
  `plan` makes. A recipe with a `planBinding` whose `readImage` is true
  is planned once more for the binding check, which reads the image too.
- An unreachable registry, a refused login, an unknown image or a
  malformed answer leaves `plan.image` at `none`, with the reason in
  words the install's image check uses (`the registry of … cannot be
  reached (…)`). The plan is still valid: the install checks the image
  again in its pre-flight and refuses it there.
- The local store read is the store of the user plan runs as. Run plan
  as the user the install runs as (root) for the store the install uses;
  a rootless store can lack an image root's store has.

A tag can move after the plan: the digest is what the registry or the
store had when plan read it. To install exactly that image, put the
digest in `image` (`registry/name@sha256:…`).

## Size estimates

The numbers are estimates for a confirmation page, not limits. What they
mean, and how far off they can be:

- `image.compressed_bytes` is exact for what it measures: the bytes of the
  compressed layers in the manifest, the download of a pull. It is a
  **lower bound** of the unpacked size: an OS image's layers (gzip or
  zstd) unpack to more than they download, often two to three times as
  much.
- `image.unpacked_bytes` is what containers-storage reports for the
  unpacked layers (`unpacked_from` `containers-storage`) or, for an image
  only a registry has, what its manifest gives without a pull: the sum of
  the layers' uncompressed-size annotations (`annotations`, exact when the
  image was built with them), else **an estimate**: three times the
  compressed size (`estimate`). Three is the high end of what OS images
  unpack to, so the estimate errs towards more room; an image that
  compresses better than 3:1 can still need more. A deployment on the
  root holds the same files, so `capacity.root_need_bytes` is this
  number, an estimate included. The root can use less
  (files the layers share, btrfs `compress=zstd:1`) or somewhat more
  (filesystem overhead, the `/etc` and `/var` of the first boot). It is
  one deployment: `bootc update` stages the next beside it, so a root
  with room for about twice the unpacked size keeps updates working.
- `scratch.need_bytes` is an **upper estimate**: the most the install
  holds in scratch at once is an OCI copy of the compressed layers
  (composefs, or a store redirected to the target) and an unpacked podman
  store. A direct install from local storage uses far less.
- `capacity.image_fit` is `not_enough` when `root_bytes` is smaller than
  the unpacked size, or, when that is unknown or an estimate, than the
  compressed size (the lower bound): an estimate alone never makes it
  `not_enough`. It is `enough` when `root_bytes` holds the unpacked size
  (an estimate too) and, when the scratch directory is on the target
  (`scratch.on_target`), `scratch.need_bytes` as well (which includes
  it). Otherwise it is `unknown`, with the reason in `plan.unknown`; for
  a root between the compressed size and an estimated unpacked size, the
  reason says so. For a manual layout
  `root_bytes` is the size of the root partition, compared the same way.
- `scratch.fit` is `enough` when `scratch.free_bytes` holds
  `scratch.need_bytes`, the upper estimate. It is `not_enough` only when
  the install exports an OCI copy of the compressed layers into scratch
  (a composefs install from a source that is not already an OCI layout)
  and the free space is smaller than `image.compressed_bytes`. Otherwise it
  is `unknown`: with less free space than the upper estimate the install
  may still fit, as a direct install from local storage uses far less.
  The free space is read when plan runs; whatever else writes to that
  filesystem before the install changes it.

## Exit codes

| Exit | When |
|---|---|
| 0 | The recipe is valid and the plan is on stdout. |
| 1 | The recipe is invalid, unreadable or not JSON. |
| 2 | Bad arguments: no `--json`, no `-`, a recipe path, or an unknown argument. |

A registry that does not answer `--read-image` is not an error: the plan
says `unknown` and plan exits 0.

## Binding the install to the planned disk

A frontend that shows the plan on its confirmation page binds the install
to the disk the user confirmed with the recipe field `diskIdentity`
([RECIPE.md](RECIPE.md)): it copies `serial`, `wwn` and `size_bytes` of
`plan.target.identity` into `serial`, `wwn` and `sizeBytes`, and leaves out
a value that is `unknown`.

```json
{"disk": "/dev/sdb", "diskIdentity": {"serial": "S5Y1NJ0R123456", "wwn": "0x5002538f4231abcd", "sizeBytes": 500107862016}}
```

The full `validate`, `plan` and the install's pre-flight then refuse the
recipe with `disk_identity_mismatch` when the device `disk` names reports
another serial, WWN or size, or no longer reports one
([VALIDATE.md](VALIDATE.md#disk-identity)). The install runs the check with
its other pre-flight checks, before its first disk step, and again right
before it writes the partition table: the device a path names can change in
between (a disk swapped, or the disks renumbered after a replug). A
mismatch there fails the install with `disk_identity_mismatch` and nothing
written; a device that is gone is a mismatch too, and one `lsblk` cannot
read is `disk_unverified`.

## Binding the install to the confirmed plan

A plan is what the machine gave when plan ran. To make the install refuse a
machine that no longer gives the plan the user confirmed, a frontend
copies `plan.binding` into the recipe field `planBinding`
([RECIPE.md](RECIPE.md)), with `read_image` spelt `readImage`:

```json
{"disk": "/dev/sdb", "planBinding": {"digest": "sha256:9f2c…", "readImage": true}}
```

The recipe still goes on stdin. The full `validate`, `plan` and the
install's pre-flight then plan the recipe again on the machine as it is
(reading the image only when `readImage` is true, as the confirmed plan
did) and refuse it with `plan_stale` when the digest differs
([VALIDATE.md](VALIDATE.md#saved-plan)). The binding is not part of what
it binds: the bound recipe plans to the same digest. It can be combined
with `diskIdentity`, which names the field that changed.

The digest holds no secret and no recipe value beyond what the plan
prints. Make the plan as the user the install runs as (root): the image
sizes come from that user's containers-storage. Any change of the basis
refuses the install, a change for the better too, and the install still
runs every other check: the binding narrows what the install accepts and
grants nothing.

## Not in this version

The plan does not yet report the mounts of the installed system beyond the
partitions, the unpacked size of an OCI layout source (only an estimate
for a registry image whose layers do not record it), the free space
inside a kept manual root partition, the size of a `varDisk`,
or the identity of a manual layout's disks (no `diskIdentity` binding for
`customMounts`; a `planBinding` binds a manual layout's partition paths and
sizes, not the disks they are on). The install checks `planBinding` in its
pre-flight, not again right before it partitions. See
tuna-os/fisherman#371.
