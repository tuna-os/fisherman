# Signed EFI / UKI byte-preservation regression tests

## Scope and provenance

[frostyard/fisherman#28](https://github.com/frostyard/fisherman/pull/28)
identified a destructive inspection command: `objcopy --dump-section ... INPUT`
without an output image rewrites INPUT and can discard its Authenticode table.
The fix uses Go's read-only `debug/pe` reader. Credit: Brian Ketelsen and the
frostyard contributors; see the linked PR for the original implementation.

At Tuna fisherman dev `02ac7097abb07a206479ea84a559c41db12bf629`, neither those
commands nor frostyard's `secure.VerifyInstalled`/Snosi contract package exists.
There is no vulnerable reader to cherry-pick over. These tests adopt the fix's
byte-invariance regression requirement on Tuna's existing paths, without adding
an unused PE reader or importing a distro-specific secure-install protocol.
No implementation code is copied from frostyard.

## What runs

`go test ./...` from `fisherman/` includes:

- `copyFile`: source/destination bytes and certificate table remain identical;
  destination-open failure leaves the signed source unchanged; malformed bytes
  remain opaque to copying (this is not validation or acceptance of a PE).
- `InstallSystemdBoot`: `.signed` candidate wins over a distinct unsigned
  candidate; both output paths preserve all bytes; repeat calls are no-ops.
- Both fallback helpers: an existing signed fallback/companion chain is retained.
- `InstallGrubFallback`: shim, second stage and MokManager copies preserve bytes.
- Missing-source and blocked-destination errors leave existing UKIs unchanged.
- Real `EnsurePlymouthArgs`/`EnsureLuksArgs` calls, repeated calls and entry-read
  errors preserve the signed UKI. Both `efi` and `uki` BLS keys are exercised,
  including an entry with no external options line.

All paths live in `t.TempDir()`. No disk, mount, firmware variable, user key or
network access is used by these tests. The fixture helper is imported only by
Go tests. It reads PE metadata with `debug/pe`; no inspection subprocess runs.

## Fixture

`internal/testutil/efifixture/testdata/signed-uki.efi` is an independently
constructed PE32+ x86-64 EFI application with synthetic `.linux`, `.osrel`,
`.cmdline`, `.pcrpkey` and `.sbat` sections. `.pcrsig` is deliberately absent.
It is **not a bootable kernel or deployable bootloader**. The same bytes stand
in for different EFI roles solely to test copying; they do not implement shim
or MokManager.

The fixture is Authenticode-signed with a disposable RSA-2048 test key and
self-signed certificate (CN `Fisherman non-production fixture`). Only the public
certificate is included. Never enroll this certificate or deploy the fixture.
The private key is not included or required for tests.

Generated with Python's `struct` module, OpenSSL and Debian sbsigntool 0.9.4.
Cryptographic verification at fixture creation succeeded:

```sh
sbverify --cert internal/testutil/efifixture/testdata/test-cert.pem \
  internal/testutil/efifixture/testdata/signed-uki.efi
# Signature verification OK
```

Ordinary Go tests validate the PE structure and PKCS-signed-data certificate
container, then assert whole-file SHA-256/byte equality. They do not implement
Authenticode verification. Regeneration/replacement requires rerunning the
`sbverify --cert` command and updating these recorded hashes:
d0db0f7cfeb2dfdc4ee9bb90346515a1c1b9a91f07d8c58d8163a2ec18e7f3a5  fisherman/internal/testutil/efifixture/testdata/signed-uki.efi
f78d158008a1c0b0424339e4d981a2b3e925556344431ec5b831ee9dd6b47b25  fisherman/internal/testutil/efifixture/testdata/test-cert.pem

To create a replacement (signature bytes and certificate will change), use a
scratch directory and a new disposable key:

```sh
python3 internal/testutil/efifixture/testdata/make-unsigned.py /tmp/unsigned.efi
openssl req -new -x509 -newkey rsa:2048 -nodes -days 3650 \
  -subj '/CN=Fisherman non-production fixture/' \
  -keyout /tmp/fixture.key -out /tmp/test-cert.pem
sbsign --key /tmp/fixture.key --cert /tmp/test-cert.pem \
  --output /tmp/signed-uki.efi /tmp/unsigned.efi
sbverify --cert /tmp/test-cert.pem /tmp/signed-uki.efi
```

Copy only the verified image and public certificate into testdata, update the
hashes above, and discard the disposable private key. Never commit the key.

## Limits / separate gates

A `.signed` suffix is selection policy, not proof of a trusted signature.
Byte preservation is necessary but does not establish firmware db/dbx trust,
shim/MOK trust, SBAT acceptance, PCR policy, architecture correctness, or
successful boot/update/rollback. Embedded signed UKI command lines are not
rewritten by these tests; external BLS changes do not prove those options will
be honored by a signed UKI under Secure Boot.

Bootc's selection of direct systemd-boot rather than an image-provided shim
chain is separate (see [bootc#2512](https://github.com/bootc-dev/bootc/pull/2512)).
The tests start at fisherman's post-bootc boundary and cannot catch substitution
inside bootc. Full qualification still requires source-image vs installed-ESP
hash/signature/SBAT receipts and boot/update/rollback with documented VM trust.
The x86-64 fixture does not qualify other EFI architectures.
