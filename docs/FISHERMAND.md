# `fishermand`: the installer daemon

`fishermand` is a Varlink service for installer frontends (tuna-os/fisherman#512,
tunaOS#4905, ADR 0018). The real executor changes disks: it mirrors the
installer medium onto the target disk, configures it, and reboots into it.
`--mock` serves scripted data and touches nothing.

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
| `io.systemd.SysInstall`: `ListCandidateDevices`, `Run` | works; mock serves scripted data, real partitions and overwrites the target disk |
| `io.systemd.CryptEnroll`: `Enroll`, `ListSlots`, `MakeRecoveryKey` | works; mock records the call, real shells out to `systemd-cryptenroll` and changes LUKS key slots |
| `io.systemd.Shutdown`: all five methods | works; mock records the call, real powers off, reboots or halts the machine |
| `io.tunaos.Fisherman`: all seven methods | works; mock serves scripted data, real writes the target disk and reboot shells out to `systemctl` |
| Real executor: repart definitions (`internal/repart`), `--dry-run` | works, read-only |
| Real executor: post-install fixups (`internal/postfix`) | works on a target root directory |
| Real executor: `Requirements`, `Probe`, `Install`, `ConfigureTarget` application, `Shutdown` (PowerOff, Reboot, Halt) | works; see "The real executor" |
| Real executor: btrfs migrate, soft-reboot, kexec | **deferred** (`ErrDeferred`, tunaOS#4908, #4909) |
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
| `ConfigureTarget(user?, hostname?, locale?, keymap?, timeZone?, autologin?, privacy?, accessibility?)` | Stores the choices. They reach the target in the `configure` step. Returns problems as data. Send `privacy` and `accessibility` only when the user changed them. |
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
- `Run` never leaves an empty password on the disk. systemd-repart
  encrypts with a random install key (64 hex characters). Only the
  daemon's tmpfs holds it, at `/run/fishermand/luks-transient/<LUKS UUID>`
  with mode 0600. gnome-setup unlocks with the empty password, as for
  systemd-sysinstall. For a volume with an install key, `CryptEnroll.Enroll`
  uses that key in place of the empty password. The file goes once an
  enroll wipes its slot. A reboot, a power cut or a closed frontend loses
  the key, and nobody can open that slot afterwards.
- `Run` honors `copyLocale`, `copyKeymap` and `copyTimezone`. The configure
  step then runs `systemd-firstboot --copy-locale`, `--copy-keymap` and
  `--copy-timezone` against each deployment. So the installed system gets the
  language and keyboard that the live session set through localed. A value
  from `ConfigureTarget` wins over a copy. `null` or `false` copies nothing.
- A copy can carry a language (a `LANG` other than `C` or `POSIX`) or a
  keyboard into the deployment. For each one, the configure step adds
  `language` or `keyboard` to `[pages] skip` in the deployment's
  `/etc/gnome-initial-setup/vendor.conf`. It starts from the image's
  `/usr/share` copy and keeps its entries. gnome-initial-setup then does not
  ask again on the first boot. Without a carried value it writes nothing, and
  it never writes on the live system. A deployment without
  gnome-initial-setup is left alone.
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

## The real executor

`Real` (`internal/varlinkapi/real*.go`) runs every host program through one
`Commander`. Unit tests use a fake and pin the command list in
`testdata/golden/real_install_passphrase.txt`.

`Probe` reuses `internal/probe` (lsblk, TPM, live) and adds existing data,
free space and Intel RST.

Probe finds RST three ways. One: a RAID-mode SATA controller (PCI class
0104, vendor 8086). Two: an Intel VMD controller with disks in its PCI
domain. Three: Intel RAID metadata on a disk. Affected disks are not
eligible (`intel_rst`).

`handoff` is `reboot` everywhere. The decision for `migrate` and
`soft_reboot` waits for tunaOS#4908 and #4909.
The wire has no offline-media field: the medium is the install source.

`Install` follows this model. The medium is a GPT image (ESP, /boot, root).

| Step | What it does |
|---|---|
| `validate_device` | The target is a whole, unmounted disk, not the medium, and large enough. |
| `partition` | `systemd-repart` writes the GPT and copies each medium partition with `CopyBlocks=`. The root lands in LUKS2 (`key-file+tpm2` on PCR 7 for the TPM2 types, else `key-file`). The bytes move here, so this step takes most of the time. |
| `luks` | The user's passphrase replaces the install key's keyslot. |
| `tpm2_enroll` | Checks that repart sealed the TPM2 token. |
| `mount` | Opens the root, gives each copied filesystem a new UUID (vfat, ext4, btrfs, xfs), then mounts root, /boot and the ESP. A btrfs seed flag is cleared. |
| `install_os` | Checks for a deployment and a pristine machine-id. |
| `configure` | `systemd-firstboot --root`, `useradd`, the password hash, autologin (GDM), privacy. Applied to every deployment. |
| `post_fixups` | `internal/postfix`: references rewritten, live entry (`medium-live`, `tunaos.live`) and the medium sysext removed, `rd.luks.uuid` and crypttab, root grown. |
| `bootloader` | `efibootmgr` entry for shim, when efivars exist. |
| `finalize` | Unmount, close LUKS, remove the scratch tree. |

Failure and cancel run `cleanup` on a context that survives the cancel:
unmount, close the LUKS volume, remove the scratch tree. The partition
write is not undone. A cancel returns the cancellation, not a failure.

Rules and limits:

- An unencrypted install (`none`) fails at `validate_device` with reason
  `unsupported_config` and next action `change_settings`.
- An empty passphrase (the SysInstall `Run` path) keeps the install key's
  slot for the frontend to replace, and the install warns. `tpm2_luks` with
  no passphrase wipes it at the end, so only the TPM unlocks the disk, and
  the install warns.
- `finalize` fails the install when the empty passphrase opens the new
  volume. A failed install deletes its install key.
- An abandoned install has no known secret. Without a TPM the disk needs a
  new install. With a TPM it boots on the TPM alone, with no recovery key.
- The new reasons and action are additive: `disk_too_small`,
  `unsupported_config`, `change_settings`.
- `ConfigureTarget`'s `privacy.locationServices` lands in
  `/etc/tunaos/privacy.conf`. The image must act on it.
- `ConfigureTarget`'s `accessibility` (screen reader, display scale) lands in
  `/etc/tunaos/accessibility.conf`, with a script and an XDG autostart entry
  that apply it once for each account at its first login: the at-spi status
  and its GSettings key, and `cosmic-randr` for the scale where the image has it.
- The step weights did not change, so the bar sits low during `partition`.
- `Shutdown` refuses `KExec` and `SoftReboot`.

## Integration test

```bash
go test -c -tags integration -o it.test ./internal/varlinkapi
sudo env PATH="$PATH" FISHERMAND_IT_DIR=$HOME/tunaos-fishermand/it ./it.test -test.run Integration -test.v
```

It builds a fake medium on a loop device (btrfs seed root, ostree deployment). It installs onto a second loop device. It checks the result with independent tools. It needs root, never writes a firmware boot entry, and
refuses anything that is not a loop device it attached.

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

## Changelog (install slice)

- Fixed: `systemd-cryptenroll` ignores stdin. The new secret goes in
  `NEWPASSWORD`. The daemon enrolls a recovery key as a password.
- Added: failure reasons `disk_too_small` and `unsupported_config`, next
  action `change_settings`.
