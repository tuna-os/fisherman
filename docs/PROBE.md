# `fisherman probe --json`

`fisherman probe --json` prints, as **one JSON object on stdout**, the facts an
installer frontend needs before it shows its first page: which disks may be
installed to, whether a TPM 2.0 is usable, whether the machine meets the
minimum requirements, whether it booted from live media, and which offline
image stores that media carries.

- Exit `0`: stdout holds exactly one JSON object.
- Exit `1`: the probe failed (lsblk failed, `/proc/meminfo` unreadable). The
  reason is on stderr; stdout is empty.
- Exit `2`: bad arguments. `--json` is required; it is the only output format.
- **No root.** Run it as the desktop user.
- **Read-only.** It writes nothing: no files, no mounts, no lock files.

Each frontend used to compute these facts itself, and the copies drifted
(see "Why" below). The logic now lives here; a frontend only renders it.

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
}
```

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

All of these are **host** paths. Inside a Flatpak, `/etc` and `/usr` are the
runtime's, so the host's are read under `/run/host/etc` and `/run/host/usr`
(as bootc-installer's `recipe.py` does), and any other host path the sandbox
cannot see is asked of the host (`flatpak-spawn --host test -d` / `cat`).

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
  }
}
```

## Compatibility

`protocol_version` changes when a field is removed or renamed or its meaning
changes. Adding a field, an `excluded_reason` value or an `unmet` value does
not change it, so a frontend must ignore fields it does not know and treat
an unknown `excluded_reason` as "not eligible".

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
