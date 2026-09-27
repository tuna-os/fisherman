# bootc-installer-tui

A terminal UI installer for bootc-based operating systems, powered by [fisherman](https://github.com/tuna-os/fisherman).

## Features

- **11-step wizard**: welcome → network → image → disk → filesystem → encryption → user → SSH keys → confirm → progress → done
- **Live progress**: streams fisherman JSON events and updates a progress bar in real time
- **Disk encryption**: optional LUKS full-disk encryption with passphrase
- **SSH key import**: from GitHub, GitLab, or pasted manually
- **Flexible filesystem**: ext4, xfs, or btrfs

## Usage

```bash
sudo bootc-installer-tui
```

## Building

```bash
go build -o bootc-installer-tui ./cmd/bootc-installer-tui
```

## Requirements

- `fisherman` must be installed and on `$PATH` (or at `/usr/bin/fisherman`)
- Root privileges required for disk operations

## Recipe format

The installer writes a recipe to `/tmp/bootc-installer-recipe.json` and runs:

```
fisherman /tmp/bootc-installer-recipe.json
```

---

Part of the [TunaOS](https://tunaos.org) ecosystem. [Docs](https://tunaos.org) · [Contributing](../CONTRIBUTING.md)