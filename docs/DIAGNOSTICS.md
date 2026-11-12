# Diagnostics bundle

`fisherman diagnostics` writes a local support bundle: one JSON file of
allowlisted facts about an install. A person reads it, and then shares it by
hand if they want to. fisherman never uploads it, never opens an issue, and
never runs it on its own: an install does not call it, so an export that
fails cannot change an install's outcome, its terminal event or its exit
code (tuna-os/fisherman#375).

The code is `fisherman/internal/diagnostics`. This document is its contract;
`TestDiagnosticsDoc` holds the recipe table and the omitted list below to the
code.

## Use

```bash
# Save the install's stdout while it runs. That log holds the recovery key:
# keep it private. The bundle does not hold it.
fisherman - < recipe.json | tee progress.ndjson

# After a failure: review the bundle on screen first ...
fisherman diagnostics --stdout --recipe recipe.json --progress-log progress.ndjson --probe | less
# ... then write it.
fisherman diagnostics --output-dir . --recipe recipe.json --progress-log progress.ndjson --probe
```

| Flag | Meaning |
|---|---|
| `--output-dir <dir>` | write the bundle to a new file in `<dir>`, and print its path |
| `--stdout` | print the bundle instead, to review it |
| `--recipe <file\|->` | the recipe of the install; read, never copied |
| `--progress-log <file\|->` | the install's saved stdout (NDJSON progress events) |
| `--probe` | run the read-only probe now |
| `--probe-report <file\|->` | use a saved `fisherman probe --json` report |
| `--include-identifiers` | keep host names, user names and device paths |

Give exactly one of `--output-dir` and `--stdout`. At most one input reads
stdin (`-`). Every input is optional; a section without input has the status
`not_requested`. The command needs no root.

Exit codes: 0 when the bundle is written or printed; 1 when it cannot be
(the output directory is a symlink or absent, the disk is full); 2 for a usage
error. An input that cannot be read or decoded does not fail the export: its
section gets the status `unreadable` and the bundle holds nothing of it.

## What the export does not do

- It only reads. It does not mount, unmount, unlock, close, retry or clean
  up anything, and it does not touch the install lock.
- It runs no command except the probe with `--probe`. The probe is
  read-only (docs/PROBE.md) and bounded: each probe command has its own
  timeout, and the export gives up on the whole probe after 60 seconds
  (status `timed_out`).
- It does not read the environment, the journal, `/etc` (except the
  allowlisted keys of `os-release`) or any configuration directory.

## The file

- A new file in the output directory, named
  `fisherman-diagnostics-<UTC time>-<8 random hex digits>.json`.
- Created with mode 0600 and `O_EXCL|O_NOFOLLOW`: it never replaces a file
  and never follows a symlink, a dangling one included. A taken name gets a
  new random suffix, five times at most.
- The output directory must be a directory, not a symlink to one.
- A write error (a full disk) removes the partial file: there is a whole
  bundle or none.

## Bounds

| Bound | Value |
|---|---|
| recipe read | 1 MiB (the install's own bound) |
| progress log read | the last 32 MiB; the first, partial line is then dropped |
| one log line | 64 KiB; a longer line is counted as `oversizedLines` and skipped |
| timeline | the last 500 events |
| one message | 2,048 bytes, after redaction |
| mounts | 256 entries |
| bundle | 4 MiB; the oldest timeline events go first |

The manifest states the same bounds under `limits`.

## Contents

Every value comes through an allowlist. Free text in a field that takes an
enum or a fixed shape becomes `[other]`; an image reference with credentials
or a local path becomes `[omitted]`. A value with a secret shape (below) is
`[other]` too.

| Section | What it holds |
|---|---|
| `manifest` | the status of each section (`included`, `not_requested`, `unreadable`, `timed_out`), whether identifiers are masked, how many redactions were made, the omitted list, the bounds and the residual limits |
| `versions` | fisherman, Go, OS and architecture; the kernel release; `ID`, `ID_LIKE`, `VERSION_ID` and `VARIANT_ID` of `os-release` |
| `probe` | disks (path, size, transport, removable, read-only, eligible, excluded reason; model and vendor only with `--include-identifiers`), TPM, RAM, CPU, UEFI and unmet requirements, live media and live image, the number of offline stores and images, host bootc version and capabilities, GPU ids, machine model, and the capability report's source, live host and execution root (docs/PROBE.md). Not: store paths, image names in stores, the bootc store items, the install defaults, the image facts |
| `recipe` | the effective options, by the field table below |
| `progress` | the timeline, the event counts, the outcome |
| `mounts` | the mounts at and under the target (`/mnt/fisherman-target` or the recipe's `targetMount`) and the scratch directory (`/var/fisherman-tmp`), with file system type, source and read-only flag; whether the LUKS mapper (`fisherman-root` or the recipe's `luksMapperName`) is open; how many other device-mapper devices exist |

### Recipe fields

The bundle holds no recipe. It holds one entry for each field, by policy:

- `value`: the value, held to its enum or shape;
- `identifier`: a host, user or device identifier, masked unless
  `--include-identifiers`;
- `presence`: only whether the field is set, or how many entries it has;
- `secret`: only whether the field is set. The value is never in the bundle.

A key the recipe type does not have is counted in `unknownFields`; its name
and value are not in the bundle. A new recipe field is left out until it gets
a policy here.

| Field | Policy |
|---|---|
| `disk` | identifier |
| `filesystem` | value |
| `btrfsSubvolumes` | value |
| `encryption.type` | value |
| `encryption.passphrase` | secret |
| `image` | value |
| `targetImgref` | value |
| `sourceReuse` | presence |
| `selinuxDisabled` | value |
| `unifiedStorage` | value |
| `composeFsBackend` | value |
| `genericImage` | value |
| `zfsPoolName` | presence |
| `bootloader` | value |
| `imageType` | value |
| `flatpakVarPath` | presence |
| `hostname` | identifier |
| `flatpaks` | presence |
| `user.username` | identifier |
| `user.fullname` | presence |
| `user.password` | secret |
| `user.groups` | value |
| `customMounts[].partition` | identifier |
| `customMounts[].target` | value |
| `customMounts[].fstype` | value |
| `varDisk.disk` | identifier |
| `varDisk.keepExisting` | value |
| `additionalImageStores` | presence |
| `slurpWallpapers` | value |
| `slurp` | presence |
| `targetMount` | presence |
| `luksMapperName` | presence |
| `distroID` | presence |
| `brewTap` | presence |
| `override` | value |
| `copyNetworkProfiles` | value |
| `copyBluetoothPairings` | value |
| `extras` | value |
| `experimental` | value |
| `setBootNext` | value |
| `locale` | value |
| `keyboard` | value |
| `timezone` | value |

### Progress timeline

The export decodes only these fields of each event
(docs/JSON-PROGRESS-FORMAT.md):

| Event | Kept |
|---|---|
| `step` | `step`, `total_steps`, `step_id`, `overall_pct`, and `messages`: how many `info` and `substep` events followed it |
| `info`, `substep` | nothing but the count; the text is not in the bundle |
| `recovery_key` | that it was shown; never the key |
| `receipt` | `version`, `written`, `first_boot_check`, `reason`; not `install_id` or `path` |
| `error` | `message` (redacted), `code`, `field`, `step_id`, `disk_changed`, `cleanup.status`, `cleanup.message` (redacted) |
| `complete` | `message` (redacted), `overall_pct`; not `boot_id` |

An event of another type is counted in `unknownEvents`. A line that is not a
JSON object (stderr mixed into the log, a cut line) is counted in
`malformedLines`. Neither is in the bundle.

`outcome` is the end the log shows: `state` `complete`, `error` (with the
`code`, `step_id`, `disk_changed` and `cleanup` status of the last `error`
event; a missing code is `unknown`) or `unfinished` (no terminal event: the
log is cut, or the install was killed). The codes are the runtime failure
codes of docs/JSON-PROGRESS-FORMAT.md and the validate codes of
docs/VALIDATE.md.

## Redaction and masking

Before any message is kept, the export removes:

1. the recipe's `encryption.passphrase` and `user.password` (a plaintext
   password or a crypt hash), and every `recovery_key` of the log, wherever
   they occur. It also removes a start of one of them that ends a message
   cut by fisherman;
2. secret shapes: recovery keys, crypt hashes, PEM private keys, Tailscale,
   GitHub, GitLab and Slack tokens, WireGuard keys, credentials in a URL,
   `--creds` arguments, `Authorization` headers and `password=`, `psk=`,
   `token:` and similar pairs.

Each becomes `[redacted]`. Unless `--include-identifiers`, the export then
replaces the recipe's host name, user name and device paths, and every
`/dev/...` path in a message, with tokens such as `host-1`, `user-1` and
`device-1`. One value gets the same token in every section.

### Always omitted

- encryption passphrase
- recovery key
- user password and password hash
- raw recipe, unknown recipe fields and their values
- info and substep message text, command arguments, stdout and stderr
- environment variables
- journals and configuration directories
- registry credentials and image references with credentials
- network, Wi-Fi and Bluetooth secrets
- SSH and WireGuard private keys
- Tailscale keys, URLs and state
- offline store paths and image names
- names of other device-mapper devices and mounts outside the target and scratch

### Residual limits

- `error` and `complete` messages stay, after redaction. Redaction knows
  the secrets of the recipe it is given and of the log, and the shapes
  above. It cannot know a secret that matches none of them, for example a
  password of another service in a command's error line. Without
  `--recipe`, it does not know the recipe's secrets; fisherman removes them
  from its own `error` events when it emits them (docs/JSON-PROGRESS-FORMAT.md).
- The machine model, GPU ids, disk sizes and image references stay. They do
  not name a person, but they can narrow down a machine.
- Masked tokens are consistent inside one bundle, so a reader can match two
  mentions of one device.

## Tests

`internal/diagnostics/sentinel_test.go` plants known secret values in every
input: the recipe (passphrase, plaintext password and crypt hash, unknown
fields, credentials in the image reference), the progress log (the recovery
key, the key repeated in messages, malformed and mixed lines, unknown events
and fields, a message cut inside a secret), the probe report, `os-release`,
`mountinfo` and the device-mapper names. The test fails if the bundle holds
any 8-byte run of any of them, with identifiers masked and kept. Other tests
cover failed cleanup, cancellation, a cut log, huge logs, the file mode and
name, name collisions, symlinks, a full disk and the probe timeout.
