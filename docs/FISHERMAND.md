# `fishermand`: the installer daemon (real executor slice)

`fishermand` is a Varlink service for installer frontends. It is the second
slice of tuna-os/fisherman#512. The real executor changes disks. It covers
repart definitions, cryptenroll, and the reboot handoff. Probe, the Install
pipeline, Power, and the deferred migrate/soft-reboot handoff still fail
until their slices land. `--mock` serves scripted data and touches nothing.

The `fisherman` CLI and the recipe pipeline do not change. wootc still uses
them.

```text
fishermand [--mock --scenario NAME[,NAME...]] [--socket-dir DIR]
fishermand --dry-run [--medium-disk DEV --root-node PART]
```

## What works and what does not

| Part | State |
|---|---|
| Varlink wire layer (`internal/varlink`) | works, with peer credentials |
| `io.systemd.SysInstall`: `ListCandidateDevices`, `Run` | works, mock data |
| `io.systemd.CryptEnroll`: `Enroll`, `ListSlots`, `MakeRecoveryKey` | works on mock; real shells out to `systemd-cryptenroll` |
| `io.systemd.Shutdown`: all five methods | works, mock records the call |
| `io.tunaos.Fisherman`: all seven methods | works, mock data; real reboot shells out to `systemctl` |
| Real executor: repart definitions (`internal/repart`), `--dry-run` | works, read-only |
| Real executor: post-install fixups (`internal/postfix`) | works on a target root directory |
| Real executor: Probe, Install pipeline, Power, btrfs migrate, soft-reboot | **not implemented** (ErrNotImplemented/ErrDeferred) |
| polkit, peer credentials, socket activation | works in real mode (mock mode asks nothing) |

## Sockets

gnome-setup connects to one socket per systemd interface. Sockets work like
systemd in real mode: one interface on each socket. In mock mode each
socket serves all interfaces, so a client can use any of them.

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

Under socket activation (`LISTEN_PID` is us) one inherited socket carries
all enabled interfaces. With N inherited sockets each one carries a single
interface. The unit files decide the matrix. Anything else is a unit bug.

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
| `Probe()` | Disks, TPM, Intel RST flag, live media, the handoff decision and the license. Each disk has `existingData`, `freeBytes` and `minimalSizeBytes`. |
| `Validate(config, partial?)` | Machine-readable problems, returned as data. `partial` checks only the fields present and reads no machine state. |
| `ConfigureTarget(user?, hostname?, locale?, keymap?, timeZone?, autologin?, privacy?)` | Stores the choices. They reach the target in the `configure` step. Returns problems as data. Send `privacy` only when the user changed it. |
| `Install(config, confirmed)` | Needs `more`. Streams events. Refused unless `confirmed` is true. |
| `Handoff(mode?)` | The daemon decides when `mode` is absent. Only after a completed install. |
| `Cancel()` | Stops a running install and waits for cleanup. Safe to call when idle. |

Validation reuses the rules of `fisherman validate` (`internal/recipe`).
The codes are the same: `hostname_invalid_char`, `username_reserved`, and so
on. The daemon adds `disk_required`, `disk_not_found`, `disk_not_eligible`,
`disk_too_small`, `install_mode_required`, `install_mode_invalid`,
`insufficient_free_space`, `conflicting_disk_label`, `encryption_required`,
`locale_invalid`, `keymap_invalid` and `timezone_invalid`.

`privacy` holds `locationServices` (default false). The daemon decides how
the target applies it. `handoff` holds the takeover decision: `mode`
(`migrate`, `soft_reboot` or `reboot`) and a `reason`. A missing field
means `reboot`.

`eula` names the shared license file: `available` and an optional `path`.
Every frontend reads `/usr/share/tuna-installer/eula.xml` unless
`TUNA_EULA_FILE` overrides it. No file means the frontend skips the page.
That file carries an AppStream component with an `agreement` of type
`eula`.

### Call order

The daemon enforces this state machine:

1. `idle`: `Install` with `confirmed: true` and a valid config moves to
   `installing`.
2. `installing`: another `Install` fails with `InstallInProgress`. Only
   `Cancel` stops the install; a dropped connection only stops that
   client's event stream, never the install.
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

**v1 freezes the step ids.** Never rename or reuse one. The v1 set is the
ten clean-install steps in the table below plus `cleanup`. Ids that the
recipe pipeline already uses keep the same meaning (`partition`, `luks`,
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

When polkit denies a call, the call fails with the standard
`org.varlink.service.PermissionDenied` error. That error lives outside any
interface.

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
  disk, `EIO` otherwise). These names match the `ReplyError` enums in
  gnome-setup branch `utah` (`SysInstallError`, `CryptEnrollError`,
  `ShutdownError`); the strict decoders in `compat_test.go` pin the bytes.

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
| `cancel` | The install waits at 50% of `install_os` until `Cancel`. |
| `handoff-migrate` | The daemon's takeover decision is `migrate`. |
| `handoff-soft-reboot` | The daemon's takeover decision is `soft_reboot`. |

## Authorization

In real mode each call that changes state asks the polkit authority through
`pkcheck`. That covers `Install`, `Cancel`, `Handoff` and `ConfigureTarget`.
It also covers SysInstall `Run`, CryptEnroll `Enroll` and `MakeRecoveryKey`,
and each `Shutdown` verb. `pkcheck` speaks to polkitd over D-Bus. The check
names only the interface and the method. Devices, slots and credentials
never leave the daemon.

The daemon attributes each caller by `SO_PEERCRED` and reads the caller's
start time from `/proc` when it accepts the connection. It names the caller
to `pkcheck` as `pid,start-time,uid`, the form `pkcheck(1)` asks for. The
UID is the one the kernel recorded for the socket, so a process that later
gets the same PID cannot answer for the caller. A caller the daemon cannot
name this way (not a UNIX socket, or gone before the daemon read its start
time) is denied.

A denial fails with the standard `org.varlink.service.PermissionDenied`
error. The matching policy ships with the installer image. Without that
policy the authority denies these calls. Mock mode asks nothing. Read-only
calls stay public: `Probe`, `Requirements`, `Validate`,
`ListCandidateDevices` and `ListSlots`.

## Secrets

Passphrases, passwords and recovery keys never reach a log. Three layers
enforce this:

- The Varlink layer logs the name of each method and error. It never logs parameters.
- Secret fields use the `Secret` type. It prints as `[redacted]`.
- Types that hold secrets have a `slog.LogValuer` method.

A test sends secrets through every call and searches the debug log. An
`Executor` must keep this rule. It must not put a secret in an error
message, because the failed event carries the message.

## The real executor so far

`Real` (`internal/varlinkapi/real.go`) covers the safe parts behind the
`Executor` interface. `internal/repart` makes the repart definitions: a pure
Go port of Utah's `utah-live-definitions`. `internal/postfix` fixes up the
target root: a pure Go port of Utah's `utah-sysinstall-post`.

`internal/crypt` enrolls passphrases and recovery keys and wipes slots.
Secrets ride stdin and an anonymous pipe. They never reach logs, errors or
disk.

`internal/handoff` reboots. `migrate` and `soft_reboot` fail with
`ErrDeferred`. Their mechanism belongs to tunaOS#4908 and tunaOS#4909. The
service still owns call order, validation, step ids and the progress bar.

## Test

```bash
cd fisherman
go vet ./cmd/fishermand ./internal/varlink ./internal/varlinkapi ./internal/auth ./internal/repart ./internal/postfix ./internal/crypt ./internal/handoff
go test ./cmd/fishermand ./internal/varlink ./internal/varlinkapi ./internal/auth ./internal/repart ./internal/postfix ./internal/crypt ./internal/handoff
go test ./internal/varlinkapi -update   # rewrite the golden files
```

The golden files are in `fisherman/internal/varlinkapi/testdata/golden/`.
They pin the bytes of the mock replies. The strict decoders in
`compat_test.go` copy the gnome-setup Rust types and reject unknown fields.

## Changelog (supervisor rulings slice)

`io.tunaos.Fisherman` changed; the systemd files did not. Step ids stay
frozen.

- `ConfigureTarget` takes `privacy` with `locationServices` (default
  false). Send it only when the user changed it.
- `Probe` reports `handoff` with the daemon's takeover decision and
  `eula` with the shared license path. A missing handoff means `reboot`.
  No license file means the frontend skips the page. Shared path:
  `/usr/share/tuna-installer/eula.xml`, `TUNA_EULA_FILE` overrides it.
- `Handoff` with no mode uses the daemon's decision.
- The wire keeps the Handoff word `soft_reboot` (not the ruling's
  `soft-reboot`). The owner can overrule this.

## Changelog (real executor slice)

What changed on the wire and in behavior:

- Real mode exists. Without `--mock` the daemon runs the real executor.
  Each socket has a single interface. Mock mode keeps all interfaces on
  each socket.
- A dropped `Install`/`Run` connection no longer cancels the install. Only
  an explicit `Cancel` does.
- v1 freezes the step ids (`validate_device` … `finalize` plus `cleanup`).
- When polkit denies a call, it fails with standard
  `org.varlink.service.PermissionDenied`. That error lives outside any
  interface.
- `fishermand --dry-run` prints the systemd-repart definitions for the
  medium and changes nothing.
- We checked `Run` errors against gnome-setup branch `utah`. Each name
  matches, so nothing changed.
