# Contributing to fisherman

Thank you for your interest in contributing to `fisherman`, the universal bootc installer backend.

## Branch Strategy

- **`dev`** is the primary development branch. All PRs must target `dev`.
- Do not target `prod` directly unless specifically instructing a release hotfix.

## Development Workflow

### Prerequisites

- [Go](https://go.dev/) 1.22 or newer for the fisherman backend
- Go 1.26.2 or newer when building or testing the TUI (`tui/go.mod`)
- [just](https://github.com/casey/just) command runner
- Standard Linux storage utilities (`util-linux`, `dosfstools`, `e2fsprogs`, `xfsprogs`, `cryptsetup`, `podman`, `skopeo`)

### Building

The Go module for fisherman is located inside the `fisherman/` subfolder:

```bash
cd fisherman
go build ./cmd/fisherman/
```

Alternatively, from the repository root using `just`:

```bash
just build
```

The compiled binary will be placed at `/tmp/fisherman`.

To build the terminal UI, use its separate Go module:

```bash
cd tui
go build -o bootc-installer-tui ./cmd/bootc-installer-tui
```

### Running Tests and Verification

Run Go unit tests:

```bash
cd fisherman
go test -v ./...
```

Run the TUI tests separately with its newer toolchain:

```bash
cd tui
go test ./...
```

Run validation tests:

```bash
just test-checks
```

For full VM end-to-end testing with QEMU:

```bash
just bootcrew-vm
```

### Code Quality & Linting

Run standard Go linting before submitting a pull request:

```bash
cd fisherman
go vet ./...
```

If `golangci-lint` is installed:

```bash
golangci-lint run ./...
```

## Pull Request Guidelines

1. **Target Branch:** Create feature/fix branches off `dev` and submit PRs targeting `dev`.
2. **Commit Sign-off (DCO):** All commits must include a Developer Certificate of Origin sign-off line:
   ```bash
   git commit -s -m "docs: description"
   ```
3. **Documentation:** Ensure any changes to recipe schemas, CLI flags, or partition logic are documented in `README.md`, `ROADMAP.md`, or `docs/`.
