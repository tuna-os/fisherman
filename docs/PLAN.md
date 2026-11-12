# `fisherman plan --json`

`fisherman plan --json -` reads a recipe on stdin and prints what an
install of it would do, as one JSON object on stdout. A frontend can show
this plan on its confirmation page instead of a summary that it builds
itself.

```sh
generate-recipe | fisherman plan --json -
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
  image, contact a registry or change the system. It needs no root. The
  tests run it with a fake command runner that fails on any command that
  is not read-only.
- It reads what `validate` reads: the target disk's probe facts, the TPM
  and the live-media marker.
- Its output holds no passphrase, user password or recovery key, and not
  the recipe. The validation messages are the ones `validate --json`
  prints, which hold no secret either. The tests plant sentinel secrets in
  the fixtures and look for them in the output.
- A plan is not a permission and not a proof. The install checks the
  recipe and the target device again before it writes. If the disk changed
  after the plan, the plan is wrong and the install decides.

## Output

```json
{
  "protocol_version": 1,
  "valid": true,
  "errors": [],
  "plan": { "source": {}, "target": {}, "boot": {}, "encryption": {},
            "partitions": [], "var_disk": null, "destroys": [],
            "scratch": {}, "steps": [], "unknown": [] }
}
```

| Field | Type | Meaning |
|---|---|---|
| `protocol_version` | integer | The schema version, now 1. It goes up on a change that a frontend could notice (a removed or renamed field, a changed meaning). A new field does not change it. |
| `valid` | boolean | The recipe passed every check of `validate --json`. |
| `errors` | array | The problems, exactly as `validate --json` reports them ([VALIDATE.md](VALIDATE.md)). Empty when `valid` is true. |
| `plan` | object or null | The plan. Null when `valid` is false. |

A value that only the install can resolve (it needs a pull, a registry or
the contents of the target disk) is the string `"unknown"`. Each one has an
entry in `plan.unknown` that names it and says why.

### `plan.source`

| Field | Meaning |
|---|---|
| `kind` | `registry`, `containers-storage`, `oci`, `live` (the running live image, for an empty `image`) or `source-reuse` (this host's bootc store, [SOURCE-REUSE.md](SOURCE-REUSE.md)). |
| `image` | The source image reference. For `live`, the running image that the host names, or `unknown`. |
| `target_imgref` | The reference the installed system tracks for updates: `targetImgref`, else the source. |
| `digest` | The source digest. Known only for `source-reuse`; else `unknown`. |
| `pull` | `none` when no registry is read (local, live and reused sources), else `unknown`: the install knows only when it checks the image cache. |

### `plan.target`

| Field | Meaning |
|---|---|
| `layout` | `auto` (fisherman partitions `disk`) or `manual` (`customMounts`). |
| `disk` | The disk as the recipe names it. Automatic layout only. |
| `device` | The device node after symlinks are resolved. The partition devices are built from it. |
| `size_bytes` | The disk size from the probe. |
| `loop` | The disk is a loop device. |
| `stable_id` | A stable identity (serial or WWN). Always `unknown` in this version. |

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

### `plan.scratch`

| Field | Meaning |
|---|---|
| `path` | The scratch directory for image layers: `/var/fisherman-tmp`, or a directory on the target when the host's `/var` is tmpfs or overlayfs. |
| `on_target` | The scratch directory is on the target disk. |
| `bytes` | The space it needs. Always `unknown`: it depends on the image size. |

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

### `plan.unknown`

| Field | Meaning |
|---|---|
| `field` | The plan field that is `unknown`, for example `source.digest` or `partitions[3].filesystem`. |
| `reason` | Why the plan cannot know it. |

## Exit codes

| Exit | When |
|---|---|
| 0 | The recipe is valid and the plan is on stdout. |
| 1 | The recipe is invalid, unreadable or not JSON. |
| 2 | Bad arguments: no `--json`, no `-`, a recipe path, or an unknown argument. |

## Not in this version

The plan does not yet report a stable disk identity, the scratch and
capacity estimates, the image identity before a pull, or the mounts of
the installed system beyond the partitions. The install does not yet read a
saved plan. See tuna-os/fisherman#371.
