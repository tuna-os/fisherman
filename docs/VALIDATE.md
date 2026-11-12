# `fisherman validate --json`

`fisherman validate --json` checks a recipe against **every rule the install
enforces before it touches a disk**, and prints the result as **one JSON
object on stdout**. Frontends use it to reject a bad hostname, username,
filesystem or encryption choice on the page where the user typed it, instead
of after the OS is on disk.

```text
fisherman validate --json <recipe.json | ->            a whole recipe
fisherman validate --json --partial <recipe.json | ->  only the fields present
fisherman validate <recipe.json>                       human summary (unchanged)
```

`-` reads the recipe from stdin. Use stdin whenever the recipe holds a LUKS
passphrase, so it never lands in a temp file or on the command line.

- Exit `0`: valid. `errors` is `[]`.
- Exit `1`: invalid. `errors` lists every problem found. A recipe that cannot
  be read or is not valid JSON is also exit `1`, reported as a problem with
  code `recipe_unreadable` or `recipe_malformed` and an empty `field`.
- A key fisherman does not know is a problem, `recipe_unknown_field`, in the
  full mode and in the install. See [Unknown keys](#unknown-keys).
- Exit `2`: bad arguments (no recipe, more than one, an unknown flag). The
  message and usage are on stderr; stdout is empty.
- **No root**, writes nothing. The full mode `stat`s the disk and partition
  paths, asks `lsblk` about the target disk, and reads the TPM and firmware
  state from `/sys`; `--partial` reads nothing at all.

## One rule set

The rules live in one place, `fisherman/internal/recipe/rules.go`. Three
callers run them:

| Caller | Runs |
|---|---|
| the install (`fisherman <recipe.json>`) | everything below, right after loading the recipe and before the host-tool check, the image check or any disk step |
| `fisherman validate --json` and `fisherman validate` | the same function as the install (`installProblems`), so they accept exactly what the install accepts |
| `fisherman validate --json --partial` | only the rules for the fields present; no disk, partition or TPM check |

The plain `fisherman validate <recipe.json>` keeps its output format; it now
applies the same rules (so it also rejects a bad hostname, a bad username and
a TPM type on a machine without a usable TPM 2.0).

## Schema (`protocol_version` 1)

```text
{
  protocol_version  int      schema version; see "Compatibility"
  valid             bool     errors is empty
  errors[]                   every problem found, in the order below; [] when valid
    field           string   JSON path of the recipe field: "hostname", "user.username",
                             "encryption.passphrase", "customMounts[0].fstype", ...;
                             "" when the recipe itself could not be read, or the
                             problem is about the machine (firmware_not_uefi)
    code            string   stable id; see "Codes". Key copy on this, never on message
    message         string   English, for logs and CLI users; may change
}
```

Order: unknown keys (sorted by path), then the order of `Recipe.Problems()`
in `fisherman/internal/recipe/recipe.go`:

1. the layout: `customMounts` (each entry, then the layout rules and
   encryption with a manual layout), or `disk`, `filesystem`, its
   combinations and the `zfs` opt-in;
2. `unifiedStorage`, `sourceReuse`, `imageType`, `image` and `targetImgref`
   (reference syntax), `bootloader`, `encryption`, `varDisk` with
   encryption, then `varDisk` itself;
3. `hostname`, `user.username`, `distroID`, `brewTap`, `slurp`,
   `targetMount`, `flatpakVarPath`, `zfsPoolName`, `luksMapperName`,
   `additionalImageStores`, `override`, `experimental`, `locale`,
   `keyboard`, `timezone`.

Then the checks that read the machine, in the order of `installProblems` in
`fisherman/cmd/fisherman/validate.go`: the TPM, `image` (live media), whether
each `additionalImageStores` entry exists, the `sourceReuse` host checks, and
target eligibility (`disk`, `varDisk.disk`, the firmware) last. Each field
reports at most one problem from its own rule (the first it breaks).

### Example

```console
$ fisherman validate --json recipe.json
{
  "protocol_version": 1,
  "valid": false,
  "errors": [
    {
      "field": "hostname",
      "code": "hostname_invalid_char",
      "message": "hostname \"my_pc\" contains '_'; use only letters, digits, hyphens and dots"
    },
    {
      "field": "user.username",
      "code": "username_invalid_start",
      "message": "username \"Alice\" must start with a lowercase letter or an underscore"
    }
  ]
}
```

More, byte for byte, in
`fisherman/cmd/fisherman/testdata/validate/*.golden` (inputs beside them).

## Unknown keys

Go's JSON decoder drops a key it does not know. For a recipe that fails open:
`"encrypton": {...}` used to validate clean and install an **unencrypted**
disk, and a field that a newer frontend sent to an older fisherman was
ignored after the user had confirmed it.

So the install and the full `validate` refuse every key the recipe schema
does not have, at any depth:

```console
$ echo '{"disk":"/dev/sda","filesystem":"xfs","hostname":"a","image":"x",
         "encrypton":{"type":"luks-passphrase","passphrase":"x"},
         "user":{"username":"alice","pasword":"pw"}}' | fisherman validate --json -
...
    {"field": "encrypton",    "code": "recipe_unknown_field", ...},
    {"field": "user.pasword", "code": "recipe_unknown_field", ...}
```

- `field` is the JSON path of the key: `encrypton`, `user.pasword`,
  `customMounts[1].fs`.
- Every unknown key is reported, sorted by path, before the other problems.
- The install stops when it loads the recipe: its `error` event is
  `loading recipe: unknown recipe field "encrypton": …` with
  `"code": "recipe_unknown_field"` and `"field": "encrypton"`
  (see [JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md#error)).
- Key names match as the decoder matches them, without regard to case. A key
  is unknown exactly when the decoder would drop it.
- `--partial` does **not** apply this rule. A page sends a fragment, and a
  frontend may be newer than the backend it asks.

A frontend must send only keys in the schema of the fisherman commit it
pins. There is no `recipeVersion` field yet (#293): to use a new field, bump
the pin first.

## Target eligibility

`fisherman probe --json` tells a frontend which disks it may offer. A recipe
can still name any path, so the install and the full `validate` judge the
path again, with the same code as the probe (`probe.Target`), before any
disk step. The install used to check only that the path existed: a recipe
could name the live USB, a partition, or `/etc/hostname`.

fisherman resolves symlinks first. `/dev/disk/by-id/…` is accepted when it
points at a whole disk, and the install then uses the device node it points
at.

| Verdict (probe's identifier) | Code for `disk` | Code for `varDisk.disk` | Override |
|---|---|---|---|
| not a whole disk: a partition, a regular file, a character device, an optical drive, a RAM disk, zram, a device-mapper node (`not_disk`) | `disk_not_disk` | `var_disk_not_disk` | no |
| the running system or the live medium is on it: it, or anything on it, is mounted at `/`, `/sysroot`, `/boot`, `/boot/efi`, `/efi`, `/usr`, `/var`, `/run/initramfs/live`, `/run/initramfs/isoscan` or `/run/media/iso` (`boot_disk`) | `disk_boot_disk` | `var_disk_boot_disk` | no |
| the kernel reports it read-only (`read_only`) | `disk_read_only` | `var_disk_read_only` | no |
| smaller than 50 GiB (`too_small`) | `disk_too_small` | does not apply | `"too_small"` |
| the machine did not boot through UEFI (`system.unmet` has `uefi`) | `firmware_not_uefi`, with an empty `field` | | `"uefi"` |
| `lsblk` failed, so there is no verdict | `disk_unverified` | `var_disk_unverified` | no |

More than one verdict can apply to a disk; each is reported. `not_disk`
stands alone.

**Override.** The recipe field `override` is a list. It accepts `"too_small"`
and `"uefi"` only, and any other value is `override_unsupported`:

```json
{"disk": "/dev/sdb", "override": ["too_small", "uefi"], "...": "..."}
```

The two are policy about this machine, not protection of data: a small disk
can hold a small image, and a disk written on a BIOS machine can boot on
another machine. The other verdicts have no override. An install to the boot
disk destroys the system that runs the installer, and an install to a
partition or a read-only disk fails after the partition table is gone.

**Loop devices.** A whole loop device (`/dev/loopN`, an image file attached
with `losetup`) is a valid target. The probe never offers one, but CI and
image builds install to one. It is an image for another machine, so
`too_small` and the firmware check do not apply to it. `boot_disk`,
`read_only` and `not_disk` (a loop partition, `/dev/loop0p1`) do apply.

**Manual layouts.** `customMounts` names partitions, not a disk, so there is
no `disk` verdict. The firmware check applies.

**Root.** `validate` needs no root. The install does, and it refuses a
non-root run before it reads the recipe: its `error` event has the code
`not_root`.

## Validating one page: `--partial`

A frontend validates a page while it is filled in, before a whole recipe
exists, by sending only what that page has, in the recipe's own shape:

```console
$ echo '{"hostname":"-router"}' | fisherman validate --json --partial -
$ echo '{"user":{"username":"root"}}' | fisherman validate --json --partial -
$ echo '{"filesystem":"zfs","encryption":{"type":"luks-passphrase","passphrase":"x"}}' \
    | fisherman validate --json --partial -
```

A rule runs when its key is present:

| Key present | Rules |
|---|---|
| `hostname` | hostname |
| `user` with `username` | username |
| `slurp` | slurp |
| `distroID` | distroID |
| `brewTap` | brewTap |
| `unifiedStorage` | unifiedStorage |
| `sourceReuse` | the static sourceReuse rules: `store`, `digest`, `platform`, the `bootc_store_reuse` opt-in in `experimental` and the composefs refusal (absent ones count as no opt-in / false), and `image` and `targetImgref` only when the fragment has them. Never the host: whether this host has the item is full mode ([Source reuse](#source-reuse)) |
| `filesystem` | filesystem, its combinations with `btrfsSubvolumes`, `composeFsBackend` and `encryption.type` (absent ones count as false / none), and the `zfs` opt-in in `experimental` (absent counts as no opt-in) |
| `encryption` | encryption type, passphrase |
| `bootloader` | bootloader |
| `imageType` | imageType |
| `image`, `targetImgref` | the reference syntax of each one present. Not with `sourceReuse` in the same fragment: its rules judge both |
| `customMounts` | each entry's target (not whether its partition exists), and the persistence rules: a target named once, a partition path named once (paths are compared as text; the full mode also follows symlinks), a data mount the installed system can mount again. `composeFsBackend` counts as false when absent |
| `varDisk` | its combinations with `encryption.type`, `composeFsBackend` and `filesystem: zfs` (absent ones count as none / false). Not the disk itself |
| `targetMount` | targetMount |
| `flatpakVarPath` | flatpakVarPath |
| `zfsPoolName` | zfsPoolName |
| `luksMapperName` | luksMapperName |
| `additionalImageStores` | each entry's path syntax (not whether it exists) |
| `override` | override |
| `experimental` | experimental |
| `locale` | locale |
| `keyboard` | keyboard layout (required when the object is present), variant, model |
| `timezone` | timezone |

Everything else (`disk`, whether the `customMounts` partitions and the
`varDisk` disk exist and are eligible, the TPM)
is ignored in partial mode: those read the machine, and the full mode checks
them. Keys fisherman does not know are ignored too
([Unknown keys](#unknown-keys)).

Why a partial recipe rather than `--field name=value` flags: the passphrase
rule needs the passphrase, and argv is world-readable in `/proc`. A partial
recipe on stdin keeps secrets off the command line, uses the shape frontends
already build, and lets cross-field rules (ZFS with LUKS) apply when the page
holds both fields.

## Rules

| Field | Rule | Codes |
|---|---|---|
| `hostname` | required; at most 64 characters (the kernel's `HOST_NAME_MAX`, also systemd's limit for `/etc/hostname`); one or more dot-separated RFC 1123 labels, each 1–63 characters of ASCII letters, digits and `-`, not starting or ending with `-`. Upper case is allowed. | `hostname_required`, `hostname_too_long`, `hostname_invalid_char`, `hostname_label_empty`, `hostname_label_too_long`, `hostname_label_hyphen` |
| `user.username` | empty is valid (no user is created). Otherwise at most 32 characters; a lowercase letter or `_` first; then lowercase letters, digits, `_` and `-`; and not a name the target already has: the base users and groups of Fedora/EL/Debian, common service accounts, the groups frontends add the first user to, and anything starting `systemd-` (`recipe.ReservedUsernames`). This is what `useradd` accepts on every image fisherman installs; it used to be checked only by `useradd` itself, in the configure step. | `username_too_long`, `username_invalid_start`, `username_invalid_char`, `username_reserved` |
| `slurp` | absent. A requested user-data migration is refused until it has a durable rescue store: the copy lives only in RAM, an extraction error or partial copy still reached partitioning, and an interruption loses it after the source is wiped. `slurpWallpapers` is unaffected. | `slurp_unsupported` |
| `distroID` | empty is valid (the install uses `bootc`). Otherwise 1–63 characters: a lowercase letter first, then lowercase letters, digits and `-`. It names `/etc/<distroID>/oem/`, the `<distroID>-oem-setup.service` unit and its preset. | `distro_id_invalid` |
| `brewTap` | empty is valid (no tap is added). Otherwise `owner/name`, each part starting with a letter or digit and containing only letters, digits, `.`, `_` and `-`. It is run as `brew tap <brewTap>` in the OEM setup script. | `brew_tap_invalid` |
| `locale` | empty or absent is valid (the image's locale is kept). Otherwise a locale name of at most 64 characters: `language[_TERRITORY][.codeset][@modifier]` (`de_DE.UTF-8`, `sr_RS@latin`), or `C` or `POSIX` with an optional codeset. It is written as `LANG=` to `/etc/locale.conf`. | `locale_invalid` |
| `keyboard` | absent is valid (the image's keyboard is kept). A present object needs `layout`. | `keyboard_layout_required` |
| `keyboard.layout` | an XKB layout name of at most 64 characters: letters, digits, `_` and `-`; several layouts separated by `,` (`us`, `de`, `us,ru`). | `keyboard_layout_invalid` |
| `keyboard.variant` | empty is valid. Otherwise an XKB variant name: letters, digits, `_`, `.`, `+` and `-`; one per layout, separated by `,`, any of them empty (`nodeadkeys`, `,phonetic`). | `keyboard_variant_invalid` |
| `keyboard.model` | empty is valid. Otherwise an XKB model name: letters, digits, `_`, `.`, `+` and `-` (`pc105`). | `keyboard_model_invalid` |
| `timezone` | empty or absent is valid (the image's zone is kept). Otherwise an IANA zone name of at most 64 characters: parts of letters, digits, `_`, `+` and `-`, joined by `/`, with a letter first (`Europe/Berlin`, `Etc/GMT+1`, `UTC`). | `timezone_invalid` |
| `filesystem` (auto layout) | one of `xfs`, `ext4`, `btrfs`, `zfs` (`recipe.Filesystems`) | `filesystem_unsupported` |
| `filesystem` (auto layout) | `zfs` needs `"experimental": ["zfs"]`. No VM test installs ZFS and boots it, so the recipe must say that it accepts that. | `zfs_experimental` |
| `experimental` | each entry is `zfs` or `bootc_store_reuse` | `experimental_unsupported` |
| `sourceReuse` | absent, or an explicit request to install from an image the host's bootc keeps. See [Source reuse](#source-reuse) and [SOURCE-REUSE.md](SOURCE-REUSE.md). | `source_reuse_*` |
| `btrfsSubvolumes` | needs `filesystem: btrfs` | `btrfs_subvolumes_require_btrfs` |
| `composeFsBackend` | needs fs-verity, so not XFS | `composefs_requires_fsverity` |
| `unifiedStorage` | `false` or absent. `true` asks for bootc's `--experimental-unified-storage`, which fisherman does not pass (it runs bootc in a container, where that storage does not resolve). The install used to finish without it and report success. It is not `composeFsBackend` and not `additionalImageStores`; both are supported. `fisherman probe --json` reports the same answer as `capabilities.features.unified_storage` (`unsupported`, `fisherman_refuses`; see docs/PROBE.md). | `unified_storage_unsupported` |
| `encryption.type` | one of the encryption choices (below); `""` means `none` | `encryption_unsupported` |
| `encryption.type` | not LUKS on ZFS | `encryption_unsupported_on_zfs` |
| `encryption.type` | not encrypted with `customMounts` (the manual path runs no `luksFormat`) | `encryption_unsupported_on_manual_layout` |
| `encryption.type` | a `tpm2-*` type needs a usable TPM 2.0 here: `probe --json`'s `tpm.usable`, including its `BOOTC_INSTALLER_FAKE_TPM` override. Full mode only. | `encryption_tpm_unavailable` |
| `encryption.passphrase` | non-empty for a type with `needs_passphrase` | `encryption_passphrase_required` |
| `disk` | required on the auto layout, and must exist. Full mode only. | `disk_required`, `disk_not_found` |
| `disk` | an eligible target: a whole disk (symlinks resolved) that does not hold the running system, is writable and is at least 50 GiB. See [Target eligibility](#target-eligibility). Full mode only. | `disk_not_disk`, `disk_boot_disk`, `disk_read_only`, `disk_too_small`, `disk_unverified` |
| (the machine) | booted through UEFI, unless the target is a loop device. Full mode only. | `firmware_not_uefi` |
| `override` | each entry is `too_small` or `uefi` | `override_unsupported` |
| `zfsPoolName` | empty is valid (`rpool`). Otherwise 1–63 characters: a letter first, then letters, digits, `_`, `-` and `.`; and not a name `zpool` reserves (`mirror*`, `raidz*`, `draid*`, `spare*`, `log`, `c0`–`c9…`). It is an argument of `zpool create`, so `-f` would be read as a flag. | `zfs_pool_name_invalid` |
| `luksMapperName` | empty is valid (`fisherman-root`). Otherwise 1–63 characters: a letter or digit first, then letters, digits, `_`, `.`, `+` and `-`; not `control`. It names `/dev/mapper/<name>`, so `../sda` would point the install at another device node. | `luks_mapper_name_invalid` |
| `additionalImageStores[i]` | an absolute path with no `..` component, no `:` or `,` and no control character. It is mounted into the bootc container as `-v <store>:<store>:ro`. | `additional_image_store_invalid` |
| `additionalImageStores[i]` | a directory on the host. Full mode only. `[]` is valid (no store), and an absent field means fisherman finds the stores itself. | `additional_image_store_not_found` |
| `customMounts[i].partition` | required, and must exist unless the target is swap or empty | `custom_mount_partition_required`, `custom_mount_partition_not_found` |
| `customMounts[i].fstype` | one of `fat32`, `ext3`, `ext4`, `xfs`, `btrfs`, `swap`, `unformatted`, `""` | `custom_mount_fstype_unsupported` |
| `customMounts[i].target` | `""`, `swap`, or an absolute path with no `..` component. It is appended to `targetMount`, so `/../../etc` would mount the partition over the host's `/etc` and a relative `etc` would land beside the target. | `custom_mount_target_invalid` |
| `customMounts` | one entry targets `/` | `custom_mounts_no_root` |
| `customMounts[i].target` | no other entry has the same mountpoint (`/boot` and `/boot/` are the same) | `custom_mount_target_duplicate` |
| `customMounts[i].partition` | no other entry names the same device. A symlink (`/dev/disk/by-id/…`) to a device already named counts. | `custom_mount_partition_duplicate` |
| `customMounts[i].target` | a data mount (any target except `""`, `swap`, `/`, `/boot`, `/boot/efi`) is one the installed system can mount again: `/var`, a path below `/var`, or `/home`, `/root`, `/srv`, `/opt`, `/mnt`, `/media`, `/usr/local` and paths below them. With `composeFsBackend` it is a path below `/var`, not `/var` itself (#252). See [Mounts after the install](#mounts-after-the-install). | `custom_mount_not_persistable` |
| `customMounts` | one entry targets `/boot/efi` (the EFI system partition). bootc writes the boot loader there, and the install reads that entry to find the EFI boot entry. | `custom_mounts_no_esp` |
| `customMounts` | for GRUB (`bootloader` `""` or `grub2`) with a root of fstype `xfs`: one entry targets `/boot`, and its fstype is not `xfs`. GRUB cannot read modern XFS features; the auto layout always makes an ext4 `/boot` for the same reason. A root kept as it is (`unformatted`) is not judged. | `custom_mounts_no_boot` |
| `targetMount` | empty is valid (`/mnt/fisherman-target`). Otherwise an absolute path with no `..` component that is not `/`: the root filesystem is mounted there and every post-install write goes under it. | `target_mount_invalid` |
| `flatpakVarPath` | empty is valid (auto-detect). Otherwise no `..` component: it is joined under the installed root for the flatpak copy. | `flatpak_var_path_invalid` |
| `varDisk.disk` | required, exists, differs from `disk` (also when the two paths are different names for one device) | `var_disk_required`, `var_disk_not_found`, `var_disk_same_as_disk` |
| `varDisk.disk` | a whole disk that does not hold the running system and is writable. Full mode only. | `var_disk_not_disk`, `var_disk_boot_disk`, `var_disk_read_only`, `var_disk_unverified` |
| `varDisk` | not set on a composefs-native target (`composeFsBackend`, or `filesystem: zfs`, which forces composefs). The initramfs mounts the deployment's own `/var` there, so the separate disk would not be used ([#252](https://github.com/tuna-os/fisherman/issues/252)). | `var_disk_unsupported_composefs` |
| `varDisk` | not set with an encrypted `encryption.type`. fisherman has no LUKS step for the `/var` disk: `/var`, and `/var/home` on it, would be plain text beside an encrypted root ([#406](https://github.com/tuna-os/fisherman/issues/406)). | `var_disk_encryption_unsupported` |
| `image` | non-empty unless this host booted from live media (probe's `live.is_live`), where empty means "install the running live image", or the recipe has `sourceReuse` (whose `image` must be empty). Full mode only. | `image_required` |
| `image`, `targetImgref` | empty is not judged here. Otherwise no whitespace or control character (a newline included), and no `-` first. `image` is an argument of `skopeo`, `podman` and `bootc`, `targetImgref` of `bootc --target-imgref`; both are read after the disk is partitioned. With `sourceReuse` the `source_reuse_*` rules judge both instead. | `image_invalid`, `target_imgref_invalid` |
| `imageType` | `""` or `bootc` | `image_type_unsupported` |
| `bootloader` | `""`, `grub2` or `systemd` | `bootloader_unsupported` |
| (whole recipe) | readable, valid JSON | `recipe_unreadable`, `recipe_malformed` |
| (any key) | a key of the recipe schema, at any depth. Full mode and the install only. | `recipe_unknown_field` |

Not fisherman rules, and left to the frontend: the password itself (fisherman
accepts an empty one, which leaves the account locked), "password and
confirmation match", whether a user is required for the chosen image
(`needs_user_creation`), and password strength.

## Mounts after the install

A `varDisk`, and every `customMounts` entry, is mounted for the install. This
table says what the installed system has after the reboot.

| Entry | During the install | On the installed system |
|---|---|---|
| target `/`, `/boot`, `/boot/efi` | formatted (unless kept) and mounted | bootc and the boot loader make these persistent. fisherman writes no fstab line for them. |
| target `""` | nothing | nothing |
| target `swap`, fstype `swap` | `mkswap`, `swapon` | fstab: `UUID=… none swap defaults,nofail` |
| a data mount: any other target | formatted (unless kept) and mounted | fstab: `UUID=… <mountpoint> <type> defaults,nofail,x-systemd.device-timeout=30s` |
| `varDisk` | formatted XFS with label `var` (unless kept) and mounted | fstab, as a data mount at `/var`, plus `x-systemd.before=local-fs.target` |

- The filesystem type and the UUID in the fstab line come from `blkid`. A kept
  disk with ext4 gets an ext4 line.
- `nofail` and the device timeout mean that a missing disk delays the boot for
  30 seconds. It does not end the boot in emergency mode.
- The fstab line goes into the `/etc` of the deployment, on ostree and on
  composefs-native targets.
- The mountpoint in the fstab line is the target as the installed image
  resolves it. On an image where `/home` is a symlink to `var/home`, the line
  for target `/home` names `/var/home`. A composefs-native deployment has no
  root filesystem to resolve in at install time, so there the recipe must
  name the `/var/…` path.
- The post-install steps write the user's home and the Flatpaks to the device
  that the first boot mounts. A separate `/var` (a `varDisk`, or a manual
  `/var` partition) is the `/var` those steps use. With no separate `/var`,
  fisherman mounts a device for a path below `/var` a second time, on the
  deployment's `var` directory, after bootc installs the image.
- fisherman does not replace an fstab line of the image. If the image's fstab
  mounts another device at the same mountpoint, the install fails.
- A second run on the same deployment adds no second line.

The install stops, after bootc ran, when it cannot keep that promise:
`blkid` reports no UUID or type for the device, the resolved mountpoint is
not below `/var` and is not a directory of the image, the image's fstab
conflicts, or an ostree image discards `/etc` at every boot
(`[etc] transient = true` in `prepare-root.conf`). These failures carry no
code: only the installed image shows them.

Before any disk work, the install also checks each device that the recipe
keeps as it is (`varDisk.keepExisting`, or a mounted `customMounts` entry
with fstype `unformatted` or `""`). `blkid` must find a filesystem on it that
can be mounted; for `/var` that is xfs, ext4, btrfs or another POSIX
filesystem. If not, the install fails with `kept_filesystem_unusable`
([Install only](#install-only)).

## Encryption choices: `data/encryption-choices.json`

The encryption types, in the order frontends offer them, are data in
[`data/encryption-choices.json`](../data/encryption-choices.json):

```json
{
  "protocol_version": 1,
  "choices": [
    {"type": "none", "encrypted": false, "needs_passphrase": false, "needs_tpm2": false, "copy_key": "encryption_none"},
    {"type": "luks-passphrase", "encrypted": true, "needs_passphrase": true, "needs_tpm2": false, "copy_key": "encryption_luks_passphrase"},
    {"type": "tpm2-luks", "encrypted": true, "needs_passphrase": false, "needs_tpm2": true, "copy_key": "encryption_tpm2_luks"},
    {"type": "tpm2-luks-passphrase", "encrypted": true, "needs_passphrase": true, "needs_tpm2": true, "copy_key": "encryption_tpm2_luks_passphrase"}
  ]
}
```

(The real file is indented and carries a `$comment`.) `copy_key` is the
branding copy-key stem: the label is `<copy_key>_label`, the description
`<copy_key>_description`. Offer a `needs_tpm2` choice only when
`probe --json` says `tpm.usable`; show the passphrase field only for
`needs_passphrase`.

It is a file rather than a `--choices` flag because frontends need the list
when they build and test, not only at run time: bootc-installer copies it to
`shared/recipe/encryption-choices.json`, and its unit tests hold every
frontend's page to that copy without running fisherman. It is generated from
the same Go table (`recipe.EncryptionChoices`) the `encryption.type` and
passphrase rules read, and `TestEncryptionChoicesFile` fails if the two
differ. Regenerate it with:

```bash
cd fisherman && go test ./internal/recipe -run TestEncryptionChoicesFile -update
```

## Error Code Reference

This section maps error codes to their causes and corrective actions. Each code is stable and will not be reused or renamed.

### Recipe Structure

| Code | Field | Cause | Fix |
|---|---|---|---|
| `recipe_unreadable` | (whole recipe) | Recipe file could not be read | Verify the file path and permissions |
| `recipe_malformed` | (whole recipe) | Recipe JSON is not valid JSON | Validate the JSON syntax |
| `recipe_unknown_field` | the unknown key's path, for example `encrypton` or `user.pasword` | The recipe has a key this fisherman does not know: a typo, or a field from a newer schema. fisherman would ignore it, so it refuses the recipe instead | Correct the key, or update fisherman to a version that has the field |

### Disk and Partition

| Code | Field | Cause | Fix |
|---|---|---|---|
| `disk_required` | `disk` | No disk specified in auto-layout recipe | Add `"disk": "/dev/sdX"` or similar |
| `disk_not_found` | `disk` | The disk path does not exist | Check the disk path; run `lsblk` to list available disks |
| `disk_not_disk` | `disk` | The path is not a whole disk: a partition (`/dev/sda1`), a file, or another kind of device | Name the disk itself (`/dev/sda`); for partitions, use `customMounts` |
| `disk_boot_disk` | `disk` | The running system or the live medium is on this disk | Choose another disk. There is no override |
| `disk_read_only` | `disk` | The kernel reports the disk read-only | Remove the write protection, or choose another disk |
| `disk_too_small` | `disk` | The disk is smaller than 50 GiB | Choose a larger disk, or add `"too_small"` to `override` |
| `disk_unverified` | `disk` | `lsblk` failed, so fisherman cannot tell whether the disk is safe | Install `util-linux` on the host; read the message for the `lsblk` error |
| `firmware_not_uefi` | (none) | The machine booted in BIOS (legacy) mode; fisherman installs UEFI boot loaders | Boot the installer in UEFI mode, or add `"uefi"` to `override` for a disk that another machine will boot |
| `override_unsupported` | `override[i]` | The entry is not `too_small` or `uefi` | Remove it. The other target checks have no override |
| `var_disk_not_disk` | `varDisk.disk` | The `/var` disk path is not a whole disk | Name the disk itself |
| `var_disk_boot_disk` | `varDisk.disk` | The running system or the live medium is on the `/var` disk | Choose another disk |
| `var_disk_read_only` | `varDisk.disk` | The `/var` disk is read-only | Remove the write protection, or choose another disk |
| `var_disk_unverified` | `varDisk.disk` | `lsblk` failed | As for `disk_unverified` |
| `var_disk_required` | `varDisk.disk` | No `/var` disk specified when `varDisk` is used | Add `"varDisk": {"disk": "/dev/sdY"}` |
| `var_disk_not_found` | `varDisk.disk` | The `/var` disk path does not exist | Check the disk path |
| `var_disk_same_as_disk` | `varDisk.disk` | The `/var` disk is the same as the root disk | Use a different disk for `/var` |
| `var_disk_unsupported_composefs` | `varDisk` | `varDisk` is set with `composeFsBackend` or `filesystem: zfs`. The disk would not be used at boot (#252) | Remove `varDisk`, or use an ostree target (no `composeFsBackend`, not `zfs`) |
| `var_disk_encryption_unsupported` | `varDisk` | `varDisk` is set with an encrypted `encryption.type`. The `/var` disk would stay plain text (#406) | Remove `varDisk`, or set `encryption.type` to `none` |
| `custom_mount_target_duplicate` | `customMounts[i].target` | Another entry has the same mountpoint | Keep one entry for each mountpoint |
| `custom_mount_partition_duplicate` | `customMounts[i].partition` | Another entry names the same device (by this path or another) | Give each entry its own partition |
| `custom_mount_not_persistable` | `customMounts[i].target` | The installed system cannot mount this target again: it is on the read-only root, or it is `/var` (or a symlinked directory) on a composefs-native target | Use a path below `/var`. On an ostree target `/var`, `/home`, `/root`, `/srv`, `/opt`, `/mnt`, `/media` and `/usr/local` are accepted too |
| `custom_mount_partition_required` | `customMounts[i].partition` | Partition path is missing in custom mount | Specify a partition path |
| `custom_mount_partition_not_found` | `customMounts[i].partition` | The partition path does not exist | Check the partition path |
| `custom_mounts_no_root` | `customMounts` | No custom mount targets `/` (root filesystem) | Add an entry with `"target": "/"` |
| `custom_mounts_no_esp` | `customMounts` | No custom mount targets `/boot/efi` | Add the EFI system partition: `{"partition": "/dev/sdX1", "target": "/boot/efi", "fstype": "unformatted"}` keeps an existing ESP; `fat32` formats it |
| `custom_mounts_no_boot` | `customMounts` | The boot loader is GRUB, the root is XFS, and there is no `/boot` entry, or `/boot` is XFS too | Add an ext4 `/boot` partition, choose another root filesystem, or use `"bootloader": "systemd"` |
| `custom_mount_target_invalid` | `customMounts[i].target` | The target is relative or has a `..` component, so it would resolve outside the install target | Use `swap` or an absolute mountpoint such as `/boot/efi` or `/home` |
| `target_mount_invalid` | `targetMount` | The target mount is `/`, relative, or has a `..` component | Leave it empty, or use an absolute path such as `/mnt/altroot` |
| `flatpak_var_path_invalid` | `flatpakVarPath` | The path has a `..` component, so it would leave the installed root | Leave it empty, or use a path inside the root such as `state/os/default/var` |

### Language, keyboard and time zone

These values are written into `/etc` files of the installed system, and the
keymap onto the kernel command line of an encrypted install. The rules admit
every real name and nothing that can add a line, an argument or a path part.
They do not check that the image has the locale, the layout or the zone: the
image is not on the disk yet. After the install, a time zone that the image
does not have is not linked, and the log says so.

| Code | Field | Cause | Fix |
|---|---|---|---|
| `locale_invalid` | `locale` | The value is not a locale name | Use a name from `locale -a`, such as `en_US.UTF-8` or `de_DE.UTF-8`, or leave the field out |
| `keyboard_layout_required` | `keyboard.layout` | The `keyboard` object is present and has no `layout` | Set `layout`, or leave `keyboard` out |
| `keyboard_layout_invalid` | `keyboard.layout` | The layout has a character outside letters, digits, `_`, `-` and `,` | Use a name from `localectl list-x11-keymap-layouts`, such as `us` or `de` |
| `keyboard_variant_invalid` | `keyboard.variant` | The variant has a character outside letters, digits, `_`, `.`, `+`, `-` and `,` | Use a name from `localectl list-x11-keymap-variants <layout>`, or leave it empty |
| `keyboard_model_invalid` | `keyboard.model` | The model has a character outside letters, digits, `_`, `.`, `+` and `-` | Use a name from `localectl list-x11-keymap-models`, such as `pc105`, or leave it empty |
| `timezone_invalid` | `timezone` | The value is not an IANA time zone name | Use a name from `timedatectl list-timezones`, such as `Europe/Berlin` or `UTC` |

### Filesystem

| Code | Field | Cause | Fix |
|---|---|---|---|
| `filesystem_unsupported` | `filesystem` | Filesystem is not one of `xfs`, `ext4`, `btrfs`, or `zfs` | Choose a supported filesystem |
| `zfs_experimental` | `filesystem` | Filesystem is `zfs` and the recipe does not list `zfs` in `experimental`. ZFS is experimental: no VM test covers it | Add `"experimental": ["zfs"]` to the recipe (in `--partial` mode, to the same fragment as `filesystem`), or choose `xfs`, `ext4` or `btrfs`. A frontend should show ZFS as experimental and send the field only after the user chose it |
| `experimental_unsupported` | `experimental[i]` | The entry is not a feature fisherman calls experimental | Use `zfs` or `bootc_store_reuse`, or remove the entry |
| `btrfs_subvolumes_require_btrfs` | `btrfsSubvolumes` | `btrfsSubvolumes` requires `filesystem: btrfs` | Either remove `btrfsSubvolumes` or change filesystem to `btrfs` |
| `composefs_requires_fsverity` | `composeFsBackend` | composefs backend requires fs-verity support, which XFS does not provide | Change filesystem to `ext4`, `btrfs`, or `zfs`; or disable `composeFsBackend` |
| `custom_mount_fstype_unsupported` | `customMounts[i].fstype` | Custom mount filesystem type is not supported | Use one of: `fat32`, `ext3`, `ext4`, `xfs`, `btrfs`, `swap`, `unformatted`, or empty |
| `unified_storage_unsupported` | `unifiedStorage` | `unifiedStorage` is `true`. fisherman does not pass `--experimental-unified-storage` to bootc, so the install would not have it | Set it to `false`, or remove the field |

### Encryption

| Code | Field | Cause | Fix |
|---|---|---|---|
| `encryption_unsupported` | `encryption.type` | Encryption type is not one of the supported choices | Use `none`, `luks-passphrase`, `tpm2-luks`, or `tpm2-luks-passphrase` |
| `encryption_unsupported_on_zfs` | `encryption.type` | LUKS encryption cannot be used with ZFS | Either disable encryption or use a different filesystem |
| `encryption_unsupported_on_manual_layout` | `encryption.type` | Encryption is not supported with custom mount layout | Use auto layout, or disable encryption |
| `encryption_tpm_unavailable` | `encryption.type` | TPM 2.0 is required but not available or not usable on this system | Choose `luks-passphrase` instead, or enable TPM in BIOS |
| `encryption_passphrase_required` | `encryption.passphrase` | A passphrase is required for this encryption type but is empty | Provide a non-empty passphrase |

### Hostname

| Code | Field | Cause | Fix |
|---|---|---|---|
| `hostname_required` | `hostname` | Hostname is required but missing | Provide a hostname |
| `hostname_too_long` | `hostname` | Hostname exceeds 64 characters | Shorten the hostname to 64 characters or less |
| `hostname_invalid_char` | `hostname` | Hostname contains invalid characters | Use only ASCII letters, digits, `-`, and `.` |
| `hostname_label_empty` | `hostname` | A label (part between dots) is empty or missing | Remove consecutive dots and ensure labels exist between dots |
| `hostname_label_too_long` | `hostname` | A label exceeds 63 characters | Shorten each label to 63 characters or less |
| `hostname_label_hyphen` | `hostname` | A label starts or ends with `-` | Hyphens cannot start or end a label |

### Username

| Code | Field | Cause | Fix |
|---|---|---|---|
| `username_too_long` | `user.username` | Username exceeds 32 characters | Shorten the username to 32 characters or less |
| `username_invalid_start` | `user.username` | Username does not start with a lowercase letter or `_` | Start with a lowercase letter (a–z) or underscore |
| `username_invalid_char` | `user.username` | Username contains invalid characters | Use only lowercase letters, digits, `-`, and `_` |
| `username_reserved` | `user.username` | Username is reserved by the system | Use a different username; reserved names include common service accounts, systemd system users, and standard UNIX users |

### Image and Distribution

| Code | Field | Cause | Fix |
|---|---|---|---|
| `image_required` | `image` | Image is required on non-live systems but is empty | Provide a container image URL |
| `image_invalid` | `image` | The image reference has a space, a newline or another control character, or starts with `-` (a flag to `podman`) | Use a reference such as `ghcr.io/example/os:stable` |
| `target_imgref_invalid` | `targetImgref` | The update reference has a space, a newline or another control character, or starts with `-` | Use a reference such as `ghcr.io/example/os:stable`, or leave the field out |
| `image_type_unsupported` | `imageType` | Image type is not empty or `bootc` | Leave empty or use `bootc` |
| `distro_id_invalid` | `distroID` | distroID contains invalid characters or format | Use 1–63 lowercase letters, digits, and `-`, starting with a lowercase letter |

### Names and image stores

| Code | Field | Cause | Fix |
|---|---|---|---|
| `zfs_pool_name_invalid` | `zfsPoolName` | The pool name starts with a character other than a letter, has a character outside letters, digits, `_`, `-`, `.`, or is a name `zpool` reserves | Leave it empty (`rpool`), or use a name such as `tank` |
| `luks_mapper_name_invalid` | `luksMapperName` | The mapper name has a `/`, a space or another character outside letters, digits, `_`, `.`, `+`, `-`, starts with `.` or `-`, or is `control` | Leave it empty (`fisherman-root`), or use a name such as `ci-job-7` |
| `additional_image_store_invalid` | `additionalImageStores[i]` | The store path is relative, or has `..`, `:`, `,` or a control character | Use the absolute path of the store directory |
| `additional_image_store_not_found` | `additionalImageStores[i]` | The store path is not a directory on the host | Correct the path, or leave the field out so fisherman finds the stores |

### Bootloader and Slurp

| Code | Field | Cause | Fix |
|---|---|---|---|
| `bootloader_unsupported` | `bootloader` | Bootloader is not empty, `grub2`, or `systemd` | Use one of: empty (auto), `grub2`, or `systemd` |
| `slurp_unsupported` | `slurp` | A requested user-data migration (`slurp`) is refused: fisherman has no durable rescue store for the copy yet, so no recipe field can make it valid | Remove the `slurp` field. `slurpWallpapers` is not affected |
| `brew_tap_invalid` | `brewTap` | Homebrew tap format is invalid | Use `owner/name` format, with each part containing only letters, digits, `.`, `_`, and `-` |

### Source reuse

`sourceReuse` installs from an image the live host's bootc already keeps,
named by its manifest digest ([SOURCE-REUSE.md](SOURCE-REUSE.md), #384).
The static codes are recipe rules (also in `--partial`); the host codes need
the machine and come from full mode and the install, which read only `bootc
status --json`, the live-media signals and bootc's store index file; the
install-only codes come from the copy itself, which only the install runs.

| Code | Field | Cause | Fix |
|---|---|---|---|
| `source_reuse_experimental` | `sourceReuse` | `experimental` does not list `bootc_store_reuse`. Only an advisory VM profile installs this way | Add `"experimental": ["bootc_store_reuse"]`, or remove `sourceReuse` |
| `source_reuse_store_unsupported` | `sourceReuse.store` | `store` is not `bootc` | Use `bootc` |
| `source_reuse_digest_invalid` | `sourceReuse.digest` | Not `sha256:` and 64 lowercase hex digits | Use `bootc_store.items[].digest` from `fisherman probe --json` |
| `source_reuse_platform_invalid` | `sourceReuse.platform` | Not a short architecture token | Use `bootc_store.items[].platform` (`x86_64`, `aarch64`) |
| `source_reuse_image_conflict` | `image` | `image` is set as well | Leave `image` empty: the reused item is the source |
| `source_reuse_target_imgref_required` | `targetImgref` | No `targetImgref` | Name the registry reference the installed system follows for updates |
| `source_reuse_target_imgref_invalid` | `targetImgref` | A `containers-storage:`, `oci:`, `oci-archive:`, `dir:` or `localhost` reference, one with credentials, or not a reference | Use a registry reference such as `ghcr.io/example/os:stable` |
| `source_reuse_backend_unsupported` | `sourceReuse` | `composeFsBackend` is true, or `filesystem` is `zfs` (which forces it). Only ostree-backend installs are qualified | Install an ostree-backend image, or remove `sourceReuse` |
| `source_reuse_host_not_bootc` | `sourceReuse` | Full mode: `bootc_store.available` is false here (no bootc, `bootc status` failed or was malformed, or the host did not boot a bootc image, as in a container) | Run on a bootc-booted host, or remove `sourceReuse` |
| `source_reuse_live_media` | `sourceReuse` | Full mode: the host booted from live media. bootc refuses `copy-to-storage` on a read-only sysroot | Use the live media's own source paths |
| `source_reuse_backend_not_qualified` | `sourceReuse` | Full mode: the host's booted entry is not ostree (composefs) | Remove `sourceReuse` |
| `source_reuse_item_not_found` | `sourceReuse` | Full mode: no ostree deployment of this host has the digest. Logically bound images and names only `bootc image list` reports are not sources | Pick an item with `storage` `ostree` and `availability` `deployed` |
| `source_reuse_item_unverified` | `sourceReuse` | Full mode: the deployment has the digest, but its reference is redacted or invalid, its platform or ostree commit is not reported, or its transport is not `registry`, `containers-storage`, `oci` or `oci-archive` | Pick an item whose `matches_machine` is true |
| `source_reuse_platform_mismatch` | `sourceReuse` | Full mode: the item is not the recipe's platform, or not this machine's | Use the item's platform; install on a machine of that architecture |
| `source_reuse_sandboxed` | `sourceReuse` | Install only: fisherman runs inside a Flatpak sandbox; the copy walks `/sysroot` and binds `/var/tmp` on the host itself | Run fisherman on the host (`flatpak-spawn --host pkexec fisherman -`) |
| `source_reuse_command_unsupported` | `sourceReuse` | Install only: bootc has no `image copy-to-storage`, or none that takes `--source` and `--target` | Update bootc on the host |
| `source_reuse_source_unavailable` | `sourceReuse` | Install only: the image's ostree ref cannot be read in `/sysroot/ostree/repo` | Check the host's ostree repository |
| `source_reuse_source_moved` | `sourceReuse` | Install only: the ref no longer points at the deployment's commit (a staged or cached update took the name) | Reboot into, or pick, the deployment the ref names |
| `source_reuse_estimate_failed` | `sourceReuse` | Install only: the deployment's checkout cannot be walked for the estimate | Check `/sysroot/ostree/deploy` |
| `source_reuse_insufficient_space` | `sourceReuse` | Install only: the scratch, the copy and a 1 GiB margin do not fit in `/var/fisherman-tmp` and containers-storage | Free space on the host's `/var` |
| `source_reuse_destination_exists` | `sourceReuse` | Install only: containers-storage already has the destination name | Remove the stale `localhost/fisherman-reuse:` image |
| `source_reuse_transfer_failed` | `sourceReuse` | Install only: `bootc image copy-to-storage`, or its `/var/tmp` bind, failed | See the install log |
| `source_reuse_disk_exhausted` | `sourceReuse` | Install only: the copy failed with the disk (nearly) full | Free space on the host's `/var` |
| `source_reuse_verify_failed` | `sourceReuse` | Install only: the copy cannot be inspected, its output is malformed, or it has no sha256 digest | See the install log |
| `source_reuse_identity_mismatch` | `sourceReuse` | Install only: the source moved during the copy, the deployment changed, or the copy is another platform | Retry once the host is not updating |
| `source_reuse_installed_digest_differs` | `sourceReuse` | Install only: bootc installed an image other than the verified copy | Report it: this is a bug |

### Install only

The install reports these in its `error` event
([JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md#error)). `validate` never
does: it needs no root.

| Code | Field | Cause | Fix |
|---|---|---|---|
| `not_root` | (none) | The install did not run as root | Run fisherman through `pkexec` or `sudo` |
| `install_already_running` | (none) | Another fisherman install holds the host-wide install lock (`/run/lock/fisherman-install.lock`). Two installs on one host share the scratch directory, so the second is refused before it reads its recipe. The message names the other process | Wait for the other install, or cancel it. A lock file left by a dead process does not block: the kernel drops the lock with the process |
| `host_tool_missing` | (none) | A program the install of this recipe runs is not on the host. The message names it and its package | Install the package on the host |
| `kept_filesystem_unusable` | `varDisk.disk`, `customMounts[i].partition` | The recipe keeps this device as it is, and `blkid` finds no filesystem on it that can be mounted (none, a LUKS or LVM volume, swap, or for `/var` a filesystem with no POSIX owners). Reported before any disk is changed | Format the device (`keepExisting: false`, or a real `fstype`), or name the device that holds the filesystem |

The host-tool list comes from the recipe. The auto layout needs `sfdisk`,
`mkfs.fat`, `mkfs.ext4`, the `mkfs` of `filesystem` (or `zpool` and `zfs`),
and `cryptsetup` for an encrypted root. A `tpm2-*` type needs no
`systemd-cryptenroll` on the host: the enrolment runs on the first boot of
the installed system, with the image's own tool, and the install looks for
it in the installed image. A manual layout needs no `sfdisk`: it needs the
`mkfs` of each `customMounts[].fstype` it formats (`mkfs.fat`, `mkfs.ext3`, `mkfs.ext4`,
`mkfs.xfs`, `mkfs.btrfs`), and `mkswap`, `swapon` and `swapoff` for a swap
entry. A `varDisk` that is not kept needs `wipefs` and `mkfs.xfs`. A
`varDisk`, a data or swap entry, and a boot entry that is kept need `blkid`.
Every install needs
`skopeo` and `podman`; a `sourceReuse` install also needs `bootc` and `ostree`. fisherman looks for them where they run: on the host,
through `flatpak-spawn --host` inside a Flatpak sandbox.

### Interpreting Errors in JSON

When calling `fisherman validate --json`, errors are returned as:

```json
{
  "protocol_version": 1,
  "valid": false,
  "errors": [
    {
      "field": "hostname",
      "code": "hostname_invalid_char",
      "message": "hostname \"my_pc\" contains '_'; use only letters, digits, hyphens and dots"
    }
  ]
}
```

**Key points for integrators:**
- Use the `code` field to match against this reference (codes are stable and will not change)
- Use the `message` field for user-facing display (messages may be improved over time)
- The `field` field identifies which recipe field caused the problem
- Multiple problems may be returned; process all of them before showing the user
- If you encounter an unknown code, treat it as an error and show the message

## Compatibility

`protocol_version` changes when a field is removed or renamed or its meaning
changes. Adding a field or a new code does not change it. A code is never
renamed or reused; a frontend must treat an unknown code as "invalid, show
`message`". The choices file has its own `protocol_version`, under the same
rule.

## Why

Before this command the rules were spread across the frontends, and none of
them matched what the backend enforced:

| Rule | GNOME | KDE | COSMIC | Niri | XFCE | fisherman before |
|---|---|---|---|---|---|---|
| hostname | none (strips whitespace) | non-empty | none | none | single RFC 1123 label regex, no length limit | non-empty |
| username | `^[a-z_][a-z0-9_-]{0,31}$` | no user page | no user page | no user page | non-empty | none: `useradd` in the configure step, after the OS was on disk |
| reserved names | none | — | — | — | none | none |
| passphrase | non-empty, confirm matches | non-empty, confirm matches | non-empty | non-empty, confirm matches | non-empty, confirm matches | non-empty |
| encryption types | switch → `luks-passphrase` / `tpm2-luks-passphrase` | own list | own list | own list | own list | enum in `Validate` |
| filesystems | from the image catalog | `xfs` default | `xfs`, `btrfs` | `xfs` only | `xfs`, `ext4`, `btrfs`, `zfs` | `xfs`, `ext4`, `btrfs`, `zfs` |

So `fisherman` installed the OS and then failed on a username like `Alice` or
`root`; only XFCE checked the hostname; and four frontends each carried their
own copy of the encryption table.
