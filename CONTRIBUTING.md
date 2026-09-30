# Contributing to fisherman

Thank you for your interest in contributing to `fisherman`, the universal bootc installer backend.

## Branch Strategy

- **`dev`** is the primary development branch. All PRs must target `dev`.
- Do not target `prod` directly unless specifically instructing a release hotfix.

## Releasing

Releases are automatic. `promote.yml` watches `dev`, and when a commit has
every required check green and has sat for 30 minutes, it fast-forwards
`prod` to that commit, tags it, and publishes the binaries.

Nothing needs a human in the normal case. What used to be two deliberate
steps — dispatch the cut, then merge a promotion PR — is now the gate.

### How the version is chosen

From the conventional-commit subjects since the last tag, by
`scripts/next-version.sh`:

| in the range | bump |
|---|---|
| a `!` before the colon, or a `BREAKING CHANGE:` trailer | major |
| any `feat:` | minor |
| anything else | patch |

Two deliberate wrinkles:

- **Bot prefixes are stripped first.** Commits arrive as
  `[sec-check] fix: ...`; without stripping, every one of them reads as an
  unknown type and quietly becomes a patch — wrong for a `feat`.
- **Before 1.0.0 a breaking change bumps the minor**, not the major. Major 0
  is the statement that the API is not stable yet, and spending it on the
  first `feat!:` would claim a stability this project has not declared.

`tests/test-next-version.sh` covers both, plus the case where a body merely
mentions "BREAKING CHANGE:" mid-sentence and must *not* trigger a major.

So write conventional commits. A `feat:` that lands as `chore:` ships as a
patch, and nobody finds out until they read the tag.

### The manual path

`release-cut.yml` is still there for forcing a release outside the gate, or
pinning an exact version. Its `bump` input defaults to `auto`, which calls
the same script, so a hand-cut release and an automatic one cannot disagree.

### When prod diverges

`promote.yml` refuses to move `prod` to a commit that is not a descendant of
it — it will never rewind or fork the branch. That guard is also why a
diverged `prod` stops promotion dead.

`prod` diverges when a promotion PR is **squashed** instead of merged: the
squash writes one new commit sharing no ancestry with the commits it
flattened, so `prod` reads as permanently ahead-and-behind even when the
content is identical. That is what #220 did.

`promote-reconcile.yml` repairs it. Run it from the Actions tab with
`dry_run` on first; it builds the reconciling merge, proves the resulting
tree is byte-identical to `dev`, and reports without pushing. Re-run with
`dry_run` off to open the PR.

**The direction is the whole point, and it is easy to get backwards.** The
gate needs `prod` to be an ancestor of `dev`, so the reconcile merges `prod`
**into `dev`** and keeps `dev`'s tree — it lands as a PR against `dev` that
changes no files. Merging `dev` into `prod` instead produces a `prod` whose
tree also matches, which looks like it worked and is not: it makes `prod` a
*descendant* of `dev`, no future `dev` commit is a descendant of `prod`, and
the gate refuses exactly as before while the reconcile no longer reads as a
no-op either. `tests/test-reconcile-direction.sh` asserts both halves of
that, because the backwards version shipped once.

It is a separate, manual workflow on purpose. Reconciling discards `prod`'s
side of the history, and that should be something someone decides, not
something the hourly gate does quietly the next time `prod` looks wrong.

Merge the reconcile PR with a **merge commit**. Squashing it would recreate
the divergence it repairs.

### Recovering a tag that was never published

A tag can exist with no release behind it — v0.2.0 and v0.3.0 both did,
because `release-cut.yml` pushed them with `GITHUB_TOKEN` and GitHub does
not start workflow runs from `GITHUB_TOKEN`-authored events. Dispatch
`release-publish.yml` and give it the tag.

That is also why `promote.yml` *calls* `release-publish.yml` rather than
pushing a tag and hoping: the call does not depend on who pushed.

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
