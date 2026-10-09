# fisherman

> ℹ️ **Note on the project home.** This repository is the **origin** and is
> actively maintained (the `dev` branch carries TunaOS fixes, e.g.
> customMounts validation and TPM2 first-boot enrolment).
> [`projectbluefin/fisherman`](https://github.com/projectbluefin/fisherman) is
> a fork of this repo that is kept in sync (see
> [tuna-os/fisherman#59](https://github.com/tuna-os/fisherman/pull/59)); pick
> whichever home matches your distro's tooling.

A universal bootc installer backend, designed to be driven by a frontend. The GNOME, KDE, COSMIC, Niri and XFCE frontends live in [tuna-os/bootc-installer](https://github.com/tuna-os/bootc-installer), which pins this repository as a submodule.

fisherman handles disk partitioning, formatting, LUKS encryption, and `bootc install to-filesystem` image installation. It works with any bootc-compatible image regardless of distro.

## Architecture

fisherman is a Go CLI that reads a JSON recipe and executes a 9-step install pipeline:

| Step | Action |
|------|--------|
| 1 | Partition disk — layout depends on bootloader (see below) |
| 2 | Format EFI (`mkfs.fat -F32`) and `/boot` (`mkfs.ext4`) |
| 3 | Set up LUKS (optional: `cryptsetup luksFormat` + open) |
| 4 | Format root filesystem (`mkfs.xfs` or `mkfs.btrfs`) |
| 5 | Mount everything at `/mnt/fisherman-target` |
| 6 | `bootc install to-filesystem` via `podman run --privileged` |
| 7 | Copy system Flatpaks from host to target |
| 8 | Write `/etc/hostname` into the deployment |
| 9 | Inject `rd.luks.uuid` + Plymouth args into BLS boot entries, then finalize (fstrim → remount ro → fsfreeze) |

The separate ext4 `/boot` partition is required for GRUB because GRUB cannot read modern XFS features (`nrext64`, `exchange`, `rmapbt`). For composefs/systemd-boot images the EFI partition holds the kernel and initrd directly.

### Partition layouts

| Bootloader | Layout | EFI | /boot | Notes |
|---|---|---|---|---|
| `grub2` (bluefin, lts) | 3-partition GPT | **2 GiB** FAT32 | **2 GiB** ext4 | GRUB reads kernel from ext4 /boot |
| `systemd` (dakota) | 2-partition GPT | **2 GiB** FAT32 | — | systemd-boot reads kernel directly from FAT32 ESP |

All fleet images use a **2 GiB ESP** for consistency. Each kernel+initrd pair is 200–400 MiB; 2 GiB accommodates the booted entry, rollback, and a staged upgrade without running out of space.

When running inside a Flatpak sandbox, fisherman automatically wraps host subprocess calls via `flatpak-spawn --host`.

## Usage

```bash
sudo fisherman <recipe.json>
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
| `fisherman images [<query>]` | no | list or search the image catalog |
| `fisherman scan <disk>` | yes | scan a disk for Windows data available to migrate |
| `fisherman probe --json` | no | print disks, TPM, RAM/CPU/UEFI, live-media and offline-store facts as one JSON object; read-only. Schema and an example: [`docs/PROBE.md`](docs/PROBE.md) |
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

**Encryption types:** `none`, `luks-passphrase`, `tpm2-luks`, `tpm2-luks-passphrase`

For `luks-passphrase` and `tpm2-luks-passphrase`, add `"passphrase": "hunter2"` inside the `encryption` object.

**Empty `image`** means "install the image this live system is running"
(bootc installs the booted container). It is accepted only on live media, by
the same detection `fisherman probe` reports as `live.is_live`
([`docs/PROBE.md`](docs/PROBE.md#live-detection)). On any other host the
recipe fails validation (`invalid recipe: image is required: …`, exit 1)
before a disk is touched.

**Offline image stores** (`additionalImageStores`): leave the field out and
fisherman finds the offline containers-storage roots on the host itself,
with the discovery `fisherman probe` reports as `offline.stores`
([`docs/PROBE.md`](docs/PROBE.md#offline-stores)), and exposes them to bootc
read-only. A list, including `[]`, is used exactly as given; `[]` turns
discovery off. `/var/lib/superiso-store` is exposed whenever it exists, as
before.

## Image catalog

`data/images.json` is a recursive JSON tree of distro groups and leaf images consumed by tuna-installer's image picker. It can be overridden at runtime:

| Path | Purpose |
|------|---------|
| `/etc/tuna-installer/images.json` | System-wide override |
| `$XDG_CONFIG_HOME/tuna-installer/images.json` | Per-user override |

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

The nightly CI runs a full install + QEMU boot test for each image in `tests/bootcrew-matrix.yaml`:

| Image | Filesystem | composefs |
|-------|-----------|-----------|
| bluefin:lts | xfs | no |
| yellowfin:gnome50 | xfs | no |
| ubuntu-bootc | xfs | yes |
| opensuse-bootc | xfs | yes |
| arch-bootc | xfs | yes |
| debian-bootc | xfs | yes |
| frostyard/snow | btrfs | yes |

Fast CI (PR gate) runs a subset. Add new images to `tests/bootcrew-matrix.yaml` to include them in both workflows automatically.

## License

GPL-3.0-only
