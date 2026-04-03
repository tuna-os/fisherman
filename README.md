# fisherman

A bootc-only installer backend for TunaOS, inspired by [Albius](https://github.com/vanilla-os/albius).

fisherman handles disk partitioning, formatting, encryption (LUKS), and bootc image installation. It is designed to be driven by a frontend such as [tuna-installer](https://github.com/tuna-os/tuna-installer).

## Architecture

fisherman is a Go CLI that accepts a JSON recipe describing the installation steps and executes them against the target disk using `bootc install to-disk`.

When running inside a Flatpak sandbox, fisherman automatically wraps host subprocess calls via `flatpak-spawn --host`.

## Usage

```bash
sudo fisherman <recipe.json>
```

## Recipe format

```json
{
  "disk": "/dev/sda",
  "imgref": "ghcr.io/tuna-os/yellowfin:latest",
  "luks": false,
  "luks_password": ""
}
```

## Building

```bash
go build ./...
```

## License

GPL-3.0-only
# Test
# Test
