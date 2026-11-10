# Troubleshooting Guide

This guide tells you how to find the cause of a failed fisherman install, and
how to recover the machine.

The commands in this guide were run on real fisherman output. The reference
documents are [`PROBE.md`](PROBE.md), [`VALIDATE.md`](VALIDATE.md) and
[`JSON-PROGRESS-FORMAT.md`](JSON-PROGRESS-FORMAT.md). If this guide and a
reference document disagree, the reference document is correct.

## Before the install

### Validate the recipe

```bash
fisherman validate recipe.json
```

This prints a summary of a valid recipe, or the first problem. It needs no
root and it does not change a disk.

To get every problem, with a stable code for each one:

```bash
fisherman validate --json recipe.json
```

```json
{
  "protocol_version": 1,
  "valid": false,
  "errors": [
    {
      "field": "hostname",
      "code": "hostname_invalid_char",
      "message": "hostname \"my_pc\" contains '_'; use only letters, digits, hyphens and dots"
    }
  ]
}
```

The exit status is 0 for a valid recipe and 1 for an invalid one.
[`VALIDATE.md`](VALIDATE.md) lists each code, its cause and its fix.

If the recipe holds a LUKS passphrase, do not write it to a file. Send it on
stdin:

```bash
printf '%s' "$RECIPE_JSON" | fisherman validate --json -
```

### Probe the machine

```bash
fisherman probe --json
```

Run it as your own user, not with `sudo`. It prints one JSON object:

| Field | Meaning |
|---|---|
| `disks[]` | Every block device. Each entry has `path`, `size_bytes`, `size_label`, `eligible`, and `excluded_reason` when `eligible` is false. |
| `tpm.present`, `tpm.version`, `tpm.usable` | The TPM. The `tpm2-luks` encryption types need `tpm.usable` to be true. |
| `system.ram_bytes` | Total memory in bytes. |
| `system.cpu_threads`, `system.cpu_cores` | Logical CPUs and physical cores. |
| `system.uefi` | True when the machine booted through UEFI firmware. |
| `system.meets_requirements`, `system.unmet[]` | `unmet` lists the requirements that the machine does not meet: `ram` (less than 3 800 000 000 bytes), `cpu` (fewer than 2 cores), `uefi`. This is advice for a frontend. fisherman does not refuse an install because of it. |
| `live.is_live`, `live.live_image` | True when the machine booted from live media. Only then can the recipe's `image` be empty. |
| `offline.stores[]`, `offline.images[]` | Offline image stores on the live media, and the images in them. |

[`PROBE.md`](PROBE.md) has the full schema and the rules behind each field.

Useful queries:

```bash
# The disks that can be install targets
fisherman probe --json | jq -r '.disks[] | select(.eligible) | "\(.path)  \(.size_label)"'

# Why a disk is not offered
fisherman probe --json | jq '.disks[] | select(.path == "/dev/sda") | {eligible, excluded_reason}'

# The requirements that the machine does not meet
fisherman probe --json | jq '.system.unmet'

# Can this machine use tpm2-luks?
fisherman probe --json | jq '.tpm.usable'
```

A disk with `"excluded_reason": "boot_disk"` holds the system that is running
now. fisherman does not offer it.

## During the install

fisherman must run as root. It erases the target disk.

```bash
sudo fisherman recipe.json
```

It writes progress events to stdout, one JSON object on each line. **stdout
also has lines that are not JSON**: fisherman prints the commands that it
runs (`+ sfdisk ...`, `+ podman ...`) and the output of bootc, podman and tar.
For that reason `fisherman ... | jq .` fails. Keep the full output, and let
`jq` skip the lines that are not JSON:

```bash
sudo fisherman recipe.json 2> install.err | tee install.log
jq -R 'fromjson?' install.log
```

Each event has a `type`. There is no `status` field and no `action` field.

| `type` | Meaning | Main fields |
|---|---|---|
| `step` | A pipeline step started. | `step`, `total_steps`, `step_id`, `step_name`, `overall_pct` |
| `substep` | Progress in the current step. | `message`, `overall_pct` |
| `info` | A note. A message that starts with `Warning:` reports a problem that did not stop the install. | `message` |
| `recovery_key` | The generated LUKS recovery key of a `tpm2-luks` install. Write it down before you reboot. | `key` |
| `complete` | The install succeeded. | `message`, `boot_id`, `overall_pct` |
| `error` | The install failed. No more events follow. | `message` |

A real `step` event and a real `error` event:

```json
{"cumulative_pct":0,"elapsed_ms":886,"overall_pct":0,"step":1,"step_id":"partition","step_name":"Partitioning disk","timestamp":"2026-11-10T14:34:05.38374144Z","total_steps":8,"type":"step","weight_pct":0}
{"elapsed_ms":303,"message":"invalid recipe: disk /dev/does-not-exist: stat /dev/does-not-exist: no such file or directory","timestamp":"2026-11-10T14:34:04.17988668Z","type":"error"}
```

### The steps

The number of steps depends on the recipe: from 5 to 11. Identify a step by
its `step_id`, not by its number.

| `step_id` | `step_name` | When it runs |
|---|---|---|
| `prepare_disk` | Preparing disk | Only with `customMounts`. It replaces the next four disk steps. |
| `partition` | Partitioning disk | Automatic layout. |
| `format_efi` | Formatting EFI partition | Automatic layout. On a GRUB layout it also formats the ext4 `/boot`. |
| `luks` | Setting up disk encryption | Automatic layout with encryption. |
| `format_root` | Formatting root filesystem | Automatic layout. |
| `mount` | Mounting filesystem | Automatic layout. |
| `format_var` | Formatting data disk (/var) | With `varDisk`, when `keepExisting` is false. |
| `install_os` | Installing OS | Always. Image pull, `bootc install to-filesystem`, bootloader. |
| `tpm2_enroll` | Enrolling TPM2 auto-unlock | `tpm2-luks` and `tpm2-luks-passphrase`. |
| `flatpaks` | Copying system Flatpaks | Always. |
| `configure` | Configuring installed system | Always. Hostname, user, kernel arguments. |
| `finalize` | Finalizing installation | Always. |

**If the install appears to stop:** `install_os` is most of the install time.
A large image on a slow network can take many minutes. Look at the `substep`
events: `Pulling image: layer 23/71` shows that the pull continues.

```bash
jq -R 'fromjson? | select(.type == "substep") | .message' install.log | tail -5
```

## After a failure

### Find the error

fisherman exits with status 1 when the install fails. The last event is an
`error` event, and stderr has the same message after `fisherman: fatal:`.

```bash
jq -R 'fromjson? | select(.type == "error") | .message' install.log
```

To see which step failed, print the last `step` event:

```bash
jq -R 'fromjson? | select(.type == "step") | {step, total_steps, step_id, step_name}' install.log | jq -s '.[-1]'
```

To see the problems that did not stop the install:

```bash
jq -R 'fromjson? | select(.type == "info" and (.message | startswith("Warning:"))) | .message' install.log
```

The message is a short prefix, then the cause. These are the prefixes that
fisherman prints:

| Message starts with | Cause | What to do |
|---|---|---|
| `loading recipe:` | fisherman cannot read the recipe, or the recipe is not valid JSON. With `fisherman -`, stdin was empty. | Correct the path or the JSON. |
| `invalid recipe:` | The recipe breaks a rule. No disk was changed. | Run `fisherman validate --json` and see [`VALIDATE.md`](VALIDATE.md). |
| `missing required host tool:` | A program that this recipe needs is not in `PATH`, for example `"mkfs.xfs" not found in PATH`. No disk was changed. | Install the package. |
| `required local source is absent or unreadable` | The recipe names a local image (`oci:` or `containers-storage:`) that is not there. | Correct the `image` field or the media. |
| `partitioning disk:` | `sfdisk` or the disk preparation failed. The disk is in use, is read-only, or has an I/O error. | Unmount the disk and close its mappings. See [Clean up](#clean-up). |
| `formatting EFI:`, `formatting /boot:`, `formatting root filesystem:` | `mkfs` failed. | Read the `mkfs` output above the event in `install.log`. |
| `LUKS format:`, `LUKS open:` | `cryptsetup` failed. | Read the `cryptsetup` output above the event. |
| `mounting root:`, `mounting /boot:`, `mounting EFI:` | `mount` failed. | Make sure that nothing else is mounted at `/mnt/fisherman-target`. |
| `formatting /var disk:`, `mounting /var disk:` | The `varDisk` step failed. The system disk is already erased at this point. | Make sure that the var disk is not mounted. |
| `bootc install:` | The image pull or `bootc install to-filesystem` failed. This is the most frequent failure. | See [The image does not install](#the-image-does-not-install). |
| `creating user:` | `useradd` or `chpasswd` failed in the installed system. | Read the output above the event. |
| `finalizing target filesystem:` | The last step failed. | Read the output above the event. |
| `installation cancelled (SIGTERM)` | The install was cancelled. The signal name can be `SIGINT` or `SIGHUP`. The exit status is 130. | See [A cancelled install](#a-cancelled-install). |

If the message ends with `; cleanup failed, the target may still be mounted
or unlocked: …`, fisherman could not release the disk. Do the steps in
[Clean up](#clean-up).

### Exit status

| Status | Meaning |
|---|---|
| 0 | Success. The last event is `complete`. |
| 1 | Failure. The last event is `error`. |
| 2 | The argument is not a recipe and not a command. No events. A Go panic also exits 2, with a stack trace on stderr and no cleanup. |
| 130 | Cancelled by SIGTERM, SIGINT or SIGHUP, or because the parent process died. The last event is `error`. |
| 126, 127 | From `pkexec`, not from fisherman: the user closed the authentication dialog, or authorization failed. fisherman did not start. |

If the output ends with no `complete` event and no `error` event, the install
failed. A SIGKILL, a panic or a power loss causes this.

### Clean up

When an install fails or is cancelled, fisherman stops its child processes,
unmounts the target and closes the LUKS mapping before it writes the `error`
event. Usually there is nothing to clean up. Do these steps after a SIGKILL
or a panic, or when the error message says `cleanup failed`.

Look at the state:

```bash
lsblk -o NAME,SIZE,FSTYPE,LABEL,MOUNTPOINTS /dev/sdX
findmnt -R /mnt/fisherman-target
sudo dmsetup ls
```

Release the disk:

```bash
sudo umount -R /mnt/fisherman-target
sudo cryptsetup close fisherman-root
```

`/mnt/fisherman-target` and `fisherman-root` are the defaults. A recipe can
change them with `targetMount` and `luksMapperName`.

Remove the scratch data. fisherman keeps image layers in `/var/fisherman-tmp`
during the install. On live media, where `/var` is small, it uses
`.fisherman-scratch` on the target disk, and the next install erases that.

```bash
sudo rm -rf /var/fisherman-tmp
```

Then run the install again. fisherman closes a mapping that an earlier run
left open, and it writes a new partition table. You do not need to erase the
disk first.

### What is on the disk after a failure

fisherman erases the target disk in its first step. It has no rollback: the
data that was on the disk is gone when the `partition` step starts.

- A failure before `partition` (`loading recipe:`, `invalid recipe:`,
  `missing required host tool:`) changes nothing on the disk.
- A failure in a later step leaves a disk that has partitions but no
  complete system. Do not boot from it. Correct the cause and install again.

With encryption, a failed install leaves a LUKS container that holds no
useful data. If you must look in it, use the recipe's passphrase. The root is
the last partition: the second on a systemd-boot layout, the third on a GRUB
layout.

```bash
sudo cryptsetup open /dev/sdX3 fisherman-inspect
sudo mount -o ro /dev/mapper/fisherman-inspect /mnt
# ...
sudo umount /mnt
sudo cryptsetup close fisherman-inspect
```

A `tpm2-luks` install has no passphrase in the recipe. Its only key at that
point is the one in the `recovery_key` event, which arrives late in the
install. If the install failed before that event, you cannot open the
container. Install again.

## Common problems

### The image does not install

The error starts with `bootc install:`.

- The image reference is wrong, or the registry needs a login. Test it as
  root, because fisherman pulls as root:
  ```bash
  sudo skopeo inspect docker://<image>
  sudo podman login <registry>
  ```
- The network is down. On live media with an offline store, fisherman uses
  the local copy and prints the `info` message
  `Offline: registry unreachable, using locally cached image`.
- The disk that holds `/var/fisherman-tmp` is full. Look at `df -h /var`.
- The image cannot install itself. Read the bootc output above the `error`
  event in `install.log`: bootc's own message is there.

### A cancelled install

A SIGTERM, SIGINT (Ctrl+C) or SIGHUP cancels the install. fisherman then:

1. stops its child processes;
2. unmounts the target and closes the LUKS mapping;
3. writes an `error` event: `installation cancelled (SIGINT)`;
4. exits with status 130.

You do not need to clean up by hand, unless the message ends with
`cleanup failed`. The target disk is already erased if the `partition` step
started. Run the install again.

A SIGKILL cannot be handled. After a SIGKILL, do the steps in
[Clean up](#clean-up).

### The recipe asks for a TPM and the machine has none

The error is `invalid recipe:` with the code `encryption_tpm_unavailable`.

```bash
fisherman probe --json | jq '.tpm'
```

- `"present": false`: the kernel sees no TPM. Enable the TPM (or "PTT",
  "fTPM") in the firmware setup, or use `"type": "luks-passphrase"`.
- `"version": "1.2"`: a TPM 1.2 cannot be used. Use `luks-passphrase`.

### The installed system asks for a passphrase at each boot

With `tpm2-luks` and `tpm2-luks-passphrase`, fisherman does not enrol the TPM
during the install. It stages a unit that enrols the TPM on the first boot.
The first boot always asks for the passphrase or the recovery key. If later
boots also ask, the first-boot enrolment did not run. See issue #360.

### The installed system does not find its root

On an encrypted GRUB install, the kernel argument `rd.luks.name=<UUID>=root`
tells the initrd which container to unlock. fisherman writes it into the
boot entries in the `configure` step. If that failed, the install log has
this `info` event, and the install still ends with `complete` (issue #410):

```bash
jq -R 'fromjson? | select(.type == "info" and (.message | test("LUKS boot args"))) | .message' install.log
```

## After a successful install

The last event is `complete`. Its `boot_id` is the EFI boot entry of the
installed system, when fisherman could find it:

```bash
jq -R 'fromjson? | select(.type == "complete")' install.log
```

After the first boot into the installed system:

```bash
bootc status
```

On an encrypted install, the boot asks for the passphrase. For `tpm2-luks`,
type the recovery key from the `recovery_key` event.

## For integrators

- Read stdout line by line. Skip each line that does not parse as a JSON
  object. Do not parse the whole output as one document.
- Match on `type`, and on `step_id` for steps. Ignore a type or a field that
  you do not know.
- Use `overall_pct` for the progress bar.
- stderr has the fisherman version and the `fisherman: fatal:` line. Keep it
  for bug reports.
- Run `fisherman validate --json --partial -` while the user fills in a page,
  and the full `fisherman validate --json -` before the install:
  ```bash
  fisherman validate --json recipe.json | jq -r '.errors[] | "\(.field): \(.code)"'
  ```
- To cancel, kill the process that you started. See "Cancelling an install"
  in [`JSON-PROGRESS-FORMAT.md`](JSON-PROGRESS-FORMAT.md).

## Report a problem

Collect this:

```bash
fisherman version
fisherman probe --json > probe.json
fisherman validate --json recipe.json > validation.json
lsblk -o NAME,SIZE,FSTYPE,LABEL,MOUNTPOINTS > lsblk.txt
```

Add `install.log` and `install.err`. **Remove the passphrase and the user
password from the recipe before you attach it, and remove the
`recovery_key` event from `install.log`.** Then open an issue at
<https://github.com/tuna-os/fisherman/issues>.
