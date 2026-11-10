# Installation receipt and boot verification

An install that exits 0 has written a disk. It has not shown that the disk
boots, or that the system that boots is the image the user chose. fisherman
keeps these claims apart (#385):

| Claim | Who makes it | Evidence |
|---|---|---|
| installed, reboot required | the install (`complete` event) | the pipeline ran to the end |
| boot verified | `fisherman verify` | the installed system's own `bootc status`, compared with the receipt |

A frontend never turns the first into the second. Until a verification
result says `verified`, the target is "installed, not yet verified"
(`not-yet-run` before the first boot). The live ISO or the installer host can
never produce that result for the target: its `bootc status` describes
itself.

This checks identity only. Desktop login, disk unlock, updates, rollback and
application checks are separate evidence.

Nothing here uploads anything, switches an image, changes the boot order,
rolls back or reboots. A failed or cancelled verification leaves the system
as it was.

## Files on the installed system

| Path (as the booted system sees it) | Written by | Content |
|---|---|---|
| `/var/lib/fisherman/install-receipt.json` | the install | the receipt (below) |
| `/etc/systemd/system/fisherman-boot-receipt.service`, `multi-user.target.wants/` link | the install | the first-boot collector unit |
| `/etc/fisherman/boot-receipt.sh` | the install | the script the unit runs with `/bin/sh` |
| `/var/lib/fisherman/boot-receipt/collection.json` | the unit, at the first boot | the collection record (below) |
| `/var/lib/fisherman/boot-receipt/bootc-status.json` | the unit | the raw output of `bootc status --format json --format-version 1` |
| `/var/lib/fisherman/boot-receipt/bootc-version.txt` | the unit | the first line of `bootc --version` |
| `/var/lib/fisherman/boot-receipt/verification.json` | `fisherman verify --write` | the result (below) |

The install writes all of these through the deployment resolver
(`post.ResolveDeployment`): the deployment's `/etc` and the `/var` the booted
system mounts (a separate `/var` disk included), never the physical sysroot.
The receipt is in `/var`, so an image with a transient `/etc` keeps it; there
the unit is not staged (it would be gone at the first boot), the install log
says so, and `fisherman verify --live` on the installed system is the way to
verify.

A failure to write the receipt is a warning in the install log. It never
fails the install: the receipt is evidence, not part of a bootable system.

The unit runs once. Its condition `!…/collection.json` stops it after the
first collection. It runs the image's own bootc, as root, after
`basic.target`. To stop it, remove the unit file and its link.

## Receipt (version 1)

```json
{
  "schema": "fisherman.install-receipt",
  "version": 1,
  "install_id": "0123456789abcdef0123456789abcdef",
  "installed_at": "2026-11-10T12:00:00Z",
  "fisherman_version": "v0.3.0",
  "backend": "ostree",
  "deployment": {
    "stateroot": "default",
    "id": "8f3c1d7a2b6e4f90a1c5d3e7b9f20468ace13579bdf02468ace13579bdf02468"
  },
  "source": {
    "ref": "containers-storage:ghcr.io/example/os:stable",
    "transport": "containers-storage",
    "reference_digest": ""
  },
  "expected": {
    "platform_manifest_digest": "sha256:1111…",
    "digest_from": "installed_deployment",
    "index_digest": "",
    "platform": "linux/amd64"
  },
  "update_ref": "ghcr.io/example/os:stable"
}
```

| Field | Meaning |
|---|---|
| `install_id` | 128 random bits, hex. The collector carries it, so a collection from another install is detected. |
| `backend` | `ostree` or `composefs`, from the deployment resolver. |
| `deployment.id` | the ostree commit checksum (from the deployment directory `<checksum>.<serial>`) or the composefs image verity (the `state/deploy/<verity>` directory). bootc status reports the same value for the booted entry. `""` when the name does not have that shape. |
| `source.ref` | the reference handed to bootc. Recorded only when it is a plain image reference: a reference with user information (`user:secret@host/…`), whitespace or shell syntax is left out. |
| `source.reference_digest` | the digest a reference pins (`name@sha256:…`). It can be an index digest, so it is information and never compared. |
| `expected.platform_manifest_digest` | the platform-specific manifest digest the system should boot. `digest_from` says where it came from: `installed_deployment` (the `ostree.manifest-digest` metadata of the installed ostree commit, read with `ostree show`) or `media_input` (verified live media, `media-input.json`'s `platformManifestDigest`). Otherwise `""` with `digest_reason: "platform_digest_not_recorded"`. |
| `expected.index_digest` | the index the media selected the platform manifest from. Never compared with a booted digest. |
| `expected.platform` | `os/arch[/variant]`: the media's platform, else fisherman's own architecture. |
| `update_ref` | the reference the installed system follows: the target reference, else the source, without a transport prefix (fisherman hands bootc the bare reference), else the live image. `""` when that is not a plain image reference. |

The receipt holds no passphrase, password, user name, hostname, machine-id,
disk serial or registry credential, and fisherman reads none to build it.

## Collection (version 1)

```json
{"schema":"fisherman.boot-collection","version":1,"install_id":"0123…","collected_at":"2026-11-10T12:05:00Z","boot_id":"3f2a1b4c-…","bootc_exit":0,"collector":"fisherman-boot-receipt.service"}
```

`bootc_exit` is the exit status of `bootc status`: `127` when bootc is
absent, `2` when bootc does not know `--format-version` (an older CLI). The
script writes every value either as a number or after a character check, so
the record is valid JSON whatever the system prints. It writes
`collection.json` last: its presence means the collection is complete.

## `fisherman verify --json`

```
fisherman verify --json [--root <dir>] [--live | --collected]
                        [--receipt <file>] [--collection <dir>] [--write]
```

- On the installed system, as root: `fisherman verify --json` (`--live` is
  the default for `--root /`) runs `bootc status --format json
  --format-version 1` and `bootc --version`, and nothing else.
- Against the first-boot collection: `--collected`, or `--root <dir>` (an
  installed system mounted elsewhere), or `--collection <dir>` (files
  copied off the machine, as the VM harness does). The host's bootc never
  runs for another root; `--live` with `--root` other than `/` is a usage
  error.
- `--write` also stores the result in the collection directory.

It prints one result on stdout and exits 0 only for `verified`; every other
outcome exits 1, and a usage error exits 2. Without a receipt, `--live`
does not run bootc at all.

## Result (version 1)

```json
{
  "schema": "fisherman.boot-verification",
  "version": 1,
  "outcome": "verified",
  "reason": "booted_matches_receipt",
  "evidence": "collected_status",
  "verified_at": "2026-11-10T12:10:00Z",
  "tools": { "fisherman": "v0.3.0", "bootc": "bootc 1.17.1" },
  "install_id": "0123456789abcdef0123456789abcdef",
  "expected": { "backend": "ostree", "deployment_id": "8f3c…", "platform_manifest_digest": "sha256:1111…",
                "digest_from": "installed_deployment", "index_digest": "", "platform": "linux/amd64",
                "update_ref": "ghcr.io/example/os:stable", "installed_at": "2026-11-10T12:00:00Z" },
  "booted": { "backend": "ostree", "deployment_id": "8f3c…", "image": "ghcr.io/example/os:stable",
              "transport": "registry", "manifest_digest": "sha256:1111…", "architecture": "amd64",
              "image_version": "42.20261101.0", "image_timestamp": "2026-11-01T00:00:00Z",
              "boot_id": "3f2a1b4c-…", "collected_at": "2026-11-10T12:05:00Z" },
  "checks": { "backend": "match", "deployment": "match", "platform_manifest_digest": "match",
              "platform": "match", "update_ref": "match" },
  "staged": { "present": false, "deployment_id": "", "manifest_digest": "" },
  "rollback": { "present": true, "deployment_id": "0000…0001", "manifest_digest": "sha256:0000…" },
  "cached_update_digest": ""
}
```

`evidence` is `live_status` or `collected_status`. Each check is `match`,
`mismatch`, `not_recorded` (the receipt has no value), `not_reported`
(bootc status has none) or `not_checked` (the evaluation stopped first).
`staged`, `rollback` and `cached_update_digest` are shown for the reader;
they are never the booted deployment.

### Rules

- Only `status.booted` counts. An expected image that is staged, cached
  (`cachedUpdate`) or the rollback entry is a `mismatch` with its own
  reason.
- bootc reports the platform manifest digest (`imageDigest`). It is
  compared with `expected.platform_manifest_digest` only. An index digest
  is a different object: a booted digest equal to the receipt's index
  digest is still a `mismatch`, and a receipt with only an index digest
  cannot verify a digest.
- `verified` needs every identity the receipt records (deployment id,
  platform manifest digest, update reference) to be reported for the
  booted entry and to match, the backend not to differ, and at least one of
  the deployment id and the digest to be compared. The architecture is
  compared when both sides have it (older bootc does not report it; the
  digest is platform-specific already).
- The update reference is compared with the booted image reference and its
  transport (`registry` by default). A source reference that differs from
  the update reference is normal and is not compared.
- A missing status, a parse failure, an unknown `apiVersion` or `kind`, or
  a field with the wrong type is never success.
- `status.booted.image` null (no bootc image booted) is `unavailable`.

### Outcomes and reasons

A code is never renamed or reused; new codes are additions and do not bump
the version. A frontend shows an unknown code as its outcome.

| Outcome | Reason | Meaning |
|---|---|---|
| `verified` | `booted_matches_receipt` | the booted entry is the receipt's deployment and image |
| `mismatch` | `backend_mismatch` | the booted backend (ostree/composefs) differs |
| `mismatch` | `deployment_mismatch` | another deployment booted |
| `mismatch` | `booted_digest_mismatch` | another image booted (or a digest of another kind) |
| `mismatch` | `expected_image_staged_not_booted` | the expected image is staged, not booted |
| `mismatch` | `expected_image_cached_not_booted` | the expected image is a cached update, not booted |
| `mismatch` | `expected_image_is_rollback` | the expected image is the rollback entry |
| `mismatch` | `platform_mismatch` | the booted architecture differs |
| `mismatch` | `update_ref_mismatch` | the system follows another reference |
| `unsupported` | `status_format_unsupported` | bootc has no `--format-version` (an older CLI) |
| `unsupported` | `status_api_unsupported` | `apiVersion` is not `org.containers.bootc/v1` or `kind` is not `BootcHost` |
| `unsupported` | `receipt_version_unsupported` | a receipt version this fisherman does not read |
| `unsupported` | `collection_version_unsupported` | a collection version this fisherman does not read |
| `unavailable` | `receipt_missing` | no receipt: not a system fisherman installed, or the installer host |
| `unavailable` | `receipt_malformed` | the receipt is not a version-1 receipt |
| `unavailable` | `collection_malformed` | the collection, or its status file, is unreadable |
| `unavailable` | `wrong_execution_context` | the collection belongs to another install, or the evidence kind is unknown |
| `unavailable` | `bootc_absent` | no bootc on the installed system |
| `unavailable` | `status_failed` | `bootc status` failed |
| `unavailable` | `status_malformed` | the output is not JSON of the documented shape |
| `unavailable` | `not_booted_bootc` | bootc reports no booted image |
| `unavailable` | `booted_identity_unreported` | bootc did not report an identity the receipt records |
| `unavailable` | `expected_identity_unknown` | the receipt records neither a deployment id nor a platform digest |
| `not-yet-run` | `collection_missing` | the installed system has not booted (or the unit has not run) yet |

## Fixtures and tests

`fisherman/internal/receipt/testdata/` holds versioned JSON fixtures: bootc
status for the supported release (ostree and composefs), extra fields,
missing fields, malformed output, wrong types, an older API, no booted
image, a wrong image, the expected image staged, cached or in rollback, an
index digest booted, another update reference and another architecture;
receipts (ostree, composefs, verified media with an index, index only,
source differing from the update reference, version 2, malformed);
collections (ok, another install, older CLI, bootc absent, failure, version
2, malformed). `results/` has the golden result for each case
(`go test ./internal/receipt -update` rewrites them). The collector script
itself runs in the tests against a fake bootc that records its arguments.

## VM acceptance

`scripts/boot-verify.sh` runs `scripts/boot-receipt.sh` after the installed
VM answers on SSH, for every `vm_boot` image of `tests/bootcrew-matrix.yaml`
(dakota covers composefs and systemd-boot, centos-bootc covers ostree and
GRUB). It copies the receipt and the collection off the VM, and checks:

1. the first-boot collection verifies;
2. `fisherman verify --json --live` on the installed system verifies (a
   static copy in `/var/tmp`, removed afterwards);
3. a copy of the receipt for another image is a `mismatch`;
4. `fisherman verify --live` on the CI host is not `verified`.

The checks are advisory: a failure goes to the job summary and fails the job
only with `BOOTCREW_RECEIPT_REQUIRED=1`. The harness boots the installed
disk image directly, with no install medium attached.
