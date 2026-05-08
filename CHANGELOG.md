# Fisherman Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
