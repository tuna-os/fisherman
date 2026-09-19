# Fisherman Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### 🔒 Security

- **Transient TPM2 enrolment key is pinned to 0600** (#221): `StageFirstBootEnrollment`
  wrote the plaintext LUKS unlock key with `os.WriteFile(…, 0o600)`, whose mode
  applies only to a file it creates and is masked by the process umask. An explicit
  `os.Chmod` now guarantees root-only access in the window before the first-boot
  oneshot shreds the key.

### 🐛 Bug Fixes

- **Root partition carries the DPS type from creation** (#219): sealed composefs
  images boot from a UKI with no `root=` on its command line, so
  `systemd-gpt-auto-generator` has to discover the root by GPT type.
  `PartitionSystemdBoot` typed it generically and only unencrypted installs were
  retagged afterwards, so encrypted sealed-composefs installs hung ~90s on
  `/dev/gpt-auto-root` and dropped to an emergency shell. The type is now set when
  the table is written, which covers the encrypted layout the post-install retag
  cannot reach, and it is architecture-aware (x86-64 and aarch64).
- **ENOSPC installing a large image from a live ISO** (#211): in direct mode
  (no podman wrapper) `bootc` staged layer blobs under the RAM-backed `/var/tmp`
  of the dracut overlay while the OCI cache itself sat on the target disk, so a
  ~10 GiB image died part way through "Copying blob". Direct mode now gets the
  same scratch-backed `/var/tmp` and `TMPDIR` the OCI export already used.
- **TUI started the backend with a command that does not exist** (#178): it ran
  `fisherman install --recipe <path>`, but the backend takes the recipe as a bare
  positional argument, so it tried to load a recipe named "install" and every
  non-dry-run install failed before touching the disk. The argv is now built by
  one function covered by a cross-module contract test against a fake backend.
- **Unknown subcommands are named** (#178): `fisherman install …` fell through to
  `recipe.Load("install")` and reported a missing file, pointing at the wrong
  thing. A flag, or a bare word that is not a file, now reports an unknown command
  and prints help; the bare recipe-path form is untouched.

### 🧪 CI

- **`tui/` runs in CI** (#204): the module had real tests and no workflow ever
  executed them — every job scoped to `fisherman/`. `bootcrew-vm.yml` gains a
  `tui-unit-tests` job running `go vet` and `go test -race` with coverage upload.

- **OCI layout for non-composefs installs**: Non-composefs images (bluefin, lts,
  lts-hwe) now export to an OCI layout at scratch and use `--source-imgref oci:...`
  for `bootc install to-filesystem`. The previous VFS squash path corrupted ostree
  commit structure, producing “Expected commit object, not File”. The overlay
  redirect eliminates ENOSPC on 8+ GiB images by placing working container layers
  on the target disk instead of the 1.4 GiB live overlayfs.
- **`--source-imgref` in direct mode**: Direct mode now emits `--source-imgref`
  so `bootc install to-filesystem` does not fail with “must be executed inside
  a podman container” on offline live-ISO installs.

### 📊 Partition Layout

- **2 GiB /boot**: grub2 `/boot` ext4 partition increased from 1 GiB → 2 GiB.
  Each kernel+initramfs pair (200–400 MiB with NVIDIA drivers) fills 1 GiB quickly
  once deployment + rollback + staged upgrade accumulate.
- **2 GiB ESP for all fleet images**: grub2 EFI System Partition increased from
  512 MiB → 2 GiB for fleet consistency with systemd-boot (dakota) images.

---

## [0.2.0] - 2026-05-08

### 🚀 Major Features

#### Storage & Filesystem Support
- **Composefs Overlay Storage**: Full support for overlay storage driver with composefs installations
- **ext4 Root Filesystem**: Added ext4 with verity support for composefs backend
- **Improved Partition Layout**: Auto-detect and optimize storage driver selection

#### systemd-boot & EFI Improvements
- **EFI Fallback Installation**: Robust systemd-boot EFI binary installation with fallback paths
- **Dynamic BOOTX64.EFI Handling**: Search multiple ostree deployment paths for systemd-boot binary
- **GPT Retagging Fixes**: Proper EFI remount handling during partition retagging

#### Release Automation
- **GoReleaser Integration**: Added `.goreleaser.yml` for automated binary releases
- **Release Workflows**: New `release-cut.yml` and `release-publish.yml` workflows
- **Dependabot Updates**: Added `.github/dependabot.yml` for automated dependency checks
- **GolangCI Linting**: Added `.golangci.yml` for consistent code quality

### 🐛 Bug Fixes

#### SSH & Boot Verification
- Enable systemd-networkd DHCP in SSH test images
- Add SSH troubleshooting to boot-verify script
- Improve SSH enablement in Containerfile for debian-bootc

#### Composefs & Storage
- Fix systemd.wants=ssh.service on composefs boot parameters
- Redirect podman container storage to scratch for composefs installs
- Improve overlayfs /var scratch directory detection
- Enable overlay storage driver on btrfs (previously disabled)

#### QEMU & Testing
- Add explicit QEMU boot order for composefs VM testing
- Revert ineffective QEMU boot order and systemd-boot timeout fixes
- Improve boot configuration detection and patching logic

### 📊 Test Coverage
- Add loopback device storage driver tests
- Add LUKS unlocking tests with Python helper script
- Add post-install verification tests
- Expand SSH image building for all VM boot test images

### 🔨 Improvements
- Refactor test matrix with Dakota and improved boot verification
- Enhance script discovery for CI/local portability
- Add `scripts/luks-unlock.py` for LUKS testing automation
- Improve `verify-installation.sh` with comprehensive status checks

---

## [0.1.0] - 2026-03-XX

### Initial Release
- First stable release of fisherman bootc installer backend
- Support for bootc install with custom partitioning
- LUKS encryption support
- Composefs with systemd-boot support
- Image catalog and customization
- flatpak installation support
- Post-install hostname and user creation
