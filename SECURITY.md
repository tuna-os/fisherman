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
  measured in the installer differs from the installed system's. Until then
  the transient key sits in `/etc/fisherman/tpm2-enroll.key` (mode `0600`,
  directory `0700`) inside the encrypted root, and the first-boot unit shreds
  it after enrolment (`fisherman/internal/luks/luks.go`).
- **Scratch and mount cleanup.** Mounts, LUKS mappings and scratch
  directories fisherman creates are released in dependency order, including
  on the fatal-error path.

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
- **Parallel installs with default paths.** The target mount and LUKS mapper
  name are shared defaults. Two installs on the same host must set
  `targetMount` and `luksMapperName` in their recipes to avoid colliding.

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
