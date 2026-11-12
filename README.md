# fisherman

> ℹ️ **Note on the project home.** This repository is the **origin** and is
> actively maintained (the `dev` branch carries TunaOS fixes, e.g.
> customMounts validation and TPM2 first-boot enrolment).
> [`projectbluefin/fisherman`](https://github.com/projectbluefin/fisherman) is
> a fork of this repo that is kept in sync (see
> [tuna-os/fisherman#59](https://github.com/tuna-os/fisherman/pull/59)); pick
> whichever home matches your distro's tooling.

fisherman is the part of a [bootc](https://bootc-dev.github.io/bootc/) installer that changes the disk.
It reads one JSON recipe, then partitions and encrypts the disk, installs the
image with `bootc install to-filesystem`, and prepares the first boot. It
reports progress as JSON lines, so a program can drive it.

It has no wizard of its own. The GNOME, KDE, COSMIC, Niri and XFCE wizards live
in [tuna-os/bootc-installer](https://github.com/tuna-os/bootc-installer), which
pins this repository as a submodule. [wootc](https://github.com/tuna-os/wootc)
uses it to install from Windows.

## Why you need it

`bootc install to-filesystem` writes an image to a root filesystem that
already exists and is already mounted. Everything around that call is left to
the caller, and that is where installers go wrong:

- choosing a partition layout that the image's bootloader can boot;
- LUKS2, TPM2 enrolment and a recovery key;
- the kernel arguments that unlock the disk at boot;
- a user account, a hostname and the system Flatpaks;
- installing with no network, from an image on the install media;
- telling a user why a recipe is wrong before a disk is erased;
- unmounting, closing and cleaning up when a step fails or the user cancels.

fisherman does that work once, behind one recipe format, and tests it by
installing real images in VMs every night. A frontend only has to collect
choices and draw a progress bar.

| You are | Use fisherman to |
|---|---|
| Writing an installer UI | Skip the disk work. Ask `fisherman probe --json` what the machine has, check fields with `fisherman validate --json --partial`, pipe a recipe to `fisherman -`, and read progress. |
| Building a bootc image | Get an install path for your image without a distro-specific installer. Any image that `bootc install to-filesystem` accepts works; the nightly matrix covers CentOS, Ubuntu, Debian, Arch and openSUSE bases, among others. |
| Automating installs | Run unattended from a recipe file, with machine-readable validation errors and progress. |

> [!WARNING]
> **An install erases the disk named in the recipe.** There is no confirmation
> prompt: fisherman is built to be driven by a frontend that has already asked.
> Before you run it by hand, check `disk` twice, back up anything you need, and
> run `fisherman validate` first. It needs root, a UEFI machine, and `podman`,
> `cryptsetup`, `sfdisk` and the `mkfs` tools on the host; a bootc live ISO has
> them.

## Architecture

fisherman is a Go CLI that reads a JSON recipe and runs an install pipeline. The stages below always run in this order. The number of progress steps a run reports depends on the recipe (encryption, a second disk and TPM enrolment each add one), so read `cumulative_pct` from the progress stream rather than counting steps ([`docs/JSON-PROGRESS-FORMAT.md`](docs/JSON-PROGRESS-FORMAT.md)).

| Stage | What it does | When |
|---|---|---|
| Prepare disk | unmount and release whatever holds the target disk | always |
| Partition | write the GPT; the layout depends on the bootloader (see below) | always |
| Format EFI | `mkfs.fat -F32`, and `mkfs.ext4` for `/boot` on GRUB layouts | always |
| Encrypt | `cryptsetup luksFormat` and open | encrypted recipes |
| Format root | `mkfs.xfs`, `mkfs.btrfs`, `mkfs.ext4` or a ZFS pool | always |
| Mount | mount everything at `/mnt/fisherman-target` | always |
| Format data disk | a second disk for `/var` | recipes that name one |
| Install OS | `bootc install to-filesystem --skip-finalize` through `podman run --privileged` | always |
| Enrol TPM2 | add a single-use keyslot, stage the first-boot unit that binds the LUKS volume to the TPM, and emit the recovery key | TPM recipes |
| Copy Flatpaks | copy the host's system Flatpak installation to the target | always; copies nothing when `flatpaks` is `[]` |
| Configure | fstab, hostname, user account, locale, keyboard and time zone, `rd.luks.name` and Plymouth kernel arguments, Wi-Fi profiles and Bluetooth pairings copied from the host (live media, or on request), and the [desktop extras](#desktop-extras) the recipe or the catalog turns on | always |
| Finalize | fstrim → remount read-only → fsfreeze, then thaw | always (skipped on ZFS) |

The separate ext4 `/boot` partition is required for GRUB because GRUB cannot read modern XFS features (`nrext64`, `exchange`, `rmapbt`). For composefs/systemd-boot images the EFI partition holds the kernel and initrd directly.

### Partition layouts

| Bootloader | Layout | EFI | /boot | Notes |
|---|---|---|---|---|
| `grub2` (bluefin) | 3-partition GPT | **2 GiB** FAT32 | **2 GiB** ext4 | GRUB reads kernel from ext4 /boot |
| `systemd` (dakota) | 2-partition GPT | **2 GiB** FAT32 | — | systemd-boot reads kernel directly from FAT32 ESP |
| `filesystem: zfs` | 2-partition GPT | **1 GiB** FAT32 | — | ESP plus a ZFS pool; not covered by the VM matrix (#413) |

GRUB and systemd-boot layouts use a **2 GiB ESP** for consistency. Each kernel+initrd pair is 200–400 MiB; 2 GiB accommodates the booted entry, rollback, and a staged upgrade without running out of space.

When running inside a Flatpak sandbox, fisherman wraps most host subprocess calls via `flatpak-spawn --host`. The frontends do not rely on this: they start fisherman itself on the host, and an install from live media refuses to run inside a sandbox.

## Usage

Validate first. This changes nothing and needs no root:

```bash
fisherman validate recipe.json
```

Then install. **This erases `disk`.**

```bash
sudo fisherman recipe.json
# or, with the recipe on stdin (no temporary file):
generate-recipe | sudo fisherman -
```

`-` reads the recipe JSON from stdin until EOF, for the install and for
`validate`. pkexec, sudo and `flatpak-spawn --host` all pass stdin through,
so a frontend can pipe the recipe instead of writing it, LUKS passphrase and
all, to a file both its sandbox and root on the host can see:

```sh
printf '%s' "$RECIPE_JSON" | flatpak-spawn --host bash -c 'pkexec /usr/local/bin/fisherman -; exit $?'
```

Empty stdin is an error (`loading recipe: recipe is empty`), as is input
that is not one JSON object. Recipes are capped at 1 MiB.

### Commands

| Command | Root? | What it does |
|---|---|---|
| `fisherman <recipe.json>` | yes | run an installation from a recipe |
| `fisherman -` | yes | run an installation from a recipe read on stdin |
| `fisherman validate <recipe.json \| ->` | no | validate a recipe without installing, including the empty-image check below; `-` reads stdin |
| `fisherman validate --json [--partial] <recipe.json\|->` | no | every problem as one JSON object with stable codes (hostname, username, filesystem, encryption, disk, TPM), the same rules the install enforces before touching a disk; `--partial` checks one page's fields. Schema, codes and the encryption choices file: [`docs/VALIDATE.md`](docs/VALIDATE.md) |
| `fisherman images [<query>]` | no | list or search the image catalog |
| `fisherman scan <disk>` | yes | scan a disk for Windows data available to migrate; output contract and safety notes: [`docs/SCAN.md`](docs/SCAN.md) |
| `fisherman probe --json` | no | print disks, TPM, RAM/CPU/UEFI, live-media and offline-store facts as one JSON object; read-only. Schema and an example: [`docs/PROBE.md`](docs/PROBE.md) |
| `fisherman verify --json` | yes, for `bootc status` | on the installed system, or against what its first boot collected: compare the installation receipt with the booted image (`verified`, `mismatch`, `unsupported`, `unavailable`, `not-yet-run`); read-only, exit 0 only for `verified`. Schemas and codes: [`docs/RECEIPT.md`](docs/RECEIPT.md) |
| `fisherman version` | no | print the version |

## Recipe format

```json
{
  "disk": "/dev/sda",
  "filesystem": "xfs",
  "composeFsBackend": false,
  "unifiedStorage": false,
  "selinuxDisabled": false,
  "encryption": {
    "type": "none"
  },
  "image": "ghcr.io/tuna-os/yellowfin:gnome50",
  "hostname": "myhost",
  "flatpaks": ["org.mozilla.firefox"]
}
```

The example shows the common fields. A recipe can also carry `user`,
`bootloader`, `targetImgref`, `btrfsSubvolumes`, `varDisk`, `customMounts`
and more; the struct in
[`fisherman/internal/recipe/recipe.go`](fisherman/internal/recipe/recipe.go)
is the reference until #320 lands.

Things a recipe author should know today:

- An unknown key is an error (`recipe_unknown_field`), in the install and in `fisherman validate`. A misspelt field is not dropped.
- `disk` must be a whole disk that the running system is not on. fisherman refuses the boot disk, a partition, a read-only disk, a disk under 50 GiB and a BIOS host before it touches anything; `"override": ["too_small", "uefi"]` accepts the last two. See [`docs/VALIDATE.md`](docs/VALIDATE.md#target-eligibility).
- A manual layout (`customMounts`) needs a `/boot/efi` entry, and a `/boot` entry for GRUB with an XFS root.
- `unifiedStorage: true` is refused (`unified_storage_unsupported`): fisherman does not pass that flag to bootc.
- One install runs on a host at a time. A second one is refused (`install_already_running`).
- `flatpaks` says whether the host's system Flatpaks are copied, not which. Absent: the host's whole `/var/lib/flatpak` is copied. `[]`: nothing is copied. A list: the whole installation is copied all the same, nothing is downloaded, and the log names each listed app that the host does not have.
- `varDisk` works on unencrypted ostree targets only. With an encrypted `encryption.type` it is refused (`var_disk_encryption_unsupported`): fisherman does not encrypt the `/var` disk. With `composeFsBackend` or `filesystem: zfs` it is refused too (`var_disk_unsupported_composefs`, [#252](https://github.com/tuna-os/fisherman/issues/252)).
- A `varDisk`, and each data or swap entry of `customMounts`, gets an fstab line on the installed system (type and UUID from `blkid`, `nofail`). A target that cannot be mounted again is refused (`custom_mount_not_persistable`). See [`docs/VALIDATE.md`](docs/VALIDATE.md#mounts-after-the-install).
- An encrypted install fails if its boot entries do not carry `rd.luks.name` at the end. Only systemd-boot with composefs is exempt (#410).
- `locale` (`"de_DE.UTF-8"`), `keyboard` (`{"layout": "de", "variant": "nodeadkeys", "model": "pc105"}`; `layout` is required when the object is present) and `timezone` (`"Europe/Berlin"`) are optional. fisherman writes them to the installed system's `/etc/locale.conf`, `/etc/vconsole.conf`, `/etc/X11/xorg.conf.d/00-keyboard.conf` and `/etc/localtime`. Without a field, the image's setting stays.
- For an encrypted install with `keyboard`, fisherman adds `rd.vconsole.keymap=<keymap>` to the boot entries, so the passphrase prompt in the initrd uses the layout the passphrase was typed with. An image that boots a unified kernel image has no boot entry to edit; the log then says that the prompt uses the image's keymap. A graphical prompt that reads keys through libxkbcommon takes its layout from the initrd's own `vconsole.conf`, which fisherman cannot change.
- The host's Wi-Fi profiles and Bluetooth pairings are copied to the target only when the host booted from live media. `"copyNetworkProfiles"` and `"copyBluetoothPairings"` (`true` or `false`) override that for each kind. On an installed host the default is not to copy: the keys are the operator's.
- fisherman sets the firmware's `BootNext` to the installed system's boot entry when the host booted from live media, so the next boot does not start the installer again. `"setBootNext": true` does it on any host, `false` never. A failure is a warning in the log.
- `"filesystem": "zfs"` is experimental and needs `"experimental": ["zfs"]` (`zfs_experimental`). No VM test installs and boots it. The pool is exported when the install ends, also when it fails.
- `btrfsSubvolumes` puts the root in an `@` subvolume (`rootflags=subvol=@,compress=zstd:1`). fisherman writes the root's `rootflags=` into every BLS boot entry itself, in place of the one bootc derives: bootc 1.15.x drops `--karg` on the composefs path. It makes no `@home` and no `@snapshots`: `/home` is `/var/home` inside `@`, as on every bootc system.
- Print services, the OEM setup unit, friendly audio names and `rhgb quiet` are applied only when the recipe's `extras` or the image catalog turns them on. See [Desktop extras](#desktop-extras).
- fisherman builds no caches in the target. The image must carry its font, icon, schema and linker caches.

**Encryption types:** `none`, `luks-passphrase`, `tpm2-luks`, `tpm2-luks-passphrase`

For `luks-passphrase` and `tpm2-luks-passphrase`, add `"passphrase": "hunter2"` inside the `encryption` object.

**Empty `image`** means "install the image this live system is running"
(bootc installs the booted container). It is accepted only on live media, by
the same detection `fisherman probe` reports as `live.is_live`
([`docs/PROBE.md`](docs/PROBE.md#live-detection)). On any other host the
recipe fails validation (`invalid recipe: image is required: …`, exit 1)
before a disk is touched.

**Embedded live media**: the host-owned
`/etc/bootc-installer/live-iso-mode` marker makes
`/etc/bootc-installer/media-input.json` mandatory for every frontend.
Fisherman needs to run natively on the host, outside a Flatpak sandbox,
before it reads required media.
It resolves the receipt before bootc status, offline-store discovery or
image-cache/registry inspection.

Fisherman checks that the layout and every referenced blob are read-only,
rejects symlinks, and verifies manifest, config and layer digests and sizes.
It refuses installation before disk work for missing or invalid receipts,
invalid layouts or incompatible requests, without registry fallback.
An empty recipe `image` selects the embedded source.
A nonempty `image` must equal the receipt's `image` or canonical exact
`oci:<ociLayoutPath>` source, not merely its day-2 `targetImgref`.
Any explicit `targetImgref` must match the receipt's target.

Fisherman passes the verified OCI layout to bootc as the source and keeps the
receipt's target as the day-2 update reference, including composefs installs.
OCI layouts are not `additionalImageStores`.
Without the marker, Fisherman uses legacy `CheckImage` for unrelated explicit
requests, whether they name a network image or a standalone OCI layout.
This also applies when an optional receipt contains malformed data.

**Offline image stores** (`additionalImageStores`): leave the field out and
fisherman finds the offline containers-storage roots on the host itself,
with the discovery `fisherman probe` reports as `offline.stores`
([`docs/PROBE.md`](docs/PROBE.md#offline-stores)), and exposes them to bootc
read-only. A list, including `[]`, is used exactly as given; `[]` turns
discovery off. `/var/lib/superiso-store` is exposed whenever it exists, as
before.

**Installing from the host's own bootc store** (`sourceReuse`,
experimental): a recipe with `"sourceReuse": {"store": "bootc", "digest":
"sha256:…", "platform": "x86_64"}`, an empty `image`, an explicit
`targetImgref` and `"experimental": ["bootc_store_reuse"]` installs the
image the installed (not live) ostree host's bootc keeps under that digest:
fisherman copies it with `bootc image copy-to-storage` to a name of its own,
verifies it, installs from it and removes it. Never a default; see
[`docs/SOURCE-REUSE.md`](docs/SOURCE-REUSE.md).

## Image catalog

`data/images.json` is a recursive JSON tree of distro groups and leaf images.
`fisherman images` lists and searches it, and the frontends use it for their
image picker. fisherman looks for the catalog in this order:

| Path | Purpose |
|------|---------|
| `$FISHERMAN_IMAGES_PATH` | explicit override |
| `data/images.json` next to the binary | development builds |
| `/usr/share/fisherman/data/images.json` | installed |
| `/app/share/fisherman/data/images.json` | Flatpak |

A frontend can ship its own catalog; bootc-installer reads
`/etc/bootc-installer/images.json` (see its
[customising guide](https://github.com/tuna-os/bootc-installer/blob/dev/docs/guide/customising.md)).

### Desktop extras

Four post-install steps suit a desktop product image and are wrong for an
image fisherman knows nothing about. They are off unless something turns
them on.

| `extras` key | Step |
|---|---|
| `printServices` | Enable `cups-browsed`, `avahi-daemon` and `ipp-usb`. Each unit only when the installed image has it. |
| `oemSetup` | Write the first-login unit that installs vendor packages with Homebrew (`ublue-os/tap`), enable the vendor's service when the image has it, and set a vendor menu icon. |
| `audioNames` | Write WirePlumber rules with friendly device names to the installed system. On live media, also apply them to the running session. |
| `bootSplash` | Add `rhgb quiet` to every boot entry. |

The decision, in this order:

1. The recipe's `extras` object. fisherman uses it exactly as given; `{}` turns all four off.
2. With no `extras` in the recipe, the catalog entry of the image. fisherman looks up the recipe's `targetImgref` and `image` (and the live image) in `/etc/bootc-installer/images.json`, `/etc/tuna-installer/images.json` and then the paths above. A node's `extras` object applies to its children; a child's own object replaces it whole.
3. Otherwise none. The log says `Desktop extras: none (…)`.

Every entry of the shipped catalog has all four on, because every install
did all four before the field existed. An image that is not in a catalog
(`centos-bootc`, a server image, your own build) gets none of them unless
its recipe asks.

A frontend that builds a recipe from a catalog entry should copy that
entry's inherited `extras` object into the recipe. Then the result does not
depend on which catalog fisherman can find on the host.

## Building

```bash
cd fisherman
go build ./cmd/fisherman/   # build binary
go vet ./...                # lint
go test ./...               # unit tests
```

Or from the repository root:

```bash
just build                  # builds binary to /tmp/fisherman
```

## CI / Bootcrew integration tests

The nightly CI (`bootcrew-nightly.yml`) runs a full install + QEMU boot test for each image in `tests/bootcrew-matrix.yaml` that is not marked `nightly: false`. It installs with the entry's `bootloader` and asserts that bootloader's partition layout:

| Image | Filesystem | composefs | Bootloader | Nightly |
|-------|-----------|-----------|------------|---------|
| dakota | ext4 | yes | systemd | gating |
| centos-bootc | xfs | no | grub2 | gating |
| yellowfin:gnome50 | xfs | no | grub2 | gating |
| ubuntu-bootc | ext4 | yes | systemd | gating |
| opensuse-bootc | xfs | no | grub2 | advisory (upstream image ships no podman) |
| arch-bootc | ext4 | yes | systemd | gating |
| debian-bootc | ext4 | yes | systemd | gating |
| frostyard/snow | btrfs | yes | systemd | gating |

`nightly_advisory: true` runs an image without letting its failure fail the workflow; it is reserved for images whose upstream is shown to be broken, with the evidence in the matrix comment.

Fast CI (PR gate) runs a subset. Add new images to `tests/bootcrew-matrix.yaml` to include them in both workflows automatically.

<!-- hive-contribute-plea: donated-compute appeal, keep in sync across repos -->
## Contribute compute — no code needed

**[Donate time with your AI CLI](https://hub.tunaos.org/#contribute).**

Your machine runs one task at a time using your signed-in AI coding CLI.
The relay alternates between eligible **School** (OS) and **Reef** (apps)
hives after each task. If one hive has no work, the other can continue.

The guide covers prerequisites, verified `just` setup, credentials, provider
costs, and how to stop with Ctrl+C. Your subscription quota or API charges apply.
Never post account credentials or API keys in issues or pull requests.

## License

GPL-3.0-only
