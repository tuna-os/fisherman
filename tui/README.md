# bootc-installer-tui

A terminal UI installer for bootc-based operating systems, powered by [fisherman](https://github.com/tuna-os/fisherman).

## Features

- **Wizard**: welcome → image → disk → filesystem and hostname → encryption → user → confirm → progress → done
- **Boot stack from the image**: each image carries its boot loader, its backend (ostree or composefs) and its filesystems. The recipe gets them. A custom URL asks for the boot stack; the TUI does not guess it.
- **Disks from fisherman**: the list is `fisherman probe --json`, and only the disks it marks eligible are offered. With no usable disk the page says why.
- **Field checks from fisherman**: each page sends its fields to `fisherman validate --json --partial -`. The TUI has no copy of the rules.
- **Live progress**: reads fisherman's JSON events and sets the bar to `overall_pct`
- **Disk encryption**: optional LUKS full-disk encryption with a passphrase
- **Recovery key**: when fisherman emits a `recovery_key` event, the key is shown on its own page, and you must confirm that you have it

The TUI has no network page and no SSH key page. The recipe has no field for them.

## Usage

```bash
sudo bootc-installer-tui
sudo bootc-installer-tui --dry-run   # no install; shows the recipe with its secrets masked
```

Ctrl+C during an install asks fisherman to stop (SIGTERM) and waits for it to clean up.

## Building

```bash
go build -o bootc-installer-tui ./cmd/bootc-installer-tui
```

## Requirements

- `fisherman` must be on `$PATH`, or at `/usr/bin/fisherman` or `/usr/local/bin/fisherman`. A `fisherman` file in the current directory is never used.
- Root privileges are required for disk operations

## Image settings

The image list is built in. For an image that the fisherman catalog (`images.json`) lists, the catalog's `bootloader`, `composefs`, `filesystem`, `filesystems` and `needs_user_creation` replace the built-in values. An image with `needs_user_creation: false` gets no user page and no `user` block; the default is `true`.

The catalog is read from `$FISHERMAN_IMAGES_PATH`, `/etc/bootc-installer/images.json`, `/etc/tuna-installer/images.json`, `data/images.json` beside the binary, or `/usr/share/fisherman/data/images.json`.

XFS with the composefs backend is refused before the confirm page: composefs needs fs-verity, and XFS has none.

## How the recipe reaches fisherman

The TUI pipes the recipe to fisherman's stdin and closes it:

```
fisherman -
```

The recipe holds the LUKS passphrase and the user's password. The TUI does not write it to a file and does not put it in the arguments.

## Tests

```bash
go test ./...
```

The tests use a fake `fisherman` on `PATH` (`internal/testbackend`). `internal/tui/realbackend_test.go` also builds the real `../fisherman` and runs the TUI's recipes through `fisherman validate`. No test installs anything.

---

Part of the [TunaOS](https://tunaos.org) ecosystem. [Docs](https://tunaos.org) · [Contributing](../CONTRIBUTING.md)
