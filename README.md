# fisherman

> **Active development:** This is the canonical repository for fisherman. The
> project has not moved or been migrated elsewhere; new development and bug
> fixes belong here.

A universal bootc installer backend, designed to be driven by a frontend such as [tuna-installer](https://github.com/tuna-os/tuna-installer).

fisherman handles disk partitioning, formatting, LUKS encryption, and `bootc install to-filesystem` image installation. It works with any bootc-compatible image regardless of distro.

## Architecture

fisherman is a Go CLI that reads a JSON recipe and executes a 9-step install pipeline:

| Step | Action |
|------|--------|
| 1 | Partition disk (3-partition GPT: EFI + ext4 `/boot` + root) |
| 2 | Format EFI (`mkfs.fat -F32`) and `/boot` (`mkfs.ext4`) |
| 3 | Set up LUKS (optional: `cryptsetup luksFormat` + open) |
| 4 | Format root filesystem (`mkfs.xfs` or `mkfs.btrfs`) |
| 5 | Mount everything at `/mnt/fisherman-target` |
| 6 | `bootc install to-filesystem` via `podman run --privileged` |
| 7 | Copy system Flatpaks from host to target |
| 8 | Write `/etc/hostname` into the deployment |
| 9 | Inject `rd.luks.uuid` + Plymouth args into BLS boot entries, then finalize (fstrim → remount ro → fsfreeze) |

The separate ext4 `/boot` partition is required because GRUB cannot read modern XFS features (`nrext64`, `exchange`, `rmapbt`). For composefs/systemd-boot images the EFI partition holds the kernel and initrd directly.

When running inside a Flatpak sandbox, fisherman automatically wraps host subprocess calls via `flatpak-spawn --host`.

## Usage

```bash
sudo fisherman <recipe.json>
```

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

## Image catalog

`data/images.json` is a recursive JSON tree of distro groups and leaf images consumed by tuna-installer's image picker. It can be overridden at runtime:

| Path | Purpose |
|------|---------|
| `/etc/tuna-installer/images.json` | System-wide override |
| `$XDG_CONFIG_HOME/tuna-installer/images.json` | Per-user override |

## Building

```bash
go build ./cmd/fisherman/   # build binary
go vet ./...                # lint
go test ./...               # unit tests
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
