# `fisherman validate --json`

`fisherman validate --json` checks a recipe against **every rule the install
enforces before it touches a disk**, and prints the result as **one JSON
object on stdout**. Frontends use it to reject a bad hostname, username,
filesystem or encryption choice on the page where the user typed it, instead
of after the OS is on disk.

```text
fisherman validate --json <recipe.json | ->            a whole recipe
fisherman validate --json --partial <recipe.json | ->  only the fields present
fisherman validate <recipe.json>                       human summary (unchanged)
```

`-` reads the recipe from stdin. Use stdin whenever the recipe holds a LUKS
passphrase, so it never lands in a temp file or on the command line.

- Exit `0`: valid. `errors` is `[]`.
- Exit `1`: invalid. `errors` lists every problem found. A recipe that cannot
  be read or is not valid JSON is also exit `1`, reported as a problem with
  code `recipe_unreadable` or `recipe_malformed` and an empty `field`.
- Exit `2`: bad arguments (no recipe, more than one, an unknown flag). The
  message and usage are on stderr; stdout is empty.
- **No root**, writes nothing. The full mode `stat`s the disk and partition
  paths and reads the TPM state from `/sys`; `--partial` reads nothing at all.

## One rule set

The rules live in one place, `fisherman/internal/recipe/rules.go`. Three
callers run them:

| Caller | Runs |
|---|---|
| the install (`fisherman <recipe.json>`) | everything below, right after loading the recipe and before the host-tool check, the image check or any disk step |
| `fisherman validate --json` and `fisherman validate` | the same function as the install (`installProblems`), so they accept exactly what the install accepts |
| `fisherman validate --json --partial` | only the rules for the fields present; no disk, partition or TPM check |

The plain `fisherman validate <recipe.json>` keeps its output format; it now
applies the same rules (so it also rejects a bad hostname, a bad username and
a TPM type on a machine without a usable TPM 2.0).

## Schema (`protocol_version` 1)

```text
{
  protocol_version  int      schema version; see "Compatibility"
  valid             bool     errors is empty
  errors[]                   every problem found, in the order below; [] when valid
    field           string   JSON path of the recipe field: "hostname", "user.username",
                             "encryption.passphrase", "customMounts[0].fstype", ...;
                             "" when the recipe itself could not be read
    code            string   stable id; see "Codes". Key copy on this, never on message
    message         string   English, for logs and CLI users; may change
}
```

Order: the layout (disk or customMounts, filesystem and its combinations),
then `imageType`, `bootloader`, `encryption`, `varDisk`, `hostname`,
`user.username`, and the TPM check last. Each field reports at most one
problem from its own rule (the first it breaks).

### Example

```console
$ fisherman validate --json recipe.json
{
  "protocol_version": 1,
  "valid": false,
  "errors": [
    {
      "field": "hostname",
      "code": "hostname_invalid_char",
      "message": "hostname \"my_pc\" contains '_'; use only letters, digits, hyphens and dots"
    },
    {
      "field": "user.username",
      "code": "username_invalid_start",
      "message": "username \"Alice\" must start with a lowercase letter or an underscore"
    }
  ]
}
```

More, byte for byte, in
`fisherman/cmd/fisherman/testdata/validate/*.golden` (inputs beside them).

## Validating one page: `--partial`

A frontend validates a page while it is filled in, before a whole recipe
exists, by sending only what that page has, in the recipe's own shape:

```console
$ echo '{"hostname":"-router"}' | fisherman validate --json --partial -
$ echo '{"user":{"username":"root"}}' | fisherman validate --json --partial -
$ echo '{"filesystem":"zfs","encryption":{"type":"luks-passphrase","passphrase":"x"}}' \
    | fisherman validate --json --partial -
```

A rule runs when its key is present:

| Key present | Rules |
|---|---|
| `hostname` | hostname |
| `user` with `username` | username |
| `filesystem` | filesystem, and its combinations with `btrfsSubvolumes`, `composeFsBackend` and `encryption.type` (absent ones count as false / none) |
| `encryption` | encryption type, passphrase |
| `bootloader` | bootloader |
| `imageType` | imageType |

Everything else (`disk`, `customMounts`, `varDisk`, the TPM) is ignored in
partial mode: those read the machine, and the full mode checks them.

Why a partial recipe rather than `--field name=value` flags: the passphrase
rule needs the passphrase, and argv is world-readable in `/proc`. A partial
recipe on stdin keeps secrets off the command line, uses the shape frontends
already build, and lets cross-field rules (ZFS with LUKS) apply when the page
holds both fields.

## Rules

| Field | Rule | Codes |
|---|---|---|
| `hostname` | required; at most 64 characters (the kernel's `HOST_NAME_MAX`, also systemd's limit for `/etc/hostname`); one or more dot-separated RFC 1123 labels, each 1–63 characters of ASCII letters, digits and `-`, not starting or ending with `-`. Upper case is allowed. | `hostname_required`, `hostname_too_long`, `hostname_invalid_char`, `hostname_label_empty`, `hostname_label_too_long`, `hostname_label_hyphen` |
| `user.username` | empty is valid (no user is created). Otherwise at most 32 characters; a lowercase letter or `_` first; then lowercase letters, digits, `_` and `-`; and not a name the target already has: the base users and groups of Fedora/EL/Debian, common service accounts, the groups frontends add the first user to, and anything starting `systemd-` (`recipe.ReservedUsernames`). This is what `useradd` accepts on every image fisherman installs; it used to be checked only by `useradd` itself, in the configure step. | `username_too_long`, `username_invalid_start`, `username_invalid_char`, `username_reserved` |
| `filesystem` (auto layout) | one of `xfs`, `ext4`, `btrfs`, `zfs` (`recipe.Filesystems`) | `filesystem_unsupported` |
| `btrfsSubvolumes` | needs `filesystem: btrfs` | `btrfs_subvolumes_require_btrfs` |
| `composeFsBackend` | needs fs-verity, so not XFS | `composefs_requires_fsverity` |
| `encryption.type` | one of the encryption choices (below); `""` means `none` | `encryption_unsupported` |
| `encryption.type` | not LUKS on ZFS | `encryption_unsupported_on_zfs` |
| `encryption.type` | not encrypted with `customMounts` (the manual path runs no `luksFormat`) | `encryption_unsupported_on_manual_layout` |
| `encryption.type` | a `tpm2-*` type needs a usable TPM 2.0 here: `probe --json`'s `tpm.usable`, including its `BOOTC_INSTALLER_FAKE_TPM` override. Full mode only. | `encryption_tpm_unavailable` |
| `encryption.passphrase` | non-empty for a type with `needs_passphrase` | `encryption_passphrase_required` |
| `disk` | required on the auto layout, and must exist. Full mode only. | `disk_required`, `disk_not_found` |
| `customMounts[i].partition` | required, and must exist unless the target is swap or empty | `custom_mount_partition_required`, `custom_mount_partition_not_found` |
| `customMounts[i].fstype` | one of `fat32`, `ext3`, `ext4`, `xfs`, `btrfs`, `swap`, `unformatted`, `""` | `custom_mount_fstype_unsupported` |
| `customMounts` | one entry targets `/` | `custom_mounts_no_root` |
| `varDisk.disk` | required, exists, differs from `disk` | `var_disk_required`, `var_disk_not_found`, `var_disk_same_as_disk` |
| `imageType` | `""` or `bootc` | `image_type_unsupported` |
| `bootloader` | `""`, `grub2` or `systemd` | `bootloader_unsupported` |
| (whole recipe) | readable, valid JSON | `recipe_unreadable`, `recipe_malformed` |

Not fisherman rules, and left to the frontend: the password itself (fisherman
accepts an empty one, which leaves the account locked), "password and
confirmation match", whether a user is required for the chosen image
(`needs_user_creation`), and password strength.

## Encryption choices: `data/encryption-choices.json`

The encryption types, in the order frontends offer them, are data in
[`data/encryption-choices.json`](../data/encryption-choices.json):

```json
{
  "protocol_version": 1,
  "choices": [
    {"type": "none", "encrypted": false, "needs_passphrase": false, "needs_tpm2": false, "copy_key": "encryption_none"},
    {"type": "luks-passphrase", "encrypted": true, "needs_passphrase": true, "needs_tpm2": false, "copy_key": "encryption_luks_passphrase"},
    {"type": "tpm2-luks", "encrypted": true, "needs_passphrase": false, "needs_tpm2": true, "copy_key": "encryption_tpm2_luks"},
    {"type": "tpm2-luks-passphrase", "encrypted": true, "needs_passphrase": true, "needs_tpm2": true, "copy_key": "encryption_tpm2_luks_passphrase"}
  ]
}
```

(The real file is indented and carries a `$comment`.) `copy_key` is the
branding copy-key stem: the label is `<copy_key>_label`, the description
`<copy_key>_description`. Offer a `needs_tpm2` choice only when
`probe --json` says `tpm.usable`; show the passphrase field only for
`needs_passphrase`.

It is a file rather than a `--choices` flag because frontends need the list
when they build and test, not only at run time: bootc-installer copies it to
`shared/recipe/encryption-choices.json`, and its unit tests hold every
frontend's page to that copy without running fisherman. It is generated from
the same Go table (`recipe.EncryptionChoices`) the `encryption.type` and
passphrase rules read, and `TestEncryptionChoicesFile` fails if the two
differ. Regenerate it with:

```bash
cd fisherman && go test ./internal/recipe -run TestEncryptionChoicesFile -update
```

## Compatibility

`protocol_version` changes when a field is removed or renamed or its meaning
changes. Adding a field or a new code does not change it. A code is never
renamed or reused; a frontend must treat an unknown code as "invalid, show
`message`". The choices file has its own `protocol_version`, under the same
rule.

## Why

Before this command the rules were spread across the frontends, and none of
them matched what the backend enforced:

| Rule | GNOME | KDE | COSMIC | Niri | XFCE | fisherman before |
|---|---|---|---|---|---|---|
| hostname | none (strips whitespace) | non-empty | none | none | single RFC 1123 label regex, no length limit | non-empty |
| username | `^[a-z_][a-z0-9_-]{0,31}$` | no user page | no user page | no user page | non-empty | none: `useradd` in the configure step, after the OS was on disk |
| reserved names | none | — | — | — | none | none |
| passphrase | non-empty, confirm matches | non-empty, confirm matches | non-empty | non-empty, confirm matches | non-empty, confirm matches | non-empty |
| encryption types | switch → `luks-passphrase` / `tpm2-luks-passphrase` | own list | own list | own list | own list | enum in `Validate` |
| filesystems | from the image catalog | `xfs` default | `xfs`, `btrfs` | `xfs` only | `xfs`, `ext4`, `btrfs`, `zfs` | `xfs`, `ext4`, `btrfs`, `zfs` |

So `fisherman` installed the OS and then failed on a username like `Alice` or
`root`; only XFCE checked the hostname; and four frontends each carried their
own copy of the encryption table.
