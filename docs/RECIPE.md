# Recipe reference

A recipe is one JSON object. fisherman reads it from a file
(`fisherman recipe.json`) or from stdin (`fisherman -`), and
`fisherman validate` checks it with the same rules. This page lists every
key the recipe has, with its type and what fisherman does when the key is
absent.

- The Go type `Recipe` in
  [`fisherman/internal/recipe/recipe.go`](../fisherman/internal/recipe/recipe.go)
  is the source. `TestRecipeReferenceCoversEveryField`
  (`fisherman/internal/recipe/recipe_doc_test.go`) fails when a key of that
  type has no row here, when a row names a key that the type does not have,
  or when the type column is wrong.
- The rules for each value (characters, lengths, combinations) and their
  error codes are in [VALIDATE.md](VALIDATE.md#rules). This page does not
  repeat them.
- A key that is not on this page is an error (`recipe_unknown_field`), at
  any depth. Key names match without regard to case, as Go's JSON decoder
  matches them.
- A JSON schema for frontends is in bootc-installer:
  [`shared/recipe/fisherman-recipe.schema.json`](https://github.com/tuna-os/bootc-installer/blob/dev/shared/recipe/fisherman-recipe.schema.json),
  with the frontend rules in
  [`shared/recipe/README.md`](https://github.com/tuna-os/bootc-installer/blob/dev/shared/recipe/README.md).
- A recipe holds secrets (`encryption.passphrase`, `user.password`). Give it
  to fisherman on stdin, not in a file. See [INTEGRATION.md](INTEGRATION.md).

"Absent" in the tables means that the key is not in the recipe. For a
string, an empty string `""` means the same as absent. For a list or an
object, `null` means the same as absent, except where the row says
otherwise.

## Fields

### Target disk and layout

| Field | Type | Required | When absent | What it does |
|---|---|---|---|---|
| `disk` | string | yes, unless `customMounts` is set | refused (`disk_required`) | The whole disk to erase and install to, for example `/dev/sda` or a `/dev/disk/by-id/…` link. fisherman refuses the boot disk, a partition, a read-only disk, and (unless `override` says so) a disk under 50 GiB. See [VALIDATE.md](VALIDATE.md#target-eligibility). |
| `filesystem` | string | yes, unless `customMounts` is set | refused (`filesystem_unsupported`) | The root filesystem: `xfs`, `ext4`, `btrfs` or `zfs`. `zfs` needs `"experimental": ["zfs"]`, turns on `composeFsBackend` and uses the systemd-boot layout. |
| `btrfsSubvolumes` | boolean | no | `false` | With `filesystem: btrfs`: put the root in an `@` subvolume (`rootflags=subvol=@,compress=zstd:1`). No `@home`, no `@snapshots`. |
| `zfsPoolName` | string | no | `rpool` | The name of the ZFS pool, with `filesystem: zfs`. |
| `customMounts` | object array | no | the automatic layout | A manual layout: fisherman formats and mounts these partitions and does not partition a disk. `disk`, `filesystem` and `btrfsSubvolumes` are then not used, and `encryption` must be `none`. |
| `customMounts[].partition` | string | yes | refused | The partition, for example `/dev/sda3`. |
| `customMounts[].target` | string | yes | refused | The mount point in the installed system (`/`, `/boot`, `/boot/efi`, `/var`, …) or `swap`. One entry must be `/`. |
| `customMounts[].fstype` | string | no | mount without a format | `fat32`, `ext3`, `ext4`, `xfs`, `btrfs` or `swap` to format the partition; `unformatted` or `""` to mount what is there. |
| `varDisk` | object | no | no separate `/var` disk | A second disk for `/var`. Refused with encryption, `composeFsBackend` or ZFS. |
| `varDisk.disk` | string | yes | refused (`var_disk_required`) | The `/var` disk. It must differ from `disk`. |
| `varDisk.keepExisting` | boolean | no | `false`: format it as XFS | `true`: mount the disk as it is. |
| `targetMount` | string | no | `/mnt/fisherman-target` | The host path where fisherman assembles the target. Change it only when the default path is in use on the host. |
| `override` | string array | no | no override | Target checks the caller accepts failing: `too_small`, `uefi`. |
| `diskIdentity` | object | no | the install is not bound to a device | Binds the install to the disk the frontend planned on: copy the `serial`, `wwn` and `size_bytes` of `plan.target.identity` ([PLAN.md](PLAN.md#plantarget)). validate and the install refuse the recipe (`disk_identity_mismatch`) when the device `disk` names reports another value for a field that is set. At least one field must be set, and not with `customMounts` (`disk_identity_invalid`). See [VALIDATE.md](VALIDATE.md#disk-identity). |
| `diskIdentity.serial` | string | no | not checked | The disk's serial (lsblk `SERIAL`). |
| `diskIdentity.wwn` | string | no | not checked | The disk's World Wide Name (lsblk `WWN`), compared without case. |
| `diskIdentity.sizeBytes` | integer | no | not checked | The disk size in bytes. |
| `planBinding` | object | no | the install is not bound to a plan | Binds the install to the plan the user confirmed: copy `plan.binding` of `fisherman plan --json` ([PLAN.md](PLAN.md#binding-the-install-to-the-confirmed-plan)). The full validate and the install plan the recipe again and refuse it (`plan_stale`) when the target disk, its partitions, what the install destroys or a capacity verdict changed. A `digest` that is not `sha256:` and 64 lowercase hex digits is `plan_binding_invalid`. See [VALIDATE.md](VALIDATE.md#saved-plan). |
| `planBinding.digest` | string | yes | refused (`plan_binding_invalid`) | `plan.binding.digest`. |
| `planBinding.readImage` | boolean | no | `false`: the check reads no image | `plan.binding.read_image`: the plan was made with `--read-image`, so the check reads the image's metadata too (a registry read for an image only a registry has). |

### Encryption

| Field | Type | Required | When absent | What it does |
|---|---|---|---|---|
| `encryption` | object | no | no encryption | The disk encryption. |
| `encryption.type` | string | no | `none` | `none`, `luks-passphrase`, `tpm2-luks` or `tpm2-luks-passphrase`. The order and labels for a UI are in [`data/encryption-choices.json`](../data/encryption-choices.json). A `tpm2-*` type needs a usable TPM 2.0 (`tpm.usable` in `fisherman probe --json`). |
| `encryption.passphrase` | string | for `luks-passphrase` and `tpm2-luks-passphrase` | refused for those types | The LUKS passphrase. fisherman replaces it with `[redacted]` in every event and log line. |
| `luksMapperName` | string | no | `fisherman-root` | The cryptsetup mapper name of the root. The installed system's `rd.luks.name` uses it too; leave it at the default. |

### Image and boot stack

| Field | Type | Required | When absent | What it does |
|---|---|---|---|---|
| `image` | string | yes, except on live media and with `sourceReuse` | on live media: the image that the live system booted; elsewhere refused (`image_required`) | The source image reference, for example `ghcr.io/org/image:tag`. |
| `targetImgref` | string | with `sourceReuse` | the installed system follows `image` | The reference that the installed system follows for updates (bootc `--target-imgref`). fisherman passes it only when it differs from `image`. |
| `sourceReuse` | object | no | the ordinary source paths | Experimental. Install from an image that this installed host's bootc keeps. Needs `"experimental": ["bootc_store_reuse"]`, an empty `image` and a `targetImgref`. See [SOURCE-REUSE.md](SOURCE-REUSE.md). |
| `sourceReuse.store` | string | yes | refused | `bootc`, the only store. |
| `sourceReuse.digest` | string | yes | refused | The manifest digest, `sha256:<64 hex>`, from `bootc_store.items[].digest` of `fisherman probe --json --bootc-image-list`. |
| `sourceReuse.platform` | string | yes | refused | The architecture of the item (`x86_64`, `aarch64`; `amd64`, `arm64` are accepted too). |
| `imageType` | string | no | `bootc` | The install backend. Only `bootc` is implemented; `ostree` is refused. |
| `bootloader` | string | no | `grub2` | `grub2` (3-partition layout: EFI, ext4 `/boot`, root) or `systemd` (systemd-boot, 2-partition layout). Take it from the image catalog: it is a fact of the image. |
| `composeFsBackend` | boolean | no | `false`; `true` with ZFS | Pass `--composefs-backend` to `bootc install`. Needed for composefs-native images. Needs fs-verity, so it is refused with XFS. |
| `unifiedStorage` | boolean | no | `false` | `true` is refused (`unified_storage_unsupported`): fisherman does not pass this flag to bootc. |
| `genericImage` | boolean | no | `false` | Pass `--generic-image` to `bootc install`: skip the bootupd check and the host-specific EFI writes. For ostree images that ship no bootupd. |
| `selinuxDisabled` | boolean | no | `false` | Pass `--disable-selinux` to `bootc install`. When the install needs fisherman's SELinux label workaround (SELinux active on the host), fisherman builds it before the first disk step, or refuses the install (`selinux_workaround_unavailable`). |
| `additionalImageStores` | string array | no | fisherman finds the host's offline stores, as `offline.stores` of `fisherman probe --json` | Host paths of containers-storage roots that bootc may read images from. A list, `[]` included, is used as given; `[]` turns the discovery off. A `CONTAINERS_STORAGE_CONF` in fisherman's environment takes priority. |
| `experimental` | string array | no | no experimental feature | The experimental features the caller accepts: `zfs`, `bootc_store_reuse`. |

### The installed system

| Field | Type | Required | When absent | What it does |
|---|---|---|---|---|
| `hostname` | string | yes | refused (`hostname_required`) | The hostname of the installed system. |
| `user` | object | no | no account | The first account. |
| `user.username` | string | no | no account is made | The login name. An empty name skips the account. |
| `user.fullname` | string | no | no comment | The full name (`useradd --comment`). |
| `user.password` | string | no | no password is set | The password, given to the target's `chpasswd` on stdin: plain text, or a crypt string (`$6$…`), which fisherman passes with `chpasswd -e`. On a composefs target fisherman hashes plain text itself (SHA-512 crypt). fisherman replaces it with `[redacted]` in every event and log line. |
| `user.groups` | string array | no | no extra groups | Extra groups. fisherman drops a group that the image does not have and logs a warning. |
| `locale` | string | no | the image's | `LANG` in `/etc/locale.conf`, for example `de_DE.UTF-8`. |
| `keyboard` | object | no | the image's | The keyboard layout for `/etc/vconsole.conf` and the X11 keyboard configuration. With encryption, its console keymap also reaches the initrd (`rd.vconsole.keymap`). |
| `keyboard.layout` | string | yes, when `keyboard` is present | refused | The XKB layout, for example `de` or `us,ru`. |
| `keyboard.variant` | string | no | none | The XKB variant, for example `nodeadkeys`. |
| `keyboard.model` | string | no | none | The XKB model, for example `pc105`. |
| `timezone` | string | no | the image's | The IANA time zone for `/etc/localtime`, for example `Europe/Berlin`. |
| `flatpaks` | string array | no | copy the host's system Flatpaks | Absent or `null`: copy the host's whole `/var/lib/flatpak`. `[]`: copy nothing. A list: copy the whole installation all the same; it is not a filter, nothing is downloaded, and the log names each listed app that the host does not have. |
| `flatpakVarPath` | string | no | fisherman finds it from the deployment | The path, relative to the target root, of the writable `/var` that receives the Flatpaks. For layouts that fisherman cannot find. |
| `copyNetworkProfiles` | boolean | no | copy only when the host booted from live media | Copy the host's NetworkManager connection profiles (Wi-Fi passwords). `true`: on any host. `false`: never. |
| `copyBluetoothPairings` | boolean | no | copy only when the host booted from live media | The same choice for the host's Bluetooth pairings. |
| `setBootNext` | boolean | no | only when the host booted from live media | Set the firmware's `BootNext` to the installed system, so the next boot starts it. `true`: always. `false`: never. |

### Desktop and product extras

| Field | Type | Required | When absent | What it does |
|---|---|---|---|---|
| `extras` | object | no | the image catalog's `extras` for this image; none for an image the catalog does not know | The desktop and product steps of the post-install stage. An object, `{}` included, is used as given. See the [README](../README.md#desktop-extras). |
| `extras.printServices` | boolean | no | `false` | Turn on `cups-browsed`, `avahi-daemon` and `ipp-usb`, each when the image has the unit. |
| `extras.oemSetup` | boolean | no | `false` | Write the first-login unit that installs vendor packages with Homebrew. |
| `extras.audioNames` | boolean | no | `false` | Write WirePlumber rules with friendly device names. |
| `extras.bootSplash` | boolean | no | `false` | Add `rhgb quiet` to every boot entry. |
| `distroID` | string | no | `bootc` | A short lowercase name for the OEM setup paths and units (`/etc/<distroID>/oem/`, `<distroID>-oem-setup.service`). |
| `brewTap` | string | no | no tap | A Homebrew tap to add before the OEM packages, for example `ublue-os/tap`. |

### Windows data

| Field | Type | Required | When absent | What it does |
|---|---|---|---|---|
| `slurpWallpapers` | boolean | no | `false` | Before the disk is partitioned, copy wallpapers from a Windows (NTFS) partition on the target disk into RAM, and put them in the user's home after the install. A failure does not stop the install. |
| `slurp` | object | no | no data migration | A user-data migration from Windows. Every recipe with this key is refused (`slurp_unsupported`): there is no durable rescue store yet. `fisherman scan` shows what it would find. |
| `slurp.sourcePartition` | string | — | — | The Windows partition. |
| `slurp.users` | object array | — | — | The Windows users to migrate. |
| `slurp.users[].name` | string | — | — | The Windows user name. |
| `slurp.users[].categories` | string array | — | — | The folders to copy, for example `Documents`, `Pictures`, `Desktop`. |

## Example

A recipe for an encrypted install with an account:

```json
{
  "disk": "/dev/nvme0n1",
  "filesystem": "btrfs",
  "btrfsSubvolumes": true,
  "encryption": {"type": "luks-passphrase", "passphrase": "correct horse"},
  "image": "ghcr.io/example/desktop:stable",
  "bootloader": "grub2",
  "hostname": "workstation",
  "user": {"username": "alice", "fullname": "Alice", "password": "secret", "groups": ["wheel"]},
  "locale": "de_DE.UTF-8",
  "keyboard": {"layout": "de", "variant": "nodeadkeys"},
  "timezone": "Europe/Berlin",
  "flatpaks": []
}
```

Check it with `fisherman validate --json - < recipe.json` before an install.
