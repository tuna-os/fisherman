# Contributing to fisherman

Thank you for your interest in contributing to `fisherman`, the universal bootc installer backend.

## Branch Strategy

- **`dev`** is the primary development branch. All PRs must target `dev`.
- Do not target `prod` directly unless specifically instructing a release hotfix.

## Releasing

A release is two steps, both deliberate. Nothing promotes on its own — this
repository has no `promote.yml`.

1. **Cut it.** Dispatch `release-cut.yml` on `dev` with a `bump` of `patch`,
   `minor` or `major`. It computes the next version from the last tag, pushes
   the tag, calls `release-publish.yml` to build and attach the binaries, and
   opens a `dev` → `prod` promotion PR.
2. **Promote it.** Review and merge that PR.

### Merge the promotion PR with a merge commit

**Never squash or rebase the `dev` → `prod` PR.** This is the one irreversible
choice in the process.

A squash discards dev's commits and writes a single new one that shares no
ancestry with them. `prod` then reads as diverged from `dev` permanently, even
though the two hold identical content, and every later promotion conflicts in
any file touched on both sides.

That is not hypothetical: #220 was squashed into `prod`, which left `prod` one
commit "ahead" and 261 behind with a byte-identical tree, and the next
promotion (#226) conflicted across 18 files. Recovering needs a hand-resolved
merge that takes dev's tree wholesale.

A merge commit keeps dev's tip as an ancestor of `prod`, so the next promotion
is an ordinary fast-forward.

`release-cut.yml` repeats this warning at the top of every promotion PR it
opens. GitHub cannot enforce a merge method per branch — the setting is
repository-wide and `dev` deliberately uses squash — so the PR body and this
section are the control.

### Recovering a tag that was never published

`release-publish.yml` accepts `workflow_dispatch` with a `tag` input. If a tag
exists but its release has no assets, dispatch it with that tag rather than
cutting a new version.

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
