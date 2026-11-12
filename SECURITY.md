# Security Policy

## Reporting Vulnerabilities

If you discover a security vulnerability in fisherman, please report it
privately:

1. **Do not file a public issue.** Use GitHub's private vulnerability
   reporting on this repository
   ([Security → Report a vulnerability](https://github.com/tuna-os/fisherman/security/advisories/new)).
2. **Provide details:** the affected version or commit, reproduction steps,
   and the impact.
3. **Allow time for a fix** before any public disclosure. Fixes land on `dev`
   and reach users through the normal release flow, and then through each
   frontend's submodule bump.

Reporters who disclose responsibly will be credited in the advisory unless
they ask not to be.

## Supported versions

| Version | Security fixes |
|---|---|
| `dev` branch | yes |
| The latest tagged release | yes |
| Older tags | no: update to the latest release |

Releases are linear tags, and consumers pin a commit and move forward. There
are no backports to older tags.

## Response

Response is best effort. There is no SLA and no fix deadline.

- We aim to acknowledge a private report within 14 days.
- A fix lands on `dev` and ships in the next release. It reaches each
  frontend through that frontend's submodule bump.
- We publish the advisory after the fix release.

## Threat Model

fisherman runs as root, partitions and formats whole block devices, and
creates LUKS volumes. It assumes a **trusted host**, **trusted container
images** and a **trusted caller**.

### What fisherman aims to guarantee

- **Validation before disk changes.** Every rule `fisherman validate` enforces
  runs inside the install itself, before any disk is touched; the install
  refuses an invalid recipe (see [`docs/VALIDATE.md`](docs/VALIDATE.md)). The
  host tools the recipe needs are also checked before partitioning.
- **Encryption as requested.** When LUKS is requested, fisherman formats the
  root volume with the recipe's passphrase, or, for `tpm2-luks`, a random
  recovery passphrase that it emits for the user to save. TPM2 is enrolled on
  the installed system's first boot, not during the install, because PCR 7
  measured in the installer differs from the installed system's.
- **No unlock key outlives its use.** The first-boot enrolment needs a key.
  fisherman never stages the user's passphrase or the recovery key for it. It
  adds a random key in its own LUKS keyslot (slot 31) and stages only that
  key, in the installed deployment's `/etc/fisherman/tpm2-enroll.key` (mode
  `0600`, directory `0700`), inside the encrypted root. The first-boot unit
  (`fisherman-tpm2-enroll.service`, in the deployment's
  `/etc/systemd/system`) tries the enrolment at most three times in that one
  boot. When it stops, it removes the keyslot and then the file. This runs in
  `ExecStopPost=`, so it also runs when the enrolment fails, times out or the
  tool is missing. After that the file's old blocks unlock nothing, on any
  filesystem. There is no retry on a later boot. fisherman looks for
  `systemd-cryptenroll` in the installed image (an ostree deployment), and
  adds no keyslot and stages nothing when the image has none
  (`fisherman/internal/luks/firstboot.go`).
- **Host secrets stay on the host unless the user is on live media.**
  NetworkManager profiles (`/etc/NetworkManager/system-connections`: Wi-Fi
  passwords, 802.1X credentials) and Bluetooth pairings (`/var/lib/bluetooth`:
  link keys) are copied to the target only when the host booted from live
  media (the `live.is_live` of `fisherman probe`), where they are what the
  user set up in the installer session. On any other host fisherman copies
  neither, because the target can be for another person. The recipe fields
  `copyNetworkProfiles` and `copyBluetoothPairings` override the default in
  both directions. The log says for each kind what was copied or why not.
- **Scratch and mount cleanup.** Mounts, LUKS mappings and scratch
  directories fisherman creates are released in dependency order, including
  on the fatal-error path. Scratch that lives on the target disk (live media)
  is deleted while the target is still mounted read-write, before finalize
  and before any unmount, so no image cache stays on the installed disk.

### What fisherman does not protect against

- **An unauthorised caller.** fisherman checks that a recipe is well formed,
  but not who wrote it or whether the caller was entitled to run it. Anyone
  who can run fisherman as root can erase any disk on the host. Authorisation
  is the job of the escalation layer (pkexec policy, sudoers) and of the
  frontend.
- **A compromised host.** If the host, its container runtime or its system
  tools are already compromised, so is the installed system.
- **A malicious image.** The image is installed and then booted; fisherman
  does not inspect its contents. Image authenticity relies on the host's
  containers configuration (registry TLS and any signature policy).
- **Data on a disk that has been installed over.** A failed install may
  already have repartitioned or formatted the target disk; cleanup releases
  mounts and mappings, it does not restore the previous contents.
- **Parallel installs on one host.** They are refused, not isolated. An
  install takes a host-wide lock (`flock` on
  `/run/lock/fisherman-install.lock`) before it reads its recipe, and a
  second install ends with the code `install_already_running`. The kernel
  releases the lock when the process ends, however it ends. Setting
  `targetMount` and `luksMapperName` in the recipes is **not** enough to run
  two installs side by side: both still use the scratch directory
  `/var/fisherman-tmp` and bind it over the host's `/var/tmp`, and each
  teardown removes that directory. The two fields remain for a host where
  the default paths are taken. The lock does not stop another program, or a
  second host that shares the disk, from touching the target.

## Secure Boot

fisherman supports one Secure Boot profile: **image-owned**. The image ships
its own signed boot chain. bootc and bootupd install that chain, and
fisherman keeps it byte for byte. fisherman adds no trust of its own.

### What fisherman guarantees for the image-owned profile

- **It prefers the signed systemd-boot binary.** When bootc leaves no UEFI
  fallback loader on the ESP, fisherman copies systemd-boot from the
  deployment and takes `<name>.signed` before `<name>`
  (`findSystemdBootBinary` in
  [`fisherman/internal/install/systemdboot.go`](fisherman/internal/install/systemdboot.go);
  test `TestFindSystemdBootBinary_PrefersSigned` in
  [`systemdboot_test.go`](fisherman/internal/install/systemdboot_test.go)).
- **It keeps an existing fallback.** When `EFI/BOOT/BOOTX64.EFI` (or
  `BOOTAA64.EFI` on arm64) is already on the ESP, `InstallSystemdBoot` and
  `InstallGrubFallback`
  ([`grub.go`](fisherman/internal/install/grub.go)) change nothing (tests
  `TestInstallSystemdBoot_NoopWhenFallbackExists`,
  `TestInstallSystemdBoot_Arm64NoopWhenFallbackExists` and
  `TestFallbackHelpersLeaveExistingSignedChainUntouched`).
- **It keeps every signed byte.** Each copy of a signed EFI binary or UKI is
  identical to its source, certificate table included, and the BLS changes
  for Plymouth and LUKS do not rewrite a signed UKI. The GRUB fallback copies
  the shim, the second stage and MokManager that are already on the ESP; it
  does not take them from image metadata. Tests:
  [`efi_preservation_test.go`](fisherman/internal/install/efi_preservation_test.go),
  described in [`docs/EFI-PRESERVATION.md`](docs/EFI-PRESERVATION.md).
- **It refuses an image for another architecture before the disk is
  touched** (`image_platform_mismatch`,
  [`docs/VALIDATE.md`](docs/VALIDATE.md)). The systemd-boot and fallback
  file names follow the host architecture: `x64` on x86-64, `aa64` on arm64
  (`systemdBootNames`; test `TestInstallSystemdBoot_ArchFileNames`).

### What fisherman does not do

- It does not stage shim or MokManager from image metadata.
- It does not enrol or import MOK keys.
- It does not change PK, KEK, db or dbx, or any other firmware setting.
- It does not read or act on secure-install metadata that an image declares.

The limits in [`docs/EFI-PRESERVATION.md`](docs/EFI-PRESERVATION.md#limits--separate-gates)
apply: a `.signed` name is a selection rule, not proof of a trusted
signature, and the byte tests use an x86-64 fixture only.

### Add a profile

A new Secure Boot profile needs three things before fisherman supports it:

1. a versioned capability in `fisherman probe` ([`docs/PROBE.md`](docs/PROBE.md#capabilities)),
2. fixtures and tests for it, and
3. a VM run with Secure Boot enforced.

## Security-Relevant Invariants

These behaviours have security implications and must not be simplified or
moved.

### Scratch directory

- On a normal host the scratch directory is `/var/fisherman-tmp`
  (`fisherman/cmd/fisherman/main.go`), created mode `0700`. Do not move it
  under `/run/*`: that is tmpfs and too small for image blobs.
- On live media whose `/var` is tmpfs or overlayfs, scratch lives on the
  target disk at `<target mount>/.fisherman-scratch` instead. It is
  self-bind-mounted, and its removal is registered as a post-removal so it
  runs after the unmount and LUKS close, and still runs on the error path.

### SELinux bypass shim

`BuildSelinuxBypassShim` (`fisherman/internal/install/bootc.go`) compiles a
small shared library as root, and that library is then `LD_PRELOAD`ed into the
privileged bootc container. It must be built in a fresh directory created by
`os.MkdirTemp` (mode `0700`, unpredictable name), never at a fixed path in a
world-writable directory such as `/tmp`. A fixed path would let an
unprivileged local user pre-plant the library for root to preload, or symlink
the source path at an arbitrary file for root to truncate (fixed in #161).

The shim is built only when the install needs it: the target disables
SELinux (`selinuxDisabled`), the host has SELinux active, and bootc runs in
the image's container (`install.SelinuxWorkaroundRequired`). It is built
before the first disk step, and an install that cannot build it is refused
with the disk untouched (`selinux_workaround_unavailable`, #370). Its
directory is removed when the install ends, on success, failure and cancel.
The workaround never changes what the recipe asks for: the target keeps
SELinux disabled, and the host's SELinux and the signature policy stay as
they are.

Limits: the shim replaces the C library's `lsetxattr` and `fsetxattr`, built
against the host's C library and preloaded by the image's dynamic loader. A
program in the image that sets the label with a direct system call, is
linked statically, or uses another C library (musl) is not covered, and an
image loader that cannot load a library built on this host ignores it. The
tests check the interposition on the host's own loader only; a failure from
a direct system call has not been reproduced.

### Partition layout

- **GRUB images always use 3 partitions** (EFI, ext4 `/boot`, root), even
  unencrypted. GRUB cannot read modern XFS features, so `/boot` is ext4 on its
  own partition. This is a boot requirement, not an optimisation.
- **systemd-boot images (dakota) use 2 partitions** (EFI, root), with the
  kernel and initrd on the ESP.
- **The ESP is 2 GiB across the whole fleet.**

See the partition-layout table in the [README](README.md).

### Recipe handling

- **Validation.** `fisherman validate [--json]` runs the same rules as the
  install, and frontends should call it to show errors early. The stable error
  codes are listed in [`docs/VALIDATE.md`](docs/VALIDATE.md).
- **Empty image.** An empty `image` is rejected unless the host booted from
  live media (`fisherman probe --json` reports this as `live.is_live`), where
  it means "install the running image". This keeps a mistake from silently
  installing the host's own image.
- **Recipes on stdin.** `fisherman -` reads the recipe from stdin (bounded to
  1 MiB), so a frontend never has to write a recipe containing a LUKS
  passphrase or user password to a temporary file.

## Post-Install Verification

After fisherman exits 0, integrators can check:

1. **The partition layout** matches the image's boot stack (3 partitions for
   GRUB, 2 for systemd-boot):
   ```bash
   lsblk /dev/X
   ```
2. **For encrypted installs**, that the unlock flow works on first boot.
3. **The deployed image**, once booted:
   ```bash
   bootc status
   ```

## Privilege Boundaries

fisherman is started as root by its frontend:

| Path | Escalation | Recipe | Notes |
|---|---|---|---|
| `sudo fisherman …` | sudo | file or stdin (`-`) | CLI and test use |
| `pkexec fisherman …` | polkit | file or stdin (`-`) | needs a polkit policy that allows it |
| `flatpak-spawn --host pkexec fisherman …` | polkit, from a Flatpak frontend | stdin (`-`) or a host-visible path | the sandbox cannot see the host, so recipes and fisherman are staged where root on the host can read them |

fisherman does not authenticate the frontend or its recipe. The frontend is
responsible for collecting input, confirming the target disk and image with
the user, and only then starting fisherman.

## Dependencies and Supply Chain

- **Go:** the `fisherman` module uses only the Go standard library
  (`fisherman/go.mod` has no requirements); CI runs `go vet`, the unit tests
  and golangci-lint. The separate `tui/` module pins its dependencies in
  `tui/go.sum`.
- **bootc** runs from the target image via `podman run --privileged` when an
  image is given; on live media it is called directly from the running image.
- **podman** and **skopeo** handle image pulls and offline image copies, and
  inherit the host's containers configuration.
- **System utilities** (`sfdisk`, `mkfs.*`, `cryptsetup`, `mount`, …) are
  taken from the host.

All of these are assumed to come from a trusted source. If any is
compromised, so is the resulting install.
