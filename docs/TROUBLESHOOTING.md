# Troubleshooting Guide

If a fisherman installation fails, this guide helps you find the cause and
recover. The event stream is specified in
[JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md), the recipe rules and
their codes in [VALIDATE.md](VALIDATE.md), and the probe in
[PROBE.md](PROBE.md). Where this guide and those documents differ, they are
right.

## Before the install

### Validate the recipe

```bash
fisherman validate recipe.json            # a summary, or the first problem
fisherman validate --json - < recipe.json # every problem, as JSON
```

`validate` runs the same rules as the install, before any disk is touched,
and needs no root. Use stdin (`-`) when the recipe holds a passphrase or a
password. The JSON is `{"protocol_version", "valid", "errors": [...]}`; each
error has a `field`, a stable `code` and a `message`:

```bash
fisherman validate --json - < recipe.json | jq -r '.errors[] | "\(.code)  \(.field)  \(.message)"'
```

[VALIDATE.md](VALIDATE.md#error-code-reference) gives the cause and the fix
for every code.

### Check the machine

```bash
fisherman probe --json
```

The fields you need most:

| Field | Meaning |
|---|---|
| `disks[]` | the block devices: `path`, `size_bytes`, `size_label`, `model`, `eligible`, and `excluded_reason` for a disk that is not offered (`boot_disk`, `read_only`, `too_small`, `not_disk`) |
| `tpm.usable` | a TPM 2.0 is present; the `tpm2-*` encryption types need it |
| `system.ram_bytes` | MemTotal, in bytes |
| `system.cpu_threads`, `system.cpu_cores` | hardware threads and physical cores |
| `system.uefi` | the machine booted through UEFI |
| `system.meets_requirements`, `system.unmet` | `unmet` lists `ram` (less than 3.8 GB, `probe.MinRAMBytes`), `cpu` (fewer than 2 cores) and `uefi`. Only `uefi` stops the install (`firmware_not_uefi`, which `override` can accept); `ram` and `cpu` are advice for the frontend |
| `live.is_live` | the machine booted from live media, so an empty `image` installs the running image |
| `offline.stores` | the offline image stores fisherman found on the host |

```bash
fisherman probe --json | jq '.disks[] | select(.path == "/dev/sda")'
fisherman probe --json | jq '.system'
```

### Run the install

```bash
sudo fisherman - < recipe.json
```

## During the install: the event stream

Each event is one JSON object on one line of stdout. stdout also carries
lines that are not JSON: the commands fisherman runs (`+ skopeo inspect …`)
and their output. So `fisherman … | jq .` stops at the first such line.
Keep only the events:

```bash
sudo fisherman - < recipe.json | tee install.log | jq -cR 'fromjson? | objects'
```

Messages for people (`fisherman: fatal: …`, the version line) go to stderr.

Every event has a `type`:

| `type` | Meaning |
|---|---|
| `step` | a step starts: `step`, `total_steps`, `step_name`, `step_id`, `overall_pct` |
| `substep` | progress inside a step (`Pulling image: layer 3/12`) |
| `info` | a note: the image cache check, offline stores, live media |
| `source_reuse` | a phase of a `sourceReuse` copy |
| `recovery_key` | the LUKS recovery key of an encrypted install. Show it to the user once; never log it |
| `receipt` | what the install did with the installation receipt |
| `complete` | the install succeeded |
| `error` | the install failed: `message`, `code` (a validate code, or a failure code for the operation that failed), `field`, `step_id`, `disk_changed` and `cleanup` |

### Steps

The steps that run depend on the recipe, so `total_steps` is 5 to 11 and a
step number has no fixed meaning. Match on `step_id`:

| `step_id` | When it runs |
|---|---|
| `prepare_disk` | manual layout (`customMounts`) only, in place of the next four |
| `partition` | auto layout: the old partition table is replaced here |
| `format_efi` | auto layout: the ESP, and the ext4 `/boot` on GRUB layouts |
| `luks` | auto layout with encryption |
| `format_root` | auto layout |
| `mount` | auto layout |
| `format_var` | a `varDisk` that is not kept |
| `install_os` | always: image pull and `bootc install to-filesystem` |
| `tpm2_enroll` | `tpm2-luks` and `tpm2-luks-passphrase`: stages the enrolment for the first boot |
| `flatpaks` | always. It copies nothing when the recipe has `"flatpaks": []` |
| `configure` | always: hostname, user, locale, kernel arguments |
| `finalize` | always: fstrim, remount read-only, fsfreeze and thaw |

**If the install seems to hang,** it is almost always in `install_os`: a
large image on a slow network. `substep` events keep arriving while the
image is pulled. Wait at least 5 minutes before you cancel.

## The install failed

### Read the error

fisherman exits 1 on a failure and 130 on a cancel. The last event is the
`error` event:

```json
{"cleanup":{"status":"ok"},"code":"partition_failed","disk_changed":true,"elapsed_ms":45123,"message":"partitioning disk: exit status 1","step_id":"partition","timestamp":"2026-10-07T19:06:15.234567Z","type":"error"}
```

```bash
jq -cR 'fromjson? | objects | select(.type == "error") | {code, field, step_id, disk_changed, cleanup, message}' install.log
```

`code` names the operation that failed
([failure codes](JSON-PROGRESS-FORMAT.md#failure-codes)), and `disk_changed`
says whether a disk step had started.

The same message is on stderr as `fisherman: fatal: <message>` (or
`fisherman: cancelled: <message>`). The message starts with the stage that
failed:

| Message starts with | Disk changed? | What to do |
|---|---|---|
| `loading recipe:` | no | The recipe is not readable JSON, is empty, or has a key fisherman does not know (`recipe_unknown_field`). Correct the recipe |
| `invalid recipe:` | no | A rule failed; `code` and `field` name it. Run `fisherman validate --json` and see [VALIDATE.md](VALIDATE.md#error-code-reference) |
| `cannot install:` | no | `not_root`: run through `pkexec` or `sudo`. `install_already_running`: another install holds the lock |
| `missing required host tool:` | no | `host_tool_missing`: install the package that the message names |
| `cannot keep the existing filesystem:` | no | `kept_filesystem_unusable`: the device that the recipe keeps has no filesystem that can be mounted |
| `source reuse:` | no | a `source_reuse_*` code; see [SOURCE-REUSE.md](SOURCE-REUSE.md) |
| `required local source is absent or unreadable` | no | the offline image (an `oci:` or `containers-storage:` source, or the live media's image) cannot be read |
| `partitioning disk:`, `formatting EFI:`, `formatting /boot:`, `LUKS format:`, `LUKS open:`, `formatting root filesystem:`, `mounting root:`, `manual disk layout:` | yes | a disk tool failed; the text after the prefix is its error. Check the disk with `lsblk` and the kernel log with `journalctl -k` |
| `bootc install:` | yes | the image could not be pulled or installed. Check the `image`, the network and the registry login (below) |
| `creating user:`, `writing hostname:`, `persistent mounts:`, `finalizing target filesystem:` | yes | a post-install step failed; the OS is on the disk, but not configured. Install again |
| `installation cancelled (SIGTERM)` (or another signal) | maybe | the install was cancelled; see below |

A failure before the first `step` event changes no disk.

### Cleanup

On a failure and on a cancel, fisherman stops its child processes, unmounts
the target and closes the LUKS mapping before it writes the `error` event. You
need to clean up by hand only when `cleanup.status` is `failed` or
`timed_out` (the message then ends with
`; cleanup failed, the target may still be mounted or unlocked: …`):

```bash
# The target mount is /mnt/fisherman-target, or the recipe's targetMount.
sudo umount -R /mnt/fisherman-target
# The mapper is fisherman-root, or the recipe's luksMapperName.
sudo cryptsetup close fisherman-root
```

Then make sure that nothing is left:

```bash
findmnt | grep fisherman      # no output
sudo dmsetup ls | grep fisherman  # no output
```

Scratch space is `/var/fisherman-tmp` on the host. On live media it is a
directory on the target disk, which the teardown removes with the target.

### The install was cancelled (Ctrl+C)

SIGINT, SIGTERM, SIGHUP and the death of the frontend's wrapper all cancel
the install. fisherman tears the target down as above and exits 130 with
`installation cancelled (<signal>)`. If the cancel came after `partition`,
the disk holds a partial install: run the install again.

## Data after a failure

After the `partition` step, the disk's old partition table and its data are
gone. A failure before the first `step` event leaves the disk as it was.

To read an encrypted target after a failure, with the passphrase or the
recovery key:

```bash
lsblk /dev/sdX                                  # find the LUKS partition
sudo cryptsetup open /dev/sdX3 fisherman-root   # the root is partition 3 on GRUB layouts, 2 on systemd-boot
sudo mount /dev/mapper/fisherman-root /mnt
# … copy what you need …
sudo umount /mnt
sudo cryptsetup close fisherman-root
```

A new install to the same disk replaces everything on it.

## Common scenarios

### The image cannot be pulled (`bootc install:`)

- The `image` reference is wrong, or the registry needs a login.
- The network is down.

```bash
skopeo inspect docker://<image>   # what fisherman asks first
podman pull <image>
podman login <registry>           # if the registry needs it
```

### The disk is too small

The install refuses a disk smaller than 50 GiB (`disk_too_small`) unless the
recipe has `"override": ["too_small"]`. If `bootc install` then runs out of
space, the image is too large for the disk: use a larger disk or a smaller
image. `"flatpaks": []` makes the `flatpaks` step copy nothing, which saves
the space of the host's Flatpaks.

### The recipe asks for a TPM and the machine has none

`encryption_tpm_unavailable` means that `fisherman probe --json` reports
`tpm.usable: false`. The probe reads `/sys/class/tpm/tpm0/tpm_version_major`
and `/dev/tpmrm0`:

```bash
cat /sys/class/tpm/tpm0/tpm_version_major   # 2 for TPM 2.0
ls -l /dev/tpmrm0
```

Turn on the TPM in the firmware settings, or use `luks-passphrase`.

The TPM enrolment runs on the first boot of the installed system, not in the
installer. If the disk still asks for the passphrase after the first boot,
read the unit's log on the installed system:

```bash
journalctl -b -u fisherman-tpm2-enroll.service
```

## After the install

On the installed system:

```bash
bootc status                 # the deployed image
fisherman verify --json      # the installation receipt against the booted image (docs/RECEIPT.md)
bootctl status               # systemd-boot images
df -h /
```

For an encrypted install, reboot and check that the passphrase prompt
appears and that the passphrase unlocks the disk.

## For integrators

- Read the events with `jq -cR 'fromjson? | objects'`, never with `jq .`.
- Key on `type`, `step_id` and `code`. `step_name` and `message` are English
  and can change.
- Treat a stream that ends without `complete` or `error` as a failure; the
  exit code tells the cases apart
  ([JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md#exit-codes)).

```bash
# The codes of every problem in a recipe
fisherman validate --json - < recipe.json | jq -r '.errors[].code' | sort -u
```

## Getting help

1. Save the install's stdout while it runs (`fisherman - < recipe.json | tee
   progress.ndjson`). That log holds the recovery key: keep it private.
2. Write a support bundle. It holds allowlisted facts only, never a
   passphrase, recovery key, password or token, and it masks host, user and
   device names ([DIAGNOSTICS.md](DIAGNOSTICS.md)):
   ```bash
   fisherman diagnostics --stdout --recipe recipe.json --progress-log progress.ndjson --probe | less
   fisherman diagnostics --output-dir . --recipe recipe.json --progress-log progress.ndjson --probe
   ```
3. Read the bundle. Open an issue with it, the steps to reproduce, and
   anything else you choose to add. Do not attach the recipe or the
   progress log themselves: they hold secrets.
