# tuna-installer

Graphical installer for TunaOS — a GTK4/Adwaita frontend paired with **fisherman**, a Go CLI backend for bootc-based OS installs.

## Architecture

```
tuna-installer (Python/GTK4 GUI)
    └─ collects choices → writes /tmp/fisherman-recipe.json
    └─ runs: sudo fisherman /tmp/fisherman-recipe.json (via VTE terminal)

fisherman (Go CLI backend)
    └─ reads recipe.json
    └─ partitions disk (sgdisk)
    └─ sets up LUKS (optional)
    └─ formats filesystem (xfs or btrfs with subvolumes)
    └─ runs: bootc install to-filesystem
    └─ writes /etc/hostname
    └─ unmounts cleanly
```

fisherman is inspired by [Albius](https://github.com/Vanilla-OS/Albius) from Vanilla OS, but purpose-built for bootc-based images — no squashfs extraction, no package management, no GRUB config; bootc handles all of that.

## fisherman Recipe

```json
{
  "disk": "/dev/sda",
  "filesystem": "xfs",
  "btrfsSubvolumes": false,
  "encryption": {
    "type": "none"
  },
  "image": "ghcr.io/tuna-os/yellowfin:gnome-hwe",
  "targetImgref": "ghcr.io/tuna-os/yellowfin:gnome-hwe",
  "selinuxDisabled": true,
  "hostname": "tunaos"
}
```

Encryption types: `none`, `tpm2-luks`, `luks-passphrase`

## Building

```bash
# Build and install
meson setup builddir --prefix=/usr
meson compile -C builddir
sudo meson install -C builddir
```

This compiles the fisherman Go binary and installs the Python GTK4 app.

## Development

```bash
# Run fisherman directly
sudo ./fisherman/fisherman /tmp/recipe.json

# Set TUNAOS_INSTALLER_DEV=1 to show loopback devices in disk selector
TUNAOS_INSTALLER_DEV=1 tuna-installer
```

## Components

- `tuna_installer/` — Python GTK4 app (pages: Welcome → Disk → Confirm → Progress → Done)
- `fisherman/` — Go CLI backend
- `data/` — Desktop entry, icons

## License

GPL-3.0-only
