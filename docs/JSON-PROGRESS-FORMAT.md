# JSON Progress Event Format

fisherman reports install progress as newline-delimited JSON on stdout. This
document describes that format and fisherman's exit codes for frontends and
other consumers.

The emitter is `fisherman/internal/progress/progress.go`. The callers are
`fisherman/cmd/fisherman/main.go` (steps, errors, completion) and the packages
under `fisherman/internal/` (substeps and info lines).

Reference consumer: bootc-installer's
[`shared/progress/README.md`](https://github.com/tuna-os/bootc-installer/blob/dev/shared/progress/README.md)
and
[`shared/progress/progress_parser.py`](https://github.com/tuna-os/bootc-installer/blob/dev/shared/progress/progress_parser.py).
All bootc-installer frontends parse the stream the way that parser does.

## Overview

Each event is one JSON object on one line. Read the stream line by line. Do
not try to parse the whole output as one JSON document.

**stdout also carries lines that are not JSON.** fisherman echoes the commands
it runs (`+ sfdisk ...`, `+ podman ...`), relays raw bootc, podman and tar
output, and prints short notes such as `  wrote hostname ...`. A consumer must
skip every line that does not parse as a JSON object. The reference parser
skips any line that does not start with `{`.

**Common fields (present in every event):**
- `type` — event type (string)
- `timestamp` — time the event was written, RFC 3339 with nanoseconds, UTC (string)
- `elapsed_ms` — milliseconds since the fisherman process started (integer)

**Key order is not guaranteed.** `write()` marshals each event, decodes it
into a map, adds `timestamp` and `elapsed_ms`, and marshals the map again.
Today that yields alphabetical keys, but that is a side effect of Go's map
encoding. Do not rely on it.

## Event Types

### `step`

A pipeline step has started.

**Fields:**
- `type` — `"step"`
- `step` — number of this step, starting at 1 (integer)
- `total_steps` — number of steps this install will emit (integer, 5–11; see [Step count](#step-count))
- `step_name` — fisherman's English name for the step (string; see [Pipeline](#pipeline))
- `weight_pct` — this step's share of the total install time, in percent (integer)
- `cumulative_pct` — progress bar position at the start of this step, in percent (integer, at most 99)
- `step_id` — stable id for the step (string; see [Pipeline](#pipeline)). Omitted for a step name that has no id.
- `overall_pct` — the progress bar after this event, 0–100 with two decimals (number; see [Progress bar](#progress-bar))
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"cumulative_pct":0,"elapsed_ms":2310,"overall_pct":0,"step":1,"step_id":"partition","step_name":"Partitioning disk","timestamp":"2026-10-07T19:05:30.123456Z","total_steps":8,"type":"step","weight_pct":0}
{"cumulative_pct":1,"elapsed_ms":9874,"overall_pct":1,"step":5,"step_id":"install_os","step_name":"Installing OS","timestamp":"2026-10-07T19:05:37.687123Z","total_steps":8,"type":"step","weight_pct":87}
```

**Usage:** Set the progress bar to `overall_pct`. Label the step by `step_id`.
See [Progress bar](#progress-bar).

### `substep`

Progress inside the current step.

**Fields:**
- `type` — `"substep"`
- `message` — status message (string)
- `overall_pct` — the progress bar after this event, 0–100 with two decimals (number). A message that does not move the bar repeats the previous value.
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"elapsed_ms":61210,"message":"Pulling image: layer 23/71","overall_pct":17.91,"timestamp":"2026-10-07T19:06:29.023456Z","type":"substep"}
```

Messages you will see include:

- `Pulling container image`
- `Pulling image: 71 layers to download`
- `Pulling image: layer 23/71` (one per finished layer; `Pulling image: layer 23` when the total is unknown)
- `Pulling image: copying config`, `Pulling image: writing manifest`, `Image pulled successfully`
- `Image already up to date, skipping pull`
- `Exporting image to OCI layout for composefs install`, `OCI export complete`
- `Deploying image`, `Initializing ostree layout`, `Installing bootloader`, `bootc installation complete` (classified from bootc's own output by `ClassifyLine` in `internal/install/bootc.go`)
- `Copying Flatpak data: 45%`

`Pulling image: layer N/M` is the one that matters for the bar: the pull is
most of a cold install.

### `info`

An informational line. Many `info` messages start with `Warning:`. These
report non-fatal problems; the install continues.

**Fields:**
- `type` — `"info"`
- `message` — message (string)
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"elapsed_ms":412,"message":"Checking image cache...","timestamp":"2026-10-07T19:05:28.225558Z","type":"info"}
```

Other examples: `Image pull required (71 layers)`,
`Source image ghcr.io/example/os:stable: manifest sha256:…, platform manifest sha256:… (linux/amd64); …`,
`Image found in local cache, no pull needed`,
`Offline: registry unreachable, using locally cached image`,
`Writing hostname: <hostname>`,
`EFI boot entry for installed system: Boot0001`.

Several `info` events can arrive before the first `step` event. The image
cache check, a `sourceReuse` copy (with its [`source_reuse`](#source_reuse)
events), live-session audio setup and Windows data migration all run
before partitioning.

### `source_reuse`

Progress of a `sourceReuse` acquisition ([SOURCE-REUSE.md](SOURCE-REUSE.md),
#384): the copy of an image the live host's bootc keeps into
containers-storage, before the first `step`. Only a recipe with
`sourceReuse` produces these.

**Fields:**
- `type` — `"source_reuse"`
- `version` — `1`; bumped only when a field is removed or renamed or its meaning changes
- `phase` — `planned`, `copying`, `verified`, `failed` or `released` (string)
- `digest` — the item's manifest digest, as the recipe names it (string)
- `platform` — the item's architecture, bootc's spelling (string)
- `source` — the reference given to `bootc image copy-to-storage --source` (string, credential-free; from `planned` on)
- `destination` — `containers-storage:localhost/fisherman-reuse:<tag>`, the copy fisherman created (string; from `planned` on)
- `destination_digest` — the copy's manifest digest (`verified`). bootc re-serialises the layers, so it is not `digest`; the installed system boots this one
- `estimate` — `planned` (and `failed` after the estimate): `scratch_bytes`, `store_bytes`, `required_bytes` (with the 1 GiB margin), `scratch_free_bytes`, `store_free_bytes`, `same_filesystem`, `files`, `estimate_ms`
- `measured` — `verified`: `copy_ms`, `scratch_peak_bytes` and `store_peak_bytes` (the largest drop in free space sampled during the copy), `store_bytes` (what the copy still holds), `verify_ms`
- `code`, `reason` — `failed`: the `source_reuse_*` code of the `error` event that follows, and a finer reason (`ref_moved`, `insufficient_space`, `disk_exhausted`, …); `released`: `reason` is `removed` or `remove_failed`
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"destination":"containers-storage:localhost/fisherman-reuse:111111111111-0123456789abcdef","digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","elapsed_ms":61210,"estimate":{"estimate_ms":4210,"files":51234,"required_bytes":8053063680,"same_filesystem":true,"scratch_bytes":3221225472,"scratch_free_bytes":60129542144,"store_bytes":3758096384,"store_free_bytes":60129542144},"phase":"planned","platform":"x86_64","source":"docker://ghcr.io/example/os:stable","timestamp":"2026-11-11T10:05:30.123456Z","type":"source_reuse","version":1}
```

`released` comes from the teardown, after `complete` is decided and before
it is written on success, or before the `error` event on a failure or
cancel. A frontend may ignore the whole event type; the `info` lines say the
same in English.

### `recovery_key`

The LUKS recovery passphrase for a `tpm2-luks` install.

**Fields:**
- `type` — `"recovery_key"`
- `key` — the passphrase (string)
- `timestamp`, `elapsed_ms` — as above

The key is 64 lowercase hexadecimal characters: 32 bytes from `crypto/rand`,
hex-encoded by `luks.RandomPassphrase()`.

**Example:**
```json
{"elapsed_ms":402117,"key":"3f9c0a7e5b1d2c4e6f8a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e","timestamp":"2026-10-07T19:12:10.240117Z","type":"recovery_key"}
```

**When it is emitted:** only when the recipe's encryption type is
`tpm2-luks`. For that type fisherman generates the passphrase itself, so the
user has no other way to unlock the disk if TPM2 unlock fails. It is not
emitted for `luks-passphrase` or `tpm2-luks-passphrase`, where the user chose
the passphrase.

**Where it appears:** at most once, inside the `Enrolling TPM2 auto-unlock`
step, after the `info` line that reports the first-boot enrolment, and before
the `Copying system Flatpaks` step.

**Usage:** Keep the key and show it to the user before they reboot.
fisherman does not pause after this event. The install keeps running, so the
frontend must hold the key and present it itself, for example on the
completion screen.

### `receipt`

What the install did with the installation receipt
([RECEIPT.md](RECEIPT.md#the-receipt-progress-event)). Informational: the
install goes on whatever it says.

**Fields:**
- `type` — `"receipt"`
- `version` — `1` (integer). Fields and reason codes are only added; a new
  version means a field changed meaning.
- `written` — the receipt file is on the installed system (boolean)
- `install_id` — the receipt's install id, 32 hex characters (string).
  Present only when `written`.
- `path` — `/var/lib/fisherman/install-receipt.json`, as the installed
  system sees it (string). Present only when `written`.
- `first_boot_check` — the first-boot collector is staged (boolean)
- `reason` — a stable code, present unless the receipt is written and the
  collector staged: `resolve_failed`, `build_failed`, `write_failed`
  (`written` false), `transient_etc`, `collector_failed` (`written` true,
  `first_boot_check` false). See
  [RECEIPT.md](RECEIPT.md#the-receipt-progress-event).
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"elapsed_ms":498120,"first_boot_check":true,"install_id":"0123456789abcdef0123456789abcdef","path":"/var/lib/fisherman/install-receipt.json","timestamp":"2026-11-11T19:13:46.130120Z","type":"receipt","version":1,"written":true}
{"elapsed_ms":498120,"first_boot_check":false,"reason":"write_failed","timestamp":"2026-11-11T19:13:46.130120Z","type":"receipt","version":1,"written":false}
```

**Where it appears:** once, inside the `Configuring installed system` step,
right after the `info` line that says the same in English, and before the
`Finalizing installation` step. A failed install that ends before that step
has none, and a fisherman older than this event never sends it.

**Usage:** Key the done page's copy on `written`, `first_boot_check` and
`reason`, never on the `info` message. `written` means "installed, not yet
verified": it is no claim that the target booted. That is `fisherman verify
--json`, run on the installed system. One case per outcome is in
`fisherman/internal/progress/testdata/receipt-events.ndjson`.

### `complete`

The install finished. fisherman has already turned off any swap it activated on the target, unmounted the target and closed
LUKS. It exits 0 right after this event.

**Fields:**
- `type` — `"complete"`
- `message` — always `"Installation complete!"` (string)
- `boot_id` — EFI boot entry number of the installed system, 4 hex digits (string, e.g. `"0001"`). Omitted when fisherman could not determine it.
- `overall_pct` — always `100` (number)
- `timestamp`, `elapsed_ms` — as above

**Example (with boot ID):**
```json
{"boot_id":"0001","elapsed_ms":512345,"message":"Installation complete!","overall_pct":100,"timestamp":"2026-10-07T19:14:00.358345Z","type":"complete"}
```

**Example (without boot ID):**
```json
{"elapsed_ms":512345,"message":"Installation complete!","overall_pct":100,"timestamp":"2026-10-07T19:14:00.358345Z","type":"complete"}
```

**Usage:** Set the bar to 100% and mark the install successful. A frontend
can use `boot_id` to set `BootNext` before rebooting.

fisherman also sets `BootNext` itself, while it is root, when the host booted
from live media or the recipe has `"setBootNext": true`. The `info` line
before `complete` says what happened: `BootNext set to Boot0001: …`,
`BootNext not set: …` with the reason, or `Warning: could not set BootNext
…`. A frontend that runs as the desktop user cannot write the variable, and
need not try when the line says it is set.

### `error`

The install failed.

**Fields:**
- `type` — `"error"`
- `message` — what failed (string), for people. At most 2048 bytes.
- `code` — the stable code of the failure. For a recipe rule or a preflight
  check it is the code `fisherman validate --json` reports for the same
  problem ([VALIDATE.md](VALIDATE.md#error-code-reference)). For any other
  failure it is one of the [failure codes](#failure-codes) below, which name
  the operation that failed; `unknown` when there is none more precise. Key
  copy on this, never on `message`. An older fisherman sent `code` only for
  a validate problem.
- `field` — optional. The recipe field the `code` is about (`hostname`,
  `user.username`, `encrypton`). Absent when the code is not about one field.
- `step_id` — optional. The `step_id` of the last `step` event: the step that
  was running. Absent before the first step.
- `disk_changed` — `true` once a disk step has started: the disk may be
  partly changed, and the install is not safe to retry as if nothing
  happened. `false`: fisherman changed no disk.
- `cleanup` — what the teardown before this event did:
  - `status` — `ok` (everything the install held is released), `failed` (a
    part failed: the target may still be mounted or unlocked) or `timed_out`
    (the teardown did not end within its bound).
  - `message` — optional, the teardown's error.

  A cleanup failure never replaces `code`: the code stays the one of the
  failure that ended the install. The message still ends with the cleanup
  failure, as before.
- `timestamp`, `elapsed_ms` — as above

No field holds the recipe, a command line, the LUKS passphrase, the user
password or the recovery key: fisherman replaces each of these three secrets
with `[redacted]` wherever it occurs in `message`, `field` or
`cleanup.message`, and in the stderr line. A secret shorter than 4 bytes is
not replaced: it would match ordinary words.

**Example:**
```json
{"cleanup":{"status":"ok"},"code":"partition_failed","disk_changed":true,"elapsed_ms":45123,"message":"partitioning disk: exit status 1","step_id":"partition","timestamp":"2026-10-07T19:06:15.234567Z","type":"error"}
```

#### Failure codes

These codes name the operation that failed, not a cause: a pull failure is
not always the network, a `cryptsetup` failure is not always a bad
passphrase, an `sfdisk` failure is not always a busy disk. The `message`
holds the tool's own reason. A code is never renamed or reused; new codes
can appear. A consumer that does not know a code, or gets no code from an
older fisherman, shows `message` and decides on the exit status.

| Code | The operation that failed |
|---|---|
| `unknown` | none more precise |
| `cancelled` | none: the install was cancelled (exit 130) |
| `lock_failed` | taking the host-wide install lock (another install holding it is `install_already_running`) |
| `local_source_unavailable` | reading the required local source: the live media's image, or an `oci:` or `containers-storage:` source |
| `manual_layout_failed` | formatting or mounting a partition of a manual layout |
| `partition_failed` | partitioning the disk |
| `format_failed` | making a filesystem: the ESP, `/boot`, the root or the `/var` disk |
| `luks_failed` | formatting or opening the LUKS container, or reading its UUID |
| `zfs_failed` | creating or mounting the ZFS pool or its root dataset |
| `mount_failed` | creating a mount point, mounting a filesystem, or the btrfs subvolumes |
| `scratch_failed` | preparing the scratch directory |
| `pull_failed` | pulling the image, after its retries |
| `bootc_install_failed` | `bootc install`, for any reason but the pull |
| `retag_failed` | the GPT retag of the root partition and the remount after it |
| `persistent_mounts_failed` | writing the mounts of the installed system |
| `hostname_failed` | writing the hostname |
| `user_failed` | creating the user |
| `luks_boot_args_failed` | writing the LUKS kernel arguments |
| `finalize_failed` | finalizing the target filesystem |

A code tells which part failed. It does not say that another attempt is safe:
read `disk_changed`.

Two paths emit this event: `fatal()` in `cmd/fisherman/main.go` (any
failure, exit 1) and a cancel (exit 130). Both go through the same teardown in
`cmd/fisherman/terminate.go`: stop child processes, run cleanup (unmounts,
close LUKS), then write the event, then write `fisherman: fatal: <message>`
(or `fisherman: cancelled: <message>`) to stderr and exit. Cleanup runs before
the event so that, if it fails, the message can say so: it then ends with
`; cleanup failed, the target may still be mounted or unlocked: …`.

The message is a short context prefix followed by the underlying error, for
example `loading recipe: ...`, `invalid recipe: ...`,
`missing required host tool: ...`, `LUKS format: ...` or `bootc install: ...`.

A recipe problem:

```json
{"cleanup":{"status":"ok"},"code":"recipe_unknown_field","disk_changed":false,"elapsed_ms":2,"field":"encrypton","message":"loading recipe: unknown recipe field \"encrypton\": this fisherman does not know it, and would ignore it","timestamp":"2026-11-10T14:32:10.428842Z","type":"error"}
```

`error` can be the only event in the stream. Recipe loading, recipe
validation and the host tool check all run before the first step. With the
recipe on stdin (`fisherman -`), empty stdin is `loading recipe: recipe is
empty` (`code` `recipe_malformed`, as `validate --json` says; a recipe file
that cannot be read is `recipe_unreadable`). An empty `image` on a host that did not boot from live media is
`invalid recipe: image is required: …` (`code` `image_required`). A recipe
key fisherman does not know is `loading recipe: unknown recipe field …`
(`code` `recipe_unknown_field`). A run that is not root is `cannot install:
fisherman must run as root …` (`code` `not_root`); it is refused before the
recipe is read. A second install on a host where one is running is `cannot
install: another fisherman install is running on this host (process N); …`
(`code` `install_already_running`); it too is refused before the recipe is
read, and it touches nothing the running install owns. A disk that is not an eligible target is `invalid recipe:
disk … ` with one of the `disk_*` codes
([VALIDATE.md](VALIDATE.md#target-eligibility)). A missing host program is
`missing required host tool: "mkfs.xfs" not found on the host; install the
"xfsprogs" package` (`code` `host_tool_missing`). An image the registry does
not give, with no local copy, or one that is not a bootc image for this host,
is `image source: …` with one of the `image_*` codes and `field` `image`
([VALIDATE.md](VALIDATE.md#install-only)). A SELinux label workaround that
the install needs and cannot build is `cannot prepare the install: …`
(`code` `selinux_workaround_unavailable`). All of these come before the
first `step` event: the disk is not changed.

`error` is **not** emitted when fisherman exits 2 (unknown command) or
panics, or when it is killed with SIGKILL. See [Exit codes](#exit-codes).

**Usage:** Stop and report the failure. No further events follow.

## Pipeline

The steps depend on the recipe. They run in this order:

| `step_name` | `step_id` | When |
|---|---|---|
| `Preparing disk` | `prepare_disk` | Manual layout only (`customMounts` set). Replaces partitioning, EFI, root and mounting (4 steps) with one: formats and mounts the user's partitions. |
| `Partitioning disk` | `partition` | Auto layout. Writes a 2-partition GPT (systemd-boot, ZFS) or a 3-partition GPT (GRUB2: EFI, ext4 `/boot`, root). |
| `Formatting EFI partition` | `format_efi` | Auto layout. Formats the FAT32 ESP and, on GRUB2 layouts, the ext4 `/boot` partition. There is no separate `/boot` step. |
| `Setting up disk encryption` | `luks` | Auto layout with encryption. LUKS format and open of the root partition. |
| `Formatting root filesystem` | `format_root` | Auto layout. XFS, Btrfs, ext4 or a ZFS pool. |
| `Mounting filesystem` | `mount` | Auto layout. Mounts root, `/boot` and the ESP at the target. |
| `Formatting data disk (/var)` | `format_var` | A separate `/var` disk is set and `keepExisting` is false. |
| `Installing OS` | `install_os` | Always. Image pull, `bootc install to-filesystem`, bootloader. |
| `Enrolling TPM2 auto-unlock` | `tpm2_enroll` | `tpm2-luks` or `tpm2-luks-passphrase`. Stages TPM2 enrolment for first boot. |
| `Copying system Flatpaks` | `flatpaks` | Always. It copies nothing when the recipe's `flatpaks` is `[]`, or when the host has no system Flatpaks; an `info` line says so. |
| `Configuring installed system` | `configure` | Always. Hostname, user, kernel arguments, network and Bluetooth copy (live media, or on request). |
| `Finalizing installation` | `finalize` | Always. fstrim, remount read-only, fsfreeze/thaw (skipped on ZFS). |

Step numbers are assigned in order to the steps that run, so `step` has no
fixed meaning. Match on `step_id`, and fall back to showing `step_name` for an
id you do not know or an event without one.

`step_id` is a public contract: frontends key their translated, rebrandable
step labels on it. An id is never renamed or reused. A renamed step keeps its
old id and a new step gets a new one. `step_name` stays fisherman's English
name and may change. The table is `stepIDs` in
`internal/progress/bar.go`.

Manual layouts reject encryption (`recipe.Validate`), so a manual install
never has the encryption or TPM2 steps.

### Step count

`total_steps` is computed before the first step. It is the length of the
weight table (`len(buildProfile(...))`), so it always matches the steps that
are emitted:

- start at 8
- minus 3 for a manual layout
- plus 1 for encryption (auto layout only)
- plus 1 for `tpm2-luks` or `tpm2-luks-passphrase` (auto layout only)
- plus 1 when a `/var` disk is formatted

The range is 5 (manual, no `/var` format) to 11 (encryption, TPM2 and a
`/var` format).

## Progress bar

**Set the bar to `overall_pct`.** fisherman computes it on every `step`,
`substep` and `complete` event (`Tracker` in `internal/progress/bar.go`), so a
frontend only renders it. It:

- never decreases;
- stays at or below 99 until `complete`, which alone reports 100;
- starts each step at that step's `cumulative_pct`, or holds where it is if
  the bar is already past it;
- moves inside a step only on the substep messages below. Any other message,
  and any message in a 0% step, repeats the previous value.

Inside a step:

- `Pulling image: layer N/M` fills the first 60% of the step.
- The bootc phases after the pull sit at fixed points in what is left of the
  step: `Exporting image to OCI layout` 5%, `OCI export complete` 30%,
  `Using …` 32%, `Initializing ostree layout`, `Writing …` and
  `Deploying image` 35%, `OS deployed, installing bootloader` 90%,
  `Detected bootloader` and `Installing bootloader` 92%,
  `Configuring EFI boot entry`, `Configuring GRUB`, `Configuring SELinux` and
  `Generating initramfs` 95%, `bootc installation complete` 100%. On an
  offline install nothing is pulled, so these span the whole step.
- `Copying Flatpak data: N%` fills the whole `Copying system Flatpaks` step.

The rest of this section describes the inputs to that calculation. Before
`overall_pct` existed, every frontend repeated it; a consumer that must also
read an older fisherman can fall back to it.

**Do not derive the bar from `step / total_steps`.** The step count changes
with the recipe, and the steps are very unequal: most of them carry 0%.

The weights come from `buildProfile` in `cmd/fisherman/main.go`. They were
measured on a loop-device install.

| `step_name` | `weight_pct`, image pulled | `weight_pct`, image cached |
|---|---|---|
| `Preparing disk` (manual layout) | 1 | 1 |
| `Partitioning disk` | 0 | 0 |
| `Formatting EFI partition` | 1 | 1 |
| `Setting up disk encryption` | 1 | 1 |
| `Formatting root filesystem` | 0 | 0 |
| `Mounting filesystem` | 0 | 0 |
| `Formatting data disk (/var)` | 0 | 0 |
| `Installing OS` | 87 | 68 |
| `Enrolling TPM2 auto-unlock` | 1 | 1 |
| `Copying system Flatpaks` | 11 | 29 |
| `Configuring installed system` | 0 | 0 |
| `Finalizing installation` | rest (1) | rest (2) |

`Installing OS` loses 1 point for encryption and 1 for TPM2, to pay for those
steps. `Finalizing installation` takes whatever is left so the weights sum to
100. "Cached" means the image check found nothing to pull, or the recipe had
no image.

`cumulative_pct` is the sum of the weights of the steps before this one. It
reaches at most 99, on the last step. Only `complete` means 100%.

Without `overall_pct`, interpolate inside a step from substeps. For the image
pull:

```
fraction = (cumulative_pct + (done / total) * weight_pct) / 100
```

where `done/total` comes from `Pulling image: layer done/total`. The reference
parser gives the pull the first 60% of `Installing OS` and places the later
bootc phases at fixed points in the rest.

A manual layout (`Preparing disk`) has its own weight table: `Preparing disk`
carries the combined weight of the four steps it replaces (1), so
`Installing OS` arrives with `cumulative_pct` 1 and `weight_pct` 87 (68 when
cached). The weights still sum to 100 and `cumulative_pct` still ends at 99 or
below. Before fisherman #266, a manual layout reused the auto-layout table
without re-indexing it, so its steps carried the wrong weights.

## Exit codes

These are the `os.Exit` paths in `cmd/fisherman/main.go` and what the stream
looks like for each.

| Exit | When | Events on stdout |
|---|---|---|
| 0 | Install succeeded. | Ends with `complete`. |
| 0 | `help`, `version`, `images`, `validate` or `scan` succeeded. | None (plain-text output). |
| 1 | `fatal()`: any install failure, including an unreadable or invalid recipe and a missing host tool. | Ends with `error`. |
| 1 | No arguments. Help is printed to stdout. | None. |
| 1 | `scan` without a disk, or `scan` failed. Message on stderr. | None. |
| 130 | Cancelled: SIGTERM, SIGINT or SIGHUP, or the parent process (the frontend's wrapper) died. Children are stopped and the target is torn down first; see [Cancelling an install](#cancelling-an-install). | Ends with `error`: `installation cancelled (SIGTERM)` (the signal name varies). |
| 130 | Cancelled: the reader of stdout went away (a closed pipe). Children are stopped and the target is torn down first; see [A closed stdout](#a-closed-stdout). | The `error` event `installation cancelled (stdout closed)` is written, but nobody can read it. The same line is on stderr. |
| 2 | The argument looks like a command, not a recipe path (a flag, or a bare word with no file behind it). A lone `-` is not one: it reads the recipe from stdin. Message and help on stderr and stdout. | None. |

The `images` and `validate` subcommands (in `images.go` and `validate.go`)
also exit 1 on failure. They do not emit progress events.

`validate --json` (see [VALIDATE.md](VALIDATE.md)) exits 0 for a valid
recipe, 1 for an invalid one (its problems are the JSON on stdout), and 2 for
bad arguments. The install runs the same rules first, so an invalid recipe
ends with an `error` event (`invalid recipe: …`) before any disk step.

`probe --json` (see [PROBE.md](PROBE.md)) exits 0 with its JSON on stdout, 1
if the probe or the encoding failed, and 2 for a missing `--json` or an
unknown argument. It does not emit progress events either.

Other ways the process can end:

- **Panic.** A Go panic exits 2 and prints a stack trace to stderr. No
  `error` event is written and cleanup does not run, so mounts and the LUKS
  mapper can stay open. One explicit panic exists:
  `luks.RandomPassphrase()` panics if `crypto/rand` fails.
- **Signals.** SIGTERM, SIGINT and SIGHUP cancel the install and exit 130;
  see [Cancelling an install](#cancelling-an-install). SIGKILL cannot be
  handled: no `error` event, no cleanup.
- **A closed pipe.** A closed stdout cancels the install with teardown and
  exits 130; see [A closed stdout](#a-closed-stdout). It is no longer one of
  the exits without cleanup. A closed stderr does not stop the install.
- **pkexec.** Frontends run fisherman through `pkexec`. pkexec returns
  fisherman's exit code, but exits 126 when the user dismisses the
  authentication dialog and 127 when authorization fails. fisherman never
  starts in those cases.

If the stream ends without `complete` or `error`, treat the install as
failed. Use the exit code to tell the cases apart.

## Cancelling an install

A frontend runs fisherman as root through `pkexec`, so it cannot signal
fisherman itself: `kill(2)` returns `EPERM`. Instead, fisherman asks the
kernel (`PR_SET_PDEATHSIG`) to send it SIGTERM when its parent dies. To
cancel, kill the process you spawned. Every frontend uses the same wrapper:

```sh
bash -c 'pkexec /usr/local/bin/fisherman "$1"; exit $?' -- /path/to/recipe.json
# or, piping the recipe on stdin:
bash -c 'pkexec /usr/local/bin/fisherman -; exit $?'
```

Spawn it in its own process group (under Flatpak, prefix it with
`flatpak-spawn --host`) and kill that group to cancel. Do not kill your own
group. bash is fisherman's parent, because pkexec execs fisherman in place,
so its death reaches fisherman. A terminal run (`sudo fisherman recipe.json`)
behaves the same way when sudo or its shell dies.

On a cancel, fisherman:

1. stops its children with SIGTERM, then SIGKILL after 10 seconds;
2. thaws the target if the finalize step froze it (`fsfreeze -u`; a frozen
   filesystem cannot be unmounted cleanly), turns off the swap a manual
   layout activated, unmounts the target and closes the LUKS mapping, for at
   most 3 minutes;
3. writes one `error` event, `installation cancelled (<SIGNAL>)`, with
   `; cleanup failed, the target may still be mounted or unlocked: …` appended
   if teardown did not finish;
4. exits 130.

### A closed stdout

The reader of the event stream can go away while fisherman's parent is still
alive: the frontend crashes behind its wrapper shell, or
`sudo fisherman recipe.json | tee log` loses its `tee`. The parent-death
signal does not fire, because the parent did not die.

fisherman used to die of SIGPIPE on its next event, with no teardown: the
target stayed mounted, the LUKS mapping stayed open and the child processes
kept running. Now SIGPIPE is handled. The first event that cannot be written
(`EPIPE`) starts the same cancel as a signal:

1. fisherman points its stdout at `/dev/null`, so the teardown commands do
   not write into the closed pipe;
2. it stops its children, thaws, turns off swap, unmounts and closes LUKS,
   as above;
3. it writes `fisherman: cancelled: installation cancelled (stdout closed)`
   to stderr and exits 130.

The cancel starts at the next event, not at the moment the reader goes away.
A child that writes to the closed pipe itself (bootc, podman) gets its own
SIGPIPE, and its failure ends the install through `fatal()` with the same
teardown.

A closed **stderr** does not cancel. fisherman's own stderr lines are lost,
and the install goes on: the event stream on stdout is the contract.

A second signal during teardown is logged and ignored, so it cannot leave
the disk half torn down. A signal that arrives after the install has
succeeded is ignored too. When a step fails, fisherman waits 300 ms before
exiting 1, in case the failure was the start of a cancel: a child killed
with the group can fail a moment before fisherman sees its own SIGTERM.

If fisherman cannot set the parent-death signal, it prints
`fisherman: warning: cannot cancel on parent exit (prctl PR_SET_PDEATHSIG): …`
to stderr and runs on. In that case, killing the wrapper leaves the install
running. If the parent has already exited by the time the signal is set,
fisherman cancels at once.

## Parsing example

```python
import json
import sys

for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue  # command echo or raw tool output
    try:
        event = json.loads(line)
    except ValueError:
        continue
    kind = event.get("type")
    if kind == "step":
        print(f"{event['overall_pct']}%  {event.get('step_id', event['step_name'])}")
    elif kind == "recovery_key":
        recovery_key = event["key"]  # keep it; show it before reboot
    elif kind == "receipt":
        receipt = event  # written, install_id, first_boot_check, reason
    elif kind == "error":
        print(f"failed: {event['message']}")
    elif kind == "complete":
        print(f"100%  {event['message']}")
    # ignore substep, info and any type you do not know
```

## Consumer rules

- Skip lines that are not JSON objects.
- Ignore unknown event types and unknown fields.
- Do not depend on key order.
- Match steps by `step_id`, not by `step` or `step_name`.
- Use `overall_pct` for the bar. Only `complete` means 100%.
- Treat EOF without `complete` or `error` as a failure, and check the exit code.

## Compatibility

There is no versioned protocol and no stability promise. The format has
grown over time:

- v0.1.0: `step`, `info` and `complete`.
- v0.2.0: the `substep` event; `weight_pct` and `cumulative_pct` on `step`; `timestamp` and
  `elapsed_ms` on every event; `boot_id` on `complete`.
- v0.3.0: the `recovery_key` event and the `error` event (#195).
- Unreleased: `overall_pct` on `step`, `substep` and `complete`, and `step_id`
  on `step` (#270); exit code 130 on cancel (#267); `code` and `field` on
  `error` (#404, #402); a closed stdout is a cancel with teardown, not a
  death by SIGPIPE (#403); the `source_reuse` event (#384); the `receipt`
  event (#439); `code` on every `error` event, with the failure codes, and
  `step_id`, `disk_changed` and `cleanup` on it (#369).

New event types and fields may appear in any release. Consumers must ignore
event types and fields they do not know.
