# `fisherman probe --json`

`fisherman probe --json` prints, as **one JSON object on stdout**, the facts an
installer frontend needs before it shows its first page: which disks may be
installed to, whether a TPM 2.0 is usable, whether the machine meets the
minimum requirements, whether it booted from live media, which offline
image stores that media carries, what the host's bootc can do, and which GPUs
and machine model the installer runs on.

- Exit `0`: stdout holds exactly one JSON object.
- Exit `1`: the probe failed (lsblk failed, `/proc/meminfo` unreadable). The
  reason is on stderr; stdout is empty.
- Exit `2`: bad arguments. `--json` is required; it is the only output format.
  An invalid `--source` or `--target-imgref` is refused without echoing it.
- **No root.** Run it as the desktop user.
- **Read-only.** It writes nothing: no files, no mounts, no lock files. The
  one exception is the opt-in `--bootc-image-list`; see
  [bootc store inventory](#bootc-store-inventory).

Each frontend used to compute these facts itself, and the copies drifted
(see "Why" below). The logic now lives here; a frontend only renders it.

```text
fisherman probe --json [--source <ref> | --live-root] [--target-imgref <ref>] [--bootc-image-list]
```

`--source`, `--live-root` and `--target-imgref` scope the `capabilities`
object to the source the user selected; see [Capabilities](#capabilities).
Every other section is the same with or without them. `--bootc-image-list`
adds `bootc image list` to the `bootc_store` inventory, under the conditions
in [bootc store inventory](#bootc-store-inventory).

## Running it from a frontend

Run it as the user, never through `pkexec`/`sudo`. Inside a Flatpak, run the
bundled fisherman directly in the sandbox: it detects the sandbox
(`runner.InFlatpak`) and forwards each host command (`lsblk`, `bootc`, `test`,
`cat`) through `flatpak-spawn --host`. lsblk therefore reports the host's
mounts, which the boot-disk rule needs.

## Schema (`protocol_version` 1)

```text
{
  protocol_version  int      schema version; see "Compatibility"
  disks[]                    every top-level block device lsblk reports, in lsblk order
    path            string   device node, e.g. "/dev/nvme0n1"
    size_bytes      int      size in bytes
    size_label      string   size in binary units, e.g. "476.9 GiB"; see "Size labels"
    model           string   lsblk MODEL, whitespace collapsed; "" when unknown
    vendor          string   lsblk VENDOR, whitespace collapsed; "" when unknown (raw: may be a PCI id like "0x1af4")
    transport       string   lsblk TRAN as reported: "nvme", "sata", "usb", ...; "" when unknown
    transport_label string   human bus name: "NVMe", "SATA", "USB", ...; "" when unknown
    removable       bool     RM or HOTPLUG: a USB stick, card or external drive
    read_only       bool     the kernel reports the device read-only
    eligible        bool     may be offered as an install target
    excluded_reason string   present only when eligible is false; see "Exclusion rules"
  tpm
    present         bool     the kernel exposes a TPM of any version
    version         string   "2.0", "1.2", or "" when absent or unknown
    usable          bool     a TPM 2.0 is present, so tpm2-luks can enrol
  system
    ram_bytes       int      MemTotal from /proc/meminfo (what `free -b` calls total)
    cpu_threads     int      online logical CPUs
    cpu_cores       int      distinct physical cores among them (= cpu_threads when the kernel exposes no topology)
    uefi            bool     booted through UEFI firmware
    meets_requirements bool  unmet is empty
    unmet[]         string   any of "ram", "cpu", "uefi", in that order; [] when none
  live
    is_live         bool     booted from live media
    live_image      string   the booted bootc image ref when live; "" otherwise or when bootc cannot say
    detected_by     string   present only when live: the signal that said so; see "Live detection"
  offline
    stores[]        string   host paths of offline containers-storage roots that exist; [] when none
    images[]        string   image names found across those stores, de-duplicated; [] when none
  bootc                      one bootc build; see "bootc"
    context         string   which bootc this is: "live-host"
    found           bool     a bootc command exists in that context
    version         string   the version bootc reports, e.g. "1.17.1"; "" when it cannot be read
    version_reason  string   present only when version is "": "bootc_missing", "bootc_failed" or "malformed_output"
    capabilities             object: capability name -> answer; every known name is always present
      <name>
        status      string   "supported", "unsupported" or "unknown"
        reason      string   a stable reason code; see "bootc"
  gpus[]                     PCI display controllers of this machine, in PCI address order; [] when none
    vendor_id       string   PCI vendor id, lower-case hex: "0x10de"
    device_id       string   PCI device id: "0x2684"
    class           string   24-bit PCI class code: "0x030000" VGA, "0x030200" 3D, "0x038000" other display
  dmi                        the firmware's name for the machine model; see "Hardware facts"
    vendor          string   DMI sys_vendor, whitespace collapsed; "" when unknown
    product         string   DMI product_name; "" when unknown
  capabilities               the selected source's bootc capabilities; see "Capabilities"
  bootc_store                the live host's bootc-owned image storage; see "bootc store inventory"
}
```

`bootc`, `gpus`, `dmi`, `capabilities` and `bootc_store` were added after `protocol_version`
1 shipped. They are additions, so the version did not change; a fisherman
that predates them simply omits the keys.

Arrays are always arrays, never `null`.

### Exclusion rules

`disks[]` lists every device so a frontend can explain why one is missing.
Offer only `eligible: true`. The first rule that applies is reported:

| `excluded_reason` | Rule | Why |
|---|---|---|
| `loop` | TYPE `loop` or name `loop*` | an image file, not a disk; a live ISO's squashfs is one |
| `zram` | name `zram*` | compressed RAM swap |
| `ram` | name `ram*` | RAM disk |
| `optical` | TYPE `rom` or name `sr*` | a DVD drive |
| `device_mapper` | name `dm-*`, TYPE `dm`, `lvm` or `crypt` | a mapping over another disk, not a disk |
| `not_disk` | any other TYPE than `disk` | |
| `boot_disk` | the disk, or anything stacked on it (partition, LUKS, LVM), is mounted at `/`, `/sysroot`, `/boot`, `/boot/efi`, `/efi`, `/usr`, `/var`, `/run/initramfs/live`, `/run/initramfs/isoscan` or `/run/media/iso` | the running system, installed or live, is on it. Covers the live USB (dracut mounts it at `/run/initramfs/live`) and an installed bootc host (its root is at `/sysroot`) |
| `read_only` | lsblk RO | the install would fail at partitioning |
| `too_small` | `size_bytes` < 50 GiB (`MinDiskBytes`, 53 687 091 200 bytes) | GNOME's `min_disk_size` (51200 MiB), the only minimum any frontend enforced |

**Removable disks are eligible, and flagged.** GNOME hid removable disks
unless no fixed disk existed. That stood in for "do not offer the live USB",
which the `boot_disk` rule now answers directly, and it also hid legitimate
targets: an external USB SSD for a portable install, or the only disk on a
machine whose internal storage is eMMC behind a removable-flagged reader. The
other four frontends offered removable disks. `removable` lets a frontend sort
them last or warn before erasing one.

### Size labels

Binary units, labelled honestly: 1 GiB = 2^30 bytes, steps of 1024 through
B, KiB, MiB, GiB, TiB, PiB, one decimal place dropped when zero ("50 GiB",
"476.9 GiB", "1.8 TiB"). Binary because the minimum is defined in binary
(50 GiB) and GNOME computed in binary already; the label says GiB because
GNOME's said "GB" for a binary number. A "512 GB" SSD therefore reads
"476.9 GiB". `size_bytes` is there for a frontend that prefers SI.

### TPM

Follows bootc-installer's `shared/tpm/README.md` contract exactly; `usable`
is that contract's answer. The probe reads
`/sys/class/tpm/tpm0/tpm_version_major` (`2` is TPM 2.0, `1` is TPM 1.2), and
on kernels older than 5.5, which lack that file, falls back to `/dev/tpmrm0`,
which exists only for a 2.0 device. `/sys/class/tpm/tpm0` alone means a TPM of
unknown version: `present` but not `usable`. The four contract fixtures are
copied into `fisherman/internal/probe/testdata/tpm/` and tested.
`BOOTC_INSTALLER_FAKE_TPM` (non-empty, not `0`) forces `usable` to true, never
to false, for screenshots; `present` and `version` stay truthful.

### Requirements

Thresholds come from the GNOME frontend (`bootc_installer/core/system.py`),
the only one that checked:

| `unmet` | Constant | Threshold |
|---|---|---|
| `ram` | `MinRAMBytes` | 3 800 000 000 bytes of MemTotal. Decimal on purpose: a 4 GB machine reports under 4 GiB once firmware and kernel take their share, and must pass |
| `cpu` | `MinCPUCores` | 2 physical cores (GNOME multiplied lscpu cores-per-socket by sockets). Cores are counted from `/sys/devices/system/cpu/cpu*/topology` |
| `uefi` | | `/sys/firmware/efi` exists. A Flatpak sandbox has no `/sys/firmware`, so there the host is asked (GNOME assumed UEFI in a Flatpak) |

Overrides such as GNOME's `IGNORE_RAM`/`IGNORE_CPU` are frontend policy and
stay in the frontend.

### Live detection

Live when any of these holds, checked in order; `detected_by` names it:

1. `/run/ostree-live` exists on the host (ostree live images);
2. the kernel command line has `rd.live.image` or a `root=live:` source
   (dracut dmsquash-live), reported as `cmdline:rd.live.image` or
   `cmdline:root=live:`;
3. `/etc/bootc-installer/live-iso-mode` exists on the host (an explicit
   opt-in an ISO builder can ship; the GNOME frontend's convention).

`/run/ostree-booted` is **not** a signal: every installed ostree or bootc
host has it. When live, `live_image` is `.status.booted.image.image.image`
from `bootc status --json`, the field every frontend read; empty when bootc
fails. A non-empty `live_image` means a recipe may omit `image`.

The install and `fisherman validate` use this same detection: a recipe with
an empty `image` is rejected (`invalid recipe: image is required: …`) unless
`is_live` would be true.

### Offline stores

Candidates, in order, de-duplicated; only existing directories are kept:

1. `$TUNA_OFFLINE_STORES`, colon-separated;
2. lines of the host's `/etc/tuna-installer/offline-stores` (`#` comments);
3. `/usr/share/tuna-installer/oci-store`;
4. `/var/lib/superiso-store` (`install.LiveMediaImageStore`, the store every
   install already exposes to bootc when present).

Image names come from each store's `overlay-images/images.json` (or
`vfs-images/images.json`), read directly. The frontends ran
`podman images --root <store>`, which needs podman on the host and takes the
store's lock; reading the index needs neither and writes nothing.

An install whose recipe omits `additionalImageStores` exposes exactly
`stores` to bootc, so a frontend no longer needs to pass them; a recipe that
sets the field (even to `[]`) is used as given.

All of these are **host** paths. Inside a Flatpak, `/etc` and `/usr` are the
runtime's, so the host's are read under `/run/host/etc` and `/run/host/usr`
(as bootc-installer's `recipe.py` does), and any other host path the sandbox
cannot see is asked of the host (`flatpak-spawn --host test -d` / `cat`).

### bootc

`bootc` reports the bootc of **one context**, named in `context`. The only
context today is `live-host`: the bootc command of the machine that runs the
installer. That is not the bootc that performs the install. fisherman runs
the selected image's own bootc, which can be older or newer, so:

- do not read a `live-host` answer as a fact about the image the user picked;
- do not compare `version` with a number to decide what to offer. `version`
  is for display and for bug reports. Branch on `capabilities` only;
- treat `unknown` as "do not offer it", never as "probably fine";
- ignore capability names and reason codes you do not know, and treat an
  unknown `status` as `unknown`.

The selected source image is answered by the separate `capabilities` object
below, not by a further value of `context` here. Read this object for the
live host only.

| Capability | What it is | How `live-host` answers |
|---|---|---|
| `container_inspect` | `bootc container inspect`, bootc's own image inspection | `bootc container inspect --help` |
| `install_print_configuration` | `bootc install print-configuration`, the merged install defaults | `bootc install print-configuration --help` |
| `install_finalize` | `bootc install finalize` | `bootc install finalize --help` |
| `deployment_mount` | bootc mounts the new deployment itself after the install (the "deployment mounting" of #383) | not established |
| `unified_storage` | one image store shared by bootc and podman | not established |
| `bootc_image_storage` | bootc's own image store on the target | not established |

| `status` | `reason` | Meaning |
|---|---|---|
| `supported` | `command_present` | the subcommand's `--help` exited 0. The command exists; this does not say that an install profile that uses it is qualified |
| `unsupported` | `command_absent` | bootc ran and rejected the subcommand (exit status 2) |
| `unsupported` | `bootc_missing` | no bootc command in this context. `found` is false |
| `unknown` | `bootc_failed` | bootc exists but `bootc --version` failed |
| `unknown` | `check_failed` | the check for this one capability failed for an unrelated reason |
| `unknown` | `not_established` | nothing the probe may run in this context answers it; it depends on the selected image |

A reason code is never renamed or reused. New codes and new capability names
can be added.

The probe runs `bootc --version` and the three `--help` calls above, and only
those: no image is pulled or inspected, no store is opened, nothing is
mounted. Only a string of the form `bootc <version>` is copied into
`version`; any other output gives `""` with `version_reason:
"malformed_output"`. bootc's own output and error text never reach the JSON.
A host without bootc is an ordinary answer, not a probe failure.

Fixtures: `fisherman/internal/probe/testdata/golden/bootc-*.json` pin the
object for a current bootc, an older one, a missing command, malformed
version output, a bootc that fails, and a single failed check.

### Capabilities

`capabilities` is the source-scoped bootc report of tuna-os/fisherman#383,
for tuna-os/bootc-installer#266. It answers one question: what can the
**selected source's own bootc** do, and how does fisherman know? It has its
own `version`, so a frontend can gate its capability UI on it.

#### Selecting the source

| Flag | Meaning |
|---|---|
| `--source <ref>` | the recipe's `image`: `containers-storage:<name>`, `oci:<path>[:<tag>]`, `docker://<name>` or a bare registry name |
| `--live-root` | the recipe omits `image`: the install runs the live host's bootc, which installs the image the host booted. Excludes `--source` |
| `--target-imgref <ref>` | the recipe's `targetImgref`, the update reference. Optional |

The flags take image references, not a recipe. A recipe on stdin was the
other choice; it was rejected because a recipe holds the LUKS passphrase and
the user password, and the probe needs neither. A reference must match
`[A-Za-z0-9][A-Za-z0-9._:/@+=-]{0,511}` and contain no `..`; anything else
exits 2, and the message does not repeat the value. Without either source
flag, `capabilities` is still present and every source feature is
`unknown` with `no_source_selected`.

Run the probe again when the selection changes, and drop an older reply: a
reply belongs to the `source.ref` and `target.update_ref` it names.

#### Schema (`capabilities.version` 1)

```text
capabilities
  version           int      1; bumped only when a field is removed, renamed or changes meaning
  live_host                  the machine that runs the installer
    context         string   "live-host"
    is_live         bool     the same answer as live.is_live
    image           string   the ref the host booted ("" when it booted no bootc image)
    digest          string   the manifest digest the host booted, "sha256:<64 hex>"; "" when unknown
    identity_reason string   present only when digest is "": "not_bootc_booted" or "digest_missing"
    platform        string   the architecture bootc status reports, in the kernel's spelling ("x86_64": bootc says "amd64"); "" when unknown
    backend         string   "ostree", "composefs", or "" when bootc does not say
    bootc_version   string   the same as bootc.version
  source                     the selected installation source
    selected        bool     a source flag was given
    ref             string   the reference as given (with --live-root: the host's booted ref)
    transport       string   "containers-storage", "oci", "registry", "live-root"; "" when none
    locality        string   "local", "remote" or "unknown"
    store           string   the containers-storage root the digest was read from; "" otherwise
    digest          string   the source's manifest digest; "" when not known offline
    digest_from     string   present only with a digest: "store_index", "oci_index", "reference", "live_root"
    digest_reason   string   present only without one; see the table below
    platform        string   "os/arch[/variant]" from an OCI index; "" when unknown
  execution_root             the root whose bootc the probe ran for the source
    context         string   "live-host" when established, otherwise "none"
    established     bool     the probe proved this root is the source's
    reason          string   why; see the table below
    backend         string   the root's backend when established, otherwise ""
    bootc_version   string   that root's bootc version when established, otherwise ""
  target                     the installed system; it does not exist during a probe
    update_ref      string   the reference the installed system will follow; "" when unknown
    update_ref_from string   "target_imgref", "source" (the source ref without its transport), "live_image" or ""
    state           string   "not_installed"
  features                   feature name -> answer; every name below is always present
    <name>
      status        string   "supported", "unsupported" or "unknown"
      reason        string   a stable reason code; see the table below
      context       string   "source-root", "fisherman" or "none": where the answer was established
  install_defaults           what the source root's `bootc install print-configuration` reports
    observed        bool     the command ran there and its output was read
    root_filesystem string   root-fs-type (or filesystem.root.type); "" when not stated
    block_setups[]  string   block, e.g. ["direct", "tpm2-luks"]; [] when not stated
    bootloader      string   bootloader when stated; "" otherwise
  image                      what the source root's `bootc container inspect --json` reports
    observed        bool     the command ran there and its output was read
    kernel_version  string   kernel.version; "" when not stated
    kernel_unified  bool     kernel.unified (a UKI); false when not stated
}
```

`install_defaults` are bootc's documented merged defaults, not a disk plan:
fisherman's own partition layout and recipe rules still decide the install.
Read them only when `observed` is true.

#### The execution root

fisherman never runs the source's bootc to answer a probe: that needs a
pull, a mount or a container, and the probe does none of those. It runs a
bootc only when it can prove that bootc **is** the source's: the live
host's, when the host booted the selected image.

| `execution_root.reason` | `established` | Meaning |
|---|---|---|
| `digest_match` | true | the source's manifest digest equals the digest the host booted (`bootc status --json`) |
| `live_root` | true | `--live-root`, and the host booted a bootc image: bootc with no source installs the root it runs in |
| `no_source_selected` | false | no source flag |
| `host_not_bootc` | false | no bootc on the host, `bootc status` failed, or the host booted no bootc image |
| `host_digest_unknown` | false | the host booted an image whose digest bootc did not report |
| `source_digest_unknown` | false | the source's digest is not known offline; `source.digest_reason` says why |
| `digest_mismatch` | false | both digests are known and differ: the host runs an unrelated image |

A matching name, a matching version string or the same distribution is never
enough: only the digest is. On a host whose image does not match, the host's
own install configuration is never reported for the source.

| `source.digest_reason` | Meaning |
|---|---|
| `remote_metadata_unavailable` | a registry source. The probe contacts no registry, so the digest stays unknown until the install pulls or inspects it |
| `not_found_locally` | no readable store has the image. Root's `/var/lib/containers/storage` is usually unreadable for the desktop user, so this does not prove the image is absent |
| `ambiguous_local_copies` | readable stores hold different images under that name |
| `oci_layout_unreadable` | the layout's `index.json` is missing, unreadable or malformed, or does not single out one manifest (by `org.opencontainers.image.ref.name`, or the only one) |
| `not_bootc_booted`, `digest_missing` | `--live-root` on a host that booted no bootc image, or one without a digest |

Stores searched for a `containers-storage:` source are `offline.stores`,
then `/var/lib/containers/storage`; a name without a tag also matches
`<name>:latest`. A store entry matches the host through any digest it
records (the manifest digest, or the list digest it was pulled by).

#### Features

| Feature | What it is | How it is answered in the source root |
|---|---|---|
| `native_inspection` | `bootc container inspect`, bootc's own image inspection | runs `bootc container inspect --json` |
| `install_configuration` | `bootc install print-configuration`, the merged install defaults | runs `bootc install print-configuration` |
| `finalization` | `bootc install finalize` | `bootc install finalize --help`: absence is proof; presence is not |
| `deployment_mount` | bootc mounts the new deployment itself after the install | never answered read-only |
| `bootc_image_storage` | bootc's own image store on the target (bound images) | never answered read-only |
| `unified_storage` | bootc's `--experimental-unified-storage` | fisherman's policy, the same for every source |

| `status` | `reason` | `context` | Meaning |
|---|---|---|---|
| `supported` | `observed` | `source-root` | the command ran in the established source root and the adapter read its output. The only way to `supported` |
| `unsupported` | `command_absent` | `source-root` | the source root's bootc rejected the subcommand (exit status 2) |
| `unsupported` | `fisherman_refuses` | `fisherman` | fisherman does not implement it, and `fisherman validate` refuses a recipe that asks for it (`unified_storage` ↔ `unified_storage_unsupported`, docs/VALIDATE.md) |
| `unknown` | `not_qualified` | `source-root` | the source root has the command, but only an install of this source on a test machine shows it works with fisherman's install profile. Help text and versions are not that proof |
| `unknown` | `malformed_output` | `source-root` | the command ran, but its output is not the JSON the adapter reads (not an object, or a known key with the wrong type or an unexpected value) |
| `unknown` | `check_failed` | `source-root` | the command failed for another reason: an error, a timeout, too much output |
| `unknown` | `source_root_unavailable` | `none` | no root the probe may run is the source's; `execution_root.reason` says why |
| `unknown` | `no_source_selected` | `none` | no source flag |

Rules for a frontend:

- branch on `status` and `reason`, never on `bootc_version` or a version
  number: no feature follows from a version;
- `unknown` means "do not offer it", never "probably fine";
- ignore feature names, reason codes and fields you do not know; treat an
  unknown `status` as `unknown`;
- a feature a recipe asks for that is `unsupported` is refused by
  `fisherman validate`, and by the install before any disk is touched. Today that is
  `unifiedStorage`; a later feature gets a validation code when a recipe
  field starts asking for it.

#### Adapters, limits and secrets

bootc's JSON is read by adapters that copy out the fields in the schema and
nothing else. Unknown keys are ignored, so a newer bootc does not change the
contract, and only values that match a short token pattern (a filesystem,
bootloader or block-setup name, a kernel release, a `sha256:` digest) are
copied. Command output, error text, kernel arguments and store metadata never
reach the JSON or stderr. Every command the probe runs is stopped after 20
seconds and refused past 8 MiB of output (`runner.BoundedOutput`), and its
stderr is discarded.

The probe pulls nothing, mounts nothing, starts no container, takes no store
lock and contacts no registry. Store indexes and OCI `index.json` files are
read as files. It runs `bootc status --json` once, and the three source-root
commands above only when the execution root is established.

`bootc` here is the first `bootc` on the desktop user's `PATH` (forwarded to
the host in a Flatpak). The install runs it as root through `pkexec`, which
resets `PATH`; on an image-based host both resolve to `/usr/bin/bootc`.

#### What only an install can prove

`deployment_mount` and `bootc_image_storage` are `unknown` with
`not_qualified` in every case, and `finalization` never becomes `supported`:
nothing read-only can show they work with fisherman's install profile. A VM
install of the source on the matrix in `tests/bootcrew-matrix.yaml` is the
proof; until a qualified profile exists for a source, they stay `unknown`. The
`bootc_store` inventory does not change this: it lists what the live host
stores, not what the source's bootc can do.

Fixtures: `fisherman/internal/probe/testdata/golden/caps-*.json` pin the
object for no source, a matching OSTree root with a current bootc, a
matching composefs root, an older bootc, an unrelated live host with a
conflicting configuration, a registry source, a digest-pinned reference, a
host without bootc, malformed output, unknown fields, wrongly typed fields, a
failed check, a local OCI layout, a source not found locally, ambiguous
local copies, and `--live-root`.

### bootc store inventory

`bootc_store` is the inventory of tuna-os/fisherman#384, phase 1, for
tuna-os/bootc-installer#267. It lists the images that the **live host's**
bootc keeps in its own storage, with the identity of each. It has its own
`version`. It describes the machine that runs the installer, never the
selected source or the installed target: `context` is always `live-host`.

The inventory exists only when the capability report found a bootc-booted
host (`capabilities.live_host.identity_reason` is not `not_bootc_booted`).
Otherwise `available` is `false`, `reason` says why, and `items` is `[]`.

#### Schema (`bootc_store.version` 1)

```text
bootc_store
  version           int      1; bumped only when a field is removed, renamed or changes meaning
  context           string   "live-host"
  available         bool     the host is bootc-booted and the items describe its storage
  reason            string   "observed", "not_bootc_booted", "bootc_missing", "check_failed" or "malformed_output"
  machine_platform  string   this machine's architecture in bootc's spelling ("x86_64", "aarch64")
  sources                    one status per input; see the table below
    bootc_status    string   the same value as reason
    store_index     string   bootc's own containers-storage index, read as a file
    image_list      string   `bootc image list --type host --format json`
  ignored_entries   int      image list entries that are not host inventory (logically bound images, unknown types)
  items[]                    [] when none
    ref             string   the reference without a transport; "" when ref_reason is set
    ref_reason      string   present only when ref is "": "credentials_redacted" or "invalid_reference"
    owner           string   "bootc": the storage belongs to the host's bootc, not to podman
    context         string   "live-host"
    storage         string   "ostree", "composefs", "bootc-containers-storage" or "unknown"
    roles[]         string   "booted", "staged", "rollback", "other_deployment", "stored", "listed_host", "listed_unified"
    availability    string   "deployed", "stored" or "listed"; see below
    digest          string   the manifest digest, "sha256:<64 hex>": the immutable identity; "" when unknown
    digest_from     string   present only with a digest: "bootc_status" or "bootc_store_index"
    digest_reason   string   present only without one: "digest_missing", "malformed", "ambiguous" or "not_reported"
    platform        string   the architecture bootc reports, in the kernel's spelling ("x86_64": bootc says "amd64"); "" when unknown
    platform_reason string   present only without one: "not_reported" or "malformed"
    matches_machine bool     ref, digest and platform are known, and platform is machine_platform
    mismatch_reason string   present only when false: "ref_unavailable", "identity_unknown", "platform_unknown" or "platform_mismatch"
}
```

| `availability` | Source | Meaning |
|---|---|---|
| `deployed` | `bootc status --json` (booted, staged, rollback, otherDeployments) | a boot entry uses the image; its content is in the host's ostree or composefs repository |
| `stored` | `/sysroot/ostree/bootc/storage/overlay-images/images.json` | bootc's own containers-storage holds the image: a logically bound image, or a host image with unified storage |
| `listed` | `bootc image list` only | bootc named the image. Nothing confirms its identity: bootc 1.17.1 prints names only |

`matches_machine` is a fact about identity. It does not mean that fisherman
can install from the item: no install path reads bootc-owned storage yet
(see [Reuse is not implemented](#reuse-is-not-implemented)). An item is
merged with another only when both come from the same store: a `unified`
list entry joins the store-index entry of the same name. A name that a boot
entry shares with a listed host image is not proof of the same image (the
ostree ref can move after a deployment), so they stay separate items.

| Source status | Meaning |
|---|---|
| `observed` | read |
| `not_applicable` | the host is not bootc-booted; the source was not consulted |
| `absent` | the store does not exist |
| `inaccessible` | the store exists, but the probe cannot read its index. It belongs to root, and the probe runs as the desktop user, so this is the usual answer |
| `not_requested` | `--bootc-image-list` was not given (the default) |
| `live_media` | asked for on live media: bootc refuses `image list` on a read-only sysroot, and the live store is not the target's |
| `backend_not_qualified` | asked for on a host whose backend is not ostree; see below |
| `needs_root` | asked for, but the probe is not root: bootc refuses `image list` without root, so fisherman does not run it |
| `command_absent` | bootc rejected the subcommand or a flag (exit 2): an older bootc |
| `check_failed` | the command failed for another reason (a timeout) |
| `malformed_output` | the output is not the JSON the adapter reads; nothing of it is used |

#### Why `bootc image list` is opt-in

The default probe makes no writes and no network access. It reads the
`bootc status --json` that the capability report already ran, and it reads
the store index as a file. It runs no extra command.

`bootc image list` is not read-only in bootc 1.17.1
([`image.rs`](https://github.com/bootc-dev/bootc/blob/v1.17.1/crates/lib/src/image.rs),
[`cli.rs`](https://github.com/bootc-dev/bootc/blob/v1.17.1/crates/lib/src/cli.rs)).
On a booted host, every `--type` calls `get_storage()`, which:

- requires root and `CAP_SYS_ADMIN`, and enters a private mount namespace;
- on composefs, mounts the ESP read-write and checks the boot entries
  (`prepend_custom_prefix`, which can rewrite them);
- when bootc's store exists, creates `/run/bootc/storage` and runs
  `podman images` against the store, which takes the store's locks.

So fisherman runs it only with `--bootc-image-list`, only as root, only on
an installed (not live) host whose booted backend is `ostree`, and only as
`bootc image list --type host --format json`. A frontend runs the probe as
the desktop user and gets `needs_root`; that is the expected answer. Even
when it runs, the list adds names only, never an identity.

`bootc image set-unified`, `pull-from-default-storage`, `image cmd` and
`copy-to-storage` are never run by the probe.

#### Adapters and secrets

The adapters copy `image_type` and `image` from the list, `names` and
`digest` from the store index, and `image.image.image`, `imageDigest` and
`architecture` from each boot entry. Other keys, signatures, ostree remotes
and store metadata are ignored. A reference loses its ostree-ext scheme
(`ostree-unverified-registry:` and the like) and its transport. A reference
with user information in the registry part (`user:token@registry/...`) is
replaced by `""` with `credentials_redacted`; one that does not match the
`--source` pattern gets `invalid_reference`. Digests must be `sha256:<64
hex>` and architectures a short token. The list is capped at 1024 entries,
the inventory at 256 items and the boot entries at 16.

#### Reuse

Phase 2 of #384, installing from bootc-owned storage through `bootc image
copy-to-storage`, is the recipe's explicit, experimental `sourceReuse`
([SOURCE-REUSE.md](SOURCE-REUSE.md)). The probe still never runs it, and
nothing here changes: an item a recipe can name is one with `storage`
`"ostree"`, `availability` `"deployed"` and `matches_machine` `true`, on a
host whose `bootc_store.available` is true and which is not live media.
`bootc_image_storage` stays `unknown`/`not_qualified`: it is about the
target's store, and the reuse is qualified only by an advisory VM profile.

Fixtures: `fisherman/internal/probe/testdata/golden/store-*.json` pin the
object for non-bootc live media, a host without bootc, a failed and a
malformed `bootc status`, an installed ostree host with and without the
list, a list of logically bound images only, an inaccessible store, an
older bootc, a failed list, malformed list and index output, wrong
architectures (of an entry, and of the machine), missing and corrupt
identities, a composefs host, a probe that is not root, bootc live media,
and references with credentials. `TestBootcStoreDefaultProbeIsReadOnly`
checks that the default probe of a bootc host leaves its root unchanged and
runs no command beyond the read-only set.

### Hardware facts

`gpus` and `dmi` are **facts only**. The probe does not decide which image
fits them, and nothing in fisherman changes an image, a kernel argument or a
driver because of them. Rules that map hardware to images are separate,
versioned data (tuna-os/fisherman#373) and must match PCI ids, not brand
names: for example, "NVIDIA" alone does not say whether the open kernel
modules support the card.

They describe **the machine that runs the installer**. The disk can be moved
to another machine after the install, so present anything derived from them
as advice that the user can override.

`gpus` comes from `/sys/bus/pci/devices/*/{class,vendor,device}`: every
device whose PCI base class is `0x03` (display controller). A device with a
missing or malformed attribute is left out. A machine without a PCI bus has
`[]`.

`dmi` comes from `/sys/class/dmi/id/sys_vendor` and `product_name`. Both are
the same for every unit of a model. Control characters are removed,
whitespace is collapsed, and a value is cut at 128 characters. No DMI, an
unreadable file or an empty value gives `""`. On some vendors' machines
`product_name` is a model code, not a marketing name.

The probe never reads the serial numbers, asset tags or the machine UUID that
sit in the same directory, and a test checks that.

## Example

The output for a laptop booted from a live USB, pinned by
`fisherman/internal/probe/testdata/golden/probe.json` (regenerate with
`go test ./internal/probe -update`; a test checks this page quotes it):

```json
{
  "protocol_version": 1,
  "disks": [
    {
      "path": "/dev/loop0",
      "size_bytes": 4294967296,
      "size_label": "4 GiB",
      "model": "",
      "vendor": "",
      "transport": "",
      "transport_label": "",
      "removable": false,
      "read_only": true,
      "eligible": false,
      "excluded_reason": "loop"
    },
    {
      "path": "/dev/sda",
      "size_bytes": 500107862016,
      "size_label": "465.8 GiB",
      "model": "Samsung SSD 870 EVO 500GB",
      "vendor": "ATA",
      "transport": "sata",
      "transport_label": "SATA",
      "removable": false,
      "read_only": false,
      "eligible": true
    },
    {
      "path": "/dev/sdb",
      "size_bytes": 15931539456,
      "size_label": "14.8 GiB",
      "model": "Ultra Fit",
      "vendor": "SanDisk",
      "transport": "usb",
      "transport_label": "USB",
      "removable": true,
      "read_only": false,
      "eligible": false,
      "excluded_reason": "boot_disk"
    },
    {
      "path": "/dev/sdc",
      "size_bytes": 1000204886016,
      "size_label": "931.5 GiB",
      "model": "Extreme SSD",
      "vendor": "SanDisk",
      "transport": "usb",
      "transport_label": "USB",
      "removable": true,
      "read_only": false,
      "eligible": true
    },
    {
      "path": "/dev/sdd",
      "size_bytes": 64023257088,
      "size_label": "59.6 GiB",
      "model": "Cruzer Blade",
      "vendor": "SanDisk",
      "transport": "usb",
      "transport_label": "USB",
      "removable": true,
      "read_only": false,
      "eligible": true
    },
    {
      "path": "/dev/sr0",
      "size_bytes": 2147483648,
      "size_label": "2 GiB",
      "model": "DVD-RW DRW-24F1ST",
      "vendor": "ASUS",
      "transport": "sata",
      "transport_label": "SATA",
      "removable": true,
      "read_only": false,
      "eligible": false,
      "excluded_reason": "optical"
    },
    {
      "path": "/dev/zram0",
      "size_bytes": 8589934592,
      "size_label": "8 GiB",
      "model": "",
      "vendor": "",
      "transport": "",
      "transport_label": "",
      "removable": false,
      "read_only": false,
      "eligible": false,
      "excluded_reason": "zram"
    },
    {
      "path": "/dev/ram0",
      "size_bytes": 67108864,
      "size_label": "64 MiB",
      "model": "",
      "vendor": "",
      "transport": "",
      "transport_label": "",
      "removable": false,
      "read_only": false,
      "eligible": false,
      "excluded_reason": "ram"
    },
    {
      "path": "/dev/mapper/vg-data",
      "size_bytes": 107374182400,
      "size_label": "100 GiB",
      "model": "",
      "vendor": "",
      "transport": "",
      "transport_label": "",
      "removable": false,
      "read_only": false,
      "eligible": false,
      "excluded_reason": "device_mapper"
    },
    {
      "path": "/dev/nvme0n1",
      "size_bytes": 1024209543168,
      "size_label": "953.9 GiB",
      "model": "WD_BLACK SN850X 1000GB",
      "vendor": "",
      "transport": "nvme",
      "transport_label": "NVMe",
      "removable": false,
      "read_only": false,
      "eligible": true
    },
    {
      "path": "/dev/nvme1n1",
      "size_bytes": 2000398934016,
      "size_label": "1.8 TiB",
      "model": "",
      "vendor": "",
      "transport": "nvme",
      "transport_label": "NVMe",
      "removable": false,
      "read_only": false,
      "eligible": false,
      "excluded_reason": "boot_disk"
    },
    {
      "path": "/dev/mmcblk0",
      "size_bytes": 31914983424,
      "size_label": "29.7 GiB",
      "model": "",
      "vendor": "",
      "transport": "mmc",
      "transport_label": "MMC",
      "removable": false,
      "read_only": false,
      "eligible": false,
      "excluded_reason": "too_small"
    },
    {
      "path": "/dev/sde",
      "size_bytes": 0,
      "size_label": "0 B",
      "model": "Card Reader",
      "vendor": "Generic",
      "transport": "usb",
      "transport_label": "USB",
      "removable": true,
      "read_only": false,
      "eligible": false,
      "excluded_reason": "too_small"
    },
    {
      "path": "/dev/sdf",
      "size_bytes": 128035676160,
      "size_label": "119.2 GiB",
      "model": "WP Disk",
      "vendor": "ATA",
      "transport": "sata",
      "transport_label": "SATA",
      "removable": false,
      "read_only": true,
      "eligible": false,
      "excluded_reason": "read_only"
    }
  ],
  "tpm": {
    "present": true,
    "version": "2.0",
    "usable": true
  },
  "system": {
    "ram_bytes": 16694403072,
    "cpu_threads": 8,
    "cpu_cores": 4,
    "uefi": true,
    "meets_requirements": true,
    "unmet": []
  },
  "live": {
    "is_live": true,
    "live_image": "ghcr.io/example/os:stable",
    "detected_by": "cmdline:root=live:"
  },
  "offline": {
    "stores": [
      "/usr/share/tuna-installer/oci-store"
    ],
    "images": [
      "ghcr.io/example/os:stable",
      "ghcr.io/example/os:beta",
      "localhost/os:beta"
    ]
  },
  "bootc": {
    "context": "live-host",
    "found": true,
    "version": "1.17.1",
    "capabilities": {
      "bootc_image_storage": {
        "status": "unknown",
        "reason": "not_established"
      },
      "container_inspect": {
        "status": "supported",
        "reason": "command_present"
      },
      "deployment_mount": {
        "status": "unknown",
        "reason": "not_established"
      },
      "install_finalize": {
        "status": "supported",
        "reason": "command_present"
      },
      "install_print_configuration": {
        "status": "supported",
        "reason": "command_present"
      },
      "unified_storage": {
        "status": "unknown",
        "reason": "not_established"
      }
    }
  },
  "gpus": [
    {
      "vendor_id": "0x8086",
      "device_id": "0xa7a0",
      "class": "0x030000"
    },
    {
      "vendor_id": "0x10de",
      "device_id": "0x28a0",
      "class": "0x030200"
    }
  ],
  "dmi": {
    "vendor": "LENOVO",
    "product": "21HMCTO1WW"
  },
  "capabilities": {
    "version": 1,
    "live_host": {
      "context": "live-host",
      "is_live": true,
      "image": "ghcr.io/example/os:stable",
      "digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
      "platform": "x86_64",
      "backend": "ostree",
      "bootc_version": "1.17.1"
    },
    "source": {
      "selected": false,
      "ref": "",
      "transport": "",
      "locality": "unknown",
      "store": "",
      "digest": "",
      "platform": ""
    },
    "execution_root": {
      "context": "none",
      "established": false,
      "reason": "no_source_selected",
      "backend": "",
      "bootc_version": ""
    },
    "target": {
      "update_ref": "",
      "update_ref_from": "",
      "state": "not_installed"
    },
    "features": {
      "bootc_image_storage": {
        "status": "unknown",
        "reason": "no_source_selected",
        "context": "none"
      },
      "deployment_mount": {
        "status": "unknown",
        "reason": "no_source_selected",
        "context": "none"
      },
      "finalization": {
        "status": "unknown",
        "reason": "no_source_selected",
        "context": "none"
      },
      "install_configuration": {
        "status": "unknown",
        "reason": "no_source_selected",
        "context": "none"
      },
      "native_inspection": {
        "status": "unknown",
        "reason": "no_source_selected",
        "context": "none"
      },
      "unified_storage": {
        "status": "unsupported",
        "reason": "fisherman_refuses",
        "context": "fisherman"
      }
    },
    "install_defaults": {
      "observed": false,
      "root_filesystem": "",
      "block_setups": [],
      "bootloader": ""
    },
    "image": {
      "observed": false,
      "kernel_version": "",
      "kernel_unified": false
    }
  },
  "bootc_store": {
    "version": 1,
    "context": "live-host",
    "available": true,
    "reason": "observed",
    "machine_platform": "x86_64",
    "sources": {
      "bootc_status": "observed",
      "store_index": "absent",
      "image_list": "not_requested"
    },
    "ignored_entries": 0,
    "items": [
      {
        "ref": "ghcr.io/example/os:stable",
        "owner": "bootc",
        "context": "live-host",
        "storage": "ostree",
        "roles": [
          "booted"
        ],
        "availability": "deployed",
        "digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
        "digest_from": "bootc_status",
        "platform": "x86_64",
        "matches_machine": true
      }
    ]
  }
}
```

## Compatibility

`protocol_version` changes when a field is removed or renamed or its meaning
changes. Adding a field, an `excluded_reason` value or an `unmet` value does
not change it, so a frontend must ignore fields it does not know and treat
an unknown `excluded_reason` as "not eligible".

`capabilities.version` and `bootc_store.version` follow the same rule for
their own objects. A new reason code, role or source status is an addition:
treat an unknown one as "not established".

## Why

Before this command every bootc-installer frontend probed the hardware
itself:

| Fact | GNOME | KDE | COSMIC | Niri | XFCE |
|---|---|---|---|---|---|
| disk filter | skips loop/ram/sr/zram/dm and the boot disk found from `findmnt /sysroot` or `/` (on a dmsquash live ISO `/` is an overlay, so the walk finds nothing); hides removable disks | `TYPE == disk` only | `TYPE == disk` only | `TYPE == disk` only | `TYPE == disk`; skips a disk whose own mount points (not its partitions') include the live mount |
| size unit | binary maths labelled "GB" | lsblk's own `SIZE` string | lsblk's `SIZE` string | lsblk's `SIZE` string | SI ("GB", 1000) |
| minimum size | 50 GiB | none | none | none | none |
| model | sysfs vendor + model | lsblk | lsblk | never shown | lsblk |
| RAM/CPU/UEFI | yes | no | no | no | no |
| live | `/run/ostree-booted` (any ostree host) | `/run/ostree-live` or `rd.live.image`, read inside the sandbox | same | same | same |
| offline stores | n/a | `/etc`, `/usr/share` read inside the sandbox (the runtime's, not the host's) | same | same | same |

So zram was offered as an install target by four frontends and the live USB
by at least three; sizes appeared in three conventions; only GNOME checked RAM,
CPU or UEFI; and four frontends looked for offline stores inside their own
sandbox.
