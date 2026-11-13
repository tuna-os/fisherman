# Firn recipe compatibility matrix

This document compares the recipe of [Firn](https://github.com/frostyard/firn)
(schema version 2) with the fisherman recipe, field by field, and records
where the two can and cannot carry the same install
(tuna-os/fisherman#386).

Every fact here comes from the code at two pinned commits. It is not a proof
that the two installers produce the same system at run time.

| Side | Commit | Files read |
|---|---|---|
| Firn | [`9968f2b3`](https://github.com/frostyard/firn/tree/9968f2b32e5aa28d8b168cf1b903d83a30e57759) (2026-10-27) | `internal/recipe/recipe.go`, `validate.go`, `fullname.go`; `internal/steps/bootc.go`, `internal/bootcimg/bootcimg.go`, `verify.go`; `internal/disk/partition.go`, `format.go`; `internal/sysconfig/user.go`, `locale.go` |
| fisherman | `dev` at the commit of this document | `fisherman/internal/recipe/recipe.go`, `rules.go`, `strict.go`, `source.go`, `image.go`, `reuse.go`; `fisherman/cmd/fisherman/main.go`; `fisherman/internal/disk/format.go`, `partition.go`; `fisherman/internal/post/user.go` |

## Decision

- **No adapter.** An import or export adapter between the two formats is out
  of scope by owner decision. This document is the whole deliverable of
  #386. If an adapter is ever wanted, it needs its own issue, and the rules
  below say what it must refuse.
- **fisherman JSON stays authoritative** for fisherman and for every
  bootc-installer frontend. Firn TOML is not a joint canonical schema, and
  this comparison asks no change of Frostyard.
- Both root licenses are GPLv3. This document copies no code.

## The formats

| Property | Firn | fisherman |
|---|---|---|
| Encoding | TOML file (`recipe.Load`, `recipe.Parse`) | JSON, on stdin (`fisherman -`) or a file (`recipe.LoadArg`); at most 1 MiB from stdin (`MaxRecipeBytes`) |
| Version field | `version`, required: `2`; bootc `1` with a deprecation warning until `BootcV1EndDate` (2026-12-30), rejected from that date; any other value is `bad-version` | none (tuna-os/fisherman#293) |
| Structure | tables `[image]`, `[target]`, `[security]`, `[system]`, `[system.user]` | flat object with nested `encryption`, `user`, `keyboard`, `varDisk`, `customMounts[]`, `sourceReuse`, `extras`, `slurp` |
| Unknown keys | fail-closed: the first `meta.Undecoded()` key is a parse error | fail-closed: `UnknownFields` returns every unknown key as `recipe_unknown_field`; `Parse` refuses the first. Key matching is case-insensitive, as in `encoding/json`; TOML keys are case-sensitive |
| Absent vs. zero | `toml.MetaData.IsSet` for `system.user` and the `mok` fields; otherwise an empty string means absent | pointer or nil slice for the fields where absence has a meaning (`flatpaks`, `additionalImageStores`, `keyboard`, `varDisk`, `sourceReuse`, `extras`, `copyNetworkProfiles`, `copyBluetoothPairings`, `setBootNext`); otherwise an empty string means absent |
| Error shape | `Issue{Code, Field, Message}`, one per rule family (`required`, `enum`, `mutex`, …) | `Problem{Field, Code, Message}`, one stable code per rule (docs/VALIDATE.md) |
| Machine facts in validation | `Env{SecureBoot, TPM, ZoneinfoDir, Now}`, given by the caller | a separate call per fact: `CheckTPM`, `CheckTarget`, `ValidateImage(live)` |

## Field matrix

Columns: the Firn field, the fisherman field, and the verdict for each
direction. "Exact" means the value carries over unchanged and means the same.
"Refuse" means a converter must stop with an error: the value has no
equivalent, and dropping it would change the install.

### Image

| Firn | fisherman | Firn → fisherman | fisherman → Firn |
|---|---|---|---|
| `image.family` (required, only `"bootc"`; never inferred) | `imageType` (`""` or `"bootc"`; `"ostree"` refused) | `bootc` → omit `imageType` | write `family = "bootc"` |
| `image.ref` (required; no space or tab) | `image` (optional; no whitespace, no control character, no leading `-`) | exact, if it passes fisherman's stricter rule; else refuse | exact; an empty `image` (fisherman's "install the running live image", `ValidateImage`) has no Firn form: refuse |
| `image.target_ref` (optional; no whitespace; empty means `ref`) | `targetImgref` (optional; same rule as `image`) | exact | exact |
| `image.cosign_pub_key` (path; needs a registry `ref`) | none | refuse: fisherman does no signature verification | — |
| none | `sourceReuse` (install from the live host's bootc store) | — | refuse |
| none | `genericImage`, `unifiedStorage`, `flatpakVarPath`, `additionalImageStores` | — | refuse when set; `unifiedStorage: true` is refused by fisherman itself |

Transports. Firn's `IsRegistryRef` treats `docker://…` and a bare reference
as registry references, and `containers-storage:`, `oci:`, `oci-archive:`,
`dir:` and `docker-archive:` as local. fisherman passes `image` to bootc,
skopeo and podman as written, and takes the transport off with `bareImageRef`
only where it retags. Both accept the same reference strings; the difference
is the rule above (fisherman also refuses control characters and a leading
`-`).

What the install does with the reference differs. Firn installs the source
that its `preflight-image` step selected (`env.BootcSourceRef`) and, with no
`target_ref`, sets the update reference to `ref`. fisherman installs `image`
(or the live media's `containers-storage:`/`oci:` input) and sets the update
reference from `targetImgref`. A converter keeps both references as given; it
cannot make the two pipelines pick the same source.

### Target

| Firn | fisherman | Firn → fisherman | fisherman → Firn |
|---|---|---|---|
| `target.disk` (required; must start with `/dev/`) | `disk` (required unless `customMounts`; must exist; `CheckTarget` refuses the boot disk, a read-only disk, a partition) | exact | exact; a recipe with `customMounts` (manual layout) has no Firn form: refuse |
| `target.filesystem` (required: `btrfs`, `xfs`, `ext4`) | `filesystem` (`xfs`, `ext4`, `btrfs`, `zfs`; `zfs` only with `experimental: ["zfs"]`) | exact | exact for the three shared values; `zfs`: refuse |
| `target.btrfs_subvolumes` (btrfs only) | `btrfsSubvolumes` (btrfs only) | **refuse**: same name, other layout (below) | **refuse**, for the same reason |
| `target.bootloader` (`""`, `systemd`, `grub2`; empty is systemd-boot) | `bootloader` (`""`, `grub2`, `systemd`; empty is GRUB2) | write the value always: Firn `""` → fisherman `"systemd"` | write the value always: fisherman `""` → Firn `"grub2"` |
| none | `varDisk`, `zfsPoolName`, `targetMount`, `luksMapperName`, `override` | — | refuse when set |
| none (Firn always passes `--composefs-backend`, `steps/bootc.go`) | `composeFsBackend` (opt-in; refused with `xfs`; forced on with `zfs`) | set `composeFsBackend: true`; with `xfs`, refuse (fisherman refuses composefs on XFS: `composefs_requires_fsverity`) | refuse unless `composeFsBackend` is `true` |

Bootloader default. The two defaults are opposite: Firn's `runBootcInstall`
uses `systemd` for an empty value, and fisherman's install treats an empty
value as GRUB2 (`main.go`: `isSystemdBoot := r.Bootloader == "systemd" ||
r.Filesystem == "zfs"`). An empty field must never cross unchanged.

Partition layout. Both use a 2 GiB ESP. For GRUB2 both write three partitions
(ESP, a 2 GiB `/boot`, root); for systemd-boot both write two (ESP, root).

Subvolumes. Firn's `CreateSubvolumes` makes `@`, `@home` and `@snapshots` and
sets `@` as the default subvolume. fisherman's `SetupBtrfsSubvolumes` makes
`@` only and mounts it with `subvol=@,compress=zstd:1` (it dropped `@home` and
`@snapshots` in tuna-os/fisherman#416 because nothing mounted them). The
flag does not mean the same layout, so `true` cannot cross either way; `false`
is exact.

### Security

| Firn | fisherman | Firn → fisherman | fisherman → Firn |
|---|---|---|---|
| `security.encryption` (required, never defaulted: `none`, `luks-passphrase`, `tpm2-luks`, `tpm2-luks-passphrase`) | `encryption.type` (same four values; empty is `none`) | exact | exact; an empty type → `"none"` (Firn requires it) |
| `security.passphrase` (inline; exactly one of it and `passphrase_file` for the passphrase types; neither for the others) | `encryption.passphrase` (required for the passphrase types; ignored for the others) | exact | exact for the passphrase types; for `none` and `tpm2-luks`, drop it (Firn refuses it there, fisherman ignores it) |
| `security.passphrase_file` (path; regular file, not world-readable) | none: the passphrase travels inline on stdin | refuse: fisherman reads no secret file. A converter that read it would hold the secret and must not write it anywhere | — |
| `security.mok`, `security.mok_password_file` (required with Secure Boot active; refused without it) | none | refuse when set | — (a fisherman recipe never sets them; Firn requires `mok` on a Secure Boot machine, so the converted recipe fails Firn's validation there) |
| TPM rule: `tpm2-*` needs `Env.TPM` (`tpm`) | `CheckTPM`: `tpm2-*` needs a usable TPM 2.0 (`encryption_tpm_unavailable`) | same rule | same rule |
| none | encryption with `customMounts` or `zfs` is refused | — | — |

### System

| Firn | fisherman | Firn → fisherman | fisherman → Firn |
|---|---|---|---|
| `system.hostname` (required; RFC 1123 labels, at most 253 characters) | `hostname` (required; letters, digits, `-`, `.`; at most 64 characters, labels at most 63) | exact if at most 64 characters; else refuse | exact |
| `system.locale` (`ll_CC[.ENC]`) | `locale` (also `ll`, `C`, `POSIX`, a 3-letter region and `@modifier`) | exact | exact if it matches Firn's narrower form; else refuse |
| `system.timezone` (IANA form; must exist in `Env.ZoneinfoDir` when given) | `timezone` (IANA form; checked against the image's zoneinfo after the install) | exact | exact; Firn may then refuse a zone the install machine lacks |
| `system.keyboard` (string `LAYOUT[:VARIANT[:MODEL]]`, lowercase, digits, `_`, `-`) | `keyboard` (object `{layout, variant, model}`; several layouts with `,`; `.`, `+` and capitals allowed) | split on `:` into the object | join with `:` when every part matches Firn's pattern; a `,` list or another character: refuse |
| `system.flatpaks` (application IDs; Firn installs them with `flatpak.Provision` and warns `flatpak_unreachable` for each one it cannot reach) | `flatpaks` (absent: copy the host's system Flatpaks; `[]`: copy nothing; a list also copies them all: not a filter, nothing is downloaded) | **refuse** a non-empty list: fisherman cannot install a named app. An empty or absent list → `flatpaks: []` | **refuse** absent or non-empty: Firn cannot copy the host's Flatpaks; `[]` → omit |
| `system.core_flatpaks` (apps from the image's `org.frostyard.core-flatpaks` label) | none (fisherman reads no image Flatpak label; tuna-os/fisherman#387) | refuse when `true` | — |
| `system.root_ssh_authorized_key` / `_file` | none | refuse when set | — |
| none | `extras`, `copyNetworkProfiles`, `copyBluetoothPairings`, `setBootNext`, `selinuxDisabled`, `distroID`, `brewTap`, `slurpWallpapers`, `slurp`, `experimental` | — | refuse when set to anything but its default |

### User

| Firn | fisherman | Firn → fisherman | fisherman → Firn |
|---|---|---|---|
| `[system.user]` table present / absent | `user.username` set / empty | table present → set `username`; absent → no `user` | `username` empty → omit the table |
| `user.name` (`[a-z_][a-z0-9_-]*`, at most 32) | `user.username` (same shape and length; also refuses the names of `ReservedUsernames` and `systemd-*`) | exact, unless fisherman refuses a reserved name | exact |
| `user.fullname` (`:`, CR and LF refused) | `user.fullname` (no rule yet; tuna-os/fisherman#414) | exact | exact if it has no `:`, CR or LF; else refuse |
| `user.password_hash` (must start with `$`; written verbatim) | `user.password` (a crypt setting `$id$…` is written verbatim with `chpasswd -e`; anything else is a plaintext password) | exact if it matches fisherman's crypt pattern `^\$[A-Za-z0-9.]+\$`; else refuse (fisherman would take it as plaintext) | a crypt setting → `password_hash`; a plaintext password: refuse (Firn has no inline plaintext field) |
| `user.password_file` (plaintext in a non-world-readable file; Firn hashes it with SHA-512 crypt) | none | refuse (same reason as `passphrase_file`) | — |
| `user.groups` (name syntax only; groups the image lacks are skipped with a `group_missing` warning) | `user.groups` (groups the image lacks are dropped with a warning, `filterExistingGroups`) | exact | exact if every name matches Firn's `[a-z_][a-z0-9_-]*` |
| `user.ssh_authorized_key` / `_file` | none | refuse when set | — |
| none | an image catalog leaf with `needs_user_creation` (images.json) | — | — (a catalog fact, not a recipe field) |

## Explicit vs. absent

These fields change meaning between "absent" and a zero value. A converter
must write the value it means on the other side, never rely on the other
side's default:

| Concept | Firn | fisherman | Rule |
|---|---|---|---|
| Bootloader | absent → systemd-boot | absent → GRUB2 | always write it |
| Encryption | absent → `required` error | absent → `none` | always write it |
| User | table absent → no user | `username` empty → no user | carry presence |
| Flatpaks | absent or `[]` → install nothing | absent → copy the host's; `[]` → copy nothing | see the matrix: only "nothing" crosses |
| Image | `ref` absent → `required` error | `image` absent → the running live image (live media only) | refuse an empty fisherman `image` |
| Update reference | `target_ref` absent → `ref` | `targetImgref` absent, or equal to `image` → no `--target-imgref`, so bootc tracks the installed source | carry as given |
| Locale, timezone, keyboard | absent → the image's | absent → the image's | exact |

## Examples

A recipe both formats carry, Firn first:

```toml
version = 2

[image]
family = "bootc"
ref = "ghcr.io/example/os:stable"

[target]
disk = "/dev/nvme0n1"
filesystem = "btrfs"
bootloader = "grub2"

[security]
encryption = "luks-passphrase"
passphrase = "correct horse battery staple"

[system]
hostname = "frost01"
keyboard = "de:nodeadkeys"

[system.user]
name = "ann"
password_hash = "$6$salt$hash"
groups = ["wheel"]
```

```json
{
  "image": "ghcr.io/example/os:stable",
  "disk": "/dev/nvme0n1",
  "filesystem": "btrfs",
  "bootloader": "grub2",
  "composeFsBackend": true,
  "encryption": {"type": "luks-passphrase", "passphrase": "correct horse battery staple"},
  "hostname": "frost01",
  "keyboard": {"layout": "de", "variant": "nodeadkeys"},
  "user": {"username": "ann", "password": "$6$salt$hash", "groups": ["wheel"]},
  "flatpaks": []
}
```

`composeFsBackend: true` and `flatpaks: []` are there because Firn always
installs with composefs and copies no host Flatpaks. With the bootloader left
out, the Firn recipe means systemd-boot and the fisherman one GRUB2.

Recipes a converter must refuse, with the reason:

| Input | Why |
|---|---|
| Firn `btrfs_subvolumes = true` | fisherman makes `@` only; Firn makes `@`, `@home`, `@snapshots` |
| Firn `passphrase_file`, `password_file`, `mok_password_file` | fisherman reads no secret file |
| Firn `cosign_pub_key` | fisherman does not verify signatures |
| Firn `flatpaks = ["org.example.App"]`, `core_flatpaks = true` | fisherman copies the host's Flatpaks; it installs no named app |
| Firn `mok`, SSH key fields | no fisherman field |
| Firn `filesystem = "xfs"` | Firn installs with composefs, which fisherman refuses on XFS |
| fisherman `image` empty | Firn needs a reference |
| fisherman `filesystem: "zfs"`, `customMounts`, `varDisk`, `sourceReuse` | no Firn field |
| fisherman `flatpaks` absent or non-empty | Firn cannot copy the host's Flatpaks |
| fisherman `user.password` plaintext | Firn takes a password only from a file or as a hash |
| fisherman `keyboard.layout: "us,ru"` | Firn's keyboard string has one layout |
| fisherman `hostname` over 64 characters | (Firn → fisherman) fisherman's limit is 64 |

## What an adapter would need

Out of scope (see Decision). Recorded so a later issue starts from the facts:

- Run fisherman's own rules on the result (`Parse`, then `Problems`, and on
  the machine `CheckTPM` and `CheckTarget`) before any disk work, as every
  fisherman install does already.
- Refuse every row marked "refuse" above with a stable code; never drop a
  security or storage choice.
- Never write a converted recipe to a file: it holds the passphrase. Pipe it
  to `fisherman -`.
- Round-trip tests against both validators, pinned to a Firn commit.
