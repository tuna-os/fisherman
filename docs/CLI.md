# Command-line reference

This page lists every fisherman command with its flags, their values and
defaults, and the exit codes. `TestCLIReferenceCoversEveryFlag`
(`fisherman/cmd/fisherman/cli_doc_test.go`) reads the argument parsers in
`fisherman/cmd/fisherman/` and fails when a command or a flag has no row
here, or a row names one that the code does not have.

- A flag that takes a value accepts `--flag value` and `--flag=value`.
- `-h`, `--help` and `help` after a command print its usage on stdout and
  exit 0. (`images` uses Go's `flag` package: `-h` and `--help` work there,
  and its usage goes to stderr.)
- fisherman has no short flags and no configuration file.

## Commands

| Command | Root | What it does | Contract |
|---|---|---|---|
| `fisherman <recipe.json>` | yes | Install from a recipe file. **This erases the disk.** | [JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md) |
| `fisherman -` | yes | Install from a recipe read on stdin to EOF (at most 1 MiB). | [JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md) |
| `fisherman validate` | no | Check a recipe without an install. | [VALIDATE.md](VALIDATE.md) |
| `fisherman probe` | no | Print the facts of this machine that a frontend needs. | [PROBE.md](PROBE.md) |
| `fisherman verify` | for `--live` | On the installed system: compare the installation receipt with the booted image. | [RECEIPT.md](RECEIPT.md) |
| `fisherman diagnostics` | no | Write a local support bundle of allowlisted facts about an install. Never uploaded. | [DIAGNOSTICS.md](DIAGNOSTICS.md) |
| `fisherman images` | no | List or search the image catalog. | [README](../README.md#image-catalog) |
| `fisherman scan` | yes | Count the Windows user data on a disk that a migration could take. | [SCAN.md](SCAN.md) |
| `fisherman version` | no | Print `fisherman <version>`. `--version` is the same. | |
| `fisherman help` | no | Print the help. `--help` and `-h` are the same. | |

With no argument, fisherman prints the help and exits 1. An argument that is
a flag, or a bare word with no file of that name, is not a recipe path:
fisherman says `unknown command` and exits 2. The recipe fields are in
[RECIPE.md](RECIPE.md).

## `fisherman <recipe.json>` and `fisherman -`

The install has no flags. It needs root, and refuses a run that is not root
(`not_root`) before it reads the recipe. Give the recipe on stdin: it can
hold the LUKS passphrase and the user password.

```sh
generate-recipe | sudo fisherman -
```

| Exit | When |
|---|---|
| 0 | The install succeeded. The last event is `complete`. |
| 1 | The install failed. The last event is `error`. |
| 2 | The argument is not a recipe path (see above). No event. |
| 130 | The install was cancelled: SIGTERM, SIGINT or SIGHUP, the death of the parent process, or a closed stdout. The last event is `error` with the code `cancelled`. |

[JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md#exit-codes) has the
details and the cases with no event (a panic, SIGKILL).

## `fisherman validate`

```text
fisherman validate <recipe.json | ->
fisherman validate --json [--partial] <recipe.json | ->
```

| Flag | Value | Default | What it does |
|---|---|---|---|
| `--json` | none | a human summary, or the first problem on stderr | Print every problem as one JSON object on stdout. |
| `--partial` | none | the full check | With `--json` only: check only the fields that are present, and read nothing from the machine. For one page of a frontend. |

The recipe is a path or `-` for stdin; exactly one is required.

| Exit | Without `--json` | With `--json` |
|---|---|---|
| 0 | valid | valid |
| 1 | invalid, unreadable, or no recipe argument | invalid, unreadable or not JSON (reported as a problem) |
| 2 | an unknown flag (`--partial` without `--json` is one) | no recipe, more than one, or an unknown flag |

## `fisherman probe`

```text
fisherman probe --json [--source <ref> | --live-root] [--target-imgref <ref>] [--bootc-image-list]
```

| Flag | Value | Default | What it does |
|---|---|---|---|
| `--json` | none | required | Print one JSON object on stdout. It is the only output format. |
| `--source` | an image reference | no source | The recipe's `image`. `capabilities` is then about that source. |
| `--live-root` | none | off | The recipe has no `image`: the live root is the source. Excludes `--source`. |
| `--target-imgref` | an image reference | none | The recipe's `targetImgref`, the update reference. |
| `--bootc-image-list` | none | off | Also run `bootc image list` for `bootc_store`. Only as root on an installed ostree host. This one is not read-only: bootc opens its own store. |

Without `--bootc-image-list` the probe writes nothing, and it never pulls,
mounts, starts a container or contacts a registry.

| Exit | When |
|---|---|
| 0 | The JSON is on stdout. |
| 1 | The probe or the encoding failed. |
| 2 | No `--json`, an unknown argument, a flag with no value, a reference that is not valid, or `--source` with `--live-root`. |

## `fisherman verify`

```text
fisherman verify --json [--root <dir>] [--live | --collected] [--receipt <file>] [--collection <dir>] [--write]
```

| Flag | Value | Default | What it does |
|---|---|---|---|
| `--json` | none | required | Print one JSON result on stdout. It is the only output format. |
| `--root` | a directory | `/` | The installed system's root. |
| `--live` | none | when `--root` is `/` and there is no `--collection` | Run `bootc status` now. Only with `--root /` and no `--collection`. Excludes `--collected`. |
| `--collected` | none | when `--root` is not `/` or `--collection` is given | Read what the first-boot unit collected. |
| `--receipt` | a file | `<root>/var/lib/fisherman/install-receipt.json` | The installation receipt. |
| `--collection` | a directory | `<root>/var/lib/fisherman/boot-receipt` | The first-boot collection. |
| `--write` | none | off | Also store the result as `<collection>/verification.json`. |

verify only reads (except `--write`): it never switches an image, changes
the boot order, rolls back or reboots.

| Exit | When |
|---|---|
| 0 | The outcome is `verified`. |
| 1 | Any other outcome, or the encoding or `--write` failed. |
| 2 | No `--json`, an unknown argument, a flag with no value, `--live` with `--collected`, or `--live` with another root or a collection. |

## `fisherman diagnostics`

```text
fisherman diagnostics (--output-dir <dir> | --stdout) [--recipe <file|->] [--progress-log <file|->] [--probe | --probe-report <file|->] [--include-identifiers]
```

| Flag | Value | Default | What it does |
|---|---|---|---|
| `--output-dir` | a directory | none | Write the bundle to a new file there (mode 0600, a unique name) and print its path. Excludes `--stdout`; one of the two is required. |
| `--stdout` | none | off | Print the bundle on stdout instead, to review it. |
| `--recipe` | a file, or `-` for stdin | none | The recipe of the install. Read for its effective options, never copied. |
| `--progress-log` | a file, or `-` for stdin | none | The install's saved stdout. The last 32 MiB are read. |
| `--probe` | none | off | Run the read-only probe now; the export gives up after 60 seconds. Excludes `--probe-report`. |
| `--probe-report` | a file, or `-` for stdin | none | A saved `fisherman probe --json` report. |
| `--include-identifiers` | none | off | Keep host names, user names and device paths instead of tokens. |

At most one input reads stdin. The export only reads: it does not mount,
unlock, retry or clean up anything, and an install never runs it. An input
that cannot be read is a section with the status `unreadable`, not a
failure.

| Exit | When |
|---|---|
| 0 | The bundle is written (its path on stdout) or printed. |
| 1 | The bundle cannot be written: the output directory is absent, not a directory or a symlink, or the write failed (no partial file is left). |
| 2 | Neither or both of `--output-dir` and `--stdout`, an unknown argument, a flag with no value, `--probe` with `--probe-report`, or two inputs on stdin. |

## `fisherman images`

```text
fisherman images [--file <path>] [--plain] [<query>]
```

| Flag | Value | Default | What it does |
|---|---|---|---|
| `--file` | a path | the catalog search path | Read this `images.json`. |
| `--plain` | none | off | Plain text, with no ANSI color and no tree characters. |

Without a query, `images` prints the whole catalog as a tree. With one (all
the words after the flags), it shows the images and families that match.

| Exit | When |
|---|---|
| 0 | The catalog or the matches are on stdout. |
| 1 | No catalog was found or read, or nothing matches the query. |
| 2 | An unknown flag. |

## `fisherman scan`

```text
fisherman scan <disk>
```

No flags. It needs root. It prints one JSON object (see
[SCAN.md](SCAN.md)) and exits 0, or exits 1 with no disk argument or when
the scan fails.

## Environment

| Variable | Read by | What it does |
|---|---|---|
| `FISHERMAN_IMAGES_PATH` | `images`; the install, for `extras` | The first place on the catalog search path ([README](../README.md#image-catalog)). For `extras` the install reads `/etc/bootc-installer/images.json` and `/etc/tuna-installer/images.json` before it. |
| `CONTAINERS_STORAGE_CONF` | the install | When it is set, the install mounts that file into the bootc container as its `storage.conf`, and writes no `storage.conf` of its own for `additionalImageStores`. |
