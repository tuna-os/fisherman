# `fishermand`: the installer daemon (mock slice)

`fishermand` is a Varlink service for installer frontends. It is the first
slice of tuna-os/fisherman#512. This slice runs in **mock mode only**. It
touches no disk, no TPM and no power state.

The `fisherman` CLI and the recipe pipeline do not change. wootc still uses
them.

```text
fishermand --mock [--scenario NAME[,NAME...]] [--socket-dir DIR]
```

## What works and what does not

| Part | State |
|---|---|
| Varlink wire layer (`internal/varlink`) | works |
| `io.systemd.SysInstall`: `ListCandidateDevices`, `Run` | works, mock data |
| `io.systemd.CryptEnroll`: `Enroll`, `ListSlots`, `MakeRecoveryKey` | works, in-memory slots |
| `io.systemd.Shutdown`: all five methods | works, records the call |
| `io.tunaos.Fisherman`: all seven methods | works, mock data |
| Real executor (repart, cryptenroll, copy, migrate, fixups) | **not implemented** (`internal/varlinkapi/real.go`) |
| polkit, peer credentials, socket activation | **not implemented** |

Without `--mock`, `fishermand` exits with code 2.

## Sockets

gnome-setup connects to one socket per systemd interface. The daemon opens
four sockets. Every socket serves every interface, so a client can use any
of them.

| Flag | Default |
|---|---|
| `--sysinstall-socket` | `/run/systemd/io.systemd.SysInstall` |
| `--cryptenroll-socket` | `/run/systemd/io.systemd.CryptEnroll` |
| `--shutdown-socket` | `/run/systemd/io.systemd.Shutdown` |
| `--fisherman-socket` | `/run/fisherman/io.tunaos.Fisherman` |
| `--socket-dir DIR` | puts all four in `DIR`, under their interface names |

An empty path disables a socket. `--socket-mode` sets the permission bits
(default `0600`). The daemon refuses to replace a socket that another
daemon holds.

Other flags: `--scenario` (or `FISHERMAN_MOCK_SCENARIO`), `--step-delay` (or
`FISHERMAN_MOCK_STEP_DELAY`, for a UI demo), `--list-scenarios`, `-v`.

## Interface definitions

The `.varlink` files are in `fisherman/internal/varlinkapi/idl/`. The
daemon embeds them and serves them through
`org.varlink.service.GetInterfaceDescription`.

The three `io.systemd.*` files are **reconstructed** from the gnome-setup
client. They hold only what gnome-setup calls. Compare them with systemd's
own files before you rely on them.

## `io.tunaos.Fisherman`

Field names use camelCase. A field with `?` may be absent or null. Arrays
are never null.

| Method | Does |
|---|---|
| `Requirements()` | RAM, CPU and firmware gates. Each gate has `ok`, `severity` and an optional `action` (`power_off`, `firmware_setup`). |
| `Probe()` | Disks, TPM, Intel RST flag, live media. Each disk has `existingData`, `freeBytes` and `minimalSizeBytes`. |
| `Validate(config, partial?)` | Machine-readable problems, returned as data. `partial` checks only the fields present and reads no machine state. |
| `ConfigureTarget(user?, hostname?, locale?, keymap?, timeZone?, autologin?)` | Stores the choices. They reach the target in the `configure` step. Returns problems as data. |
| `Install(config, confirmed)` | Needs `more`. Streams events. Refused unless `confirmed` is true. |
| `Handoff(mode?)` | `migrate`, `soft_reboot` or `reboot` (default). Only after a completed install. |
| `Cancel()` | Stops a running install and waits for cleanup. Safe to call when idle. |

Validation reuses the rules of `fisherman validate` (`internal/recipe`).
The codes are the same: `hostname_invalid_char`, `username_reserved`, and so
on. The daemon adds `disk_required`, `disk_not_found`, `disk_not_eligible`,
`disk_too_small`, `install_mode_required`, `install_mode_invalid`,
`insufficient_free_space`, `conflicting_disk_label`, `encryption_required`,
`locale_invalid`, `keymap_invalid` and `timezone_invalid`.

### Call order

The daemon enforces this state machine:

1. `idle`: `Install` with `confirmed: true` and a valid config moves to
   `installing`.
2. `installing`: another `Install` fails with `InstallInProgress`. `Cancel`
   or a dropped connection moves back to `idle` after cleanup.
3. A failed or cancelled install returns to `idle`. You can retry.
4. A complete install moves to `installed`. `Handoff` works. Another
   `Install` fails with `AlreadyInstalled`.

Errors: `NotConfirmed`, `InvalidConfig(errors)`, `InstallInProgress`,
`AlreadyInstalled`, `NotInstalled`, `NoSuchDisk`.

### Install events

Each reply is one event. The last reply has no `continues` flag. It is
`complete`, `failed` or `cancelled`.

| `event` | Meaning |
|---|---|
| `step_started`, `progress`, `step_finished` | A step runs. `stepId`, `stepIndex` and `totalSteps` are set. |
| `warning` | Something went wrong, but the install goes on. |
| `failed` | Final. Has `stepId`, `reason`, `message` and `nextAction`. |
| `cancelled` | Final. Cleanup is done. |
| `complete` | Final. `overallPct` is 100. |

`overallPct` never goes down. It stops at 99 until `complete`.

**Step ids are stable.** Never rename or reuse one. Ids that the recipe
pipeline already uses keep the same meaning (`partition`, `luks`,
`tpm2_enroll`, `mount`, `install_os`, `configure`, `finalize`).

| Step id | Runs |
|---|---|
| `validate_device` | always |
| `partition` | always |
| `luks` | when encryption is not `none` |
| `tpm2_enroll` | for `tpm2_luks` and `tpm2_luks_passphrase` |
| `mount` | always |
| `install_os` | always |
| `configure` | always |
| `post_fixups` | always |
| `bootloader` | always |
| `finalize` | always |
| `cleanup` | only after a failure or a cancel. It has no index. |

Failure `reason` values: `device_lost`, `repart_failed`, `luks_failed`,
`internal_error`. `nextAction` values: `choose_another_disk`, `retry`,
`report_bug`.

A client that drops the connection during `Install` cancels the install.

## `io.systemd.SysInstall` compatibility

The shapes come from gnome-setup, branch `utah` (zlink proxies and the demo
server).

- `ListCandidateDevices` needs `more`. Without `subscribe` it returns the
  eligible disks and ends. With `subscribe` it sends each disk with
  `action: "add"`, then `{"action":"ready"}`, then stays open. A lost disk
  arrives as `{"node":...,"action":"remove"}`.
- `fit` is one of `enough_free_space`, `conflicting_disk_label_present`,
  `insufficient_free_space`, `disk_too_small`.
- `currentSizeBytes` in a candidate is the space in use. In the
  `DiskTooSmall` and `InsufficientFreeSpace` errors it is the disk size.
- `Run` maps the install steps to the eight phases, in this order:
  `validate_block_device`, `load_credentials`, `encrypt_credentials`,
  `install_partitions` (with `progress` 0 to 100), `mount_partitions`,
  `install_kernel`, `install_bootloader`, `unmount_partitions`. The stream
  ends with an empty reply (no `parameters` key). gnome-setup ignores it on
  purpose.
- `Run` encrypts with an empty password. gnome-setup replaces it through
  `io.systemd.CryptEnroll`.
- `Run` errors: `ConflictingDiskLabelPresent`, `InsufficientFreeSpace` and
  `DiskTooSmall` when `erase` is false or the disk is too small. A failed
  install returns `io.systemd.System` with an errno (`ENODEV` for a lost
  disk, `EIO` otherwise).

`io.systemd.CryptEnroll` accepts the `password` and `recovery` mechanisms. It
answers `PasswordRequired`, `PasswordIncorrect`, `FidoDeviceNotFound` and
`VolumeUnderForeignManagement` as systemd does. A recovery key has 8 groups
of 8 letters, joined by `-`.

## Mock scenarios

Pick scenarios with `--scenario` or `FISHERMAN_MOCK_SCENARIO`. Join names
with commas, for example `no-tpm,existing-data`. `fishermand
--list-scenarios` prints the list.

| Name | Effect |
|---|---|
| `happy` | Empty 477 GiB NVMe disk, TPM 2.0, an 8 GiB USB stick (too small). The default. |
| `no-tpm` | No usable TPM. The frontend asks for a passphrase. |
| `rst` | Intel RST hides the NVMe disk. `intelRst.detected` is true and the disk is not eligible. |
| `existing-data` | Windows on the NVMe disk with room to spare, plus a disk with a foreign partition table. |
| `tight-space` | Like `existing-data`, but free space is below the minimum. |
| `low-ram` | The RAM gate blocks. |
| `no-uefi` | The firmware gate blocks. |
| `inhibitor` | `Shutdown` is blocked unless `skipInhibitors` is true. |
| `device-lost` | The disk disappears during `install_os`. `reason` is `device_lost`. |
| `repart-error` | systemd-repart fails during `partition`. `reason` is `repart_failed`. |
| `cancel` | The install waits at 50% of `install_os` until `Cancel` or a disconnect. |

## Secrets

Passphrases, passwords and recovery keys never reach a log. Three layers
enforce this:

- The Varlink layer logs the name of each method and error. It never logs parameters.
- Secret fields use the `Secret` type. It prints as `[redacted]`.
- Types that hold secrets have a `slog.LogValuer` method.

A test sends secrets through every call and searches the debug log. An
`Executor` must keep this rule. It must not put a secret in an error
message, because the failed event carries the message.

## Put real code behind the mock

Write a new `varlinkapi.Executor` (`executor.go`). The service owns the call
order, validation, step ids and the progress bar. The executor only reads
hardware and changes disks. The placeholder is `Real` in `real.go`. It lists
the work that slots in.

## Test

```bash
cd fisherman
go vet ./cmd/fishermand ./internal/varlink ./internal/varlinkapi
go test ./cmd/fishermand ./internal/varlink ./internal/varlinkapi
go test ./internal/varlinkapi -update   # rewrite the golden files
```

The golden files are in `fisherman/internal/varlinkapi/testdata/golden/`.
They pin the bytes of the mock replies. The strict decoders in
`compat_test.go` copy the gnome-setup Rust types and reject unknown fields.
